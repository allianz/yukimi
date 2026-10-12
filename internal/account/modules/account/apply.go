/*
Copyright 2026 The Yukimi Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package account

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/account/pipeline"
	"github.com/allianz/yukimi/internal/account/tenant"
	"github.com/allianz/yukimi/internal/errors"
	"github.com/allianz/yukimi/internal/secrets"
	"github.com/allianz/yukimi/internal/snowflake/statement"
)

// duplicateAccountSQLState is the ANSI "duplicate object" SQLSTATE Snowflake
// reports for an "Object ... already exists" compilation error — the signal
// used to tell an org-wide account-name collision (a tenant-fixable mistake)
// apart from every other CREATE ACCOUNT failure (a system error).
const duplicateAccountSQLState = "42710"

// maxAccountLabelLen is Snowflake's DNS label limit on "<org>-<resolvedName>",
// (specs/012-account-module.md, Key Concept: Account Name Length Limit).
const maxAccountLabelLen = 63

// resumeClockSkewBuffer absorbs drift between the kube-apiserver's clock
// (cr.CreationTimestamp) and the secrets backend's clock (a credential's
// RotatedAt) when deciding whether an occupied identifier is this resource's
// own crashed attempt or an unrelated orphan — precision doesn't matter
// here, only ordering (specs/012-account-module.md, Key Concept: Resuming a
// Crashed Create).
const resumeClockSkewBuffer = 5 * time.Minute

// platformUser is the service user CREATE ACCOUNT bootstraps as the account's
// admin. The same name is stored with its credential, so the pool logs in as
// it, and the email sync looks it up and alters it — every use must agree.
const platformUser = "platform"

// Apply re-asserts the account module's desired state: create the account on
// the first reconcile, or re-confirm the platform can still reach it — and
// keep the platform user's EMAIL in sync with spec.Contact — on every later
// one. It never repeats a create once a locator is known — see Key Concept:
// Create-Then-Verify Lifecycle, specs/012-account-module.md.
func (m *module) Apply(ctx context.Context, data *pipeline.ResourceData) pipeline.Outcome {
	out := m.apply(ctx, data)
	m.syncStatus(data) // after apply: a fresh create has just set the locator
	// Nothing after this module can run without a live account, so every
	// result but Done stops the pipeline (Abort is a no-op on Done).
	return out.Abort()
}

func (m *module) apply(ctx context.Context, data *pipeline.ResourceData) pipeline.Outcome {
	if data.CR().Status.AccountLocator != "" {
		return m.reconcileExisting(ctx, data)
	}
	return m.createAccount(ctx, data)
}

// reconcileExisting re-confirms the platform can reach an account whose
// locator is already known, then keeps the platform user's EMAIL in sync with
// spec.Contact. Within the post-create grace period it attempts no
// connection at all (Key Concept: Post-Create Grace Period).
func (m *module) reconcileExisting(ctx context.Context, data *pipeline.ResourceData) pipeline.Outcome {
	cr := data.CR()
	if withinGracePeriod(cr, m.gracePeriod) {
		return pipeline.Pending(provisioningMessage)
	}
	db, err := connectTenant(ctx, data)
	if err != nil {
		return pipeline.Failed(err)
	}
	if err := syncPlatformEmail(ctx, statement.New(db), cr.Spec.Contact); err != nil {
		return pipeline.Failed(err)
	}
	return pipeline.Done()
}

// syncPlatformEmail reasserts contact as the platform user's EMAIL only when
// a live read-back shows it has drifted, so a healthy reconcile issues no
// write and leaves no ALTER USER in Snowflake's query history. The platform
// user is exclusively controlled by this module, so there is nothing to
// detect here beyond "does Snowflake already have what the CRD says" — no
// state is persisted to status for this purpose.
func syncPlatformEmail(ctx context.Context, runner *statement.Runner, contact string) error {
	result, err := runner.Query(ctx, "show platform user", "SHOW USERS LIKE ?", platformUser)
	if err != nil {
		return fmt.Errorf("failed to look up platform user: %w", err)
	}
	// A missing platform row or email yields "", never matching a real
	// contact, so either case is treated as drift and ALTER USER runs rather
	// than silently skipping.
	if email, _ := result.FindRow("name", platformUser).StringValue("email"); email == contact {
		return nil
	}

	if err := runner.Exec(ctx, "sync platform user email",
		"ALTER USER IDENTIFIER(?) SET EMAIL = ?", platformUser, contact); err != nil {
		return fmt.Errorf("failed to update platform user email: %w", err)
	}
	return nil
}

// createAccount runs the fresh-create path: check the request is creatable,
// obtain the platform keypair (freshly stored, or recovered from this
// resource's own crashed attempt), issue CREATE ACCOUNT over the org-admin
// connection, then record the resulting locator and creation time directly
// on the CRD's status. It never runs when a locator is already known, and it
// never verifies reachability itself — that is deferred to a later
// reconcile, once the grace period has elapsed (Key Concept: Create-Then-Verify
// Lifecycle).
func (m *module) createAccount(ctx context.Context, data *pipeline.ResourceData) pipeline.Outcome {
	cr := data.CR()
	resolvedName := data.ResolvedAccountName()

	if err := m.checkCreatable(cr, resolvedName, data.NamespaceLabels()); err != nil {
		return pipeline.Failed(err)
	}

	creds, resumed, err := m.acquireCredentials(ctx, cr)
	if err != nil {
		return pipeline.Failed(err)
	}

	orgAdminDB, err := data.OrgAdminDB(ctx)
	if err != nil {
		return pipeline.Failed(err)
	}
	runner := statement.New(orgAdminDB)

	// Only a resumed attempt can have crashed after CREATE ACCOUNT already
	// succeeded; reissuing it would surface as a name collision with the
	// tenant's own account (Key Concept: Resuming a Crashed Create).
	if resumed {
		locator, found, err := findAccountLocator(ctx, runner, "check for existing account before resuming create", resolvedName)
		if err != nil {
			return pipeline.Failed(err)
		}
		if found {
			return finishCreate(cr, locator)
		}
	}

	if err := execCreateAccount(ctx, runner, resolvedName, cr.Spec, creds.PublicKey); err != nil {
		return pipeline.Failed(err)
	}
	locator, err := locateCreatedAccount(ctx, runner, resolvedName)
	if err != nil {
		return pipeline.Failed(err)
	}
	return finishCreate(cr, locator)
}

// checkCreatable runs every fresh-create check that must pass before any
// side effect — keypair generation, secret storage, or the org-admin
// connection: the region exists in the Backplane Config (007) and, unless
// the tenant is an alpha tester, is available (Key Concept: Region
// Validation), and cr.Name fits this organization's real account-name limit
// (Key Concept: Account Name Length Limit).
func (m *module) checkCreatable(cr *v1alpha1.SnowflakeAccount, resolvedName string, namespaceLabels map[string]string) error {
	region, err := m.backplane.Region(cr.Spec.Region)
	if err != nil {
		return err
	}

	isAlphaTester, err := tenant.AlphaTester(namespaceLabels)
	if err != nil {
		return err
	}
	if !region.Available && !isAlphaTester {
		return errors.NewUserError(fmt.Sprintf(
			"region '%s' is not yet available; choose a different one", cr.Spec.Region))
	}

	// Snowflake caps "<org>-<resolvedName>" at maxAccountLabelLen, not
	// cr.Name alone, so maxNameLen converts that into a plain length the
	// tenant can act on. resolvedName is cr.Name plus a fixed-length suffix
	// (006), so the difference is that suffix.
	suffixLen := len(resolvedName) - len(cr.Name)
	if maxNameLen := maxAccountLabelLen - 1 - len(m.org) - suffixLen; len(cr.Name) > maxNameLen {
		return errors.NewUserError(fmt.Sprintf(
			"account name must be %d characters or fewer", maxNameLen))
	}
	return nil
}

// acquireCredentials stores a freshly generated platform keypair at cr's
// tenant secret identifier, create-only. If the identifier is already
// occupied, it reports resumed == true and returns the occupying credential
// when that is this resource's own crashed attempt (see resumeCrashedCreate);
// an identifier still inside its deletion recovery window is a user error
// instead.
func (m *module) acquireCredentials(ctx context.Context, cr *v1alpha1.SnowflakeAccount) (creds *secrets.Credentials, resumed bool, err error) {
	id, err := secrets.NewTenantIdentifier(m.org, cr.Namespace, cr.Name)
	if err != nil {
		return nil, false, err
	}

	creds, err = m.keyManager.CreateCredentials(ctx, id, platformUser)
	if err == nil {
		return creds, false, nil
	}
	if errors.Is(err, secrets.ErrPendingDeletion) {
		return nil, false, errors.NewUserError(fmt.Sprintf(
			"account %q was deleted recently and is still within its deletion recovery "+
				"window; wait for the recovery window to elapse, then try again", cr.Name))
	}

	creds, err = m.resumeCrashedCreate(ctx, id, cr.CreationTimestamp.Time, err)
	if err != nil {
		return nil, false, err
	}
	return creds, true, nil
}

// resumeCrashedCreate is called once CreateCredentials has failed to create a
// fresh credential at id for any reason other than ErrPendingDeletion. id is
// unique to this resource (003's collision-free join), so nothing but this
// module's own CreateCredentials calls for this exact resource ever write to
// it: a secret found there, written at or after createdAt (minus a
// clock-skew buffer), can only be an earlier attempt by this same resource
// that crashed before CREATE ACCOUNT ran or before its locator was
// persisted — safe to reuse outright, since nothing in Snowflake has used it
// yet. A secret written earlier than that predates this resource and is
// refused rather than guessed at. The secrets taxonomy offers no "already
// exists" error, so createErr may not mean the identifier is occupied at
// all; if nothing can be read back, there is nothing to resume and createErr
// is what gets reported (specs/012-account-module.md, Key Concept: Resuming
// a Crashed Create).
func (m *module) resumeCrashedCreate(ctx context.Context, id secrets.Identifier, createdAt time.Time, createErr error) (*secrets.Credentials, error) {
	creds, err := m.keyManager.GetCredentials(ctx, id)
	if err != nil {
		return nil, fmt.Errorf(
			"failed to create platform credentials: %w (and no earlier attempt could be read back to resume from: %w)",
			createErr, err)
	}
	if creds.RotatedAt.Before(createdAt.Add(-resumeClockSkewBuffer)) {
		return nil, fmt.Errorf(
			"failed to create platform credentials: identifier is occupied by a secret written at %s, before this resource's own creation at %s (minus a %s clock-skew buffer) — this cannot be this resource's own crashed attempt and requires manual investigation: %w",
			creds.RotatedAt, createdAt, resumeClockSkewBuffer, createErr)
	}
	return creds, nil
}

// finishCreate records locator and the current time directly on cr's status
// and returns the Pending(...) outcome every successful create path shares,
// whether the account was just created by this call or found already
// existing by the resume pre-check. On the latter path the account is in
// fact somewhat older than now, which only lengthens the grace period — the
// safe direction (specs/012-account-module.md, Key Concept: Resuming a
// Crashed Create).
func finishCreate(cr *v1alpha1.SnowflakeAccount, locator string) pipeline.Outcome {
	cr.Status.AccountLocator = locator
	cr.Status.AccountCreatedAt = &metav1.Time{Time: time.Now()}
	return pipeline.Pending("account created; waiting for it to become reachable before continuing")
}

// execCreateAccount renders and executes CREATE ACCOUNT over runner. A
// collision with an existing account name org-wide is a user error; every
// other failure is a system error.
func execCreateAccount(ctx context.Context, runner *statement.Runner, resolvedName string, spec v1alpha1.SnowflakeAccountSpec, publicKey string) error {
	// Every position binds (specs/005, Verified Bind Positions): the account
	// name via IDENTIFIER(?), everything else with a plain ?.
	sql := "CREATE ACCOUNT IDENTIFIER(?) ADMIN_NAME=? ADMIN_RSA_PUBLIC_KEY=? ADMIN_USER_TYPE=SERVICE EMAIL=? EDITION=ENTERPRISE REGION=?"
	args := []any{
		resolvedName,
		platformUser,
		publicKey,
		spec.Contact,
		snowflakeRegionID(spec.Region),
	}
	if spec.Description != "" {
		sql += " COMMENT=?"
		args = append(args, spec.Description)
	}

	if err := runner.Exec(ctx, "create account", sql, args...); err != nil {
		var stmtErr *statement.Error
		if errors.As(err, &stmtErr) && stmtErr.SQLState == duplicateAccountSQLState {
			return errors.NewUserError(fmt.Sprintf(
				"account name '%s' is already in use by another account in the organization; rename this resource and try again", resolvedName))
		}
		return fmt.Errorf("failed to create account: %w", err)
	}
	return nil
}

// snowflakeRegionID converts a CRD region (aws-eu-central-1) into the region
// identifier CREATE ACCOUNT's REGION expects (AWS_EU_CENTRAL_1).
func snowflakeRegionID(region string) string {
	return strings.ToUpper(strings.ReplaceAll(region, "-", "_"))
}

// findAccountLocator runs SHOW ACCOUNTS LIKE against resolvedName and
// returns the exact, case-insensitive matching row's locator, if any — the
// row-matching logic shared by locateCreatedAccount (post-create, where "not
// found" is an error) and the resume pre-check in createAccount (pre-create,
// where "not found" is the normal, expected signal to proceed to CREATE
// ACCOUNT). The LIKE pattern is a coarse pre-filter only — every underscore
// in resolvedName is a wildcard to LIKE, so a row LIKE matched but is not an
// exact match is discarded before trusting its locator
// (specs/012-account-module.md, Edge Cases).
func findAccountLocator(ctx context.Context, runner *statement.Runner, label, resolvedName string) (locator string, found bool, err error) {
	result, err := runner.Query(ctx, label, "SHOW ACCOUNTS LIKE ?", resolvedName)
	if err != nil {
		return "", false, fmt.Errorf("failed to look up account %q: %w", resolvedName, err)
	}

	locator, ok := result.FindRow("account_name", resolvedName).StringValue("account_locator")
	return locator, ok && locator != "", nil
}

// locateCreatedAccount is findAccountLocator with "not found" treated as an
// error — the post-create expectation that CREATE ACCOUNT having just
// succeeded means SHOW ACCOUNTS must now find it.
func locateCreatedAccount(ctx context.Context, runner *statement.Runner, resolvedName string) (string, error) {
	locator, found, err := findAccountLocator(ctx, runner, "locate created account", resolvedName)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("CREATE ACCOUNT succeeded but no account named %q was found by SHOW ACCOUNTS", resolvedName)
	}
	return locator, nil
}
