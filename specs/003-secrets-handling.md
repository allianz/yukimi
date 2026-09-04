# Specification: Secrets Handling (003)

## Overview

`internal/secrets/` stores and retrieves the credentials the platform uses to log in to Snowflake: the org-admin credential that creates accounts, and one per-tenant credential for each account the platform manages afterwards. All of them are RSA keypairs belonging to service users, held in a cloud secret manager.

This package is the only place in the codebase that reaches that secret manager. It generates the keypairs, decides the path each one is stored at, and caches values briefly so the same credential is not re-fetched on every reconcile. Those paths are what isolates one tenant from another (design.md 3.11.1), so this package constructs and validates them itself instead of trusting callers. The secret manager itself is pluggable: this spec defines only the `Backend` interface and the behavior every implementation owes its callers, with AWS Secrets Manager as the first implementation (003.a).

## Scope

This specification defines the `internal/secrets/` package that:
- Defines the `Backend` interface — a string-valued keystore — and the per-method success and failure conditions every implementation owes its callers.
- Constructs and validates the two secret paths design.md 3.11.1 requires: the tenant `platform` credential path and the org-admin credential path.
- Generates RSA keypairs and defines the JSON shape credentials are stored in.
- Wraps any `Backend` in an in-memory, TTL-based, lazily-evicted cache.
- Exports an in-memory fake `Backend`, with injectable per-method failures, for every other package to test against.
- Classifies every failure this package can produce into a user or system error per 001's model.

**Out of Scope**:
- Any concrete store or vendor SDK. `go.mod` gains no AWS dependency from this spec — that is `003.a-aws-secrets-backend.md`.
- Constructing or selecting a `Backend`. That is `cmd/provider/main.go`'s job, switching on `Config.CloudProvider()` (002).
- A singleton or `Initialize`/`GetInstance` access pattern. Every function takes a `Backend` explicitly; `main.go` owns the only instance.
- Reconciling an occupied path against the world outside the store — a credential whose Snowflake account was never created, or one inherited from a deleted account whose name a new one reuses. `Create` reports the collision and stops; this package cannot see the account behind a path.
- Credential rotation, including pushing a rotated public key into Snowflake (`ALTER USER ... SET RSA_PUBLIC_KEY`). That is the connection pool's job (004), calling this package's key generation and `Backend.Update` directly rather than a rotation primitive here.
- A `HealthCheck` method.
- Validating `PrivateKey`/`PublicKey` contents beyond non-emptiness — whether a key actually parses is the first consumer's (004's) problem.

## Key Concept: The `Backend` Interface and the Path Grammar

A `Backend` sees paths and opaque value strings, nothing else. It never parses a credential, never caches, and never logs — it returns a plainly worded error naming the path it failed on. Its four methods are the narrow set any keystore can implement: `Get`, `Create` (fails if occupied), `Update` (fails if absent), `Delete`. `Create` and `Update` are separate rather than one upsert because create-if-absent must be **atomic in the store**: a retried request must never overwrite the key a live account authenticates with. `Get` additionally returns the time the store last wrote that value, as a second return value rather than a field inside the value — the backend still never looks inside.

Paths are an opaque `Path` type, constructible only through the two constructors below, so an unvalidated path can never reach a store:

- **Tenant path** (design.md 3.11.1): `snowflake/tenant/<org>/<namespace>/<accountName>/platform-credentials`. `<namespace>` comes from the runtime `metadata.namespace`, never a spec field; `<accountName>` is the CRD's `metadata.name`, **never** the resolved hash-suffixed account name (design.md 3.12). Every segment is a Kubernetes identifier — that is what makes the namespace the trust anchor design.md 3.11.1 requires.
- **Org-admin path**: `snowflake/org/<org>/<orgAdminAccount>/org-admin-credentials`, from `Config.Snowflake` (002) resolved at the call site. This package takes plain strings and does not import `internal/config/base`.

**Important**: both constructors re-validate every segment regardless of upstream validation — reject an empty segment, or one containing `/`, `.`, `..`, or a character outside `[A-Za-z0-9_-]` — so the isolation guarantee holds even on a flat key-value store with no hierarchical authorization of its own.

## Key Concept: Credential Shape and Key Generation

A stored credential is a `Credentials` value with exactly three JSON fields: `username`, `public_key`, `private_key`. There is deliberately no `account` field — the path already identifies the account, and a duplicate would only drift.

