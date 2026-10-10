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

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
)

// fakeModule records call order and returns scripted Observe, Apply and
// Teardown results. It never touches mc.
type fakeModule struct {
	name          string
	observeOut    Outcome
	applyOut      Outcome
	teardownErr   error
	order         *[]string
	applyCalled   int
	teardownCalls int
}

func (f *fakeModule) Name() string { return f.name }

func (f *fakeModule) Observe(ctx context.Context, mc *ModuleContext) Outcome {
	*f.order = append(*f.order, "observe:"+f.name)
	return f.observeOut
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

// newMC builds a ModuleContext around a CR at the given generation, with
// observedGeneration and Ready as scripted.
func newMC(generation, observed int64, ready bool) *ModuleContext {
	cr := &v1alpha1.SnowflakeAccount{ObjectMeta: metav1.ObjectMeta{Name: "acc", Namespace: "ns", Generation: generation}}
	cr.Status.SetObservedGeneration(observed)
	if ready {
		cr.SetConditions(xpv1.Available())
	}
	return NewModuleContext(cr, nil, nil, nil)
}

// SC-001: New preserves registration order; Apply calls each module's Apply
// in that exact order.
func TestApply_PreservesOrder(t *testing.T) {
	var order []string
	m1 := &fakeModule{name: "m1", order: &order, applyOut: Done()}
	m2 := &fakeModule{name: "m2", order: &order, applyOut: Done()}
	m3 := &fakeModule{name: "m3", order: &order, applyOut: Done()}

	result := New(m1, m2, m3).Apply(context.Background(), newMC(1, 0, false))

	wantOrder := []string{"apply:m1", "apply:m2", "apply:m3"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Errorf("call order = %v, want %v", order, wantOrder)
	}

	// SC-007: the pipeline stamps Outcome.Module.
	var gotNames []string
	for _, o := range result.Outcomes {
		gotNames = append(gotNames, o.Module)
	}
	if want := []string{"m1", "m2", "m3"}; !reflect.DeepEqual(gotNames, want) {
		t.Errorf("Outcomes module order = %v, want %v", gotNames, want)
	}
}

// SC-002: ResourceExists reflects only the account module's outcome (Done or
// Drifted), regardless of its position or what other modules report.
func TestObserve_ExistsFromAccountModuleByName(t *testing.T) {
	cases := []struct {
		name         string
		accountIndex int
		states       []Outcome
		wantExists   bool
	}{
		{"account first, Done; others Pending", 0, []Outcome{Done(), Pending("x"), Pending("y")}, true},
		{"account first, Drifted", 0, []Outcome{Drifted(), Done(), Done()}, true},
		{"account first, Pending; others Done", 0, []Outcome{Pending("x"), Done(), Done()}, false},
		{"account in the middle, Done", 1, []Outcome{Pending("x"), Done(), Failed(errors.New("e"))}, true},
		{"account last, Pending", 2, []Outcome{Done(), Done(), Pending("x")}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			var modules []Module
			for i, out := range tc.states {
				name := string(rune('a' + i))
				if i == tc.accountIndex {
					name = AccountModuleName
				}
				modules = append(modules, &fakeModule{name: name, observeOut: out, order: &order})
			}
			obs := New(modules...).Observe(context.Background(), newMC(1, 1, false))
			if got := obs.ExternalObservation().ResourceExists; got != tc.wantExists {
				t.Errorf("ResourceExists = %v, want %v", got, tc.wantExists)
			}
		})
	}
}

// SC-003: ResourceUpToDate needs the generation applied and no Drifted
// outcome; Pending and Failed do not affect it.
func TestObserve_UpToDate(t *testing.T) {
	cases := []struct {
		name       string
		generation int64
		observed   int64
		outcomes   []Outcome
		want       bool
	}{
		{"applied, all Done", 2, 2, []Outcome{Done(), Done()}, true},
		{"generation newer", 3, 2, []Outcome{Done(), Done()}, false},
		{"applied, one Drifted", 2, 2, []Outcome{Done(), Drifted()}, false},
		{"applied, Pending does not matter", 2, 2, []Outcome{Done(), Pending("x")}, true},
		{"applied, Failed does not matter", 2, 2, []Outcome{Done(), Failed(errors.New("e"))}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			var modules []Module
			for i, out := range tc.outcomes {
				name := string(rune('a' + i))
				if i == 0 {
					name = AccountModuleName
				}
				modules = append(modules, &fakeModule{name: name, observeOut: out, order: &order})
			}
			obs := New(modules...).Observe(context.Background(), newMC(tc.generation, tc.observed, false))
			if got := obs.ExternalObservation().ResourceUpToDate; got != tc.want {
				t.Errorf("ResourceUpToDate = %v, want %v", got, tc.want)
			}
		})
	}
}

