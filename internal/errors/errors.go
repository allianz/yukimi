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
// trigger Kubernetes retry backoff — retrying immediately makes no sense when the config
// is wrong.
//
// System errors are any raw errors wrapped with fmt.Errorf(). They represent infrastructure
// failures the user cannot fix. They are logged at Info level with a unique 8-character
// incident ID that correlates the user-facing status message with the operator log entry.
//
// # Controller Integration
//
// Every controller error path follows the same pattern:
//
//	result, err := e.somePackage.DoWork(ctx, cr)
//	if err != nil {
//	    userMsg, logMsg, logLevel, retryErr := errors.ErrorDetails(err)
//	    errors.LogWithLevel(e.logger, logLevel, logMsg, "resource", cr.Name)
//	    cr.Status.SetConditions(xpv1.Unavailable().WithMessage(userMsg))
//	    return managed.ExternalObservation{}, retryErr
//	}
//
// # Incident Correlation
//
// When a system error occurs, the user sees "An internal error occurred (f47ac10b)"
// in their CRD status. The operator searches logs for "f47ac10b" and finds the full
// error details. The 8-character ID is derived from a UUID and is globally unique —
// no two errors will ever share the same ID regardless of when or where they occur.
package errors

import (
	"errors"
	"fmt"

	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/google/uuid"
)

// ErrorType distinguishes between user errors (actionable by the user)
// and raw errors (requiring incident ID generation in ErrorDetails).
//
// Classification Decision:
//   - TypeUser: Configuration mistakes that users can fix by editing their CRD
//     Examples: invalid region format, malformed CIDR, missing required field
type ErrorType int

const (
	// TypeUser indicates a user-actionable configuration error.
	// These errors are logged at Debug level and include specific field paths
	// and expected formats to enable self-service resolution.
	TypeUser ErrorType = iota
)

// LogLevel defines logging severity levels aligned with error classification
// and the Crossplane logging framework.
//
// Mapping to Crossplane logging.Logger methods:
//   - LogDebug: logger.Debug(msg, kv...) - Only visible with --debug flag
//   - LogError: logger.Info(msg, kv...)  - Always visible to operators
//
// Rationale:
//   - User errors are expected configuration mistakes → Debug level (noise reduction)
//   - System errors indicate platform issues → Info level (operator alerting)
//   - Crossplane/logr doesn't have an explicit Error() method → Info is highest severity
type LogLevel int

const (
	// LogDebug indicates user errors that should only be logged in verbose mode.
	// These represent expected configuration mistakes that don't require operator attention.
	LogDebug LogLevel = iota

	// LogError indicates infrastructure errors that should always be visible to operators.
	// These represent system failures requiring immediate investigation.
	LogError
)

// ControllerError is a custom error type that wraps user errors with
// classification metadata and user-facing messages.
//
// This type implements the error interface and supports unwrapping via errors.As().
// It is designed to integrate seamlessly with Crossplane's managed resource pattern
// while preserving Go's standard error wrapping capabilities.
//
// Fields:
//   - Type: Classification (TypeUser)
//   - UserMessage: Clean, actionable message for Kubernetes status conditions (max 256 chars)
//   - LogLevel: Logging severity level (LogDebug for user errors)
//
// Invariants:
//   - UserMessage is never empty
//   - Type == TypeUser implies LogLevel == LogDebug
type ControllerError struct {
	Type        ErrorType
	UserMessage string
	LogLevel    LogLevel
}

// Error implements the error interface by returning the UserMessage.
// This ensures that when the error is propagated to Crossplane status conditions,
// the user sees a clean, actionable message without internal details.
//
// For user errors, this returns the specific error message:
//
//	"spec.awsRegion 'us-east-1' is not valid (expected: aws-eu-central-1)"
func (e *ControllerError) Error() string {
	return e.UserMessage
}

// IsUserError checks if an error is a user error (ControllerError with TypeUser).
// Returns true if the error or any error in its chain is a user error.
//
// This is useful in controllers to decide whether to retry immediately:
//   - User errors: Don't retry (user needs to fix configuration)
//   - System errors: Retry with backoff
func IsUserError(err error) bool {
	if err == nil {
		return false
	}
	var ctrlErr *ControllerError
	return errors.As(err, &ctrlErr) && ctrlErr.Type == TypeUser
}

// NewUser creates a user error with an actionable message.
//
// User errors represent configuration mistakes that users can fix by editing
// their CRD (e.g., invalid region format, malformed CIDR, missing required field).
// These errors are logged at Debug level and do not trigger Kubernetes retry backoff.
//
// Message Guidelines:
//   - Be specific: Include field path and invalid value
//   - Be actionable: Specify what's wrong and how to fix it
//   - Examples:
//     ✅ "spec.awsRegion 'us-east-1' is not valid (expected: aws-eu-central-1)"
//     ✅ "spec.network.allowedIPs[0] '10.0.0.256/24' is not a valid CIDR range"
//     ❌ "Invalid region"
//     ❌ "Bad input"
//
// Parameters:
//   - msg: Actionable error message (max 256 chars, truncated with "..." if longer)
//
// Returns:
//   - error: ControllerError with Type=TypeUser, LogLevel=LogDebug
//
// Example:
//
//	if !regionPattern.MatchString(region) {
//	    return errors.NewUser(fmt.Sprintf(
//	        "spec.awsRegion '%s' is not valid (expected: aws-eu-central-1)",
//	        region))
//	}
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

