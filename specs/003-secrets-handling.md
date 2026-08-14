# Specification: Secrets Handling (003)

## Overview

`internal/secrets/` defines a backend-agnostic interface for storing and retrieving the RSA-keypair credentials the platform uses to authenticate to Snowflake — the per-tenant `platform` service user created during account bootstrapping (design.md 3.6, Appendix B X1) and the org-admin credential used to run `CREATE ACCOUNT` itself (design.md 3.11 intro). It solves two problems: enforcing the namespace-anchored tenant isolation that design.md 3.11.1 requires regardless of which concrete secret store a deployment runs, and making the credential-provisioning sequence safe to retry despite two non-atomic seams in its callers — a `SnowflakeAccount` deleted and immediately recreated under the same name, and a controller crash between storing a keypair and running `CREATE ACCOUNT`. The technical approach is a narrow `Backend` interface implemented against no vendor in this spec, a small vocabulary of sentinel errors a backend reports through, and a handful of backend-agnostic functions — path construction, key generation, an in-memory TTL cache, and an idempotent create-or-recover operation — layered on top of that interface so every concrete backend inherits identical behavior for free.

## Scope

This specification defines the `internal/secrets/` package that:
- Defines the `Backend` interface — an opaque byte-blob keystore — and the sentinel errors a backend reports failure through.
- Constructs and validates the two secret paths design.md 3.11.1 requires: the tenant `platform` credential path and the org-admin credential path.
- Generates RSA keypairs and defines the JSON shape credentials are stored in.
- Provides `CreateOrRecover`, an idempotent operation that resolves both the delete-then-recreate collision and the interrupted-provisioning-retry collision described below, and `Rotate`, the key-replacement primitive a future credential-rotation feature will need.
- Wraps any `Backend` in an in-memory, TTL-based, lazily-evicted cache.
- Exports an in-memory fake `Backend`, with injectable per-method failures, for every other package to test against.
- Classifies every sentinel into a user or system error per 001's model.

**Out of Scope**:
- Any concrete store or vendor SDK. `go.mod` gains no AWS dependency from this spec — that is `003-a-aws-secrets-backend.md`.
- Constructing or selecting a `Backend`. That is `cmd/provider/main.go`'s job, switching on `BaseConfig.CloudProvider()` (002).
- A singleton, package-level instance, or `Initialize`/`GetInstance`-style access pattern. Every function in this package takes a `Backend` explicitly; whoever wires `main.go` owns the only instance, wrapped once in the cache decorator this spec defines.
- Pushing a rotated public key into Snowflake (`ALTER USER ... SET RSA_PUBLIC_KEY`). That needs the connection pool (004), which does not exist yet — `Rotate` only replaces what is stored, so a future rotation feature has a primitive to build on rather than a redesign to do.
- A `HealthCheck` method.
- Interpreting or validating `PrivateKey`/`PublicKey` contents beyond non-emptiness — whether a stored key actually parses as a valid RSA key is the first consumer's (004's) problem, not this package's.

## Key Concept: The `Backend` Interface and the Path Grammar

A `Backend` sees paths and bytes, nothing else. It never parses a credential, never caches, and never logs — it returns errors from a fixed vocabulary and lets 001 do the reporting. Four of its five methods are the narrow set any keystore can implement: `Get`, `Create` (fails if something is already there), `Update` (fails if nothing is there), and `Delete`. `Create` and `Update` are deliberately separate rather than one upsert: 010 must store a keypair *before* `CREATE ACCOUNT` runs, and "create, failing if occupied" has to be atomic in the store, or a retried request could silently overwrite the key a live account already authenticates with.

The fifth method, `Purge`, is this spec's own addition — the scope note this spec replaces proposed only the first four. It exists to resolve the delete-then-recreate collision below: `Delete` alone cannot unconditionally clear a path that is sitting in some backend's soft-delete recovery window, because calling `Delete` again on something already scheduled for deletion is not guaranteed to purge it. `Purge` means "remove permanently and immediately, succeeding even if there is nothing to remove." A backend with no soft-delete concept implements it as a plain, idempotent delete; 003-a will map it to AWS Secrets Manager's force-delete-without-recovery.

Paths are an opaque type, `Path`, constructible only through `NewTenantPath` and `NewOrgAdminPath`. A `Backend` implementation never receives a raw string, so an unvalidated path can never reach a store:

