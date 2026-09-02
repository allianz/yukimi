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
	"testing"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	corev1 "k8s.io/api/core/v1"

	coreaccount "github.com/allianz/yukimi/internal/account"
	"github.com/allianz/yukimi/internal/config"
	internalerrors "github.com/allianz/yukimi/internal/errors"
)

func newApplyExternal(modules ...coreaccount.Module) *external {
	return &external{
		kube: fakeClientWithNamespace("team-a"),
		log:  logging.NewNopLogger(),
		deps: Dependencies{
			Backplane: newTestBackplane(),
			Pipeline:  coreaccount.New(modules...),
			Config:    &config.BaseConfig{},
			Pool:      &fakePool{},
		},
	}
}

// SC-006: Create and Update share apply()'s body; given the same CRD and
// pipeline outcome, both produce identical status and condition results.
func TestApply_CreateAndUpdateAreIdentical(t *testing.T) {
	for _, op := range []string{"create", "update"} {
		t.Run(op, func(t *testing.T) {
			e := newApplyExternal(&fakeModule{name: "account", applyOut: coreaccount.Done()})
			cr := newTestCR("team-a", "aws-eu-central-1")
			cr.Generation = 3

			var err error
			if op == "create" {
				_, err = e.Create(context.Background(), cr)
			} else {
				_, err = e.Update(context.Background(), cr)
			}
			if err != nil {
				t.Fatalf("%s() error = %v, want nil", op, err)
			}
			if cond := cr.GetCondition(xpv1.TypeReady); cond.Status != corev1.ConditionTrue {
				t.Errorf("Ready condition = %v, want True", cond.Status)
			}
			if got := cr.Status.GetObservedGeneration(); got != cr.Generation {
				t.Errorf("ObservedGeneration = %d, want %d", got, cr.Generation)
			}
		})
	}
}

// SC-011: a Rejected/aborted outcome renders Ready=Unavailable() carrying
// the module's own classified message, unmodified by this package.
// SC-010: ObservedGeneration does not advance.
func TestApply_RejectedOutcome(t *testing.T) {
	e := newApplyExternal(&fakeModule{
		name:     "account",
		applyOut: coreaccount.Rejected(internalerrors.NewUserError("bad input")).Aborting(),
	})
	cr := newTestCR("team-a", "aws-eu-central-1")
	cr.Generation = 5

	if _, err := e.Create(context.Background(), cr); err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}

	cond := cr.GetCondition(xpv1.TypeReady)
	if cond.Status != corev1.ConditionFalse {
		t.Errorf("Ready condition = %v, want False", cond.Status)
	}
	if cond.Message != "bad input" {
		t.Errorf("Ready condition message = %q, want %q", cond.Message, "bad input")
	}
	if got := cr.Status.GetObservedGeneration(); got == cr.Generation {
		t.Errorf("ObservedGeneration advanced to %d, want unchanged", got)
	}
}

// buildModuleContext failing (e.g. an unresolvable region) is returned as a
// framework-classified error, per CLAUDE.md's Create/Update error pattern.
func TestApply_InvalidRegionReturnsError(t *testing.T) {
	e := newApplyExternal(&fakeModule{name: "account"})
	cr := newTestCR("team-a", "aws-does-not-exist")

	if _, err := e.Create(context.Background(), cr); err == nil {
		t.Fatal("Create() error = nil, want error")
	}
}

// SC-007/SC-008: status.accountLocator/accountName persist from the
// ModuleContext even when the run ends in a non-Done outcome, and are
// unaffected by that failure.
func TestApply_PersistsStatusEvenOnFailure(t *testing.T) {
	e := newApplyExternal(&fakeModule{
		name:     "account",
		applyOut: coreaccount.Failed(internalerrors.NewUserError("boom")).Aborting(),
	})
	cr := newTestCR("team-a", "aws-eu-central-1")
	cr.Status.AccountLocator = "xc12345" // as if a previous Apply already created the account

	if _, err := e.Create(context.Background(), cr); err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	if cr.Status.AccountLocator != "xc12345" {
		t.Errorf("AccountLocator = %q, want unchanged %q", cr.Status.AccountLocator, "xc12345")
	}
	if cr.Status.AccountName == "" {
		t.Errorf("AccountName not set")
	}
}
