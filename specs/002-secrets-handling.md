# Specification: Secrets Handling (002)

## Overview

This specification defines the secrets handling subsystem for the Crossplane provider that manages secure retrieval and caching of JWT credentials for Snowflake authentication. The system solves the challenge of providing high-performance credential access while maintaining security through a pluggable secret backend (AWS Secrets Manager, Azure Key Vault, HashiCorp Vault, GCP Secret Manager) and namespace-based tenant isolation. It is needed to enable both organization-level administrative operations and tenant-specific resource provisioning with minimal latency impact across different cloud environments. The technical approach uses a singleton pattern with a backend-agnostic `SecretBackend` interface, in-memory TTL-based caching, RSA key pair management, and integration with the provider's error handling system for appropriate classification of credential-related failures.

## Scope

This specification defines the secrets handling subsystem that:
- Abstracts secret storage behind a pluggable `SecretBackend` interface
- Supports AWS Secrets Manager, Azure Key Vault, HashiCorp Vault, and GCP Secret Manager as backends
- Manages RSA key pairs for JWT authentication
- Caches secrets in memory with configurable TTL
- Provides namespace-based tenant isolation
- Supports both org admin and tenant credential types

**Out of Scope**:
- Password-based authentication (JWT-only)
- Direct Snowflake credential storage (uses secret backend only)
- Secret rotation automation (manual via InvalidateCache)
- Cross-provider credential sharing
- Background cache cleanup (uses lazy eviction)
- Backend-specific credential rotation policies

## Key Concept: Singleton Initialization

The secrets manager follows a singleton pattern where the manager instance is initialized once by the ProviderConfig controller and then accessed by all resource controllers. Initialization flow: (1) Provider starts with uninitialized singleton (instance = nil), (2) ProviderConfig "default" creation triggers Initialize() call with a pre-built SecretBackend, (3) Initialize() creates manager with the provided backend and cache using sync.Once for thread safety, (4) Resource controllers call GetInstance() to access the initialized manager, (5) If GetInstance() called before Initialize(), returns error triggering Crossplane retry until ProviderConfig is ready.

The ProviderConfig controller is responsible for selecting and constructing the correct backend based on `spec.secretsBackend.type`, then passing it to `Initialize()`. The secrets package itself is backend-agnostic — it has no knowledge of which backend is in use.

**Important**: Thread safety is guaranteed using sync.Once for initialization (ensures exactly one initialization) and sync.RWMutex for instance access (optimized for read-heavy workloads with minimal contention).

## Key Concept: Secret Backend

The `SecretBackend` interface abstracts all secret storage operations behind five methods. Each backend implementation lives in its own sub-package under `internal/secrets/backends/` and is responsible only for raw get/put/delete/restore/health operations on secret paths. All higher-level logic (caching, path construction, credential parsing, RSA key generation) lives in the manager layer and is shared across all backends.

```
ProviderConfig controller
    │
    ├── reads spec.secretsBackend.type
    ├── constructs backend: aws.NewBackend() / azure.NewBackend() / vault.NewBackend() / gcp.NewBackend()
    └── calls secrets.Initialize(backend, ttl, logger)
                │
                └── manager uses backend for get/put/delete
                    cache is always in front of backend
```

`IsSecretPendingDeletion()` checks whether a secret exists but is in a pending deletion state (e.g., AWS 30-day pending deletion window). Backends that do not support soft-delete return false.

**Important**: Backends receive and return raw JSON bytes — they have no knowledge of credential structure. Parsing, validation, and RSA key generation all happen in the manager layer.

## Key Concept: Secret Path Format

Secret paths follow a structured format that enforces tenant isolation. Tenant credentials use the path `snowflake/tenant/{namespace}/{org}/{account}/platform-credentials` where namespace comes from Kubernetes metadata (NEVER from spec). Organization admin credentials use `snowflake/org/{org}/org-admin-credentials` without namespace component. Path construction and validation happen in the manager layer and are backend-agnostic — the same paths are used regardless of whether the backend is AWS, Azure, or Vault.

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
- **private_key**: PKCS#8 format with PEM delimiters, used directly by Snowflake Go driver for JWT authentication

