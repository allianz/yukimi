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

package account

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// fakeModule records call order and returns scripted, single-shot Observe
// and Apply results. It never touches mc, so callers may pass nil.
type fakeModule struct {
	name          string
	observeInSync bool
	applyOut      Outcome
	order         *[]string
	applyCalled   int
}

func (f *fakeModule) Name() string { return f.name }

func (f *fakeModule) Observe(ctx context.Context, mc *ModuleContext) (bool, Outcome) {
	*f.order = append(*f.order, "observe:"+f.name)
	return f.observeInSync, Done()
}

func (f *fakeModule) Apply(ctx context.Context, mc *ModuleContext) Outcome {
	*f.order = append(*f.order, "apply:"+f.name)
	f.applyCalled++
	return f.applyOut
}

// SC-001: New preserves registration order; Apply calls each module's Apply
// in that exact order.
func TestApply_PreservesOrder(t *testing.T) {
	var order []string
	m1 := &fakeModule{name: "m1", order: &order, applyOut: Done()}
	m2 := &fakeModule{name: "m2", order: &order, applyOut: Done()}
	m3 := &fakeModule{name: "m3", order: &order, applyOut: Done()}

	p := New(m1, m2, m3)
	result, err := p.Apply(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantOrder := []string{"apply:m1", "apply:m2", "apply:m3"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Errorf("call order = %v, want %v", order, wantOrder)
	}

	var gotNames []string
	for _, mo := range result.Outcomes {
		gotNames = append(gotNames, mo.Module)
	}
	wantNames := []string{"m1", "m2", "m3"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Errorf("Outcomes module order = %v, want %v", gotNames, wantNames)
	}
}

// SC-002: Observation.Exists reflects only modules[0]'s Observe result,
// regardless of what later modules report.
func TestObserve_ExistsFromModule0Only(t *testing.T) {
	cases := []struct {
		name       string
		inSyncs    []bool
		wantExists bool
	}{
		{"module0 true, later false", []bool{true, false, true}, true},
		{"module0 false, later true", []bool{false, true, true}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			var modules []Module
			for i, inSync := range tc.inSyncs {
				modules = append(modules, &fakeModule{name: string(rune('a' + i)), observeInSync: inSync, order: &order})
			}
			p := New(modules...)
			obs, err := p.Observe(context.Background(), nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if obs.Exists != tc.wantExists {
				t.Errorf("Exists = %v, want %v", obs.Exists, tc.wantExists)
			}
		})
	}
}

// SC-003: Observation.InSync is true iff every module's Observe returned
// inSync == true.
func TestObserve_InSyncRequiresAll(t *testing.T) {
	cases := []struct {
		name       string
		inSyncs    []bool
		wantInSync bool
	}{
		{"all true", []bool{true, true, true}, true},
		{"module0 false", []bool{false, true, true}, false},
		{"middle false", []bool{true, false, true}, false},
		{"last false", []bool{true, true, false}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			var modules []Module
			for i, inSync := range tc.inSyncs {
				modules = append(modules, &fakeModule{name: string(rune('a' + i)), observeInSync: inSync, order: &order})
			}
			p := New(modules...)
			obs, err := p.Observe(context.Background(), nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if obs.InSync != tc.wantInSync {
				t.Errorf("InSync = %v, want %v", obs.InSync, tc.wantInSync)
			}
		})
	}
}

// SC-004: An Outcome with Abort == true stops Pipeline.Apply immediately
// after that module; Result.Aborted is true and Result.Outcomes contains no
// entry for any later module.
func TestApply_AbortStopsEarly(t *testing.T) {
	var order []string
	m1 := &fakeModule{name: "m1", order: &order, applyOut: Done()}
	m2 := &fakeModule{name: "m2", order: &order, applyOut: Rejected(errors.New("bad")).Aborting()}
	m3 := &fakeModule{name: "m3", order: &order, applyOut: Done()}

	p := New(m1, m2, m3)
	result, err := p.Apply(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Aborted {
		t.Error("Aborted = false, want true")
	}
	if len(result.Outcomes) != 2 {
		t.Fatalf("len(Outcomes) = %d, want 2", len(result.Outcomes))
	}
	if !result.Outcomes[1].Outcome.Abort {
		t.Error("last recorded outcome should have Abort == true")
	}
	if m3.applyCalled != 0 {
		t.Errorf("m3.Apply was called %d times, want 0", m3.applyCalled)
	}
}

// SC-005: A non-aborting Outcome (Rejected, Failed, or Pending) from any
// module does not prevent later modules from running.
func TestApply_NonAbortingOutcomesDontStopLaterModules(t *testing.T) {
	var order []string
	m1 := &fakeModule{name: "m1", order: &order, applyOut: Done()}
	m2 := &fakeModule{name: "m2", order: &order, applyOut: Rejected(errors.New("rejected"))}
	m3 := &fakeModule{name: "m3", order: &order, applyOut: Failed(errors.New("failed"))}
	m4 := &fakeModule{name: "m4", order: &order, applyOut: Pending("waiting")}

	p := New(m1, m2, m3, m4)
	result, err := p.Apply(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Aborted {
		t.Error("Aborted = true, want false")
	}
	if len(result.Outcomes) != 4 {
		t.Fatalf("len(Outcomes) = %d, want 4", len(result.Outcomes))
	}
	for _, m := range []*fakeModule{m1, m2, m3, m4} {
		if m.applyCalled != 1 {
			t.Errorf("%s.Apply was called %d times, want 1", m.name, m.applyCalled)
		}
	}
}

// SC-008: Result.AllDone is true iff Outcomes is non-empty, Aborted is
// false, and every entry's State is StateDone.
func TestResult_AllDone(t *testing.T) {
	cases := []struct {
		name   string
		result Result
		want   bool
	}{
		{"empty", Result{}, false},
		{"aborted with all-done outcomes", Result{Aborted: true, Outcomes: []ModuleOutcome{{Module: "m1", Outcome: Done()}}}, false},
		{"one non-done among dones", Result{Outcomes: []ModuleOutcome{{Module: "m1", Outcome: Done()}, {Module: "m2", Outcome: Pending("x")}}}, false},
		{"non-empty all-done not aborted", Result{Outcomes: []ModuleOutcome{{Module: "m1", Outcome: Done()}, {Module: "m2", Outcome: Done()}}}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.result.AllDone(); got != tc.want {
				t.Errorf("AllDone() = %v, want %v", got, tc.want)
			}
		})
	}
}
