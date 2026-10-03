# Specification: Secrets Handling (003)

This specification covers the package: `internal/secrets/`.

## Overview

The platform uses an organization-admin credential to create Snowflake accounts, then a separate credential to manage each account. Operations within an account use its own credential without requiring highly privileged organization-wide access. The platform stores these RSA credentials in a secret manager and retrieves them when it connects to Snowflake. A common storage contract supports different backends, starting with AWS Secrets Manager.

OIDC can later let the platform reach an account without a stored secret (design.md 3.11.2), but Snowflake needs a keypair to create an account, so every account starts with one and relies on it until OIDC takes over.

## Key Concept: Credentials Are Keypairs

Each credential contains a service username and an RSA public and private key. Snowflake receives the public key, while the platform keeps the private key in the secret manager for authentication. 

```json
{
  "username": "platform",
  "public_key": "MIIBIjANBgkq…",
  "private_key": "-----BEGIN PRIVATE KEY-----\nMIIEvQIB…\n-----END PRIVATE KEY-----\n"
}
```

## Key Concept: A Common Contract for Secret Backends

The platform defines a common contract that every secret-storage backend must fulfill. Each backend stores and retrieves credentials under the same rules, so the rest of the platform does not depend on a particular store. AWS Secrets Manager is the first implementation; Azure, GCP, and Kubernetes backends are also planned.

```mermaid
flowchart LR
    C[Consumers] --> KM[KeyManager]
    KM --> AWS[AWS KeyStore]
    KM -.-> AZ[Azure KeyStore]
    KM -.-> GCP[GCP KeyStore]
    KM -.-> K8S[Kubernetes KeyStore]
```

## Key Concept: Tenant Isolation Through Secret Identifiers

Each tenant has its own Kubernetes namespace, which anchors access to its Snowflake account credentials. The platform stores each credential under `yk-<org>--<namespace>--<accountName>`, taking the namespace from the account resource's actual location rather than a tenant-supplied setting. A tenant cannot reach a secret under another tenant's namespace, so it cannot use the platform to access that tenant's Snowflake account, even if both accounts have the same name.

| Namespace (set by Kubernetes) | Account name (chosen by tenant) | Secret identifier |
|---|---|---|
| `team-a` | `analytics` | `yk-my-org--team-a--analytics` |
| `team-b` | `analytics` | `yk-my-org--team-b--analytics` |

## Key Concept: One Identifier Shape for Every Store

The identifier is used as the key name in every secret store. Azure Key Vault is the most restrictive: at most 127 characters, and `-` is the only special character allowed. Parts are separated by `--` to avoid collisions, so a part itself must never contain `--`; this is validated.

## Key Concept: Recover Accounts and Credentials Together

Snowflake keeps a deleted account recoverable for a grace period, and its credential should remain recoverable with it. The secret's recovery window follows the account's grace period without outlasting it, so a restored account can use its credential while both remain recoverable. Snowflake reserves the account name throughout its grace period, preventing a new account with the same name; the secret identifier also stays reserved while its credential is recoverable.

```mermaid
stateDiagram-v2
    direction LR
    [*] --> Free
    Free --> Stored: credential created
    Stored --> Stored: credential rotated
    Stored --> Recoverable: deleted
    Recoverable --> Stored: restored 
    Recoverable --> Free: recovery window ends
```

A stored credential can be read and rotated but not created again. A recoverable one can be neither read nor created again until it is restored or its window ends.

## Key Concept: Short-Lived Credential Cache

The platform caches credentials briefly to avoid fetching them from the secret manager on every read. A failed read is never cached, and creating, changing, or deleting a credential clears its cached copy. Only reads fill the cache. After the cache expires, the next read fetches the credential from the secret manager again; there is no background cleanup.

## Public API

