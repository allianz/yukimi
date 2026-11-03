//go:build scaletest

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

package scaletest

import (
	"strings"
	"testing"
	"time"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
)

var (
	t0     = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	limits = Limits{Timeout: 30 * time.Minute, ErrorGrace: 5 * time.Minute, DegradeGrace: 5 * time.Minute, StartTimeout: 2 * time.Minute}
)

func account(name, locator string, conditions ...xpv1.Condition) v1alpha1.SnowflakeAccount {
	a := v1alpha1.SnowflakeAccount{ObjectMeta: metav1.ObjectMeta{Name: name}}
	a.Status.AccountLocator = locator
	a.SetConditions(conditions...)
	return a
}

func ready() []xpv1.Condition {
	return []xpv1.Condition{xpv1.Available(), xpv1.ReconcileSuccess()}
}

func TestTrackerRecordsTheTimes(t *testing.T) {
	tr := NewTracker(limits)
	tr.Applied(t0, "a")

	pending, failures := tr.Observe(t0.Add(10*time.Second), []v1alpha1.SnowflakeAccount{account("a", "", xpv1.Creating())})
	if pending != 1 || len(failures) != 0 {
		t.Fatalf("pending = %d, failures = %v", pending, failures)
	}
	tr.Observe(t0.Add(60*time.Second), []v1alpha1.SnowflakeAccount{account("a", "xy1", xpv1.Creating(), xpv1.ReconcileSuccess())})
	pending, _ = tr.Observe(t0.Add(5*time.Minute), []v1alpha1.SnowflakeAccount{account("a", "xy1", ready()...)})
	if pending != 0 {
		t.Fatalf("pending = %d, want 0", pending)
	}

	timing := tr.Timings()[0]
	for name, tt := range map[string]struct {
		got  func() (float64, bool)
		want float64
	}{
		"first condition": {timing.SecondsToFirstCondition, 10},
		"locator":         {timing.SecondsToLocator, 60},
		"ready":           {timing.SecondsToReady, 300},
	} {
		if s, ok := tt.got(); !ok || s != tt.want {
			t.Errorf("seconds to %s = %v, %v, want %v", name, s, ok, tt.want)
		}
	}
}

func TestTrackerFailures(t *testing.T) {
	syncedFalse := xpv1.ReconcileError(errTest("snowflake said no"))
	tests := []struct {
		name    string
		account v1alpha1.SnowflakeAccount
		at      time.Duration
		want    string // part of the failure reason; "" means no failure
	}{
		{"no condition for a short time", account("a", ""), time.Minute, ""},
		{"no condition for too long", account("a", ""), 3 * time.Minute, "no condition"},
		{"an error that is still within the grace", account("a", "", syncedFalse), time.Minute, ""},
		{"not ready after the timeout", account("a", "xy1", xpv1.Creating(), xpv1.ReconcileSuccess()), 31 * time.Minute, "not ready"},
		{"still creating within the timeout", account("a", "xy1", xpv1.Creating(), xpv1.ReconcileSuccess()), 20 * time.Minute, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := NewTracker(limits)
			tr.Applied(t0, "a")
			_, failures := tr.Observe(t0.Add(tt.at), []v1alpha1.SnowflakeAccount{tt.account})
			if tt.want == "" {
				if len(failures) != 0 {
					t.Fatalf("unexpected failure: %v", failures)
				}
				return
			}
			if len(failures) != 1 || !strings.Contains(failures[0].Reason, tt.want) {
				t.Fatalf("failures = %v, want one containing %q", failures, tt.want)
			}
		})
	}
}

func TestTrackerStartTimeoutIsASetting(t *testing.T) {
	// With a long start timeout the operator may take its time to react; the ready
	// timeout still holds.
	patient := limits
	patient.StartTimeout = 10 * time.Minute
	tr := NewTracker(patient)
	tr.Applied(t0, "a")

	if _, failures := tr.Observe(t0.Add(5*time.Minute), []v1alpha1.SnowflakeAccount{account("a", "")}); len(failures) != 0 {
		t.Fatalf("5 minutes without a condition is fine for a 10 minute start timeout, got %v", failures)
	}
	if _, failures := tr.Observe(t0.Add(11*time.Minute), []v1alpha1.SnowflakeAccount{account("a", "")}); len(failures) != 1 {
		t.Fatalf("failures = %v, want the account", failures)
	}
}

func TestTrackerErrorGrace(t *testing.T) {
	syncedFalse := xpv1.ReconcileError(errTest("snowflake said no"))
	tr := NewTracker(limits)
	tr.Applied(t0, "a")

	tr.Observe(t0.Add(time.Minute), []v1alpha1.SnowflakeAccount{account("a", "", syncedFalse)})
	if _, failures := tr.Observe(t0.Add(7*time.Minute), []v1alpha1.SnowflakeAccount{account("a", "", syncedFalse)}); len(failures) != 1 ||
		!strings.Contains(failures[0].Reason, "snowflake said no") {
		t.Fatalf("an error longer than the grace must fail with its message, got %v", failures)
	}
	if got := tr.Timings()[0].LastError; got != "snowflake said no" {
		t.Fatalf("last error = %q", got)
	}

	// An error that went away starts the grace over.
	tr = NewTracker(limits)
	tr.Applied(t0, "a")
	tr.Observe(t0.Add(time.Minute), []v1alpha1.SnowflakeAccount{account("a", "", syncedFalse)})
	tr.Observe(t0.Add(4*time.Minute), []v1alpha1.SnowflakeAccount{account("a", "", xpv1.Creating(), xpv1.ReconcileSuccess())})
	if _, failures := tr.Observe(t0.Add(7*time.Minute), []v1alpha1.SnowflakeAccount{account("a", "", syncedFalse)}); len(failures) != 0 {
		t.Fatalf("the error is new, so the grace starts again, got %v", failures)
	}
}

