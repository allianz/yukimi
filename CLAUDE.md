# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a Crossplane provider for Snowflake that enables self-service provisioning of Snowflake accounts and related data platform resources. Built using the standard Crossplane provider pattern with Go and Kubernetes APIs.

## Key Architecture

### Directory Structure
```
apis/                     # Kubernetes API types
├── v1alpha1/            # Provider config & usage types
├── infra/v1alpha1/      # Infrastructure resource types (e.g., SnowflakeAccount)
└── snowflake.go         # API registration

internal/
├── controller/          # Controller implementations
│   ├── providerconfig/  # ProviderConfig controller
│   ├── snowflakeaccount/ # SnowflakeAccount controller
│   ├── snowflakeexecution/ # SnowflakeExecution controller
│   └── snowflaketemplate/ # SnowflakeTemplate controller
├── connection/          # ProviderConfig business logic (validation, init, health)
├── snowflake/
│   ├── pool/           # Connection pool management with JWT auth
│   └── statement/      # SQL execution with position-aware errors
├── secrets/            # AWS Secrets Manager with in-memory caching
├── template/           # Template validation (naming, version chain, SQL)
├── execution/          # SQL execution orchestration (inline & template modes)
├── errors/             # Error handling (user vs system, incident IDs)
├── features/           # Feature flags
└── version/            # Version information

cmd/provider/           # Main provider binary
package/               # Crossplane package manifests & CRDs
examples/              # Example resource manifests
hack/helpers/          # Code generation templates
```

### Specification Documents

Each `internal/` package has a corresponding numbered spec in `specs/`. The spec is the authoritative source for that package — before implementing or modifying code in a package, always read its spec first.

| Spec | Package | Description |
|------|---------|-------------|
| `001-error-handling.md` | `internal/errors/` | Error handling system (user vs system errors, incident IDs) |
| `002-secrets-handling.md` | `internal/secrets/` | AWS Secrets Manager integration with caching |
| `003-connection-pooling.md` | `internal/snowflake/pool/` | Connection pool management with JWT auth |
| `004-statement-execution.md` | `internal/snowflake/statement/` | SQL execution with position-aware errors |
| `005-provider-config.md` | `internal/controller/providerconfig/` + `internal/connection/` | ProviderConfig singleton initialization |
| `006-provider-governance.md` | `internal/governance/` | ProviderGovernance validation |
| `007-snowflake-account.md` | `internal/account/` + `internal/controller/snowflakeaccount/` | SnowflakeAccount provisioning orchestration |
| `008-account-module.md` | `internal/account/modules/account/` | Account creation module |
| `009-backplane-module.md` | `internal/account/modules/backplane/` | Backplane integration module |
| `010-parameter-module.md` | `internal/account/modules/parameter/` | Parameter enforcement module |
| `011-network-module.md` | `internal/account/modules/network/` | Network policy module |
| `012-identity-module.md` | `internal/account/modules/identity/` | Identity and SCIM module |
| `013-snowflake-template.md` | `internal/template/` + `internal/controller/snowflaketemplate/` | Template validation |
| `014-snowflake-execution-foundation.md` | `internal/execution/` + `internal/controller/snowflakeexecution/` | SnowflakeExecution controller and orchestration |
| `015-snowflake-inline-execution.md` | `internal/execution/` | Inline SQL execution mode |
| `016-snowflake-template-execution.md` | `internal/execution/` | Template-based execution mode |

## Crossplane Controller Types

This provider uses the standard Crossplane managed resource reconciler (`crossplane-runtime`). Each resource type has its own controller in `internal/controller/`, registered in `internal/controller/snowflake.go`. There are three distinct controller patterns that differ in how they handle external state.

### Validation-Only Controllers (e.g., SnowflakeTemplate)

- No external resource to manage — all logic lives in Observe
- Create, Update, and Delete are no-ops
- Observe always returns `ResourceExists: true` and `ResourceUpToDate: true` so the reconciler never calls Create or Update
- Only run validation when the spec has changed (`ObservedGeneration != Generation`); skip validation and return early otherwise to save CPU cycles
- In Observe, detect deletion by checking `GetDeletionTimestamp()` and return `ResourceExists: false` to release the finalizer