The encodings are chosen so no consumer transforms them: `PublicKey` is PKIX, single-line base64 with no PEM delimiters, dropping straight into `ADMIN_RSA_PUBLIC_KEY = '<...>'` and `ALTER USER ... SET RSA_PUBLIC_KEY = '<...>'` (design.md 3.6, 3.9); `PrivateKey` is PKCS#8, PEM-wrapped, for the Snowflake driver's JWT signing. Generation uses `crypto/rand`, minimum 2048-bit RSA. `Username` is caller-supplied — design.md 3.6's `platform` is the account module's (012) domain knowledge, not a literal here.

`RotatedAt` is in-memory only, never persisted: `UnmarshalCredentials` takes it as a parameter — ordinarily whatever `Get` returned alongside the value — so the store never holds a second copy of the same fact.

## Key Concept: The Cache Is a `Backend`, Not a Manager

`NewCachedBackend(b Backend, ttl)` decorates any `Backend` and implements `Backend` itself — no manager type, no package-level state. Whatever `main.go` constructs is wrapped exactly once, so every backend inherits identical freshness semantics with no cache logic of its own.

`Get` serves a cached value and its timestamp within `ttl` without touching the backend; a miss — including an expired entry, evicted lazily with no background goroutine — fetches and populates. Two rules keep the cache racing toward "cold," never "stale": a failed `Get` is never cached, so a `Create` landing after a failed lookup is not masked by a negative result; and `Create`/`Update`/`Delete` write through and then *invalidate* the entry rather than pre-populating it. `Invalidate(path)` is also exposed directly.

## Public API

```go
package secrets

import (
    "context"
    "time"

    "github.com/allianz/yukimi/internal/errors"
)

// Backend is a string-valued keystore. It never parses a credential, never
// caches, and never logs — every method reports failure as an ordinary error
// whose message names the path it failed on, and no caller branches on an
// error's identity. How the value string is persisted is each implementation's
// own choice.
type Backend interface {
    // Get returns the value stored at path, along with the time the backend
    // last wrote that value — creation time if never overwritten,
    // modification time otherwise. It fails if nothing is stored there, and
    // it fails if the store cannot be read; the returned time is the zero
    // value on error.
    Get(ctx context.Context, path Path) (string, time.Time, error)

    // Create stores value at path. It fails if path is already occupied, and
    // leaves the occupying value untouched when it does — this is the
    // atomicity 012 depends on to never silently overwrite a live account's
    // credential on a retried request.
    Create(ctx context.Context, path Path, value string) error

    // Update overwrites the value already stored at path. It fails if nothing
    // is stored there — Update never creates.
    Update(ctx context.Context, path Path, value string) error

    // Delete removes path. Whether the value is gone immediately or sits in a
    // recovery window first is the implementation's business; nothing in this
    // package reads a deleted path afterwards.
    Delete(ctx context.Context, path Path) error
}

// Path is an opaque, pre-validated secret path. The zero value is not valid;
// only NewTenantPath and NewOrgAdminPath produce one.
type Path struct{ /* unexported */ }

// NewTenantPath builds the tenant platform-credential path (design.md 3.11.1):
// snowflake/tenant/<org>/<namespace>/<accountName>/platform-credentials.
//
// Parameters:
//   - org: Snowflake organization name (Config.Snowflake.Org, 002)
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
//   - org: Config.Snowflake.Org (002)
//   - orgAdminAccount: Config.Snowflake.OrgAdminAccount (002)
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
    Username   string    `json:"username"`
    PublicKey  string    `json:"public_key"`  // PKIX, single-line base64, no PEM delimiters
    PrivateKey string    `json:"private_key"` // PKCS#8, PEM-wrapped
    RotatedAt  time.Time `json:"-"`           // when this value was last written to the store; never persisted
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
// as a Credentials value for username, with RotatedAt set to time.Now().
// username is caller-supplied domain knowledge (e.g. design.md 3.6's
// "platform") — this package owns no literal.
func NewCredentials(username string) (*Credentials, error)

// MarshalCredentials and UnmarshalCredentials convert between Credentials and
// the JSON string a Backend stores. MarshalCredentials never serializes
// RotatedAt. UnmarshalCredentials rejects a value with any of the three JSON
// fields empty; it does not otherwise validate PublicKey or PrivateKey
// contents. It sets the returned Credentials' RotatedAt to rotatedAt —
// ordinarily whatever Backend.Get just returned alongside value.
func MarshalCredentials(c *Credentials) (string, error)
func UnmarshalCredentials(data string, rotatedAt time.Time) (*Credentials, error)

// CachedBackend wraps a Backend with an in-memory, TTL-based, lazily-evicted
// cache. It implements Backend itself, so callers depend on the interface,
// never on this concrete type.
type CachedBackend struct { /* unexported */ }

// NewCachedBackend wraps b. Every concrete Backend should be wrapped exactly
// once, at construction time in cmd/provider/main.go.
func NewCachedBackend(b Backend, ttl time.Duration) *CachedBackend

// Invalidate clears path's cache entry without touching the underlying
// Backend. Exposed for a caller that needs a path forced cold without going
// through Create/Update/Delete.
func (c *CachedBackend) Invalidate(path Path)

// FakeBackend is an in-memory Backend for tests, exported (not a _test.go
// file) so 004, 012, and every other consumer can depend on it without a real
// store. Each hook, if set and returning a non-nil error, short-circuits the
// call before any state mutation — this lets a test flip behavior mid-run
// (e.g. "OnCreate fails once, then is cleared") in a way a construction-time
// option cannot.
type FakeBackend struct {
    OnGet    func(path Path) error
    OnCreate func(path Path) error
    OnUpdate func(path Path) error
    OnDelete func(path Path) error

    // Clock returns the time recorded against a path on Create and Update,
    // and returned by Get. Defaults to time.Now; tests override it for a
    // deterministic RotatedAt.
    Clock func() time.Time
}

// NewFakeBackend returns an empty FakeBackend. Delete removes the entry
// outright and is idempotent, so a Create on a deleted path succeeds and a Get
// on one fails exactly as it would on a path nothing was ever stored at.
func NewFakeBackend() *FakeBackend
```

