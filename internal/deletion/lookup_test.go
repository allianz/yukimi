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
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/allianz/yukimi/apis/base/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.SchemeBuilder.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to build scheme: %v", err)
	}
	return scheme
}

func newRequest(name, namespace, targetKind, targetName, state string, createdAt time.Time) *v1alpha1.SnowflakeDeletionRequest {
	return &v1alpha1.SnowflakeDeletionRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         namespace,
			CreationTimestamp: metav1.NewTime(createdAt),
		},
		Spec: v1alpha1.SnowflakeDeletionRequestSpec{
			TargetRef: v1alpha1.TargetRef{Kind: targetKind, Name: targetName},
			Reason:    "test",
		},
		Status: v1alpha1.SnowflakeDeletionRequestStatus{
			State: state,
		},
	}
}

// SC-013: no match returns nil, nil.
func TestFindActiveRequest_NoMatch(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()

	got, err := FindActiveRequest(context.Background(), c, "ns", "SnowflakeAccount", "acct")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

// SC-013: only Active requests matching kind/name are returned.
func TestFindActiveRequest_FiltersStateAndTarget(t *testing.T) {
	now := time.Now()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(
		newRequest("expired", "ns", "SnowflakeAccount", "acct", "Expired", now),
		newRequest("consumed", "ns", "SnowflakeAccount", "acct", "Consumed", now),
		newRequest("other-target", "ns", "SnowflakeAccount", "other-acct", "Active", now),
		newRequest("other-ns", "ns2", "SnowflakeAccount", "acct", "Active", now),
		newRequest("match", "ns", "SnowflakeAccount", "acct", "Active", now),
	).Build()

	got, err := FindActiveRequest(context.Background(), c, "ns", "SnowflakeAccount", "acct")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.Name != "match" {
		t.Fatalf("expected 'match', got %+v", got)
	}
}

// SC-014: multiple Active candidates resolve to the earliest creationTimestamp.
func TestFindActiveRequest_EarliestWins(t *testing.T) {
	now := time.Now()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(
		newRequest("later", "ns", "SnowflakeAccount", "acct", "Active", now),
		newRequest("earlier", "ns", "SnowflakeAccount", "acct", "Active", now.Add(-time.Hour)),
	).Build()

	got, err := FindActiveRequest(context.Background(), c, "ns", "SnowflakeAccount", "acct")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.Name != "earlier" {
		t.Fatalf("expected 'earlier', got %+v", got)
	}
}

// erroringClient wraps a client.Client and fails every List call, to
// exercise FindActiveRequest's system-error wrapping (SC-016) — the fake
// client itself never fails.
type erroringClient struct {
	client.Client
}

func (e *erroringClient) List(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
	return errors.New("simulated API failure")
}

// SC-016: a Kubernetes API failure is wrapped as a system error.
func TestFindActiveRequest_ListError(t *testing.T) {
	c := &erroringClient{Client: fake.NewClientBuilder().WithScheme(testScheme(t)).Build()}

	_, err := FindActiveRequest(context.Background(), c, "ns", "SnowflakeAccount", "acct")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}
