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
	"time"

	"github.com/allianz/yukimi/internal/account/pipeline"
	"github.com/allianz/yukimi/internal/secrets"
)

// module is the account module. It is the only module in the pipeline that
// ever opens an org-admin-scoped connection — on the fresh-create path in
// Apply and the drop path in Teardown.
type module struct {
	backend                 secrets.Backend
	org                     string
	gracePeriod             time.Duration
	deletionGracePeriodDays int
}

// New constructs the account module.
//
// Parameters:
//   - backend: the secrets.Backend (003) the platform keypair is stored
//     through, via Backend.Create and, on teardown, Backend.Delete — this
//     module never calls Update.
//   - org: Config.Snowflake.Org (002), used to build the tenant secret
//     path (003) exactly as internal/snowflake/pool does.
//   - gracePeriod: Config.Snowflake.AccountCreationGracePeriod (002); how
//     long a fresh account is given to become reachable before the first
//     post-create connection attempt.
//   - deletionGracePeriodDays: Config.Deletion.GracePeriodDays (002) —
//     rendered verbatim as DROP ACCOUNT's GRACE_PERIOD_IN_DAYS on teardown.
//     Not to be confused with gracePeriod above, which is a post-create
//     reachability delay and has nothing to do with deletion. Already
//     bounded to Snowflake's own 3-90 by 002's loader, so this module does
//     not re-validate it.
//
// Returns:
//   - pipeline.Module: never nil.
func New(backend secrets.Backend, org string, gracePeriod time.Duration, deletionGracePeriodDays int) pipeline.Module {
	return &module{backend: backend, org: org, gracePeriod: gracePeriod, deletionGracePeriodDays: deletionGracePeriodDays}
}

func (m *module) Name() string { return pipeline.AccountModuleName }