- **Tenant path** (design.md 3.11.1): `snowflake/tenant/<snowflake-org-name>/<kubernetes-namespace>/<snowflake-account-name>/platform-credentials`. `<snowflake-account-name>` is the CRD's `metadata.name` as the tenant wrote it — **never** the resolved, hash-suffixed Snowflake account name from design.md 3.12. `<kubernetes-namespace>` comes from the runtime `metadata.namespace`, never a spec field. Every segment is a Kubernetes identifier, which is what makes the namespace the sole trust anchor design.md 3.11.1 requires: a controller bug that constructs the wrong namespace fails at the store's own authorization layer, not merely at a client-side check.
- **Org-admin path**: `snowflake/org/<org>/<org-admin-account>/org-admin-credentials`, built from `BaseConfig.Snowflake.Org` and `BaseConfig.Snowflake.OrgAdminAccount` (002) at the call site. `internal/secrets` itself takes these as plain strings and does not import `internal/config` — the caller resolves config, this package only validates and joins.

**Important**: Both constructors re-validate every segment independently of whatever validation the caller already did — reject any empty segment, or one containing `/`, `.`, `..`, or a byte outside `[A-Za-z0-9_-]`. This has to be redone here rather than trusted from upstream, because the isolation guarantee must hold even for the weakest possible backend — a flat key-value store with no hierarchical authorization of its own — not just for one whose IAM layer would also catch a malformed path.

## Key Concept: Credential Shape and Key Generation

A stored credential is a `Credentials` value with exactly three fields — `Username`, `PublicKey`, `PrivateKey` — serialized as JSON with keys `username`, `public_key`, `private_key`. There is deliberately no `account` field: the path already identifies which account a credential belongs to, and a payload field that has to be kept in sync with the path it lives at is a place for the two to drift with no benefit. `PublicKey` is PKIX-encoded, single-line base64 with no PEM delimiters, so it drops directly into the `CREATE ACCOUNT ... ADMIN_RSA_PUBLIC_KEY = '<...>'` and `ALTER USER ... SET RSA_PUBLIC_KEY = '<...>'` statements (design.md 3.6, 3.9). `PrivateKey` is PKCS#8, PEM-wrapped, so it goes straight into the Snowflake Go driver's JWT signing path with no transformation. Key generation uses `crypto/rand`, minimum 2048-bit RSA.

`Username` is a caller-supplied string, not a literal this package owns — design.md 3.6's `ADMIN_NAME='platform'` is domain knowledge belonging to the account module (010), not to a generic secrets package. `NewCredentials(username string)` generates a keypair and fills in the username the caller passes.

## Key Concept: Recovering From a Non-Atomic Provisioning Sequence

Storing a tenant's keypair and running `CREATE ACCOUNT` are two separate steps controlled by two different systems (010's SQL execution comes after this package's `Create`), so they can never be one transaction. Two distinct situations then land on the same call — a `Create` at the tenant path that finds something already there — and need different resolutions:

- **Interrupted provisioning.** The controller stored a keypair, then crashed or was rescheduled before `CREATE ACCOUNT` ran. On the next reconcile, 010 runs the same code path again: `Create` at the same path fails with `ErrAlreadyExists`, but nothing live is at risk — the Snowflake account behind that path does not exist yet, so the safe move is to reuse the already-stored keypair rather than treat this as a foreign collision.
- **Delete-then-recreate collision.** A tenant deletes a `SnowflakeAccount` (via the 017 deletion-warrant flow, not yet written) and immediately recreates one with the identical `metadata.name` in the same namespace. Because the tenant path is built from `metadata.name` and the namespace — never the resolved, suffixed name — the new account's path is byte-for-byte identical to the deleted one's. If the backend behind `Delete` has any soft-delete window, `Create` on that path finds the old, soft-deleted secret and fails with `ErrPendingDeletion`. This is semantically a brand-new account and should get fresh key material — reusing the deleted account's old keypair for a different account incarnation is not desirable, even though it happens to share a name.

`CreateOrRecover` is the one function that resolves both, because they surface identically (a failed `Create`) and differ only in which sentinel comes back — the fork is what happens *after* detecting "something is already here," not the detection itself:

- On outright success: return the newly generated credentials.
- On `ErrPendingDeletion`: `Purge` the path, then `Create` once more with the fresh keypair. This never restores or reuses the old value — the old secret is discarded, not recovered.
- On `ErrAlreadyExists`: `Get` and return what is already stored, and report that it already existed. `CreateOrRecover` never calls `Update` here — it did not just write what it found, so it never overwrites it.

