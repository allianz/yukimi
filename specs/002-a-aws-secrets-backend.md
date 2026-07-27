# Specification: AWS Secrets Backend (002-a)

## Overview

This specification defines the AWS Secrets Manager implementation of the `SecretBackend` interface defined in `002-secrets-handling.md`. It provides the concrete `GetSecret`, `PutSecret`, `DeleteSecret`, `IsSecretPendingDeletion`, and `HealthCheck` operations backed by the AWS Secrets Manager API.

It is needed because `002-secrets-handling.md` defines the interface and manager logic but is intentionally backend-agnostic — this spec is where an actual backend gets built and tested against a real AWS account.

The technical approach wraps the AWS SDK for Go v2 Secrets Manager client behind the `SecretBackend` interface, translating AWS-specific error codes into the user/system error classification defined by `002-secrets-handling.md`.

## Scope

This specification defines the AWS Secrets Manager backend that:

- Implements `SecretBackend` using AWS SDK for Go v2
- Resolves AWS credentials from `ProviderConfig.Spec.Credentials` via Crossplane's standard credential mechanism
- Maps AWS Secrets Manager API responses and error codes to the error classification from `002-secrets-handling.md`
- Provides `InitializeForIntegrationTesting()` for tests that run against a real AWS account

**Out of Scope**:

- The `SecretBackend` interface definition itself (see `002-secrets-handling.md`)
- Caching, path construction, credential parsing, RSA key generation (all in the manager layer, `002-secrets-handling.md`)
- Any backend other than AWS Secrets Manager

## Key Concept: Credential Resolution

