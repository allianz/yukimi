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
	stderrors "errors"
	"fmt"
	"strings"

	"github.com/allianz/yukimi/internal/account/pipeline"
	internalerrors "github.com/allianz/yukimi/internal/errors"
	"github.com/allianz/yukimi/internal/secrets"
	"github.com/allianz/yukimi/internal/snowflake/statement"
)

// duplicateAccountSQLState is the SQLSTATE this codebase's own statement
// package tests use to illustrate a Snowflake "already exists" failure (see
// statement/statement_test.go, TestRunnerExecFailureSnowflakeError) — the
// signal used to tell an org-wide account-name collision (a tenant-fixable
// mistake) apart from every other CREATE ACCOUNT failure (a system error).
const duplicateAccountSQLState = "42710"

// Apply re-asserts the account module's desired state: create the account on
// the first reconcile, or re-confirm the platform can still reach it on every
// later one. It never repeats a create once a locator is known — see Key
// Concept: Create-Then-Verify Lifecycle, specs/012-account-module.md.
func (m *module) Apply(ctx context.Context, mc *pipeline.ModuleContext) pipeline.Outcome {
	if mc.Locator() != "" {
		if _, err := mc.TenantDB(ctx); err != nil {
			return pipeline.Failed(fmt.Errorf(
				"platform connection failed for existing account locator %s: %w", mc.Locator(), err)).Aborting()
		}
		return pipeline.Done()
	}

	return m.createAccount(ctx, mc)
}

// createAccount runs the fresh-create path: generate and store the platform
// keypair create-only, issue CREATE ACCOUNT over the org-admin connection,
// then capture the resulting locator onto mc. It never runs when a locator
// is already known.
func (m *module) createAccount(ctx context.Context, mc *pipeline.ModuleContext) pipeline.Outcome {
	cr := mc.CR()

	if len(cr.Spec.Contacts) == 0 {
		return pipeline.Rejected(internalerrors.NewUserError(
			"spec.contacts must contain at least one address")).Aborting()
	}

	resolvedName := mc.ResolvedAccountName()

	creds, err := secrets.NewCredentials("platform")
	if err != nil {
		return pipeline.Failed(fmt.Errorf("failed to generate platform keypair: %w", err)).Aborting()
	}

	marshaled, err := secrets.MarshalCredentials(creds)
	if err != nil {
		return pipeline.Failed(fmt.Errorf("failed to marshal platform credentials: %w", err)).Aborting()
	}

	path, err := secrets.NewTenantPath(m.org, cr.Namespace, cr.Name)
	if err != nil {
		return pipeline.Failed(err).Aborting()
	}

	if err := m.backend.Create(ctx, path, marshaled); err != nil {
		return pipeline.Failed(fmt.Errorf("failed to store platform credentials: %w", err)).Aborting()
	}

	orgAdminDB, err := mc.OrgAdminDB(ctx)
	if err != nil {
		return pipeline.Failed(err).Aborting()
	}
	runner := statement.New(orgAdminDB)

	locator, outcome := runCreateAccount(ctx, runner, resolvedName, cr.Spec.Region, cr.Spec.Contacts[0], cr.Spec.Description, creds.PublicKey)
	if outcome.State != pipeline.StateDone {
		return outcome
	}

	mc.SetLocator(locator)
	return pipeline.Done()
}

// runCreateAccount renders and executes CREATE ACCOUNT over runner, then
// looks up the locator Snowflake assigned. It is pure with respect to
// ModuleContext — testable with a sqlmock-backed *statement.Runner alone.
func runCreateAccount(ctx context.Context, runner *statement.Runner, resolvedName, region, email, description, publicKey string) (string, pipeline.Outcome) {
	nameToken, err := statement.BareIdentifier(resolvedName)
	if err != nil {
		return "", pipeline.Rejected(err).Aborting()
	}

	regionToken, err := statement.BareIdentifier(strings.ToUpper(strings.ReplaceAll(region, "-", "_")))
	if err != nil {
		return "", pipeline.Rejected(err).Aborting()
	}

	sql := fmt.Sprintf(
		"CREATE ACCOUNT %s ADMIN_NAME=%s ADMIN_RSA_PUBLIC_KEY=%s ADMIN_USER_TYPE=SERVICE EMAIL=%s EDITION=ENTERPRISE REGION=%s",
		nameToken,
		statement.QuoteLiteral("platform"),
		statement.QuoteLiteral(publicKey),
		statement.QuoteLiteral(email),
		regionToken,
	)
	if description != "" {
		sql += " COMMENT=" + statement.QuoteLiteral(description)
	}

	if err := runner.Exec(ctx, "create account", sql); err != nil {
		var stmtErr *statement.Error
		if stderrors.As(err, &stmtErr) && stmtErr.SQLState == duplicateAccountSQLState {
			return "", pipeline.Rejected(internalerrors.NewUserError(fmt.Sprintf(
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

// locateCreatedAccount runs SHOW ACCOUNTS LIKE against the just-created
// account's resolved name and returns its locator. The LIKE pattern is a
// coarse pre-filter only — every underscore in resolvedName is a wildcard to
// LIKE, so it discards any row that is not an exact, case-insensitive match
// on the account name before trusting its locator (specs/012-account-module.md,
// Edge Cases).
func locateCreatedAccount(ctx context.Context, runner *statement.Runner, resolvedName string) (string, error) {
	result, err := runner.Query(ctx, "locate created account", "SHOW ACCOUNTS LIKE "+statement.QuoteLiteral(resolvedName))
	if err != nil {
		return "", fmt.Errorf("failed to locate created account %q: %w", resolvedName, err)
	}

	for _, row := range result.Rows {
		name, locator, ok := accountNameAndLocator(row)
		if ok && strings.EqualFold(name, resolvedName) {
			return locator, nil
		}
	}

	return "", fmt.Errorf("CREATE ACCOUNT succeeded but no account named %q was found by SHOW ACCOUNTS", resolvedName)
}

// accountNameAndLocator extracts the account_name/account_locator values from
// a SHOW ACCOUNTS row, matching column keys case-insensitively since the
// driver's actual casing for SHOW output columns is not confirmed.
func accountNameAndLocator(row map[string]any) (name, locator string, ok bool) {
	for key, value := range row {
		s, isString := value.(string)
		if !isString {
			continue
		}
		switch {
		case strings.EqualFold(key, "account_name"):
			name = s
		case strings.EqualFold(key, "account_locator"):
			locator = s
		}
	}
	return name, locator, name != "" && locator != ""
}