The caller (010) is the only one who knows whether the Snowflake account behind a path actually exists. `CreateOrRecover` reports whether it found something already there; 010 combines that with its own observation of Snowflake to decide whether reusing the existing credential is the interrupted-retry case (safe) or a sign of a bug (an already-live secret when 010 itself is about to run `CREATE ACCOUNT` for the first time — that should fail loudly, not proceed).

**Important**: `CreateOrRecover` never reuses stale key material across two different account incarnations sharing a path (the delete-then-recreate case), and never generates a fresh keypair when reusing the existing one is the safe, correct move (the interrupted-retry case). Those are different guarantees, and the sentinel each situation returns is exactly what lets one function tell them apart deterministically instead of guessing.

## Key Concept: The Cache Is a `Backend`, Not a Manager

The in-memory TTL cache is a decorator over any `Backend`, not a separate manager type and not package-level state: `NewCachedBackend(b Backend, ttl time.Duration)` returns a value that itself implements `Backend`. Whichever concrete backend `main.go` constructs gets wrapped exactly once, and every backend — 003-a today, any future 003-b — inherits identical freshness semantics with no cache logic of its own to get wrong or forget.

`Get` serves a cached value within `ttl` without touching the underlying backend; a miss (including an expired entry — lazy eviction, no background goroutine) fetches from the backend and populates the cache. A failed `Get` (`ErrNotFound` or anything else) is never cached, so a `Create` that lands moments after a failed lookup is never masked by a stale negative result. `Create`, `Update`, `Delete`, and `Purge` write through to the underlying backend and, on success, invalidate that path's cache entry rather than pre-populating it with the new value — the next `Get` simply misses and re-fetches. This is deliberately the cache racing toward "cold," never toward "stale." `Invalidate(path Path)` is also exposed directly, for a future rotation feature that needs a path's cache entry cleared before its write becomes externally visible through the store.

## Public API

