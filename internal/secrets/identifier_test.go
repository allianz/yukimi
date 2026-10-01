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

import (
	"strings"
	"testing"

	"github.com/allianz/yukimi/internal/errors"
)

// SC-003: NewTenantIdentifier constructs the exact expected identifier from its four inputs.
func TestNewTenantIdentifier_BuildsExpectedIdentifier(t *testing.T) {
	id, err := NewTenantIdentifier("my_org", "finance", "analytics-team-eu")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "snowflake/tenant/my_org/finance/analytics-team-eu/platform-credentials"
	if id.String() != want {
		t.Errorf("got %q, want %q", id.String(), want)
	}
}

// SC-004: NewOrgAdminIdentifier constructs the exact expected identifier from its two inputs.
func TestNewOrgAdminIdentifier_BuildsExpectedIdentifier(t *testing.T) {
	id, err := NewOrgAdminIdentifier("my_org", "my_org_admin_account")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "snowflake/org/my_org/my_org_admin_account/org-admin-credentials"
	if id.String() != want {
		t.Errorf("got %q, want %q", id.String(), want)
	}
}

// SC-005: NewTenantIdentifier rejects an empty, '/'-containing, '.'-containing,
// '..'-containing, or out-of-class segment in any of its three positions.
func TestNewTenantIdentifier_RejectsInvalidSegments(t *testing.T) {
	invalid := []string{"", "team/a", "team.a", "..", "team a", "team@a"}
	for _, bad := range invalid {
		if _, err := NewTenantIdentifier(bad, "finance", "analytics-team-eu"); err == nil || !errors.IsUserError(err) {
			t.Errorf("org=%q: expected user error, got %v", bad, err)
		}
		if _, err := NewTenantIdentifier("my_org", bad, "analytics-team-eu"); err == nil || !errors.IsUserError(err) {
			t.Errorf("namespace=%q: expected user error, got %v", bad, err)
		}
		if _, err := NewTenantIdentifier("my_org", "finance", bad); err == nil || !errors.IsUserError(err) {
			t.Errorf("accountName=%q: expected user error, got %v", bad, err)
		}
	}
}

// SC-005: NewOrgAdminIdentifier rejects the same invalid forms in both of its positions.
func TestNewOrgAdminIdentifier_RejectsInvalidSegments(t *testing.T) {
	invalid := []string{"", "org/admin", "org.admin", "..", "org admin", "org@admin"}
	for _, bad := range invalid {
		if _, err := NewOrgAdminIdentifier(bad, "my_org_admin_account"); err == nil || !errors.IsUserError(err) {
			t.Errorf("org=%q: expected user error, got %v", bad, err)
		}
		if _, err := NewOrgAdminIdentifier("my_org", bad); err == nil || !errors.IsUserError(err) {
			t.Errorf("orgAdminAccount=%q: expected user error, got %v", bad, err)
		}
	}
}

// SC-005: A failed construction returns the zero-value Identifier alongside the error.
func TestNewTenantIdentifier_ReturnsZeroValueOnError(t *testing.T) {
	id, err := NewTenantIdentifier("", "finance", "analytics-team-eu")
	if err == nil {
		t.Fatal("expected error")
	}
	if id.String() != "" {
		t.Errorf("expected zero-value Identifier, got %q", id.String())
	}
}

// Security consideration: Identifier.String() contains only the identifiers
// that make it up — never anything else, and in particular never credential
// material, since Identifier never holds credential bytes in the first place.
func TestIdentifierString_ContainsOnlyIdentifiers(t *testing.T) {
	id, err := NewTenantIdentifier("my_org", "finance", "analytics-team-eu")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := id.String()
	for _, want := range []string{"my_org", "finance", "analytics-team-eu", "snowflake/tenant", "platform-credentials"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing expected substring %q", got, want)
		}
	}
	if strings.Count(got, "/") != 5 {
		t.Errorf("String() = %q, want exactly 5 '/' separators (no extra content)", got)
	}
}
