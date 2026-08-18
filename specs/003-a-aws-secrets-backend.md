# Specification: AWS Secrets Manager Backend (003-a)

## Overview

`internal/secrets/aws/` implements 003's `Backend` interface against AWS Secrets Manager (ASM), the reference secret store design.md 3.11.1 names. It solves the one problem 003 deliberately left open: 003 defines the interface, the path grammar, the credential shape and every backend-agnostic behavior layered on top, but ships only an in-memory fake — so the test suite is green while no deployed provider can authenticate to Snowflake, because there is nowhere durable to keep the `platform` user's RSA private key. This spec is needed because the platform cannot run without exactly one concrete backend, and ASM is the one design.md 3.11.1 builds its isolation argument around: the secret path is enforced by AWS IAM rather than by the controller, so a controller bug that constructs the wrong namespace is denied by AWS instead of quietly succeeding. The technical approach is a thin, stateless adapter: each of the five interface methods maps to one primary ASM API call, a second `DescribeSecret` call disambiguates the two outcomes ASM reports with one error code, and every AWS failure is translated into one of 003's five sentinel errors — which makes this package the only place in the repository where a vendor error code appears, and the only place an AWS SDK enters `go.mod`.

## Scope

This specification defines the `internal/secrets/aws/` package that:
- Constructs an ASM client from a region and an optional customer-managed KMS key ID, taking AWS credentials exclusively from the SDK's default chain.
- Implements all five `Backend` methods — `Get`, `Create`, `Update`, `Delete`, `Purge` — over five ASM operations: `GetSecretValue`, `CreateSecret`, `PutSecretValue`, `DeleteSecret`, `DescribeSecret`.
- Reproduces, against a real store with a real soft-delete recovery window, exactly the state machine `FakeBackend` (003) models in memory — including which of `ErrAlreadyExists` and `ErrPendingDeletion` a failed `Create` reports.
- Translates every ASM error, typed and untyped alike, into one of 003's sentinels, or passes it through unrecognized for 003 to treat as a permanent fault.
- Documents the IAM grants and KMS key-policy grants a deployment must give the controller's role, and the two ways such a policy silently matches nothing.
- Defines the integration tests that prove the mapping and the state machine against a live AWS account — the only tests in the repository that need one.

**Out of Scope**:
- Everything 003 owns: path construction and validation, the `Credentials` JSON shape, keypair generation, the TTL cache, `CreateOrRecover`, and `Rotate`. This package sees a `Path` and a `[]byte` and nothing else.
- **All user/system error classification**, with exactly one exception — the constructor's empty-region check. Every other failure leaves this package as a sentinel from `internal/secrets`; 001's user-vs-system decision for each sentinel is made by 003's Error Classification, and the incident ID and log line are 001's. This package never logs.
- Any other backend. A Vault implementation would be `003-b` in `internal/secrets/vault/`, and is deliberately not planned.
- Selecting which backend runs. That is `cmd/provider/main.go` switching on `BaseConfig.CloudProvider()` (002); this spec owns the constructor `main.go` calls, not the call.
- The IAM policy as a deployable artifact. Ops owns the role and its policy document; this spec documents the grants required and the shape they must take.
- Restoring a secret from its recovery window. `RestoreSecret` is never called — see below.
- A `HealthCheck` method, or any API call at construction time.

## Key Concept: One Interface, Two AWS Write APIs

ASM has no upsert. It has `CreateSecret`, which fails if the name is taken, and `PutSecretValue`, which fails if the name is free — which is precisely 003's `Create`/`Update` split, and the reason that split exists in the interface at all. The mapping is therefore one-to-one and must not be blurred:

- **`Create` is `CreateSecret`, never `PutSecretValue`.** 010 stores a keypair *before* `CREATE ACCOUNT` runs, so "create, failing if occupied" has to be atomic in the store itself. `CreateSecret` is atomic on the name: when two controller replicas race on the same fresh tenant path, exactly one succeeds and the other is told the name is taken. A `PutSecretValue`-based create would instead add a version to whatever is already there — silently re-keying a live tenant whose account already authenticates with the existing key.
- **`Update` is `PutSecretValue`, never `CreateSecret`.** `PutSecretValue` adds a new version and moves the `AWSCURRENT` staging label to it; ASM automatically moves `AWSPREVIOUS` to the version `AWSCURRENT` came from. The previous credential therefore stays retrievable after a rotation, which is what makes a botched rotation recoverable by an operator rather than terminal. This package passes no `VersionStages`, precisely so that automatic `AWSCURRENT`/`AWSPREVIOUS` handling applies.

Neither method may fall back to the other on failure. A `Create` that fails because the name is taken is a state this package reports, not one it works around; the workaround belongs to `CreateOrRecover` (003), which is the only code allowed to decide that an occupied path should be purged or reused.

**Important**: Values are written as `SecretBinary`, not `SecretString`. `Backend` is an opaque byte-blob store, and `SecretString` would force this package to assume its input is valid UTF-8 — an assumption the interface does not license. `Get`, however, accepts **both**: it returns `SecretBinary` when set and falls back to `[]byte(SecretString)` otherwise. That fallback is not a convenience. The org-admin credential is provisioned out-of-band by ops (003 treats `ErrNotFound` on the org-admin path as "ops has not provisioned this yet"), which in practice means the console or `aws secretsmanager create-secret --secret-string`, so a read path that understood only `SecretBinary` would fail on the one secret the platform never writes itself.

## Key Concept: The Recovery Window Is the State Machine

