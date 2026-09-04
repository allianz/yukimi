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
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"

	"github.com/allianz/yukimi/apis/base/v1alpha1"
)

func newRequest(createdAt time.Time, duration time.Duration) *v1alpha1.SnowflakeDeletionRequest {
	return &v1alpha1.SnowflakeDeletionRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "req",
			Namespace:         "ns",
			CreationTimestamp: metav1.NewTime(createdAt),
		},
		Spec: v1alpha1.SnowflakeDeletionRequestSpec{
			TargetRef: v1alpha1.TargetRef{Kind: "SnowflakeAccount", Name: "acct"},
			Duration:  metav1.Duration{Duration: duration},
			Reason:    "test",
		},
	}
}

// SC-011: a fresh request with a valid duration reaches Active with the
// correct validUntil within one reconcile.
func TestObserve_NewRequestBecomesActive(t *testing.T) {
	now := time.Now()
	cr := newRequest(now, time.Hour)

	e := external{}
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.ResourceExists || !got.ResourceUpToDate {
		t.Fatalf("unexpected observation: %+v", got)
	}
	if cr.Status.State != stateActive {
		t.Fatalf("expected state %q, got %q", stateActive, cr.Status.State)
	}
	wantValidUntil := now.Add(time.Hour)
	if cr.Status.ValidUntil == nil || !cr.Status.ValidUntil.Time.Equal(wantValidUntil) {
		t.Fatalf("expected validUntil %v, got %v", wantValidUntil, cr.Status.ValidUntil)
	}
	if got := cr.GetCondition(xpv1.TypeReady); got.Status != "True" {
		t.Fatalf("expected Ready=True, got %+v", got)
	}
}

// SC-010: a request whose window has passed flips to Expired on the next
// Observe with no spec change required.
func TestObserve_PastWindowBecomesExpired(t *testing.T) {
	cr := newRequest(time.Now().Add(-2*time.Hour), time.Hour)

	e := external{}
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cr.Status.State != stateExpired {
		t.Fatalf("expected state %q, got %q", stateExpired, cr.Status.State)
	}
}

// SC-012: once state reaches a terminal value, a later edit to spec.duration
// does not move validUntil or revert state to Active.
func TestObserve_TerminalStateFreezes(t *testing.T) {
	now := time.Now()
	frozenValidUntil := metav1.NewTime(now.Add(-30 * time.Minute))
	cr := newRequest(now.Add(-time.Hour), time.Hour)
	cr.Status.State = stateExpired
	cr.Status.ValidUntil = &frozenValidUntil

	// Simulate a later edit widening the window well into the future.
	cr.Spec.Duration = metav1.Duration{Duration: 8 * time.Hour}

	e := external{}
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cr.Status.State != stateExpired {
		t.Fatalf("expected state to remain %q, got %q", stateExpired, cr.Status.State)
	}
	if !cr.Status.ValidUntil.Time.Equal(frozenValidUntil.Time) {
		t.Fatalf("expected validUntil to remain %v, got %v", frozenValidUntil, cr.Status.ValidUntil)
	}

	// Consumed is terminal too.
	cr.Status.State = stateConsumed
	if _, err := e.Observe(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cr.Status.State != stateConsumed {
		t.Fatalf("expected state to remain %q, got %q", stateConsumed, cr.Status.State)
	}
}

// SC-017: a deleted request releases the finalizer via ResourceExists: false.
func TestObserve_Deleted(t *testing.T) {
	now := metav1.NewTime(time.Now())
	cr := newRequest(time.Now(), time.Hour)
	cr.DeletionTimestamp = &now

	e := external{}
	got, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ResourceExists {
		t.Fatalf("expected ResourceExists=false, got %+v", got)
	}
}

func TestCreateUpdateDeleteDisconnect_AreNoOps(t *testing.T) {
	cr := newRequest(time.Now(), time.Hour)
	e := external{}

	if _, err := e.Create(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := e.Update(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := e.Delete(context.Background(), cr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := e.Disconnect(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConnector_Connect(t *testing.T) {
	c := &connector{}
	client, err := c.Connect(context.Background(), newRequest(time.Now(), time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("expected a non-nil external client")
	}
}
