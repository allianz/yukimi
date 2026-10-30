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

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/e2e/support"
	"github.com/allianz/yukimi/internal/account/tenant"
)

// Group A: the lifecycle of a real account. These cases create
// real Snowflake accounts in the test organization. A new account takes minutes
// to become ready (the operator waits out a creation grace period before it
// connects), so the waits are long. Each case calls t.Parallel and uses its own
// fixture (support.Fixture), so the accounts are created at the same time.

const (
	// readyTimeout covers the creation grace period (5 minutes by default), the
	// poll interval (1 minute) and the time Snowflake needs to create the account.
	readyTimeout = 20 * time.Minute
	// deleteTimeout covers DROP ACCOUNT and the removal of the finalizer.
	deleteTimeout = 10 * time.Minute
)

// lifecycleName is a name with exactly the longest length the operator accepts
// in this namespace, so the account also proves that limit is correct.
func lifecycleName() string {
	const prefix = "lifecycle-"
	return prefix + strings.Repeat("x", env.Cfg.MaxAccountNameLength(env.Namespace)-len(prefix))
}

// lifecycleFixture is the account of A1.
func lifecycleFixture() *support.Fixture {
	return env.Fixture("lifecycle", func() *v1alpha1.SnowflakeAccount {
		return support.NewAccount(lifecycleName(), env.Namespace, env.Region)
	})
}

// deleteFixture is the account A4 deletes.
func deleteFixture() *support.Fixture {
	return env.Fixture("delete", func() *v1alpha1.SnowflakeAccount {
		return support.NewAccount("delete-flow", env.Namespace, env.Region)
	})
}

func TestA1_AValidAccountBecomesReady(t *testing.T) {
	t.Parallel()
	ctx := env.Ctx
	fixture := lifecycleFixture()
	name := lifecycleName()
	resolved := tenant.ResolveName(name, env.Namespace)

	var ready *v1alpha1.SnowflakeAccount

	support.Given(t, fmt.Sprintf("a valid account whose name has the maximum length of %d characters", len(name)), func(t *testing.T) {
		if len(name) != env.Cfg.MaxAccountNameLength(env.Namespace) {
			t.Fatalf("the name has %d characters, the maximum is %d", len(name), env.Cfg.MaxAccountNameLength(env.Namespace))
		}
	})

	support.When(t, "it is applied", func(t *testing.T) {
		if _, err := fixture.Start(ctx); err != nil {
			t.Fatal(err)
		}
	})

	support.Then(t, "it becomes ready: Ready and Synced are both true", func(t *testing.T) {
		var err error
		if ready, err = fixture.Ready(ctx, readyTimeout); err != nil {
			t.Fatal(err)
		}
	})

	support.Then(t, "its status names the account: locator, resolved name and URL are set", func(t *testing.T) {
		if ready.Status.AccountLocator == "" {
			t.Error("status.accountLocator is empty")
		}
		if ready.Status.AccountName != resolved {
			t.Errorf("status.accountName = %q, want the resolved name %q", ready.Status.AccountName, resolved)
		}
		if ready.Status.AccountURL == "" {
			t.Error("status.accountUrl is empty")
		}
	})

	support.Then(t, "Snowflake lists the account under its resolved name", func(t *testing.T) {
		row, err := env.AccountRow(ctx, resolved)
		if err != nil {
			t.Fatalf("looking the account up in Snowflake: %v", err)
		}
		if !row.Found() {
			t.Fatalf("SHOW ACCOUNTS does not list %q", resolved)
		}
		if locator, ok := row.StringValue("account_locator"); ok && !strings.EqualFold(locator, ready.Status.AccountLocator) {
			t.Errorf("Snowflake says the locator is %q, the status says %q", locator, ready.Status.AccountLocator)
		}
	})

	support.Then(t, "its credential secret exists", func(t *testing.T) {
		info, err := env.SecretInfo(ctx, name)
		if err != nil {
			t.Fatalf("looking the secret up: %v", err)
		}
		if !info.Exists {
			t.Errorf("no credential secret exists for %s", name)
		}
	})
}