### Execute-Once Controllers Without Drift Detection (e.g., SnowflakeExecution)

- External state is created but never queried — no drift detection
- Observe tracks existence via status fields (e.g., `LastExecutionTime != nil`) rather than querying an external system
- Up-to-date check uses generation tracking: resource is up-to-date when `ObservedGeneration == Generation`
- **Critical**: Create must explicitly persist status with `kube.Status().Update()` to prevent re-execution loops — without this, the next Observe would see no status and trigger Create again
- Deletion uses a `DeletionCompleted` status flag to prevent re-execution of cleanup SQL
- Custom finalizer ensures cleanup SQL runs before the resource is removed
- Errors in Create and Delete return `retryErr` so the framework handles conditions

### Standard Controllers with External State (e.g., SnowflakeAccount)

- Observe queries the external system to determine whether the resource exists and whether it has drifted
- Create, Update, and Delete interact with the external system directly
- Errors in Create/Update/Delete return `retryErr` so the framework sets appropriate conditions

### Shared Across All Controller Types

- On successful observation, set condition to `xpv1.Available()`
- On error in Observe, set `xpv1.Unavailable().WithMessage(userMsg)` and return nil to avoid retry flood
- Do not implement retries in controller code. On error, return and let Kubernetes handle the retry.
- **Error handling in Observe**: extract error details with `errors.ErrorDetails(err)`, log at the appropriate level, set condition, and return nil. Returning nil prevents exponential backoff retry loops for user-fixable errors.
- **Error handling in Create/Update/Delete**: extract error details and log them, then return `retryErr`. The framework automatically sets conditions when these methods return an error, so the controller should not set conditions itself.

## Accessing Snowflake Connections

The provider uses singleton managers for secrets and connection pooling. Access them in your business logic and controllers:

**Getting Connections in Resource Controllers**:
```go
// Get the pool manager instance
poolMgr, err := pool.GetInstance()  // Returns error if not initialized
if err != nil {
    return err
}

// Get appropriate connection for your operation
orgDB, _ := poolMgr.OrgAdminPool(ctx)      // Org-level operations
tenantDB, _ := poolMgr.TenantPool(ctx, accountName, namespace)  // Tenant operations

// Optional: Access secrets manager directly
secretsMgr, err := secrets.GetInstance()  // Returns error if not initialized
```

**Initialization**: The ProviderConfig controller initializes these singletons at startup:
1. `secrets.Initialize(creds, cacheTTL, logger)` - AWS Secrets Manager client with in-memory cache
2. `pool.Initialize(secretRetriever, providerConfig, logger)` - Connection pool manager

Once initialized, all controllers can safely call `GetInstance()` to access these shared resources.
## Error Handling

The project uses a standardized error handling system in `internal/errors` that distinguishes between user errors (configuration mistakes) and system errors (infrastructure failures).

### Usage in Business Logic

```go
import "github.com/crossplane/provider-snowflake/internal/errors"

// User error - configuration mistake
if !regionPattern.MatchString(region) {
    return errors.NewUser(fmt.Sprintf(
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
import "github.com/crossplane/provider-snowflake/internal/errors"

func (e *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
    result, err := e.policy.BuildTargetState(ctx, cr)
    if err != nil {
        userMsg, logMsg, logLevel, retryErr := errors.ErrorDetails(err)
        errors.LogWithLevel(e.logger, logLevel, logMsg, "resource", cr.Name)
        return managed.ExternalObservation{}, retryErr
    }
    // ... success path
}
```

### Error Classification

- **User Errors** (TypeUser): Configuration mistakes users can fix by editing their CRD
  - Logged at Debug level (only visible with --debug flag)
  - Examples: invalid region format, malformed CIDR, missing required field

- **System Errors** (TypeSystem): Infrastructure failures requiring operator intervention
  - Logged at Info level (always visible to operators)
  - Include unique 5-digit incident IDs for correlation
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

New files use `Copyright 2026 The Yukimi Authors.` Files inherited from the Crossplane provider template have both headers: the original `Copyright 2025 The Crossplane Authors.` followed by `Copyright 2026 The Yukimi Authors.`

## Development Commands

