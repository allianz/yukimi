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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
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

// Regression test for a bug found live: crossplane-runtime's managed
// reconciler reverts any in-memory status change Create() makes once it
// persists the critical external-create-succeeded annotation right
// afterward (a plain Update() on a status-subresource type decodes the API
// server's still-stale status back over ours). Without apply()'s own
// synchronous Status().Update() call, a freshly captured accountLocator
// never survives past the in-memory struct — this test proves it survives
// a second, independent Get() against the same fake client, not just a
// re-read of the same cr pointer apply() already mutated.
func TestApply_StatusSurvivesAFreshGet(t *testing.T) {
	cr := newTestCR("team-a", "aws-eu-central-1")
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(clientgoscheme) error = %v", err)
	}
	if err := v1alpha1.SchemeBuilder.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(v1alpha1) error = %v", err)
	}
	// WithStatusSubresource is required for the fake client's Status().Update()
	// to behave like a real cluster's status subresource (kubebuilder's
	// +kubebuilder:subresource:status marker on the type isn't itself visible
	// to the fake client) — without it Status().Update() fails "not found".
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.SnowflakeAccount{}).WithObjects(ns, cr).Build()

	// A real reconcile always calls Create with an object freshly Get'd from
	// the API server (carrying its real resourceVersion); WithObjects seeded
	// the tracker with a copy, leaving cr's own resourceVersion at its zero
	// value, so re-fetch it here to match that real precondition.
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(cr), cr); err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	e := &external{
		kube: kube,
		log:  logging.NewNopLogger(),
		deps: Dependencies{
			Backplane: newTestBackplane(),
			Pipeline:  coreaccount.New(&fakeModule{name: "account", applyOut: coreaccount.Done(), setLocator: "xc19114"}),
			Config:    &config.BaseConfig{},
			Pool:      &fakePool{},
		},
	}

	if _, err := e.Create(context.Background(), cr); err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}

	fresh := &v1alpha1.SnowflakeAccount{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(cr), fresh); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if fresh.Status.AccountLocator != "xc19114" {
		t.Errorf("a fresh Get() sees AccountLocator = %q, want %q — status did not survive", fresh.Status.AccountLocator, "xc19114")
	}
}
