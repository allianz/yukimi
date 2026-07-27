# Specification: Secrets Handling (002)

## Overview

This specification defines the secrets handling subsystem for the Crossplane provider that manages secure retrieval and caching of JWT credentials for Snowflake authentication.

The system solves the challenge of providing high-performance credential access while maintaining security, through a backend-agnostic `SecretBackend` interface and namespace-based tenant isolation. It is needed to enable both organization-level administrative operations and tenant-specific resource provisioning with minimal latency impact.

The technical approach uses a singleton pattern with a `SecretBackend` interface, in-memory TTL-based caching, RSA key pair management, and integration with the provider's error handling system for appropriate classification of credential-related failures.

The interface is implemented once for AWS Secrets Manager in `002-a-aws-secrets-backend.md`; additional backends get their own spec when they are actually built.

## Scope

This specification defines the secrets handling subsystem that:

- Abstracts secret storage behind a `SecretBackend` interface
- Manages RSA key pairs for JWT authentication
- Caches secrets in memory with configurable TTL
- Provides namespace-based tenant isolation
- Supports both org admin and tenant credential types
- Generates and stores replacement credentials for rotation

**Out of Scope**:

- Password-based authentication (JWT-only)
- Direct Snowflake credential storage (uses secret backend only)
- Pushing rotated public keys to Snowflake (`ALTER USER ... SET RSA_PUBLIC_KEY`) — requires the connection pool (003), which does not exist yet. The caller is responsible for this step once available.
- Automatic/scheduled rotation triggers (rotation is invoked explicitly by a caller)
- Cross-provider credential sharing
- Background cache cleanup (uses lazy eviction)
- Backend-specific credential rotation policies

## Key Concept: Singleton Initialization

The secrets manager follows a singleton pattern where the manager instance is initialized once by the ProviderConfig controller and then accessed by all resource controllers.

Initialization flow:

1. Provider starts with uninitialized singleton (`instance = nil`)
2. ProviderConfig "default" creation triggers `Initialize()` with a pre-built `SecretBackend`
3. `Initialize()` creates the manager with the provided backend and cache, using `sync.Once` for thread safety
4. Resource controllers call `GetInstance()` to access the initialized manager
5. If `GetInstance()` is called before `Initialize()`, it returns an error, triggering a Crossplane retry until ProviderConfig is ready

The ProviderConfig controller is responsible for constructing the backend and passing it to `Initialize()`. The secrets package itself is backend-agnostic — it has no knowledge of which backend is in use.

**Important**: Thread safety is guaranteed using `sync.Once` for initialization (ensures exactly one initialization) and `sync.RWMutex` for instance access (optimized for read-heavy workloads with minimal contention).

## Key Concept: Secret Backend

The `SecretBackend` interface abstracts all secret storage operations behind five methods.

Each backend implementation lives in its own sub-package under `internal/secrets/backends/` and is responsible only for raw get/put/delete/pending-deletion/health operations on secret paths. All higher-level logic (caching, path construction, credential parsing, RSA key generation) lives in the manager layer and is shared across all backends.

```
ProviderConfig controller
    │
    ├── constructs backend: aws.NewBackend(...)
    └── calls secrets.Initialize(backend, ttl)
                │
                └── manager uses backend for get/put/delete
                    cache is always in front of backend
```