RSA key generation uses `crypto/rand` for cryptographically secure random numbers, minimum 2048-bit key size, PKCS#8 encoding for private keys, and PKIX encoding for public keys.

**Important**: No password storage or transmission occurs. All authentication uses JWT tokens signed with the RSA private key, with the public key registered in the Snowflake user account.

## Technical Context

**Language/Version**: Go 1.24.0
**Primary Dependencies**: AWS SDK for Go v2 (Secrets Manager), Azure SDK for Go (Key Vault), HashiCorp Vault SDK, GCP Secret Manager SDK, crypto/rand, crypto/rsa, crypto/x509, Snowflake Go driver (gosnowflake) with JWT support
**Storage**: Pluggable secret backend (AWS/Azure/Vault/GCP), in-memory cache with TTL (performance optimization)
**Testing**: Go testing framework, integration tests with .env configuration, mock backend for unit tests. `ResetForTesting()` resets singleton state between tests (test-only, not thread-safe, never called in production code)
**Performance Goals**: <1μs cache hit latency, 100x+ speedup vs backend API calls (>100ms), zero allocations on cache hit path
**Constraints**: Thread-safe (sync.RWMutex for cache, sync.Once for initialization), idempotent operations, singleton pattern, Crossplane reconciliation compatible, lazy cache eviction (no background goroutines)

## Public API

### SecretBackend Interface

```go
// SecretBackend abstracts secret storage operations.
// Implementations live in internal/secrets/backends/{aws,azure,vault,gcp}/.
// All methods operate on raw JSON bytes — no credential parsing.
type SecretBackend interface {
    // GetSecret retrieves raw secret bytes at the given path.
    // Returns user error if path not found or permissions denied.
    // Returns system error if backend unavailable.
    GetSecret(ctx context.Context, path string) ([]byte, error)

    // PutSecret stores raw secret bytes at the given path.
    // Creates or overwrites the secret.
    PutSecret(ctx context.Context, path string, value []byte) error

    // DeleteSecret removes the secret at the given path.
    // Soft delete where supported (e.g., AWS 30-day window).
    DeleteSecret(ctx context.Context, path string) error

    // IsSecretPendingDeletion checks if a secret exists but is pending deletion.
    // Used by GenerateAndStore() to detect and surface the conflict as a user error.
    // Returns false for backends that do not support soft-delete.
    IsSecretPendingDeletion(ctx context.Context, path string) (bool, error)

    // HealthCheck verifies backend connectivity and credentials.
    // Returns user error if credentials invalid or permissions denied.
    // Returns system error if backend unavailable.
    HealthCheck(ctx context.Context) error
}
```

### Backend Constructor Functions