## Project Structure

```text
internal/secrets/
├── backend.go            # Backend interface
├── path.go               # Path type, NewTenantPath, NewOrgAdminPath, validation
├── path_test.go
├── credentials.go        # Credentials, GenerateKeyPair, NewCredentials, Marshal/UnmarshalCredentials
├── credentials_test.go
├── cache.go              # CachedBackend, NewCachedBackend, Invalidate
├── cache_test.go
├── fake.go               # FakeBackend — exported, not a _test.go file (004/012 import it directly)
└── doc.go
```

`internal/secrets` must never import `internal/secrets/aws` (003.a) or any other concrete backend package — the parent defining an interface never depends on a child implementing it. The only import outside the standard library is `internal/errors` (001).

## Error Classification

**User Errors** (use `errors.NewUserError()`):
- Path validation failure: `invalid secrets path segment 'team/a': must not contain '/'`

**System Errors** (use `fmt.Errorf("context: %w", err)`):
- Nothing stored at the path a `Get` or `Update` names: `secrets: no secret stored at snowflake/tenant/my_org/finance/analytics-team-eu/platform-credentials`
- A `Create` onto an occupied path: `secrets: a secret already exists at snowflake/tenant/my_org/finance/analytics-team-eu/platform-credentials`
- Any other store fault — access denied, throttling, a request timeout, a connection failure, or a vendor condition this package has no opinion about: `failed to read secret at <path>: %w`
- Key generation failure: `failed to generate RSA key pair: %w`
- Malformed stored JSON: `failed to unmarshal credentials: %w`

## Edge Cases

