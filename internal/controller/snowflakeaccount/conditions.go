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
	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/account"
)

// renderReady sets cr's Ready condition from one Apply pass's Result: True
// iff every module that ran reported Done and the run did not abort
// (SC-011, equivalent to account.Result.AllDone()); False otherwise, carrying
// failureMsg when one is available.
//
// This is not yet the per-module condition-rendering loop 009's Appendix
// sketches over account.GatesReady (walking Result.Outcomes and surfacing
// each module's own Outcome.Condition, e.g. IdentitySynced/QuotaAvailable).
// That loop only becomes live once a registered module actually sets
// Outcome.Condition — neither 015 nor 016 is registered today, so there is
// nothing yet for it to render (specs/018, D-004/D-005, Forward Contracts).
func renderReady(cr *v1alpha1.SnowflakeAccount, result account.Result, failureMsg string) {
	if result.AllDone() {
		cr.SetConditions(xpv1.Available())
		return
	}

	cond := xpv1.Unavailable()
	if failureMsg != "" {
		cond = cond.WithMessage(failureMsg)
	}
	cr.SetConditions(cond)
}
