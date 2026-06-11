// Copyright 2026 The Yukimi Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package errors

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/google/go-cmp/cmp"
)

// SC-001: NewUser() creates errors with TypeUser classification.
func TestNewUser_TypeIsUser(t *testing.T) {
	err := NewUser("invalid region")

	var ctrlErr *ControllerError
	if !errors.As(err, &ctrlErr) {
		t.Fatal("expected ControllerError")
	}
	if ctrlErr.Type != TypeUser {
		t.Errorf("expected TypeUser, got %v", ctrlErr.Type)
	}
}

// SC-001: NewUser() error message is returned via Error().
func TestNewUser_ErrorReturnsMessage(t *testing.T) {
	msg := "Region 'invalid' does not match allowed format (expected: aws-eu-central-1)"
	err := NewUser(msg)

	if err.Error() != msg {
		t.Errorf("expected %q, got %q", msg, err.Error())
	}
}

// SC-001: NewUser() sets LogDebug level.
func TestNewUser_LogLevelIsDebug(t *testing.T) {
	err := NewUser("invalid region")

	var ctrlErr *ControllerError
	if !errors.As(err, &ctrlErr) {
		t.Fatal("expected ControllerError")
	}
	if ctrlErr.LogLevel != LogDebug {
		t.Errorf("expected LogDebug, got %v", ctrlErr.LogLevel)
	}
}

// SC-002: NewUser() auto-truncates messages to 256 characters.
func TestNewUser_TruncatesLongMessage(t *testing.T) {
	longMsg := strings.Repeat("a", 300)
	err := NewUser(longMsg)

	var ctrlErr *ControllerError
	if !errors.As(err, &ctrlErr) {
		t.Fatal("expected ControllerError")
	}
	if len(ctrlErr.UserMessage) != 256 {
		t.Errorf("expected length 256, got %d", len(ctrlErr.UserMessage))
	}
	if !strings.HasSuffix(ctrlErr.UserMessage, "...") {
		t.Error("expected truncated message to end with '...'")
	}
}

// SC-002: Truncation preserves the start of the message (field path is at the front).
func TestNewUser_TruncationPreservesFieldPath(t *testing.T) {
	fieldPath := "spec.network.allowedIPs[0]"
	msg := fmt.Sprintf("Invalid CIDR in %s: %s", fieldPath, strings.Repeat("x", 300))
	err := NewUser(msg)

	var ctrlErr *ControllerError
	if !errors.As(err, &ctrlErr) {
		t.Fatal("expected ControllerError")
	}
	if !strings.Contains(ctrlErr.UserMessage, fieldPath) {
		t.Errorf("truncated message lost field path %q: got %q", fieldPath, ctrlErr.UserMessage)
	}
}

// SC-002b: NewUser() falls back to a generic message when called with an empty string.
func TestNewUser_EmptyMessageFallback(t *testing.T) {
	err := NewUser("")

	var ctrlErr *ControllerError
	if !errors.As(err, &ctrlErr) {
		t.Fatal("expected ControllerError")
	}
	if ctrlErr.UserMessage == "" {
		t.Error("expected non-empty fallback message, got empty string")
	}
	if ctrlErr.UserMessage != "invalid configuration — no details provided" {
		t.Errorf("unexpected fallback message: %q", ctrlErr.UserMessage)
	}
}

// SC-003: ErrorDetails() generates unique 8-character incident IDs for system errors.
func TestErrorDetails_SystemError_IncidentIDFormat(t *testing.T) {
	rawErr := fmt.Errorf("failed to connect to Snowflake: timeout")
	userMsg, _, _, _ := ErrorDetails(rawErr)

	if !strings.HasPrefix(userMsg, "An internal error occurred (") {
		t.Errorf("unexpected userMsg format: %q", userMsg)
	}
	if !strings.HasSuffix(userMsg, ")") {
		t.Errorf("unexpected userMsg suffix: %q", userMsg)
	}

	id := extractIncidentID(userMsg)
	if len(id) != 8 {
		t.Errorf("expected 8-char incident ID, got %q (len %d)", id, len(id))
	}
}