```go
package secrets

import (
    "context"
    "errors"
    "time"

    yukimierrors "github.com/allianz/yukimi/internal/errors"
)

// ErrPendingDeletion marks a Create failure on an identifier occupied by a
// secret scheduled for deletion. The failure is still a system error; a
// caller with more context may detect it via errors.Is.
var ErrPendingDeletion = errors.New("secrets: identifier pending deletion")

// KeyStore is a string-valued keystore. It never parses, caches, or logs;
// every error names the identifier it failed on.
type KeyStore interface {
    // Get returns the value at id and the time the backend last wrote it.
    // It fails if nothing is stored there or the store cannot be read.
    Get(ctx context.Context, id Identifier) (string, time.Time, error)

    // Create stores value at id. If id is already occupied it fails and
    // leaves the existing value untouched; if the occupant is scheduled for
    // deletion, the error also wraps ErrPendingDeletion.
    Create(ctx context.Context, id Identifier, value string) error

    // Update overwrites the value at id. It fails if nothing is stored there.
    Update(ctx context.Context, id Identifier, value string) error

    // Delete schedules the removal of id, never deleting it immediately, with
    // a recovery window within the account grace period it was constructed
    // with (002). While pending, id stays occupied and Get, Create, and Update
    // all fail on it.
    Delete(ctx context.Context, id Identifier) error
}

// Identifier is an opaque, pre-validated secret identifier. The zero value is
// not valid; only NewTenantIdentifier and NewOrgAdminIdentifier produce one.
type Identifier struct{ /* unexported */ }

// NewTenantIdentifier builds yk-<org>--<namespace>--<accountName>
// (design.md 3.11.1). namespace MUST come from metadata.namespace, never a
// spec field; accountName is the CRD's metadata.name, NOT the hash-suffixed
// Snowflake account name (design.md 3.12).
//
// Returns a user error if any segment is empty, starts with '-'/'_', or
// contains '/', '.', any of "--", "__", "-_", "_-", or a character outside
// [A-Za-z0-9_-]; a system error if the result exceeds 127 characters (an
// upstream invariant drift, see Error Classification). "__", "-_" and "_-"
// are rejected because '_' becomes '-'. Segments are re-validated even if
// the caller already did, so isolation never depends on upstream checks.
func NewTenantIdentifier(org, namespace, accountName string) (Identifier, error)

// NewOrgAdminIdentifier builds yk-orgadmin--<org>--<orgAdminAccount> from
// Config.Snowflake (002). Errors as for NewTenantIdentifier.
func NewOrgAdminIdentifier(org, orgAdminAccount string) (Identifier, error)

// String returns the identifier. It contains no secret material and is safe
// to log.
func (i Identifier) String() string

// Credentials is the stored JSON shape. It has no account field; the
// identifier already names the account.
type Credentials struct {
    Username   string    `json:"username"`
    PublicKey  string    `json:"public_key"`  // PKIX, single-line base64, no PEM delimiters: used as-is in ADMIN_RSA_PUBLIC_KEY / RSA_PUBLIC_KEY
    PrivateKey string    `json:"private_key"` // PKCS#8, PEM-wrapped: what the Snowflake driver signs JWTs with
    RotatedAt  time.Time `json:"-"`           // the store's last-written time; never persisted, so there is no second copy to drift
}

// GenerateKeyPair generates a 2048-bit RSA keypair with the encodings shown
// on Credentials. Both halves come from one call so they cannot mismatch.
// Any error is a system error.
func GenerateKeyPair() (publicKeyB64, privateKeyPEM string, err error)

// NewCredentials returns Credentials for username with a fresh keypair and
// RotatedAt set to now. The caller supplies username (e.g. design.md 3.6's
// "platform"); this package hard-codes none.
func NewCredentials(username string) (*Credentials, error)

// KeyManager wraps a KeyStore with an in-memory TTL cache. Consumers outside
// this package hold a *KeyManager, never a KeyStore, and can only read or
// write values as Credentials — (un)marshaling stays internal.
//
// A failed read is never cached, so a credential created right after is not
// hidden. Writes clear the entry instead of refilling it, so racing writes
// cannot leave a stale value cached.
type KeyManager struct { /* unexported */ }

// NewKeyManager wraps store; do so once, in cmd/provider/main.go.
func NewKeyManager(store KeyStore, ttl time.Duration) *KeyManager

// CreateCredentials generates a keypair for username, stores it at id
// create-only, and returns it so the caller can use the public key (e.g. in
// CREATE ACCOUNT).
func (c *KeyManager) CreateCredentials(ctx context.Context, id Identifier, username string) (*Credentials, error)

// UpdateCredentials stores caller-generated creds at id update-only — for
// 004's rotation, which pushes the new public key to Snowflake before
// persisting it.
func (c *KeyManager) UpdateCredentials(ctx context.Context, id Identifier, creds *Credentials) error

// GetCredentials reads the credential at id, rejecting one with any field
// empty; key contents are not otherwise validated. RotatedAt is the time the
// backend last wrote the value.
func (c *KeyManager) GetCredentials(ctx context.Context, id Identifier) (*Credentials, error)

// DeleteCredentials schedules the removal of the credential at id.
func (c *KeyManager) DeleteCredentials(ctx context.Context, id Identifier) error

// Invalidate drops id's cache entry without touching the KeyStore.
func (c *KeyManager) Invalidate(id Identifier)

// FakeKeyStore is an in-memory KeyStore for tests in any package. A hook
// returning a non-nil error fails the call before any state change; hooks
// can be set or cleared mid-test.
//
// Unlike a real store, it deletes immediately by default as a test
// convenience; set SchedulesDeletion to match the KeyStore contract.
type FakeKeyStore struct {
    OnGet    func(id Identifier) error
    OnCreate func(id Identifier) error
    OnUpdate func(id Identifier) error
    OnDelete func(id Identifier) error

    // Clock stamps Create and Update; Get returns the stamp. Defaults to
    // time.Now.
    Clock func() time.Time

    // SchedulesDeletion makes Delete leave the identifier occupied but
    // unreadable until Restore. By default Delete removes outright.
    SchedulesDeletion bool
}

// Restore cancels a pending deletion at id (the store side of 012's manual
// repair). It fails if nothing at id is pending deletion.
func (f *FakeKeyStore) Restore(id Identifier) error

// NewFakeKeyStore returns an empty FakeKeyStore whose Delete is outright and
// idempotent.
func NewFakeKeyStore() *FakeKeyStore
```

