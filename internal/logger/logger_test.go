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

package logger

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/allianz/yukimi/internal/errors"
)

// spyLogger records every log call for inspection in tests.
type spyLogger struct {
	mu     sync.Mutex
	calls  []logCall
}

type logCall struct {
	level  string // "info" or "debug"
	msg    string
	contextFields    []any
}

func (s *spyLogger) Info(msg string, contextFields ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, logCall{level: "info", msg: msg, contextFields: contextFields})
}

func (s *spyLogger) Debug(msg string, contextFields ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, logCall{level: "debug", msg: msg, contextFields: contextFields})
}

func (s *spyLogger) WithValues(_ ...any) logging.Logger { return s }

func (s *spyLogger) last() (logCall, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return logCall{}, false
	}
	return s.calls[len(s.calls)-1], true
}

func (s *spyLogger) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func newLogger(spy *spyLogger) *Logger {
	return New(spy, "ns", "SnowflakeAccount", "my-account", OpObserve)
}

// hasKV checks whether the KV slice contains a key with the given value.
func hasKV(contextFields []any, key string, value any) bool {
	for i := 0; i+1 < len(contextFields); i += 2 {
		if contextFields[i] == key && fmt.Sprintf("%v", contextFields[i+1]) == fmt.Sprintf("%v", value) {
			return true
		}
	}
	return false
}

// SC-017: Operation constants produce correct strings.
func TestOperationConstants(t *testing.T) {
	cases := []struct {
		op   Operation
		want string
	}{
		{OpObserve, "observe"},
		{OpCreate, "create"},
		{OpUpdate, "update"},
		{OpDelete, "delete"},
	}
	for _, c := range cases {
		if string(c.op) != c.want {
			t.Errorf("Operation %q: got %q, want %q", c.op, string(c.op), c.want)
		}
	}
}

// SC-018: New() returns a non-nil *Logger.
func TestNew_NonNil(t *testing.T) {
	spy := &spyLogger{}
	l := New(spy, "ns", "Kind", "name", OpCreate)
	if l == nil {
		t.Fatal("New returned nil")
	}
}

// SC-019: Info() calls underlying logger at Info level with all KV pairs.
func TestInfo_LogsAtInfoLevel(t *testing.T) {
	spy := &spyLogger{}
	l := newLogger(spy)
	l.Info("resource created")

	c, ok := spy.last()
	if !ok {
		t.Fatal("no log calls recorded")
	}
	if c.level != "info" {
		t.Errorf("expected info level, got %q", c.level)
	}
	if c.msg != "resource created" {
		t.Errorf("unexpected message: %q", c.msg)
	}
	for _, kv := range []struct{ k, v string }{
		{"namespace", "ns"},
		{"kind", "SnowflakeAccount"},
		{"name", "my-account"},
		{"operation", "observe"},
	} {
		if !hasKV(c.contextFields, kv.k, kv.v) {
			t.Errorf("KV pair %q=%q missing from Info log", kv.k, kv.v)
		}
	}
}

// SC-020: Debug() calls underlying logger at Debug level with the same KV pairs.
func TestDebug_LogsAtDebugLevel(t *testing.T) {
	spy := &spyLogger{}
	l := newLogger(spy)
	l.Debug("observed state")

	c, ok := spy.last()
	if !ok {
		t.Fatal("no log calls recorded")
	}
	if c.level != "debug" {
		t.Errorf("expected debug level, got %q", c.level)
	}
	for _, kv := range []struct{ k, v string }{
		{"namespace", "ns"},
		{"kind", "SnowflakeAccount"},
		{"name", "my-account"},
		{"operation", "observe"},
	} {
		if !hasKV(c.contextFields, kv.k, kv.v) {
			t.Errorf("KV pair %q=%q missing from Debug log", kv.k, kv.v)
		}
	}
}

// SC-021: Handle(nil) returns nil and makes no log calls.
func TestHandle_Nil(t *testing.T) {
	spy := &spyLogger{}
	l := newLogger(spy)
	result := l.Handle(nil)
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
	if spy.count() != 0 {
		t.Errorf("expected 0 log calls, got %d", spy.count())
	}
}

// SC-022: Handle(user error) logs at Debug; returned .Error() equals the user message.
func TestHandle_UserError(t *testing.T) {
	spy := &spyLogger{}
	l := newLogger(spy)
	userErr := errors.NewUserError("Region 'invalid' is not allowed")

	result := l.Handle(userErr)
	if result == nil {
		t.Fatal("expected non-nil error")
	}
	if result.Error() != userErr.Error() {
		t.Errorf("got %q, want %q", result.Error(), userErr.Error())
	}

	c, ok := spy.last()
	if !ok {
		t.Fatal("no log calls recorded")
	}
	if c.level != "debug" {
		t.Errorf("expected debug level for user error, got %q", c.level)
	}
}

// SC-023: Handle(wrapped user error) correctly classifies via error chain.
func TestHandle_WrappedUserError(t *testing.T) {
	spy := &spyLogger{}
	l := newLogger(spy)
	base := errors.NewUserError("bad CIDR")
	wrapped := fmt.Errorf("validation failed: %w", base)

	result := l.Handle(wrapped)
	if result == nil {
		t.Fatal("expected non-nil error")
	}

	c, ok := spy.last()
	if !ok {
		t.Fatal("no log calls recorded")
	}
	if c.level != "debug" {
		t.Errorf("expected debug level for wrapped user error, got %q", c.level)
	}
}

// SC-024: Handle(system error) logs at Info; returned .Error() matches the incident ID format.
func TestHandle_SystemError(t *testing.T) {
	spy := &spyLogger{}
	l := newLogger(spy)
	sysErr := fmt.Errorf("failed to connect to Snowflake: dial tcp timeout")

	result := l.Handle(sysErr)
	if result == nil {
		t.Fatal("expected non-nil error")
	}

	re := regexp.MustCompile(`^An internal error occurred \([0-9a-f]{8}\)$`)
	if !re.MatchString(result.Error()) {
		t.Errorf("system error message %q does not match expected format", result.Error())
	}

	c, ok := spy.last()
	if !ok {
		t.Fatal("no log calls recorded")
	}
	if c.level != "info" {
		t.Errorf("expected info level for system error, got %q", c.level)
	}
	if !strings.Contains(c.msg, sysErr.Error()) {
		t.Error("error message missing from system error log message")
	}
}

// SC-003 / SC-025: Multiple Handle calls produce unique incident IDs.
func TestHandle_UniqueIncidentIDs(t *testing.T) {
	spy := &spyLogger{}
	l := newLogger(spy)

	const n = 20
	seen := make(map[string]bool, n)
	re := regexp.MustCompile(`\(([0-9a-f]{8})\)$`)

	for i := 0; i < n; i++ {
		result := l.Handle(fmt.Errorf("system error %d", i))
		m := re.FindStringSubmatch(result.Error())
		if m == nil {
			t.Fatalf("unexpected format: %q", result.Error())
		}
		id := m[1]
		if seen[id] {
			t.Errorf("duplicate incident ID %q at iteration %d", id, i)
		}
		seen[id] = true
	}
}

// SC-003: Incident ID is exactly 8 hex characters.
func TestHandle_IncidentIDFormat(t *testing.T) {
	spy := &spyLogger{}
	l := newLogger(spy)
	result := l.Handle(fmt.Errorf("any system error"))

	re := regexp.MustCompile(`^An internal error occurred \([0-9a-f]{8}\)$`)
	if !re.MatchString(result.Error()) {
		t.Errorf("incident ID format wrong: %q", result.Error())
	}
}
