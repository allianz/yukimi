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

// SC-003: NewTenantPath constructs the exact expected path from its four inputs.
func TestNewTenantPath_BuildsExpectedPath(t *testing.T) {
	p, err := NewTenantPath("my_org", "finance", "analytics-team-eu")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "snowflake/tenant/my_org/finance/analytics-team-eu/platform-credentials"
	if p.String() != want {
		t.Errorf("got %q, want %q", p.String(), want)
	}
}

// SC-004: NewOrgAdminPath constructs the exact expected path from its two inputs.
func TestNewOrgAdminPath_BuildsExpectedPath(t *testing.T) {
	p, err := NewOrgAdminPath("my_org", "my_org_admin_account")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "snowflake/org/my_org/my_org_admin_account/org-admin-credentials"
	if p.String() != want {
		t.Errorf("got %q, want %q", p.String(), want)
	}
}

// SC-005: NewTenantPath rejects an empty, '/'-containing, '.'-containing,
// '..'-containing, or out-of-class segment in any of its three positions.
func TestNewTenantPath_RejectsInvalidSegments(t *testing.T) {
	invalid := []string{"", "team/a", "team.a", "..", "team a", "team@a"}
	for _, bad := range invalid {
		if _, err := NewTenantPath(bad, "finance", "analytics-team-eu"); err == nil || !errors.IsUserError(err) {
			t.Errorf("org=%q: expected user error, got %v", bad, err)
		}
		if _, err := NewTenantPath("my_org", bad, "analytics-team-eu"); err == nil || !errors.IsUserError(err) {
			t.Errorf("namespace=%q: expected user error, got %v", bad, err)
		}
		if _, err := NewTenantPath("my_org", "finance", bad); err == nil || !errors.IsUserError(err) {
			t.Errorf("accountName=%q: expected user error, got %v", bad, err)
		}
	}
}

// SC-005: NewOrgAdminPath rejects the same invalid forms in both of its positions.
func TestNewOrgAdminPath_RejectsInvalidSegments(t *testing.T) {
	invalid := []string{"", "org/admin", "org.admin", "..", "org admin", "org@admin"}
	for _, bad := range invalid {
		if _, err := NewOrgAdminPath(bad, "my_org_admin_account"); err == nil || !errors.IsUserError(err) {
			t.Errorf("org=%q: expected user error, got %v", bad, err)
		}
		if _, err := NewOrgAdminPath("my_org", bad); err == nil || !errors.IsUserError(err) {
			t.Errorf("orgAdminAccount=%q: expected user error, got %v", bad, err)
		}
	}
}

// SC-005: A failed construction returns the zero-value Path alongside the error.
func TestNewTenantPath_ReturnsZeroValueOnError(t *testing.T) {
	p, err := NewTenantPath("", "finance", "analytics-team-eu")
	if err == nil {
		t.Fatal("expected error")
	}
	if p.String() != "" {
		t.Errorf("expected zero-value Path, got %q", p.String())
	}
}

// Security consideration: Path.String() contains only the identifiers that
// make up the path — never anything else, and in particular never credential
// material, since Path never holds credential bytes in the first place.
func TestPathString_ContainsOnlyIdentifiers(t *testing.T) {
	p, err := NewTenantPath("my_org", "finance", "analytics-team-eu")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := p.String()
	for _, want := range []string{"my_org", "finance", "analytics-team-eu", "snowflake/tenant", "platform-credentials"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing expected substring %q", got, want)
		}
	}
	if strings.Count(got, "/") != 5 {
		t.Errorf("String() = %q, want exactly 5 '/' separators (no extra content)", got)
	}
}