```go
package secrets

import (
    "context"
    stderrors "errors"
    "time"

    "github.com/allianz/yukimi/internal/errors"
)

// Backend is an opaque byte-blob keystore. It never parses a credential, never
// caches, and never logs — every method reports failure using the sentinel
// errors below, wrapped with %w so callers match them with errors.Is.
type Backend interface {
    // Get returns the raw bytes stored at path.
    //
    // Returns:
    //   - ErrNotFound if nothing is stored at path
    //   - ErrDenied, ErrUnavailable, or an unclassified store fault otherwise
    Get(ctx context.Context, path Path) ([]byte, error)

    // Create stores value at path. Fails if path is already occupied — this
    // is the atomicity 010 depends on to never silently overwrite a live
    // account's credential on a retried request.
    //
    // Returns:
    //   - ErrAlreadyExists if something live is already stored at path
    //   - ErrPendingDeletion if path holds a soft-deleted value inside a
    //     backend's recovery window (backends with no soft-delete concept
    //     never return this)
    //   - ErrDenied, ErrUnavailable, or an unclassified store fault otherwise
    Create(ctx context.Context, path Path, value []byte) error

    // Update overwrites the value already stored at path. Fails if nothing
    // is there — Update never creates.
    //
    // Returns:
    //   - ErrNotFound if nothing is stored at path
    //   - ErrDenied, ErrUnavailable, or an unclassified store fault otherwise
    Update(ctx context.Context, path Path, value []byte) error

    // Delete removes path, subject to whatever recovery window (if any) the
    // backend applies. A backend with no soft-delete concept removes it
    // outright.
    //
    // Returns:
    //   - ErrDenied, ErrUnavailable, or an unclassified store fault
    Delete(ctx context.Context, path Path) error

    // Purge removes path permanently and immediately, bypassing any recovery
    // window Delete would otherwise leave it in. Idempotent: succeeds even if
    // nothing is there.
    //
    // Returns:
    //   - ErrDenied, ErrUnavailable, or an unclassified store fault
    Purge(ctx context.Context, path Path) error
}

// Sentinel errors every Backend reports through. Backends wrap the concrete
// vendor error with %w; callers match with errors.Is.
var (
    ErrNotFound        = stderrors.New("secrets: not found")
    ErrAlreadyExists   = stderrors.New("secrets: already exists")
    ErrPendingDeletion = stderrors.New("secrets: pending deletion")
    ErrDenied          = stderrors.New("secrets: access denied")
    ErrUnavailable     = stderrors.New("secrets: unavailable")
)

// Path is an opaque, pre-validated secret path. The zero value is not valid;
// only NewTenantPath and NewOrgAdminPath produce one.
type Path struct{ /* unexported */ }

// NewTenantPath builds the tenant platform-credential path (design.md 3.11.1):
// snowflake/tenant/<org>/<namespace>/<accountName>/platform-credentials.
//
// Parameters:
//   - org: Snowflake organization name (BaseConfig.Snowflake.Org, 002)
//   - namespace: Kubernetes namespace — MUST come from metadata.namespace at
//     the call site, never a spec field (design.md 3.11.1)
//   - accountName: the CRD's metadata.name — MUST NOT be the resolved,
//     hash-suffixed Snowflake account name from design.md 3.12
//
// Returns:
//   - User error if any segment is empty or contains '/', '.', '..', or a
//     character outside [A-Za-z0-9_-]
func NewTenantPath(org, namespace, accountName string) (Path, error)

// NewOrgAdminPath builds the org-admin credential path:
// snowflake/org/<org>/<orgAdminAccount>/org-admin-credentials.
//
// Parameters:
//   - org: BaseConfig.Snowflake.Org (002)
//   - orgAdminAccount: BaseConfig.Snowflake.OrgAdminAccount (002)
//
// Returns:
//   - User error under the same validation rule as NewTenantPath
func NewOrgAdminPath(org, orgAdminAccount string) (Path, error)

// String returns the path for logging. It never contains secret material —
// only the identifiers that make up the path itself.
func (p Path) String() string

// Credentials is the JSON shape a credential is stored in: exactly three
// fields, deliberately no account field (the path already identifies it).
type Credentials struct {
    Username   string `json:"username"`
    PublicKey  string `json:"public_key"`  // PKIX, single-line base64, no PEM delimiters
    PrivateKey string `json:"private_key"` // PKCS#8, PEM-wrapped
}

// GenerateKeyPair generates a fresh RSA keypair: crypto/rand, minimum 2048-bit,
// PKCS#8-encoded private key wrapped in PEM, PKIX-encoded public key as
// single-line base64 with no PEM delimiters. One function, not two, so a
// caller can never end up with two independently generated, mismatched halves.
//
// Returns:
//   - System error if key generation fails (a cryptographic/OS-level fault)
func GenerateKeyPair() (publicKeyB64, privateKeyPEM string, err error)

// NewCredentials generates a fresh keypair via GenerateKeyPair and returns it
// as a Credentials value for username. username is caller-supplied domain
// knowledge (e.g. design.md 3.6's "platform") — this package owns no literal.
func NewCredentials(username string) (*Credentials, error)

// MarshalCredentials and UnmarshalCredentials convert between Credentials and
// the JSON bytes a Backend stores. UnmarshalCredentials rejects a value with
// any of the three fields empty; it does not otherwise validate PublicKey or
// PrivateKey contents.
func MarshalCredentials(c *Credentials) ([]byte, error)
func UnmarshalCredentials(data []byte) (*Credentials, error)

// CreateOrRecover stores newCreds at path, resolving whichever already-there
// condition it can decide on its own and surfacing the one it cannot. See Key
// Concept: Recovering From a Non-Atomic Provisioning Sequence.
//
// Parameters:
//   - newCreds: freshly generated credentials the caller wants stored if
//     nothing is already there
//
// Returns:
//   - stored: newCreds on a clean create or a pending-deletion recovery;
//     the value already at path (never newCreds) when existed is true
//   - existed: true only when Create found a live ErrAlreadyExists — the
//     caller (010) must combine this with its own knowledge of whether the
//     Snowflake account exists to decide whether reuse is safe
//   - err: System error for anything Create/Purge/Get returns that this
//     function does not resolve itself (ErrDenied, ErrUnavailable, a second
//     failed Create after a Purge, or an unclassified store fault)
func CreateOrRecover(ctx context.Context, b Backend, path Path, newCreds *Credentials) (stored *Credentials, existed bool, err error)

// Rotate generates a fresh keypair and overwrites whatever is stored at path.
// Update semantics: it fails if nothing is there yet — rotation only ever
// replaces a live credential, never creates one. Pushing the new public key
// into Snowflake (ALTER USER ... SET RSA_PUBLIC_KEY) is the caller's job once
// the connection pool (004) exists; this function only makes key replacement
// available as a primitive.
//
// Returns:
//   - System error if nothing is stored at path, or if the store or key
//     generation fails
func Rotate(ctx context.Context, b Backend, path Path, username string) (*Credentials, error)

// CachedBackend wraps a Backend with an in-memory, TTL-based, lazily-evicted
// cache. It implements Backend itself, so callers depend on the interface,
// never on this concrete type.
type CachedBackend struct { /* unexported */ }

// NewCachedBackend wraps b. Every concrete Backend should be wrapped exactly
// once, at construction time in cmd/provider/main.go.
func NewCachedBackend(b Backend, ttl time.Duration) *CachedBackend

// Invalidate clears path's cache entry without touching the underlying
// Backend. Exposed for a future rotation feature that needs the cache cleared
// before its own write becomes visible through normal Create/Update/Delete/
// Purge invalidation.
func (c *CachedBackend) Invalidate(path Path)

// FakeBackend is an in-memory Backend for tests, exported (not a _test.go
// file) so 004, 010, and every other consumer can depend on it without a real
// store. Each hook, if set and returning a non-nil error, short-circuits the
// call before any state mutation — this lets a test flip behavior mid-run
// (e.g. "OnCreate fails once, then is cleared") in a way a construction-time
// option cannot.
type FakeBackend struct {
    OnGet    func(path Path) error
    OnCreate func(path Path) error
    OnUpdate func(path Path) error
    OnDelete func(path Path) error
    OnPurge  func(path Path) error
}

// NewFakeBackend returns an empty FakeBackend. Delete marks an entry
// pending-deletion rather than removing it, so a subsequent Create or Get
// against that path returns ErrPendingDeletion until Purge — mirroring the
// state machine 003-a implements against a real backend's recovery window.
func NewFakeBackend() *FakeBackend
```

