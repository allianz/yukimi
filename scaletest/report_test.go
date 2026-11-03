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
	"os"
	"testing"
	"time"
)

func TestDistribute(t *testing.T) {
	if got := Distribute(nil); got.Count != 0 || got.String() != "no data" {
		t.Fatalf("empty = %+v (%s)", got, got)
	}
	values := make([]float64, 100)
	for i := range values {
		values[i] = float64(100 - i) // unsorted on purpose: 100..1
	}
	got := Distribute(values)
	if got.Count != 100 || got.P50 != 50 || got.P95 != 95 || got.Max != 100 {
		t.Fatalf("distribution = %+v", got)
	}
	if got := Distribute([]float64{7}); got.P50 != 7 || got.P95 != 7 || got.Max != 7 {
		t.Fatalf("one value = %+v", got)
	}
}

func TestResultSummarizeAndWrite(t *testing.T) {
	timings := []Timing{
		{Name: "a", Applied: t0, FirstCondition: t0.Add(10 * time.Second), Located: t0.Add(30 * time.Second), Ready: t0.Add(100 * time.Second)},
		{Name: "b", Applied: t0, FirstCondition: t0.Add(20 * time.Second), Located: t0.Add(40 * time.Second), LastError: "boom"}, // not ready
	}
	r := &Result{Started: t0, Finished: t0.Add(time.Hour), Target: 2}
	r.Summarize(timings)
	if r.Applied != 2 || r.Ready != 1 || r.ToLocator.Count != 2 || r.ToReady.Max != 100 || r.ToFirstCondition.Max != 20 {
		t.Fatalf("result = %+v", r)
	}

	path, err := r.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var back Result
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("the file is not valid JSON: %v", err)
	}
	if back.Target != 2 || len(back.Accounts) != 2 || back.Accounts[1].LastError != "boom" {
		t.Fatalf("read back = %+v", back)
	}
}

func TestSummarizeLeavesResumedAccountsOutOfTheDistributions(t *testing.T) {
	r := &Result{}
	r.Summarize([]Timing{
		{Name: "old", Applied: t0, Resumed: true, FirstCondition: t0, Located: t0, Ready: t0.Add(time.Second)},
		{Name: "new", Applied: t0, FirstCondition: t0.Add(10 * time.Second), Located: t0.Add(40 * time.Second), Ready: t0.Add(100 * time.Second)},
	})
	if r.Applied != 2 || r.Ready != 2 {
		t.Fatalf("applied %d, ready %d; both count", r.Applied, r.Ready)
	}
	if r.ToReady.Count != 1 || r.ToReady.Max != 100 || r.ToLocator.Max != 40 || r.ToFirstCondition.Max != 10 {
		t.Fatalf("distributions = %+v %+v %+v; only the new account counts", r.ToFirstCondition, r.ToLocator, r.ToReady)
	}
}
