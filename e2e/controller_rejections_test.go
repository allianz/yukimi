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
	"fmt"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/e2e/support"
)

// Group C: input the API server accepts and the controller
// rejects. There is no admission webhook, so these checks only exist in the
// controller. Nothing may be created by any of them.

// crdMaxNameLength is the longest metadata.name the CRD accepts. The operator
// is stricter, and names between the two limits reach the controller.
const crdMaxNameLength = 55

// apply creates the account and fails the test if the API server rejects it:
// the cases in this file are about what happens after the API server accepted it.
func apply(ctx context.Context, t *testing.T, account *v1alpha1.SnowflakeAccount) {
	t.Helper()
	if err := env.Kube.Create(ctx, account); err != nil {
		t.Fatalf("the API server rejected the account, but this case is about the controller: %v", err)
	}
}

// awaitCondition waits until a condition of the account contains the text.
func awaitCondition(ctx context.Context, t *testing.T, account *v1alpha1.SnowflakeAccount, text string) {
	t.Helper()
	key := types.NamespacedName{Namespace: account.Namespace, Name: account.Name}
	support.Eventually(ctx, t, 2*time.Minute, 2*time.Second, fmt.Sprintf("a condition that contains %q", text),
		func(ctx context.Context) (bool, error) {
			var got v1alpha1.SnowflakeAccount
			if err := env.Kube.Get(ctx, key, &got); err != nil {
				return false, err
			}
			for _, c := range got.Status.Conditions {
				if strings.Contains(c.Message, text) {
					return true, nil
				}
			}
			return false, nil
		})
}

// assertNothingCreated checks that the rejected account has no Snowflake
// account (no locator) and no credential secret.
func assertNothingCreated(ctx context.Context, t *testing.T, account *v1alpha1.SnowflakeAccount) {
	t.Helper()
	var got v1alpha1.SnowflakeAccount
	if err := env.Kube.Get(ctx, types.NamespacedName{Namespace: account.Namespace, Name: account.Name}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.AccountLocator != "" {
		t.Errorf("an account locator was recorded (%s), so a Snowflake account was created", got.Status.AccountLocator)
	}
	exists, err := env.SecretExists(ctx, account.Name)
	if err != nil {
		t.Fatalf("checking for the credential secret: %v", err)
	}
	if exists {
		t.Errorf("a credential secret exists for %s, but the account was rejected before anything is created", account.Name)
	}
}

func TestC1_NameAboveTheOperatorLimitIsRejectedByTheController(t *testing.T) {
	ctx := env.Ctx
	limit := env.Cfg.MaxAccountNameLength(env.Namespace)
	if limit+1 > crdMaxNameLength {
		t.Skipf("the operator limit (%d) is not below the CRD limit (%d) for organization %q, so no name can pass the API server and fail the controller",
			limit, crdMaxNameLength, env.Cfg.Org())
	}
	name := "c1-" + strings.Repeat("x", limit+1-len("c1-"))

	var account *v1alpha1.SnowflakeAccount

	support.Given(t, fmt.Sprintf("a name of %d characters, one above the operator limit of %d, which the CRD still accepts", len(name), limit), func(t *testing.T) {
		account = support.NewAccount(name, env.Namespace, env.Region)
	})

	support.When(t, "it is applied", func(t *testing.T) {
		apply(ctx, t, account)
	})

	support.Then(t, "the controller says how many characters are allowed", func(t *testing.T) {
		awaitCondition(ctx, t, account, fmt.Sprintf("account name must be %d characters or fewer", limit))
	})

	support.Then(t, "nothing was created: no account locator and no credential secret", func(t *testing.T) {
		assertNothingCreated(ctx, t, account)
	})
}

func TestC2_UnknownRegionIsRejectedByTheController(t *testing.T) {
	ctx := env.Ctx

	var account *v1alpha1.SnowflakeAccount

	support.Given(t, "an account in a region that is not in the operator configuration", func(t *testing.T) {
		account = support.NewAccount("unknown-region", env.Namespace, env.Cfg.UnknownRegion())
	})

	support.When(t, "it is applied", func(t *testing.T) {
		apply(ctx, t, account)
	})

	support.Then(t, "the controller reports that the region is not available", func(t *testing.T) {
		awaitCondition(ctx, t, account, "is not yet available")
	})

	support.Then(t, "nothing was created: no account locator and no credential secret", func(t *testing.T) {
		assertNothingCreated(ctx, t, account)
	})
}

func TestC3_AnAccountThatNeverGotALocatorCanBeDeleted(t *testing.T) {
	ctx := env.Ctx
	const name = "never-located"
	key := types.NamespacedName{Namespace: env.Namespace, Name: name}

	var account *v1alpha1.SnowflakeAccount

	support.Given(t, "an account the controller has rejected, so it has no account locator", func(t *testing.T) {
		account = support.NewAccount(name, env.Namespace, env.Cfg.UnknownRegion())
		apply(ctx, t, account)
		awaitCondition(ctx, t, account, "is not yet available")
	})

	support.When(t, "it is deleted, with a deletion permit created first", func(t *testing.T) {
		if err := env.DeleteAccount(ctx, name); err != nil {
			t.Fatal(err)
		}
	})

	support.Then(t, "the account disappears", func(t *testing.T) {
		support.Eventually(ctx, t, 2*time.Minute, 2*time.Second, "the account to be gone",
			func(ctx context.Context) (bool, error) {
				var got v1alpha1.SnowflakeAccount
				err := env.Kube.Get(ctx, key, &got)
				if apierrors.IsNotFound(err) {
					return true, nil
				}
				return false, err
			})
	})
}
