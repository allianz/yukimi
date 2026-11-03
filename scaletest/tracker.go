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
	"fmt"
	"time"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
)

// Timing is what was observed about one account. The times are those of the
// polls, so they are accurate to the poll interval.
type Timing struct {
	Name           string    `json:"name"`
	Applied        time.Time `json:"applied"`                 // the account was applied, or resumed by a later run
	FirstCondition time.Time `json:"firstCondition,omitzero"` // the operator reacted: it set the first condition
	Located        time.Time `json:"located,omitzero"`        // the account has a locator: CREATE ACCOUNT ran
	Ready          time.Time `json:"ready,omitzero"`          // Ready and Synced are true
	LastError      string    `json:"lastError,omitempty"`     // the last message of Synced=False
	Resumed        bool      `json:"resumed,omitempty"`       // an account of an earlier run, followed from the start of this one
}

// SecondsToFirstCondition is the time from the apply until the operator reacted.
func (t Timing) SecondsToFirstCondition() (float64, bool) { return since(t.Applied, t.FirstCondition) }

// SecondsToLocator is the time from the apply until the account had a locator.
func (t Timing) SecondsToLocator() (float64, bool) { return since(t.Applied, t.Located) }

// SecondsToReady is the time from the apply until the account was ready.
func (t Timing) SecondsToReady() (float64, bool) { return since(t.Applied, t.Ready) }

func since(from, to time.Time) (float64, bool) {
	if to.IsZero() {
		return 0, false
	}
	return to.Sub(from).Seconds(), true
}

// Failure is an account that does not get ready, and why.
type Failure struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Limits decide when an account counts as failed.
type Limits struct {
	Timeout      time.Duration // the account is not ready this long after the apply
	ErrorGrace   time.Duration // Synced stays false this long
	DegradeGrace time.Duration // an account that was ready stays not ready this long
	StartTimeout time.Duration // the account has no condition this long: the operator does not react
}

// errorWatch remembers since when the Synced condition of each account has been
// false, without a break.
type errorWatch struct{ since map[string]time.Time }

func newErrorWatch() *errorWatch { return &errorWatch{since: map[string]time.Time{}} }

// exceeded updates the watch with the Synced condition of an account and returns
// the failure if it has been false for longer than grace.
func (w *errorWatch) exceeded(now time.Time, name string, synced xpv1.Condition, grace time.Duration) (reason string, over bool) {
	if synced.Status != corev1.ConditionFalse {
		delete(w.since, name)
		return "", false
	}
	if _, ok := w.since[name]; !ok {
		w.since[name] = now
	}
	if now.Sub(w.since[name]) > grace {
		return fmt.Sprintf("Synced has been false for more than %s: %s", grace, synced.Message), true
	}
	return "", false
}

func (w *errorWatch) forget(name string) { delete(w.since, name) }

// syncedError returns the message of the Synced condition if it is false.
func syncedError(a *v1alpha1.SnowflakeAccount) string {
	if c := a.GetCondition(xpv1.TypeSynced); c.Status == corev1.ConditionFalse {
		return c.Message
	}
	return ""
}

// Tracker follows a set of applied accounts. It only reads what Observe is
// given, so it needs no cluster.
type Tracker struct {
	limits  Limits
	timings map[string]*Timing
	order   []string
	watch   *errorWatch

	// ready are the accounts that were ready and are still watched: one that stops
	// being ready, or disappears, must not go unnoticed while later batches are created.
	ready      map[string]time.Time // since when the account has not been ready; zero while it is
	readyOrder []string
}

// NewTracker returns a tracker with no accounts.
func NewTracker(limits Limits) *Tracker {
	return &Tracker{limits: limits, timings: map[string]*Timing{}, watch: newErrorWatch(), ready: map[string]time.Time{}}
}

// Watch starts watching accounts that are already ready, for example those of an
// earlier run.
func (tr *Tracker) Watch(names ...string) {
	for _, name := range names {
		if _, ok := tr.ready[name]; !ok {
			tr.ready[name] = time.Time{}
			tr.readyOrder = append(tr.readyOrder, name)
		}
	}
}

// Applied starts following accounts that were applied at now.
func (tr *Tracker) Applied(now time.Time, names ...string) { tr.follow(now, false, names) }

// Resumed starts following accounts of an earlier run that are not ready, from now
// on. Their times say nothing about the system, since they started long ago.
func (tr *Tracker) Resumed(now time.Time, names ...string) { tr.follow(now, true, names) }

