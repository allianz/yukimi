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

	"github.com/allianz/yukimi/internal/account/pipeline"
	"github.com/allianz/yukimi/internal/secrets"
	"github.com/allianz/yukimi/internal/snowflake/statement"
)

// Teardown drops the account (if one was ever created), evicts the pooled
// connection to it, and deletes the platform credential — in that fixed
// order, stopping at the first real failure. Every step is safe to re-run:
// DROP ACCOUNT IF EXISTS makes a missing account a no-op, EvictTenant is a
// no-op on a key never dialed, and KeyStore.Delete is idempotent on an
// absent or already-pending credential (003) (Key Concept: Two Restore
// Windows, specs/012-account-module.md).
func (m *module) Teardown(ctx context.Context, mc *pipeline.ModuleContext) error {
	cr := mc.CR()

	if cr.Status.AccountLocator != "" {
		if err := m.dropAccount(ctx, mc); err != nil {
			return err
		}
		mc.EvictTenant()
		// Observe's deletion check (020) reports ResourceExists solely from
		// this field, so leaving it set would make the account look
		// permanently undeleted and the finalizer would never clear.
		cr.Status.AccountLocator = ""
	}

	return m.deleteCredential(ctx, mc)
}

// dropAccount issues DROP ACCOUNT over the org-admin connection, reserved in
// this module for exactly this call and CREATE ACCOUNT (Key Concept: The
// Only Module With Organization-Wide Privileges). IF EXISTS makes a
// concurrently- or already-dropped account a success rather than an error,
// so no SQLSTATE needs classifying here.
func (m *module) dropAccount(ctx context.Context, mc *pipeline.ModuleContext) error {
	db, err := mc.OrgAdminDB(ctx)
	if err != nil {
		return err
	}

	// The name binds; GRACE_PERIOD_IN_DAYS does not (specs/005, Verified Bind
	// Positions), so the integer from config is formatted into the text.
	sql := fmt.Sprintf("DROP ACCOUNT IF EXISTS IDENTIFIER(?) GRACE_PERIOD_IN_DAYS = %d", m.deletionGracePeriodDays)
	if err := statement.New(db).Exec(ctx, "drop account", sql, mc.ResolvedAccountName()); err != nil {
		return fmt.Errorf("failed to drop account: %w", err)
	}
	return nil
}

// deleteCredential schedules the platform credential's removal. An absent or
// already-pending credential is success (KeyStore.Delete is idempotent, 003),
// so any error returned here is a real failure — never swallowed, since a
// leftover credential keeps the tenant's name blocked after the account is
// gone.
func (m *module) deleteCredential(ctx context.Context, mc *pipeline.ModuleContext) error {
	cr := mc.CR()

	id, err := secrets.NewTenantIdentifier(m.org, cr.Namespace, cr.Name)
	if err != nil {
		return err
	}

	if err := m.keyManager.DeleteCredentials(ctx, id); err != nil {
		return fmt.Errorf("failed to delete platform credential: %w", err)
	}
	return nil
}