**Important**: This package needs both the standard library's `errors` (for the sentinel `var`s and `errors.Is`) and `internal/errors` (for `errors.NewUserError`) in the same files. The standard library import is aliased `stderrors`; `internal/errors` keeps the plain `errors` name, matching how the rest of the codebase already refers to it.

## Project Structure

```text
internal/secrets/
├── backend.go            # Backend interface, sentinel errors
├── path.go               # Path type, NewTenantPath, NewOrgAdminPath, validation
├── path_test.go
├── credentials.go        # Credentials, GenerateKeyPair, NewCredentials, Marshal/UnmarshalCredentials
├── credentials_test.go
├── provision.go          # CreateOrRecover, Rotate
├── provision_test.go
├── cache.go              # CachedBackend, NewCachedBackend, Invalidate
├── cache_test.go
├── fake.go                # FakeBackend — exported, not a _test.go file (004/010 import it directly)
└── doc.go
```

`internal/secrets` must never import `internal/secrets/aws` (003-a) or any other concrete backend package — the parent defining an interface never depends on a child implementing it. The only import outside the standard library is `internal/errors` (001).

## Error Classification

**User Errors** (use `errors.NewUserError()`):
- Path validation failure: `invalid secrets path segment 'team/a': must not contain '/'`

**System Errors** (use `fmt.Errorf("context: %w", err)`):
- `ErrNotFound` on a `Get` or `Update`: `no credentials found at snowflake/tenant/my_org/finance/analytics-team-eu/platform-credentials`
- `ErrDenied`: `failed to read secret: access denied`
- `ErrUnavailable`: `failed to read secret: request timed out`
- `ErrPendingDeletion` escaping `CreateOrRecover` (a second `Create` failure after `Purge`, or a bare `Create`/`Update`/`Get` call made outside `CreateOrRecover`): `failed to create secret: pending deletion`
- `ErrAlreadyExists` escaping a bare `Create` call made outside `CreateOrRecover`: `failed to create secret: already exists`
- Key generation failure: `failed to generate RSA key pair: %w`
- Malformed stored JSON: `failed to unmarshal credentials: %w`
- Any unclassified backend fault

## Edge Cases