`IsSecretPendingDeletion()` checks whether a secret exists but is in a pending deletion state (e.g., AWS's 30-day pending deletion window). Backends that do not support soft-delete return `false`.

**Important**: Backends receive and return raw JSON bytes — they have no knowledge of credential structure. Parsing, validation, and RSA key generation all happen in the manager layer.

## Key Concept: Secret Path Format

Secret paths follow a structured format that enforces tenant isolation.

Tenant credentials use the path:

```
snowflake/tenant/{org}/{namespace}/{account}/platform-credentials
```

Namespace comes from Kubernetes metadata — never from spec.

Organization admin credentials use:

```
snowflake/org/{org}/{account}/org-admin-credentials
```

Here, `account` is the org admin account name, and there is no namespace component.

Path construction and validation happen in the manager layer and are backend-agnostic — the same paths are used regardless of which backend is configured.

**Important**: The namespace MUST come from `metadata.namespace` (Kubernetes runtime), NEVER from user-provided spec fields. Different Kubernetes namespaces represent different tenants and must have separate credentials even if they specify the same account name.

## Key Concept: Credential Structure

Credentials are stored as JSON objects with four fields:

```json
{
  "account": "platform_dev_internal",
  "username": "PLATFORM",
  "public_key": "MIIBIjANBgkqh....",
  "private_key": "-----BEGIN PRIVATE KEY-----\nMIIEvgIBADANBgkqhk.....\n-----END PRIVATE KEY-----"
}
```

- **account**: Snowflake account name
- **username**: Fixed value `PLATFORM` for all secrets
- **public_key**: Single-line base64 without PEM delimiters, used directly in `CREATE ACCOUNT` and `ALTER USER` SQL commands
- **private_key**: PKCS#8 format with PEM delimiters, used directly by the Snowflake Go driver for JWT authentication

RSA key generation uses `crypto/rand` for cryptographically secure random numbers, minimum 2048-bit key size, PKCS#8 encoding for private keys, and PKIX encoding for public keys.

**Important**: No password storage or transmission occurs. All authentication uses JWT tokens signed with the RSA private key, with the public key registered in the Snowflake user account.

## Technical Context

**Language/Version**: Go 1.24.0

**Primary Dependencies**: crypto/rand, crypto/rsa, crypto/x509, Snowflake Go driver (gosnowflake) with JWT support. Backend-specific SDKs (e.g., AWS SDK for Go v2) are dependencies of the backend implementation, not this package.

**Storage**: Backend-agnostic `SecretBackend` interface (see `002-a-aws-secrets-backend.md` for the AWS implementation), in-memory cache with TTL (performance optimization)

**Testing**: Go testing framework, integration tests with `.env` configuration, mock backend for unit tests. `ResetForTesting()` resets singleton state between tests (test-only, not thread-safe, never called in production code)

**Performance Goals**: <1μs cache hit latency, 100x+ speedup vs backend API calls (>100ms), zero allocations on cache hit path

**Constraints**: Thread-safe (`sync.RWMutex` for cache, `sync.Once` for initialization), idempotent operations, singleton pattern, Crossplane reconciliation compatible, lazy cache eviction (no background goroutines)

## Public API

### SecretBackend Interface

```go
// SecretBackend abstracts secret storage operations.
// Implementations live in internal/secrets/backends/ (one sub-package per backend, e.g. aws/).
// All methods operate on raw JSON bytes — no credential parsing.
type SecretBackend interface {
    // GetSecret retrieves raw secret bytes at the given path.
    // Returns system error if path not found, permissions denied, or backend unavailable.
    GetSecret(ctx context.Context, path string) ([]byte, error)

    // PutSecret stores raw secret bytes at the given path.
    // Creates or overwrites the secret.
    PutSecret(ctx context.Context, path string, value []byte) error

    // DeleteSecret removes the secret at the given path.
    // Soft delete where supported (e.g., AWS 30-day window).
    DeleteSecret(ctx context.Context, path string) error

    // IsSecretPendingDeletion checks if a secret exists but is pending deletion.
    // Used by GenerateAndStore() to detect and surface the conflict as a system error.
    // Returns false for backends that do not support soft-delete.
    IsSecretPendingDeletion(ctx context.Context, path string) (bool, error)

    // HealthCheck verifies backend connectivity and credentials.
    // Returns system error if credentials invalid, permissions denied, or backend unavailable.
    HealthCheck(ctx context.Context) error
}
```

Backend constructors (e.g., `backends/aws.NewBackend()`) and their credential types are defined in each backend's own spec (see `002-a-aws-secrets-backend.md` for AWS).

Credentials are resolved by the ProviderConfig controller using Crossplane's standard `xpv1.CommonCredentialSelectors` mechanism (`ProviderConfig.Spec.Credentials`) — this package never defines its own Kubernetes Secret reference type.

### Singleton Functions

```go
// Initialize sets up the secrets manager singleton with a pre-built backend.
// Called once by ProviderConfig controller during startup.
// Thread-safe using sync.Once.
//
// The secrets package is business logic — it never logs. It only returns errors;
// the calling controller creates its own scoped Logger (see 001-error-and-logging.md)
// and calls Handle() on whatever error propagates up.
//
// Parameters:
//   - backend: Pre-constructed SecretBackend implementation
//   - cacheTTL: Time-to-live for cached credentials (typically 5 minutes)
//
// Returns:
//   - error: Initialization failure
func Initialize(backend SecretBackend, cacheTTL time.Duration) error

// GetInstance returns the initialized secrets manager singleton.
// Returns error if Initialize() has not been called.
// Triggers Crossplane retry until ProviderConfig is ready.
//
// Returns:
//   - SecretManager: Initialized manager instance
//   - error: "secrets manager not initialized - waiting for ProviderConfig 'default'" if not initialized
func GetInstance() (SecretManager, error)

// InitializeForIntegrationTesting is implemented per-backend, since it requires a
// concrete backend and real credentials. See the backend's own spec
// (e.g., 002-a-aws-secrets-backend.md) for its signature and required environment variables.

// InitializeForMockTesting initializes with a mock backend.
// For unit tests that don't need real backend dependencies.
//
// Parameters:
//   - backend: Mock SecretBackend implementation
//   - cacheTTL: Time-to-live for cached credentials
func InitializeForMockTesting(backend SecretBackend, cacheTTL time.Duration)
```

### SecretManager Interface

```go
type SecretManager interface {
    // GetOrgAdminCredentials retrieves organization-level admin credentials.
    // Used for account creation, org policies, and cross-account operations.
    // Path: snowflake/org/{org}/{account}/org-admin-credentials
    //
    // Returns:
    //   - *OrgAdminCredentials: Credentials with account, username, public key, private key
    //   - System error if secret not found, backend permissions denied, backend unavailable, or credential parsing fails
    GetOrgAdminCredentials(ctx context.Context, orgName, account string) (*OrgAdminCredentials, error)

    // GetTenantCredentials retrieves namespace-specific tenant credentials.
    // Path: snowflake/tenant/{org}/{namespace}/{account}/platform-credentials
    //
    // Returns:
    //   - *PlatformCredentials: Credentials with account, username, public key, private key
    //   - System error if secret not found, empty parameters, backend permissions denied, backend unavailable, or credential parsing fails
    GetTenantCredentials(ctx context.Context, orgName, namespace, account string) (*PlatformCredentials, error)

    // GenerateAndStore creates new RSA key pair and stores via backend.
    // If the secret path is pending deletion, returns a system error — never cancels the deletion silently.
    // The operator must cancel the pending deletion in the backend before retrying.
    //
    // Returns:
    //   - *PlatformCredentials: Generated credentials
    //   - User error if spec.org or spec.account is empty
    //   - System error if secret is pending deletion, key generation fails, backend permissions denied, or backend unavailable
    GenerateAndStore(ctx context.Context, orgName, namespace, account string) (*PlatformCredentials, error)

    // DeleteTenantSecret removes tenant credentials via backend.
    // Invalidates cache entry.
    DeleteTenantSecret(ctx context.Context, orgName, namespace, account string) error

    // RotateTenantCredentials generates a new RSA key pair and overwrites the
    // existing tenant secret in the backend, then invalidates the cache entry.
    // The caller is responsible for pushing the new public key to Snowflake
    // (ALTER USER ... SET RSA_PUBLIC_KEY) before or after this call — this method
    // only replaces the stored secret. Any live connection still using the old
    // private key will fail once the Snowflake-side key is updated; the caller
    // must coordinate that update with reconnection.
    //
    // Returns:
    //   - *PlatformCredentials: Newly generated credentials
    //   - User error if spec.org or spec.account is empty
    //   - System error if secret is pending deletion, key generation fails, backend permissions denied, or backend unavailable
    RotateTenantCredentials(ctx context.Context, orgName, namespace, account string) (*PlatformCredentials, error)

    // RotateOrgAdminCredentials generates a new RSA key pair and overwrites the
    // existing org admin secret in the backend, then invalidates the cache entry.
    // Same caller responsibility as RotateTenantCredentials: pushing the new
    // public key to Snowflake is not handled by this package.
    //
    // Returns:
    //   - *OrgAdminCredentials: Newly generated credentials
    //   - System error if secret is pending deletion, key generation fails, backend permissions denied, or backend unavailable
    RotateOrgAdminCredentials(ctx context.Context, orgName, account string) (*OrgAdminCredentials, error)

    // InvalidateTenantCache forces cache refresh on next GetTenantCredentials call.
    InvalidateTenantCache(orgName, namespace, account string)

    // InvalidateOrgAdminCache forces cache refresh on next GetOrgAdminCredentials call.
    InvalidateOrgAdminCache(orgName, account string)

    // ClearCache removes all cached credentials.
    ClearCache()

    // HealthCheck verifies backend connectivity.
    HealthCheck(ctx context.Context) error
}
```

### Credential Types

```go
// PlatformCredentials represents tenant-level credentials.
type PlatformCredentials struct {
    Account    string `json:"account"`     // Snowflake account name
    Username   string `json:"username"`    // Fixed value: "PLATFORM"
    PublicKey  string `json:"public_key"`  // Single-line base64, no PEM delimiters
    PrivateKey string `json:"private_key"` // PKCS#8 format with PEM delimiters
}

// OrgAdminCredentials represents organization-level admin credentials.
type OrgAdminCredentials struct {
    Account    string `json:"account"`     // Organization account name
    Username   string `json:"username"`    // Org admin username
    PublicKey  string `json:"public_key"`  // Single-line base64, no PEM delimiters
    PrivateKey string `json:"private_key"` // PKCS#8 format with PEM delimiters
}
```

## Project Structure

```text
internal/secrets/
├── manager.go           # SecretManager interface, implementation, singleton
├── backend.go           # SecretBackend interface definition
├── credentials.go       # PlatformCredentials and OrgAdminCredentials value objects
├── cache.go             # cachedSecret type + TTL cache with lazy eviction
├── paths.go             # secretPath types + path construction and validation
├── keygen.go            # rsaKeyPair type + RSA key generation (2048-bit minimum)
├── doc.go               # Package documentation
├── manager_test.go      # Manager and singleton unit tests (uses mock backend)
├── cache_test.go        # Cache unit tests (TTL, eviction, thread safety)
├── paths_test.go        # Path validation unit tests
└── keygen_test.go       # Key generation unit tests

# Backend implementations live under internal/secrets/backends/, one sub-package
# per backend. See each backend's own spec (e.g., 002-a-aws-secrets-backend.md)
# for its Project Structure.
```

## Error Classification

**User Errors** (use `errors.NewUserError()`):

- Missing required fields: empty `spec.org` or `spec.account` in CRD

The end user creating a `SnowflakeAccount` never touches backend credentials, secrets, or ProviderConfig — those are operator/platform concerns configured separately. The only failure a user can fix by editing their own CRD is an empty required field.

**System Errors** (use `fmt.Errorf("context: %w", err)`):

- Secret not found (operator must verify the secret was created for this account)
- Secret pending deletion (operator must cancel the deletion in the backend)
- Invalid backend credentials (`InvalidClientTokenId`, `ExpiredToken`, etc.)
- Missing permissions (`AccessDenied`, `Forbidden`, etc.)
- Invalid backend configuration
- Backend service unavailable
- Network failures
- Credential parsing failures (malformed JSON)
- Cryptographic errors during key generation

## Edge Cases

- **What happens if GetInstance() is called before Initialize()?**
  Returns error "secrets manager not initialized - waiting for ProviderConfig 'default'", triggers Crossplane retry until ProviderConfig is ready.

- **How does GenerateAndStore handle secrets pending deletion?**
  Returns a system error: "Secret for account '{account}' in namespace '{namespace}' is pending deletion. The operator must cancel the pending deletion in the backend before this secret can be regenerated." Never restores or cancels the deletion silently — the operator decides whether to cancel it or let the deletion complete.

- **What happens when cache entry expires during retrieval?**
  Lazy eviction removes the expired entry on access, fetches fresh from backend, and stores in cache with a new expiration time.

- **Can multiple namespaces share the same tenant credentials?**
  No, namespace isolation ensures different secret paths even with the same org/account (e.g., `azdonedia/team-a/shared` vs `azdonedia/team-b/shared`).

- **What if RSA key generation fails?**
  Returns a system error, does not store partial credentials, and allows Crossplane to retry.

- **How are concurrent cache invalidations handled?**
  A write lock ensures thread-safe deletion; multiple invalidations are idempotent.

- **What happens if the backend is temporarily unavailable?**
  Returns a system error with an incident ID, Crossplane retries with exponential backoff, and the cache preserves existing valid entries until TTL expires.

- **Can the backend be swapped at runtime?**
  No. `Initialize()` uses `sync.Once` — the backend is fixed for the lifetime of the provider process. A backend change (e.g., new credentials, different configuration) requires a provider pod restart (`kubectl rollout restart deployment/provider-snowflake`), which resets `sync.Once` and triggers a new ProviderConfig reconciliation.

- **What happens to connections still using the old key during rotation?**
  `RotateTenantCredentials`/`RotateOrgAdminCredentials` overwrite the stored secret immediately — there is no dual-key transition period. Any live Snowflake connection using the old private key keeps working until Snowflake's user record is updated with the new public key; once the caller performs that `ALTER USER` update, the old key stops authenticating immediately. The caller must coordinate updating Snowflake and re-establishing connections to avoid an availability gap.

## Dependencies

- **Error Handling and Logging (001)** - Used APIs: `NewUserError()` - Contract: Must be available before secrets package is initialized

## Integration Points

- **ProviderConfig Controller** - Constructs a backend and calls Initialize() - Key functions: `Initialize()` - Notes: Backend construction is defined in the backend's own spec (see `002-a-aws-secrets-backend.md`)
- **SnowflakeAccount Controller** - Generates and retrieves tenant credentials - Key functions: `GenerateAndStore()`, `GetTenantCredentials()`, `InvalidateTenantCache()` - Notes: Namespace from cr.Namespace, public key used in CREATE ACCOUNT SQL
- **SnowflakeExecution Controller** - Retrieves tenant credentials for SQL execution - Key functions: `GetTenantCredentials()` - Notes: Credentials used for SQL execution in target account
- **Connection Pool (003)** - Consumes credentials for Snowflake connection creation - Key functions: Uses PlatformCredentials and OrgAdminCredentials types - Notes: Private key passed to Snowflake driver for JWT authentication
- **Rotation Caller (future, requires 003)** - Pushes the new public key to Snowflake after rotation - Key functions: `RotateTenantCredentials()`, `RotateOrgAdminCredentials()` - Notes: Must call `ALTER USER ... SET RSA_PUBLIC_KEY` using the connection pool (003) once it exists; this package only replaces the stored secret

## Success Criteria

- **SC-001**: SecretBackend interface with GetSecret, PutSecret, DeleteSecret, IsSecretPendingDeletion, HealthCheck
- **SC-002**: SecretManager interface fully implemented with all methods
- **SC-003**: Singleton pattern with thread-safe initialization using sync.Once
- **SC-004**: GetInstance() returns error before Initialize() called
- **SC-005**: TTL-based in-memory caching with lazy eviction on access
- **SC-006**: Cache hits complete in <1μs (100x+ faster than backend API calls)
- **SC-007**: Zero allocations on cache hit path
- **SC-008**: Secret path construction validates all components non-empty
- **SC-009**: Tenant paths include namespace: `snowflake/tenant/{org}/{namespace}/{account}/platform-credentials`
- **SC-010**: Org admin paths exclude namespace: `snowflake/org/{org}/{account}/org-admin-credentials`
- **SC-011**: RSA key generation uses 2048-bit minimum key size
- **SC-012**: Public keys stored as single-line base64 without PEM delimiters
- **SC-013**: Private keys stored in PKCS#8 format with PEM delimiters
- **SC-014**: GenerateAndStore() returns a system error when secret is pending deletion — never cancels the deletion silently
- **SC-015**: User error classification for missing required fields (empty spec.org or spec.account)
- **SC-016**: System error classification for secret not found, invalid credentials, permission denied, service unavailable, network failures, parsing errors
- **SC-017**: HealthCheck() delegates to backend HealthCheck()
- **SC-018**: Mock testing support via InitializeForMockTesting() with mock SecretBackend
- **SC-019**: Test coverage ≥ 95%
- **SC-020**: Zero race conditions (go test -race passes)
- **SC-021**: RotateTenantCredentials()/RotateOrgAdminCredentials() generate a new RSA key pair, overwrite the stored secret, and invalidate the corresponding cache entry
- **SC-022**: Rotation methods never call Snowflake — pushing the new public key via `ALTER USER` is the caller's responsibility

## Security Considerations

- **Namespace Isolation**: Namespace MUST come from `metadata.namespace` (Kubernetes runtime), NEVER from user-provided spec fields. Different Kubernetes namespaces represent different tenants and must have separate credentials even if they specify the same account name. Example: `team-a/azdonedia/shared-account` uses different credentials than `team-b/azdonedia/shared-account`.

- **Backend Credential Resolution**: Backend credentials are resolved via `ProviderConfig.Spec.Credentials`, using Crossplane's standard `xpv1.CommonCredentialSelectors` mechanism:
  - `Source: Secret` reads a Kubernetes Secret via RBAC-protected `SecretRef`
  - `Source: InjectedIdentity` uses the pod's identity (e.g., AWS IRSA), with no Secret involved

  This package never defines its own credential-reference type — see the backend's own spec (e.g., `002-a-aws-secrets-backend.md`) for how credentials are extracted and passed to its `NewBackend()`.

- **In-Memory Cache**: The cache holds plaintext RSA private keys in Go heap memory. This is acceptable because Kubernetes pod isolation prevents other pods and containers from accessing process memory. A node-level compromise would expose cache contents — this is an accepted risk in Kubernetes security models and applies equally to all Kubernetes secrets. The cache is never written to disk or persisted in etcd.

- **No Credential Logging**: Credentials (private keys, public keys, backend tokens) must never appear in log messages, error messages, or Kubernetes status conditions. Error messages must reference incident IDs only. This applies to both the secrets package and all callers.

- **JWT Authentication**: No passwords stored or transmitted. JWT tokens are signed with the RSA private key, and the public key is registered in the Snowflake user account. The private key never leaves the in-memory cache.

- **Key Generation**: RSA key generation uses `crypto/rand` (cryptographically secure). Minimum 2048-bit key size enforced. PKCS#8 encoding for private keys, PKIX encoding for public keys.

- **Credential Validation After Parsing**: After fetching from the backend and parsing JSON into `PlatformCredentials` or `OrgAdminCredentials`, the SecretManager must validate that all four fields (`account`, `username`, `public_key`, `private_key`) are non-empty before caching or returning. A tampered or malformed secret in the backend must fail fast rather than be cached and served to controllers.

- **Secret Name Collision**: A secret name is a flat string key, not a filesystem path — there is no directory to traverse. But an unvalidated `namespace`, `org`, or `account` value containing `/` could still construct a string that exactly matches a different tenant's secret name, breaking tenant isolation. Path components are validated against an allowlist (alphanumeric, hyphens, underscores only) in `paths.go`, returning a system error for invalid components.

- **Cache Invalidation After Rotation**: `RotateTenantCredentials()` and `RotateOrgAdminCredentials()` invalidate the corresponding cache entry automatically after overwriting the secret, so the next retrieval always fetches the new credentials rather than serving a stale cached value for the remainder of the TTL. If a secret is rotated directly in the backend (bypassing these methods), operators must call `InvalidateTenantCache()` or `InvalidateOrgAdminCache()` manually — this is a security procedure, not just a performance concern.

- **Log Injection via Credential Fields**: The `account` and `username` fields parsed from backend secrets are user-controlled values. If they contain newlines, tab characters, or JSON control characters and are used directly in structured log output, they can corrupt log entries or inject fake log lines. Credential fields must be sanitized (strip control characters) before use in any log message or error string.

## Performance Considerations

- **Cache Hit Performance**: <1μs latency for cache hits vs >100ms for backend API calls (100x+ speedup)
- **Lock Contention**: Read lock for cache hits (minimal contention), write lock only for insertions/eviction
- **Allocation Efficiency**: Zero allocations on cache hit path
- **Lazy Eviction**: No background goroutines for cache cleanup (eviction on access only)
- **TTL Configuration**: Configurable via ProviderConfig.spec.cacheTTL (default 5 minutes)
- **Manual Invalidation**: Explicit InvalidateTenantCache() and InvalidateOrgAdminCache() for forced refresh after credential updates

## References

- **Error Handling and Logging Specification**: `specs/001-error-and-logging.md` - Error classification and logging patterns
- **Internal Errors Package**: `internal/errors/` - NewUserError(), IsUserError() API
- **Secrets Manager Package**: `internal/secrets/` - Complete implementation
- **AWS Backend Specification**: `specs/002-a-aws-secrets-backend.md` - AWS implementation and integration tests



================

## Appendix: Usage Examples

### Example 1: Initialize with a Backend (ProviderConfig Controller)

```go
import (
    "context"
    "time"
    "github.com/allianz/yukimi/internal/secrets"
)

func (c *ProviderConfigController) Setup(ctx context.Context, cfg *v1alpha1.ProviderConfig) error {
    // Backend construction is defined in the backend's own spec.
    // See 002-a-aws-secrets-backend.md for how the AWS backend is built
    // from cfg.Spec.Credentials (Crossplane's standard credential mechanism).
    backend, err := buildBackend(ctx, cfg)
    if err != nil {
        return fmt.Errorf("failed to create secrets backend: %w", err)
    }

    // Initialize secrets manager with the backend
    if err := secrets.Initialize(backend, 5*time.Minute); err != nil {
        return fmt.Errorf("failed to initialize secrets manager: %w", err)
    }

    return nil
}
```

### Example 2: Retrieve Tenant Credentials (Resource Controller)

```go
import (
    "context"
    "github.com/allianz/yukimi/internal/secrets"
)

func (e *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
    cr := mg.(*v1alpha1.SnowflakeAccount)

    mgr, err := secrets.GetInstance()
    if err != nil {
        // Not initialized yet — Crossplane will retry
        return managed.ExternalObservation{}, err
    }

    // Namespace comes from cr.Namespace (metadata), never from spec
    creds, err := mgr.GetTenantCredentials(ctx, cr.Spec.Org, cr.Namespace, cr.Spec.Account)
    if err != nil {
        return managed.ExternalObservation{}, err
    }

    // Use credentials for Snowflake connection
    conn, err := snowflake.Connect(creds.Account, creds.Username, creds.PrivateKey)
    // ... use connection
}
```

### Example 3: Generate and Store New Credentials (Account Creation)

```go
import (
    "context"
    "github.com/allianz/yukimi/internal/secrets"
)

func (e *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
    cr := mg.(*v1alpha1.SnowflakeAccount)

    mgr, err := secrets.GetInstance()
    if err != nil {
        return managed.ExternalCreation{}, err
    }

    // Generates RSA key pair, stores via backend — returns system error if secret is pending deletion
    creds, err := mgr.GenerateAndStore(ctx, cr.Spec.Org, cr.Namespace, cr.Spec.Account)
    if err != nil {
        return managed.ExternalCreation{}, err
    }

    sql := fmt.Sprintf(`
        CREATE ACCOUNT %s
        ADMIN_NAME = '%s'
        ADMIN_RSA_PUBLIC_KEY = '%s'
        EDITION = ENTERPRISE
    `, creds.Account, creds.Username, creds.PublicKey)

    if err := e.snowflake.Execute(ctx, sql); err != nil {
        return managed.ExternalCreation{}, fmt.Errorf("failed to create account: %w", err)
    }

    return managed.ExternalCreation{}, nil
}
```

### Example 4: Rotate Tenant Credentials (Future — requires Connection Pool 003)

```go
import (
    "context"
    "github.com/allianz/yukimi/internal/secrets"
)

// Illustrative only — the pool dependency (003) does not exist yet.
// This shows the caller's responsibility once it does: the secrets package
// only replaces the stored secret, it never touches Snowflake.
func (e *external) RotateCredentials(ctx context.Context, mg resource.Managed) error {
    cr := mg.(*v1alpha1.SnowflakeAccount)

    mgr, err := secrets.GetInstance()
    if err != nil {
        return err
    }

    // Generates new key pair, overwrites secret, invalidates cache.
    // Old key still works until the ALTER USER below runs.
    newCreds, err := mgr.RotateTenantCredentials(ctx, cr.Spec.Org, cr.Namespace, cr.Spec.Account)
    if err != nil {
        return err
    }

    // Caller pushes the new public key to Snowflake — old key stops working now.
    sql := fmt.Sprintf(`ALTER USER %s SET RSA_PUBLIC_KEY = '%s'`, newCreds.Username, newCreds.PublicKey)
    if err := e.snowflake.Execute(ctx, sql); err != nil {
        return fmt.Errorf("failed to apply rotated key to Snowflake: %w", err)
    }

    return nil
}
```

### Example 5: Mock Backend for Unit Tests

```go
import (
    "context"
    "testing"
    "github.com/allianz/yukimi/internal/secrets"
)

type mockBackend struct {
    secrets map[string][]byte
}

func (m *mockBackend) GetSecret(_ context.Context, path string) ([]byte, error) {
    v, ok := m.secrets[path]
    if !ok {
        return nil, fmt.Errorf("secret not found: %s", path)
    }
    return v, nil
}

func (m *mockBackend) PutSecret(_ context.Context, path string, value []byte) error {
    m.secrets[path] = value
    return nil
}

func (m *mockBackend) DeleteSecret(_ context.Context, path string) error {
    delete(m.secrets, path)
    return nil
}

func (m *mockBackend) IsSecretPendingDeletion(_ context.Context, path string) (bool, error) {
    return false, nil
}
func (m *mockBackend) HealthCheck(_ context.Context) error { return nil }

func TestGetTenantCredentials_Unit(t *testing.T) {
    mock := &mockBackend{secrets: make(map[string][]byte)}
    secrets.InitializeForMockTesting(mock, 5*time.Minute)
    defer secrets.ResetForTesting()

    // Pre-populate with test credentials
    mock.secrets["snowflake/tenant/myorg/team-a/myaccount/platform-credentials"] = []byte(`{
        "account": "myaccount",
        "username": "PLATFORM",
        "public_key": "MIIBIjAN...",
        "private_key": "-----BEGIN PRIVATE KEY-----\n..."
    }`)

    mgr, _ := secrets.GetInstance()
    creds, err := mgr.GetTenantCredentials(context.Background(), "myorg", "team-a", "myaccount")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if creds.Username != "PLATFORM" {
        t.Errorf("expected PLATFORM, got %s", creds.Username)
    }
}
```
