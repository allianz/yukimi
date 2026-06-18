# Specification: {Feature Name} ({NNN})

## Overview

{Write 3-4 sentences that answer: (1) What does this subsystem do? (2) What problem does it solve? (3) Why is it needed in the provider? (4) What's the high-level technical approach?}

## Scope

This specification defines the {subsystem name} that:
- {Capability 1}
- {Capability 2}
- {Capability 3}

**Out of Scope**:
- {What this does NOT cover}

## Key Concept: {Topic Name}

<!--
Add 1-2 sections explaining key concepts specific to this feature.
Examples: "Key Concept: Connection Format", "Key Concept: Secret Path Structure", "Key Concept: Execution Model"
Focus on WHAT, not HOW. Explain domain-specific concepts, data formats, or behavioral models.
-->

{Explanation of key concept}

**Important**: {Critical requirement or constraint}

## Technical Context

**Language/Version**: {e.g., Go 1.23.0}
**Primary Dependencies**: {e.g., Crossplane runtime v0.19+, AWS SDK for Go v2, Snowflake Go driver v1.18.1}
**Storage**: {e.g., Kubernetes etcd (status persistence), AWS Secrets Manager (credentials), in-memory cache}
**Testing**: {e.g., Go testing, sqlmock, integration tests with .env config}
**Performance Goals**: {e.g., <100μs operation latency, zero allocations on happy path}
**Constraints**: {e.g., thread-safe, idempotent operations, Crossplane reconciliation compatible}

## Public API

```go
// Main interface or functions that other components use
type {InterfaceName} interface {
    // {MethodName} {brief description}.
    //
    // Returns:
    //   - User error if {condition}
    //   - System error if {condition}
    {MethodName}(ctx context.Context, {params}) ({ReturnType}, error)
}

// Supporting types
type {ConfigType} struct {
    {Field1} {Type} // {Description}
    {Field2} {Type} // {Description}
}
```

## Project Structure

### Source Code

```text
{Package/directory structure for this feature}

Example:
internal/{feature}/
├── manager.go           # Main interface implementation
├── types.go             # Internal types
├── config.go            # Configuration
├── manager_test.go      # Unit tests
└── integration_test.go  # Integration tests
```

## Error Classification

**User Errors** (use `errors.NewUser()`):
- {Description of user-fixable error scenario}
- {Description of user-fixable error scenario}
- {Description of user-fixable error scenario}

**System Errors** (use `fmt.Errorf("context: %w", err)`):
- {Description of infrastructure failure scenario}
- {Description of infrastructure failure scenario}
- {Description of infrastructure failure scenario}

## Edge Cases

<!--
Document unusual scenarios, corner cases, and non-obvious behaviors as a bullet list.
Format: **Question?** - Answer
-->

- **{What happens in edge case scenario X?}** - {Explain the behavior, constraints, or workarounds}
- **{How does the system handle unusual condition Y?}** - {Describe the handling, guarantees, or limitations}
- **{What are the implications of configuration Z?}** - {Clarify trade-offs, performance impacts, or constraints}

## Dependencies

<!-- OPTIONAL: Remove if no dependencies -->

- **{Dependency Name} ({NNN})** - Used APIs: `Function1()`, `Function2()`, `Function3()` - Contract: {Initialization order or requirements}
- **{Dependency Name} ({NNN})** - Used APIs: `Function1()`, `Function2()` - Contract: {Requirements}

## Integration Points

- **{Component 1}** - {Brief description of how this feature integrates} - Key functions: `Function1()`, `Function2()` - Notes: {Important constraints}
- **{Component 2}** - {Brief description of how this feature integrates} - Key functions: `Function1()`, `Function2()`

## Success Criteria

- **SC-001**: {Measurable criterion}
- **SC-002**: {Measurable criterion}
- **SC-003**: {Measurable criterion}
- **SC-004**: {Measurable criterion}
- **SC-005**: {Measurable criterion}
<!-- Add 10-20 criteria total -->

## Security Considerations

<!-- OPTIONAL: Remove if not security-sensitive -->

- {Security decision or guarantee}
- {Security decision or guarantee}

## Performance Considerations

<!-- OPTIONAL: Remove if not performance-critical -->

- {Performance requirement}
- {Performance guarantee}

## References

- **{Name}**: `{path}` - {Description}
- **{Name}**: {URL} - {Description}



================

## Appendix: Usage Examples

<!--
Show 2-4 concrete examples of how to use this feature's API in real controller/integration code.
Examples should be copy-pasteable and show common scenarios.
Always keep this section at the bottom of the spec.
-->

### Example 1: {Primary Use Case}

```go
// Show initialization, basic usage, or most common scenario
// Include necessary imports, error handling, and context
```

### Example 2: {Secondary Use Case}

```go
// Show configuration updates, edge cases, or integration with other components
```