## Project Structure

```text
internal/secrets/
├── keystore.go           # KeyStore interface
├── identifier.go         # Identifier type, NewTenantIdentifier, NewOrgAdminIdentifier, validation
├── identifier_test.go
├── credentials.go        # Credentials, GenerateKeyPair, NewCredentials; private marshal/unmarshal helpers
├── credentials_test.go
├── manager.go             # KeyManager, NewKeyManager, CreateCredentials, UpdateCredentials, GetCredentials, Invalidate
├── manager_test.go
├── fake.go               # FakeKeyStore — exported, not a _test.go file (004/012 import it directly)
└── doc.go
```

`internal/secrets` must never import `internal/secrets/aws` (003.a) or any other concrete store package — the parent defining an interface never depends on a child implementing it. The only import outside the standard library is `internal/errors` (001).

## Error Classification

**User Errors** (use `errors.NewUserError()`):
- Identifier validation failure: `invalid secret identifier segment 'team/a': must not contain '/'`

**System Errors** (use `fmt.Errorf("context: %w", err)`):
- Identifier exceeding the shared backend length limit: `secret identifier "yk-..." is 140 characters, which exceeds the 127-character limit shared by supported secrets-manager backends`. Every length this package can see is already bounded tightly enough elsewhere (006, 012) that this should never actually trigger — if it does, an invariant drifted out of sync upstream, which is an operator's problem to reconcile, not a caller's fixable input mistake.
- Nothing stored at the identifier a `Get` or `Update` names: `secrets: no secret stored at yk-my-org--finance--analytics-team-eu`
- A `Create` onto an occupied identifier: `secrets: a secret already exists at yk-my-org--finance--analytics-team-eu`. Also wraps `ErrPendingDeletion` when the occupying secret is scheduled for deletion rather than live (see `ErrPendingDeletion`).
- Any other store fault — access denied, throttling, a request timeout, a connection failure, or a vendor condition this package has no opinion about: `failed to read secret at <identifier>: %w`
- Key generation failure: `failed to generate RSA key pair: %w`
- Malformed stored JSON: `failed to unmarshal credentials: %w`

