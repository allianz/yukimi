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

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/gate"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
)

// SetupGated only registers Setup against the gate; it never invokes Setup
// (which needs a real ctrl.Manager) unless every dependency the
// SnowflakeAccount GVK is gated on is satisfied — never true in this test,
// so passing a nil manager is safe.
func TestSetupGated_RegistersWithoutInvokingSetup(t *testing.T) {
	o := controller.Options{
		Logger: logging.NewNopLogger(),
		Gate:   new(gate.Gate[schema.GroupVersionKind]),
	}

	if err := SetupGated(nil, o, Dependencies{}); err != nil {
		t.Fatalf("SetupGated() error = %v, want nil", err)
	}
}

// Connect returns an *external carrying this connector's kube client and
// Dependencies unchanged.
func TestConnector_Connect(t *testing.T) {
	kube := fake.NewClientBuilder().Build()
	deps := Dependencies{Backplane: newTestBackplane()}
	c := &connector{kube: kube, deps: deps, log: logging.NewNopLogger()}

	client, err := c.Connect(context.Background(), &v1alpha1.SnowflakeAccount{})
	if err != nil {
		t.Fatalf("Connect() error = %v, want nil", err)
	}

	e, ok := client.(*external)
	if !ok {
		t.Fatalf("Connect() returned %T, want *external", client)
	}
	if e.kube != kube {
		t.Errorf("external.kube not forwarded from connector")
	}
	if e.deps.Backplane != deps.Backplane {
		t.Errorf("external.deps not forwarded from connector")
	}
}

func TestExternal_Disconnect(t *testing.T) {
	e := &external{}
	if err := e.Disconnect(context.Background()); err != nil {
		t.Errorf("Disconnect() error = %v, want nil", err)
	}
}
