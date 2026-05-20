# Specification: Error Handling (001)

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

**Important**: User errors must be created explicitly using `errors.NewUser()`, while system errors are implicit (any raw error). The logging level distinction is critical: Debug-level logging for user errors prevents noise in production logs, while Error-level logging for system errors ensures operator visibility for infrastructure failures.

## Key Concept: Incident Correlation

For every system error, the error handling system generates a unique 8-character incident ID derived from a random UUID (e.g. `f47ac10b`). This ID appears in both the user-facing status message and the operator logs, enabling correlation: when a user reports `"An internal error occurred (f47ac10b)"`, operators can search logs for `f47ac10b` to find the full error details including stack traces and internal context.

Incident IDs are generated using `github.com/google/uuid` and are globally unique — no two errors will ever share the same ID regardless of when or where they occur. With 16^8 = 4,294,967,296 possible values, collisions are practically impossible even at high error rates.

**Important**: Unlike random numeric IDs, UUID-based IDs require no shared state and are safe to generate concurrently across multiple pods and controllers.

## Technical Context

**Language/Version**: Go 1.24.0
**Primary Dependencies**: Crossplane runtime v0.19+, `github.com/google/uuid`, Go stdlib (errors, fmt)
**Storage**: N/A (errors are ephemeral, logged to stdout/stderr)
**Testing**: Go testing with comprehensive unit tests, 95% code coverage
**Performance Goals**: <100μs incident ID generation, zero allocations on happy path
**Constraints**: Thread-safe incident ID generation, Crossplane reconciliation compatible, 256-char message truncation for Kubernetes status fields

## Public API

```go
// NewUser creates a user error with an actionable message.
//
// Message Guidelines:
//   - Include field path: spec.region, spec.network.allowedIPs[0]
//   - Show invalid value: Region 'invalid'
//   - Explain expected format: (expected: aws-eu-central-1)
//   - Be specific and actionable
//
// Parameters:
//   - msg: Actionable error message (auto-truncated to 256 chars if longer)
//
// Returns:
//   - error: ControllerError with Type=TypeUser, LogLevel=LogDebug
func NewUser(msg string) error

// IsUserError checks if an error is a user error.
//
// Parameters:
//   - err: Error to check (supports wrapped errors)
//
// Returns:
//   - bool: true if error chain contains TypeUser ControllerError
func IsUserError(err error) bool

// ErrorDetails extracts error classification metadata for controller integration.
//
// Parameters:
//   - err: Error from business logic (may be ControllerError or raw error)
//
// Returns:
//   - userMsg: Clean message for Kubernetes status conditions
//   - logMsg: Full error string for structured logging
//   - logLevel: Severity level (LogDebug or LogError)
//   - retry: Error to return to Crossplane (triggers failure handling and retry)
//
// Behavior:
//   - If err contains ControllerError (user error):
//     Returns ctrlErr.UserMessage as userMsg, full error chain as logMsg,
//     LogDebug as logLevel, ctrlErr as retry error
//   - If err is raw error (system error):
//     Generates unique 8-character incident ID from UUID, returns
//     "An internal error occurred (f47ac10b)" as userMsg, full details
//     with incident ID as logMsg, LogError as logLevel,
//     sanitized error as retry error
//   - If err is nil:
//     Returns all zero/empty values
func ErrorDetails(err error) (userMsg, logMsg string, logLevel LogLevel, retry error)

// LogWithLevel logs a message at the specified level using Crossplane logging framework.
//
// Parameters:
//   - logger: Crossplane logging.Logger instance
//   - level: Severity level (LogDebug or LogError)
//   - msg: Log message (typically from ErrorDetails logMsg)
//   - keysAndValues: Structured fields (e.g., "resource", cr.Name, "namespace", cr.Namespace)
//
// Log Level Mapping:
//   - LogDebug → logger.Debug(msg, kv...) - Only visible with --debug flag
//   - LogError → logger.Info(msg, kv...) - Always visible to operators
func LogWithLevel(logger logging.Logger, level LogLevel, msg string, keysAndValues ...interface{})

// ErrorType distinguishes between user errors (actionable by the user)
// and raw errors (requiring incident ID generation in ErrorDetails).
type ErrorType int

const (
    // TypeUser indicates a user-actionable configuration error.
    // These errors are logged at Debug level and include specific field paths
    // and expected formats to enable self-service resolution.
    TypeUser ErrorType = iota
)

// LogLevel defines logging severity levels aligned with error classification
// and the Crossplane logging framework.
type LogLevel int

const (
    // LogDebug indicates user errors that should only be logged in verbose mode.
    // These represent expected configuration mistakes that don't require operator attention.
    LogDebug LogLevel = iota

    // LogError indicates infrastructure errors that should always be visible to operators.
    // These represent system failures requiring immediate investigation and are logged
    // using logger.Info() for maximum visibility.
    LogError
)

// ControllerError is a custom error type that wraps user errors with
// classification metadata and user-facing messages.
//
// Fields:
//   - Type: Classification (TypeUser)
//   - UserMessage: Clean, actionable message for Kubernetes status conditions (max 256 chars)
//   - LogLevel: Logging severity level (LogDebug for user errors)
//
// Implements error interface via Error() method.
type ControllerError struct {
    Type        ErrorType
    UserMessage string
    LogLevel    LogLevel
}
```

