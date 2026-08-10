# Specification: Error Handling (001)

This specification covers two packages: `internal/errors` (user error types) and `internal/logger` (operation-scoped structured logging and error handling). They are documented together because the logger's `Handle` method is the consumer that ties error classification to logging behavior.

## Overview

This specification defines the error handling system for the Crossplane provider that distinguishes between user errors (configuration mistakes users can fix) and system errors (infrastructure failures requiring operator intervention). The system ensures appropriate logging levels, retry behavior, and seamless integration with Crossplane's managed resource pattern. By providing clear, actionable feedback for configuration errors and generating unique incident IDs for system errors, it enables self-service resolution while facilitating operator troubleshooting of infrastructure failures.

## Scope

This specification defines the error handling system that:
- Distinguishes between user errors and system errors
- Generates unique incident IDs for system error correlation
- Enables appropriate logging levels based on error classification
- Integrates seamlessly with Crossplane's managed resource pattern
- Supports error wrapping and context preservation

**Out of Scope**:
- Error recovery strategies (handled by Crossplane retry logic)
- Status condition management (handled by Crossplane runtime)
- Metric collection for error rates

## Key Concept: Error Classification

The error handling system categorizes errors into two types: **user errors** and **system errors**. User errors represent configuration mistakes that users can fix by editing their CRD (e.g., invalid region format, malformed CIDR). These are logged at Debug level and include specific field paths and expected formats to enable self-service resolution. System errors represent infrastructure failures that users cannot fix (e.g., Snowflake API unreachable, AWS Secrets Manager timeout). These are logged at Error level with unique incident IDs for correlation between user status messages and operator logs.

**Important**: User errors must be created explicitly using `errors.NewUserError()`, while system errors are implicit (any raw error). The logging level distinction is critical: Debug-level logging for user errors prevents noise in production logs, while Error-level logging for system errors ensures operator visibility for infrastructure failures.

## Key Concept: Incident Correlation

For every system error, the error handling system generates a unique 8-character incident ID derived from a random UUID (e.g. `f47ac10b`). This ID appears in both the user-facing status message and the operator logs, enabling correlation: when a user reports `"An internal error occurred (f47ac10b)"`, operators can search logs for `f47ac10b` to find the full error details including stack traces and internal context.

IDs are UUID-based: globally unique, stateless, and safe to generate concurrently across multiple pods.

## Public API

The system spans two packages with a one-way dependency: `internal/logger` imports `internal/errors` (to classify errors via `IsUserError`); `internal/errors` imports nothing internal.

### Package `internal/errors`

Error types and constructors, imported by business logic (validators, policy engines, provisioners). Has no logging concern.

```go
// NewUserError creates a user error with an actionable message.
//
// Message Guidelines:
//   - Include field path: spec.region, spec.networkPolicy.users.tu_airflow[0].allowedIPs[0]
//   - Show invalid value: Region 'invalid'
//   - Explain expected format: (expected: aws-eu-central-1)
//   - Be specific and actionable
//
// Parameters:
//   - msg: Actionable error message (must be non-empty, auto-truncated to 256 chars if longer)
//           Falls back to "invalid configuration — no details provided" if empty.
//
// Returns:
//   - error: user error whose .Error() returns the message
func NewUserError(msg string) error

// IsUserError checks if an error is a user error.
//
// Parameters:
//   - err: Error to check (supports wrapped errors)
//
// Returns:
//   - bool: true if the error chain contains a user error
func IsUserError(err error) bool
```

### Package `internal/logger`

Structured, operation-scoped logging plus the error-handling entry point, imported by controllers.

```go
// Operation identifies the controller lifecycle method in which an error occurred.
// String-based so log output is self-documenting — operators see "operation":"create"
// directly in structured logs without needing a lookup table.
type Operation string

const (
    OpObserve Operation = "observe"
    OpCreate  Operation = "create"
    OpUpdate  Operation = "update"
    OpDelete  Operation = "delete"
)

// Logger holds contextual dimensions for a single controller method invocation.
// Create one Logger per controller method call and pass it to business logic
// for consistent structured log fields across all log calls within that scope.
type Logger struct { /* unexported fields */ }

// New creates a Logger pre-loaded with the contextual dimensions for a
// single controller method invocation. Every log call made through this Logger
// includes namespace, kind, resource name, and operation as structured KV pairs.
//
// Parameters:
//   - logger:    Crossplane logging.Logger from controller.Options.Logger
//   - namespace: Kubernetes namespace of the managed resource (cr.Namespace)
//   - kind:      Kubernetes Kind of the managed resource (e.g., "SnowflakeAccount")
//   - name:      Name of the managed resource (cr.Name)
//   - op:        Controller lifecycle method (OpObserve, OpCreate, OpUpdate, OpDelete)
//
// Returns:
//   - *Logger: Logger ready for use; never nil
func New(logger logging.Logger, namespace, kind, name string, op Operation) *Logger

// Info logs an informational message with all contextual dimensions as structured
// KV pairs: namespace, kind, resource name, and operation.
//
// Use for significant lifecycle events (resource created, deleted) that should
// always be visible to operators regardless of log level settings.
func (l *Logger) Info(msg string)

// Debug logs a diagnostic message with all contextual dimensions as structured KV pairs.
//
// Use for detailed state information (observed state, computed diffs) only visible
// when running with --debug flag.
func (l *Logger) Debug(msg string)

// Handle processes an error from business logic in a single call: it classifies
// the error, logs at the appropriate level with all contextual dimensions, and
// returns the retry error for Crossplane.
//
// Behavior by error type:
//   - nil:          Returns nil immediately. No logging occurs.
//   - User error:   Logs at Debug level (only visible with --debug flag).
//                   Returns an error whose .Error() gives the user-facing message directly.
//   - System error: Logs at Info level (always visible) with the error message and
//                   incident ID. Returns a sanitized error whose .Error() gives
//                   "An internal error occurred (XXXXXXXX)" — safe for status conditions.
//
// The returned error's .Error() is always suitable for cr.SetConditions(...WithMessage(...)).
//
// Parameters:
//   - err: Error from business logic (user error, wrapped error, or raw error)
//
// Returns:
//   - error: nil if err is nil; otherwise the retry error (non-nil tells Crossplane the operation failed)
func (l *Logger) Handle(err error) error
```

