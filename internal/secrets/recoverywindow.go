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

import "fmt"

// RecoveryWindow is the recovery window a backend will schedule on Delete: the longest window
// the store can represent that does not outlive the account's grace period (002). Zero days
// means the value is destroyed irreversibly, because the store can represent no window short
// enough.
//
// The asymmetry is deliberate. A credential that outlives its account is a pure blockade: the
// account name is free to reuse, but the secret path is still occupied and Create fails on it,
// with nothing worth recovering since the account itself is gone for good. A credential that
// expires before its account is merely degraded — an operator can restore the account, re-key
// the platform user, and store a fresh credential. So the window is capped hard and lengthened
// only on a best-effort basis, never the other way around.
type RecoveryWindow struct {
	days            int // 0 means Delete destroys the value irreversibly
	gracePeriodDays int // the account grace period this was derived from (002)
	minDays         int // the shortest window the store can represent, 0 if it has none
}

// DeriveRecoveryWindow returns the longest window within the store's [minDays, maxDays] band
// that does not exceed gracePeriodDays. A store with no recovery feature at all passes a zero
// band and always gets a zero window.
//
// Parameters:
//   - gracePeriodDays: the account grace period, Config.Deletion.GracePeriodDays (002)
//   - minDays, maxDays: the day band the store's own API can represent (AWS Secrets Manager:
//     7 and 30); both zero for a store with no recovery window
//
// Returns:
//   - RecoveryWindow: zero days when the band's shortest window would outlive the grace period,
//     which is itself invariant-compliant — destroying the credential outright never blocks
//     re-provisioning
func DeriveRecoveryWindow(gracePeriodDays, minDays, maxDays int) RecoveryWindow {
	w := RecoveryWindow{gracePeriodDays: gracePeriodDays, minDays: minDays}

	longest := maxDays
	if gracePeriodDays < longest {
		longest = gracePeriodDays
	}
	if longest >= minDays && longest > 0 {
		w.days = longest
	}
	return w
}

// Days returns the window in days, 0 when Delete destroys the value irreversibly.
func (w RecoveryWindow) Days() int { return w.days }

// Immediate reports whether Delete destroys the value irreversibly rather than scheduling it.
func (w RecoveryWindow) Immediate() bool { return w.days == 0 }

// Describe returns a one-line operator-facing statement of how the window relates to the
// account grace period it was derived from — logged once at startup so the gap, if any, is
// visible before anyone needs it. Three cases:
//
//   - coupled: the window matches the grace period exactly, so a restored account always still
//     has its credential and nothing needs repairing by hand
//   - partially coupled: the window is shorter, and the message names the day band in which a
//     restored account needs its credential repaired by hand
//   - uncoupled: there is no window, so every restore needs a manual credential repair
func (w RecoveryWindow) Describe() string {
	switch {
	case w.days == 0 && w.minDays <= 0:
		return "credential recovery is off: the store has no recovery window, so a deleted credential is destroyed irreversibly and every restored account needs manual credential repair"
	case w.days == 0:
		return fmt.Sprintf(
			"credential recovery is off: the store's shortest window (%dd) would outlive the %dd account grace period, so a deleted credential is destroyed irreversibly and every restored account needs manual credential repair",
			w.minDays, w.gracePeriodDays)
	case w.days == w.gracePeriodDays:
		return fmt.Sprintf(
			"credential recovery window %dd matches the account grace period: a restored account keeps its credential, no manual repair needed",
			w.days)
	default:
		return fmt.Sprintf(
			"credential recovery window %dd is shorter than the %dd account grace period: an account restored on days %d-%d needs manual credential repair",
			w.days, w.gracePeriodDays, w.days+1, w.gracePeriodDays)
	}
}
