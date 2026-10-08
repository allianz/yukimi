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

// duplicateAccountSQLState is the SQLSTATE this codebase's own statement
// package tests use to illustrate a Snowflake "already exists" failure (see
// statement/statement_test.go, TestRunnerExecFailureSnowflakeError) — the
// signal used to tell an org-wide account-name collision (a tenant-fixable
// mistake) apart from every other CREATE ACCOUNT failure (a system error).
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

// resolvedNameSuffixLen is the length of the "_" plus 5-character hash
// tenant.ResolveName always appends, so len(cr.Name) == len(resolvedName) -
// resolvedNameSuffixLen (ResolveName only ever substitutes "-" for "_"
// otherwise).
const resolvedNameSuffixLen = 6

// hasRepeatedSeparator reports whether s contains a run of 2+ '-'/'_'
// characters. The secret identifier (003) joins org/namespace/accountName
// with "--", mapping '_' to '-' first — a segment containing such a run
// would be indistinguishable from that join, which could make two different
// tenants collide onto the same secret identifier. org comes from base.yaml
// (002), not the CRD, so it can't be validated via kubebuilder; namespace is
// cluster metadata, not part of this CRD's own spec, so it can't be either —
// both are checked here instead, ahead of secrets.NewTenantIdentifier.
func hasRepeatedSeparator(s string) bool {
	for i := 1; i < len(s); i++ {
		if (s[i] == '-' || s[i] == '_') && (s[i-1] == '-' || s[i-1] == '_') {
			return true
		}
	}
	return false
}

// withinGracePeriod reports whether cr's account was created recently enough
// that a connection attempt should be skipped rather than tried and left to
// fail — Snowflake accounts take minutes to become reachable after CREATE
// ACCOUNT returns. A nil AccountCreatedAt (an account that already existed
// before this field was introduced) is treated as "the grace period has
// already elapsed", so nothing changes for pre-existing accounts.
func withinGracePeriod(cr *v1alpha1.SnowflakeAccount, gracePeriod time.Duration) bool {
	createdAt := cr.Status.AccountCreatedAt
	return createdAt != nil && time.Since(createdAt.Time) < gracePeriod
}

// Apply re-asserts the account module's desired state: create the account on
// the first reconcile, or re-confirm the platform can still reach it — and
// keep the platform user's EMAIL in sync with spec.Contact — on every later
// one. It never repeats a create once a locator is known — see Key Concept:
// Create-Then-Verify Lifecycle, specs/012-account-module.md.
func (m *module) Apply(ctx context.Context, mc *pipeline.ModuleContext) pipeline.Outcome {
	cr := mc.CR()
	if cr.Status.AccountLocator != "" {
		if withinGracePeriod(cr, m.gracePeriod) {
			return pipeline.Pending("waiting for the account to finish provisioning before attempting to connect").Aborting()
		}
		db, err := mc.TenantDB(ctx)
		if err != nil {
			return pipeline.Failed(fmt.Errorf(
				"platform connection failed for existing account locator %s: %w", cr.Status.AccountLocator, err)).Aborting()
		}
		if err := syncPlatformEmail(ctx, statement.New(db), cr.Spec.Contact); err != nil {
			return pipeline.Failed(err).Aborting()
		}
		return pipeline.Done()
	}

	return m.createAccount(ctx, mc)
}

// syncPlatformEmail reasserts contact as the platform user's EMAIL only when
// a live read-back shows it has drifted, so a healthy reconcile issues no
// write and leaves no ALTER USER in Snowflake's query history. The platform
// user is exclusively controlled by this module, so there is nothing to
// detect here beyond "does Snowflake already have what the CRD says" — no
// state is persisted to status for this purpose.
func syncPlatformEmail(ctx context.Context, runner *statement.Runner, contact string) error {
	result, err := runner.Query(ctx, "show platform user", "SHOW USERS LIKE ?", "platform")
	if err != nil {
		return fmt.Errorf("failed to look up platform user: %w", err)
	}
	// A missing platform row or email yields "", never matching a real
	// contact, so either case is treated as drift and ALTER USER runs rather
	// than silently skipping.
	if email, _ := result.FindRow("name", "platform").StringValue("email"); email == contact {
		return nil
	}

	if err := runner.Exec(ctx, "sync platform user email",
		"ALTER USER IDENTIFIER(?) SET EMAIL = ?", "platform", contact); err != nil {
		return fmt.Errorf("failed to update platform user email: %w", err)
	}
	return nil
}

