# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This project builds a self-service platform for provisioning and managing Snowflake accounts and related data platform resources (see `specs/design.md`). It is scaffolded from the Crossplane provider template and reuses its code layout, Go tooling, and Kubernetes CRD/controller conventions — but the goal is not to build a standard Crossplane provider for the wider Crossplane ecosystem. There is no intent to publish this as a general-purpose, community-facing provider; the CRDs, controllers, and specs are shaped around this platform's own tenant-onboarding and Snowflake-provisioning model, not around Crossplane ecosystem conventions or compatibility.

## Key Architecture

### Directory Structure
```
apis/
├── base/v1alpha1/       # SnowflakeAccount and SnowflakeDeletionRequest types
├── v1alpha1/            # Group metadata for snowflake.yukimi.io (registers no API types)
└── yukimi.go            # API group registration

internal/
├── account/
│   ├── modules/account/ # CREATE ACCOUNT and platform user bootstrapping
│   ├── pipeline/        # Module interface, outcomes, condition aggregation
│   └── tenant/          # Account naming, namespace labels, account URLs
├── config/
│   ├── backplane/       # Per-region backplane inventory, parameters, allowlist
│   └── base/            # Platform-wide settings from a mounted ConfigMap
├── controller/          # Controller registration (yukimi.go), one subpackage per resource
│   ├── snowflakeaccount/
│   └── snowflakedeletionrequest/
├── deletion/            # Deletion request lookup and consumption (positive control)
├── errors/              # User error types (NewUserError, IsUserError)
├── logger/              # Operation-scoped logging and error handling (Handle, incident IDs)
├── secrets/             # Backend interface, secret paths, RSA keypairs, TTL cache
│   └── aws/             # AWS Secrets Manager backend
├── snowflake/
│   ├── host/            # Connection host and account URL construction
│   ├── pool/            # Pooled JWT keypair connections, org-admin vs per-account scopes
│   └── statement/       # SQL execution, safe rendering, materialized rows
└── version/             # Version information

cmd/provider/            # Main controller binary (directory name is scaffolding legacy)
package/                 # Generated CRD manifests
hack/helpers/            # Code generation templates
```

Only what exists today is shown above. Planned package locations for not-yet-implemented specs are listed in the table below.

### Specification Documents

Each `internal/` package has a corresponding numbered spec in `specs/`. The spec is the authoritative source for that package — before implementing or modifying code in a package, always read its spec first.

A spec's `## Overview` and `## Key Concept: …` sections are written for humans, not implementers: a reader should be able to understand what the subsystem does and roughly predict how it behaves without opening the code. Keep them high level and conceptual — the mental model and why it is shaped that way, in domain terms. No type, method or field names, no algorithms in prose, no case or error enumeration; that detail belongs in the sections below them. 

Specs are written and implemented one at a time in ascending order, so **a spec may depend only on specs numbered strictly below it** — the code for higher-numbered specs does not exist yet. For a spec not yet written, `specs/scope-NNN-<slug>.md` (if present) gives a starting-point idea of its intended scope — see that file's own header for how much weight to give it; `specs/design.md` is always the authoritative source. A letter suffix (`003.a`) marks a pluggable backend implementing an interface owned by its parent number; it sorts between `003` and `004`, and only `cmd/provider/main.go` may depend on one.

A `specs/wip-NNN-<slug>.md` is a clarification record produced by `/yukimi.clarify NNN`: decisions, problem areas, open questions and the verified research behind them. It is not product design and not a spec. Read it together with the scope note when writing `NNN-<slug>.md`.

The two transient documents are deleted at different points:

- `scope-NNN-<slug>.md` — delete once `NNN-<slug>.md` is written.
- `wip-NNN-<slug>.md` — keep until the code for `NNN` is implemented, then delete. It holds the detail the spec leaves out on purpose, so the spec can stay at the level of contracts and mental models instead of restating the research. Where a wip record and its spec disagree, the spec wins.