### Build & Test
```bash
make test               # Run unit tests (uses -short flag to skip integration tests)
make test-integration   # Run integration tests only (requires AWS and Snowflake access)
make reviewable         # Run full validation: generate, lint, test
```

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
- Uses `.allianz.io` domain instead of standard `.crossplane.io` for organizational ownership
- API groups: `snowflake.allianz.io` (core) and `infra.snowflake.allianz.io` (infrastructure resources)
- All APIs currently at v1alpha1 version

Use the scaffolding system instead of manual creation:
```bash
export type=MyType      # CamelCase (e.g., Database, User, Role)
make provider.addtype provider=Snowflake group=infra kind=${type}
make reviewable         # Regenerate and validate
```

After scaffolding:
1. Update `apis/snowflake.go` to register the new API group
2. Update `internal/controller/snowflake.go` to register the new controller
3. Implement the actual controller logic in the generated files

#### Generated Files (Never Edit)
All files matching `zz_generated.*` are auto-generated:
- `**/zz_generated.deepcopy.go` - Deep copy methods
- `**/zz_generated.managed.go` - Managed resource interfaces
- `**/zz_generated.managedlist.go` - Managed resource list types
- `**/zz_generated.pc.go` - ProviderConfig interfaces
- `**/zz_generated.pcu.go` - ProviderConfigUsage interfaces

#### Templates
- API scaffolding uses templates in `hack/helpers/apis/` with gomplate substitution
- Controller scaffolding uses templates in `hack/helpers/controller/`
- Templates support environment variables: `PROVIDER`, `GROUP`, `KIND`, `APIVERSION`

## Testing Strategy

### Mock Tests (Unit)
Mock tests run without external dependencies using sqlmock:
```go
func TestMyFeature_Unit(t *testing.T) {
    // 1. Create mock database
    mockDB, mock, _ := sqlmock.New()
    defer mockDB.Close()

    // 2. Set SQL expectations
    mock.ExpectExec("CREATE WAREHOUSE").WillReturnResult(sqlmock.NewResult(0, 1))

    // 3. Initialize pool manager singleton with mock
    pool.InitializeForMockTesting(mockDB)
    defer pool.ResetForTesting()

    // 4. Test business logic
    executor := execution.NewInlineExecutor(logging.NewNopLogger())
    result, err := executor.Apply(ctx, cr)

    // 5. Verify expectations
    if err := mock.ExpectationsWereMet(); err != nil {
        t.Errorf("unfulfilled expectations: %s", err)
    }
}
```

### Integration Tests
Integration tests run against real AWS Secrets Manager and Snowflake without Kubernetes.

**Requirements:**
- Test function names MUST contain "Integration" (e.g., `TestMyFeature_Integration`, `TestIntegration_FullLifecycle`)
- All integration tests MUST include the `testing.Short()` guard at the start to skip when running unit tests

```go
func TestMyFeature_Integration(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test in short mode")
    }

    _ = godotenv.Load("../../.env")

    // 1. Initialize secrets manager (reads AWS_REGION, AWS_PROFILE from env)
    secrets.InitializeForIntegrationTesting()

    // 2. Initialize pool manager (reads SNOWFLAKE_ORG, SNOWFLAKE_USE_PRIVATELINK from env)
    pool.InitializeForIntegrationTesting()

    // 3. Access Snowflake (org-level or tenant-level)
    poolMgr, _ := pool.GetInstance()
    tenantDB, _ := poolMgr.TenantPool(ctx,
        os.Getenv("SNOWFLAKE_TENANT_TEST_ACCOUNT"),
        os.Getenv("SNOWFLAKE_TENANT_TEST_NAMESPACE"))

    // Test logic validates against real AWS and Snowflake
}
```

### E2E Tests
```bash
make e2e.automated  # Fully automated with kind cluster
make e2e.manual     # Against running 'make dev' in another terminal
```

## Resources & References

### Example Manifests
Reference manifests:
- `examples/infra/snowflake-account.yaml` - SnowflakeAccount example
- `cluster/manifests/provider-config.yaml` - ProviderConfig template for production
- `cluster/local/config-dev.yaml` - ProviderConfig for local development

### General Reference Specs
- `specs/product_design.md` - Product requirements, resource schemas, and behavior specifications

