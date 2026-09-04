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
// no-op on a key never dialed, and an already-absent credential path is
// detected by this module itself (see deleteCredential) rather than trusted
// to the backend (Key Concept: Two Restore Windows, specs/012-account-module.md).
func (m *module) Teardown(ctx context.Context, mc *pipeline.ModuleContext) error {
	cr := mc.CR()

	if cr.Status.AccountLocator != "" {
		if err := m.dropAccount(ctx, mc); err != nil {
			return err
		}
		mc.EvictTenant()
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

	nameToken, err := statement.BareIdentifier(mc.ResolvedAccountName())
	if err != nil {
		return err
	}

	sql := fmt.Sprintf("DROP ACCOUNT IF EXISTS %s GRACE_PERIOD_IN_DAYS = %d", nameToken, m.deletionGracePeriodDays)
	if err := statement.New(db).Exec(ctx, "drop account", sql); err != nil {
		return fmt.Errorf("failed to drop account: %w", err)
	}
	return nil
}

// deleteCredential removes the platform credential. secrets.Backend's
// contract is that no caller branches on an error's identity, and the
// reference AWS backend is not itself idempotent on an already-absent path
// (internal/secrets/aws/backend.go) — so instead of inspecting Delete's
// error, this checks presence with Get first: an unreadable path is either
// genuinely absent or already scheduled for deletion by an earlier Teardown
// attempt, and either way there is nothing left to delete.
func (m *module) deleteCredential(ctx context.Context, mc *pipeline.ModuleContext) error {
	cr := mc.CR()

	path, err := secrets.NewTenantPath(m.org, cr.Namespace, cr.Name)
	if err != nil {
		return err
	}

	if _, _, err := m.backend.Get(ctx, path); err != nil {
		return nil
	}

	restorableUntil, err := m.backend.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("failed to delete platform credential: %w", err)
	}

	if log := mc.Logger(); log != nil {
		log.Info(fmt.Sprintf("platform credential at %s restorable until %s", path, restorableUntil))
	}
	return nil
}