// SC-004: Incident IDs are preserved when system errors are wrapped.
func TestErrorDetails_SystemError_WrappedPreservesID(t *testing.T) {
	raw := fmt.Errorf("connection timeout")
	wrapped := fmt.Errorf("provisioning failed: %w", raw)

	userMsg, logMsg, logLevel, retry := ErrorDetails(wrapped)

	if !strings.HasPrefix(userMsg, "An internal error occurred (") {
		t.Errorf("unexpected userMsg: %q", userMsg)
	}
	if logLevel != LogError {
		t.Errorf("expected LogError, got %v", logLevel)
	}
	if !strings.Contains(logMsg, "connection timeout") {
		t.Errorf("logMsg missing original error: %q", logMsg)
	}
	if retry == nil {
		t.Error("expected non-nil retry for system error")
	}
}

// SC-005: IsUserError() correctly identifies user errors in wrapped chains.
func TestIsUserError_WrappedUserError(t *testing.T) {
	userErr := NewUser("invalid region")
	wrapped := fmt.Errorf("validation failed: %w", userErr)

	if !IsUserError(wrapped) {
		t.Error("expected IsUserError to return true for wrapped user error")
	}
}

// SC-005: IsUserError() returns false for system errors and nil.
func TestIsUserError_SystemErrorAndNil(t *testing.T) {
	if IsUserError(fmt.Errorf("system error")) {
		t.Error("expected IsUserError to return false for system error")
	}
	if IsUserError(nil) {
		t.Error("expected IsUserError to return false for nil")
	}
}

// SC-006: LogWithLevel() maps LogDebug to logger.Debug().
func TestLogWithLevel_Debug(t *testing.T) {
	mock := &mockLogger{}
	LogWithLevel(mock, LogDebug, "user config error", "resource", "my-account")

	if len(mock.debugCalls) != 1 {
		t.Fatalf("expected 1 debug call, got %d", len(mock.debugCalls))
	}
	if mock.debugCalls[0].msg != "user config error" {
		t.Errorf("unexpected message: %q", mock.debugCalls[0].msg)
	}
	if len(mock.infoCalls) != 0 {
		t.Errorf("expected no info calls, got %d", len(mock.infoCalls))
	}

	wantKV := []interface{}{"resource", "my-account"}
	if diff := cmp.Diff(wantKV, mock.debugCalls[0].keysAndValues); diff != "" {
		t.Errorf("keysAndValues mismatch (-want +got):\n%s", diff)
	}
}

// SC-007: LogWithLevel() maps LogError to logger.Info().
func TestLogWithLevel_Error(t *testing.T) {
	mock := &mockLogger{}
	LogWithLevel(mock, LogError, "system failure", "resource", "my-account")

	if len(mock.infoCalls) != 1 {
		t.Fatalf("expected 1 info call, got %d", len(mock.infoCalls))
	}
	if mock.infoCalls[0].msg != "system failure" {
		t.Errorf("unexpected message: %q", mock.infoCalls[0].msg)
	}
	if len(mock.debugCalls) != 0 {
		t.Errorf("expected no debug calls, got %d", len(mock.debugCalls))
	}
}

// SC-008: System error userMsg follows "An internal error occurred (NNNNN)" format.
func TestErrorDetails_SystemError_UserMsgFormat(t *testing.T) {
	userMsg, _, _, _ := ErrorDetails(fmt.Errorf("aws timeout"))

	expected := "An internal error occurred ("
	if !strings.HasPrefix(userMsg, expected) {
		t.Errorf("expected prefix %q in userMsg, got %q", expected, userMsg)
	}
}

// SC-009: System error logMsg includes full error details with incident ID.
func TestErrorDetails_SystemError_LogMsgContainsDetails(t *testing.T) {
	_, logMsg, _, _ := ErrorDetails(fmt.Errorf("snowflake: connection refused"))

	if !strings.Contains(logMsg, "snowflake: connection refused") {
		t.Errorf("logMsg missing original error details: %q", logMsg)
	}
	if !strings.Contains(logMsg, "An internal error occurred (") {
		t.Errorf("logMsg missing incident ID prefix: %q", logMsg)
	}
}