```go
// SecretReference points to a Kubernetes Secret containing backend credentials.
// The ProviderConfig controller reads the referenced Secret and passes the raw
// credential value to NewBackend(). NewBackend() never touches Kubernetes directly.
type SecretReference struct {
    Name      string // Kubernetes Secret name
    Namespace string // Kubernetes Secret namespace
    Key       string // Key within the Secret's data map
}

// In internal/secrets/backends/aws/
// NewBackend creates an AWS Secrets Manager backend.
func NewBackend(creds *AWSCredentials) (secrets.SecretBackend, error)

type AWSCredentials struct {
    Source    string // "Secret" or "InjectedIdentity"
    Region    string // AWS region (e.g., "eu-central-1")
    // For "Secret" source — references a Kubernetes Secret containing AccessKeyID + SecretAccessKey
    // For "InjectedIdentity" — leave nil, uses AWS IRSA (pod web identity token automatically)
    CredentialsSecretRef *SecretReference
}

// In internal/secrets/backends/azure/
// NewBackend creates an Azure Key Vault backend.
func NewBackend(creds *AzureCredentials) (secrets.SecretBackend, error)

type AzureCredentials struct {
    VaultURL string // e.g., "https://my-vault.vault.azure.net"
    // References a Kubernetes Secret containing clientId + clientSecret
    // For managed identity (AKS pod identity) — leave nil
    CredentialsSecretRef *SecretReference
}

// In internal/secrets/backends/vault/
// NewBackend creates a HashiCorp Vault backend.
func NewBackend(creds *VaultCredentials) (secrets.SecretBackend, error)

type VaultCredentials struct {
    Address   string // e.g., "https://vault.example.com"
    MountPath string // KV mount path (e.g., "secret")
    // References a Kubernetes Secret containing the Vault token
    // For Kubernetes auth method — leave nil, uses pod service account JWT automatically
    CredentialsSecretRef *SecretReference
}

// In internal/secrets/backends/gcp/
// NewBackend creates a GCP Secret Manager backend.
func NewBackend(creds *GCPCredentials) (secrets.SecretBackend, error)

type GCPCredentials struct {
    ProjectID string // GCP project ID (e.g., "my-project-123")
    // References a Kubernetes Secret containing service account JSON
    // For Workload Identity (GKE pod identity) — leave nil
    CredentialsSecretRef *SecretReference
}
```

### Singleton Functions