func (tr *Tracker) follow(now time.Time, resumed bool, names []string) {
	for _, name := range names {
		if _, ok := tr.timings[name]; !ok {
			tr.timings[name] = &Timing{Name: name, Applied: now, Resumed: resumed}
			tr.order = append(tr.order, name)
		}
	}
}

// Observe records what the accounts look like at now. It returns how many of the
// followed accounts are not ready yet and which of those have failed. A failed
// account is reported on every call until it is ready. Accounts that were not
// applied through Applied or Watch are ignored. One that is not in the list is
// pending, and fails like any other when it takes too long.
//
// A watched account that is not ready is pending, so a wait does not end while it
// may still recover, and fails when it stays that way for longer than the degrade
// grace, or when it is no longer in the cluster.
func (tr *Tracker) Observe(now time.Time, accounts []v1alpha1.SnowflakeAccount) (pending int, failures []Failure) {
	seen := map[string]*v1alpha1.SnowflakeAccount{}
	for i := range accounts {
		seen[accounts[i].Name] = &accounts[i]
	}
	for _, name := range tr.order {
		t := tr.timings[name]
		if !t.Ready.IsZero() {
			continue
		}
		pending++
		account := seen[name]
		if account == nil {
			if age := now.Sub(t.Applied); age > tr.limits.Timeout {
				failures = append(failures, Failure{Name: name, Reason: fmt.Sprintf("not in the cluster %s after the apply", age.Round(time.Second))})
			}
			continue
		}
		if len(account.Status.Conditions) > 0 && t.FirstCondition.IsZero() {
			t.FirstCondition = now
		}
		if account.Status.AccountLocator != "" && t.Located.IsZero() {
			t.Located = now
		}
		if msg := syncedError(account); msg != "" {
			t.LastError = msg
		}
		if isReady(account) {
			t.Ready = now
			tr.watch.forget(name)
			tr.Watch(name)
			pending--
			continue
		}
		if reason := tr.failure(now, t, account); reason != "" {
			failures = append(failures, Failure{Name: name, Reason: reason})
		}
	}

	for _, name := range tr.readyOrder {
		account := seen[name]
		switch {
		case account == nil:
			failures = append(failures, Failure{Name: name, Reason: "no longer in the cluster"})
		case isReady(account):
			tr.ready[name] = time.Time{}
		default:
			if tr.ready[name].IsZero() {
				tr.ready[name] = now
			}
			pending++
			if age := now.Sub(tr.ready[name]); age > tr.limits.DegradeGrace {
				failures = append(failures, Failure{Name: name, Reason: fmt.Sprintf("not ready any more for %s: %s", age.Round(time.Second), DescribeConditions(account))})
			}
		}
	}
	return pending, failures
}

// failure returns why an account that is not ready counts as failed, or "".
func (tr *Tracker) failure(now time.Time, t *Timing, account *v1alpha1.SnowflakeAccount) string {
	age := now.Sub(t.Applied)
	if len(account.Status.Conditions) == 0 {
		if age > tr.limits.StartTimeout {
			return fmt.Sprintf("the operator has set no condition %s after the apply", age.Round(time.Second))
		}
	} else if reason, over := tr.watch.exceeded(now, t.Name, account.GetCondition(xpv1.TypeSynced), tr.limits.ErrorGrace); over {
		return reason
	}
	if age > tr.limits.Timeout {
		return fmt.Sprintf("not ready %s after the apply: %s", age.Round(time.Second), DescribeConditions(account))
	}
	return ""
}

// Timings returns the timings of all followed accounts, in the order they were applied.
func (tr *Tracker) Timings() []Timing {
	out := make([]Timing, 0, len(tr.order))
	for _, name := range tr.order {
		out = append(out, *tr.timings[name])
	}
	return out
}

func isReady(a *v1alpha1.SnowflakeAccount) bool {
	return a.GetCondition(xpv1.TypeReady).Status == corev1.ConditionTrue &&
		a.GetCondition(xpv1.TypeSynced).Status == corev1.ConditionTrue
}

// DescribeConditions lists the conditions of an account, for messages.
func DescribeConditions(a *v1alpha1.SnowflakeAccount) string {
	if len(a.Status.Conditions) == 0 {
		return "no conditions yet"
	}
	s := ""
	for i, c := range a.Status.Conditions {
		if i > 0 {
			s += "; "
		}
		s += fmt.Sprintf("%s=%s (%s: %s)", c.Type, c.Status, c.Reason, c.Message)
	}
	return s
}