- **What happens if `Create` finds a credential already stored at the path?** - It fails, and the stored value is left exactly as it was. This package never reuses, overwrites, or discards what it finds there: it cannot see whether the stored credential belongs to a live Snowflake account, and either guess is destructive — overwriting locks the platform out of an account it still manages, reusing hands a new account its predecessor's key. Clearing a path that is genuinely stale is an operator action.
- **What happens if two controller replicas race to `Create` the same path?** - One wins outright. The other's `Create` fails on the now-occupied path, which surfaces as a system error with an incident ID (001) rather than being reconciled away, because from inside this package that loss is indistinguishable from any other occupied path.
- **Why is a missing credential a system error rather than a user error, when path validation failures are user errors?** - A malformed path segment is fixed by editing the CRD or config value that produced it — that is what makes it a user error. A well-formed path with nothing stored at it is not fixable that way: there is no CRD field a tenant edits to make a credential appear, and for the org-admin path there is no owning CRD at all. Whether the missing credential reflects a controller sequencing bug (a `Get` running ahead of the `Create` that should have provisioned it), an unexpected deletion, or ops never having provisioned an org-admin credential, all three need operator visibility — an incident ID, not a silent Debug-level message — so a `Get` or `Update` that finds nothing stored is classified as a system error regardless of which path type it came from.
- **What happens to a tenant secret after `DROP ACCOUNT` (012)?** - `Backend.Delete` is called on the tenant path. What that leaves behind is the concrete backend's business — an outright removal on a store with no recovery concept, a value inside a recovery window on one that has. This package makes no guarantee about which, and nothing in it reads a deleted path afterwards.
- **What if `UnmarshalCredentials` receives well-formed JSON but a truncated or otherwise invalid PEM private key?** - Out of scope for this package's validation. `UnmarshalCredentials` checks only that the three fields are non-empty strings; whether `PrivateKey` parses as an actual RSA key is the first consumer's (the connection pool, 004) problem to detect when it tries to use it.
- **What happens if a cache entry expires while a request is in flight?** - Lazy eviction: the next `Get` after expiry is a plain cache miss. It fetches from the underlying `Backend` and repopulates the entry with a fresh TTL — there is no special-cased mid-flight behavior.
- **What if the underlying store is unavailable while a cached entry is still within its TTL?** - `CachedBackend.Get` returns the cached value without calling the underlying `Backend` at all. Serving a value that could be up to `ttl` stale in exchange for availability during an outage is an accepted trade-off, not a defect.
- **What does a failed `Get` return as a timestamp?** - The zero `time.Time`, alongside the error. No caller reads it, since the error already signals that the value (and its timestamp) were not obtained.
- **Where does `FakeBackend` get a timestamp from, since it has no real store to ask?** - Its own `Clock` field, defaulting to `time.Now`: `Create` and `Update` record `Clock()` against the path, and `Get` returns whatever was last recorded. Tests that need a fixed `RotatedAt` set `Clock` to a function returning a constant time.

## Dependencies

- **`internal/errors` (001)** - Used APIs: `errors.NewUserError()` - Contract: none beyond error construction; `internal/secrets` has no other internal dependency.

## Integration Points

- **`internal/secrets/aws` (003.a)** - Implements `Backend` against AWS Secrets Manager, carrying the value string as a `SecretString` and reporting AWS API failures as plainly worded errors satisfying this interface's per-method contracts - Key functions: implements `secrets.Backend` - Notes: the only place an AWS SDK enters `go.mod`; never imported by anything above 003.
- **`cmd/provider/main.go`** - Constructs the concrete `Backend` selected by `Config.CloudProvider()` (002), wraps it exactly once in `NewCachedBackend(backend, cfg.Secrets.CacheTTL)` — the TTL comes from `Config.Secrets.CacheTTL` (002), not a literal — and passes the wrapped result to every consumer below - Key functions: `secrets.NewCachedBackend()`.
- **`internal/snowflake/pool` (004)** - Reads org-admin and per-tenant credentials through the `Backend` interface, keyed by the same `(org, namespace, account)` tuple as the tenant path - Key functions: `Backend.Get()`, `UnmarshalCredentials()`, `NewOrgAdminPath()`, `NewTenantPath()` - Notes: unit tests run against `FakeBackend`, never a real store.
- **`internal/account/modules/account` (012)** - Generates a keypair and stores it with `Backend.Create` — never `Update` — before running `CREATE ACCOUNT`, using the generated public key in the SQL statement and never persisting the private key anywhere but the store; on teardown, calls `Backend.Delete` on the same tenant path once `DROP ACCOUNT` has succeeded - Key functions: `NewCredentials()`, `MarshalCredentials()`, `Backend.Create()`, `Backend.Delete()`, `NewTenantPath()`.

## Success Criteria