`DeleteSecret` in ASM is a soft delete: the secret is scheduled for deletion, keeps its name reserved, and remains restorable for a recovery window — 30 days by default. This package leans on that window deliberately rather than working around it, and the resulting behavior is exactly what `internal/secrets/fake.go` models in memory:

- **`Delete` never sets `ForceDeleteWithoutRecovery` and never sets `RecoveryWindowInDays`**, so the account-wide default window applies. A tenant credential deleted alongside a `DROP ACCOUNT` (017, not yet written) is recoverable by an operator for a month.
- **`Purge` always sets `ForceDeleteWithoutRecovery: true`.** It is the single force-delete in the package, and 003 calls it only from `CreateOrRecover`'s pending-deletion branch, where the stored value has already been decided to be stale.
- **`RestoreSecret` is never called.** Resurrecting a credential the platform deliberately retired is an operator's decision made with knowledge this package does not have — whether the Snowflake account behind the path still exists. An automatic restore inside a reconcile loop would make a deletion silently reversible by a retry.

The correspondence with `FakeBackend` is the acceptance criterion for this package, method by method. If a row below diverges, either this implementation is wrong or 003's fake is lying to every consumer that tests against it:

| `FakeBackend` (003) | This package, against ASM |
| ------------------- | ------------------------- |
| `Get` on a pending entry → `ErrPendingDeletion` | `GetSecretValue` → `InvalidRequestException` → `ErrPendingDeletion` |
| `Get` on an absent entry → `ErrNotFound` | `GetSecretValue` → `ResourceNotFoundException` → `ErrNotFound` |
| `Create` on a live entry → `ErrAlreadyExists` | `CreateSecret` collision, `DeletedDate` nil → `ErrAlreadyExists` |
| `Create` on a pending entry → `ErrPendingDeletion` | `CreateSecret` collision, `DeletedDate` non-nil → `ErrPendingDeletion` |
| `Update` on an absent **or pending** entry → `ErrNotFound` | `PutSecretValue` → `ResourceNotFoundException` **or** `InvalidRequestException` → `ErrNotFound` |
| `Delete` on an absent entry → `nil` | `DeleteSecret` → `ResourceNotFoundException` → `nil` |
| `Delete` on a live entry → marks pending | `DeleteSecret` → schedules deletion |
| `Purge` → always `nil`, entry gone | force-delete, then confirm the name is free |

The one row worth stating twice is `Update`: ASM reports a pending-deletion secret to `PutSecretValue` as `InvalidRequestException`, and this package maps that to `ErrNotFound` rather than `ErrPendingDeletion`. `Backend.Update`'s documented vocabulary does not include `ErrPendingDeletion`, and 003's `Rotate` — the only caller — treats not-found as "rotating something never provisioned is a caller bug." A secret inside its recovery window is, for the purpose of rotating a live credential, not there.

## Key Concept: `DescribeSecret` Disambiguates, It Does Not Detect

Pending deletion is never discovered by polling. `DescribeSecret` is a *second* call, made only after a primary call has already failed in a way that maps to more than one sentinel:

- `CreateSecret` against an occupied name reports the same failure whether the occupant is live (`ErrAlreadyExists`) or scheduled for deletion (`ErrPendingDeletion`) — the two branches `CreateOrRecover` must tell apart, and the entire reason 003 has a fifth method. On any collision-shaped error from `CreateSecret` — `ResourceExistsException`, or an `InvalidRequestException` — this package calls `DescribeSecret` and reads `DeletedDate`: non-nil → `ErrPendingDeletion`, nil → `ErrAlreadyExists`.
- `DescribeSecret` is trustworthy for this because it *succeeds* on a secret scheduled for deletion — its documented failures are only `ResourceNotFoundException`, `InvalidParameterException` and `InternalServiceError`. There is no state in which the secret exists and `DescribeSecret` refuses to describe it.
- **If the disambiguating `DescribeSecret` itself fails, its own mapped sentinel is returned.** A `Describe` that is throttled or denied must surface as `ErrUnavailable` or `ErrDenied`, never be defaulted to `ErrAlreadyExists`. Guessing `ErrAlreadyExists` would send `CreateOrRecover` down its reuse branch and hand 010 a credential nobody verified; guessing `ErrPendingDeletion` would send it down the purge branch and destroy a live tenant's key.
- If `DescribeSecret` returns `ResourceNotFoundException` — the occupant vanished between the two calls — the collision is reported as `ErrAlreadyExists`. This is the conservative choice: it makes `CreateOrRecover` re-`Get` (which will fail loudly with `ErrNotFound`) rather than force-delete a name whose state is unknown.

`GetSecretValue` needs the same treatment for the opposite reason: it reports a pending-deletion secret as `InvalidRequestException`, a code that also covers "managed by another service." The mapping is `ErrPendingDeletion` confirmed by `DescribeSecret`'s `DeletedDate`, falling back to returning the error unclassified when `DeletedDate` is nil — a secret this package cannot read and cannot explain is a permanent fault for an operator, not a state 003 has a sentinel for.

## Key Concept: Force Deletion Is Asynchronous

`DeleteSecret` with `ForceDeleteWithoutRecovery` is not immediately consistent. AWS states it plainly: "If you delete a secret and then immediately create a secret with the same name, use appropriate back off and retry logic." 003's `CreateOrRecover` issues **exactly one** `Create` after `Purge` (003 SC-010), and that is not negotiable from this side — so a `Purge` that returned as soon as the API call succeeded would make SC-010 pass forever against `FakeBackend` and fail intermittently against AWS, with the failure surfacing as a spurious `ErrAlreadyExists` on a brand-new tenant.