```go
// Initialize sets up the secrets manager singleton with a pre-built backend.
// Called once by ProviderConfig controller during startup.
// Thread-safe using sync.Once.
//
// Parameters:
//   - backend: Pre-constructed SecretBackend (aws, azure, vault, or gcp)
//   - cacheTTL: Time-to-live for cached credentials (typically 5 minutes)
//   - logger: Crossplane logging.Logger for structured logging
//
// Returns:
//   - error: Initialization failure
func Initialize(backend SecretBackend, cacheTTL time.Duration, logger logging.Logger) error

// GetInstance returns the initialized secrets manager singleton.
// Returns error if Initialize() has not been called.
// Triggers Crossplane retry until ProviderConfig is ready.
//
// Returns:
//   - SecretManager: Initialized manager instance
//   - error: "secrets manager not initialized - waiting for ProviderConfig 'default'" if not initialized
func GetInstance() (SecretManager, error)

// InitializeForIntegrationTesting initializes with a real backend from environment variables.
// Selects backend based on SECRET_BACKEND env var (defaults to "aws").
// Cache TTL is hardcoded to 30 seconds. Panics if config loading fails.
//
// Environment variables:
//   - SECRET_BACKEND: Backend type ("aws", "azure", "vault", "gcp") — defaults to "aws"
//
// AWS (SECRET_BACKEND=aws):
//   - AWS_REGION: AWS region (e.g., "eu-central-1") — required
//   - AWS_PROFILE: AWS named profile (optional)
//
// Azure (SECRET_BACKEND=azure):
//   - AZURE_VAULT_URL: Azure Key Vault URL — required
//   - AZURE_TENANT_ID: Azure tenant ID — required
//   - AZURE_CLIENT_ID: Service principal client ID — required
//   - AZURE_CLIENT_SECRET: Service principal secret — required
//
// Vault (SECRET_BACKEND=vault):
//   - VAULT_ADDR: Vault server address — required
//   - VAULT_TOKEN: Vault token — required
//   - VAULT_MOUNT_PATH: KV mount path — defaults to "secret"
//
// GCP (SECRET_BACKEND=gcp):
//   - GCP_PROJECT_ID: GCP project ID — required
//   - GOOGLE_APPLICATION_CREDENTIALS: Path to service account JSON (optional, uses ADC if empty)
func InitializeForIntegrationTesting()

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
    // Path: snowflake/org/{org}/org-admin-credentials
    //
    // Returns:
    //   - *OrgAdminCredentials: Credentials with account, username, public key, private key
    //   - User error if secret not found or backend permissions denied
    //   - System error if backend unavailable or credential parsing fails
    GetOrgAdminCredentials(ctx context.Context, orgName string) (*OrgAdminCredentials, error)

    // GetTenantCredentials retrieves namespace-specific tenant credentials.
    // Path: snowflake/tenant/{namespace}/{org}/{account}/platform-credentials
    //
    // Returns:
    //   - *PlatformCredentials: Credentials with account, username, public key, private key
    //   - User error if secret not found or backend permissions denied
    //   - System error if empty parameters, backend unavailable, or credential parsing fails
    GetTenantCredentials(ctx context.Context, namespace, orgName, account string) (*PlatformCredentials, error)

    // GenerateAndStore creates new RSA key pair and stores via backend.
    // If the secret path is pending deletion, returns a user error — never restores silently.
    // The user must manually restore or wait for deletion to complete before retrying.
    //
    // Returns:
    //   - *PlatformCredentials: Generated credentials
    //   - User error if secret is pending deletion, or backend permissions denied
    //   - User error if spec.org or spec.account is empty
    //   - System error if key generation fails or backend unavailable
    GenerateAndStore(ctx context.Context, namespace, orgName, account string) (*PlatformCredentials, error)

    // DeleteTenantSecret removes tenant credentials via backend.
    // Invalidates cache entry.
    DeleteTenantSecret(ctx context.Context, namespace, orgName, account string) error

    // InvalidateTenantCache forces cache refresh on next GetTenantCredentials call.
    InvalidateTenantCache(namespace, orgName, account string)

    // InvalidateOrgAdminCache forces cache refresh on next GetOrgAdminCredentials call.
    InvalidateOrgAdminCache(orgName string)

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
├── backends/
│   ├── aws/
│   │   ├── backend.go       # AWS Secrets Manager implementation
│   │   └── backend_test.go  # Integration tests (require real AWS credentials)
│   ├── azure/
│   │   ├── backend.go       # Azure Key Vault implementation
│   │   └── backend_test.go  # Integration tests (require real Azure credentials)
│   ├── vault/
│   │   ├── backend.go       # HashiCorp Vault implementation
│   │   └── backend_test.go  # Integration tests (require real Vault credentials)
│   └── gcp/
│       ├── backend.go       # GCP Secret Manager implementation
│       └── backend_test.go  # Integration tests (require real GCP credentials)
├── manager_test.go      # Manager and singleton unit tests (uses mock backend)
├── cache_test.go        # Cache unit tests (TTL, eviction, thread safety)
├── paths_test.go        # Path validation unit tests
├── keygen_test.go       # Key generation unit tests
└── integration_test.go  # Integration tests with real backend (selected via SECRET_BACKEND env var)
```

## Error Classification

**User Errors** (use `errors.NewUser()`):
- Secret not found (user must create SnowflakeAccount first)
- Secret pending deletion (user must restore manually or wait for deletion to complete)
- Invalid backend credentials (`InvalidClientTokenId`, `ExpiredToken`, etc.)
- Missing permissions (`AccessDenied`, `Forbidden`, etc.)
- Invalid backend configuration (region, vault URL, etc.)
- Missing required fields: empty `spec.org` or `spec.account` in CRD

**System Errors** (use `fmt.Errorf("context: %w", err)`):
- Backend service unavailable
- Network failures
- Credential parsing failures (malformed JSON)
- Cryptographic errors during key generation

## Edge Cases