<br/><br/><br/><br/><br/>

================

## Appendix: Code Generation Details

## Scope

This specification defines the `internal/secrets/` package that:
- Defines the `KeyStore` interface — a string-valued keystore — and the per-method success and failure conditions every implementation owes its callers.
- Constructs and validates the two secret identifiers design.md 3.11.1 requires: the tenant `platform` credential identifier and the org-admin credential identifier.
- Generates RSA keypairs and defines the JSON shape credentials are stored in.
- Wraps any `KeyStore` in an in-memory, TTL-based, lazily-evicted cache (`KeyManager`), the type every consumer outside this package holds.
- Requires every store to keep a deleted credential recoverable as long as it can, never longer than the account grace period (002); each store decides how (003.a).
- Exports an in-memory fake `KeyStore`, with injectable per-method failures, for every other package to test against.
- Classifies every failure this package can produce into a user or system error per 001's model.

**Out of Scope**:
- Any concrete store or vendor SDK. `go.mod` gains no AWS dependency from this spec — that is `003.a-aws-secrets-backend.md`.
- Constructing or selecting a `KeyStore`. That is `cmd/provider/main.go`'s job, switching on `Config.CloudProvider()` (002).
- A singleton or `Initialize`/`GetInstance` access pattern. Every function takes a `KeyStore` or `*KeyManager` explicitly; `main.go` owns the only `KeyStore` instance.
- Reconciling an occupied identifier against the world outside the store — a credential whose Snowflake account was never created, or one inherited from a deleted account whose name a new one reuses. `Create` reports the collision and stops; this package cannot see the account behind an identifier.
- Credential rotation, including pushing a rotated public key into Snowflake (`ALTER USER ... SET RSA_PUBLIC_KEY`). That is the connection pool's job (004), calling this package's `NewCredentials` and `KeyManager.UpdateCredentials` directly rather than a rotation primitive here.
- A `HealthCheck` method.
- Validating `PrivateKey`/`PublicKey` contents beyond non-emptiness — whether a key actually parses is the first consumer's (004's) problem.

## Edge Cases