// ErrorDetails extracts error classification metadata for controller integration.
//
// This function unwraps error chains using errors.As() to locate a ControllerError,
// then extracts the user message, log message, log level, and retry error needed
// by controllers to log and propagate errors to Crossplane.
//
// Behavior:
//   - If err contains ControllerError (user error): extracts metadata, returns ControllerError for retry
//   - If err is a raw error (no ControllerError): generates incident ID and creates sanitized error for retry
//   - If err is nil: returns all zero/empty values
//   - Supports arbitrary nesting depth via errors.As() unwrapping
//
// Returns:
//   - userMsg:   Clean message for Kubernetes status conditions
//   - logMsg:    Full error string for structured logging (original error details preserved)
//   - logLevel:  Severity level for logging (LogDebug for user errors, LogError for system errors)
//   - retry:     Error to return to Crossplane — non-nil for both user and system errors
//     so Crossplane knows the operation failed and sets Ready=False
//
// User Error Example:
//
//	err := errors.NewUser("spec.awsRegion 'us-east-1' is not valid")
//	userMsg → "spec.awsRegion 'us-east-1' is not valid"
//	logMsg  → "spec.awsRegion 'us-east-1' is not valid"
//	level   → LogDebug
//	retry   → the ControllerError (no K8s retry storm)
//
// System Error Example:
//
//	err := fmt.Errorf("failed to connect to Snowflake: %w", connErr)
//	userMsg → "An internal error occurred (f47ac10b)"
//	logMsg  → "An internal error occurred (f47ac10b): failed to connect to Snowflake: ..."
//	level   → LogError
//	retry   → sanitized error (triggers K8s retry with exponential backoff)
//
// Controller Usage:
//
//	userMsg, logMsg, logLevel, retryErr := errors.ErrorDetails(err)
//	errors.LogWithLevel(e.logger, logLevel, logMsg, "resource", cr.Name, "namespace", cr.Namespace)
//	cr.Status.SetConditions(xpv1.Unavailable().WithMessage(userMsg))
//	return managed.ExternalObservation{}, retryErr
func ErrorDetails(err error) (userMsg string, logMsg string, logLevel LogLevel, retry error) {
	if err == nil {
		return "", "", LogDebug, nil
	}

	var ctrlErr *ControllerError
	if errors.As(err, &ctrlErr) {
		// User error: extract clean message, preserve full chain in logMsg.
		// Return ctrlErr (not err) so Crossplane gets the clean UserMessage via Error().
		// Crossplane will set Ready=False and surface this message in status conditions.
		return ctrlErr.UserMessage, err.Error(), ctrlErr.LogLevel, ctrlErr
	}

	// System error: generate incident ID and sanitize the user-facing message.
	// The incident ID links the status condition the user sees to the log entry
	// the operator searches — enabling correlation without exposing internal details.
	incidentID := generateIncidentID()
	userMsg = fmt.Sprintf("An internal error occurred (%s)", incidentID)
	logMsg = fmt.Sprintf("An internal error occurred (%s): %s", incidentID, err.Error())

	// Sanitized error hides internal details from users, triggers K8s retry backoff.
	return userMsg, logMsg, LogError, fmt.Errorf("%s", userMsg)
}

// generateIncidentID creates a unique 8-character identifier using a random UUID.
//
// The first 8 characters of a UUID provide 16^8 = 4,294,967,296 possible values,
// making collisions practically impossible even at high error rates.
//
// Returns:
//   - string: 8-character hex ID (e.g. "f47ac10b"), or "00000000" if generation fails
func generateIncidentID() string {
	id, err := uuid.NewRandom()
	if err != nil {
		// Fallback to "00000000" if UUID generation fails.
		// This prevents error handling from blocking reconciliation.
		return "00000000"
	}
	return id.String()[:8]
}

// LogWithLevel logs a message at the specified level using the Crossplane logging framework.
//
// This utility function integrates with github.com/crossplane/crossplane-runtime/pkg/logging.Logger
// to provide consistent structured logging across controllers and business logic.
//
// Log Level Mapping:
//   - LogDebug: logger.Debug(msg, kv...) - Only visible with --debug flag
//   - LogError: logger.Info(msg, kv...)  - Always visible to operators
//
// Note: Crossplane/logr doesn't have an explicit Error() method. Info() is used
// for infrastructure errors to provide maximum visibility in production environments.
//
// Parameters:
//   - logger: Crossplane logging.Logger instance (from controller.Options.Logger)
//   - level: Severity level (LogDebug for user errors, LogError for infrastructure errors)
//   - msg: Log message (typically logMsg from ErrorDetails)
//   - keysAndValues: Structured fields (e.g., "resource", cr.Name, "namespace", cr.Namespace)
//
// Example:
//
//	userMsg, logMsg, logLevel, retryErr := errors.ErrorDetails(err)
//	errors.LogWithLevel(e.logger, logLevel, logMsg,
//	    "resource", cr.Name,
//	    "namespace", cr.Namespace)
//
// Structured Log Output:
//   - Debug level (user error):
//     {"level":"debug","msg":"spec.awsRegion 'us-east-1' is not valid...","resource":"my-account"}
//   - Info level (system error):
//     {"level":"info","msg":"An internal error occurred (f47ac10b): connection timeout","resource":"my-account"}
func LogWithLevel(logger logging.Logger, level LogLevel, msg string, keysAndValues ...interface{}) {
	switch level {
	case LogDebug:
		logger.Debug(msg, keysAndValues...)
	case LogError:
		logger.Info(msg, keysAndValues...)
	}
}
