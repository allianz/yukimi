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

// Package account creates a tenant's Snowflake account and bootstraps the
// platform service user (design.md 3.6). Every module that needs a live
// Snowflake connection must be registered after it in the pipeline, but it
// need not be registered first overall — see pipeline.AccountModuleName. See
// specs/012-account-module.md.
package account

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/account/pipeline"
	"github.com/allianz/yukimi/internal/account/tenant"
	"github.com/allianz/yukimi/internal/config/backplane"
	"github.com/allianz/yukimi/internal/secrets"
)

// module is the account module. It is the only module in the pipeline that
// ever opens an org-admin-scoped connection — on the fresh-create path in
// Apply and the drop path in Teardown.
type module struct {
	keyManager              *secrets.KeyManager
	org                     string
	gracePeriod             time.Duration
	deletionGracePeriodDays int
	usePrivateLink          bool
	backplane               *backplane.Config
}

// New constructs the account module.
//
// Parameters:
//   - keyManager: the *secrets.KeyManager (003) the platform keypair is stored
//     through, via KeyManager.CreateCredentials and, on teardown,
//     KeyManager.DeleteCredentials — this module never calls Update. On the
//     fresh-create path, if CreateCredentials fails for any reason other than
//     ErrPendingDeletion, KeyManager.GetCredentials is called once, read-only,
//     to decide whether a secret found there is this resource's own crashed
//     attempt (Key Concept: Resuming a Crashed Create).
//   - org: Config.Snowflake.Org (002), used to build the tenant secret
//     identifier (003) exactly as internal/snowflake/pool does.
//   - gracePeriod: Config.Snowflake.AccountCreationGracePeriod (002); how
//     long a fresh account is given to become reachable before the first
//     post-create connection attempt.
//   - deletionGracePeriodDays: Config.Deletion.GracePeriodDays (002) —
//     rendered verbatim as DROP ACCOUNT's GRACE_PERIOD_IN_DAYS on teardown.
//     Not to be confused with gracePeriod above, which is a post-create
//     reachability delay and has nothing to do with deletion. Already
//     bounded to 7-90 by 002's loader, so this module does
//     not re-validate it.
//   - usePrivateLink: Config.Snowflake.UsePrivateLink (002), used only to
//     build status.accountUrl via tenant.AccountURL.
//   - bpConfig: the loaded Backplane Config (007), consulted on the
//     fresh-create path for region existence via Region(), and — combined
//     with the tenant's alpha-tester namespace label — for region
//     availability via Region.Available (Key Concept: Region Validation).
//
// Returns:
//   - pipeline.Module: never nil.
func New(keyManager *secrets.KeyManager, org string, gracePeriod time.Duration, deletionGracePeriodDays int, usePrivateLink bool, bpConfig *backplane.Config) pipeline.Module {
	return &module{keyManager: keyManager, org: org, gracePeriod: gracePeriod, deletionGracePeriodDays: deletionGracePeriodDays, usePrivateLink: usePrivateLink, backplane: bpConfig}
}

func (m *module) Name() string { return pipeline.AccountModuleName }

// provisioningMessage is the Pending message Observe and Apply share while
// the account is within its post-create grace period.
const provisioningMessage = "waiting for the account to finish provisioning before attempting to connect"

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

// connectTenant opens the platform connection to an account whose locator is
// already known. A failure here means the account exists but is currently
// unreachable — a system error, never a reason to create it again (Key
// Concept: Create-Then-Verify Lifecycle).
func connectTenant(ctx context.Context, mc *pipeline.ModuleContext) (*sql.DB, error) {
	db, err := mc.TenantDB(ctx)
	if err != nil {
		return nil, fmt.Errorf(
			"platform connection failed for existing account locator %s: %w", mc.CR().Status.AccountLocator, err)
	}
	return db, nil
}

// syncStatus sets status.accountName and, once a locator is known,
// status.accountUrl on the CRD. A tenant.AccountURL error is logged and
// swallowed, never failing the caller: it can only fire despite CREATE ACCOUNT
// having already succeeded with that same region string
// (specs/012-account-module.md, Integration Points).
func (m *module) syncStatus(mc *pipeline.ModuleContext) {
	cr := mc.CR()
	cr.Status.AccountName = mc.ResolvedAccountName()
	if cr.Status.AccountLocator == "" {
		return
	}
	url, err := tenant.AccountURL(cr.Status.AccountLocator, cr.Spec.Region, m.usePrivateLink)
	if err != nil {
		_ = mc.Logger().Handle(err)
		return
	}
	cr.Status.AccountURL = url
}
