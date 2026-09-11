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

package tenant

import (
	"regexp"
	"testing"
)

// SC-009: matches design.md 3.12's worked example exactly.
func TestResolveName_DesignExample(t *testing.T) {
	got := ResolveName("analytics-team-eu", "finance")
	want := "analytics_team_eu_5k3wf"
	if got != want {
		t.Errorf("ResolveName() = %q, want %q", got, want)
	}
}

// SC-010: every '-' in name is translated to '_'.
func TestResolveName_TranslatesHyphens(t *testing.T) {
	got := ResolveName("multi-word-account-name", "ns")
	if got[:len("multi_word_account_name")] != "multi_word_account_name" {
		t.Errorf("ResolveName() = %q, want prefix %q", got, "multi_word_account_name")
	}
}

// SC-010: deterministic — same inputs always produce the same output.
func TestResolveName_Deterministic(t *testing.T) {
	first := ResolveName("dev", "finance")
	second := ResolveName("dev", "finance")
	if first != second {
		t.Errorf("ResolveName() not deterministic: %q != %q", first, second)
	}
}

// Two tenants naming an account identically in different namespaces resolve
// to different Snowflake names (see spec 006's Edge Cases).
func TestResolveName_DifferentNamespacesNoCollision(t *testing.T) {
	first := ResolveName("dev", "finance")
	second := ResolveName("dev", "analytics")
	if first == second {
		t.Errorf("ResolveName() collided across namespaces: both = %q", first)
	}
}

// validCRDName mirrors the SnowflakeAccount CRD's own XValidation pattern on
// metadata.name (snowflakeaccount_types.go): "^[a-z][a-z0-9-]*$". ResolveName
// itself never validates name — the CRD's admission control is its only
// guard — so a fuzz target has no way to know which arbitrary inputs are
// "supposed" to be reachable; it can only assert real invariants on inputs
// the CRD would actually admit, and treat everything else as out of scope.
var validCRDName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// bareIdentifierCharset mirrors statement.BareIdentifier's own pattern
// (render.go): "^[A-Za-z][A-Za-z0-9_]*$". apply.go's createAccount feeds
// ResolveName's output straight into statement.BareIdentifier with no
// transform of its own, so ResolveName must never produce a name
// BareIdentifier would reject for any name the CRD itself admits — that
// defense-in-depth check would otherwise reject a perfectly legitimate
// tenant account name.
var bareIdentifierCharset = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// FuzzResolveName: no panic and deterministic (same inputs, same output) for
// any input at all — name and namespace both ultimately come from
// Kubernetes object metadata, which ResolveName trusts rather than
// re-validates (see its own doc comment). For the narrower case of a name
// the CRD's own pattern would actually admit, the resolved name must also
// satisfy statement.BareIdentifier's charset, since apply.go relies on that
// holding without any transform in between.
func FuzzResolveName(f *testing.F) {
	f.Add("analytics-team-eu", "finance")
	f.Add("dev", "ns")
	f.Add("", "")
	f.Add("a", "a")
	f.Add("multi-word-account-name", "ns")
	f.Add(`'; DROP TABLE X; --`, "ns")
	f.Add("dev", `'; DROP TABLE X; --`)
	f.Fuzz(func(t *testing.T, name, namespace string) {
		got := ResolveName(name, namespace)

		if again := ResolveName(name, namespace); again != got {
			t.Fatalf("ResolveName(%q, %q) not deterministic: %q != %q", name, namespace, got, again)
		}

		if !validCRDName.MatchString(name) {
			return
		}
		if !bareIdentifierCharset.MatchString(got) {
			t.Fatalf("ResolveName(%q, %q) = %q, which statement.BareIdentifier's charset would reject", name, namespace, got)
		}
	})
}