- **What happens if `Create` finds a credential already stored at the identifier?** - It fails, and the stored value is left exactly as it was. This package never reuses, overwrites, or discards what it finds there: it cannot see whether the stored credential belongs to a live Snowflake account, and either guess is destructive — overwriting locks the platform out of an account it still manages, reusing hands a new account its predecessor's key. Clearing an identifier that is genuinely stale is an operator action.
- **What happens if two controller replicas race to `Create` the same identifier?** - One wins outright. The other's `Create` fails on the now-occupied identifier, which surfaces as a system error with an incident ID (001) rather than being reconciled away, because from inside this package that loss is indistinguishable from any other occupied identifier.
- **Why is a missing credential a system error rather than a user error, when identifier validation failures are user errors?** - A malformed identifier segment is fixed by editing the CRD or config value that produced it; a well-formed identifier with nothing stored at it is not. No tenant field makes a credential appear, and the org-admin identifier has no owning CRD at all. Whether the cause is a controller sequencing bug, an unexpected deletion, or an org-admin credential ops never provisioned, all three need an incident ID rather than a Debug-level message.
- **What happens to a tenant secret after `DROP ACCOUNT` (012)?** - `KeyManager.DeleteCredentials` is called on the tenant identifier, which schedules its removal. The credential stays recoverable for the store's recovery window, never longer than the account grace period. Nothing in this package reads a deleted identifier afterwards.
- **What if a store's shortest recovery window is longer than the account grace period?** - That store cannot implement `KeyStore`: every store must be able to schedule a window within the grace period, and none deletes immediately. AWS Secrets Manager's 7-day minimum matches 002's grace-period floor (003.a).
- **What happens on a `Delete` of an identifier whose removal is already pending?** - It succeeds and changes nothing: the store scheduled the removal once and does not restart its clock, so a retried teardown neither fails nor silently extends the blockade. (AWS Secrets Manager is the exception among the operations here in not being idempotent on an *absent* identifier — see 003.a.)
- **What can be done with an identifier whose removal is pending?** - Only waiting it out or, where the store offers it, restoring. `Get` and `Update` fail because the value is not readable, and `Create` fails because the name has not been released — this is the blockade the invariant above exists to bound. `FakeKeyStore.Restore` models the restore for tests. `Create`'s failure also wraps `ErrPendingDeletion`, so a caller with more context can catch it and classify it itself.
- **What if a stored credential's JSON is well-formed but has a truncated or otherwise invalid PEM private key?** - Out of scope for this package's validation. `KeyManager.GetCredentials` checks only that the three fields are non-empty strings; whether `PrivateKey` parses as an actual RSA key is the first consumer's (the connection pool, 004) problem to detect when it tries to use it.
- **What happens if a cache entry expires while a request is in flight?** - Lazy eviction: the next `Get` after expiry is a plain cache miss. It fetches from the underlying `KeyStore` and repopulates the entry with a fresh TTL — there is no special-cased mid-flight behavior.
- **What if the underlying store is unavailable while a cached entry is still within its TTL?** - `KeyManager.GetCredentials` returns the cached value without calling the underlying `KeyStore` at all. Serving a value that could be up to `ttl` stale in exchange for availability during an outage is an accepted trade-off, not a defect.
- **What does a failed `Get` return as a timestamp?** - The zero `time.Time`, alongside the error. No caller reads it, since the error already signals that the value (and its timestamp) were not obtained.
- **Where does `FakeKeyStore` get a timestamp from, since it has no real store to ask?** - Its own `Clock` field, defaulting to `time.Now`: `Create` and `Update` record `Clock()` against the identifier, and `Get` returns whatever was last recorded. Tests that need a fixed `RotatedAt` set `Clock` to a function returning a constant time.

## Dependencies

- **`internal/errors` (001)** - Used APIs: `errors.NewUserError()` - Contract: none beyond error construction; `internal/secrets` has no other internal dependency.

## Integration Points

- **`internal/secrets/aws` (003.a)** - Implements `KeyStore` against AWS Secrets Manager, carrying the value string as a `SecretString` and reporting AWS API failures as plainly worded errors satisfying this interface's per-method contracts - Key functions: implements `secrets.KeyStore` - Notes: the only place an AWS SDK enters `go.mod`; never imported by anything above 003.
- **`cmd/provider/main.go`** - Constructs the concrete `KeyStore` selected by `Config.CloudProvider()` (002), passing it `Config.Deletion.GracePeriodDays` so it can compute its own recovery window, wraps it exactly once in `NewKeyManager(store, cfg.Secrets.CacheTTL)` — the TTL comes from `Config.Secrets.CacheTTL` (002), not a literal — and passes the wrapped result to every consumer below. Any operator-facing gap between that window and the grace period is the concrete store's own concern to log (003.a); `main.go` neither computes nor logs it - Key functions: `secrets.NewKeyManager()`.
- **`internal/snowflake/pool` (004)** - Reads org-admin and per-tenant credentials through the `*KeyManager` handed to `pool.New`, keyed by the same `(org, namespace, account)` tuple as the tenant identifier; rotates a stale credential by generating a fresh one, pushing its public key into Snowflake, and only then persisting it - Key functions: `KeyManager.GetCredentials()`, `NewCredentials()`, `KeyManager.UpdateCredentials()`, `NewOrgAdminIdentifier()`, `NewTenantIdentifier()` - Notes: unit tests run against a `FakeKeyStore` wrapped in a `KeyManager`, never a real store.
- **`internal/account/modules/account` (012)** - Generates and stores a keypair with `KeyManager.CreateCredentials` — never `Update` — before running `CREATE ACCOUNT`, using the returned `Credentials.PublicKey` in the SQL statement and never persisting the private key anywhere but the store; on teardown, calls `KeyManager.DeleteCredentials` on the same tenant identifier once `DROP ACCOUNT` has succeeded. Matches `CreateCredentials`'s error against `ErrPendingDeletion` via `errors.Is` and decides its own classification and message for that case - Key functions: `KeyManager.CreateCredentials()`, `KeyManager.DeleteCredentials()`, `NewTenantIdentifier()`, `ErrPendingDeletion`.