- **SC-001**: `Backend` has exactly four methods — `Get`, `Create`, `Update`, `Delete` — each taking a `Path`.
- **SC-002**: Every `Backend` method carries its value as a `string` — `Get` returns one (alongside a `time.Time`), `Create` and `Update` accept one; no method exposes `[]byte`.
- **SC-003**: `NewTenantPath` constructs `snowflake/tenant/<org>/<namespace>/<accountName>/platform-credentials` from exactly those four inputs.
- **SC-004**: `NewOrgAdminPath` constructs `snowflake/org/<org>/<orgAdminAccount>/org-admin-credentials`.
- **SC-005**: Both path constructors return a user error for any empty segment, or one containing `/`, `.`, `..`, or a character outside `[A-Za-z0-9_-]`.
- **SC-006**: `Path` values are constructible only via `NewTenantPath`/`NewOrgAdminPath` — no exported field or function accepts an arbitrary unvalidated string as a `Path`.
- **SC-007**: `Credentials` marshals to JSON with exactly the fields `username`, `public_key`, `private_key` — no `account` field.
- **SC-008**: `GenerateKeyPair` produces a minimum 2048-bit RSA key: PKCS#8-encoded, PEM-wrapped private key; PKIX-encoded, single-line base64 public key with no PEM delimiters.
- **SC-009**: `UnmarshalCredentials` returns an error when any of the three JSON fields is empty; on success it sets the returned `Credentials.RotatedAt` to its `rotatedAt` parameter, and `MarshalCredentials` never serializes `RotatedAt`.
- **SC-010**: `Create` on an occupied path returns an error and leaves the stored value byte-for-byte unchanged.
- **SC-012**: `CachedBackend.Get` returns a cached value and its timestamp within `ttl` without invoking the underlying `Backend`.
- **SC-013**: `CachedBackend` never caches a failed `Get` — two consecutive `Get`s on a path nothing is stored at both reach the underlying `Backend`.
- **SC-014**: `CachedBackend` invalidates a path's cache entry on every successful `Create`/`Update`/`Delete` through it, and via an explicit `Invalidate` call.
- **SC-015**: `FakeBackend`'s per-method hooks, when set and returning a non-nil error, short-circuit before any state mutation.
- **SC-016**: `FakeBackend.Delete` removes the entry outright and is idempotent: a following `Create` on that path succeeds, a following `Get` fails as it would on a path nothing was ever stored at, and a `Delete` of an absent path is not an error.
- **SC-016a**: `FakeBackend.Get` returns the timestamp its `Create` or `Update` most recently recorded for that path, taken from `Clock` (default `time.Now`).
- **SC-017**: `internal/secrets` exposes no `Initialize`/`GetInstance`-style singleton and holds no package-level mutable state.
- **SC-018**: `internal/secrets` imports `internal/errors` and no other package internal to this repository.
- **SC-019**: `internal/secrets` exposes no `HealthCheck` method.
- **SC-020**: Unit test coverage exceeds 95%, exercised entirely against `FakeBackend` — no network calls in this package's own test suite.

## Security Considerations

- **Namespace as sole trust anchor** (design.md 3.11.1): `NewTenantPath` takes `namespace` as a plain parameter and performs no Kubernetes lookup of its own — the guarantee depends entirely on every caller passing `metadata.namespace` from the runtime object, never a value read from `spec`. This package can enforce path *shape*; it cannot enforce which namespace a caller passes.
- **Non-resolved account name in the path** (design.md 3.11.1, 3.12): `accountName` in `NewTenantPath` must be the CRD's `metadata.name`, not the resolved, hash-suffixed Snowflake account name — using the resolved name would still be internally consistent but would depend on a value not derivable purely from Kubernetes identifiers, weakening the trust-anchor argument design.md makes.
- **`Create` is the only guard against overwriting a live credential**: because this package never reconciles an occupied path, a store whose `Create` is not atomic — one that silently upserts instead of failing — would let a retried request replace the key a live account authenticates with, and nothing above it would notice. Atomic create-if-absent is a hard requirement on every backend, not a nicety.
- **Plaintext in the cache is an accepted trade-off**: `CachedBackend` holds decrypted credential strings in process memory for up to `ttl`. This is acceptable under the platform's pod-isolation model (design.md 3.11) and is what makes the cache useful at all; it is not a reason to shorten `ttl` reflexively, since a shorter `ttl` only trades store round-trips for the same in-memory exposure.
- **Known accepted gap** (design.md Appendix B X1): once a tenant holds `ACCOUNTADMIN` on their account, they can re-key or drop the `platform` service user this package's credential authenticates as, locking the platform out of an account it remains responsible for. This spec does not attempt to prevent that — it is recorded here as a gap pending Snowflake Organization Policies, not something `internal/secrets` can close from the credential-storage side.
- **No credential value ever appears in a path or a log line**: `Path.String()` returns only the identifiers that make up the path (org, namespace, account, or org-admin-account) — never a `PublicKey` or `PrivateKey`. Every error message this package's own error classification defines is built from paths and fixed descriptive text, never from credential contents.