## Types

```go
type ErrorType int
const TypeUser ErrorType = iota  // user-actionable configuration error

type LogLevel int
const (
    LogDebug LogLevel = iota  // logger.Debug() — user errors
    LogError                  // logger.Info() — system errors
)

type ControllerError struct {
    Type        ErrorType
    UserMessage string
    LogLevel    LogLevel
}
```

## Project Structure

```text
internal/errors/
├── errors.go              # All types and functions (NewUser, ErrorDetails, LogWithLevel, types, incident ID generation)
└── errors_test.go         # Unit tests (95% coverage)
```

## Error Classification

**User Errors** (use `errors.NewUser()`):
- Invalid region format: `Region 'invalid' does not match allowed format (expected: aws-eu-central-1)`
- Malformed CIDR: `Invalid CIDR '10.0.0.256/24' in spec.network.allowedIPs[0]: not a valid IP range`
- Missing required field: `At least one contact email is required in spec.contacts`
- Value outside allowed range: `Edition 'premium' is not supported (expected: standard, enterprise)`

**System Errors** (use `fmt.Errorf("context: %w", err)`):
- Snowflake API unreachable: `failed to connect to Snowflake: dial tcp timeout`
- AWS Secrets Manager timeout: `failed to retrieve secret: context deadline exceeded`
- SQL execution failure: `failed to execute SQL: permission denied`
- Network connection error: `failed to dial Snowflake at xyz.snowflakecomputing.com`

## Edge Cases

- **What happens when error messages exceed 256 characters?** - Automatically truncated with "..." suffix for Kubernetes status fields
- **Can system errors be wrapped multiple times?** - Yes, incident IDs are preserved through error chains via fmt.Errorf("%w")
- **What if incident ID generation fails?** - Falls back to "00000000" as incident ID, logs error separately
- **How are nil errors handled?** - ErrorDetails returns all zero/empty values, no panic
- **Can user errors contain sensitive data?** - No, user messages should never include credentials, tokens, or internal IPs

## Dependencies

**N/A** - This is a foundational package with no internal dependencies

## Integration Points

- **Controller Layer** - Controllers call ErrorDetails() to extract metadata for logging and status updates - Key functions: `ErrorDetails()`, `LogWithLevel()` - Notes: Must return retryErr to Crossplane, not original error
- **Business Logic Layer** - Policy engines and provisioners create user errors for validation failures - Key functions: `NewUser()`, `IsUserError()` - Notes: Use NewUser() for config errors, raw errors for infrastructure failures
- **Crossplane Runtime** - Integration with managed.External interface and status conditions - Key functions: Returns errors from Observe/Create/Update/Delete - Notes: Crossplane automatically sets Ready=False and manages retry backoff

