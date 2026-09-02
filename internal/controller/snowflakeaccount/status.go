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
	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/account"
	"github.com/allianz/yukimi/internal/logger"
	"github.com/allianz/yukimi/internal/tenant"
)

// persistStatus sets status.accountName and status.accountLocator from mc
// immediately — before computing status.accountUrl or rendering any
// condition — so a crash between this call and the rest of apply() never
// loses a locator Apply already captured (specs/018, SC-007; specs/010's
// create-then-verify lifecycle).
//
// status.accountName/accountLocator come only from mc, never from any
// module's Outcome (SC-008).
func persistStatus(cr *v1alpha1.SnowflakeAccount, mc *account.ModuleContext, usePrivateLink bool, log *logger.Logger) {
	cr.Status.AccountName = mc.ResolvedAccountName()
	cr.Status.AccountLocator = mc.Locator()

	if mc.Locator() == "" {
		return
	}

	url, err := tenant.AccountURL(mc.Locator(), cr.Spec.Region, usePrivateLink)
	if err != nil {
		// A cosmetic URL failure must not fail apply() or block Ready — the
		// account itself was created successfully (SC-009).
		_ = log.Handle(err)
		return
	}
	cr.Status.AccountURL = url
}