// SC-017: Observation.Outcomes contains exactly one entry per registered
// module, in registration order, matching what each module's Observe
// returned.
func TestObserve_PopulatesOutcomesInOrder(t *testing.T) {
	var order []string
	wantErr := errors.New("bad")
	m1 := &fakeModule{name: "m1", order: &order, observeOut: Pending("waiting")}
	m2 := &fakeModule{name: "m2", order: &order, observeOut: Failed(wantErr)}
	m3 := &fakeModule{name: "m3", order: &order, observeOut: Done()}

	obs := New(m1, m2, m3).Observe(context.Background(), newMC(1, 1, false))

	if len(obs.Outcomes) != 3 {
		t.Fatalf("len(Outcomes) = %d, want 3", len(obs.Outcomes))
	}
	for i, want := range []string{"m1", "m2", "m3"} {
		if obs.Outcomes[i].Module != want {
			t.Errorf("Outcomes[%d].Module = %q, want %q", i, obs.Outcomes[i].Module, want)
		}
	}
	if o := obs.Outcomes[0]; o.State != StatePending || o.Reason != "waiting" {
		t.Errorf("Outcomes[0] = %+v, want Pending(\"waiting\")", o)
	}
	if o := obs.Outcomes[1]; o.State != StateFailed || o.Err != wantErr {
		t.Errorf("Outcomes[1] = %+v, want Failed(wantErr)", o)
	}
	if o := obs.Outcomes[2]; o.State != StateDone {
		t.Errorf("Outcomes[2] = %+v, want Done()", o)
	}
}

// SC-018: an aborting outcome in Observe has no effect on control flow —
// every later module still runs and is recorded.
func TestObserve_AbortDoesNotStopEarly(t *testing.T) {
	var order []string
	m1 := &fakeModule{name: "m1", order: &order, observeOut: Done()}
	m2 := &fakeModule{name: "m2", order: &order, observeOut: Failed(errors.New("bad")).Abort()}
	m3 := &fakeModule{name: "m3", order: &order, observeOut: Done()}

	obs := New(m1, m2, m3).Observe(context.Background(), newMC(1, 1, false))

	want := []string{"observe:m1", "observe:m2", "observe:m3"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("call order = %v, want %v — Observe must not stop early on Abort", order, want)
	}
	if len(obs.Outcomes) != 3 {
		t.Fatalf("len(Outcomes) = %d, want 3", len(obs.Outcomes))
	}
}

// SC-004: a non-Done outcome marked Abort stops Pipeline.Apply immediately
// after that module; Result.Outcomes has no entry for any later module.
func TestApply_AbortStopsEarly(t *testing.T) {
	for name, out := range map[string]Outcome{
		"Pending": Pending("wait").Abort(),
		"Failed":  Failed(errors.New("bad")).Abort(),
		"Drifted": Drifted().Abort(),
	} {
		t.Run(name, func(t *testing.T) {
			var order []string
			m1 := &fakeModule{name: "m1", order: &order, applyOut: Done()}
			m2 := &fakeModule{name: "m2", order: &order, applyOut: out}
			m3 := &fakeModule{name: "m3", order: &order, applyOut: Done()}

			mc := newMC(2, 1, false)
			result := New(m1, m2, m3).Apply(context.Background(), mc)

			if len(result.Outcomes) != 2 {
				t.Fatalf("len(Outcomes) = %d, want 2", len(result.Outcomes))
			}
			if m3.applyCalled != 0 {
				t.Errorf("m3.Apply was called %d times, want 0", m3.applyCalled)
			}
			if got := mc.CR().Status.GetObservedGeneration(); got != 1 {
				t.Errorf("observedGeneration = %d, want 1 (unchanged)", got)
			}
		})
	}
}

