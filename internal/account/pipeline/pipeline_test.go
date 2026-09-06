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

package pipeline

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// fakeModule records call order and returns scripted, single-shot Observe,
// Apply and Teardown results. It never touches mc, so callers may pass nil.
type fakeModule struct {
	name          string
	observeInSync bool
	observeOut    Outcome // returned by Observe; zero value is Outcome{} == Done()
	applyOut      Outcome
	teardownErr   error
	order         *[]string
	applyCalled   int
	teardownCalls int
}

func (f *fakeModule) Name() string { return f.name }

func (f *fakeModule) Observe(ctx context.Context, mc *ModuleContext) (bool, Outcome) {
	*f.order = append(*f.order, "observe:"+f.name)
	return f.observeInSync, f.observeOut
}

func (f *fakeModule) Apply(ctx context.Context, mc *ModuleContext) Outcome {
	*f.order = append(*f.order, "apply:"+f.name)
	f.applyCalled++
	return f.applyOut
}

func (f *fakeModule) Teardown(ctx context.Context, mc *ModuleContext) error {
	*f.order = append(*f.order, "teardown:"+f.name)
	f.teardownCalls++
	return f.teardownErr
}

// SC-001: New preserves registration order; Apply calls each module's Apply
// in that exact order.
func TestApply_PreservesOrder(t *testing.T) {
	var order []string
	m1 := &fakeModule{name: "m1", order: &order, applyOut: Done()}
	m2 := &fakeModule{name: "m2", order: &order, applyOut: Done()}
	m3 := &fakeModule{name: "m3", order: &order, applyOut: Done()}

	p := New(m1, m2, m3)
	result := p.Apply(context.Background(), nil)

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

// SC-002: Observation.Exists reflects only the account module's
// (Name() == AccountModuleName) Observe result, regardless of its position
// in the registered list or what later modules report.
func TestObserve_ExistsFromAccountModuleByName(t *testing.T) {
	cases := []struct {
		name         string
		accountIndex int // position of the AccountModuleName fake within inSyncs
		inSyncs      []bool
		wantExists   bool
	}{
		{"account module first, true; others false", 0, []bool{true, false, true}, true},
		{"account module first, false; others true", 0, []bool{false, true, true}, false},
		{"account module in the middle, true", 1, []bool{false, true, false}, true},
		{"account module last, false", 2, []bool{true, true, false}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			var modules []Module
			for i, inSync := range tc.inSyncs {
				name := string(rune('a' + i))
				if i == tc.accountIndex {
					name = AccountModuleName
				}
				modules = append(modules, &fakeModule{name: name, observeInSync: inSync, order: &order})
			}
			p := New(modules...)
			obs := p.Observe(context.Background(), nil)
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
			obs := p.Observe(context.Background(), nil)
			if obs.InSync != tc.wantInSync {
				t.Errorf("InSync = %v, want %v", obs.InSync, tc.wantInSync)
			}
		})
	}
}

// SC-017: Observation.Outcomes contains exactly one entry per registered
// module, in registration order, matching what each module's Observe
// returned.
func TestObserve_PopulatesOutcomesInOrder(t *testing.T) {
	var order []string
	m1 := &fakeModule{name: "m1", order: &order, observeOut: Pending("waiting")}
	wantErr := errors.New("bad")
	m2 := &fakeModule{name: "m2", order: &order, observeOut: Rejected(wantErr)}
	m3 := &fakeModule{name: "m3", order: &order, observeOut: Done()}

	p := New(m1, m2, m3)
	obs := p.Observe(context.Background(), nil)

	if len(obs.Outcomes) != 3 {
		t.Fatalf("len(Outcomes) = %d, want 3", len(obs.Outcomes))
	}

	wantNames := []string{"m1", "m2", "m3"}
	for i, want := range wantNames {
		if obs.Outcomes[i].Module != want {
			t.Errorf("Outcomes[%d].Module = %q, want %q", i, obs.Outcomes[i].Module, want)
		}
	}

	if obs.Outcomes[0].Outcome.State != StatePending || obs.Outcomes[0].Outcome.Reason != "waiting" {
		t.Errorf("Outcomes[0].Outcome = %+v, want Pending(\"waiting\")", obs.Outcomes[0].Outcome)
	}
	if obs.Outcomes[1].Outcome.State != StateRejected || obs.Outcomes[1].Outcome.Err != wantErr {
		t.Errorf("Outcomes[1].Outcome = %+v, want Rejected(wantErr)", obs.Outcomes[1].Outcome)
	}
	if obs.Outcomes[2].Outcome.State != StateDone {
		t.Errorf("Outcomes[2].Outcome = %+v, want Done()", obs.Outcomes[2].Outcome)
	}
}

