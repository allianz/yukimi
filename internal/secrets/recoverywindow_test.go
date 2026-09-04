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
)

// SC-021: the derived window is the longest the store can represent without exceeding the
// account grace period. The band used throughout is AWS Secrets Manager's 7-30 (003.a).
func TestDeriveRecoveryWindow(t *testing.T) {
	tests := []struct {
		name            string
		gracePeriodDays int
		minDays         int
		maxDays         int
		wantDays        int
	}{
		{"grace period above the ceiling is capped at the ceiling", 90, 7, 30, 30},
		{"grace period equal to the ceiling matches exactly", 30, 7, 30, 30},
		{"grace period inside the band is used verbatim", 14, 7, 30, 14},
		{"grace period equal to the floor is used verbatim", 7, 7, 30, 7},
		{"grace period below the floor leaves no compliant window", 6, 7, 30, 0},
		{"grace period below Snowflake's own minimum still leaves none", 3, 7, 30, 0},
		{"a store with no recovery feature always gets zero", 30, 0, 0, 0},
		{"a store that can represent any length matches the grace period", 30, 1, 3650, 30},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := DeriveRecoveryWindow(tc.gracePeriodDays, tc.minDays, tc.maxDays)
			if w.Days() != tc.wantDays {
				t.Errorf("Days() = %d, want %d", w.Days(), tc.wantDays)
			}
			if got := w.Immediate(); got != (tc.wantDays == 0) {
				t.Errorf("Immediate() = %v, want %v", got, tc.wantDays == 0)
			}
			// The invariant itself: a derived window may fall short of the grace period but
			// must never exceed it, whatever the band.
			if w.Days() > tc.gracePeriodDays {
				t.Errorf("Days() = %d exceeds the %d-day grace period", w.Days(), tc.gracePeriodDays)
			}
		})
	}
}

// SC-022: Describe names the manual-repair gap, or says there is none, in each of the three
// coupling states.
func TestRecoveryWindow_Describe(t *testing.T) {
	t.Run("coupled: no gap to report", func(t *testing.T) {
		got := DeriveRecoveryWindow(30, 7, 30).Describe()
		for _, want := range []string{"30d", "matches the account grace period", "no manual repair"} {
			if !strings.Contains(got, want) {
				t.Errorf("Describe() = %q, want it to contain %q", got, want)
			}
		}
	})

	t.Run("partially coupled: names the repair band", func(t *testing.T) {
		got := DeriveRecoveryWindow(90, 7, 30).Describe()
		// The band starts the day after the credential expires and runs to the account's own
		// deadline — the days on which a restore succeeds but arrives at a missing credential.
		for _, want := range []string{"30d", "90d", "days 31-90", "manual credential repair"} {
			if !strings.Contains(got, want) {
				t.Errorf("Describe() = %q, want it to contain %q", got, want)
			}
		}
	})

	t.Run("uncoupled: the band cannot represent a compliant window", func(t *testing.T) {
		got := DeriveRecoveryWindow(3, 7, 30).Describe()
		for _, want := range []string{"recovery is off", "7d", "3d", "irreversibly"} {
			if !strings.Contains(got, want) {
				t.Errorf("Describe() = %q, want it to contain %q", got, want)
			}
		}
	})

	t.Run("uncoupled: the store has no recovery feature at all", func(t *testing.T) {
		got := DeriveRecoveryWindow(30, 0, 0).Describe()
		if !strings.Contains(got, "no recovery window") {
			t.Errorf("Describe() = %q, want it to say the store has no recovery window", got)
		}
	})
}
