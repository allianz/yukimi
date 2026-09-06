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

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
)

// Pipeline runs an ordered list of modules against one ModuleContext per call.
type Pipeline struct {
	modules []Module
}

// New builds a pipeline from an ordered module list. Registration order is
// execution order for Observe and Apply, and its reverse for Destroy. Exactly one module must be the
// account module, identified by Name() == AccountModuleName: its Observe
// result is the sole source of Observation.Exists, and every module that
// calls ModuleContext.TenantDB must be registered after it, since TenantDB
// requires the locator only its Apply sets. The account module need not be
// registered first overall — a module needing no Snowflake connection (for
// example, a quota-check admission gate that must abort before the account
// is ever created) may run earlier.
func New(modules ...Module) *Pipeline {
	return &Pipeline{modules: modules}
}

// Observation is Pipeline.Observe's result.
type Observation struct {
	Exists   bool            // from the account module's Observe alone (Name() == AccountModuleName); no other module contributes to it
	InSync   bool            // true iff every module's Observe reported inSync == true
	Outcomes []ModuleOutcome // one entry per registered module, in registration order — always all of them, since Observe never stops early
}

// Observe calls every module's Observe in order and aggregates the result. It
// performs no mutation of its own. Every module's Outcome is recorded in
// Observation.Outcomes regardless of its content — an Outcome.Abort returned
// here is ignored; only Apply honors Abort.
//
// It never returns an error: no module's Observe can produce one — every
// failure a module reports already lives in its own Outcome.
func (p *Pipeline) Observe(ctx context.Context, mc *ModuleContext) Observation {
	obs := Observation{InSync: true}
	for _, m := range p.modules {
		inSync, outcome := m.Observe(ctx, mc)
		obs.Outcomes = append(obs.Outcomes, ModuleOutcome{Module: m.Name(), Outcome: outcome})
		if m.Name() == AccountModuleName {
			obs.Exists = inSync
		}
		if !inSync {
			obs.InSync = false
		}
	}
	return obs
}

// Ready reports the resource's aggregate Ready condition from this Observe
// call's outcomes and the CRD's own persisted status (Key Concept: Ready Is
// a One-Way Latch).
func (o Observation) Ready(cr *v1alpha1.SnowflakeAccount) xpv1.Condition {
	return readyFrom(cr, o.Outcomes)
}

// Result is Pipeline.Apply's result.
type Result struct {
	Aborted  bool
	Outcomes []ModuleOutcome // one entry per module that actually ran, in execution order
}

// ModuleOutcome pairs a module's name with the Outcome it returned.
type ModuleOutcome struct {
	Module  string
	Outcome Outcome
}

// AllDone reports whether every module ran and every one reported StateDone.
func (r Result) AllDone() bool {
	if r.Aborted || len(r.Outcomes) == 0 {
		return false
	}
	for _, mo := range r.Outcomes {
		if mo.Outcome.State != StateDone {
			return false
		}
	}
	return true
}

// Ready reports the resource's aggregate Ready condition from this Apply
// call's outcomes and the CRD's own persisted status (Key Concept: Ready Is
// a One-Way Latch). Callers advancing status.observedGeneration on an
// all-Done run (AllDone) must do so before calling Ready, so a run that just
// completed its first fully-Done pass reports Ready=True immediately rather
// than waiting for the next Observe.
func (r Result) Ready(cr *v1alpha1.SnowflakeAccount) xpv1.Condition {
	return readyFrom(cr, r.Outcomes)
}

// readyFrom is a one-way latch: once cr.Status.GetObservedGeneration() is
// non-zero — meaning some earlier Apply saw every module report Done —
// Ready stays True from then on, regardless of what any later Observe or
// Apply reports (design.md §7.1, §4.3): a group added after that point and
// still syncing is visible on IdentitySynced, not here. Before that first
// success, Ready is False, carrying the first Pending outcome's Reason as
// its message so the tenant knows what they're waiting for. A Rejected or
// Failed outcome never sets a message here — that belongs on Synced (020's
// job, out of scope for this package).
//
// Deletion is not this package's concern: once the controller's Delete
// returns an error, the managed reconciler sets Ready to
// Deleting()/ReconcileError on its own, and neither Observe nor Apply runs
// during that window (design.md §6.3).
func readyFrom(cr *v1alpha1.SnowflakeAccount, outcomes []ModuleOutcome) xpv1.Condition {
	if cr.Status.GetObservedGeneration() != 0 {
		return xpv1.Available()
	}
	for _, mo := range outcomes {
		if mo.Outcome.State == StatePending {
			return xpv1.Unavailable().WithMessage(mo.Outcome.Reason)
		}
	}
	return xpv1.Unavailable()
}

// Apply calls every module's Apply in order, unconditionally, stopping early
// only if a module's Outcome has Abort set. It is idempotent by construction
// — callers may call it from both a create and an update path with identical
// behavior.
//
// It never returns an error: a module's own failure is already captured in
// its Outcome, and Result.Outcomes carries every one of them.
func (p *Pipeline) Apply(ctx context.Context, mc *ModuleContext) Result {
	var result Result
	for _, m := range p.modules {
		outcome := m.Apply(ctx, mc)
		result.Outcomes = append(result.Outcomes, ModuleOutcome{Module: m.Name(), Outcome: outcome})
		if outcome.Abort {
			result.Aborted = true
			break
		}
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
