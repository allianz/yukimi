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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	coreaccount "github.com/allianz/yukimi/internal/account"
)

// fakeClientWithNamespace returns a fake client seeded with an empty
// namespace object of the given name, so buildModuleContext's namespace
// fetch succeeds.
func fakeClientWithNamespace(name string) client.Client {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	return fake.NewClientBuilder().WithObjects(ns).Build()
}

func newObserveExternal(modules ...coreaccount.Module) *external {
	return &external{
		kube: fake.NewClientBuilder().Build(),
		log:  logging.NewNopLogger(),
		deps: Dependencies{
			Backplane: newTestBackplane(),
			Pipeline:  coreaccount.New(modules...),
		},
	}
}

// SC-004: an unresolvable region sets Ready=Unavailable() and returns a nil
// error, without ever reaching the pipeline.
func TestObserve_InvalidRegion(t *testing.T) {
	e := newObserveExternal(&fakeModule{name: "account"})
	cr := newTestCR("team-a", "aws-does-not-exist")

	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe() error = %v, want nil", err)
	}
	if got.ResourceExists || got.ResourceUpToDate {
		t.Errorf("Observe() = %+v, want ResourceExists/ResourceUpToDate both false", got)
	}
	if cond := cr.GetCondition(xpv1.TypeReady); cond.Status != corev1.ConditionFalse {
		t.Errorf("Ready condition status = %v, want False", cond.Status)
	}
}

// SC-002: the account not existing and the account existing-but-unreachable
// both surface as ResourceExists: false, with no condition change.
func TestObserve_DoesNotExist(t *testing.T) {
	e := newObserveExternal(&fakeModule{name: "account", observeInSync: false})
	e.kube = fakeClientWithNamespace("team-a")
	cr := newTestCR("team-a", "aws-eu-central-1")

	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe() error = %v, want nil", err)
	}
	if got.ResourceExists {
		t.Errorf("ResourceExists = true, want false")
	}
}

// SC-003: the account existing and in sync sets Ready=Available() and
// computes ResourceUpToDate from ObservedGeneration and InSync.
func TestObserve_ExistsAndInSync(t *testing.T) {
	cases := []struct {
		name               string
		generation         int64
		observedGeneration int64
		wantUpToDate       bool
	}{
		{"generations match", 2, 2, true},
		{"generations differ", 3, 2, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newObserveExternal(&fakeModule{name: "account", observeInSync: true})
			e.kube = fakeClientWithNamespace("team-a")
			cr := newTestCR("team-a", "aws-eu-central-1")
			cr.Generation = tc.generation
			cr.Status.SetObservedGeneration(tc.observedGeneration)

			got, err := e.Observe(context.Background(), cr)
			if err != nil {
				t.Fatalf("Observe() error = %v, want nil", err)
			}
			if !got.ResourceExists {
				t.Fatalf("ResourceExists = false, want true")
			}
			if got.ResourceUpToDate != tc.wantUpToDate {
				t.Errorf("ResourceUpToDate = %v, want %v", got.ResourceUpToDate, tc.wantUpToDate)
			}
			if cond := cr.GetCondition(xpv1.TypeReady); cond.Status != corev1.ConditionTrue {
				t.Errorf("Ready condition status = %v, want True", cond.Status)
			}
		})
	}
}

// The account existing but not in sync sets Ready=Unavailable() and
// ResourceUpToDate: false.
func TestObserve_ExistsButNotInSync(t *testing.T) {
	e := newObserveExternal(
		&fakeModule{name: "account", observeInSync: true},
		&fakeModule{name: "parameter", observeInSync: false},
	)
	e.kube = fakeClientWithNamespace("team-a")
	cr := newTestCR("team-a", "aws-eu-central-1")

	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("Observe() error = %v, want nil", err)
	}
	if !got.ResourceExists {
		t.Fatalf("ResourceExists = false, want true")
	}
	if got.ResourceUpToDate {
		t.Errorf("ResourceUpToDate = true, want false")
	}
	if cond := cr.GetCondition(xpv1.TypeReady); cond.Status != corev1.ConditionFalse {
		t.Errorf("Ready condition status = %v, want False", cond.Status)
	}
}
