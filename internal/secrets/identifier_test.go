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
	want := "yk-my-org--finance--analytics-team-eu"
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
	want := "yk-orgadmin--my-org--my-org-admin-account"
	if id.String() != want {
		t.Errorf("got %q, want %q", id.String(), want)
	}
}

// SC-003, SC-004: both constructors lowercase every segment. Snowflake org
// and account names are case-insensitive, so spellings that differ only in
// case name the same org and must reach the same credential.
func TestIdentifiers_AreLowercase(t *testing.T) {
	tenant, err := NewTenantIdentifier("My_Org", "finance", "analytics-team-eu")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "yk-my-org--finance--analytics-team-eu"; tenant.String() != want {
		t.Errorf("tenant: got %q, want %q", tenant.String(), want)
	}
	admin, err := NewOrgAdminIdentifier("MY_ORG", "Platform_Admin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "yk-orgadmin--my-org--platform-admin"; admin.String() != want {
		t.Errorf("org-admin: got %q, want %q", admin.String(), want)
	}
}

// A tenant identifier must never equal the org-admin identifier of the same
// deployment, or a tenant account would share the org-admin credential's
// secret. Both use the same org from base.yaml (002). The tenant identifier
// yk-<org>--<namespace>--<accountName> takes the org-admin shape
// yk-orgadmin--<org>--<orgAdminAccount> when org is "orgadmin" (in any case),
// the namespace equals the org, and the account is named like
// orgAdminAccount. Either constructor rejecting its input also prevents the
// collision.
func TestIdentifiers_TenantNeverCollidesWithOrgAdmin(t *testing.T) {
	cases := []struct{ org, orgAdminAccount, namespace, accountName string }{
		{"orgadmin", "platform", "orgadmin", "platform"},
		{"OrgAdmin", "platform", "orgadmin", "platform"},
		{"ORGADMIN", "PLATFORM_ADMIN", "orgadmin", "platform-admin"},
	}
	for _, c := range cases {
		admin, adminErr := NewOrgAdminIdentifier(c.org, c.orgAdminAccount)
		tenant, tenantErr := NewTenantIdentifier(c.org, c.namespace, c.accountName)
		if adminErr != nil || tenantErr != nil {
			continue
		}
		if admin == tenant {
			t.Errorf("org=%q orgAdminAccount=%q: tenant (namespace=%q, accountName=%q) gets the org-admin identifier %q",
				c.org, c.orgAdminAccount, c.namespace, c.accountName, admin)
		}
	}
}

// SC-005b: both constructors reject "orgadmin" as org, in any case, with a
// user error: the value comes from base.yaml and is fixed by editing it.
func TestIdentifiers_RejectOrgAdminAsOrg(t *testing.T) {
	for _, org := range []string{"orgadmin", "OrgAdmin", "ORGADMIN"} {
		if id, err := NewTenantIdentifier(org, "finance", "analytics-team-eu"); !errors.IsUserError(err) || id != (Identifier{}) {
			t.Errorf("NewTenantIdentifier(org=%q) = (%q, %v), want zero value and a user error", org, id, err)
		}
		if id, err := NewOrgAdminIdentifier(org, "platform"); !errors.IsUserError(err) || id != (Identifier{}) {
			t.Errorf("NewOrgAdminIdentifier(org=%q) = (%q, %v), want zero value and a user error", org, id, err)
		}
	}
	// Only the org is reserved: the same name in another segment is fine.
	if _, err := NewTenantIdentifier("my_org", "orgadmin", "orgadmin"); err != nil {
		t.Errorf("namespace/accountName \"orgadmin\" must be allowed, got %v", err)
	}
	if _, err := NewOrgAdminIdentifier("my_org", "orgadmin"); err != nil {
		t.Errorf("orgAdminAccount \"orgadmin\" must be allowed, got %v", err)
	}
}