// SC-018: An Outcome.Abort == true returned from any module's Observe has no
// effect on Pipeline.Observe's control flow — every later module still runs
// and is still recorded in Observation.Outcomes.
func TestObserve_AbortHasNoEffect(t *testing.T) {
	var order []string
	m1 := &fakeModule{name: "m1", order: &order, observeInSync: true}
	m2 := &fakeModule{name: "m2", order: &order, observeInSync: false,
		observeOut: Rejected(errors.New("bad")).Aborting()}
	m3 := &fakeModule{name: "m3", order: &order, observeInSync: true}

	p := New(m1, m2, m3)
	obs := p.Observe(context.Background(), nil)

	wantOrder := []string{"observe:m1", "observe:m2", "observe:m3"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Errorf("call order = %v, want %v — Observe must not stop early on Abort", order, wantOrder)
	}
	if len(obs.Outcomes) != 3 {
		t.Fatalf("len(Outcomes) = %d, want 3", len(obs.Outcomes))
	}
	if !obs.Outcomes[1].Outcome.Abort {
		t.Error("recorded outcome should still carry Abort == true even though it had no effect")
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
	result := p.Apply(context.Background(), nil)

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
	result := p.Apply(context.Background(), nil)

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

// SC-012: Pipeline.Destroy calls each module's Teardown in the exact reverse
// of registration order.
func TestDestroy_ReverseOrder(t *testing.T) {
	var order []string
	m1 := &fakeModule{name: "m1", order: &order}
	m2 := &fakeModule{name: "m2", order: &order}
	m3 := &fakeModule{name: "m3", order: &order}

	p := New(m1, m2, m3)
	if err := p.Destroy(context.Background(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantOrder := []string{"teardown:m3", "teardown:m2", "teardown:m1"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Errorf("call order = %v, want %v", order, wantOrder)
	}
}

// SC-013: Destroy stops at the first Teardown error, returns it unchanged,
// and calls Teardown on no earlier-registered module.
func TestDestroy_StopsAtFirstError(t *testing.T) {
	var order []string
	wantErr := errors.New("teardown failed")
	m1 := &fakeModule{name: "m1", order: &order}
	m2 := &fakeModule{name: "m2", order: &order, teardownErr: wantErr}
	m3 := &fakeModule{name: "m3", order: &order}

	p := New(m1, m2, m3)
	err := p.Destroy(context.Background(), nil)
	if err != wantErr {
		t.Errorf("err = %v, want %v", err, wantErr)
	}

	wantOrder := []string{"teardown:m3", "teardown:m2"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Errorf("call order = %v, want %v", order, wantOrder)
	}
	if m1.teardownCalls != 0 {
		t.Errorf("m1.Teardown was called %d times, want 0", m1.teardownCalls)
	}
}

// PendingReason returns the first Pending outcome's Reason.
func TestPendingReason_ReturnsFirstPendingReason(t *testing.T) {
	outcomes := []ModuleOutcome{
		{Module: "account", Outcome: Done()},
		{Module: "identity", Outcome: Pending("waiting on giam sync")},
	}
	if got := (Observation{Outcomes: outcomes}).PendingReason(); got != "waiting on giam sync" {
		t.Errorf("PendingReason() = %q, want %q", got, "waiting on giam sync")
	}
}

// With no Pending outcome at all (only Done/Rejected/Failed), PendingReason
// is empty — that detail belongs on Synced, not here.
func TestPendingReason_EmptyWhenNonePending(t *testing.T) {
	outcomes := []ModuleOutcome{
		{Module: "account", Outcome: Done()},
		{Module: "network", Outcome: Rejected(errors.New("bad cidr"))},
	}
	if got := (Result{Outcomes: outcomes}).PendingReason(); got != "" {
		t.Errorf("PendingReason() = %q, want empty", got)
	}
}

// With more than one Pending outcome, the reason comes from the first one
// in outcome order.
func TestPendingReason_MultiplePending_UsesFirstInOrder(t *testing.T) {
	outcomes := []ModuleOutcome{
		{Module: "account", Outcome: Pending("waiting for the account to finish provisioning")},
		{Module: "identity", Outcome: Pending("waiting on giam sync")},
	}
	got := (Observation{Outcomes: outcomes}).PendingReason()
	if got != "waiting for the account to finish provisioning" {
		t.Errorf("PendingReason() = %q, want the first Pending outcome's Reason", got)
	}
}