func TestA4_DeletingAnAccountRemovesItEverywhere(t *testing.T) {
	t.Parallel()
	ctx := env.Ctx
	fixture := deleteFixture()
	var name, resolved string
	var key types.NamespacedName

	support.Given(t, "an account that is ready and exists in Snowflake", func(t *testing.T) {
		account, err := fixture.Ready(ctx, readyTimeout)
		if err != nil {
			t.Fatal(err)
		}
		name = account.Name
		key = types.NamespacedName{Namespace: env.Namespace, Name: name}
		resolved = tenant.ResolveName(name, env.Namespace)
		row, err := env.AccountRow(ctx, resolved)
		if err != nil {
			t.Fatalf("looking the account up in Snowflake: %v", err)
		}
		if !row.Found() {
			t.Fatalf("the account %q is not listed by Snowflake, so there is nothing to delete", resolved)
		}
	})

	support.When(t, "a deletion permit is created and the account is deleted", func(t *testing.T) {
		if err := fixture.Delete(ctx, readyTimeout, deleteTimeout); err != nil {
			t.Fatal(err)
		}
	})

	support.Then(t, "the Kubernetes object is gone", func(t *testing.T) {
		support.Eventually(ctx, t, deleteTimeout, 10*time.Second, "the account to be gone from Kubernetes", func(ctx context.Context) (bool, error) {
			var got v1alpha1.SnowflakeAccount
			err := env.Kube.Get(ctx, key, &got)
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			return false, fmt.Errorf("still present: %s", support.DescribeConditions(&got))
		})
	})

	support.Then(t, "Snowflake no longer lists the account", func(t *testing.T) {
		row, err := env.AccountRow(ctx, resolved)
		if err != nil {
			t.Fatalf("looking the account up in Snowflake: %v", err)
		}
		if row.Found() {
			t.Errorf("SHOW ACCOUNTS still lists %q", resolved)
		}
	})

	support.Then(t, "its credential secret is scheduled for deletion", func(t *testing.T) {
		info, err := env.SecretInfo(ctx, name)
		if err != nil {
			t.Fatalf("looking the secret up: %v", err)
		}
		if info.Exists && !info.ScheduledForDeletion {
			t.Errorf("the credential secret of %s still exists and is not scheduled for deletion", name)
		}
	})
}

// settleTime is how long A3 watches a ready account: three poll intervals of
// one minute, so the operator reconciles it several times.
const settleTime = 3 * time.Minute

func TestA3_ASettledAccountCausesNoFurtherChange(t *testing.T) {
	t.Parallel()
	ctx := env.Ctx
	fixture := lifecycleFixture()
	resolved := tenant.ResolveName(lifecycleName(), env.Namespace)

	var before *v1alpha1.SnowflakeAccount

	support.Given(t, "a ready account", func(t *testing.T) {
		var err error
		if before, err = fixture.Ready(ctx, readyTimeout); err != nil {
			t.Fatal(err)
		}
		if before.Status.AccountCreatedAt == nil {
			t.Fatal("status.accountCreatedAt is not set")
		}
	})

	support.When(t, fmt.Sprintf("the operator reconciles it for %s", settleTime), func(t *testing.T) {
		select {
		case <-time.After(settleTime):
		case <-ctx.Done():
			t.Fatal("interrupted")
		}
	})

	support.Then(t, "the locator and the creation time are unchanged, so no second account was created", func(t *testing.T) {
		var got v1alpha1.SnowflakeAccount
		if err := env.Kube.Get(ctx, types.NamespacedName{Namespace: env.Namespace, Name: before.Name}, &got); err != nil {
			t.Fatal(err)
		}
		if got.Status.AccountLocator != before.Status.AccountLocator {
			t.Errorf("the locator changed from %q to %q", before.Status.AccountLocator, got.Status.AccountLocator)
		}
		if got.Status.AccountCreatedAt == nil || !got.Status.AccountCreatedAt.Equal(before.Status.AccountCreatedAt) {
			t.Errorf("status.accountCreatedAt changed from %v to %v", before.Status.AccountCreatedAt, got.Status.AccountCreatedAt)
		}
		row, err := env.AccountRow(ctx, resolved)
		if err != nil {
			t.Fatalf("looking the account up in Snowflake: %v", err)
		}
		if !row.Found() {
			t.Errorf("Snowflake no longer lists %q", resolved)
		}
	})
}

func TestB12_TheSnowflakeCommentIsNotUpdatedFromTheDescription(t *testing.T) {
	t.Parallel()
	ctx := env.Ctx
	fixture := lifecycleFixture()
	resolved := tenant.ResolveName(lifecycleName(), env.Namespace)
	const description = "added later by the e2e suite"

	var account *v1alpha1.SnowflakeAccount

	support.Given(t, "a ready account that was created without a description", func(t *testing.T) {
		var err error
		if account, err = fixture.Ready(ctx, readyTimeout); err != nil {
			t.Fatal(err)
		}
		if account.Spec.Description != "" {
			t.Fatalf("the account has a description already: %q", account.Spec.Description)
		}
	})

	var generation int64
	support.When(t, "a description is added with a real update and the operator has reconciled it", func(t *testing.T) {
		current := account.DeepCopy()
		current.Spec.Description = description
		if err := env.Kube.Patch(ctx, current, client.MergeFrom(account)); err != nil {
			t.Fatalf("the description could not be added: %v", err)
		}
		generation = current.Generation
		key := types.NamespacedName{Namespace: env.Namespace, Name: account.Name}
		support.Eventually(ctx, t, 5*time.Minute, 5*time.Second, "the operator to reconcile the new generation", func(ctx context.Context) (bool, error) {
			var got v1alpha1.SnowflakeAccount
			if err := env.Kube.Get(ctx, key, &got); err != nil {
				return false, err
			}
			return got.GetCondition(xpv1.TypeSynced).ObservedGeneration >= generation, nil
		})
	})

	// This documents a gap, not a wanted behavior: the description is only
	// written at CREATE ACCOUNT, so a description added later never reaches the
	// Snowflake comment. When the gap is closed, this case flips: change the
	// expectation to comment == description.
	support.Then(t, "the Snowflake comment still does not hold the description (known gap: spec and Snowflake drift)", func(t *testing.T) {
		row, err := env.AccountRow(ctx, resolved)
		if err != nil {
			t.Fatalf("looking the account up in Snowflake: %v", err)
		}
		if !row.Found() {
			t.Fatalf("Snowflake does not list %q", resolved)
		}
		comment, _ := row.StringValue("comment")
		if comment == description {
			t.Fatalf("the comment now holds the description, so the gap is closed; change this case to expect it")
		}
	})
}

