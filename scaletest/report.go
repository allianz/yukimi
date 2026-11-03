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
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// Distribution describes a set of durations in seconds.
type Distribution struct {
	Count int     `json:"count"`
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	Max   float64 `json:"max"`
}

func (d Distribution) String() string {
	if d.Count == 0 {
		return "no data"
	}
	return fmt.Sprintf("p50 %.0fs, p95 %.0fs, max %.0fs (%d accounts)", d.P50, d.P95, d.Max, d.Count)
}

// Distribute computes the median, the 95th percentile (nearest rank) and the
// maximum of values.
func Distribute(values []float64) Distribution {
	if len(values) == 0 {
		return Distribution{}
	}
	sorted := slices.Sorted(slices.Values(values))
	rank := func(p float64) float64 {
		return sorted[int(math.Ceil(p*float64(len(sorted))))-1]
	}
	return Distribution{Count: len(sorted), P50: rank(0.50), P95: rank(0.95), Max: sorted[len(sorted)-1]}
}

// Result is the outcome of a run.
type Result struct {
	Started    time.Time `json:"started"`
	Finished   time.Time `json:"finished"`
	Target     int       `json:"target"`
	Existing   int       `json:"existing"`   // accounts that existed before the run
	Applied    int       `json:"applied"`    // accounts this run applied, or resumed because they were not ready
	Ready      int       `json:"ready"`      // of those, how many became ready
	ReadyTotal int       `json:"readyTotal"` // accounts among 1..target that are ready at the end; -1 if they could not be counted

	ToFirstCondition Distribution `json:"toFirstCondition"` // apply until the operator reacted
	ToLocator        Distribution `json:"toLocator"`        // apply until CREATE ACCOUNT ran
	ToReady          Distribution `json:"toReady"`          // apply until Ready and Synced
	Failures         []Failure    `json:"failures,omitempty"`
	Error            string       `json:"error,omitempty"` // why the run stopped, if it did
	Accounts         []Timing     `json:"accounts"`
}

// Summarize fills in the numbers of a result from the timings. Accounts of an
// earlier run that were resumed are counted, but left out of the distributions.
func (r *Result) Summarize(timings []Timing) {
	var toFirst, toLocator, toReady []float64
	ready := 0
	for _, t := range timings {
		if !t.Ready.IsZero() {
			ready++
		}
		if t.Resumed {
			continue
		}
		if s, ok := t.SecondsToFirstCondition(); ok {
			toFirst = append(toFirst, s)
		}
		if s, ok := t.SecondsToLocator(); ok {
			toLocator = append(toLocator, s)
		}
		if s, ok := t.SecondsToReady(); ok {
			toReady = append(toReady, s)
		}
	}
	r.Accounts = timings
	r.Applied = len(timings)
	r.Ready = ready
	r.ToFirstCondition = Distribute(toFirst)
	r.ToLocator = Distribute(toLocator)
	r.ToReady = Distribute(toReady)
}

// Write stores the result as JSON in dir and returns the path of the file.
func (r *Result) Write(dir string) (string, error) {
	return writeJSON(dir, "result-", r.Started, r)
}

// writeJSON stores v in dir, in a file named after prefix and the start time.
func writeJSON(dir, prefix string, started time.Time, v any) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, prefix+started.UTC().Format("20060102T150405Z")+".json")
	return path, os.WriteFile(path, data, 0o644)
}