## Success Criteria

- **SC-001**: `KeyStore` has exactly four methods — `Get`, `Create`, `Update`, `Delete` — each taking an `Identifier`.
- **SC-002**: Every `KeyStore` method carries its value as a `string` — `Get` returns one (alongside a `time.Time`), `Create` and `Update` accept one; no method exposes `[]byte`.
- **SC-003**: `NewTenantIdentifier` constructs `yk-<org>--<namespace>--<accountName>` from exactly those three inputs, mapping `_` to `-` in each.
- **SC-004**: `NewOrgAdminIdentifier` constructs `yk-orgadmin--<org>--<orgAdminAccount>`, mapping `_` to `-` in each.
- **SC-005**: Both identifier constructors return a user error for any empty segment, one starting with `-`/`_`, one containing `/`, `.`, `..`, any of `--`, `__`, `-_`, `_-`, or a character outside `[A-Za-z0-9_-]`.
- **SC-005a**: Both identifier constructors return a system error, not a user error, for a resulting identifier longer than 127 characters — this is a last-resort invariant check, not a condition a caller's input can normally reach.
- **SC-006**: `Identifier` values are constructible only via `NewTenantIdentifier`/`NewOrgAdminIdentifier` — no exported field or function accepts an arbitrary unvalidated string as an `Identifier`.
- **SC-007**: `Credentials` marshals to JSON with exactly the fields `username`, `public_key`, `private_key` — no `account` field.
- **SC-008**: `GenerateKeyPair` produces a minimum 2048-bit RSA key from `crypto/rand`: PKCS#8-encoded, PEM-wrapped private key; PKIX-encoded, single-line base64 public key with no PEM delimiters.
- **SC-009**: `KeyManager.GetCredentials` returns an error when the stored value's JSON fields (`username`, `public_key`, `private_key`) are not all non-empty; on success it sets the returned `Credentials.RotatedAt` to the store's recorded modification time. Neither `KeyManager.CreateCredentials` nor `KeyManager.UpdateCredentials` ever persists `RotatedAt`.
- **SC-010**: `Create` on an occupied identifier returns an error and leaves the stored value byte-for-byte unchanged.
- **SC-012**: `KeyManager.GetCredentials` returns a value built from the cache within `ttl` without invoking the underlying `KeyStore`.
- **SC-013**: `KeyManager` never caches a failed read — two consecutive `GetCredentials` calls on an identifier nothing is stored at both reach the underlying `KeyStore`.
- **SC-014**: `KeyManager` invalidates an identifier's cache entry on every successful `CreateCredentials`/`UpdateCredentials`/`DeleteCredentials` through it, and via an explicit `Invalidate` call.
- **SC-015**: `FakeKeyStore`'s per-method hooks, when set and returning a non-nil error, short-circuit before any state mutation.
- **SC-016**: With `SchedulesDeletion` unset, `FakeKeyStore.Delete` removes the entry outright and is idempotent: a following `Create` on that identifier succeeds, a following `Get` fails as it would on an identifier nothing was ever stored at, and a `Delete` of an absent identifier is not an error.
- **SC-016a**: `FakeKeyStore.Get` returns the timestamp its `Create` or `Update` most recently recorded for that identifier, taken from `Clock` (default `time.Now`).
- **SC-017**: `internal/secrets` exposes no `Initialize`/`GetInstance`-style singleton and holds no package-level mutable state.
- **SC-018**: `internal/secrets` imports `internal/errors` and no other package internal to this repository.
- **SC-019**: `internal/secrets` exposes no `HealthCheck` method.
- **SC-020**: Unit test coverage exceeds 95%, exercised entirely against `FakeKeyStore` — no network calls in this package's own test suite.
- **SC-021**: With `SchedulesDeletion` set, `FakeKeyStore.Delete` leaves the identifier occupied but unusable: `Get` and `Update` fail naming it as scheduled for deletion, and `Create` fails naming the identifier as unreusable. A second `Delete` on a pending identifier succeeds and changes nothing — the identifier stays blockaded and `Restore` still works — and a `Delete` of an absent identifier still schedules nothing.
- **SC-022**: `FakeKeyStore.Restore` cancels a pending deletion, restoring the stored value for `Get` and `Update`, and returns an error when nothing at the identifier is scheduled — whether the identifier is empty or holds a live value.
- **SC-023**: `FakeKeyStore.Create` wraps `ErrPendingDeletion` when the existing entry is pending deletion; the returned error is not itself a user error.