func TestA5_RecreatingADeletedNameIsRefused(t *testing.T) {
	t.Parallel()
	ctx := env.Ctx
	fixture := deleteFixture()
	var again *v1alpha1.SnowflakeAccount

	support.Given(t, "an account that was deleted a moment ago", func(t *testing.T) {
		if err := fixture.Delete(ctx, readyTimeout, deleteTimeout); err != nil {
			t.Fatal(err)
		}
	})

	support.When(t, "an account with the same name is applied again", func(t *testing.T) {
		again = support.NewAccount("delete-flow", env.Namespace, env.Region)
		apply(ctx, t, again)
	})

	support.Then(t, "the controller refuses it with a message, and no account is created", func(t *testing.T) {
		key := types.NamespacedName{Namespace: env.Namespace, Name: again.Name}
		var message string
		support.Eventually(ctx, t, 4*time.Minute, 5*time.Second, "a refusal in the conditions", func(ctx context.Context) (bool, error) {
			var got v1alpha1.SnowflakeAccount
			if err := env.Kube.Get(ctx, key, &got); err != nil {
				return false, err
			}
			if got.Status.AccountLocator != "" {
				return false, fmt.Errorf("an account was created again (locator %s)", got.Status.AccountLocator)
			}
			for _, c := range got.Status.Conditions {
				if c.Status == corev1.ConditionFalse && c.Message != "" {
					message = c.Message
					return true, nil
				}
			}
			return false, fmt.Errorf("no refusal yet: %s", support.DescribeConditions(&got))
		})
		t.Logf("the controller says: %s", message)
		if !strings.Contains(message, "recovery window") {
			t.Errorf("the refusal does not explain the recovery window: %s", message)
		}
	})
}

// contactFixture is the account A2 changes.
func contactFixture() *support.Fixture {
	return env.Fixture("contact", func() *v1alpha1.SnowflakeAccount {
		return support.NewAccount("contact-change", env.Namespace, env.Region)
	})
}

func TestA2_AContactChangeReachesSnowflake(t *testing.T) {
	t.Parallel()
	ctx := env.Ctx
	fixture := contactFixture()
	const changed = "e2e-changed@example.com"

	var account *v1alpha1.SnowflakeAccount
	waitForEmail := func(t *testing.T, want string) {
		t.Helper()
		support.Eventually(ctx, t, 5*time.Minute, 10*time.Second, "the platform user's email to be "+want, func(ctx context.Context) (bool, error) {
			var current v1alpha1.SnowflakeAccount
			if err := env.Kube.Get(ctx, types.NamespacedName{Namespace: env.Namespace, Name: account.Name}, &current); err != nil {
				return false, err
			}
			email, err := env.PlatformEmail(ctx, &current)
			if err != nil {
				return false, err
			}
			if email != want {
				return false, fmt.Errorf("the email is %q", email)
			}
			return true, nil
		})
	}

	support.Given(t, "a ready account whose platform user has the contact as its email", func(t *testing.T) {
		var err error
		if account, err = fixture.Ready(ctx, readyTimeout); err != nil {
			t.Fatal(err)
		}
		waitForEmail(t, account.Spec.Contact)
	})

	support.When(t, "the contact is changed", func(t *testing.T) {
		updated := account.DeepCopy()
		updated.Spec.Contact = changed
		if err := env.Kube.Patch(ctx, updated, client.MergeFrom(account)); err != nil {
			t.Fatalf("the contact could not be changed: %v", err)
		}
	})

	support.Then(t, "the platform user's email follows", func(t *testing.T) {
		waitForEmail(t, changed)
	})

	support.Then(t, "the account is still ready", func(t *testing.T) {
		var got v1alpha1.SnowflakeAccount
		if err := env.Kube.Get(ctx, types.NamespacedName{Namespace: env.Namespace, Name: account.Name}, &got); err != nil {
			t.Fatal(err)
		}
		if got.GetCondition(xpv1.TypeReady).Status != corev1.ConditionTrue {
			t.Errorf("the account is not ready: %s", support.DescribeConditions(&got))
		}
	})
}