## Success Criteria

- **SC-001**: NewUser() creates errors with TypeUser classification
- **SC-002**: NewUser() auto-truncates messages to 256 characters
- **SC-003**: ErrorDetails() generates unique 8-character incident IDs for system errors
- **SC-004**: ErrorDetails() preserves incident IDs through wrapped error chains
- **SC-005**: IsUserError() correctly identifies user errors in wrapped chains
- **SC-006**: LogWithLevel() maps LogDebug to logger.Debug()
- **SC-007**: LogWithLevel() maps LogError to logger.Info()
- **SC-008**: System error user messages follow format "An internal error occurred (XXXXXXXX)"
- **SC-009**: System error log messages include full error details with incident ID
- **SC-010**: Incident IDs are globally unique (UUID-based, 4,294,967,296 possible values)
- **SC-011**: Incident ID generation completes in <100μs
- **SC-012**: Happy path (no error) has zero allocations
- **SC-013**: Unit test coverage exceeds 95%
- **SC-014**: All error types implement standard error interface
- **SC-015**: Error wrapping with %w preserves classification through fmt.Errorf
- **SC-016**: Nil errors handled gracefully (ErrorDetails returns empty values)

## Performance Considerations

- Incident ID generation uses `github.com/google/uuid` with crypto/rand: <100μs latency
- Error classification check via type assertion: O(1) operation
- Error wrapping preserves memory efficiency: zero allocations on happy path
- Message truncation only allocates when exceeding 256 chars

## References

- **Error Package**: `internal/errors/errors.go` - Complete implementation (types, functions, incident ID generation)
- **Test Suite**: `internal/errors/errors_test.go` - 95% code coverage
- **Usage Example**: `internal/controller/snowflakeaccount/snowflakeaccount.go` - Controller integration pattern



================

## Appendix: Usage Examples

### Example 1: Creating User Errors (Primary Use Case)

```go
import "github.com/allianz/yukimi/internal/errors"

// Validate region format
func ValidateRegion(region string) error {
    if !regionPattern.MatchString(region) {
        return errors.NewUser(fmt.Sprintf(
            "Region '%s' does not match allowed format (expected: aws-eu-central-1)",
            region))
    }
    return nil
}

// Validate CIDR blocks
func ValidateNetworkPolicy(policy *NetworkPolicy) error {
    for i, cidr := range policy.AllowedIPs {
        if _, _, err := net.ParseCIDR(cidr); err != nil {
            return errors.NewUser(fmt.Sprintf(
                "Invalid CIDR '%s' in spec.network.allowedIPs[%d]: not a valid IP range",
                cidr, i))
        }
    }
    return nil
}

// Validate required fields
func ValidateContacts(contacts []string) error {
    if len(contacts) == 0 {
        return errors.NewUser("At least one contact email is required in spec.contacts")
    }
    return nil
}
```

### Example 2: Handling Errors in Controllers (Integration Pattern)

```go
import (
    "github.com/crossplane/crossplane-runtime/pkg/logging"
    "github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
    "github.com/allianz/yukimi/internal/errors"
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

    // Call business logic (may return user error or system error)
    result, err := e.provisioner.Observe(ctx, cr, e.policy, e.namespace)
    if err != nil {
        // Extract error metadata
        userMsg, logMsg, logLevel, retryErr := errors.ErrorDetails(err)

        // Log with structured fields
        errors.LogWithLevel(e.logger, logLevel, logMsg,
            "resource", cr.Name,
            "namespace", cr.Namespace)

        // Return error to Crossplane (triggers status update and retry)
        return managed.ExternalObservation{}, retryErr
    }

    // Success path
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
    // Validate configuration (may return errors.NewUser())
    if err := e.ValidateRegion(cr.Spec.Region); err != nil {
        // Wrap with context - preserves ControllerError classification
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
        // Return wrapped error - ErrorDetails() treats this as system error
        return fmt.Errorf("failed to execute SQL: %w", err)
    }
    return nil
}
```
