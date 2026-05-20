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

// Package errors provides the error handling system for the Yukimi Crossplane provider.
// It distinguishes between user errors (configuration mistakes users can fix) and system
// errors (infrastructure failures requiring operator intervention).
//
// # Error Classification
//
// User errors are created explicitly with NewUser() and represent configuration mistakes
// that users can fix by editing their CRD spec. They are logged at Debug level and do not
// trigger Kubernetes retry backoff.
//
// System errors are any raw errors wrapped with fmt.Errorf(). They are logged at Error
// level with a unique 5-digit incident ID that correlates the user-facing status message
// with the operator log entry.
//
// # Controller Integration
//
//	result, err := e.somePackage.DoWork(ctx, cr)
//	if err != nil {
//	    userMsg, logMsg, logLevel, retryErr := errors.ErrorDetails(err)
//	    errors.LogWithLevel(e.logger, logLevel, logMsg, "resource", cr.Name)
//	    cr.Status.SetConditions(xpv1.Unavailable().WithMessage(userMsg))
//	    return managed.ExternalObservation{}, retryErr
//	}
package errors

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"

	"github.com/crossplane/crossplane-runtime/pkg/logging"
)

// ErrorType distinguishes between user errors (actionable by the user)
// and raw errors (requiring incident ID generation in ErrorDetails).
//
// Classification:
//   - TypeUser: Configuration mistakes that users can fix by editing their CRD.
//     Examples: invalid region format, malformed CIDR, missing required field.
type ErrorType int

const (
	// TypeUser indicates a user-actionable configuration error.
	// Logged at Debug level with specific field paths and expected formats
	// to enable self-service resolution.
	TypeUser ErrorType = iota
)

// LogLevel defines logging severity levels aligned with error classification
// and the Crossplane logging framework.
type LogLevel int

const (
	// LogDebug maps to logger.Debug() — only visible with --debug flag.
	// Used for user errors that represent expected configuration mistakes.
	LogDebug LogLevel = iota

	// LogError maps to logger.Info() — always visible to operators.
	// Used for system errors representing infrastructure failures.
	// Note: Crossplane/logr has no explicit Error() method; Info() provides maximum visibility.
	LogError
)

// ControllerError wraps user errors with classification metadata and a user-facing message.
// It implements the error interface and supports unwrapping via errors.As().
//
// Invariants:
//   - UserMessage is never empty
//   - Type == TypeUser implies LogLevel == LogDebug
type ControllerError struct {
	Type        ErrorType
	UserMessage string
	LogLevel    LogLevel
}

// Error implements the error interface, returning the clean user-facing message.
// This ensures Crossplane status conditions receive an actionable message
// without internal implementation details.
func (e *ControllerError) Error() string {
	return e.UserMessage
}

// NewUser creates a user error with an actionable message. Use this in business logic
// packages when validating CRD fields.
//
// Message guidelines (from spec):
//   - Include the field path: spec.region, spec.network.allowedIPs[0]
//   - Show the invalid value: Region 'invalid'
//   - State the expected format: (expected: aws-eu-central-1)
//   - ✅ "Region 'us-east-1' does not match allowed format (expected: aws-eu-central-1)"
//   - ✅ "Invalid CIDR '10.0.0.256/24' in spec.network.allowedIPs[0]: not a valid IP range"
//   - ❌ "Invalid region"
//
// Messages longer than 256 characters are automatically truncated with "..." to satisfy
// Kubernetes status field constraints.
func NewUser(msg string) error {
	if len(msg) > 256 {
		msg = msg[:253] + "..."
	}
	return &ControllerError{
		Type:        TypeUser,
		UserMessage: msg,
		LogLevel:    LogDebug,
	}
}

// IsUserError reports whether err or any error in its chain is a user error.
// Use this when branching on error type without needing the full ErrorDetails metadata.
func IsUserError(err error) bool {
	if err == nil {
		return false
	}
	var ctrlErr *ControllerError
	return errors.As(err, &ctrlErr) && ctrlErr.Type == TypeUser
}

// ErrorDetails extracts error classification metadata for controller integration.
// It is the single entry point for handling errors in controllers.
//
// For user errors (ControllerError in the chain):
//   - userMsg:   the original user-facing message from NewUser()
//   - logMsg:    the full error chain string (includes wrapping context)
//   - logLevel:  LogDebug
//   - retry:     the ControllerError (non-nil — Crossplane must know the operation failed)
//
// For system errors (any raw error not containing a ControllerError):
//   - userMsg:   "An internal error occurred (NNNNN)" — safe for users, no internal details
//   - logMsg:    "An internal error occurred (NNNNN): <original error>" — full details for operators
//   - logLevel:  LogError
//   - retry:     new sanitized error (triggers Kubernetes retry with exponential backoff)
//
// For nil errors:
//   - all return values are zero/empty (SC-015)
func ErrorDetails(err error) (userMsg string, logMsg string, logLevel LogLevel, retry error) {
	if err == nil {
		return "", "", LogDebug, nil
	}

	var ctrlErr *ControllerError
	if errors.As(err, &ctrlErr) {
		// User error: extract clean message, preserve full chain in logMsg.
		// Return ctrlErr (not err) so Crossplane gets the clean UserMessage.
		return ctrlErr.UserMessage, err.Error(), ctrlErr.LogLevel, ctrlErr
	}

	// System error: generate incident ID and sanitize the user-facing message.
	incidentID := generateIncidentID()
	userMsg = fmt.Sprintf("An internal error occurred (%s)", incidentID)
	logMsg = fmt.Sprintf("An internal error occurred (%s): %s", incidentID, err.Error())

	// Sanitized error hides internal details from users, triggers K8s retry backoff.
	return userMsg, logMsg, LogError, fmt.Errorf("%s", userMsg)
}

// generateIncidentID creates a unique 5-digit identifier (00000–99999) using crypto/rand.
// Falls back to "00000" if random generation fails, to avoid blocking reconciliation.
func generateIncidentID() string {
	n, err := rand.Int(rand.Reader, big.NewInt(100000))
	if err != nil {
		return "00000"
	}
	return fmt.Sprintf("%05d", n)
}

// LogWithLevel logs msg at the appropriate level for the error classification.
//
//   - LogDebug → logger.Debug() — only visible with --debug flag (user errors)
//   - LogError → logger.Info() — always visible to operators (system errors)
//     Note: Crossplane/logr has no logger.Error(); logger.Info() provides highest visibility.
func LogWithLevel(logger logging.Logger, level LogLevel, msg string, keysAndValues ...interface{}) {
	switch level {
	case LogDebug:
		logger.Debug(msg, keysAndValues...)
	case LogError:
		logger.Info(msg, keysAndValues...)
	}
}