- **What happens if GetInstance() is called before Initialize()?** - Returns error "secrets manager not initialized - waiting for ProviderConfig 'default'", triggers Crossplane retry until ProviderConfig is ready
- **How does GenerateAndStore handle secrets pending deletion?** - Returns a user error: "Secret for account '{account}' in namespace '{namespace}' is pending deletion. If accidental, restore it manually in the backend and retry. If intentional, wait for deletion to complete before recreating." Never restores silently — the human decides whether to restore or let it delete.
- **What happens when cache entry expires during retrieval?** - Lazy eviction removes expired entry on access, fetches fresh from backend, stores in cache with new expiration time
- **Can multiple namespaces share the same tenant credentials?** - No, namespace isolation ensures different secret paths even with same org/account (e.g., team-a/azdonedia/shared vs team-b/azdonedia/shared)
- **What if RSA key generation fails?** - Returns system error, does not store partial credentials, allows Crossplane to retry
- **How are concurrent cache invalidations handled?** - Write lock ensures thread-safe deletion, multiple invalidations are idempotent
- **What happens if the backend is temporarily unavailable?** - Returns system error with incident ID, Crossplane retries with exponential backoff, cache preserves existing valid entries until TTL expires
- **Can the backend be swapped at runtime?** - No. Initialize() uses sync.Once — the backend is fixed for the lifetime of the provider process. Updating `spec.secretsBackend.type` in ProviderConfig has no effect on a running provider — the second Initialize() call is silently ignored. A backend change requires a provider pod restart (`kubectl rollout restart deployment/provider-snowflake`), which resets sync.Once and triggers a new ProviderConfig reconciliation with the new backend.

## Dependencies

- **Error Handling (001)** - Used APIs: `NewUser()` - Contract: Must be available before secrets package is initialized

## Integration Points

- **ProviderConfig Controller** - Constructs backend from spec.secretsBackend, calls Initialize() - Key functions: `Initialize()`, `backends/aws.NewBackend()`, `backends/azure.NewBackend()`, `backends/vault.NewBackend()`, `backends/gcp.NewBackend()` - Notes: Backend type selected from ProviderConfig spec
- **SnowflakeAccount Controller** - Generates and retrieves tenant credentials - Key functions: `GenerateAndStore()`, `GetTenantCredentials()`, `InvalidateTenantCache()` - Notes: Namespace from cr.Namespace, public key used in CREATE ACCOUNT SQL
- **SnowflakeExecution Controller** - Retrieves tenant credentials for SQL execution - Key functions: `GetTenantCredentials()` - Notes: Credentials used for SQL execution in target account
- **Connection Pool (003)** - Consumes credentials for Snowflake connection creation - Key functions: Uses PlatformCredentials and OrgAdminCredentials types - Notes: Private key passed to Snowflake driver for JWT authentication

## Success Criteria

- **SC-001**: SecretBackend interface with GetSecret, PutSecret, DeleteSecret, IsSecretPendingDeletion, HealthCheck
- **SC-002**: AWS Secrets Manager backend implementation in backends/aws/
- **SC-003**: Azure Key Vault backend implementation in backends/azure/
- **SC-004**: HashiCorp Vault backend implementation in backends/vault/
- **SC-005**: GCP Secret Manager backend implementation in backends/gcp/
- **SC-006**: IsSecretPendingDeletion() returns false for backends without soft-delete (Azure, Vault, GCP)
- **SC-007**: SecretManager interface fully implemented with all methods
- **SC-008**: Singleton pattern with thread-safe initialization using sync.Once
- **SC-009**: GetInstance() returns error before Initialize() called
- **SC-010**: TTL-based in-memory caching with lazy eviction on access
- **SC-011**: Cache hits complete in <1μs (100x+ faster than backend API calls)
- **SC-012**: Zero allocations on cache hit path
- **SC-013**: Secret path construction validates all components non-empty
- **SC-014**: Tenant paths include namespace: `snowflake/tenant/{namespace}/{org}/{account}/platform-credentials`
- **SC-015**: Org admin paths exclude namespace: `snowflake/org/{org}/org-admin-credentials`
- **SC-016**: RSA key generation uses 2048-bit minimum key size
- **SC-017**: Public keys stored as single-line base64 without PEM delimiters
- **SC-018**: Private keys stored in PKCS#8 format with PEM delimiters
- **SC-019**: GenerateAndStore() returns a user error when secret is pending deletion — never restores silently
- **SC-020**: User error classification for secret not found, invalid credentials, permission denied
- **SC-021**: System error classification for service unavailable, network failures, parsing errors
- **SC-022**: HealthCheck() delegates to backend HealthCheck()
- **SC-023**: Integration tests select backend via SECRET_BACKEND env var (defaults to "aws"), each backend has its own integration test with real credentials
- **SC-024**: Mock testing support via InitializeForMockTesting() with mock SecretBackend
- **SC-025**: Test coverage ≥ 95%
- **SC-026**: Zero race conditions (go test -race passes)