`Purge` therefore does not return until the name is free: after the force-delete call, it polls `DescribeSecret` until `ResourceNotFoundException`, with bounded exponential backoff and a total budget, honoring the caller's context throughout. Exhausting the budget is `ErrUnavailable` — a retryable condition, which is correct, because the next reconcile's `CreateOrRecover` will simply try again.

Two AWS behaviors make this cheap rather than awkward. Force-deleting a name that holds nothing — never created, or already force-deleted — does **not** return `ResourceNotFoundException`, so `Purge`'s required idempotency (003: "succeeding even if there is nothing to remove") comes free from the service. And the very first poll usually already sees `ResourceNotFoundException`, so the common path costs one extra call, not a wait.

**Important**: the poll's budget and backoff are unexported fields on `Backend`, set by `New` from package constants — not package-level variables. Tests construct a `Backend` directly with a short budget and a fake client; nothing mutable lives at package scope (mirroring 003 SC-018).

## Public API

```go
package aws

import (
    "context"

    "github.com/allianz/yukimi/internal/secrets"
)

// Backend implements secrets.Backend against AWS Secrets Manager. It is
// stateless apart from its client and configuration, and safe for concurrent
// use by every controller worker.
type Backend struct {
    // unexported: the ASM client, the optional KMS key ID, and the Purge
    // confirmation poll's backoff and budget.
}

// Compile-time proof that this package satisfies the interface 003 owns. This
// assertion, not a doc comment, is what keeps the two in step.
var _ secrets.Backend = (*Backend)(nil)

// New constructs a Backend for one AWS region.
//
// region and kmsKeyID come from BaseConfig.AWS.Region and
// BaseConfig.AWS.KmsKeyId (002), which have already checked their *shape*;
// this constructor only rejects an empty region. They are taken as plain
// strings rather than as a config.AWSSettings so that this package never
// imports internal/config — the store must not depend on how the operator
// happens to configure it.
//
// AWS credentials come from the SDK's default chain and from nowhere else:
// IRSA in-cluster and AWS_PROFILE locally are the same code path, with no
// branch and nothing read from BaseConfig. ctx is used only for
// config.LoadDefaultConfig; it is not retained.
//
// New makes no AWS API call — no probe, no ListSecrets, no key check. A
// misconfigured region or a missing IAM grant is discovered on first use, as
// a sentinel, by the reconcile that needed it.
//
// Returns:
//   - User error if region is empty, so a ConfigMap missing aws.region fails
//     at startup rather than on the first reconcile
//   - System error if the SDK cannot resolve a default configuration
func New(ctx context.Context, region, kmsKeyID string) (*Backend, error)

// Get implements secrets.Backend. Returns SecretBinary when set, otherwise
// []byte(SecretString) — see the note on out-of-band org-admin credentials.
func (b *Backend) Get(ctx context.Context, path secrets.Path) ([]byte, error)

// Create implements secrets.Backend via CreateSecret, passing KmsKeyId when
// New received a non-empty one.
func (b *Backend) Create(ctx context.Context, path secrets.Path, value []byte) error

// Update implements secrets.Backend via PutSecretValue.
func (b *Backend) Update(ctx context.Context, path secrets.Path, value []byte) error

// Delete implements secrets.Backend via DeleteSecret, leaving the account's
// default recovery window.
func (b *Backend) Delete(ctx context.Context, path secrets.Path) error

// Purge implements secrets.Backend via DeleteSecret with
// ForceDeleteWithoutRecovery, then confirms the name is free.
func (b *Backend) Purge(ctx context.Context, path secrets.Path) error
```

The whole surface is one type, one constructor and five methods. There is no exported option type, no interface of this package's own, and nothing else a consumer could reach for — an import of `internal/secrets/aws` outside `cmd/provider/` should have exactly one plausible line in it.

Each method's ASM call, in full:

| Method | Primary call | Parameters | Notes |
| ------ | ------------ | ---------- | ----- |
| `Get` | `GetSecretValue` | `SecretId: path.String()` | no `VersionId`/`VersionStage` — `AWSCURRENT` implicitly |
| `Create` | `CreateSecret` | `Name: path.String()`, `SecretBinary`, `KmsKeyId` (iff non-empty) | on collision → `DescribeSecret` |
| `Update` | `PutSecretValue` | `SecretId: path.String()`, `SecretBinary` | no `VersionStages` — ASM moves `AWSCURRENT`/`AWSPREVIOUS` |
| `Delete` | `DeleteSecret` | `SecretId: path.String()` | no force flag, no `RecoveryWindowInDays` |
| `Purge` | `DeleteSecret` | `SecretId: path.String()`, `ForceDeleteWithoutRecovery: true` | then poll `DescribeSecret` until not found |

`SecretId` is always `path.String()` — a bare secret name, never an ARN. `Path` (003) exposes nothing but `String()`, the platform stores no ARNs anywhere, and a name lookup is exactly what makes the IAM path prefix the enforcement point design.md 3.11.1 relies on. Both of 003's paths are legal ASM names: `/` is an allowed name character, both are far under the 512-character limit, and neither ends in a hyphen followed by six characters — the suffix shape that makes ASM's partial-ARN lookup ambiguous.

`ClientRequestToken` is never set on `PutSecretValue`; the SDK generates one per call. Setting a deterministic token would make a retried `Update` with different bytes fail instead of creating a new version, which is the opposite of what a rotation retry needs.

## Project Structure

### Source Code