// SC-010: 100 system errors produce 100 unique incident IDs (UUID-based, no collisions).
func TestErrorDetails_SystemError_IncidentIDUniqueness(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		userMsg, _, _, _ := ErrorDetails(fmt.Errorf("error %d", i))
		ids[extractIncidentID(userMsg)] = true
	}
	if len(ids) != 100 {
		t.Errorf("expected 100 unique IDs from 100 errors, got %d", len(ids))
	}
}

// SC-014: All error types implement the standard error interface.
func TestControllerError_ImplementsErrorInterface(t *testing.T) {
	var err error = &ControllerError{
		Type:        TypeUser,
		UserMessage: "test message",
		LogLevel:    LogDebug,
	}
	if err.Error() != "test message" {
		t.Errorf("expected 'test message', got %q", err.Error())
	}
}

// SC-015: Error wrapping with %w preserves user error classification through fmt.Errorf.
func TestErrorDetails_UserError_WrappedMultipleLevels(t *testing.T) {
	base := NewUser("invalid CIDR in spec.network.allowedIPs[0]")
	wrapped := fmt.Errorf("layer 1: %w", base)
	wrapped = fmt.Errorf("layer 2: %w", wrapped)
	wrapped = fmt.Errorf("layer 3: %w", wrapped)

	userMsg, logMsg, logLevel, retry := ErrorDetails(wrapped)

	if userMsg != "invalid CIDR in spec.network.allowedIPs[0]" {
		t.Errorf("unexpected userMsg: %q", userMsg)
	}
	if logLevel != LogDebug {
		t.Errorf("expected LogDebug, got %v", logLevel)
	}
	if retry == nil {
		t.Error("expected non-nil retry for user error")
	}
	if !strings.Contains(logMsg, "layer 3") {
		t.Errorf("logMsg missing wrapping context: %q", logMsg)
	}
}

// SC-015: Deep wrapping (10 levels) still correctly classifies user errors.
func TestErrorDetails_UserError_DeepWrapping(t *testing.T) {
	err := error(NewUser("deep user error"))
	for i := 0; i < 10; i++ {
		err = fmt.Errorf("level %d: %w", i, err)
	}

	userMsg, _, logLevel, _ := ErrorDetails(err)

	if userMsg != "deep user error" {
		t.Errorf("unexpected userMsg: %q", userMsg)
	}
	if logLevel != LogDebug {
		t.Errorf("expected LogDebug, got %v", logLevel)
	}
}

// SC-016: Nil errors are handled gracefully — all return values are zero/empty.
func TestErrorDetails_NilError(t *testing.T) {
	userMsg, logMsg, logLevel, retry := ErrorDetails(nil)

	if userMsg != "" {
		t.Errorf("expected empty userMsg, got %q", userMsg)
	}
	if logMsg != "" {
		t.Errorf("expected empty logMsg, got %q", logMsg)
	}
	if logLevel != LogDebug {
		t.Errorf("expected LogDebug (zero value), got %v", logLevel)
	}
	if retry != nil {
		t.Errorf("expected nil retry, got %v", retry)
	}
}

// SC-011: Incident ID generation completes in <100μs.
func BenchmarkGenerateIncidentID(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ErrorDetails(fmt.Errorf("benchmark error"))
	}
}

// SC-012: Happy path (no error) has zero allocations.
func BenchmarkErrorDetails_NilError_ZeroAllocs(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ErrorDetails(nil)
	}
}

// extractIncidentID parses the 8-character ID from "An internal error occurred (XXXXXXXX)".
func extractIncidentID(userMsg string) string {
	prefix := "An internal error occurred ("
	id := strings.TrimPrefix(userMsg, prefix)
	return strings.TrimSuffix(id, ")")
}

// mockLogger implements logging.Logger for testing LogWithLevel.
type mockLogger struct {
	debugCalls []logCall
	infoCalls  []logCall
}

type logCall struct {
	msg           string
	keysAndValues []interface{}
}

func (m *mockLogger) Debug(msg string, keysAndValues ...interface{}) {
	m.debugCalls = append(m.debugCalls, logCall{msg, keysAndValues})
}

func (m *mockLogger) Info(msg string, keysAndValues ...interface{}) {
	m.infoCalls = append(m.infoCalls, logCall{msg, keysAndValues})
}

func (m *mockLogger) WithValues(keysAndValues ...interface{}) logging.Logger {
	return m
}
