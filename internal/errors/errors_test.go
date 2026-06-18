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

package errors

import (
	"fmt"
	"strings"
	"testing"
)

// SC-001: NewUserError creates errors that IsUserError recognises.
func TestNewUserError_IsUserError(t *testing.T) {
	err := NewUserError("invalid region")
	if !IsUserError(err) {
		t.Fatal("expected IsUserError to return true for a user error")
	}
}

// SC-014: .Error() returns the user-facing message.
func TestNewUserError_ErrorMessage(t *testing.T) {
	msg := "Region 'invalid' does not match allowed format"
	err := NewUserError(msg)
	if err.Error() != msg {
		t.Errorf("got %q, want %q", err.Error(), msg)
	}
}

// SC-002: Messages longer than 256 chars are truncated with "..." suffix.
func TestNewUserError_Truncation(t *testing.T) {
	long := strings.Repeat("a", 300)
	err := NewUserError(long)
	got := err.Error()
	if len(got) != 259 { // 256 + len("...")
		t.Errorf("expected length 259, got %d", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected '...' suffix, got %q", got[len(got)-5:])
	}
	if got[:256] != long[:256] {
		t.Error("first 256 chars should be preserved")
	}
}

// SC-002: Message exactly 256 chars should not be truncated.
func TestNewUserError_ExactLimit(t *testing.T) {
	exact := strings.Repeat("b", 256)
	err := NewUserError(exact)
	if err.Error() != exact {
		t.Errorf("256-char message should not be truncated")
	}
}

// SC-002b: Empty message falls back to the default string.
func TestNewUserError_EmptyFallback(t *testing.T) {
	err := NewUserError("")
	want := "invalid configuration — no details provided"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
	if !IsUserError(err) {
		t.Error("empty-fallback error should still be a user error")
	}
}

// SC-015 / SC-005: IsUserError returns true through a fmt.Errorf("%w", ...) chain.
func TestIsUserError_WrappedChain(t *testing.T) {
	base := NewUserError("bad CIDR")
	wrapped := fmt.Errorf("validation failed: %w", base)
	if !IsUserError(wrapped) {
		t.Fatal("expected IsUserError to return true for wrapped user error")
	}
}

// SC-015: Double-wrapped chain.
func TestIsUserError_DoubleWrapped(t *testing.T) {
	base := NewUserError("missing field")
	w1 := fmt.Errorf("layer1: %w", base)
	w2 := fmt.Errorf("layer2: %w", w1)
	if !IsUserError(w2) {
		t.Fatal("expected IsUserError to return true for double-wrapped user error")
	}
}

// Negative: plain fmt.Errorf is not a user error.
func TestIsUserError_PlainError(t *testing.T) {
	err := fmt.Errorf("connection timeout")
	if IsUserError(err) {
		t.Fatal("expected IsUserError to return false for plain error")
	}
}

// Negative: nil is not a user error.
func TestIsUserError_Nil(t *testing.T) {
	if IsUserError(nil) {
		t.Fatal("expected IsUserError to return false for nil")
	}
}