// SC-005: NewTenantIdentifier rejects an empty, '/'-containing, '.'-containing,
// '..'-containing, leading-separator, repeated-separator, or out-of-class
// segment in any of its three positions.
func TestNewTenantIdentifier_RejectsInvalidSegments(t *testing.T) {
	invalid := []string{"", "team/a", "team.a", "..", "team a", "team@a", "-team", "_team", "team--a", "team__a", "team-_a", "team_-a"}
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
	invalid := []string{"", "org/admin", "org.admin", "..", "org admin", "org@admin", "-org", "_org", "org--admin", "org__admin", "org-_admin", "org_-admin"}
	for _, bad := range invalid {
		if _, err := NewOrgAdminIdentifier(bad, "my_org_admin_account"); err == nil || !errors.IsUserError(err) {
			t.Errorf("org=%q: expected user error, got %v", bad, err)
		}
		if _, err := NewOrgAdminIdentifier("my_org", bad); err == nil || !errors.IsUserError(err) {
			t.Errorf("orgAdminAccount=%q: expected user error, got %v", bad, err)
		}
	}
}

// An identifier exceeding the shared backend length limit is rejected as a
// system error, not a user error: every length this package can see is
// already bounded tightly enough elsewhere that this should never actually
// trigger, so firing it means an invariant drifted out of sync somewhere
// upstream rather than that the caller supplied a fixable bad input.
func TestNewTenantIdentifier_RejectsOverLongIdentifier(t *testing.T) {
	longNamespace := strings.Repeat("a", maxIdentifierLen)
	_, err := NewTenantIdentifier("my_org", longNamespace, "analytics-team-eu")
	if err == nil {
		t.Fatal("expected an error for over-long identifier")
	}
	if errors.IsUserError(err) {
		t.Errorf("expected a system error for over-long identifier, got a user error: %v", err)
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
	for _, want := range []string{"my-org", "finance", "analytics-team-eu", "yk-"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing expected substring %q", got, want)
		}
	}
	if strings.Contains(got, "/") {
		t.Errorf("String() = %q, must not contain '/' (illegal in some secrets-manager backends)", got)
	}
}

// splitIdentifierParts splits the part of an identifier after its "yk-"
// prefix back into the segments it was joined from. It is written
// independently of the production code: a segment never starts with '-' and
// never contains "--", but it may end with '-', so a separator is the "--"
// that is directly followed by a character other than '-' (in "a---b" the
// segments are "a-" and "b").
func splitIdentifierParts(s string) []string {
	var parts []string
	start := 0
	for i := 0; i+2 < len(s); i++ {
		if s[i] == '-' && s[i+1] == '-' && s[i+2] != '-' {
			parts = append(parts, s[start:i])
			start = i + 2
			i++
		}
	}
	return append(parts, s[start:])
}