| Spec | Package | Description |
|------|---------|-------------|
| `001-error-and-logging.md` | `internal/errors/` + `internal/logger/` | User vs system errors, incident IDs, operation-scoped logging |
| `002-base-config.md` | `internal/config/base/` | Platform-wide settings loaded from a mounted ConfigMap |
| `003-secrets-handling.md` | `internal/secrets/` | Backend interface, secret paths, RSA keypairs, TTL cache |
| `003.a-aws-secrets-backend.md` | `internal/secrets/aws/` | AWS Secrets Manager implementation of the 003 backend interface |
| `004-connection-pooling.md` | `internal/snowflake/pool/` + `internal/snowflake/host/` | Pooled JWT keypair connections, org-admin vs per-account scopes; connection host and account URL construction |
| `005-statement-execution.md` | `internal/snowflake/statement/` | SQL execution with safe rendering, error decoration and a materialized row type |
| `006-snowflake-account-crd.md` | `apis/base/v1alpha1/` + `internal/account/tenant/` | SnowflakeAccount schema, account naming, namespace labels |
| `007-backplane-config.md` | `internal/config/backplane/` | Per-region backplane inventory, parameters, allowlist |
| `008-guardrails.md` | `internal/config/guardrails/` | Tenant input constraints, approved exceptions |
| `009-account-pipeline.md` | `internal/account/pipeline/` | Module interface, outcomes, condition aggregation |
| `010-guardrail-check.md` | `internal/account/modules/guardrailcheck/` | Admission check: guardrails (008) evaluation as a pipeline module, aborts before account creation |
| `011-quota-check.md` | `internal/account/modules/quotacheck/` | Admission check: claimed vs. namespace credit-quota allowance, aborts before account creation |
| `012-account-module.md` | `internal/account/modules/account/` | `CREATE ACCOUNT` and platform user bootstrapping |
| `013-parameter-module.md` | `internal/account/modules/parameter/` | Global and regional account parameter enforcement |
| `014-network-module.md` | `internal/account/modules/network/` | Network rules and policies, baseline plus custom |
| `015-auth-module.md` | `internal/account/modules/auth/` | SSO-only baseline and per-user auth exceptions |
| `016-identity-sync-request.md` | `apis/identity/v1alpha1/` + `internal/identitysync/` | IdentitySyncRequest contract and emitter |
| `017-identity-module.md` | `internal/account/modules/identity/` | Group import and system role bindings |
| `018-quota-monitor.md` | `internal/account/modules/quotamonitor/` | Resource monitor/budget enforcement, credit-exhaustion condition |
| `019-deletion-request.md` | `apis/base/v1alpha1/` + `internal/deletion/` | Deletion requests (positive control) |
| `020-snowflakeaccount-controller.md` | `internal/controller/snowflakeaccount/` | Module wiring, validation phase, deletion gate, reporting |
| `021-replication.md` | `apis/base/v1alpha1/` + `internal/replication/` | SnowflakeReplication setup, auto-repair, manual failover |


## Controller Guidelines

These controllers use the standard Crossplane managed resource reconciler (`crossplane-runtime`). Each resource type has its own controller in `internal/controller/`, registered in `internal/controller/yukimi.go`.

- Set `xpv1.Available()` in exactly one place per controller — usually after a successful apply. Ready means provisioned and normally latches: once true it stays true, and a spec that can no longer be applied reports `Synced=False` rather than going un-Ready.
- On error in Observe, set `xpv1.Unavailable().WithMessage(userMsg)` and return the handled error, not nil — nil only sets `Synced=True` and, with a zero-value `ExternalObservation`, can trigger a spurious `Create`.
- Do not implement retries in controller code. On error, return and let Kubernetes handle the retry.
- **Error handling in Observe**: create a `Logger` at method start, call `log.Handle(err)` to get `retryErr`, set `xpv1.Unavailable().WithMessage(retryErr.Error())`, and return `retryErr`.
- **Error handling in Create/Update/Delete**: call `log.Handle(err)` and return the result — the framework turns that into `Synced`, so never set `Synced` yourself. `Ready` and any resource-specific conditions remain the controller's to set, on both the success and error paths.