## References

- **Product design**: `specs/design.md`, §3.6 (the `platform` user and `ADMIN_RSA_PUBLIC_KEY`), §3.11 (org-admin vs. per-account access), §3.11.1 (tenant secret path, namespace as trust anchor), §3.12 (resolved vs. CRD account name), Appendix B X1 (the `platform` user re-key/drop gap).
- **Error Handling (001)**: `internal/errors/errors.go` - `NewUserError()`, used to classify path-validation and not-found failures.
- **Base Config (002)**: `internal/config/base/base.go` - `SnowflakeSettings.Org`, `SnowflakeSettings.OrgAdminAccount`, `CloudProvider()`; its own Example 1 already anticipates `secrets.Backend` and a `secretsaws.New(region)` constructor this spec's sibling (003.a) provides.

<br/><br/><br/><br/><br/>

================

## Appendix: Usage Examples

### Example 1: Provisioning Tenant Credentials Before `CREATE ACCOUNT` (Primary Use Case)

```go
// In internal/account/modules/account (012, not yet written)
import (
    "context"

    "github.com/allianz/yukimi/internal/secrets"
)

func (m *Module) provisionCredentials(ctx context.Context, backend secrets.Backend, org, namespace, accountName string) (*secrets.Credentials, error) {
    path, err := secrets.NewTenantPath(org, namespace, accountName)
    if err != nil {
        return nil, err // user error: caller passed a malformed identifier
    }

    creds, err := secrets.NewCredentials("platform") // design.md 3.6's ADMIN_NAME
    if err != nil {
        return nil, err
    }

    value, err := secrets.MarshalCredentials(creds)
    if err != nil {
        return nil, err
    }

    // Create, never Update: if something is already stored here the store says
    // so instead of overwriting a key a live account may still authenticate
    // with. That failure is a system error here — this module does not reuse
    // or replace what it finds.
    if err := backend.Create(ctx, path, value); err != nil {
        return nil, err
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

    value, rotatedAt, err := cached.Get(ctx, path) // cache hit avoids a store round-trip on every reconcile
    if err != nil {
        return nil, err // nothing stored here means ops has not provisioned this credential yet
    }

    return secrets.UnmarshalCredentials(value, rotatedAt)
}

// Wired once at startup:
// backend := secretsaws.New(cfg.AWS.Region)                     // 003.a
// cached := secrets.NewCachedBackend(backend, cfg.Secrets.CacheTTL) // TTL from Config (002)
// pool := pool.New(cached, ...)                                  // 004 depends only on secrets.Backend
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

func TestCreate_RejectsAnOccupiedPath(t *testing.T) {
    ctx := context.Background()
    backend := secrets.NewFakeBackend()

    path, _ := secrets.NewTenantPath("my_org", "finance", "analytics-team-eu")
    first, _ := secrets.NewCredentials("platform")
    value, _ := secrets.MarshalCredentials(first)

    if err := backend.Create(ctx, path, value); err != nil {
        t.Fatalf("first create: %v", err)
    }

    second, _ := secrets.NewCredentials("platform")
    other, _ := secrets.MarshalCredentials(second)
    if err := backend.Create(ctx, path, other); err == nil {
        t.Fatal("expected the second create to fail on an occupied path")
    }

    stored, _, err := backend.Get(ctx, path)
    if err != nil {
        t.Fatalf("get: %v", err)
    }
    if stored != value {
        t.Fatal("a rejected create must leave the stored value untouched")
    }
}

// errStoreUnavailable is the caller's own error value, declared in its test
// file. FakeBackend propagates a hook's error unchanged, so a test asserts on a
// value it owns rather than on anything secrets exports.
var errStoreUnavailable = errors.New("store unavailable")

func TestGet_PropagatesInjectedFailure(t *testing.T) {
    ctx := context.Background()
    backend := secrets.NewFakeBackend()
    backend.OnGet = func(path secrets.Path) error { return errStoreUnavailable }

    path, _ := secrets.NewOrgAdminPath("my_org", "my_org_admin_account")
    if _, _, err := backend.Get(ctx, path); !errors.Is(err, errStoreUnavailable) {
        t.Fatalf("got %v, want errStoreUnavailable", err)
    }
}
```
