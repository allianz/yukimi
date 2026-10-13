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

	"github.com/allianz/yukimi/internal/account/pipeline"
)

// Observe reports whether the account exists (a known locator) and, if so,
// re-confirms the platform can still log in — it never looks accounts up
// over the org-admin connection (see Key Concept: Create-Then-Verify
// Lifecycle, specs/012-account-module.md), since every module downstream of
// this one already needs the same platform-authenticated connection. While
// the account is within its post-create grace period, it skips the
// connection attempt entirely rather than trying and failing.
func (m *module) Observe(ctx context.Context, data *pipeline.ResourceData) pipeline.Outcome {
	cr := data.CR()
	if cr.Status.AccountLocator == "" {
		return pipeline.Pending("account not created yet")
	}
	m.syncStatus(data)

	if withinGracePeriod(cr, m.gracePeriod) {
		return pipeline.Pending(provisioningMessage)
	}

	if _, err := connectTenant(ctx, data); err != nil {
		return pipeline.Failed(err)
	}

	return pipeline.Done()
}