- **What happens if two controller replicas race to call `CreateOrRecover` on the same path?** - One wins the `Create` outright. The other's `Create` returns `ErrAlreadyExists`; it falls into the `existed` branch, `Get`s the winner's just-written credentials, and reports `existed=true`. No conflict, no error surfaced to either caller.
- **What happens if `Purge` runs against a path another `CreateOrRecover` call already purged and recreated a moment earlier?** - `Purge` is defined as idempotent — succeeding even with nothing to remove — so this is never an error by itself. The immediately following `Create` then decides the outcome: if the other caller's `Create` already landed, this one sees `ErrAlreadyExists` and recovers those credentials instead.
- **Why is `ErrNotFound` a system error rather than a user error, when path validation failures are user errors?** - A malformed path segment is fixed by editing the CRD or config value that produced it — that is what makes it a user error. A well-formed path with nothing stored at it is not fixable that way: there is no CRD field a tenant edits to make a credential appear, and for the org-admin path there is no owning CRD at all. Whether the missing credential reflects a controller sequencing bug (a `Get` running ahead of the `CreateOrRecover` that should have provisioned it), an unexpected deletion, or ops never having provisioned an org-admin credential, all three need operator visibility — an incident ID, not a silent Debug-level message — so `ErrNotFound` is classified uniformly as a system error regardless of which path type it came from.
- **What happens to a tenant secret after `DROP ACCOUNT` (017, not yet written) on a backend with no soft-delete concept at all?** - `Delete` removes it outright. A same-name recreate's `CreateOrRecover` then succeeds on the very first `Create` — `ErrPendingDeletion` is never seen, and `Purge` is never invoked, because nothing was ever left in a recovery window to purge.
- **Can `Rotate` be called on a path nothing has ever been stored at?** - No. `Rotate` uses `Update` semantics; calling it on an empty path returns the store's not-found condition as a system error, since rotating a credential that was never provisioned is a caller bug, not a recoverable state.
- **What if `UnmarshalCredentials` receives well-formed JSON but a truncated or otherwise invalid PEM private key?** - Out of scope for this package's validation. `UnmarshalCredentials` checks only that the three fields are non-empty strings; whether `PrivateKey` parses as an actual RSA key is the first consumer's (the connection pool, 004) problem to detect when it tries to use it.
- **What happens if a cache entry expires while a request is in flight?** - Lazy eviction: the next `Get` after expiry is a plain cache miss. It fetches from the underlying `Backend` and repopulates the entry with a fresh TTL — there is no special-cased mid-flight behavior.
- **What if the underlying store is unavailable while a cached entry is still within its TTL?** - `CachedBackend.Get` returns the cached value without calling the underlying `Backend` at all. Serving a value that could be up to `ttl` stale in exchange for availability during an outage is an accepted trade-off, not a defect.

## Dependencies

- **`internal/errors` (001)** - Used APIs: `errors.NewUserError()` - Contract: none beyond error construction; `internal/secrets` has no other internal dependency.

## Integration Points

- **`internal/secrets/aws` (003-a)** - Implements `Backend` against AWS Secrets Manager; maps AWS API errors to this package's sentinels and `Purge` to force-delete-without-recovery - Key functions: implements `secrets.Backend` - Notes: the only place an AWS SDK enters `go.mod`; never imported by anything above 003.
- **`cmd/provider/main.go`** - Constructs the concrete `Backend` selected by `BaseConfig.CloudProvider()` (002), wraps it exactly once in `NewCachedBackend`, and passes the wrapped result to every consumer below - Key functions: `secrets.NewCachedBackend()`.
- **`internal/snowflake/pool` (004)** - Reads org-admin and per-tenant credentials through the `Backend` interface, keyed by the same `(org, namespace, account)` tuple as the tenant path - Key functions: `Backend.Get()`, `UnmarshalCredentials()`, `NewOrgAdminPath()`, `NewTenantPath()` - Notes: unit tests run against `FakeBackend`, never a real store.
- **`internal/account/modules/account` (010)** - Generates a keypair and calls `CreateOrRecover` before running `CREATE ACCOUNT`, using the returned public key in the SQL statement and never persisting the private key anywhere but the store - Key functions: `NewCredentials()`, `CreateOrRecover()`, `NewTenantPath()`.
- **`internal/deletion` (017, not yet written)** - Calls `Backend.Delete()` on the tenant path when `DROP ACCOUNT` executes, leaving whatever recovery window the concrete backend applies — the exact window `CreateOrRecover` is built to resolve on a same-name recreate - Key functions: `Backend.Delete()`.

## Success Criteria

