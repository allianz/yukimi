# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This project builds a self-service platform for provisioning and managing Snowflake accounts and related data platform resources (see `specs/design.md`). It is scaffolded from the Crossplane provider template and reuses its code layout, Go tooling, and Kubernetes CRD/controller conventions — but the goal is not to build a standard Crossplane provider for the wider Crossplane ecosystem. There is no intent to publish this as a general-purpose, community-facing provider; the CRDs, controllers, and specs are shaped around this platform's own tenant-onboarding and Snowflake-provisioning model, not around Crossplane ecosystem conventions or compatibility.

## Key Architecture

### Directory Structure
```
apis/
├── v1alpha1/            # Provider config & usage types (currently registers no API types)
└── yukimi.go            # API group registration

internal/
├── controller/          # Controller registration (per-resource controllers are added here as specs land)
├── errors/              # User error types (NewUserError, IsUserError)
├── logger/              # Operation-scoped logging and error handling (Handle, incident IDs)
└── version/             # Version information

cmd/provider/            # Main provider binary
package/                 # Crossplane package manifests & CRDs
hack/helpers/            # Code generation templates
```

Only what exists today is shown above. Planned package locations for not-yet-implemented specs are listed in the table below.

### Specification Documents

Each `internal/` package has a corresponding numbered spec in `specs/`. The spec is the authoritative source for that package — before implementing or modifying code in a package, always read its spec first.

Specs are written and implemented one at a time in ascending order, so **a spec may depend only on specs numbered strictly below it** — the code for higher-numbered specs does not exist yet. For a spec not yet written, `specs/scope-NNN-<slug>.md` (if present) gives a starting-point idea of its intended scope — see that file's own header for how much weight to give it; `specs/design.md` is always the authoritative source. A letter suffix (`003.a`) marks a pluggable backend implementing an interface owned by its parent number; it sorts between `003` and `004`, and only `cmd/provider/main.go` may depend on one.

| Spec | Package | Description |
|------|---------|-------------|
| `001-error-and-logging.md` | `internal/errors/` + `internal/logger/` | User vs system errors, incident IDs, operation-scoped logging |
| `002-base-config.md` | `internal/config/` | Provider-wide settings loaded from a mounted ConfigMap |
| `003-secrets-handling.md` | `internal/secrets/` | Backend interface, secret paths, RSA keypairs, TTL cache |
| `003.a-aws-secrets-backend.md` | `internal/secrets/aws/` | AWS Secrets Manager implementation of the 003 backend interface |
| `004-connection-pooling.md` | `internal/snowflake/pool/` | Pooled JWT keypair connections, org-admin vs per-account scopes |
| `005-statement-execution.md` | `internal/snowflake/statement/` | SQL execution with safe rendering, error decoration and a materialized row type |
| `006-snowflake-account-crd.md` | `apis/base/v1alpha1/` + `internal/tenant/` | SnowflakeAccount schema, account naming, namespace labels |
| `007-backplane-config.md` | `internal/backplane/` | Per-region backplane inventory, parameters, allowlist |
| `008-guardrails.md` | `internal/guardrails/` | Tenant input constraints, presets, approved exceptions |
| `009-account-pipeline.md` | `internal/account/` | Module interface, outcomes, condition aggregation |
| `010-account-module.md` | `internal/account/modules/account/` | `CREATE ACCOUNT` and platform user bootstrapping |
| `011-parameter-module.md` | `internal/account/modules/parameter/` | Global and regional account parameter enforcement |
| `012-network-module.md` | `internal/account/modules/network/` | Network rules and policies, baseline plus custom |
| `013-auth-module.md` | `internal/account/modules/auth/` | SSO-only baseline and per-user auth exceptions |
| `014-identity-sync-request.md` | `apis/identity/v1alpha1/` + `internal/identitysync/` | IdentitySyncRequest contract and emitter |
| `015-identity-module.md` | `internal/account/modules/identity/` | Group import and system role bindings |
| `016-quota.md` | `internal/quota/` | Credit quota admission, resource monitors, exhaustion |
| `017-deletion-request.md` | `apis/base/v1alpha1/` + `internal/deletion/` | Deletion warrants (positive control) |
| `018-snowflakeaccount-controller.md` | `internal/controller/snowflakeaccount/` | Module wiring, validation phase, deletion gate, reporting |
| `019-replication.md` | `apis/base/v1alpha1/` + `internal/replication/` | SnowflakeReplication setup, auto-repair, manual failover |