// FuzzNewTenantIdentifier checks the guarantee the identifier format exists
// for: two different (org, namespace, accountName) tuples never share a
// secret, because every identifier decodes back to exactly its own segments
// (after the documented mapping of '_' to '-' and of uppercase to lowercase). It also checks the charset and
// length every backend relies on, and that a rejected input is always a
// user error for an invalid segment and a system error only for the length.
func FuzzNewTenantIdentifier(f *testing.F) {
	f.Add("my_org", "finance", "analytics-team-eu")
	f.Add("a-", "b", "c")
	f.Add("a", "b-", "c-")
	f.Add("a", "-b", "c")
	f.Add("a", "b--c", "d")
	f.Add("a", "b_-c", "d")
	f.Add("a", "b", "c/../d")
	f.Add("", "b", "c")
	f.Add(strings.Repeat("a", 60), strings.Repeat("b", 60), "c")
	f.Fuzz(func(t *testing.T, org, namespace, accountName string) {
		id, err := NewTenantIdentifier(org, namespace, accountName)
		if err != nil {
			if id != (Identifier{}) {
				t.Fatalf("NewTenantIdentifier(%q, %q, %q) returned %q together with an error", org, namespace, accountName, id)
			}
			segmentsValid := validateSegment("", org) == nil && validateSegment("", namespace) == nil &&
				validateSegment("", accountName) == nil
			if errors.IsUserError(err) == segmentsValid {
				t.Fatalf("NewTenantIdentifier(%q, %q, %q): user error = %v, but segments valid = %v: %v",
					org, namespace, accountName, errors.IsUserError(err), segmentsValid, err)
			}
			return
		}

		got := id.String()
		if len(got) > maxIdentifierLen {
			t.Fatalf("identifier %q is %d characters, limit is %d", got, len(got), maxIdentifierLen)
		}
		for _, r := range got {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				t.Fatalf("identifier %q contains %q, outside the charset shared by all backends", got, r)
			}
		}
		rest, ok := strings.CutPrefix(got, "yk-")
		if !ok {
			t.Fatalf("identifier %q does not start with \"yk-\"", got)
		}
		secretSafe := func(seg string) string { return strings.ToLower(strings.ReplaceAll(seg, "_", "-")) }
		want := []string{secretSafe(org), secretSafe(namespace), secretSafe(accountName)}
		parts := splitIdentifierParts(rest)
		if len(parts) != len(want) {
			t.Fatalf("identifier %q decodes to %d segments %q, want %q", got, len(parts), parts, want)
		}
		for i := range want {
			if parts[i] != want[i] {
				t.Fatalf("identifier %q decodes to %q, want %q", got, parts, want)
			}
		}
	})
}

// maxSeenIdentifiers bounds the memory FuzzNewTenantIdentifier_NoCollisions
// uses; the table of seen identifiers is cleared once it reaches this size.
const maxSeenIdentifiers = 100_000

// FuzzNewTenantIdentifier_NoCollisions treats NewTenantIdentifier as a
// one-way function and checks that two different tenants never get the same
// identifier. From four fuzzed values it builds a few tenant tuples by
// swapping values, leaving one empty, or joining two neighbors with a
// separator, and remembers which tuple produced each identifier. The same
// identifier from a different tuple is a collision. Tuples are compared after
// the documented mapping of '_' to '-' and of uppercase to lowercase, since
// those are meant to name the same tenant.
func FuzzNewTenantIdentifier_NoCollisions(f *testing.F) {
	f.Add("my_org", "finance", "analytics-team-eu", "eu")
	f.Add("a", "b", "c", "d")
	f.Add("a-", "b", "c", "d")

	type tuple [3]string
	canonical := func(t tuple) tuple {
		for i := range t {
			t[i] = strings.ToLower(strings.ReplaceAll(t[i], "_", "-"))
		}
		return t
	}
	seen := make(map[string]tuple)

	f.Fuzz(func(t *testing.T, a, b, c, d string) {
		tuples := []tuple{
			// swapped values
			{a, b, c}, {a, c, b}, {b, a, c}, {b, c, a}, {c, a, b}, {c, b, a},
			// empty values
			{"", b, c}, {a, "", c}, {a, b, ""},
		}
		// two neighbors joined, moving the boundary between segments
		for _, sep := range []string{"", "-", "_", "--"} {
			tuples = append(tuples,
				tuple{a + sep + b, c, d},
				tuple{a, b + sep + c, d},
				tuple{a, b, c + sep + d})
		}

		for _, tu := range tuples {
			id, err := NewTenantIdentifier(tu[0], tu[1], tu[2])
			if err != nil {
				continue
			}
			if len(seen) >= maxSeenIdentifiers {
				clear(seen)
			}
			if prev, ok := seen[id.String()]; ok && prev != canonical(tu) {
				t.Fatalf("identifier %q is produced by %q and by %q", id, prev, canonical(tu))
			}
			seen[id.String()] = canonical(tu)
		}
	})
}