func TestTrackerIgnoresReadyAndUnknownAccounts(t *testing.T) {
	tr := NewTracker(limits)
	tr.Applied(t0, "a")
	accounts := []v1alpha1.SnowflakeAccount{account("a", "xy1", ready()...), account("other", "")}

	if pending, failures := tr.Observe(t0.Add(time.Minute), accounts); pending != 0 || len(failures) != 0 {
		t.Fatalf("pending = %d, failures = %v", pending, failures)
	}
}

func TestTrackerAccountMissingFromTheCluster(t *testing.T) {
	tr := NewTracker(limits)
	tr.Applied(t0, "b")

	// Not in the list yet (the listing may lag behind the create): only pending.
	if pending, failures := tr.Observe(t0.Add(time.Minute), nil); pending != 1 || len(failures) != 0 {
		t.Fatalf("pending = %d, failures = %v", pending, failures)
	}
	// Gone for good (someone deleted it): it must not hang the run forever.
	_, failures := tr.Observe(t0.Add(40*time.Minute), nil)
	if len(failures) != 1 || !strings.Contains(failures[0].Reason, "not in the cluster") {
		t.Fatalf("failures = %v, want one saying the account is not in the cluster", failures)
	}
}

func TestDescribeConditions(t *testing.T) {
	a := account("a", "")
	if got := DescribeConditions(&a); got != "no conditions yet" {
		t.Fatalf("got %q", got)
	}
	a = account("a", "", xpv1.Creating())
	if got := DescribeConditions(&a); !strings.Contains(got, string(corev1.ConditionFalse)) {
		t.Fatalf("got %q", got)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

func TestTrackerWatchesAccountsThatWereReady(t *testing.T) {
	good := func() []v1alpha1.SnowflakeAccount {
		return []v1alpha1.SnowflakeAccount{account("a", "xy1", ready()...)}
	}
	bad := func() []v1alpha1.SnowflakeAccount {
		return []v1alpha1.SnowflakeAccount{account("a", "xy1", xpv1.Available(), xpv1.ReconcileError(errTest("throttled")))}
	}

	tr := NewTracker(limits) // error grace 5 minutes
	tr.Watch("a")
	if pending, failures := tr.Observe(t0, good()); pending != 0 || len(failures) != 0 {
		t.Fatalf("pending = %d, failures = %v", pending, failures)
	}
	// It degrades: pending while it may recover, a failure when the grace is over.
	if pending, failures := tr.Observe(t0.Add(time.Minute), bad()); pending != 1 || len(failures) != 0 {
		t.Fatalf("pending = %d, failures = %v; want it pending within the grace", pending, failures)
	}
	if _, failures := tr.Observe(t0.Add(7*time.Minute), bad()); len(failures) != 1 || !strings.Contains(failures[0].Reason, "not ready any more") {
		t.Fatalf("failures = %v", failures)
	}
	// A recovery starts the grace over.
	if pending, failures := tr.Observe(t0.Add(8*time.Minute), good()); pending != 0 || len(failures) != 0 {
		t.Fatalf("pending = %d, failures = %v", pending, failures)
	}
	if _, failures := tr.Observe(t0.Add(9*time.Minute), bad()); len(failures) != 0 {
		t.Fatalf("failures = %v, want the grace to start again", failures)
	}
	// It disappears.
	if _, failures := tr.Observe(t0.Add(10*time.Minute), nil); len(failures) != 1 || !strings.Contains(failures[0].Reason, "no longer in the cluster") {
		t.Fatalf("failures = %v", failures)
	}
}

func TestTrackerWatchesAnAccountOnceItIsReady(t *testing.T) {
	tr := NewTracker(limits)
	tr.Applied(t0, "a")
	tr.Observe(t0.Add(time.Minute), []v1alpha1.SnowflakeAccount{account("a", "xy1", ready()...)})

	// Ready is not the end of the matter: it breaks later, while other batches are made.
	broken := []v1alpha1.SnowflakeAccount{account("a", "xy1", xpv1.Available(), xpv1.ReconcileError(errTest("boom")))}
	tr.Observe(t0.Add(2*time.Minute), broken)
	if _, failures := tr.Observe(t0.Add(10*time.Minute), broken); len(failures) != 1 {
		t.Fatalf("failures = %v, want the account that broke after it was ready", failures)
	}
	if got := tr.Timings(); len(got) != 1 {
		t.Fatalf("timings = %d; a watched account must not become a second entry", len(got))
	}
}