## Crossplane Controller Types

This provider uses the standard Crossplane managed resource reconciler (`crossplane-runtime`). Each resource type has its own controller in `internal/controller/`, registered in `internal/controller/snowflake.go`. There are three distinct controller patterns that differ in how they handle external state.

### Validation-Only Controllers

- No external resource to manage — all logic lives in Observe
- Create, Update, and Delete are no-ops
- Observe always returns `ResourceExists: true` and `ResourceUpToDate: true` so the reconciler never calls Create or Update
- Only run validation when the spec has changed (`ObservedGeneration != Generation`); skip validation and return early otherwise to save CPU cycles
- In Observe, detect deletion by checking `GetDeletionTimestamp()` and return `ResourceExists: false` to release the finalizer

### Standard Controllers with External State (e.g., SnowflakeAccount)

- Observe queries the external system to determine whether the resource exists and whether it has drifted
- Create, Update, and Delete interact with the external system directly
- Errors in Create/Update/Delete return `retryErr` so the framework sets appropriate conditions

### Shared Across All Controller Types

- On successful observation, set condition to `xpv1.Available()`
- On error in Observe, set `xpv1.Unavailable().WithMessage(userMsg)` and return nil to avoid retry flood
- Do not implement retries in controller code. On error, return and let Kubernetes handle the retry.
- **Error handling in Observe**: create a `Logger` at method start, call `log.Handle(err)` to get `retryErr`, set `xpv1.Unavailable().WithMessage(retryErr.Error())`, and return nil. Returning nil prevents exponential backoff retry loops for user-fixable errors.
- **Error handling in Create/Update/Delete**: call `log.Handle(err)` and return the result. The framework automatically sets conditions when these methods return an error, so the controller should not set conditions itself.


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

func (e *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
    log := logger.New(e.logger, cr.Namespace, "SnowflakeAccount", cr.Name, logger.OpObserve)

    result, err := e.policy.BuildTargetState(ctx, cr)
    if err != nil {
        retryErr := log.Handle(err)
        cr.SetConditions(xpv1.Unavailable().WithMessage(retryErr.Error()))
        return managed.ExternalObservation{}, nil // nil avoids retry flood; the condition already reports the failure
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
- **Add `loader.go`**: When the package needs to load CRDs or process CRD fields (unmarshaling, transformation, validation)

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

### Code Generation
```bash
make generate           # Regenerate all auto-generated code (run after API changes)
```

### Local Development
```bash
make dev                # Create kind cluster and run provider with debug logging
make dev-clean          # Clean up local development cluster
```

### Adding New Resource Types
- Uses `.yukimi.io` domain
- API groups (per specs/design.md): `base.snowflake.yukimi.io` (SnowflakeAccount, SnowflakeReplication, SnowflakeDeletionRequest) and `base.identity.yukimi.io` (IdentitySyncRequest — emitted by this platform's controller, fulfilled by a company-specific controller outside this repo)
- Today's code has not yet migrated: `apis/v1alpha1/` (group `snowflake.yukimi.io`) currently registers no API types
- All APIs currently at v1alpha1 version

Use the scaffolding system instead of manual creation:
```bash
export type=SnowflakeAccount   # CamelCase kind, per specs/design.md
make provider.addtype provider=Snowflake group=base kind=${type}
make reviewable         # Regenerate and validate
```

After scaffolding:
1. Update `apis/yukimi.go` to register the new API group
2. Update `internal/controller/yukimi.go` to register the new controller
3. Implement the actual controller logic in the generated files

#### Generated Files (Never Edit)
All files matching `zz_generated.*` are auto-generated:
- `**/zz_generated.deepcopy.go` - Deep copy methods
- `**/zz_generated.managed.go` - Managed resource interfaces
- `**/zz_generated.managedlist.go` - Managed resource list types

#### Templates
- API scaffolding uses templates in `hack/helpers/apis/` with gomplate substitution
- Controller scaffolding uses templates in `hack/helpers/controller/`
- Templates support environment variables: `PROVIDER`, `GROUP`, `KIND`, `APIVERSION`


### E2E Tests
```bash
make e2e.automated  # Fully automated with kind cluster
make e2e.manual     # Against running 'make dev' in another terminal
```

## Resources & References

### General Reference Specs
- `specs/design.md` - Product requirements, resource schemas, and behavior specifications

