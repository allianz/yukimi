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

package snowflakedeletionrequest

import (
	"testing"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/config"

	"github.com/allianz/yukimi/apis/base/v1alpha1"
)

// newTestManager builds a controller-runtime Manager against an
// unreachable API server. Manager/controller construction does not itself
// contact the API server, so this is enough to exercise Setup without a
// real cluster or envtest.
func newTestManager(t *testing.T) ctrl.Manager {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.SchemeBuilder.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to build scheme: %v", err)
	}
	skipNameValidation := true
	mgr, err := ctrl.NewManager(&rest.Config{Host: "http://127.0.0.1:1"}, ctrl.Options{
		Scheme:     scheme,
		Controller: config.Controller{SkipNameValidation: &skipNameValidation},
	})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	return mgr
}

// SC-019 support: exercises Setup's default option wiring.
func TestSetup_Default(t *testing.T) {
	mgr := newTestManager(t)
	o := controller.Options{
		Logger:   logging.NewNopLogger(),
		Features: &feature.Flags{},
	}
	if err := Setup(mgr, o); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Exercises the ManagementPolicies, ChangeLogs and MetricOptions branches
// together, matching how cmd/provider/main.go can enable all three.
func TestSetup_AllOptionalFeaturesEnabled(t *testing.T) {
	mgr := newTestManager(t)

	features := &feature.Flags{}
	features.Enable(feature.EnableBetaManagementPolicies)
	features.Enable(feature.EnableAlphaChangeLogs)

	o := controller.Options{
		Logger:           logging.NewNopLogger(),
		Features:         features,
		ChangeLogOptions: &controller.ChangeLogOptions{},
		MetricOptions:    &controller.MetricOptions{},
	}
	if err := Setup(mgr, o); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// fakeGate captures Register calls instead of gating on real GVK readiness.
type fakeGate struct {
	registered []schema.GroupVersionKind
	callback   func()
}

func (g *fakeGate) Register(callback func(), gvks ...schema.GroupVersionKind) {
	g.registered = append(g.registered, gvks...)
	g.callback = callback
}

func (g *fakeGate) Set(_ schema.GroupVersionKind, _ bool) bool {
	return false
}

// SC-009 support: SetupGated registers against the SnowflakeDeletionRequest
// GVK and its callback successfully runs Setup.
func TestSetupGated_RegistersAndRuns(t *testing.T) {
	mgr := newTestManager(t)
	gate := &fakeGate{}
	o := controller.Options{
		Logger:   logging.NewNopLogger(),
		Features: &feature.Flags{},
		Gate:     gate,
	}

	if err := SetupGated(mgr, o); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gate.registered) != 1 || gate.registered[0] != v1alpha1.SnowflakeDeletionRequestGroupVersionKind {
		t.Fatalf("expected registration for %v, got %v", v1alpha1.SnowflakeDeletionRequestGroupVersionKind, gate.registered)
	}
	if gate.callback == nil {
		t.Fatal("expected a callback to be registered")
	}

	// Running the callback should not panic — it drives Setup to
	// completion successfully.
	gate.callback()
}
