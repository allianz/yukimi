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
	"errors"
	"testing"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
)

// SC-006: Done constructs an Outcome with only State populated.
func TestDone(t *testing.T) {
	o := Done()
	if o.State != StateDone {
		t.Errorf("State = %v, want StateDone", o.State)
	}
	if o.Reason != "" || o.Err != nil || o.Abort || o.Condition != nil || o.Event != nil {
		t.Errorf("Done() populated fields beyond State: %+v", o)
	}
}

// SC-006: Pending constructs an Outcome with State and Reason populated, and
// nothing else.
func TestPending(t *testing.T) {
	o := Pending("waiting for identity sync")
	if o.State != StatePending {
		t.Errorf("State = %v, want StatePending", o.State)
	}
	if o.Reason != "waiting for identity sync" {
		t.Errorf("Reason = %q, want %q", o.Reason, "waiting for identity sync")
	}
	if o.Err != nil || o.Abort || o.Condition != nil || o.Event != nil {
		t.Errorf("Pending() populated fields beyond State/Reason: %+v", o)
	}
}

// SC-006: Rejected constructs an Outcome with State and Err populated, and
// nothing else.
func TestRejected(t *testing.T) {
	wantErr := errors.New("bad input")
	o := Rejected(wantErr)
	if o.State != StateRejected {
		t.Errorf("State = %v, want StateRejected", o.State)
	}
	if o.Err != wantErr {
		t.Errorf("Err = %v, want %v", o.Err, wantErr)
	}
	if o.Reason != "" || o.Abort || o.Condition != nil || o.Event != nil {
		t.Errorf("Rejected() populated fields beyond State/Err: %+v", o)
	}
}

// SC-006: Failed constructs an Outcome with State and Err populated, and
// nothing else.
func TestFailed(t *testing.T) {
	wantErr := errors.New("connection refused")
	o := Failed(wantErr)
	if o.State != StateFailed {
		t.Errorf("State = %v, want StateFailed", o.State)
	}
	if o.Err != wantErr {
		t.Errorf("Err = %v, want %v", o.Err, wantErr)
	}
	if o.Reason != "" || o.Abort || o.Condition != nil || o.Event != nil {
		t.Errorf("Failed() populated fields beyond State/Err: %+v", o)
	}
}

// SC-007: Aborting returns a copy with Abort set true and every other field
// unchanged; the original Outcome is untouched.
func TestOutcome_Aborting(t *testing.T) {
	cond := xpv1.Available()
	evt := event.Warning("SomeReason", errors.New("boom"))
	cases := []Outcome{
		Done(),
		Pending("reason"),
		Rejected(errors.New("rejected")),
		Failed(errors.New("failed")),
		{State: StateFailed, Err: errors.New("with condition"), Condition: &cond},
		{State: StateFailed, Err: errors.New("with event"), Event: &evt},
		{State: StateDone, Abort: true}, // already aborting
	}

	for _, original := range cases {
		wantAbort := original.Abort
		got := original.Aborting()

		if !got.Abort {
			t.Errorf("Aborting() on %+v: Abort = false, want true", original)
		}
		if got.State != original.State || got.Reason != original.Reason ||
			got.Err != original.Err || got.Condition != original.Condition || got.Event != original.Event {
			t.Errorf("Aborting() on %+v changed a field it shouldn't have: got %+v", original, got)
		}
		if original.Abort != wantAbort {
			t.Errorf("original Outcome was mutated by Aborting(): Abort = %v, want %v", original.Abort, wantAbort)
		}
	}
}
