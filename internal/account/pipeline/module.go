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

// Package pipeline sequences the modules that provision a Snowflake account —
// creation, parameters, network, auth, identity, quota — through two shared
// entry points, Observe and Apply, so the SnowflakeAccount controller (020)
// stays a thin caller. See specs/009-account-pipeline.md.
package pipeline

import (
	"context"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
)

// State is the fixed vocabulary every Outcome reports through.
type State int

const (
	StateDone    State = iota // provisioned and matches the spec
	StatePending              // not yet provisioned; Reason says why (Ready=False)
	StateDrifted              // Observe only: provisioned, but differs from the spec
	StateFailed               // Err is set; user vs system is decided by the error itself
)

// Outcome is everything one module reports from one Observe or Apply call.
type Outcome struct {
	Module    string // set by the pipeline, never by the module
	State     State
	Reason    string          // Pending: why it is waiting; becomes Ready's message
	Err       error           // Failed: errors.NewUserError(...) or a wrapped system error
	Condition *xpv1.Condition // optional: a condition this module owns
	Events    []event.Event   // optional: zero or more events
}

// Done reports that this module's state is provisioned and matches the spec.
func Done() Outcome { return Outcome{State: StateDone} }

// Pending reports that this module's work is not yet provisioned but is
// expected to resolve on a later reconcile. reason is operator-visible.
func Pending(reason string) Outcome { return Outcome{State: StatePending, Reason: reason} }

// Drifted reports (from Observe) that this module's state is provisioned but
// differs from the spec. A module that wants the drift to be visible attaches
// an event itself.
func Drifted() Outcome { return Outcome{State: StateDrifted} }

// Failed reports a failure. err must already be classified by the calling
// module — errors.NewUserError for a tenant mistake, fmt.Errorf wrapping for a
// system failure; this package never classifies or wraps it.
func Failed(err error) Outcome { return Outcome{State: StateFailed, Err: err} }

// WithCondition returns a copy of o carrying c as the condition this module owns.
func (o Outcome) WithCondition(c xpv1.Condition) Outcome {
	o.Condition = &c
	return o
}

// WithEvent returns a copy of o with e appended to its events.
func (o Outcome) WithEvent(e event.Event) Outcome {
	o.Events = append(append([]event.Event(nil), o.Events...), e)
	return o
}

// Module is implemented by each pipeline stage (010, 011, 012, 013, 014, 015, 017, 018).
type Module interface {
	Name() string

	// Observe is read-back only; it must mutate nothing in Snowflake (it may
	// set status fields on the CR). Done: provisioned and matches the spec.
	// Pending: not yet provisioned. Drifted: provisioned, but differs from the
	// spec. Failed: could not read back — the exception; domain checks belong
	// in Apply.
	Observe(ctx context.Context, mc *ModuleContext) Outcome

	// Apply re-asserts this module's full desired state, pruning any object
	// the CRD no longer lists. It must be safe to call repeatedly with no
	// other call in between. Drifted counts as not Done.
	Apply(ctx context.Context, mc *ModuleContext) Outcome

	// Teardown removes the state this module leaves outside the tenant's own
	// account, which dropping that account would not take with it. Most
	// modules have none and return nil. It uses OrgAdminDB or no connection
	// at all — never TenantDB — and must be safe to call repeatedly.
	Teardown(ctx context.Context, mc *ModuleContext) error
}

// AccountModuleName is the account module's (012) Name(). Observation uses it
// to find which registered module's outcome decides whether the resource
// exists, regardless of that module's position in the
// registered list — a module needing no Snowflake connection (guardrail-check,
// 010, or quota-check, 011) may be registered ahead of the account module.
const AccountModuleName = "account"