// createAccount runs the fresh-create path: confirm the region exists in the
// Backplane Config (007) and, unless the tenant is an alpha tester, that it
// is available (Key Concept: Alpha-Tester Region Bypass), generate and store
// the platform keypair create-only, issue CREATE ACCOUNT over the org-admin
// connection, then record the resulting locator and creation time directly
// on the CRD's status. It never runs when a locator is already known, and it
// never verifies reachability itself — that is deferred to a later
// reconcile, once the grace period has elapsed (Key Concept: Create-Then-Verify
// Lifecycle).
func (m *module) createAccount(ctx context.Context, mc *pipeline.ModuleContext) pipeline.Outcome {
	cr := mc.CR()

	region, err := m.backplane.Region(cr.Spec.Region)
	if err != nil {
		return pipeline.Rejected(err).Aborting()
	}

	isAlphaTester, err := tenant.AlphaTester(mc.NamespaceLabels())
	if err != nil {
		return pipeline.Failed(err).Aborting()
	}
	if !region.Available && !isAlphaTester {
		return pipeline.Rejected(errors.NewUserError(fmt.Sprintf(
			"region '%s' is not yet available; choose a different one", cr.Spec.Region))).Aborting()
	}

	resolvedName := mc.ResolvedAccountName()

	// Snowflake caps "<org>-<resolvedName>" at maxAccountLabelLen, not
	// cr.Name alone, so maxNameLen converts that into a plain length the
	// tenant can act on (specs/012-account-module.md, Key Concept: Account
	// Name Length Limit).
	if maxNameLen := maxAccountLabelLen - 1 - resolvedNameSuffixLen - len(m.org); len(cr.Name) > maxNameLen {
		return pipeline.Rejected(errors.NewUserError(fmt.Sprintf(
			"account name must be %d characters or fewer", maxNameLen))).Aborting()
	}

	if hasRepeatedSeparator(m.org) {
		return pipeline.Rejected(errors.NewUserError(
			"the configured Snowflake organization name contains a repeated '-' or '_'; fix snowflake.org in base.yaml")).Aborting()
	}
	if hasRepeatedSeparator(cr.Namespace) {
		return pipeline.Rejected(errors.NewUserError(fmt.Sprintf(
			"namespace %q contains a repeated '-' or '_', which is not allowed for a SnowflakeAccount's namespace",
			cr.Namespace))).Aborting()
	}

	id, err := secrets.NewTenantIdentifier(m.org, cr.Namespace, cr.Name)
	if err != nil {
		return pipeline.Failed(err).Aborting()
	}

	creds, err := m.keyManager.CreateCredentials(ctx, id, "platform")
	resumed := false
	if err != nil {
		if errors.Is(err, secrets.ErrPendingDeletion) {
			return pipeline.Rejected(errors.NewUserError(fmt.Sprintf(
				"account %q was deleted recently and is still within its deletion recovery "+
					"window; wait for the recovery window to elapse, then try again", cr.Name))).Aborting()
		}

		resumedCreds, resumeErr := m.resumeCrashedCreate(ctx, id, cr.CreationTimestamp.Time, err)
		if resumeErr != nil {
			return pipeline.Failed(resumeErr).Aborting()
		}
		creds = resumedCreds
		resumed = true
	}

	orgAdminDB, err := mc.OrgAdminDB(ctx)
	if err != nil {
		return pipeline.Failed(err).Aborting()
	}
	runner := statement.New(orgAdminDB)

	if resumed {
		locator, found, err := findAccountLocator(ctx, runner, "check for existing account before resuming create", resolvedName)
		if err != nil {
			return pipeline.Failed(err).Aborting()
		}
		if found {
			return finishCreate(cr, locator)
		}
	}

	locator, outcome := runCreateAccount(ctx, runner, resolvedName, cr.Spec.Region, cr.Spec.Contact, cr.Spec.Description, creds.PublicKey)
	if outcome.State != pipeline.StateDone {
		return outcome
	}
	return finishCreate(cr, locator)
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
// refused rather than guessed at. If the occupying secret cannot even be
// read, resume is not attempted and createErr is what gets reported (specs/012-account-module.md,
// Key Concept: Resuming a Crashed Create).
func (m *module) resumeCrashedCreate(ctx context.Context, id secrets.Identifier, createdAt time.Time, createErr error) (*secrets.Credentials, error) {
	creds, err := m.keyManager.GetCredentials(ctx, id)
	if err != nil {
		return nil, fmt.Errorf(
			"failed to create platform credentials: %w (and the occupying secret could not be read to check whether this is a resumable crashed attempt: %w)",
			createErr, err)
	}
	if creds.RotatedAt.Before(createdAt.Add(-resumeClockSkewBuffer)) {
		return nil, fmt.Errorf(
			"failed to create platform credentials: identifier is occupied by a secret written at %s, before this resource's own creation at %s (minus a %s clock-skew buffer) — this cannot be this resource's own crashed attempt and requires manual investigation: %w",
			creds.RotatedAt, createdAt, resumeClockSkewBuffer, createErr)
	}
	return creds, nil
}

// finishCreate records locator and the current time directly on cr's
// status — the only two status fields this module ever sets — and returns
// the Pending(...).Aborting() outcome every successful create path shares,
// whether the account was just created by this call or found already
// existing by the resume pre-check (specs/012-account-module.md, Key
// Concept: Resuming a Crashed Create).
func finishCreate(cr *v1alpha1.SnowflakeAccount, locator string) pipeline.Outcome {
	cr.Status.AccountLocator = locator
	cr.Status.AccountCreatedAt = &metav1.Time{Time: time.Now()}
	return pipeline.Pending("account created; waiting for it to become reachable before continuing").Aborting()
}

// runCreateAccount renders and executes CREATE ACCOUNT over runner, then
// looks up the locator Snowflake assigned. It is pure with respect to
// ModuleContext — testable with a sqlmock-backed *statement.Runner alone.
func runCreateAccount(ctx context.Context, runner *statement.Runner, resolvedName, region, email, description, publicKey string) (string, pipeline.Outcome) {
	// Every position binds (specs/005, Verified Bind Positions): the account
	// name via IDENTIFIER(?), everything else with a plain ?.
	sql := "CREATE ACCOUNT IDENTIFIER(?) ADMIN_NAME=? ADMIN_RSA_PUBLIC_KEY=? ADMIN_USER_TYPE=SERVICE EMAIL=? EDITION=ENTERPRISE REGION=?"
	args := []any{
		resolvedName,
		"platform",
		publicKey,
		email,
		strings.ToUpper(strings.ReplaceAll(region, "-", "_")),
	}
	if description != "" {
		sql += " COMMENT=?"
		args = append(args, description)
	}

	if err := runner.Exec(ctx, "create account", sql, args...); err != nil {
		var stmtErr *statement.Error
		if errors.As(err, &stmtErr) && stmtErr.SQLState == duplicateAccountSQLState {
			return "", pipeline.Rejected(errors.NewUserError(fmt.Sprintf(
				"account name '%s' is already in use by another account in the organization; rename this resource and try again", resolvedName))).Aborting()
		}
		return "", pipeline.Failed(fmt.Errorf("failed to create account: %w", err)).Aborting()
	}

	locator, err := locateCreatedAccount(ctx, runner, resolvedName)
	if err != nil {
		return "", pipeline.Failed(err).Aborting()
	}
	return locator, pipeline.Done()
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