## Project Structure

```text
internal/errors/
├── errors.go              # User error type, NewUserError, IsUserError
└── errors_test.go         # Unit tests (95% coverage)

internal/logger/
├── logger.go              # Logger, New, Operation, Info/Debug/Handle, incident ID generation
└── logger_test.go         # Unit tests (95% coverage)
```

`internal/logger` depends on `internal/errors` (via `IsUserError`). The reverse never happens.

## Error Classification

**User Errors** (use `errors.NewUserError()`):
- Invalid region format: `Region 'invalid' does not match allowed format (expected: aws-eu-central-1)`
- Malformed CIDR: `Invalid CIDR '10.0.0.256/24' in spec.networkPolicy.users.tu_airflow[0].allowedIPs[0]: not a valid IP range`
- Missing required field: `At least one contact email is required in spec.contacts`
- Value outside allowed range: `Edition 'premium' is not supported (expected: standard, enterprise)`

**System Errors** (use `fmt.Errorf("context: %w", err)`):
- Snowflake API unreachable: `failed to connect to Snowflake: dial tcp timeout`
- AWS Secrets Manager timeout: `failed to retrieve secret: context deadline exceeded`
- SQL execution failure: `failed to execute SQL: permission denied`
- Network connection error: `failed to dial Snowflake at xyz.snowflakecomputing.com`

## Edge Cases

- **What happens when error messages exceed 256 characters?** - Automatically truncated with "..." suffix for Kubernetes status fields
- **What if incident ID generation fails?** - `uuid.New()` panics on `crypto/rand` failure (an OS-level catastrophe). The Crossplane reconciler's built-in panic handler catches it and requeues the resource — the same outcome as a reconciliation error, without silently producing duplicate IDs.
- **What if the retry error from Handle is re-wrapped and passed to Handle again?** - A new incident ID is generated. The sanitized retry error is a plain string error with no user error in its chain and no reference to the original error. Controllers must pass the result of Handle directly to Crossplane — never re-wrap and re-process it through Handle.

## Dependencies

- `internal/errors` - Foundational package with no internal dependencies
- `internal/logger` - Depends only on `internal/errors` (via `IsUserError`)

## Integration Points

- **Controller Layer** - Controllers create a Logger at the start of each method and call Handle() for all error paths - Key functions: `logger.New()`, `Logger.Handle()`, `Logger.Info()`, `Logger.Debug()` - Notes: Must return the error from Handle() to Crossplane; use Handle().Error() for status condition messages
- **Business Logic Layer** - Policy engines and provisioners create user errors for validation failures - Key functions: `errors.NewUserError()`, `errors.IsUserError()` - Notes: Use NewUserError() for config errors, raw errors for infrastructure failures
- **Crossplane Runtime** - Integration with managed.External interface and status conditions - Key functions: Returns errors from Observe/Create/Update/Delete - Notes: Crossplane automatically sets Ready=False and manages retry backoff

## Success Criteria

