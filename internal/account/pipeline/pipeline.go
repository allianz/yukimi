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

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	corev1 "k8s.io/api/core/v1"
)

// Pipeline runs an ordered list of modules against one ModuleContext per call.
type Pipeline struct {
	modules []Module
}

// New builds a pipeline from an ordered module list, e.g.
// New(guardrailcheck, account, network, auth). Registration order
// is execution order for Observe and Apply, and its reverse for Destroy.
// Exactly one module must be the account module, identified by
// Name() == AccountModuleName: its Observe outcome alone decides whether the
// resource exists, and every module that calls ModuleContext.TenantDB must be
// registered after it, since TenantDB requires the locator only its Apply
// sets. The account module need not be registered first overall — a module
// needing no Snowflake connection (for example, a quota-check admission gate
// that must stop the run before the account is ever created) may run earlier.
func New(modules ...Module) *Pipeline {
	return &Pipeline{modules: modules}
}

// Outcomes is every module's Outcome from one run, in execution order. All
// derived values are computed here, nowhere else.
type Outcomes []Outcome

// Err returns the first Failed outcome's Err, or nil. Only the first is
// returned; a later failure is visible only through its own Condition or
// Events.
func (o Outcomes) Err() error {
	for _, out := range o {
		if out.State == StateFailed {
			return out.Err
		}
	}
	return nil
}

// AllDone reports whether the list is non-empty and every entry is Done.
func (o Outcomes) AllDone() bool {
	if len(o) == 0 {
		return false
	}
	for _, out := range o {
		if out.State != StateDone {
			return false
		}
	}
	return true
}

// Conditions returns every module-owned condition, in order.
func (o Outcomes) Conditions() []xpv1.Condition {
	var conds []xpv1.Condition
	for _, out := range o {
		if out.Condition != nil {
			conds = append(conds, *out.Condition)
		}
	}
	return conds
}

// Events returns every module's events, in order.
func (o Outcomes) Events() []event.Event {
	var events []event.Event
	for _, out := range o {
		events = append(events, out.Events...)
	}
	return events
}

// PendingReason returns the first Pending outcome's Reason, or "" if none is
// Pending. A Failed outcome never contributes: errors belong on Synced only.
func (o Outcomes) PendingReason() string {
	for _, out := range o {
		if out.State == StatePending {
			return out.Reason
		}
	}
	return ""
}

// Report is what the controller renders onto the resource after any run.
type Report interface {
	Events() []event.Event
	Conditions() []xpv1.Condition // module-owned conditions only
	Ready() xpv1.Condition        // the resource's aggregate Ready condition
}

var (
	_ Report = Observation{}
	_ Report = Result{}
)

// readyLatched reports whether the CR's persisted Ready condition is True.
func readyLatched(mc *ModuleContext) bool {
	return mc.CR().GetCondition(xpv1.TypeReady).Status == corev1.ConditionTrue
}

// ready is the one place the aggregate Ready condition is built: Available
// when latched or satisfied, otherwise Unavailable with the first pending
// reason.
func ready(latched, satisfied bool, o Outcomes) xpv1.Condition {
	if latched || satisfied {
		return xpv1.Available()
	}
	return xpv1.Unavailable().WithMessage(o.PendingReason())
}

// Observation is Pipeline.Observe's result.
type Observation struct {
	Outcomes
	generationApplied bool // snapshot at run start: observedGeneration == generation
	readyLatched      bool // snapshot at run start: persisted Ready == True
}

// ExternalObservation is what the controller returns from Observe. Errors
// affect neither field; they are returned separately (Err).
func (o Observation) ExternalObservation() managed.ExternalObservation {
	return managed.ExternalObservation{ResourceExists: o.exists(), ResourceUpToDate: o.upToDate()}
}

// Ready is Available when Ready was already persisted True (the latch), and
// otherwise Unavailable with the first pending reason. Observe never flips
// Ready to True for the first time; only a complete Apply does.
func (o Observation) Ready() xpv1.Condition { return ready(o.readyLatched, false, o.Outcomes) }

// exists reports whether the account module's outcome is Done or Drifted.
func (o Observation) exists() bool {
	for _, out := range o.Outcomes {
		if out.Module == AccountModuleName {
			return out.State == StateDone || out.State == StateDrifted
		}
	}
	return false
}

// upToDate reports whether the current generation was applied and no module
// found drift.
func (o Observation) upToDate() bool {
	if !o.generationApplied {
		return false
	}
	for _, out := range o.Outcomes {
		if out.State == StateDrifted {
			return false
		}
	}
	return true
}

// Result is Pipeline.Apply's result.
type Result struct {
	Outcomes
	readyLatched bool // snapshot at run start: persisted Ready == True
}

// Ready is Available when Ready was already persisted True or the run
// completed, and otherwise Unavailable with the first pending reason.
func (r Result) Ready() xpv1.Condition { return ready(r.readyLatched, r.complete(), r.Outcomes) }

// complete reports that the generation counts as applied: every module ran and
// was Done. A stopped run always contains a non-Done aborting outcome.
func (r Result) complete() bool { return r.AllDone() }

// Observe calls every module's Observe in order and collects the outcomes. It
// performs no mutation of its own and never stops early.
func (p *Pipeline) Observe(ctx context.Context, mc *ModuleContext) Observation {
	cr := mc.CR()
	obs := Observation{
		generationApplied: cr.Status.GetObservedGeneration() == cr.Generation,
		readyLatched:      readyLatched(mc),
	}
	for _, m := range p.modules {
		out := m.Observe(ctx, mc)
		out.Module = m.Name()
		obs.Outcomes = append(obs.Outcomes, out)
	}
	return obs
}

// Apply calls every module's Apply in order, stopping early only after a
// non-Done outcome marked Abort. It sets status.observedGeneration iff the run
// completed. It is idempotent by construction — callers may call it from both
// a create and an update path with identical behavior.
func (p *Pipeline) Apply(ctx context.Context, mc *ModuleContext) Result {
	result := Result{readyLatched: readyLatched(mc)}
	for _, m := range p.modules {
		out := m.Apply(ctx, mc)
		out.Module = m.Name()
		result.Outcomes = append(result.Outcomes, out)
		if out.Aborted {
			break
		}
	}
	if result.complete() {
		cr := mc.CR()
		cr.Status.SetObservedGeneration(cr.Generation)
	}
	return result
}

// Destroy calls every module's Teardown in reverse registration order, so
// every module registered after the account module tears down before the
// account itself is dropped.
//
// A nil return means every teardown was accepted. It does not mean the
// external state is gone: the account and its credential may both still be
// inside their restore windows.
//
// Returns:
//   - error: the first Teardown error, returned unchanged and already
//     classified by the module that produced it. No later Teardown runs.
func (p *Pipeline) Destroy(ctx context.Context, mc *ModuleContext) error {
	for i := len(p.modules) - 1; i >= 0; i-- {
		if err := p.modules[i].Teardown(ctx, mc); err != nil {
			return err
		}
	}
	return nil
}
