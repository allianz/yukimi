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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/e2e/support"
	"github.com/allianz/yukimi/internal/account/tenant"
)

// Group D: inputs that only Snowflake can judge. The CRD pattern
// cannot tell what Snowflake accepts, so every case creates a real account and
// reads back what Snowflake stored. The existing integration tests of the account
// module cover the same kind of input directly against Snowflake; these cases go
// through the whole chain: API server, controller, operator, Snowflake.
//
// Each case has its own fixture, so the accounts are created at the same time.

// storedComment returns the comment Snowflake holds for an account.
func storedComment(ctx context.Context, t *testing.T, name string) string {
	t.Helper()
	resolved := tenant.ResolveName(name, env.Namespace)
	row, err := env.AccountRow(ctx, resolved)
	if err != nil {
		t.Fatalf("looking the account up in Snowflake: %v", err)
	}
	if !row.Found() {
		t.Fatalf("Snowflake does not list %q", resolved)
	}
	comment, _ := row.StringValue("comment")
	return comment
}

// descriptionIsStored creates an account with the description and checks the
// comment Snowflake holds for it.
func descriptionIsStored(t *testing.T, fixtureName, accountName, description, want string) {
	t.Helper()
	ctx := env.Ctx
	fixture := env.Fixture(fixtureName, func() *v1alpha1.SnowflakeAccount {
		account := support.NewAccount(accountName, env.Namespace, env.Region)
		account.Spec.Description = description
		return account
	})

	support.Given(t, fmt.Sprintf("an account whose description has %d characters", len([]rune(description))), func(t *testing.T) {})

	support.When(t, "it is applied and becomes ready", func(t *testing.T) {
		if _, err := fixture.Ready(ctx, readyTimeout); err != nil {
			t.Fatal(err)
		}
	})

	support.Then(t, "the comment Snowflake holds is the expected one", func(t *testing.T) {
		got := storedComment(ctx, t, accountName)
		t.Logf("sent:     %q", description)
		t.Logf("stored:   %q", got)
		if got != want {
			t.Errorf("stored comment = %q, want %q", got, want)
		}
	})
}

func TestD1_JapaneseAndSQLInjectionLikeDescriptionIsStoredAsText(t *testing.T) {
	t.Parallel()
	const description = "テストアカウント 日本語 x'); SELECT CURRENT_USER(); --"
	descriptionIsStored(t, "d1-text", "d1-text", description, description)
}

func TestD2_QuotesAndBackslashesAreInterpretedByTheBoundParameter(t *testing.T) {
	t.Parallel()
	// Snowflake interprets backslash escapes and doubled quotes in a bound
	// string (specs/005-statement-execution.md), so what is stored is not what
	// was sent. This case records that behavior: a doubled quote becomes one,
	// \n becomes a newline, and \\ becomes one backslash.
	const sent = `quote a''b, newline a\nb, backslash a\\b`
	const want = "quote a'b, newline a\nb, backslash a\\b"
	descriptionIsStored(t, "d2-escapes", "d2-escapes", sent, want)
}

func TestD4_ADescriptionOfTheMaximumLengthIsStoredCompletely(t *testing.T) {
	t.Parallel()
	description := strings.Repeat("d", 1024)
	descriptionIsStored(t, "d4-long", "d4-long", description, description)
}

func TestD3_UnicodeInTheContactEndsInAKnownOutcome(t *testing.T) {
	t.Parallel()
	ctx := env.Ctx
	const name = "d3-contact"
	fixture := env.Fixture(name, func() *v1alpha1.SnowflakeAccount {
		account := support.NewAccount(name, env.Namespace, env.Region)
		account.Spec.Contact = "日本@example.com"
		return account
	})
	key := types.NamespacedName{Namespace: env.Namespace, Name: name}

	support.Given(t, "a contact with Japanese characters, which the CRD pattern accepts", func(t *testing.T) {})

	var outcome, message string
	support.When(t, "the account is applied and the operator either creates it or fails", func(t *testing.T) {
		if _, err := fixture.Start(ctx); err != nil {
			t.Fatal(err)
		}
		support.Eventually(ctx, t, readyTimeout, 10*time.Second, "the account to be created or to fail", func(ctx context.Context) (bool, error) {
			var got v1alpha1.SnowflakeAccount
			if err := env.Kube.Get(ctx, key, &got); err != nil {
				return false, err
			}
			ready := got.GetCondition("Ready").Status == corev1.ConditionTrue
			synced := got.GetCondition("Synced")
			switch {
			case ready && synced.Status == corev1.ConditionTrue:
				outcome = "created"
				return true, nil
			case synced.Status == corev1.ConditionFalse && synced.Message != "":
				outcome, message = "failed", synced.Message
				return true, nil
			}
			return false, fmt.Errorf("neither created nor failed yet: %s", support.DescribeConditions(&got))
		})
	})

	// The outcome is not decided by the operator but by what Snowflake accepts, so
	// this case records it. A failure has to be readable, and it must not leave a
	// Snowflake account behind.
	support.Then(t, "the outcome is known and, if it is a failure, readable and without a Snowflake account", func(t *testing.T) {
		t.Logf("outcome: %s %s", outcome, message)
		if outcome == "failed" {
			resolved := tenant.ResolveName(name, env.Namespace)
			row, err := env.AccountRow(ctx, resolved)
			if err != nil {
				t.Fatalf("looking the account up in Snowflake: %v", err)
			}
			if row.Found() {
				t.Errorf("the account failed, but Snowflake lists %q", resolved)
			}
			// Whether a credential secret stays behind is case E1.
			if info, err := env.SecretInfo(ctx, name); err == nil {
				t.Logf("credential secret exists: %v", info.Exists)
			}
		}
	})
}