- **SC-001**: NewUserError() creates errors that IsUserError() recognises
- **SC-002**: NewUserError() auto-truncates messages to 256 characters
- **SC-002b**: NewUserError() falls back to "invalid configuration — no details provided" for empty messages
- **SC-003**: Handle() generates unique 8-character incident IDs for system errors
- **SC-004**: Handle() preserves incident IDs through wrapped error chains
- **SC-005**: IsUserError() correctly identifies user errors in wrapped chains
- **SC-008**: System error user messages follow format "An internal error occurred (XXXXXXXX)"
- **SC-009**: System error log messages include the error message and incident ID
- **SC-010**: Incident IDs are globally unique (UUID-based, 4,294,967,296 possible values)
- **SC-011**: Incident ID generation completes in <100μs
- **SC-012**: Happy path (no error) has zero allocations
- **SC-013**: Unit test coverage exceeds 95%
- **SC-014**: User errors implement the standard error interface (.Error() returns the user-facing message)
- **SC-015**: Error wrapping with %w preserves classification through fmt.Errorf
- **SC-016**: Nil errors handled gracefully (Handle returns nil)
- **SC-017**: Operation constants produce correct string values (`observe`, `create`, `update`, `delete`)
- **SC-018**: logger.New() returns a non-nil `*Logger`
- **SC-019**: Logger.Info() calls underlying logger at Info level with namespace, kind, resource, and operation KV pairs
- **SC-020**: Logger.Debug() calls underlying logger at Debug level with the same KV pairs
- **SC-021**: Logger.Handle(nil) returns nil and makes no log calls
- **SC-022**: Logger.Handle with user error logs at Debug level; returned error's .Error() equals the user-facing message
- **SC-023**: Logger.Handle with wrapped user error correctly classifies via error chain
- **SC-024**: Logger.Handle with system error logs at Info level with the error message and incident ID; returned error's .Error() follows the incident ID format
- **SC-025**: Logger.Handle produces unique incident IDs across multiple calls

## Performance Considerations

- Incident ID generation uses `github.com/google/uuid` with crypto/rand: <100μs latency
- Error classification check via type assertion: O(1) operation
- Error wrapping preserves memory efficiency: zero allocations on happy path
- Message truncation only allocates when exceeding 256 chars

## References

- **Error Package**: `internal/errors/errors.go` - User error type, NewUserError, IsUserError
- **Logger Package**: `internal/logger/logger.go` - Logger, New, Operation, Info/Debug/Handle, incident ID generation
- **Test Suites**: `internal/errors/errors_test.go`, `internal/logger/logger_test.go` - 95% code coverage

---

<br/><br/><br/><br/>

## Appendix: Usage Examples

### Example 1: Creating User Errors (Primary Use Case)

```go
import "github.com/allianz/yukimi/internal/errors"

// Validate region format
func ValidateRegion(region string) error {
    if !regionPattern.MatchString(region) {
        return errors.NewUserError(fmt.Sprintf(
            "Region '%s' does not match allowed format (expected: aws-eu-central-1)",
            region))
    }
    return nil
}

// Validate CIDR blocks. Each technical user under networkPolicy.users holds a list
// of connection entries, so the field path carries both the user and the entry index.
func ValidateUserConnections(user string, entries []ConnectionEntry) error {
    for i, entry := range entries {
        for j, cidr := range entry.AllowedIPs {
            if _, _, err := net.ParseCIDR(cidr); err != nil {
                return errors.NewUserError(fmt.Sprintf(
                    "Invalid CIDR '%s' in spec.networkPolicy.users.%s[%d].allowedIPs[%d]: not a valid IP range",
                    cidr, user, i, j))
            }
        }
    }
    return nil
}

// Validate required fields
func ValidateContacts(contacts []string) error {
    if len(contacts) == 0 {
        return errors.NewUserError("At least one contact email is required in spec.contacts")
    }
    return nil
}
```

### Example 2: Handling Errors in Controllers (Logger Pattern)

```go
import (
    "github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
    "github.com/allianz/yukimi/internal/logger"
)

type external struct {
    logger logging.Logger
    // ... other fields
}

func (e *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
    cr, ok := mg.(*v1alpha1.SnowflakeAccount)
    if !ok {
        return managed.ExternalObservation{}, fmt.Errorf("managed resource is not a SnowflakeAccount")
    }

    // Create Logger once per method call — pre-loads namespace, kind, name, operation
    log := logger.New(e.logger, cr.Namespace, "SnowflakeAccount", cr.Name, logger.OpObserve)

    result, err := e.provisioner.Observe(ctx, cr)
    if err != nil {
        // Single call: classifies, logs, and returns sanitized retry error
        retryErr := log.Handle(err)
        cr.SetConditions(xpv1.Unavailable().WithMessage(retryErr.Error()))
        return managed.ExternalObservation{}, retryErr
    }

    log.Debug("observed current state")
    cr.SetConditions(xpv1.Available())
    return managed.ExternalObservation{
        ResourceExists:   result.Exists,
        ResourceUpToDate: result.UpToDate,
    }, nil
}
```

### Example 3: Error Wrapping in Business Logic

```go
// In internal/policy/engine.go
func (e *Engine) BuildTargetState(ctx context.Context, cr *v1alpha1.SnowflakeAccount) (AccountState, error) {
    // Validate configuration (may return errors.NewUserError())
    if err := e.ValidateRegion(cr.Spec.Region); err != nil {
        // Wrap with context - preserves user error classification
        return AccountState{}, fmt.Errorf("validation failed: %w", err)
    }

    // Apply defaults (may return system error)
    if err := e.ApplyDefaults(cr); err != nil {
        return AccountState{}, fmt.Errorf("failed to apply defaults: %w", err)
    }

    return targetState, nil
}

// Infrastructure failure - return raw error
func (c *Client) Execute(ctx context.Context, sql string) error {
    if err := c.snowflake.Exec(ctx, sql); err != nil {
        // Return wrapped error - Handle() treats this as a system error
        return fmt.Errorf("failed to execute SQL: %w", err)
    }
    return nil
}
```