## Security Considerations

- **Namespace as sole trust anchor** (design.md 3.11.1): `NewTenantIdentifier` takes `namespace` as a plain parameter and performs no Kubernetes lookup of its own — the guarantee depends entirely on every caller passing `metadata.namespace` from the runtime object, never a value read from `spec`. This package can enforce identifier *shape*; it cannot enforce which namespace a caller passes.
- **Isolation protects against tenants, not against the controller**: the controller's single store role can read every tenant's secret, so a controller bug that builds the wrong identifier, or an attacker inside the controller, is not stopped by the identifier scheme.
- **Non-resolved account name in the identifier** (design.md 3.11.1, 3.12): `accountName` in `NewTenantIdentifier` must be the CRD's `metadata.name`, not the resolved, hash-suffixed Snowflake account name — using the resolved name would still be internally consistent but would depend on a value not derivable purely from Kubernetes identifiers, weakening the trust-anchor argument design.md makes.
- **`Create` is the only guard against overwriting a live credential**: because this package never reconciles an occupied identifier, a store whose `Create` is not atomic — one that silently upserts instead of failing — would let a retried request replace the key a live account authenticates with, and nothing above it would notice. Atomic create-if-absent is a hard requirement on every `KeyStore`, not a nicety.
- **Plaintext in the cache is an accepted trade-off**: `KeyManager` holds decrypted credential strings in process memory for up to `ttl`. This is acceptable under the platform's pod-isolation model (design.md 3.11) and is what makes the cache useful at all; it is not a reason to shorten `ttl` reflexively, since a shorter `ttl` only trades store round-trips for the same in-memory exposure.
- **Known accepted gap** (design.md Appendix B X1): once a tenant holds `ACCOUNTADMIN` on their account, they can re-key or drop the `platform` service user this package's credential authenticates as, locking the platform out of an account it remains responsible for. This spec does not attempt to prevent that — it is recorded here as a gap pending Snowflake Organization Policies, not something `internal/secrets` can close from the credential-storage side.
- **No credential value ever appears in an identifier or a log line**: `Identifier.String()` returns only the identifiers that make it up (org, namespace, account, or org-admin-account) — never a `PublicKey` or `PrivateKey`. Every error message this package's own error classification defines is built from identifiers and fixed descriptive text, never from credential contents.

## References