- **SC-001**: `Backend` has exactly five methods — `Get`, `Create`, `Update`, `Delete`, `Purge` — each taking a `Path`.
- **SC-002**: `NewTenantPath` constructs `snowflake/tenant/<org>/<namespace>/<accountName>/platform-credentials` from exactly those four inputs.
- **SC-003**: `NewOrgAdminPath` constructs `snowflake/org/<org>/<orgAdminAccount>/org-admin-credentials`.
- **SC-004**: Both path constructors return a user error for any empty segment, or one containing `/`, `.`, `..`, or a character outside `[A-Za-z0-9_-]`.
- **SC-005**: `Path` values are constructible only via `NewTenantPath`/`NewOrgAdminPath` — no exported field or function accepts an arbitrary unvalidated string as a `Path`.
- **SC-006**: `Credentials` marshals to JSON with exactly the fields `username`, `public_key`, `private_key` — no `account` field.
- **SC-007**: `GenerateKeyPair` produces a minimum 2048-bit RSA key: PKCS#8-encoded, PEM-wrapped private key; PKIX-encoded, single-line base64 public key with no PEM delimiters.
- **SC-008**: `UnmarshalCredentials` returns an error when any of the three JSON fields is empty.
- **SC-009**: `CreateOrRecover` returns `(newCreds, false, nil)` when the initial `Create` succeeds outright.
- **SC-010**: `CreateOrRecover`, on `ErrPendingDeletion`, purges then creates, returning `(newCreds, false, nil)` on success — never returning the value that was purged.
- **SC-011**: `CreateOrRecover`, on `ErrAlreadyExists`, returns `(existingCreds, true, nil)` from a `Get` of the existing value, and never calls `Update`.
- **SC-012**: `Rotate` returns a system error when nothing exists yet at `path`, and otherwise overwrites the stored value with a freshly generated `Credentials`.
- **SC-013**: `CachedBackend.Get` returns a cached value within `ttl` without invoking the underlying `Backend`.
- **SC-014**: `CachedBackend` never caches an `ErrNotFound` result.
- **SC-015**: `CachedBackend` invalidates a path's cache entry on every successful `Create`/`Update`/`Delete`/`Purge` through it, and via an explicit `Invalidate` call.
- **SC-016**: `FakeBackend`'s per-method hooks, when set and returning a non-nil error, short-circuit before any state mutation.
- **SC-017**: `FakeBackend.Delete` marks an entry pending-deletion rather than removing it; a subsequent `Create` or `Get` on that path returns `ErrPendingDeletion` until `Purge` is called.
- **SC-018**: `internal/secrets` exposes no `Initialize`/`GetInstance`-style singleton and holds no package-level mutable state.
- **SC-019**: `internal/secrets` imports `internal/errors` and no other package internal to this repository.
- **SC-020**: `internal/secrets` exposes no `HealthCheck` method.
- **SC-021**: Unit test coverage exceeds 95%, exercised entirely against `FakeBackend` — no network calls in this package's own test suite.

## Security Considerations

- **Namespace as sole trust anchor** (design.md 3.11.1): `NewTenantPath` takes `namespace` as a plain parameter and performs no Kubernetes lookup of its own — the guarantee depends entirely on every caller passing `metadata.namespace` from the runtime object, never a value read from `spec`. This package can enforce path *shape*; it cannot enforce which namespace a caller passes.
- **Non-resolved account name in the path** (design.md 3.11.1, 3.12): `accountName` in `NewTenantPath` must be the CRD's `metadata.name`, not the resolved, hash-suffixed Snowflake account name — using the resolved name would still be internally consistent but would depend on a value not derivable purely from Kubernetes identifiers, weakening the trust-anchor argument design.md makes.
- **Plaintext in the cache is an accepted trade-off**: `CachedBackend` holds decrypted `Credentials` bytes in process memory for up to `ttl`. This is acceptable under the platform's pod-isolation model (design.md 3.11) and is what makes the cache useful at all; it is not a reason to shorten `ttl` reflexively, since a shorter `ttl` only trades store round-trips for the same in-memory exposure.
- **Known accepted gap** (design.md Appendix B X1): once a tenant holds `ACCOUNTADMIN` on their account, they can re-key or drop the `platform` service user this package's credential authenticates as, locking the platform out of an account it remains responsible for. This spec does not attempt to prevent that — it is recorded here as a gap pending Snowflake Organization Policies, not something `internal/secrets` can close from the credential-storage side.
- **No credential value ever appears in a path or a log line**: `Path.String()` returns only the identifiers that make up the path (org, namespace, account, or org-admin-account) — never a `PublicKey` or `PrivateKey`. Every error message this package's own error classification defines is built from paths and sentinel descriptions, never from credential contents.

## References

- **Product design**: `specs/design.md`, §3.6 (the `platform` user and `ADMIN_RSA_PUBLIC_KEY`), §3.11 (org-admin vs. per-account access), §3.11.1 (tenant secret path, namespace as trust anchor), §3.12 (resolved vs. CRD account name), Appendix B X1 (the `platform` user re-key/drop gap).
- **Error Handling (001)**: `internal/errors/errors.go` - `NewUserError()`, used to classify path-validation and not-found failures.
- **Base Config (002)**: `internal/config/config.go` - `SnowflakeSettings.Org`, `SnowflakeSettings.OrgAdminAccount`, `CloudProvider()`; its own Example 1 already anticipates `secrets.Backend` and a `secretsaws.New(region)` constructor this spec's sibling (003-a) provides.

