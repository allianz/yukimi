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
	if o.Reason != "" || o.Err != nil || o.Condition != nil || o.Events != nil {
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
	if o.Err != nil || o.Condition != nil || o.Events != nil {
		t.Errorf("Pending() populated fields beyond State/Reason: %+v", o)
	}
}

// SC-006: Drifted constructs an Outcome with only State populated.
func TestDrifted(t *testing.T) {
	o := Drifted()
	if o.State != StateDrifted {
		t.Errorf("State = %v, want StateDrifted", o.State)
	}
	if o.Reason != "" || o.Err != nil || o.Condition != nil || o.Events != nil {
		t.Errorf("Drifted() populated fields beyond State: %+v", o)
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
	if o.Reason != "" || o.Condition != nil || o.Events != nil {
		t.Errorf("Failed() populated fields beyond State/Err: %+v", o)
	}
}

// SC-006: WithCondition and WithEvent return a copy with that field added and
// every other field unchanged; the original is untouched.
func TestOutcome_WithConditionAndEvent(t *testing.T) {
	cond := xpv1.Available()
	e1 := event.Normal("First", "one")
	e2 := event.Warning("Second", errors.New("two"))

	original := Pending("waiting")
	withCond := original.WithCondition(cond)
	if original.Condition != nil {
		t.Error("WithCondition mutated the original Outcome")
	}
	if withCond.Condition == nil || withCond.Condition.Type != cond.Type {
		t.Fatalf("WithCondition did not set the condition: %+v", withCond)
	}
	if withCond.State != StatePending || withCond.Reason != "waiting" {
		t.Errorf("WithCondition changed another field: %+v", withCond)
	}

	one := withCond.WithEvent(e1)
	two := one.WithEvent(e2)
	if len(withCond.Events) != 0 || len(one.Events) != 1 || len(two.Events) != 2 {
		t.Fatalf("event counts = %d/%d/%d, want 0/1/2", len(withCond.Events), len(one.Events), len(two.Events))
	}
	if two.Events[0].Reason != e1.Reason || two.Events[1].Reason != e2.Reason {
		t.Errorf("events out of order: %+v", two.Events)
	}
	if two.Condition == nil {
		t.Error("WithEvent dropped the condition")
	}
}
