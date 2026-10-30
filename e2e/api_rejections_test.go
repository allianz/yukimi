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
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/e2e/support"
)

// Group B: input the API server rejects because of the rules in
// the CRD (OpenAPI schema and CEL). Every case is a server-side dry run, so
// nothing is stored and the operator never sees it. Which rule rejected the
// object matters, because an object rejected for the wrong reason would make
// the case pass without testing anything; that is why the message is checked
// where the CRD gives the rule a message of its own.

// Messages of the rules in the SnowflakeAccount CRD.
const (
	msgNameCharset = "metadata.name must start with a lowercase letter"
	msgNameLength  = "metadata.name must be 55 characters or fewer"
	msgRoleBinding = "roleBindings must bind ACCOUNTADMIN"
	msgPattern     = "should match"
	msgEnum        = "Unsupported value"
	msgTooLong     = "Too long"
)

// dryRun submits the account to the API server without storing it.
func dryRun(ctx context.Context, account *v1alpha1.SnowflakeAccount) error {
	return env.Kube.Create(ctx, account, client.DryRunAll)
}

// requireInvalid fails unless err is an "Invalid" rejection of the API server
// whose message contains want (an empty want only requires the rejection).
func requireInvalid(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("the API server accepted the account, but it should have rejected it (expected a message with %q)", want)
	}
	if !apierrors.IsInvalid(err) {
		t.Fatalf("expected an Invalid rejection from the API server, got: %v", err)
	}
	if want != "" && !strings.Contains(err.Error(), want) {
		t.Fatalf("the rejection does not mention %q: %v", want, err)
	}
}

// rejected runs one scenario: build the account, submit it as a dry run, expect
// an Invalid rejection that contains want.
func rejected(t *testing.T, given string, build func() *v1alpha1.SnowflakeAccount, want string) {
	t.Helper()
	ctx := env.Ctx

	var account *v1alpha1.SnowflakeAccount
	var err error

	support.Given(t, given, func(t *testing.T) {
		account = build()
	})
	support.When(t, "it is submitted as a dry run, so nothing is stored", func(t *testing.T) {
		err = dryRun(ctx, account)
	})
	support.Then(t, "the API server rejects it as invalid and names the rule", func(t *testing.T) {
		requireInvalid(t, err, want)
	})
}

// validAccount is the starting point of every case: a name that is valid, in a
// region the operator knows.
func validAccount(name string) *v1alpha1.SnowflakeAccount {
	return support.NewAccount(name, env.Namespace, env.Region)
}

func TestB0_AValidAccountIsAccepted(t *testing.T) {
	ctx := env.Ctx

	var account *v1alpha1.SnowflakeAccount
	var err error

	support.Given(t, "an account with valid values", func(t *testing.T) {
		account = validAccount("valid-account")
	})
	support.When(t, "it is submitted as a dry run, so nothing is stored", func(t *testing.T) {
		err = dryRun(ctx, account)
	})
	support.Then(t, "the API server accepts it, so every rejection below is about the one value that was changed", func(t *testing.T) {
		if err != nil {
			t.Fatalf("a valid account was rejected: %v", err)
		}
	})
}

func TestB1_NonASCIINameIsRejected(t *testing.T) {
	rejected(t, "an account named in Japanese", func() *v1alpha1.SnowflakeAccount {
		return validAccount("テスト")
	}, msgNameCharset)
}

func TestB2_NameOverTheCRDLimitIsRejected(t *testing.T) {
	rejected(t, "an account name of 56 characters, one above the CRD limit", func() *v1alpha1.SnowflakeAccount {
		return validAccount("a" + strings.Repeat("b", 55))
	}, msgNameLength)
}

func TestB3_NameBoundariesAreRejected(t *testing.T) {
	cases := []struct {
		name string
		why  string
	}{
		{"1abc", "starts with a digit; the resolved Snowflake name must start with a letter"},
		{"Upper", "uppercase; Kubernetes names are lowercase and the Snowflake name is derived from it"},
		{"a_b", "underscore; the resolved name replaces '-' with '_', so '_' in the input could collide"},
		{"-abc", "starts with a dash"},
		{"abc-", "ends with a dash"},
		{"a--b", "repeated dash; the secret identifier forbids repeated separators"},
		{"a'; drop account x; --", "SQL-injection-like text is not a valid name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Log(tc.why)
			rejected(t, "an account named "+tc.name, func() *v1alpha1.SnowflakeAccount {
				return validAccount(tc.name)
			}, msgNameCharset)
		})
	}
}

func TestB4_RegionFormatIsRejected(t *testing.T) {
	for _, region := range []string{"EU-CENTRAL-1", "eu-central-1", "aws-", "aws-Europe"} {
		t.Run(region, func(t *testing.T) {
			rejected(t, "a region written as "+region, func() *v1alpha1.SnowflakeAccount {
				account := validAccount("region-format")
				account.Spec.Region = region
				return account
			}, msgPattern)
		})
	}
}

func TestB5_UnknownEnvironmentIsRejected(t *testing.T) {
	rejected(t, "an environment that is neither dev nor prod", func() *v1alpha1.SnowflakeAccount {
		account := validAccount("environment")
		account.Spec.Environment = "staging"
		return account
	}, msgEnum)
}

func TestB6_InvalidContactIsRejected(t *testing.T) {
	for _, contact := range []string{"nobody", "a b@example.com", "a@b", "@example.com"} {
		t.Run(contact, func(t *testing.T) {
			rejected(t, "a contact written as "+contact, func() *v1alpha1.SnowflakeAccount {
				account := validAccount("contact")
				account.Spec.Contact = contact
				return account
			}, msgPattern)
		})
	}
}

func TestB7_TooLongDescriptionIsRejected(t *testing.T) {
	rejected(t, "a description of 1025 characters, one above the limit", func() *v1alpha1.SnowflakeAccount {
		account := validAccount("description")
		account.Spec.Description = strings.Repeat("x", 1025)
		return account
	}, msgTooLong)
}

func TestB8_RoleBindingsWithoutAccountAdminAreRejected(t *testing.T) {
	rejected(t, "role bindings that bind SYSADMIN but not ACCOUNTADMIN", func() *v1alpha1.SnowflakeAccount {
		account := validAccount("role-bindings")
		account.Spec.IdentityIntegration.RoleBindings = map[string]string{"SYSADMIN": "E2E_GROUP"}
		return account
	}, msgRoleBinding)
}

func TestB9_EmptyRequiredFieldsAreRejected(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*v1alpha1.SnowflakeAccount)
	}{
		{"region", func(a *v1alpha1.SnowflakeAccount) { a.Spec.Region = "" }},
		{"environment", func(a *v1alpha1.SnowflakeAccount) { a.Spec.Environment = "" }},
		{"contact", func(a *v1alpha1.SnowflakeAccount) { a.Spec.Contact = "" }},
		{"role bindings", func(a *v1alpha1.SnowflakeAccount) { a.Spec.IdentityIntegration.RoleBindings = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Any rejection is enough here: which message the API server gives
			// for an empty value depends on the field.
			rejected(t, "an account with an empty "+tc.name, func() *v1alpha1.SnowflakeAccount {
				account := validAccount("required-field")
				tc.mutate(account)
				return account
			}, "")
		})
	}
}
