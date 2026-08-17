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

## Schema Specification

<!--
OPTIONAL: Include only if this feature defines or consumes YAML — a CRD (`spec`/`status`)
or a config file. Delete the whole section (including this comment) for pure-Go packages
with no YAML surface.

Rules:
- One table per YAML document or root block, and name the heading after what it
  describes. A CRD gets `Fields (spec)` plus `Fields (status)`. A config file gets one
  table named after the file — e.g. `Fields (backplane.yaml)` — and no status table.
- Field paths are relative to the heading's root and written as they appear in YAML
  (`groups.accountAdmin`), with `[]` for list elements
  (`networkPolicy.whitelisting[].user`). Do not repeat the `spec.` prefix in every row.
- `Required` is relative to its parent object: mark a field **Yes** when the parent is
  present. Note mutual exclusivity and conditional requirements in the constraints column.
- `Mutability` is about the field's own lifecycle, not the file's: **Immutable** fields
  need an enforcing validation rule — say which one. Drop the column entirely for
  documents where every field is freely editable and reloaded wholesale (most config
  files); say so in a sentence above the table instead.
- Put defaults in the constraints column (`Default: UTC`); leave it blank only if the
  field has no constraint beyond its type.
-->

### Fields (`{spec | filename.yaml}`)

| Field Path | Type | Required | Mutability | Validation / Constraints |
| ---------- | ---- | -------- | ---------- | ------------------------ |
| `{field}` | string | **Yes** | **Immutable** | {Format or enum, e.g. must match `aws-eu-central-1`. Enforced by {CEL rule / controller check}.} |
| `{field}` | string | No | Mutable | {Constraint and default, e.g. valid IANA time zone. Default: `UTC`.} |
| `{parent}.{child}` | []object | No | Mutable | {What the block configures.} |
| `{parent}.{child}[].{leaf}` | []string | No\* | Mutable | {Semantics.} <br>*Mutually exclusive with `{otherField}`.* |

<!-- OPTIONAL: CRDs only — remove this heading and its table for config files. -->

### Fields (`status`)

| Field Path | Type | Description |
| ---------- | ---- | ----------- |
| `{field}` | string | {What it reports and which component writes it.} |

<!--
Back every table above with an illustrative YAML document, added as the LAST example in
the Appendix.
-->


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
- **SC-XXX**: Unit test coverage exceeds 95%.
<!-- OPTIONAL: only if this package integrates with an external system (AWS, Snowflake, etc.) and has its own integration test suite -->
- **SC-XXX**: Integration test coverage {covers all external-system call paths / exceeds N%}.

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

<br/><br/><br/><br/><br/>

================

## Appendix: Usage Examples

<!--
Show 2-5 concrete examples of how to use this feature's API in real controller/integration code.
Examples should be copy-pasteable and show common scenarios.
If this spec has a Schema Specification section, the LAST example is an illustrative YAML
document for it — one per table in that section.
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

### Example 3: {Document Name} YAML

<!-- OPTIONAL: only for specs with a Schema Specification section; keep it last. -->

```yaml
# A valid document exercising every required field and at least one optional block
```