The AWS backend never reads Kubernetes Secrets itself and never defines its own credential-reference type. The ProviderConfig controller resolves credentials from `ProviderConfig.Spec.Credentials` (`xpv1.CommonCredentialSelectors`, Crossplane's standard mechanism) and passes the resolved raw values into `AWSCredentials`:

- **`Source: InjectedIdentity`** — the ProviderConfig controller passes an `AWSCredentials{Region: ...}` with no static keys, without calling the extractor. `resource.CommonCredentialExtractor` has no built-in handler for `InjectedIdentity` (it returns an error if called with this source) — each provider is expected to skip the extractor and rely on the AWS SDK's default credential chain, which picks up the pod's IRSA web identity token automatically. `NewBackend()` does nothing special for this case.

- **`Source: Secret`** — the ProviderConfig controller calls `resource.CommonCredentialExtractor(ctx, xpv1.CredentialsSourceSecret, client, cfg.Spec.Credentials.CommonCredentialSelectors)`, which reads `Spec.Credentials.SecretRef` via Crossplane's standard extractor, parses the raw bytes (expected to contain an access key ID and secret access key), and passes them into `AWSCredentials{AccessKeyID, SecretAccessKey, Region}`.

**Important**: This spec's `NewBackend()` function only ever receives already-resolved credential values. It never touches `client-go`, Kubernetes Secrets, or `SecretRef` directly — that responsibility belongs to the ProviderConfig controller.

## Key Concept: Pending Deletion via AWS Soft-Delete

AWS Secrets Manager supports a 30-day soft-delete window: calling `DeleteSecret` marks a secret for deletion but keeps it recoverable until `DeletedDate` passes, unless `ForceDeleteWithoutRecovery` is set (which this backend never uses).

`IsSecretPendingDeletion()` calls AWS's `DescribeSecret` API and checks whether the response's `DeletedDate` field is set and non-nil — if so, the secret is pending deletion.

**Important**: This backend never calls AWS's `RestoreSecret` API automatically. Per `002-secrets-handling.md`, `GenerateAndStore()` and the rotation methods surface a pending-deletion secret as a system error; only an operator decides whether to restore it (via a manual `RestoreSecret` call outside this package) or let the deletion complete.

## Technical Context

**Language/Version**: Go 1.24.0

**Primary Dependencies**: `github.com/aws/aws-sdk-go-v2`, `github.com/aws/aws-sdk-go-v2/service/secretsmanager`, `github.com/aws/aws-sdk-go-v2/config`

**Storage**: AWS Secrets Manager (encrypted at rest with AWS KMS)

**Testing**: Go testing framework, integration tests against a real AWS account using `.env` configuration

**Performance Goals**: Backend calls are the slow path (>100ms) by design — the TTL cache in `002-secrets-handling.md` is what makes the overall system fast, not this backend

**Constraints**: Must implement `secrets.SecretBackend` exactly; never calls `RestoreSecret` automatically; never logs (returns errors only, per `002-secrets-handling.md`)

## Public API

```go
// AWSCredentials configures the AWS Secrets Manager backend.
// Populated by the ProviderConfig controller from Spec.Credentials
// (Crossplane's standard credential mechanism) — never constructed
// from a custom Kubernetes Secret reference type.
type AWSCredentials struct {
    Region          string // AWS region (e.g., "eu-central-1") — required
    AccessKeyID     string // Empty when Source is InjectedIdentity
    SecretAccessKey string // Empty when Source is InjectedIdentity
    SessionToken    string // Optional, only used with AccessKeyID/SecretAccessKey
}

// NewBackend creates an AWS Secrets Manager backend implementing secrets.SecretBackend.
//
// Returns:
//   - System error if Region is empty or the AWS SDK client cannot be constructed
func NewBackend(creds *AWSCredentials) (secrets.SecretBackend, error)

// InitializeForIntegrationTesting initializes the secrets manager singleton
// (see secrets.Initialize in 002-secrets-handling.md) with a real AWS backend
// built from environment variables. Cache TTL is hardcoded to 30 seconds.
// Panics if AWS config loading fails — integration tests are expected to run
// in a controlled environment where this cannot happen.
//
// Environment variables:
//   - AWS_REGION: AWS region (e.g., "eu-central-1") — required
//   - AWS_PROFILE: AWS named profile (optional; uses default credential chain if unset)
func InitializeForIntegrationTesting()
```

## Project Structure

```text
internal/secrets/backends/aws/
├── backend.go       # AWS Secrets Manager implementation of secrets.SecretBackend
├── credentials.go   # AWSCredentials type
├── errors.go        # AWS error code -> user/system error classification
├── integration.go   # InitializeForIntegrationTesting()
└── backend_test.go  # Integration tests (require real AWS credentials via .env)
```

## Error Classification

Per `002-secrets-handling.md`, the end user creating a `SnowflakeAccount` never touches AWS directly — every failure this backend can produce is a system error. This backend never returns a user error.

**System Errors** (use `fmt.Errorf("context: %w", err)`):

- Secret not found (`ResourceNotFoundException`)
- Invalid credentials (`InvalidClientTokenId`, `ExpiredToken`, `UnrecognizedClientException`)
- Missing IAM permissions (`AccessDeniedException`)
- Invalid region or malformed request (`InvalidParameterException`, `InvalidRequestException`)
- AWS service unavailable (`InternalServiceErrorException`)
- Network failures (timeouts, DNS resolution failures)

## Edge Cases

- **What happens if `Region` is empty in `AWSCredentials`?**
  `NewBackend()` returns a system error immediately; the backend is never constructed.

- **What happens if IRSA is misconfigured (no web identity token available) and `AccessKeyID`/`SecretAccessKey` are also empty?**
  The AWS SDK's default credential chain returns a credential resolution error on the first API call, surfaced as a system error from that call (not from `NewBackend()`, which does not make any AWS calls itself).

- **How does `IsSecretPendingDeletion` behave for a secret that does not exist at all?**
  `DescribeSecret` returns `ResourceNotFoundException`; this is surfaced as `(false, nil)` rather than an error, since "not pending deletion" and "does not exist" are both valid states for a caller about to create the secret via `GenerateAndStore`.

- **What happens if AWS Secrets Manager rate-limits requests (`ThrottlingException`)?**
  Surfaced as a system error; the SDK's built-in retry/backoff is used before this error is returned, so this only occurs after retries are exhausted.

- **Does this backend ever call `RestoreSecret`?**
  No, never automatically. See Key Concept: Pending Deletion via AWS Soft-Delete.

## Dependencies

- **Secrets Handling (002)** - Used APIs: `secrets.SecretBackend` (interface implemented by this package) - Contract: This package's `NewBackend()` return value is passed to `secrets.Initialize()`

## Integration Points

- **ProviderConfig Controller** - Resolves AWS credentials from `Spec.Credentials` and calls `aws.NewBackend()`, then passes the result to `secrets.Initialize()` - Key functions: `NewBackend()` - Notes: Credential resolution uses Crossplane's standard mechanism, not a custom type

## Success Criteria

- **SC-001**: `backend` struct implements `secrets.SecretBackend` fully
- **SC-002**: `GetSecret` maps to AWS Secrets Manager `GetSecretValue`
- **SC-003**: `PutSecret` maps to AWS Secrets Manager `CreateSecret` (new) or `PutSecretValue` (existing)
- **SC-004**: `DeleteSecret` maps to AWS Secrets Manager `DeleteSecret` with soft-delete (never `ForceDeleteWithoutRecovery`)
- **SC-005**: `IsSecretPendingDeletion` maps to AWS Secrets Manager `DescribeSecret`, checking `DeletedDate`
- **SC-006**: `HealthCheck` verifies AWS connectivity and credentials (e.g., via `ListSecrets` with a minimal page size)
- **SC-007**: AWS error codes are classified as system errors per Error Classification above
- **SC-008**: `NewBackend()` returns a system error for empty `Region`
- **SC-009**: `InitializeForIntegrationTesting()` reads `AWS_REGION` and `AWS_PROFILE` from environment
- **SC-010**: Integration tests run against a real AWS account and are skipped in `-short` mode
- **SC-011**: Test coverage ≥ 90% (lower than 002's 95% since integration tests dominate this package)

## Security Considerations

- **No Static Credentials in ProviderConfig Spec**: AWS access keys must never be stored inline in the ProviderConfig spec — they are resolved via Crossplane's standard `Spec.Credentials` mechanism, which reads them from an RBAC-protected Kubernetes Secret when `Source: Secret` is used.

- **Prefer InjectedIdentity**: `Source: InjectedIdentity` (AWS IRSA) is preferred over `Source: Secret` where the cluster supports it, since it avoids storing long-lived static credentials entirely.

- **No Automatic Restore**: This backend never calls `RestoreSecret` automatically — see Key Concept: Pending Deletion via AWS Soft-Delete.

## Performance Considerations

- **Backend Call Latency**: AWS Secrets Manager API calls typically take >100ms; this is expected and mitigated by the TTL cache in the manager layer (`002-secrets-handling.md`), not by this backend
- **SDK Retry Behavior**: Uses the AWS SDK's default retry/backoff configuration for transient errors (e.g., throttling)

## References

- **Secrets Handling Specification**: `specs/002-secrets-handling.md` - SecretBackend interface, SecretManager, caching, error classification
- **AWS Backend Package**: `internal/secrets/backends/aws/` - Complete implementation
- **AWS SDK for Go v2 Secrets Manager**: `github.com/aws/aws-sdk-go-v2/service/secretsmanager` - Client library used by this backend



================

## Appendix: Usage Examples

### Example 1: Construct the AWS Backend (ProviderConfig Controller)

```go
import (
    "context"
    "github.com/allianz/yukimi/internal/secrets"
    awsbackend "github.com/allianz/yukimi/internal/secrets/backends/aws"
)

func (c *ProviderConfigController) buildBackend(ctx context.Context, cfg *v1alpha1.ProviderConfig) (secrets.SecretBackend, error) {
    creds := &awsbackend.AWSCredentials{Region: cfg.Spec.Region}

    // CommonCredentialExtractor has no built-in InjectedIdentity handler — each
    // provider implements its own. For InjectedIdentity, leave creds' key fields
    // empty; the AWS SDK's default credential chain picks up the pod's IRSA
    // token automatically. Only call the extractor for Source: Secret.
    if cfg.Spec.Credentials.Source == xpv1.CredentialsSourceSecret {
        rawCreds, err := resource.CommonCredentialExtractor(ctx, cfg.Spec.Credentials.Source, c.kube, cfg.Spec.Credentials.CommonCredentialSelectors)
        if err != nil {
            return nil, fmt.Errorf("failed to resolve provider credentials: %w", err)
        }
        // rawCreds contains an access key ID and secret access key (format
        // defined by the operator's Kubernetes Secret contents).
        if err := parseAWSCredentials(rawCreds, creds); err != nil {
            return nil, fmt.Errorf("failed to parse AWS credentials: %w", err)
        }
    }

    return awsbackend.NewBackend(creds)
}
```

### Example 2: Full Initialization (ProviderConfig Controller)

```go
import (
    "context"
    "time"
    "github.com/allianz/yukimi/internal/secrets"
)

func (c *ProviderConfigController) Setup(ctx context.Context, cfg *v1alpha1.ProviderConfig) error {
    backend, err := c.buildBackend(ctx, cfg)
    if err != nil {
        return fmt.Errorf("failed to create secrets backend: %w", err)
    }

    if err := secrets.Initialize(backend, 5*time.Minute); err != nil {
        return fmt.Errorf("failed to initialize secrets manager: %w", err)
    }

    return nil
}
```

### Example 3: Integration Test

```go
import (
    "context"
    "testing"
    awsbackend "github.com/allianz/yukimi/internal/secrets/backends/aws"
)

func TestAWSBackend_HealthCheck_Integration(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test in short mode")
    }

    awsbackend.InitializeForIntegrationTesting()

    mgr, err := secrets.GetInstance()
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }

    if err := mgr.HealthCheck(context.Background()); err != nil {
        t.Fatalf("health check failed: %v", err)
    }
}
```
