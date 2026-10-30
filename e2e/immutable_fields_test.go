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

package e2e

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/e2e/support"
)

// Cases B10 and B11: the immutability rules of the CRD. They
// change a stored account, always as a server-side dry run, so the stored
// account stays as it is.
//
// The stored account is in a region the operator does not know. The controller
// rejects such an account before it touches Snowflake or AWS (see C2), so the
// cases need no real account and do not depend on the lifecycle account of
// group A. The CRD rules look only at the spec, not at the status.

// storedAccount is the account the update cases change, created on first use.
var storedAccount *v1alpha1.SnowflakeAccount

func immutabilityFixture(ctx context.Context, t *testing.T) *v1alpha1.SnowflakeAccount {
	t.Helper()
	if storedAccount == nil {
		account := support.NewAccount("immutable-fields", env.Namespace, env.Cfg.UnknownRegion())
		apply(ctx, t, account)
		storedAccount = account
	}
	var got v1alpha1.SnowflakeAccount
	if err := env.Kube.Get(ctx, types.NamespacedName{Namespace: env.Namespace, Name: storedAccount.Name}, &got); err != nil {
		t.Fatal(err)
	}
	return &got
}

// dryRunChange submits the difference between before and account as a
// server-side dry-run patch. A patch carries no resourceVersion, so it is not
// rejected as a conflict when the controller touches the stored account
// meanwhile, which it does.
func dryRunChange(ctx context.Context, before, account *v1alpha1.SnowflakeAccount) error {
	return env.Kube.Patch(ctx, account, client.MergeFrom(before), client.DryRunAll)
}

func TestB10_RegionAndEnvironmentCannotBeChanged(t *testing.T) {
	ctx := env.Ctx

	cases := []struct {
		name   string
		change func(*v1alpha1.SnowflakeAccount)
		want   string
	}{
		{"region", func(a *v1alpha1.SnowflakeAccount) { a.Spec.Region = env.Region }, "region is immutable"},
		{"environment", func(a *v1alpha1.SnowflakeAccount) { a.Spec.Environment = "prod" }, "environment is immutable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var account *v1alpha1.SnowflakeAccount
			var err error

			support.Given(t, "a stored account", func(t *testing.T) {
				account = immutabilityFixture(ctx, t)
			})
			support.When(t, "its "+c.name+" is changed in a dry-run patch", func(t *testing.T) {
				before := account.DeepCopy()
				c.change(account)
				err = dryRunChange(ctx, before, account)
			})
			support.Then(t, "the API server rejects it and names the rule", func(t *testing.T) {
				requireInvalid(t, err, c.want)
			})
		})
	}
}

// This is the CRD half of the gap. That the controller never updates the
// Snowflake comment (the drift) is case B12, which needs a real account.
func TestB11_ADescriptionCanBeAddedLaterButThenNeverChanged(t *testing.T) {
	ctx := env.Ctx

	var account *v1alpha1.SnowflakeAccount
	var err error

	support.Given(t, "a stored account that has no description", func(t *testing.T) {
		account = immutabilityFixture(ctx, t)
		if account.Spec.Description != "" {
			t.Fatalf("the fixture account already has a description: %q", account.Spec.Description)
		}
	})
	support.When(t, "a description is added in a dry-run patch", func(t *testing.T) {
		before := account.DeepCopy()
		account.Spec.Description = "added later"
		err = dryRunChange(ctx, before, account)
	})
	// This documents a gap of the CRD, not a wanted behavior: the rule
	// "description is immutable" is not evaluated when the old value is absent.
	// When the gap is closed, this case flips:
	// change the expectation to a rejection.
	support.Then(t, "the API server accepts it (known gap: the immutability rule does not apply to an absent old value)", func(t *testing.T) {
		if err != nil {
			t.Fatalf("the update was rejected, so the gap is closed; change this case to expect the rejection: %v", err)
		}
	})
}
