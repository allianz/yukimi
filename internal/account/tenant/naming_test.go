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

import "testing"

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
