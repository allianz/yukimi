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

package deletion

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/allianz/yukimi/apis/base/v1alpha1"
)

// SC-015: MarkConsumed sets state to Consumed and leaves validUntil as-is.
func TestMarkConsumed_SetsState(t *testing.T) {
	frozen := metav1.NewTime(time.Now().Truncate(time.Second))
	req := newRequest("req", "ns", "SnowflakeAccount", "acct", stateActive, time.Now())
	req.Status.ValidUntil = &frozen

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(req).WithStatusSubresource(req).Build()

	if err := MarkConsumed(context.Background(), c, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Status.State != stateConsumed {
		t.Fatalf("expected state %q, got %q", stateConsumed, req.Status.State)
	}
	if req.Status.ValidUntil == nil || !req.Status.ValidUntil.Equal(&frozen) {
		t.Fatalf("expected validUntil to remain %v, got %v", frozen, req.Status.ValidUntil)
	}

	var persisted v1alpha1.SnowflakeDeletionRequest
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(req), &persisted); err != nil {
		t.Fatalf("unexpected error fetching persisted object: %v", err)
	}
	if persisted.Status.State != stateConsumed {
		t.Fatalf("expected persisted state %q, got %q", stateConsumed, persisted.Status.State)
	}
}

// erroringStatusClient wraps a client.Client and fails every status Update
// call, to exercise MarkConsumed's system-error wrapping (SC-016).
type erroringStatusClient struct {
	client.Client
}

func (e *erroringStatusClient) Status() client.SubResourceWriter {
	return erroringSubResourceWriter{}
}

type erroringSubResourceWriter struct{}

func (erroringSubResourceWriter) Create(_ context.Context, _ client.Object, _ client.Object, _ ...client.SubResourceCreateOption) error {
	return errors.New("simulated API failure")
}

func (erroringSubResourceWriter) Update(_ context.Context, _ client.Object, _ ...client.SubResourceUpdateOption) error {
	return errors.New("simulated API failure")
}

func (erroringSubResourceWriter) Patch(_ context.Context, _ client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
	return errors.New("simulated API failure")
}

// SC-016: a Kubernetes API failure is wrapped as a system error.
func TestMarkConsumed_UpdateError(t *testing.T) {
	req := newRequest("req", "ns", "SnowflakeAccount", "acct", stateActive, time.Now())
	c := &erroringStatusClient{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(req).Build()}

	if err := MarkConsumed(context.Background(), c, req); err == nil {
		t.Fatal("expected an error, got nil")
	}
}
