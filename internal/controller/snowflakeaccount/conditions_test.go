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
	"testing"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	coreaccount "github.com/allianz/yukimi/internal/account"
)

// SC-011: Ready is Available() only when every module that ran reported
// success and the run did not abort; Unavailable() otherwise, carrying
// failureMsg when one is supplied.
func TestRenderReady(t *testing.T) {
	cases := []struct {
		name       string
		result     coreaccount.Result
		failureMsg string
		wantStatus corev1.ConditionStatus
		wantMsg    string
	}{
		{
			name:       "all modules done",
			result:     coreaccount.Result{Outcomes: []coreaccount.ModuleOutcome{{Module: "account", Outcome: coreaccount.Done()}}},
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "aborted with a failure message",
			result: coreaccount.Result{
				Aborted:  true,
				Outcomes: []coreaccount.ModuleOutcome{{Module: "account", Outcome: coreaccount.Rejected(nil).Aborting()}},
			},
			failureMsg: "bad input",
			wantStatus: corev1.ConditionFalse,
			wantMsg:    "bad input",
		},
		{
			name:       "no modules ran",
			result:     coreaccount.Result{},
			wantStatus: corev1.ConditionFalse,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := &v1alpha1.SnowflakeAccount{}
			renderReady(cr, tc.result, tc.failureMsg)

			cond := cr.GetCondition(xpv1.TypeReady)
			if cond.Status != tc.wantStatus {
				t.Errorf("status = %v, want %v", cond.Status, tc.wantStatus)
			}
			if cond.Message != tc.wantMsg {
				t.Errorf("message = %q, want %q", cond.Message, tc.wantMsg)
			}
		})
	}
}