// Abort on a Done outcome is a no-op: the run continues and can complete.
func TestApply_AbortOnDoneContinues(t *testing.T) {
	var order []string
	m1 := &fakeModule{name: "m1", order: &order, applyOut: Done().Abort()}
	m2 := &fakeModule{name: "m2", order: &order, applyOut: Done()}

	mc := newMC(1, 0, false)
	result := New(m1, m2).Apply(context.Background(), mc)
	if len(result.Outcomes) != 2 || m2.applyCalled != 1 {
		t.Errorf("outcomes = %d, m2.applyCalled = %d, want 2 and 1", len(result.Outcomes), m2.applyCalled)
	}
	if got := mc.CR().Status.GetObservedGeneration(); got != 1 {
		t.Errorf("observedGeneration = %d, want 1", got)
	}
}

// SC-005: a non-Done Outcome that is not marked Abort does not prevent
// later modules from running.
func TestApply_NonAbortingOutcomesDontStopLaterModules(t *testing.T) {
	var order []string
	m1 := &fakeModule{name: "m1", order: &order, applyOut: Done()}
	m2 := &fakeModule{name: "m2", order: &order, applyOut: Failed(errors.New("rejected"))}
	m3 := &fakeModule{name: "m3", order: &order, applyOut: Failed(errors.New("failed"))}
	m4 := &fakeModule{name: "m4", order: &order, applyOut: Pending("waiting")}

	result := New(m1, m2, m3, m4).Apply(context.Background(), newMC(1, 0, false))

	if len(result.Outcomes) != 4 {
		t.Fatalf("len(Outcomes) = %d, want 4", len(result.Outcomes))
	}
	for _, m := range []*fakeModule{m1, m2, m3, m4} {
		if m.applyCalled != 1 {
			t.Errorf("%s.Apply was called %d times, want 1", m.name, m.applyCalled)
		}
	}
}

// SC-023: Apply sets status.observedGeneration iff every module ran and was
// Done.
func TestApply_ObservedGeneration(t *testing.T) {
	cases := []struct {
		name string
		outs []Outcome
		want int64
	}{
		{"all Done", []Outcome{Done(), Done()}, 5},
		{"one Pending", []Outcome{Done(), Pending("x")}, 4},
		{"one Failed", []Outcome{Failed(errors.New("e")), Done()}, 4},
		{"one Drifted", []Outcome{Done(), Drifted()}, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			var modules []Module
			for i, out := range tc.outs {
				modules = append(modules, &fakeModule{name: string(rune('a' + i)), applyOut: out, order: &order})
			}
			mc := newMC(5, 4, false)
			New(modules...).Apply(context.Background(), mc)
			if got := mc.CR().Status.GetObservedGeneration(); got != tc.want {
				t.Errorf("observedGeneration = %d, want %d", got, tc.want)
			}
		})
	}
}

// SC-022: Ready is Available when latched (or, for Result, complete); otherwise
// Unavailable with the first Pending reason.
func TestReady(t *testing.T) {
	var order []string
	pending := &fakeModule{name: "p", order: &order, observeOut: Pending("waiting for X"), applyOut: Pending("waiting for X")}
	failed := &fakeModule{name: "f", order: &order, observeOut: Failed(errors.New("boom")), applyOut: Failed(errors.New("boom"))}
	done := &fakeModule{name: "d", order: &order, observeOut: Done(), applyOut: Done()}

	cases := []struct {
		name      string
		modules   []Module
		latched   bool
		wantReady bool
		wantMsg   string
	}{
		{"not latched, pending", []Module{done, pending}, false, false, "waiting for X"},
		{"latched, pending", []Module{done, pending}, true, true, ""},
		{"not latched, failed contributes no message", []Module{failed, done}, false, false, ""},
		{"not latched, first pending wins", []Module{pending, failed}, false, false, "waiting for X"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := New(tc.modules...)
			for _, c := range []xpv1.Condition{
				p.Observe(context.Background(), newMC(1, 1, tc.latched)).Ready(),
				p.Apply(context.Background(), newMC(1, 0, tc.latched)).Ready(),
			} {
				gotReady := c.Status == corev1.ConditionTrue
				if gotReady != tc.wantReady || (!gotReady && c.Message != tc.wantMsg) {
					t.Errorf("Ready = %+v, want ready=%v msg=%q", c, tc.wantReady, tc.wantMsg)
				}
			}
		})
	}

	// A complete Apply flips Ready for the first time; Observe never does.
	p := New(done)
	if c := p.Apply(context.Background(), newMC(1, 0, false)).Ready(); c.Status != corev1.ConditionTrue {
		t.Errorf("complete Apply Ready = %+v, want Available", c)
	}
	if c := p.Observe(context.Background(), newMC(1, 1, false)).Ready(); c.Status == corev1.ConditionTrue {
		t.Errorf("Observe Ready = %+v, want not Available while unlatched", c)
	}
}