## Error Handling

The project uses a standardized error handling system split across two packages: `internal/errors` provides user error types (imported by business logic), and `internal/logger` provides operation-scoped logging plus the `Handle` entry point (imported by controllers). `internal/logger` depends on `internal/errors`; never the reverse.

### Usage in Business Logic

```go
import "github.com/allianz/yukimi/internal/errors"

// User error - configuration mistake
if !regionPattern.MatchString(region) {
    return errors.NewUserError(fmt.Sprintf(
        "Region '%s' does not match allowed format (expected: aws-eu-central-1)",
        region))
}

// System error - infrastructure failure
if err := snowflakeClient.Execute(sql); err != nil {
    return fmt.Errorf("failed to execute SQL: %w", err)
}
```

### Usage in Controllers

```go
import "github.com/allianz/yukimi/internal/logger"

func (e *external) Observe(ctx context.Context, cr *v1alpha1.SnowflakeAccount) (managed.ExternalObservation, error) {
    log := logger.New(e.logger, cr.Namespace, "SnowflakeAccount", cr.Name, logger.OpObserve)

    labels, err := e.namespaceLabels(ctx, cr.Namespace)
    if err != nil {
        retryErr := log.Handle(err)
        return managed.ExternalObservation{}, retryErr // returning nil would report Synced=True
    }
    // ... success path
}
```

### Error Classification

- **User Errors**: Configuration mistakes users can fix by editing their CRD
  - Logged at Debug level (only visible with --debug flag)
  - Examples: invalid region format, malformed CIDR, missing required field

- **System Errors**: Infrastructure failures requiring operator intervention
  - Logged at Info level (always visible to operators)
  - Include unique 8-character incident IDs for correlation
  - Examples: Snowflake API unreachable, AWS Secrets Manager timeout

## Code Organization Philosophy

### Business Logic Placement
- **Core Principle**: Business logic resides in `internal/` packages (outside controllers) to maximize test coverage
- Controllers are thin orchestration layers that validate, call business logic, and update status
- This allows unit and integration testing without Kubernetes infrastructure
- Internal packages may depend on Crossplane/Kubernetes types as input/output (e.g., `xpv1.Condition`)

### Package Organization Conventions
- **No `types.go` files**: Type definitions live alongside their implementation in descriptively-named files
- **No `_impl.go` files**: Interface and implementation belong together in the same file

## Copyright Headers

New files use 

```
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
```

Files inherited from the Crossplane provider template have both headers: the original `Copyright 2025 The Crossplane Authors.` followed by `Copyright 2026 The Yukimi Authors.`

## Development Commands

### Build & Test
```bash
make test               # Run unit tests (uses -short flag to skip integration tests)
make test-integration   # Run integration tests only (requires AWS and Snowflake access)
make reviewable         # Run full validation: generate, lint, test
```

### Integration Tests
`TestIntegration...` tests load `.env` themselves (e.g. via `godotenv.Load`), so they also run directly from an IDE's test runner (single-click "run test"), not just via `make test-integration`. Resources they create use a `test-`/`integration-test-` prefix, with a timestamp suffix where useful to avoid collisions.

### Local Development
```bash
make dev                # Create kind cluster and run the controllers with debug logging
make dev-clean          # Clean up local development cluster
```

### Code Generation & Adding New Resource Types
See [docs/development/development.md](docs/development/development.md#adding-new-managed-resource-types)
for regenerating auto-generated code (`make generate`) and scaffolding a new managed resource type.

### E2E Tests


## Resources & References

### General Reference Specs
- `specs/design.md` - Product requirements, resource schemas, and behavior specifications
- `docs/development/development.md` - Development setup, Makefile targets, and scaffolding new managed resource types