```text
internal/secrets/aws/
├── backend.go            # Backend, New, the five methods, the unexported api interface
├── backend_test.go       # per-method behavior against a fake api — no AWS account
├── errors.go             # mapError — the only file in the repo naming an AWS error code
├── errors_test.go        # one case per row of the mapping table
├── integration_test.go   # TestIntegration*, skipped by -short — needs a real AWS account
└── doc.go
```

`backend.go` defines an unexported interface over exactly the five ASM operations used, satisfied by `*secretsmanager.Client`:

```go
type api interface {
    GetSecretValue(context.Context, *secretsmanager.GetSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
    CreateSecret(context.Context, *secretsmanager.CreateSecretInput, ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error)
    PutSecretValue(context.Context, *secretsmanager.PutSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error)
    DeleteSecret(context.Context, *secretsmanager.DeleteSecretInput, ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error)
    DescribeSecret(context.Context, *secretsmanager.DescribeSecretInput, ...func(*secretsmanager.Options)) (*secretsmanager.DescribeSecretOutput, error)
}
```

Narrowing to five methods is what makes every branch in this package — both disambiguation forks, the `Purge` poll's timeout, and every row of the mapping table — reachable from a unit test with no AWS account and no network. It is also a standing check on scope: adding a sixth line to that interface means this package started doing something this spec did not authorize.