## Security Considerations

- **Namespace Isolation**: Namespace MUST come from `metadata.namespace` (Kubernetes runtime), NEVER from user-provided spec fields. Different Kubernetes namespaces represent different tenants and must have separate credentials even if they specify the same account name. Example: team-a/azdonedia/shared-account uses different credentials than team-b/azdonedia/shared-account.

- **Backend Credential References**: Backend credentials (AWS keys, Azure service principal, Vault token, GCP service account) must NEVER be stored inline in the ProviderConfig spec — they must be referenced via a Kubernetes Secret using `credentialsSecretRef`. This ensures credentials are protected by Kubernetes RBAC and are not visible to anyone with read access to the ProviderConfig CRD:
    ```yaml
    spec:
      secretsBackend:
        type: aws         # or azure, vault, gcp
        aws:
          source: Secret  # or InjectedIdentity (leave credentialsSecretRef nil)
          region: eu-central-1
          credentialsSecretRef:
            name: aws-creds
            namespace: crossplane-system
            key: credentials
    ```

- **In-Memory Cache**: The cache holds plaintext RSA private keys in Go heap memory. This is acceptable because Kubernetes pod isolation prevents other pods and containers from accessing process memory. A node-level compromise would expose cache contents — this is an accepted risk in Kubernetes security models and applies equally to all Kubernetes secrets. The cache is never written to disk or persisted in etcd.

- **No Credential Logging**: Credentials (private keys, public keys, backend tokens) must never appear in log messages, error messages, or Kubernetes status conditions. Error messages must reference incident IDs only. This applies to both the secrets package and all callers.

- **JWT Authentication**: No passwords stored or transmitted. JWT tokens signed with RSA private key, public key registered in Snowflake user account. Private key never leaves the in-memory cache.

- **Key Generation**: RSA key generation uses `crypto/rand` (cryptographically secure). Minimum 2048-bit key size enforced. PKCS#8 encoding for private keys, PKIX encoding for public keys.

- **Credential Validation After Parsing**: After fetching from the backend and parsing JSON into `PlatformCredentials` or `OrgAdminCredentials`, the SecretManager must validate that all four fields (`account`, `username`, `public_key`, `private_key`) are non-empty before caching or returning. A tampered or malformed secret in the backend must fail fast rather than be cached and served to controllers.

- **Secret Path Traversal**: Path components (`namespace`, `org`, `account`) must be validated against an allowlist (alphanumeric, hyphens, underscores only) before constructing secret paths. A value containing `/` or `..` could construct a path pointing to a different tenant's credentials. Validation happens in `paths.go` and returns a system error for invalid components.

- **Cache Invalidation After Rotation**: The TTL-based cache keeps serving credentials for up to the configured TTL duration after a secret is rotated in the backend. Operators must call `InvalidateTenantCache()` or `InvalidateOrgAdminCache()` immediately after rotation to force fresh retrieval. This is a security procedure, not just a performance concern.

- **Log Injection via Credential Fields**: The `account` and `username` fields parsed from backend secrets are user-controlled values. If they contain newlines, tab characters, or JSON control characters and are used directly in structured log output, they can corrupt log entries or inject fake log lines. Credential fields must be sanitized (strip control characters) before use in any log message or error string.

## Performance Considerations

