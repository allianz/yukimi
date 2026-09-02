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

package snowflakeaccount

import (
	"context"
	"fmt"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/logger"
	"github.com/allianz/yukimi/internal/snowflake/statement"
	"github.com/allianz/yukimi/internal/tenant"
)

// Delete drops the Snowflake account unconditionally: one idempotent
// DROP ACCOUNT IF EXISTS, over the org-admin connection, every time it is
// invoked — whether or not the account was ever actually created, and with
// no warrant lookup, no stall, no DeletionBlocked event. Positive-control
// deletion warrants (017) do not exist yet; when they land, this method is
// replaced outright, not extended (specs/018, Key Concept: Delete Now,
// Warrant Later; D-002).
func (e *external) Delete(ctx context.Context, cr *v1alpha1.SnowflakeAccount) (managed.ExternalDelete, error) {
	log := logger.New(e.log, cr.Namespace, "SnowflakeAccount", cr.Name, logger.OpDelete)

	resolvedName := tenant.ResolveName(cr.Name, cr.Namespace)
	nameToken, err := statement.BareIdentifier(resolvedName)
	if err != nil {
		return managed.ExternalDelete{}, log.Handle(err)
	}

	db, err := e.deps.Pool.OrgAdmin(ctx)
	if err != nil {
		return managed.ExternalDelete{}, log.Handle(err)
	}

	runner := statement.New(db)
	sql := fmt.Sprintf("DROP ACCOUNT IF EXISTS %s GRACE_PERIOD_IN_DAYS = 3", nameToken)
	if err := runner.Exec(ctx, "drop account", sql); err != nil {
		return managed.ExternalDelete{}, log.Handle(fmt.Errorf("failed to drop account: %w", err))
	}
	return managed.ExternalDelete{}, nil
}
