//go:build e2e

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

package support

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/account/tenant"
)

func TestPlanForceClean(t *testing.T) {
	old := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	young := metav1.NewTime(time.Now())
	ns := func(name string, created metav1.Time) *corev1.Namespace {
		return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: created}}
	}
	account := func(namespace, name string) client.Object {
		return &v1alpha1.SnowflakeAccount{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	}
	kube := fake.NewClientBuilder().WithScheme(Scheme()).WithObjects(
		ns("e2e-20261116134411-dfde", old),
		ns("e2e-20261116150000", young),
		ns("e2e-notarun", old), // does not match the run pattern
		ns("production", old),
		account("e2e-20261116134411-dfde", "b-account"),
		account("e2e-20261116134411-dfde", "a-account"),
		account("production", "tenant"),
	).Build()

	plan, err := planForceClean(context.Background(), kube, "org", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 || plan[0].Name != "e2e-20261116134411-dfde" || plan[1].Name != "e2e-20261116150000" {
		t.Fatalf("only the run namespaces, in order, are planned: %+v", plan)
	}
	if got := plan[0].Accounts; len(got) != 2 || got[0].Name != "a-account" || got[1].Name != "b-account" {
		t.Fatalf("accounts are sorted by name: %+v", got)
	}
	if got, want := plan[0].Accounts[0].ResolvedName, tenant.ResolveName("a-account", "e2e-20261116134411-dfde"); got != want {
		t.Errorf("resolved name = %q, want %q", got, want)
	}
	if plan[0].Accounts[0].SecretID.String() == "" {
		t.Error("the secret identifier is empty")
	}

	plan, err = planForceClean(context.Background(), kube, "org", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || plan[0].Name != "e2e-20261116134411-dfde" {
		t.Fatalf("older-than spares the young namespace: %+v", plan)
	}
}
