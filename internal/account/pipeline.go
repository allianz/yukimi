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

import "context"

// Pipeline runs an ordered list of modules against one ModuleContext per call.
type Pipeline struct {
	modules []Module
}

// New builds a pipeline from an ordered module list. modules[0] must be the
// account module (010): its Observe result is the sole source of
// Observation.Exists, and every later module depends on the connection it
// establishes.
func New(modules ...Module) *Pipeline {
	return &Pipeline{modules: modules}
}

// Observation is Pipeline.Observe's result.
type Observation struct {
	Exists bool // from modules[0]'s Observe alone; no other module contributes to it
	InSync bool // true iff every module's Observe reported inSync == true
}

// Observe calls every module's Observe in order and aggregates the result. It
// performs no mutation of its own.
//
// Returns:
//   - error: always nil today. Reserved for a future structural failure
//     inside the pipeline itself; no module can produce one — every failure
//     a module reports already lives in its own Outcome.
func (p *Pipeline) Observe(ctx context.Context, mc *ModuleContext) (Observation, error) {
	obs := Observation{InSync: true}
	for i, m := range p.modules {
		inSync, _ := m.Observe(ctx, mc)
		if i == 0 {
			obs.Exists = inSync
		}
		if !inSync {
			obs.InSync = false
		}
	}
	return obs, nil
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

// Apply calls every module's Apply in order, unconditionally, stopping early
// only if a module's Outcome has Abort set. It is idempotent by construction
// — callers may call it from both a create and an update path with identical
// behavior.
//
// Returns:
//   - error: always nil today, for the same reason as Observe.
func (p *Pipeline) Apply(ctx context.Context, mc *ModuleContext) (Result, error) {
	var result Result
	for _, m := range p.modules {
		outcome := m.Apply(ctx, mc)
		result.Outcomes = append(result.Outcomes, ModuleOutcome{Module: m.Name(), Outcome: outcome})
		if outcome.Abort {
			result.Aborted = true
			break
		}
	}
	return result, nil
}