- **Product design**: `specs/design.md`, §3.6 (the `platform` user and `ADMIN_RSA_PUBLIC_KEY`), §3.11 (org-admin vs. per-account access), §3.11.1 (tenant secret identifier, namespace as trust anchor), §3.12 (resolved vs. CRD account name), Appendix B X1 (the `platform` user re-key/drop gap).
- **Error Handling (001)**: `internal/errors/errors.go` - `NewUserError()`, used to classify identifier-validation and not-found failures.
- **Base Config (002)**: `internal/config/base/base.go` - `SnowflakeSettings.Org`, `SnowflakeSettings.OrgAdminAccount`, `DeletionSettings.GracePeriodDays`, `CloudProvider()`; its own Example 1 already anticipates `secrets.KeyStore` and the `secretsaws.New` constructor this spec's sibling (003.a) provides.

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

func (m *Module) provisionCredentials(ctx context.Context, keyManager *secrets.KeyManager, org, namespace, accountName string) (*secrets.Credentials, error) {
    id, err := secrets.NewTenantIdentifier(org, namespace, accountName)
    if err != nil {
        return nil, err // user error: caller passed a malformed identifier
    }

    // CreateCredentials, never UpdateCredentials: if something is already
    // stored here the store says so instead of overwriting a key a live
    // account may still authenticate with. That failure is a system error
    // here — this module does not reuse or replace what it finds.
    creds, err := keyManager.CreateCredentials(ctx, id, "platform") // design.md 3.6's ADMIN_NAME
    if err != nil {
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

func (p *Pool) orgAdminCredentials(ctx context.Context, keyManager *secrets.KeyManager, org, orgAdminAccount string) (*secrets.Credentials, error) {
    id, err := secrets.NewOrgAdminIdentifier(org, orgAdminAccount)
    if err != nil {
        return nil, err
    }

    // cache hit avoids a store round-trip on every reconcile
    return keyManager.GetCredentials(ctx, id) // nothing stored here means ops has not provisioned this credential yet
}

// Wired once at startup:
// store := secretsaws.New(cfg.AWS.Region, cfg.AWS.KmsKeyId, cfg.Deletion.GracePeriodDays) // 003.a
// keyManager := secrets.NewKeyManager(store, cfg.Secrets.CacheTTL) // TTL from Config (002)
// pool := pool.New(keyManager, ...)                              // 004 depends only on *secrets.KeyManager
```

### Example 3: Testing Against `FakeKeyStore`

```go
// In a caller's own _test.go file — 003 ships FakeKeyStore so no test anywhere
// outside internal/secrets needs a real store or the AWS SDK.
import (
    "context"
    "errors"
    "testing"

    "github.com/allianz/yukimi/internal/secrets"
)

func TestCreate_RejectsAnOccupiedIdentifier(t *testing.T) {
    ctx := context.Background()
    store := secrets.NewFakeKeyStore()

    id, _ := secrets.NewTenantIdentifier("my_org", "finance", "analytics-team-eu")

    if err := store.Create(ctx, id, "first"); err != nil {
        t.Fatalf("first create: %v", err)
    }

    if err := store.Create(ctx, id, "second"); err == nil {
        t.Fatal("expected the second create to fail on an occupied identifier")
    }

    stored, _, err := store.Get(ctx, id)
    if err != nil {
        t.Fatalf("get: %v", err)
    }
    if stored != "first" {
        t.Fatal("a rejected create must leave the stored value untouched")
    }
}

// errStoreUnavailable is the caller's own error value, declared in its test
// file. FakeKeyStore propagates a hook's error unchanged, so a test asserts on a
// value it owns rather than on anything secrets exports.
var errStoreUnavailable = errors.New("store unavailable")

func TestGet_PropagatesInjectedFailure(t *testing.T) {
    ctx := context.Background()
    store := secrets.NewFakeKeyStore()
    store.OnGet = func(id secrets.Identifier) error { return errStoreUnavailable }

    id, _ := secrets.NewOrgAdminIdentifier("my_org", "my_org_admin_account")
    if _, _, err := store.Get(ctx, id); !errors.Is(err, errStoreUnavailable) {
        t.Fatalf("got %v, want errStoreUnavailable", err)
    }
}
```