This package imports `internal/secrets` (for the interface, `Path` and the sentinels), `internal/errors` (for the constructor's one user error), and the AWS SDK. It must never be imported by `internal/secrets` — the parent defining an interface never depends on a child implementing it, and here the reverse import is a compile-time cycle rather than a style objection.

## Error Classification

This package makes **one** classification decision. Everything else is a sentinel handed to 003, which owns the user-vs-system verdict for each one (003's Error Classification), and to 001, which owns the log line and the incident ID. This package never calls `logger.Handle`, never constructs an incident ID and never logs.

**User Errors** (use `errors.NewUserError()`):
- Empty region at construction: `aws.region is empty; the AWS secrets backend requires a region (expected: eu-central-1)`. This is the one user error because it is the one failure caused by an editable value that no reconcile can ever recover from — every AWS call would fail identically forever. Raising it in `New` turns a mis-set ConfigMap into a startup failure with a readable message instead of an unexplained sentinel on every tenant.

**System Errors** (use `fmt.Errorf("context: %w", err)`):
- The SDK failing to resolve a default configuration in `New`: `failed to load AWS SDK configuration: %w`.
- Every sentinel below, wrapped with `%w` so `errors.Is` matches at the 003 layer, with the operation and the path in the message: `failed to create secret at snowflake/tenant/my_org/finance/analytics/platform-credentials: %w`. The path is safe to include — `Path.String()` carries only identifiers, never key material (003).
- Any ASM error this table does not recognize, returned wrapped but unmapped, for 003 to treat as a permanent fault.

The mapping, verified against the ASM API reference including its shared *Common Errors* list:

| ASM condition | Sentinel |
| ------------- | -------- |
| `ResourceNotFoundException` on `GetSecretValue` | `ErrNotFound` |
| `ResourceNotFoundException` on `PutSecretValue` | `ErrNotFound` |
| `ResourceNotFoundException` on `DeleteSecret` (either form) | `nil` — nothing to delete is success |
| `ResourceExistsException` or `InvalidRequestException` on `CreateSecret` | `DescribeSecret` disambiguates → `ErrAlreadyExists` or `ErrPendingDeletion` |
| `InvalidRequestException` on `GetSecretValue` | `ErrPendingDeletion` when `DeletedDate` is non-nil; otherwise unmapped |
| `InvalidRequestException` on `PutSecretValue` | `ErrNotFound` — parity with `FakeBackend.Update` |
| `InvalidRequestException` on `DeleteSecret` (already scheduled) | `nil` |
| `AccessDeniedException`, `NotAuthorized`, `UnrecognizedClientException`, `InvalidClientTokenId`, `OptInRequired`, `RequestExpired`, `IncompleteSignature` | `ErrDenied` |
| `ThrottlingException`, `InternalServiceError`, `InternalFailure`, `ServiceUnavailable`, request timeouts, connection failures, `context.Canceled`, `context.DeadlineExceeded` | `ErrUnavailable` |
| `Purge` confirmation poll exhausting its budget | `ErrUnavailable` |
| `LimitExceededException`, `InvalidParameterException`, `ValidationException`, `ValidationError`, `DecryptionFailure`, `EncryptionFailure`, anything unrecognized | unmapped — wrapped and returned for 003 to treat as a permanent fault |

Two mechanics of the mapping are load-bearing:

- **`errors.As` on `secretsmanager/types` first, then `smithy.APIError` on the code string.** The typed-error pass is not sufficient on its own: `AccessDeniedException`, `ThrottlingException`, `ServiceUnavailable`, `InternalFailure` and the other *Common Errors* are not in any operation's error list and have no generated Go type in the service package. A types-only mapping compiles, passes a naive test, and silently drops every permission failure and every throttle into the unmapped bucket — the two conditions an operator most needs named correctly. The `smithy.APIError` fallback is mandatory.
- **The classification depends on which call produced the error**, so `mapError` takes the operation as a parameter rather than inspecting the error alone. `ResourceNotFoundException` is `ErrNotFound` from a `Get` and `nil` from a `Delete`; `InvalidRequestException` is `ErrPendingDeletion` from a `Get` and `ErrNotFound` from a `Put`. A single global error→sentinel function cannot express this and must not be written.

## Edge Cases

- **What happens on `Create` immediately after `Purge` — the sequence `CreateOrRecover` performs?** - It succeeds, because `Purge` does not return until `DescribeSecret` reports the name free. This is the entire reason `Purge` polls: 003 SC-010 permits exactly one `Create` after the purge, and ASM's force-delete is eventually consistent, so the guarantee has to be established on this side of the interface.
- **What if the `Purge` confirmation poll never sees the name free?** - `ErrUnavailable` after the budget is exhausted, with the force-delete already issued. The path is left in whatever state AWS reached; the next reconcile calls `CreateOrRecover` again, which purges again — a force-delete on an already-purged name is not an error — and proceeds.
- **What happens on `Delete` for a path nothing was ever stored at?** - `nil`. `ResourceNotFoundException` from `DeleteSecret` is mapped to success, matching `FakeBackend.Delete` and keeping 017's deletion flow idempotent across a crash between `DROP ACCOUNT` and the secret delete.
- **What happens on `Delete` for a secret already scheduled for deletion?** - `nil`, and the existing deletion date is *not* extended. `Delete` is not a way to reset the recovery window; a second `Delete` is a no-op on purpose, since the alternative — silently re-scheduling — would make the window's end date depend on how many times a reconcile ran.
- **What happens on `Update` against a secret inside its recovery window?** - `ErrNotFound`, not `ErrPendingDeletion`. `Backend.Update`'s documented vocabulary has no pending-deletion case, and its only caller, `Rotate` (003), correctly treats this as "nothing live to rotate."
- **What happens if `aws.kmsKeyId` is added, changed, or removed after secrets already exist?** - Only secrets created *after* the change use the new key. This package passes `KmsKeyId` on `CreateSecret` and never calls `UpdateSecret`, so it never retrofits a key onto an existing secret — re-keying existing secrets is an ops migration, not something a reconcile should do implicitly to a store it does not own. Existing secrets keep working: `GetSecretValue` decrypts with whichever key each secret was created under, provided the role still has `kms:Decrypt` on it.
- **What if the configured KMS key is disabled, deleted, or not granted to the controller's role?** - `EncryptionFailure` on writes and `DecryptionFailure` on reads, both deliberately left **unmapped**. They are not `ErrDenied`: `ErrDenied` says "this principal may not touch this path," and 003 reports it as such, while a broken KMS key is a store-configuration fault that needs an operator and an incident ID, not a permissions review.
- **What if the org-admin credential was created by ops as a `SecretString`?** - It reads correctly — `Get` prefers `SecretBinary` and falls back to `SecretString`. If the platform later `Rotate`s that secret, the new version is a `SecretBinary` while older versions remain `SecretString`; ASM permits this per-version, and `Get` handles both, so the transition needs no migration.
- **What if a secret holds neither `SecretBinary` nor `SecretString`?** - Unmapped permanent fault, not `ErrNotFound`. The secret exists; something wrote it in a shape the platform cannot consume, and an operator has to look.
- **What if two provider replicas are configured with different regions?** - They operate on two different, unrelated stores and each will happily `Create` the same tenant path in its own region. Nothing in this package can detect that — the region is a single-valued deployment input from 002, and a split-brain deployment is an ops error this spec records rather than defends against.
- **Does `Get` ever return a version other than the current one?** - No. No `VersionId` or `VersionStage` is ever passed, so `AWSCURRENT` is always what is read. `AWSPREVIOUS` exists for an operator recovering from a bad rotation, and is reachable only outside this package.
- **Does this package retry?** - Only inside `Purge`'s confirmation poll, which is a consistency wait rather than a retry. Everything else relies on the SDK's built-in retryer for transport-level attempts and, above that, on the controller-runtime requeue — per CLAUDE.md, controllers do not implement retries, and neither does the store beneath them.

## Dependencies

- **`internal/secrets` (003)** - Used APIs: the `Backend` interface, `Path.String()`, and the sentinels `ErrNotFound`, `ErrAlreadyExists`, `ErrPendingDeletion`, `ErrDenied`, `ErrUnavailable` - Contract: this package imports 003 and 003 must never import this package; `Path` values are always constructed by 003's constructors, so this package performs no path validation of its own.
- **`internal/errors` (001)** - Used APIs: `errors.NewUserError()` - Contract: used exactly once, for the empty-region check in `New`.
- **`internal/config` (002)** - Not imported. `New` receives `BaseConfig.AWS.Region` and `BaseConfig.AWS.KmsKeyId` as plain strings from `cmd/provider/main.go`.
- **AWS SDK for Go v2** - Used APIs: `config.LoadDefaultConfig`, `secretsmanager.NewFromConfig`, the five operations above, `secretsmanager/types` typed errors, and `smithy.APIError` - Contract: the only place in the repository an AWS SDK dependency exists; credentials come from the default chain only.

## Integration Points

- **`cmd/provider/main.go`** - The single construction site: switches on `BaseConfig.CloudProvider()` (002), calls `New(ctx, cfg.AWS.Region, cfg.AWS.KmsKeyId)`, wraps the result exactly once in `secrets.NewCachedBackend`, and passes the wrapped `secrets.Backend` down - Key functions: `aws.New()`, `secrets.NewCachedBackend()` - Notes: `main.go`'s own flag and wiring work belongs to 002 and to whoever lands the wiring; this spec owns only the constructor being called.
- **`internal/secrets` (003)** - Consumes this package solely through the `Backend` interface it defines; `CreateOrRecover`, `Rotate` and `CachedBackend` all operate on this implementation without naming it - Key functions: `CreateOrRecover()`, `Rotate()`, `NewCachedBackend()` - Notes: `CreateOrRecover`'s post-`Purge` `Create` is the contract `Purge`'s confirmation poll exists to honor.
- **`internal/snowflake/pool` (004) and `internal/account/modules/account` (010)** - Reach this store only through 003's interface and are unit-tested against `FakeBackend`; neither may import this package - Notes: an import of `internal/secrets/aws` anywhere but `cmd/provider/` is the single grep that proves the store is still pluggable.
- **`make test-integration`** - Must export `.env` before running the suite (`set -a && source .env && set +a`, as the `dev` target already does), since the target runs `go test -v -run Integration` in a bare environment and these tests read their AWS settings from `.env` - Notes: this is a requirement on the target, landing with the implementation.

## Success Criteria

- **SC-001**: `*Backend` satisfies `secrets.Backend`, proven by a compile-time `var _ secrets.Backend = (*Backend)(nil)` assertion in the package.
- **SC-002**: `New` returns a user error when `region` is empty, and makes no AWS API call in any case — including the success path.
- **SC-003**: `New` reads AWS credentials only through the SDK's default chain; no credential, profile or role name is a parameter of, or read by, this package.
- **SC-004**: `Create` calls `CreateSecret` and never `PutSecretValue`; `Update` calls `PutSecretValue` and never `CreateSecret`. Neither falls back to the other on any error.
- **SC-005**: `Create` sets `KmsKeyId` when and only when `New` received a non-empty `kmsKeyID`.
- **SC-006**: Values are written as `SecretBinary`; `Get` returns `SecretBinary` when present and `[]byte(SecretString)` otherwise.
- **SC-007**: `Delete` never sets `ForceDeleteWithoutRecovery` and never sets `RecoveryWindowInDays`; `Purge` always sets `ForceDeleteWithoutRecovery: true`.
- **SC-008**: The strings `RestoreSecret`, `UpdateSecret`, `TagResource` and `ListSecrets` appear nowhere in the package.
- **SC-009**: A `CreateSecret` collision returns `ErrPendingDeletion` when `DescribeSecret` reports a non-nil `DeletedDate`, and `ErrAlreadyExists` when it is nil.
- **SC-010**: A failing disambiguating `DescribeSecret` returns that call's own mapped sentinel — never a defaulted `ErrAlreadyExists` or `ErrPendingDeletion`.
- **SC-011**: `Update` against a secret scheduled for deletion returns `ErrNotFound`, matching `FakeBackend.Update`.
- **SC-012**: `Delete` returns `nil` both for a path that never existed and for one already scheduled for deletion.
- **SC-013**: `Purge` returns only after `DescribeSecret` reports the name free, and returns `ErrUnavailable` when its confirmation budget is exhausted.
- **SC-014**: `Purge` returns `nil` for a path that never existed.
- **SC-015**: Every sentinel this package returns is wrapped with `%w` and matches via `errors.Is`, with the failing operation and the path in the message and no credential bytes anywhere in it.
- **SC-016**: Untyped AWS *Common Errors* — at minimum `AccessDeniedException` and `ThrottlingException` — are mapped via `smithy.APIError`, not left unmapped.
- **SC-017**: `mapError` is operation-aware: the same AWS error code maps to different outcomes for different calls, per the mapping table.
- **SC-018**: The package exposes no `HealthCheck`, no `Initialize`/`GetInstance` singleton, and holds no package-level mutable state — the `Purge` poll's backoff and budget are `Backend` fields.
- **SC-019**: The package imports `internal/secrets`, `internal/errors`, the AWS SDK and the standard library, and nothing else internal to this repository — in particular not `internal/config`.
- **SC-020**: No package other than `internal/secrets/aws` and `cmd/provider` imports `github.com/aws/aws-sdk-go-v2/...`. This grep is the pluggability guarantee.
- **SC-021**: Unit test coverage exceeds 95% and every row of the mapping table has a case, all against the unexported `api` fake with no network access.
- **SC-022**: Integration tests are named `TestIntegration*` **and** skip on `testing.Short()`, skip with a message when any required `.env` value is absent, use a unique account segment per run, and clean up every secret they created via `Purge`.

## Security Considerations

- **IAM is the enforcement point, and this package's job is to make that possible** (design.md 3.11.1). The controller's role must be granted per path prefix, tenant and org-admin separately, so org-admin credentials can be held to a narrower grant than tenant ones:

  ```text
  secretsmanager:GetSecretValue, CreateSecret, PutSecretValue, DeleteSecret, DescribeSecret
    on arn:aws:secretsmanager:<region>:<account>:secret:snowflake/tenant/<org>/*-??????
  secretsmanager:GetSecretValue, DescribeSecret
    on arn:aws:secretsmanager:<region>:<account>:secret:snowflake/org/<org>/*-??????
  ```

  The org-admin prefix needs no write actions at all: ops provisions that credential out-of-band, and the platform only reads it. This is documented, not shipped — ops owns the policy document.
- **Trap 1: the random ARN suffix.** ASM appends a random six-character suffix to every secret's ARN, so a policy resource that ends at the path (`.../platform-credentials`) matches *nothing*. It must end in `-??????` or `*`. This fails silently in the worst way: every call returns `AccessDeniedException`, which this package maps to `ErrDenied` and 003 reports as a permission problem — pointing an operator at the role's trust or actions rather than at a typo in the resource pattern.
- **Trap 2: the role is not per-tenant scoped.** One controller serves every namespace, so it necessarily holds a grant covering every tenant path under the org. Path-based isolation therefore defends against the controller *constructing the wrong path* — the bug design.md 3.11.1 is written against — and not against a compromised controller, which already holds a credential valid for every path it could construct. design.md 3.11.2's OIDC route exists precisely because this is the weaker of the two guarantees; nothing in this package can strengthen it.
- **Customer-managed KMS keys need two more grants.** With a non-empty `aws.kmsKeyId`, the controller's role additionally needs `kms:GenerateDataKey` (writes) and `kms:Decrypt` (reads) on that key, and the key's own policy must admit the role. A missing KMS grant surfaces as `EncryptionFailure`/`DecryptionFailure` — unmapped, not `ErrDenied` — so the diagnostic path for a KMS misconfiguration is deliberately distinct from that of a Secrets Manager one.
- **This package never logs.** Not the path, not an error, not a request ID. Every message reaching an operator is built by 001 from what 003 classified, which is what keeps a single audited place where a value could ever be written to a log — and it is not here, where the plaintext credential bytes are in hand.
- **No credential value ever leaves through a non-return path.** Values pass from `[]byte` into `SecretBinary` and back; they are never put into an error message, a tag, a `ClientRequestToken`, or any request parameter AWS documents as appearing in CloudTrail. AWS is explicit that only `SecretBinary`, `SecretString` and `RotationToken` are excluded from CloudTrail logging, which is exactly the set this package confines secret material to.

## Performance Considerations

- **One AWS round-trip per call on the happy path.** The second `DescribeSecret` occurs only on a collision or a pending-deletion state, i.e. only when the primary call already failed, so the steady state costs nothing extra. `Purge` is the one method that always costs at least two calls, and it runs only from `CreateOrRecover`'s recovery branch.
- **Read amplification is 003's problem, solved.** Every reconcile that needs a credential would otherwise be a `GetSecretValue`; `CachedBackend` (003) wraps this package once in `main.go`, so the store sees one read per path per TTL, not one per reconcile. This package holds no cache of its own and must not grow one — a second cache layer with its own TTL would make staleness impossible to reason about.
- **`PutSecretValue` should not be called at a sustained rate above once per 10 minutes** per secret: ASM keeps every version created in the last 24 hours plus the 100 most recent, so a tighter loop accumulates versions faster than they are reclaimed and eventually hits the per-secret version quota as `LimitExceededException`. Nothing in the current tree does this — `Update` is reached only through `Rotate`, which no scheduled feature calls yet — but a future rotation feature must respect it, and this is where the constraint is recorded.
- **The `Purge` confirmation poll is bounded and context-aware**, with exponential backoff so a slow force-delete does not spin. It blocks the calling reconcile for at most its budget; because the first poll usually already sees the name free, the typical cost is one extra call rather than a wait.
- **`Backend` is safe for concurrent use** by every controller worker: the SDK client is concurrency-safe and this package adds no mutable state, so no lock, pool or per-reconcile client is needed.

## References

- **Product design**: `specs/design.md`, §3.11 (stepping down from org-level privilege), §3.11.1 (the ASM path grammar, the namespace as sole trust anchor, enforcement by the store's authorization layer, and ASM as reference rather than requirement), §3.11.2 (why the OIDC route exists and why the IAM path check is the weaker guarantee), §3.6 (the `platform` user whose keypair this store holds).
- **Secrets Handling (003)**: `specs/003-secrets-handling.md`, `internal/secrets/backend.go`, `internal/secrets/path.go`, `internal/secrets/fake.go` - the `Backend` interface and sentinels this package implements and emits, `Path.String()`, and the `FakeBackend` state machine the table above must match.
- **Error Handling (001)**: `internal/errors/errors.go` - `NewUserError()`, used exactly once, for the empty-region check.
- **Base Config (002)**: `specs/002-base-config.md`, `internal/config/config.go` - `AWSSettings.Region` and `AWSSettings.KmsKeyId`, shape-checked there and consumed here as plain strings; `CloudProvider()`, which `main.go` switches on to reach this constructor.
- **AWS Secrets Manager API Reference**: https://docs.aws.amazon.com/secretsmanager/latest/apireference/ - `CreateSecret`, `GetSecretValue`, `PutSecretValue`, `DeleteSecret`, `DescribeSecret`, and the shared *Common Errors* list every mapping row above was verified against — including the asynchronous force-delete caveat under `DeleteSecret` and the `AWSCURRENT`/`AWSPREVIOUS` staging-label behavior under `PutSecretValue`.
- **AWS Secrets Manager IAM reference**: https://docs.aws.amazon.com/secretsmanager/latest/userguide/reference_iam-permissions.html - the action names and resource-ARN forms the documented policy above uses.

<br/><br/><br/><br/><br/>

================

## Appendix: Usage Examples

### Example 1: Constructing the Backend in `main.go` (Primary Use Case)

```go
// In cmd/provider/main.go — the only place this package may be imported.
import (
    "context"
    "flag"
    "log"
    "time"

    "github.com/allianz/yukimi/internal/config"
    "github.com/allianz/yukimi/internal/secrets"
    secretsaws "github.com/allianz/yukimi/internal/secrets/aws"
)

func main() {
    configDir := flag.String("configDir", "/etc/yukimi/config", "directory containing baseConfig.yaml and sibling config files")
    flag.Parse()

    ctx := context.Background()

    cfg, err := config.Load(*configDir)
    if err != nil {
        log.Fatalf("failed to load base config: %v", err)
    }

    var backend secrets.Backend
    switch cfg.CloudProvider() {
    case "aws":
        // Region and KmsKeyId arrive shape-checked by 002; New rejects only an
        // empty region, so a ConfigMap missing aws.region fails here rather
        // than on the first reconcile.
        backend, err = secretsaws.New(ctx, cfg.AWS.Region, cfg.AWS.KmsKeyId)
        if err != nil {
            log.Fatalf("failed to construct AWS secrets backend: %v", err)
        }
    default:
        log.Fatalf("no secrets backend compiled in for cloud section %q (compiled in: aws)", cfg.CloudProvider())
    }

    // Wrapped exactly once (003); every consumer below sees only secrets.Backend
    // and never learns which store is behind it.
    cached := secrets.NewCachedBackend(backend, 5*time.Minute)

    // ... wire cached into the pool (004) and start the controller manager
    _ = cached
}
```

### Example 2: The Two-Pass Error Mapping

```go
// In internal/secrets/aws/errors.go — the only file in the repository that
// names an AWS error code.
import (
    stderrors "errors"
    "fmt"

    "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
    "github.com/aws/smithy-go"

    "github.com/allianz/yukimi/internal/secrets"
)

type operation string

const (
    opGet      operation = "get"
    opCreate   operation = "create"
    opUpdate   operation = "update"
    opDelete   operation = "delete"
    opDescribe operation = "describe"
)

// mapError translates an ASM error into one of 003's sentinels. It takes op
// because the same AWS code means different things per call:
// ResourceNotFoundException is ErrNotFound from a Get and success from a
// Delete. A single global error->sentinel function cannot express that.
//
// Returns nil only where the operation defines the condition as success.
func mapError(op operation, err error) error {
    if err == nil {
        return nil
    }

    // Pass 1 — typed errors from the service package.
    var notFound *types.ResourceNotFoundException
    if stderrors.As(err, &notFound) {
        switch op {
        case opDelete:
            return nil // nothing to delete is success (FakeBackend.Delete parity)
        default:
            return fmt.Errorf("%s: %w", op, secrets.ErrNotFound)
        }
    }

    var invalidRequest *types.InvalidRequestException
    if stderrors.As(err, &invalidRequest) {
        switch op {
        case opUpdate:
            // Backend.Update has no pending-deletion case; Rotate treats a
            // secret inside its recovery window as nothing live to rotate.
            return fmt.Errorf("%s: %w", op, secrets.ErrNotFound)
        case opDelete:
            return nil // already scheduled for deletion
        }
        // opGet / opCreate are resolved by the caller's DescribeSecret pass.
    }

    // ... ResourceExistsException, LimitExceededException, EncryptionFailure,
    // DecryptionFailure, InvalidParameterException

    // Pass 2 — untyped Common Errors. AccessDeniedException and
    // ThrottlingException have no generated Go type and appear in no
    // operation's error list, so a types-only mapping would silently drop
    // every permission failure and every throttle into the unmapped bucket.
    var apiErr smithy.APIError
    if stderrors.As(err, &apiErr) {
        switch apiErr.ErrorCode() {
        case "AccessDeniedException", "NotAuthorized", "UnrecognizedClientException",
            "InvalidClientTokenId", "OptInRequired", "RequestExpired", "IncompleteSignature":
            return fmt.Errorf("%s: %w", op, secrets.ErrDenied)
        case "ThrottlingException", "InternalServiceError", "InternalFailure", "ServiceUnavailable":
            return fmt.Errorf("%s: %w", op, secrets.ErrUnavailable)
        }
    }

    // Timeouts, connection failures and context cancellation also map to
    // ErrUnavailable; everything else is returned wrapped but unmapped, for
    // 003 to report as a permanent fault with an incident ID.
    return fmt.Errorf("%s: %w", op, err)
}
```

### Example 3: `CreateOrRecover` After a Delete-Then-Recreate, as an AWS Call Trace

```text
A tenant deletes SnowflakeAccount "analytics" and recreates it under the same
metadata.name in the same namespace, so 003 builds a byte-identical path:
snowflake/tenant/my_org/finance/analytics/platform-credentials

003: CreateOrRecover(ctx, backend, path, freshCredentials)   // freshCredentials from NewCredentials("platform")
 |
 |-- Backend.Create(path, freshKeypair)
 |     -> CreateSecret{Name: path, SecretBinary: ..., KmsKeyId: "alias/yukimi-secrets"}
 |        <- ResourceExistsException           // ambiguous: live, or scheduled for deletion?
 |     -> DescribeSecret{SecretId: path}       // disambiguation, not detection
 |        <- 200 { DeletedDate: 2026-08-12T09:14:02Z }
 |     => ErrPendingDeletion                   // NOT ErrAlreadyExists — this fork decides
 |                                             // whether the next step purges or reuses
 |-- Backend.Purge(path)                       // 003 discards the retired account's key
 |     -> DeleteSecret{SecretId: path, ForceDeleteWithoutRecovery: true}
 |        <- 200
 |     -> DescribeSecret{SecretId: path}       // confirmation poll: force-delete is async
 |        <- ResourceNotFoundException         // name is free; usually the first poll
 |     => nil
 |
 |-- Backend.Create(path, freshKeypair)        // 003 SC-010 permits exactly ONE retry here.
 |     -> CreateSecret{...}                    // Without the poll above, this would
 |        <- 200                               // intermittently return ResourceExistsException
 |     => nil                                  // and surface as a spurious ErrAlreadyExists
 |                                             // on a brand-new tenant.
 => (freshCredentials, existed=false, nil)

The same first call against a LIVE secret differs in one field — DeletedDate is
nil — and 003 takes the other branch: Get the existing value and report
existed=true, never purging and never overwriting. That single field is the
whole reason DescribeSecret is called.
```
