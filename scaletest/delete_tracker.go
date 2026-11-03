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

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
)

// DeleteTiming is what was observed about the deletion of one account.
type DeleteTiming struct {
	Name      string    `json:"name"`
	Deleted   time.Time `json:"deleted"`             // the delete was requested
	Gone      time.Time `json:"gone,omitzero"`       // the account was no longer in the cluster
	LastError string    `json:"lastError,omitempty"` // the last message of Synced=False
}

// SecondsToGone is the time from the delete request until the account was gone.
func (t DeleteTiming) SecondsToGone() (float64, bool) { return since(t.Deleted, t.Gone) }

// GoneTracker follows accounts whose deletion was requested. Like Tracker it
// only reads what Observe is given.
type GoneTracker struct {
	limits  Limits
	timings map[string]*DeleteTiming
	order   []string
	watch   *errorWatch
}

// NewGoneTracker returns a tracker with no accounts. limits.Timeout is how long
// a deletion may take.
func NewGoneTracker(limits Limits) *GoneTracker {
	return &GoneTracker{limits: limits, timings: map[string]*DeleteTiming{}, watch: newErrorWatch()}
}

// Deleted starts following accounts whose deletion was requested at now.
func (tr *GoneTracker) Deleted(now time.Time, names ...string) {
	for _, name := range names {
		if _, ok := tr.timings[name]; !ok {
			tr.timings[name] = &DeleteTiming{Name: name, Deleted: now}
			tr.order = append(tr.order, name)
		}
	}
}

// Observe records which of the followed accounts are gone from accounts, the
// current list. It returns how many are still there and which of those have
// failed: an account that stays longer than the limit, or reports an error for
// longer than the grace.
func (tr *GoneTracker) Observe(now time.Time, accounts []v1alpha1.SnowflakeAccount) (pending int, failures []Failure) {
	present := map[string]*v1alpha1.SnowflakeAccount{}
	for i := range accounts {
		present[accounts[i].Name] = &accounts[i]
	}
	for _, name := range tr.order {
		t := tr.timings[name]
		if !t.Gone.IsZero() {
			continue
		}
		account := present[name]
		if account == nil {
			t.Gone = now
			tr.watch.forget(name)
			continue
		}
		pending++
		if msg := syncedError(account); msg != "" {
			t.LastError = msg
		}
		if reason, over := tr.watch.exceeded(now, name, account.GetCondition(xpv1.TypeSynced), tr.limits.ErrorGrace); over {
			failures = append(failures, Failure{Name: name, Reason: reason})
		} else if age := now.Sub(t.Deleted); age > tr.limits.Timeout {
			failures = append(failures, Failure{Name: name, Reason: fmt.Sprintf("still there %s after the delete: %s", age.Round(time.Second), DescribeConditions(account))})
		}
	}
	return pending, failures
}

// Timings returns the timings of all followed accounts, in the order of the requests.
func (tr *GoneTracker) Timings() []DeleteTiming {
	out := make([]DeleteTiming, 0, len(tr.order))
	for _, name := range tr.order {
		out = append(out, *tr.timings[name])
	}
	return out
}