- **Cache Hit Performance**: <1μs latency for cache hits vs >100ms for backend API calls (100x+ speedup)
- **Lock Contention**: Read lock for cache hits (minimal contention), write lock only for insertions/eviction
- **Allocation Efficiency**: Zero allocations on cache hit path
- **Lazy Eviction**: No background goroutines for cache cleanup (eviction on access only)
- **TTL Configuration**: Configurable via ProviderConfig.spec.cacheTTL (default 5 minutes)
- **Manual Invalidation**: Explicit InvalidateTenantCache() and InvalidateOrgAdminCache() for forced refresh after credential updates

## References

- **Error Handling Specification**: `specs/001-error-handling.md` - Error classification and logging patterns
- **Internal Errors Package**: `internal/errors/` - NewUser(), ErrorDetails(), LogWithLevel() API
- **Secrets Manager Package**: `internal/secrets/` - Complete implementation
- **Integration Tests**: `internal/secrets/integration_test.go` - Real AWS backend testing with .env configuration



================

## Appendix: Usage Examples

### Example 1: Initialize with AWS Backend (ProviderConfig Controller)

```go
import (
    "context"
    "time"
    "github.com/allianz/yukimi/internal/secrets"
    awsbackend "github.com/allianz/yukimi/internal/secrets/backends/aws"
)

func (c *ProviderConfigController) Setup(ctx context.Context, cfg *v1alpha1.ProviderConfig) error {
    // Construct the backend based on spec
    var backend secrets.SecretBackend
    var err error

    switch cfg.Spec.SecretsBackend.Type {
    case "aws":
        backend, err = awsbackend.NewBackend(&awsbackend.AWSCredentials{
            Source: cfg.Spec.SecretsBackend.AWS.Source,
            Region: cfg.Spec.SecretsBackend.AWS.Region,
        })
    case "azure":
        backend, err = azurebackend.NewBackend(&azurebackend.AzureCredentials{
            VaultURL: cfg.Spec.SecretsBackend.Azure.VaultURL,
            TenantID: cfg.Spec.SecretsBackend.Azure.TenantID,
        })
    case "vault":
        backend, err = vaultbackend.NewBackend(&vaultbackend.VaultCredentials{
            Address:   cfg.Spec.SecretsBackend.Vault.Address,
            Token:     cfg.Spec.SecretsBackend.Vault.Token,
            MountPath: cfg.Spec.SecretsBackend.Vault.MountPath,
        })
    case "gcp":
        backend, err = gcpbackend.NewBackend(&gcpbackend.GCPCredentials{
            ProjectID: cfg.Spec.SecretsBackend.GCP.ProjectID,
        })
    default:
        return errors.NewUser(fmt.Sprintf(
            "spec.secretsBackend.type '%s' is not supported (expected: aws, azure, vault, gcp)",
            cfg.Spec.SecretsBackend.Type))
    }
    if err != nil {
        return fmt.Errorf("failed to create secrets backend: %w", err)
    }

    // Initialize secrets manager with the backend
    if err := secrets.Initialize(backend, 5*time.Minute, c.logger); err != nil {
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
    creds, err := mgr.GetTenantCredentials(ctx, cr.Namespace, cr.Spec.Org, cr.Spec.Account)
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

    // Generates RSA key pair, stores via backend — returns user error if secret is pending deletion
    creds, err := mgr.GenerateAndStore(ctx, cr.Namespace, cr.Spec.Org, cr.Spec.Account)
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

### Example 4: Mock Backend for Unit Tests

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
    mock.secrets["snowflake/tenant/team-a/myorg/myaccount/platform-credentials"] = []byte(`{
        "account": "myaccount",
        "username": "PLATFORM",
        "public_key": "MIIBIjAN...",
        "private_key": "-----BEGIN PRIVATE KEY-----\n..."
    }`)

    mgr, _ := secrets.GetInstance()
    creds, err := mgr.GetTenantCredentials(context.Background(), "team-a", "myorg", "myaccount")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if creds.Username != "PLATFORM" {
        t.Errorf("expected PLATFORM, got %s", creds.Username)
    }
}
```
