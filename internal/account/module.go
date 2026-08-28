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

// Package account sequences the modules that provision a Snowflake account —
// creation, parameters, network, auth, identity, quota — through two shared
// entry points, Observe and Apply, so the SnowflakeAccount controller (018)
// stays a thin caller. See specs/009-account-pipeline.md.
package account

import (
	"context"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
)

// State is the fixed vocabulary every Outcome reports through.
type State int

const (
	StateDone     State = iota // fully applied; nothing pending, nothing wrong
	StatePending               // not yet applied; expected to resolve on a later reconcile
	StateRejected              // the tenant's own input was refused
	StateFailed                // an unexpected failure calling out to Snowflake or another system
)

// Outcome is the only channel a module has to report what happened on one
// Observe or Apply call. Each Outcome is a complete, self-classified
// statement — modules never return a separate error.
type Outcome struct {
	State     State
	Reason    string          // Pending only: the operator-visible reason for the wait
	Err       error           // Rejected/Failed only: the module's own classified error
	Abort     bool            // if true, Apply stops after this module on this pass
	Condition *xpv1.Condition // optional: a condition this module owns and wants surfaced
}

// Done reports that this module's desired state is fully applied.
func Done() Outcome { return Outcome{State: StateDone} }

// Pending reports that this module's work is not yet applied but is expected
// to resolve on a later reconcile. reason is operator-visible.
func Pending(reason string) Outcome { return Outcome{State: StatePending, Reason: reason} }

// Rejected reports that the tenant's own input was refused. err must already
// be classified by the calling module with errors.NewUserError — this
// package never classifies it itself.
func Rejected(err error) Outcome { return Outcome{State: StateRejected, Err: err} }

// Failed reports an unexpected failure calling out to Snowflake or another
// system. err must already be wrapped by the calling module with
// fmt.Errorf — this package never wraps it itself.
func Failed(err error) Outcome { return Outcome{State: StateFailed, Err: err} }

// Aborting returns a copy of o with Abort set true; every other field is
// unchanged. Only the account module (010) calls this today, on any outcome
// that is not Done.
func (o Outcome) Aborting() Outcome {
	o.Abort = true
	return o
}

// Module is implemented by each pipeline stage (010, 011, 012, 013, 015, 016).
type Module interface {
	Name() string

	// Observe is read-back only; it must mutate nothing in Snowflake.
	Observe(ctx context.Context, mc *ModuleContext) (inSync bool, outcome Outcome)

	// Apply re-asserts this module's full desired state, pruning any object
	// the CRD no longer lists. It must be safe to call repeatedly with no
	// other call in between.
	Apply(ctx context.Context, mc *ModuleContext) Outcome
}
