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

package secrets

import "testing"

// SC-014: Tenant paths include namespace, ordered org/namespace/account.
func TestTenantSecretPath_Format(t *testing.T) {
	path, err := tenantSecretPath("myorg", "team-a", "myaccount")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "snowflake/tenant/myorg/team-a/myaccount/platform-credentials"
	if path != want {
		t.Errorf("got %q, want %q", path, want)
	}
}

// SC-015: Org admin paths exclude namespace, ordered org/account.
func TestOrgAdminSecretPath_Format(t *testing.T) {
	path, err := orgAdminSecretPath("myorg", "myaccount")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "snowflake/org/myorg/myaccount/org-admin-credentials"
	if path != want {
		t.Errorf("got %q, want %q", path, want)
	}
}

// SC-013: Empty path components return error.
func TestTenantSecretPath_EmptyComponents(t *testing.T) {
	cases := []struct {
		org, namespace, account string
	}{
		{"", "ns", "acc"},
		{"org", "", "acc"},
		{"org", "ns", ""},
	}
	for _, c := range cases {
		_, err := tenantSecretPath(c.org, c.namespace, c.account)
		if err == nil {
			t.Errorf("expected error for empty component: org=%q namespace=%q account=%q", c.org, c.namespace, c.account)
		}
	}
}

// SC-013: Empty path components return error for org admin paths.
func TestOrgAdminSecretPath_EmptyComponents(t *testing.T) {
	cases := []struct {
		org, account string
	}{
		{"", "acc"},
		{"org", ""},
	}
	for _, c := range cases {
		_, err := orgAdminSecretPath(c.org, c.account)
		if err == nil {
			t.Errorf("expected error for empty component: org=%q account=%q", c.org, c.account)
		}
	}
}

// Path traversal characters are rejected.
func TestTenantSecretPath_TraversalRejected(t *testing.T) {
	cases := []struct {
		org, namespace, account string
	}{
		{"../etc", "ns", "acc"},
		{"org", "ns/evil", "acc"},
		{"org", "ns", "acc\x00null"},
	}
	for _, c := range cases {
		_, err := tenantSecretPath(c.org, c.namespace, c.account)
		if err == nil {
			t.Errorf("expected error for traversal attempt: %v", c)
		}
	}
}