<br/><br/><br/><br/><br/>

================

## Appendix: Usage Examples

### Example 1: Provisioning Tenant Credentials Before `CREATE ACCOUNT` (Primary Use Case)

```go
// In internal/account/modules/account (010, not yet written)
import (
    "context"
    "fmt"

    "github.com/allianz/yukimi/internal/secrets"
)

func (m *Module) provisionCredentials(ctx context.Context, backend secrets.Backend, org, namespace, accountName string, accountAlreadyExistsInSnowflake bool) (*secrets.Credentials, error) {
    path, err := secrets.NewTenantPath(org, namespace, accountName)
    if err != nil {
        return nil, err // user error: caller passed a malformed identifier
    }

    fresh, err := secrets.NewCredentials("platform") // design.md 3.6's ADMIN_NAME
    if err != nil {
        return nil, err
    }

    creds, existed, err := secrets.CreateOrRecover(ctx, backend, path, fresh)
    if err != nil {
        return nil, err
    }
    if existed {
        if accountAlreadyExistsInSnowflake {
            // A live secret at this path with no CREATE ACCOUNT yet to follow
            // is a bug, not a recoverable state — 010 never overwrites it.
            return nil, fmt.Errorf("tenant secret already stored at %s but account %s is not yet created", path, accountName)
        }
        // Interrupted-retry case: reuse the credential from the prior attempt.
    }

    // creds.PublicKey now goes into CREATE ACCOUNT ... ADMIN_RSA_PUBLIC_KEY = '<creds.PublicKey>'
    return creds, nil
}
```

### Example 2: Reading Org-Admin Credentials Through the Cache

```go
// In internal/snowflake/pool (004, not yet written)
import (
    "context"

    "github.com/allianz/yukimi/internal/secrets"
)

func (p *Pool) orgAdminCredentials(ctx context.Context, cached secrets.Backend, org, orgAdminAccount string) (*secrets.Credentials, error) {
    path, err := secrets.NewOrgAdminPath(org, orgAdminAccount)
    if err != nil {
        return nil, err
    }

    raw, err := cached.Get(ctx, path) // cache hit avoids a store round-trip on every reconcile
    if err != nil {
        return nil, err // ErrNotFound here means ops has not provisioned this credential yet
    }

    return secrets.UnmarshalCredentials(raw)
}

// Wired once at startup:
// backend := secretsaws.New(cfg.AWS.Region)       // 003-a
// cached := secrets.NewCachedBackend(backend, 5*time.Minute)
// pool := pool.New(cached, ...)                    // 004 depends only on secrets.Backend
```

### Example 3: Testing Against `FakeBackend`

```go
// In a caller's own _test.go file — 003 ships FakeBackend so no test anywhere
// outside internal/secrets needs a real store or the AWS SDK.
import (
    "context"
    "errors"
    "testing"

    "github.com/allianz/yukimi/internal/secrets"
)

func TestCreateOrRecover_RecoversFromInterruptedRetry(t *testing.T) {
    ctx := context.Background()
    backend := secrets.NewFakeBackend()

    path, _ := secrets.NewTenantPath("my_org", "finance", "analytics-team-eu")
    first, _ := secrets.NewCredentials("platform")

    stored, existed, err := secrets.CreateOrRecover(ctx, backend, path, first)
    if err != nil || existed {
        t.Fatalf("first call: got existed=%v err=%v, want existed=false err=nil", existed, err)
    }

    second, _ := secrets.NewCredentials("platform")
    recovered, existed, err := secrets.CreateOrRecover(ctx, backend, path, second)
    if err != nil || !existed {
        t.Fatalf("retry: got existed=%v err=%v, want existed=true err=nil", existed, err)
    }
    if recovered.PrivateKey != stored.PrivateKey {
        t.Fatal("retry must recover the first attempt's keypair, not overwrite it")
    }
}

func TestGet_PropagatesInjectedFailure(t *testing.T) {
    ctx := context.Background()
    backend := secrets.NewFakeBackend()
    backend.OnGet = func(path secrets.Path) error { return secrets.ErrUnavailable }

    path, _ := secrets.NewOrgAdminPath("my_org", "my_org_admin_account")
    if _, err := backend.Get(ctx, path); !errors.Is(err, secrets.ErrUnavailable) {
        t.Fatalf("got %v, want ErrUnavailable", err)
    }
}
```