// SC-008: Outcomes.AllDone is true iff non-empty and every entry is Done.
func TestOutcomes_AllDone(t *testing.T) {
	cases := []struct {
		name string
		o    Outcomes
		want bool
	}{
		{"empty", nil, false},
		{"one non-done among dones", Outcomes{Done(), Pending("x")}, false},
		{"drifted", Outcomes{Done(), Drifted()}, false},
		{"all done", Outcomes{Done(), Done()}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.o.AllDone(); got != tc.want {
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

	if err := New(m1, m2, m3).Destroy(context.Background(), nil); err != nil {
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

	err := New(m1, m2, m3).Destroy(context.Background(), nil)
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

// SC-020: PendingReason returns the first Pending outcome's Reason; a Failed
// outcome never contributes; "" if none is Pending.
func TestOutcomes_PendingReason(t *testing.T) {
	cases := []struct {
		name string
		o    Outcomes
		want string
	}{
		{"first pending", Outcomes{Done(), Pending("waiting on giam sync")}, "waiting on giam sync"},
		{"none pending", Outcomes{Done(), Failed(errors.New("bad cidr"))}, ""},
		{"multiple pending uses first", Outcomes{Pending("first"), Pending("second")}, "first"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.o.PendingReason(); got != tc.want {
				t.Errorf("PendingReason() = %q, want %q", got, tc.want)
			}
		})
	}
}

// SC-021: Outcomes.Err returns the first Failed outcome's Err, or nil.
func TestOutcomes_Err(t *testing.T) {
	firstErr := errors.New("rejected")
	secondErr := errors.New("failed")
	if got := (Outcomes{Done(), Failed(firstErr), Failed(secondErr)}).Err(); got != firstErr {
		t.Errorf("Err() = %v, want the first failing outcome's Err (%v)", got, firstErr)
	}
	if got := (Outcomes{Done(), Pending("waiting")}).Err(); got != nil {
		t.Errorf("Err() = %v, want nil", got)
	}
}

// SC-019: Events and Conditions survive unchanged through Outcomes, in order,
// independent of State.
func TestOutcomes_ConditionsAndEvents(t *testing.T) {
	c1 := xpv1.Condition{Type: TypeIdentitySynced, Status: corev1.ConditionFalse, Reason: "SyncPending"}
	c2 := xpv1.Condition{Type: TypeQuotaAvailable, Status: corev1.ConditionTrue, Reason: "Available"}
	e1 := event.Normal("A", "one")
	e2 := event.Warning("B", errors.New("two"))
	e3 := event.Normal("C", "three")

	o := Outcomes{
		Pending("x").WithCondition(c1).WithEvent(e1),
		Done(),
		Failed(errors.New("e")).WithEvent(e2).WithEvent(e3).WithCondition(c2),
	}

	conds := o.Conditions()
	if len(conds) != 2 || conds[0].Type != c1.Type || conds[1].Type != c2.Type {
		t.Errorf("Conditions() = %+v, want [c1 c2] in order", conds)
	}
	events := o.Events()
	if len(events) != 3 || events[0].Reason != "A" || events[1].Reason != "B" || events[2].Reason != "C" {
		t.Errorf("Events() = %+v, want [A B C] in order", events)
	}
	if got := (Outcomes{Done()}).Conditions(); len(got) != 0 {
		t.Errorf("Conditions() on none = %+v, want empty", got)
	}
}
