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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	internalerrors "github.com/allianz/yukimi/internal/errors"
	"github.com/allianz/yukimi/internal/logger"
	"github.com/allianz/yukimi/internal/tenant"
)

func newTestCR(namespace, region string) *v1alpha1.SnowflakeAccount {
	return &v1alpha1.SnowflakeAccount{
		ObjectMeta: metav1.ObjectMeta{Name: "acct", Namespace: namespace},
		Spec:       v1alpha1.SnowflakeAccountSpec{Region: region},
	}
}

// SC-005: buildModuleContext fetches the tenant namespace's labels and
// passes them, and the resolved region, unchanged into NewModuleContext.
func TestBuildModuleContext_Success(t *testing.T) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "team-a", Labels: map[string]string{"department": "data"}},
	}
	kube := fake.NewClientBuilder().WithObjects(ns).Build()
	e := &external{kube: kube, deps: Dependencies{Backplane: newTestBackplane()}}
	cr := newTestCR("team-a", "aws-eu-central-1")

	mc, err := e.buildModuleContext(context.Background(), cr, newTestLogger(logger.OpObserve))
	if err != nil {
		t.Fatalf("buildModuleContext() error = %v, want nil", err)
	}
	if got := mc.NamespaceLabels()["department"]; got != "data" {
		t.Errorf("NamespaceLabels()[department] = %q, want %q", got, "data")
	}
	if mc.BackplaneRegion() == nil || !mc.BackplaneRegion().Available {
		t.Errorf("BackplaneRegion() = %+v, want an available region", mc.BackplaneRegion())
	}
	if got, want := mc.ResolvedAccountName(), tenant.ResolveName(cr.Name, cr.Namespace); got != want {
		t.Errorf("ResolvedAccountName() = %q, want %q", got, want)
	}
}

// Unknown region: bubbles 007's own user error unchanged.
func TestBuildModuleContext_UnknownRegion(t *testing.T) {
	kube := fake.NewClientBuilder().Build()
	e := &external{kube: kube, deps: Dependencies{Backplane: newTestBackplane()}}
	cr := newTestCR("team-a", "aws-does-not-exist")

	_, err := e.buildModuleContext(context.Background(), cr, newTestLogger(logger.OpObserve))
	if err == nil || !internalerrors.IsUserError(err) {
		t.Fatalf("buildModuleContext() error = %v, want a user error", err)
	}
}

// SC-004 (validation phase half): a region known but not available is this
// package's own user error.
func TestBuildModuleContext_RegionNotAvailable(t *testing.T) {
	kube := fake.NewClientBuilder().Build()
	e := &external{kube: kube, deps: Dependencies{Backplane: newTestBackplane()}}
	cr := newTestCR("team-a", "aws-eu-west-1-cold")

	_, err := e.buildModuleContext(context.Background(), cr, newTestLogger(logger.OpObserve))
	if err == nil || !internalerrors.IsUserError(err) {
		t.Fatalf("buildModuleContext() error = %v, want a user error", err)
	}
}

// A namespace fetch failure is a system error, not a user error.
func TestBuildModuleContext_NamespaceFetchFails(t *testing.T) {
	kube := fake.NewClientBuilder().Build() // no namespace seeded
	e := &external{kube: kube, deps: Dependencies{Backplane: newTestBackplane()}}
	cr := newTestCR("missing-ns", "aws-eu-central-1")

	_, err := e.buildModuleContext(context.Background(), cr, newTestLogger(logger.OpObserve))
	if err == nil || internalerrors.IsUserError(err) {
		t.Fatalf("buildModuleContext() error = %v, want a system error", err)
	}
}
