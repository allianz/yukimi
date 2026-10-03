# Specification: Secrets Handling (003)

## Overview

`internal/secrets/` stores and retrieves the credentials the platform uses to log in to Snowflake: the org-admin credential that creates accounts, and one per-tenant credential for each account the platform manages afterwards. All of them are RSA keypairs belonging to service users, held in a cloud secret manager.

This package is the only place in the codebase that reaches that secret manager. It generates the keypairs, decides the identifier each one is stored at, and caches values briefly so the same credential is not re-fetched on every reconcile. Those identifiers are what isolates one tenant from another (design.md 3.11.1), so this package constructs and validates them itself instead of trusting callers. The secret manager itself is pluggable: this spec defines only the `KeyStore` interface and the behavior every implementation owes its callers, with AWS Secrets Manager as the first implementation (003.a).

## Key Concept: The `KeyStore` Interface and the Identifier Grammar

A `KeyStore` sees identifiers and opaque value strings, nothing else. It never parses a credential, never caches, and never logs — it returns a plainly worded error naming the identifier it failed on. Its four methods are the narrow set any keystore can implement: `Get`, `Create` (fails if occupied), `Update` (fails if absent), `Delete`. `Create` and `Update` are separate rather than one upsert because create-if-absent must be **atomic in the store**: a retried request must never overwrite the key a live account authenticates with. `Get` additionally returns the time the store last wrote that value, as a second return value rather than a field inside the value — the store still never looks inside.

Identifiers are an opaque `Identifier` type, constructible only through the two constructors below, so an unvalidated identifier can never reach a store:

- **Tenant identifier** (design.md 3.11.1): `yk-<org>--<namespace>--<accountName>`. `<namespace>` comes from the runtime `metadata.namespace`, never a spec field; `<accountName>` is the CRD's `metadata.name`, **never** the resolved hash-suffixed account name (design.md 3.12). Every segment is a Kubernetes identifier — that is what makes the namespace the trust anchor design.md 3.11.1 requires.
- **Org-admin identifier**: `yk-orgadmin--<org>--<orgAdminAccount>`, from `Config.Snowflake` (002) resolved at the call site. This package takes plain strings and does not import `internal/config/base`.

The identifier charset (letters, digits, `-`) and the 127-character ceiling are both set by the tightest of the supported secrets-manager backends, Azure Key Vault — a secret name there must match `^[0-9a-zA-Z-]+$` and be no longer than that. `_` is mapped to `-` for exactly this reason: it is the only character upstream validation (002, the CRD) otherwise permits that Azure's charset does not.

**Important**: both constructors re-validate every segment regardless of upstream validation — reject an empty segment, one starting with `-`/`_`, one containing `/`, `.`, `..`, a repeated `-`/`_` (`--`, `__`, `-_`, `_-`), or a character outside `[A-Za-z0-9_-]` — so the isolation guarantee holds even on a flat key-value store with no hierarchical authorization of its own. The repeated-separator rule exists because the identifier is built by joining segments with `--`: `/` was banned inside a segment under the old `/`-delimited scheme, which made segment boundaries unambiguous by construction, but `-` is not banned (namespace and account names routinely contain it) — without this rule, two different tenants could join onto the identical identifier (e.g. `namespace="foo",accountName="bar-baz"` and `namespace="foo-bar",accountName="baz"`). Combined with no segment ever starting with `-`/`_`, the first `--` run encountered scanning left to right is always a true segment boundary, never segment-internal content, which is what makes the join collision-free again. The final length check against the 127-character ceiling exists because, while `internal/account/modules/account` (012) and Kubernetes already bound `org`+`accountName` and `namespace` tightly enough that the tenant identifier can't currently exceed it (worst case leaves 1 character of headroom), that bound is enforced elsewhere and this package does not trust it silently — and no equivalent bound exists at all for `orgAdminAccount`, which `002` validates for Snowflake-identifier shape but not for length. Because every length this package can see is otherwise already accounted for, a failure here is a system error, not a user error: it means some upstream bound drifted out of sync with this one rather than that the caller passed an ordinary fixable mistake.

## Key Concept: Credential Shape and Key Generation

A stored credential is a `Credentials` value with exactly three JSON fields: `username`, `public_key`, `private_key`. There is deliberately no `account` field — the identifier already names the account, and a duplicate would only drift.

The encodings are chosen so no consumer transforms them: `PublicKey` is PKIX, single-line base64 with no PEM delimiters, dropping straight into `ADMIN_RSA_PUBLIC_KEY = '<...>'` and `ALTER USER ... SET RSA_PUBLIC_KEY = '<...>'` (design.md 3.6, 3.9); `PrivateKey` is PKCS#8, PEM-wrapped, for the Snowflake driver's JWT signing. Generation uses `crypto/rand`, minimum 2048-bit RSA. `Username` is caller-supplied — design.md 3.6's `platform` is the account module's (012) domain knowledge, not a literal here.

`RotatedAt` is in-memory only, never persisted: `KeyManager.GetCredentials` sets it from whatever the underlying store returned alongside the value — so the store never holds a second copy of the same fact.

## Key Concept: Deleting a Credential Reserves Its Identifier

Secret stores rarely delete on the spot. They hold the identifier for a recovery window and refuse to store anything there meanwhile. Because the tenant identifier is derived from the tenant's own name (design.md 3.11.1), that reservation lands on the next tenant of the same name in the same namespace.

Snowflake reserves a dropped account name the same way, for its grace period. Keeping the credential's window inside that grace period leaves the account as the only thing that ever delays re-provisioning: a recovery window of a credential is as long as the secret store can make it, never longer than the grace period of the Snowflake account. Each store decides for itself how to keep that promise, with whatever means its own store offers — this package prescribes no shared type or derivation helper for the decision. With one implementation in the tree today (003.a), that decision stays a one-line cap; a second store with a stricter floor than the grace period's own minimum would face the tradeoff this package used to resolve centrally, and would resolve it itself instead.

## Key Concept: A KeyStore Error Taxonomy

A small taxonomy of defined errors covers the causes a caller does need to recognize: the failure wraps one, and `errors.Is` reaches it. `ErrPendingDeletion` belongs to that taxonomy — a secret identifier is already occupied by a secret scheduled for deletion.

## Key Concept: `KeyManager` Wraps a `KeyStore`, It Does Not Replace One

`NewKeyManager(store KeyStore, ttl)` decorates any `KeyStore` — no package-level state, no singleton, no branching logic of its own beyond the cache. Whatever `main.go` constructs is wrapped exactly once, so every store inherits identical freshness semantics. This is also the structural boundary the package enforces: everything outside `internal/secrets` depends on `*KeyManager`, never on the `KeyStore` interface directly — the interface exists so a new store implementation (a future `003.b`) has something to implement and so `main.go` has something concrete to construct before wrapping it, not as a type consumers are meant to hold onto. `KeyManager` does not implement `KeyStore` itself: `Get`/`Create`/`Update` are replaced by the credential-shaped `GetCredentials`/`CreateCredentials`/`UpdateCredentials` below, so a caller outside this package can never read or write a raw string a `KeyStore` would accept; `Delete` is simply renamed to `DeleteCredentials` for the same naming consistency, even though it has no value to be shaped around.

`Get` serves a cached value and its timestamp within `ttl` without touching the store; a miss — including an expired entry, evicted lazily with no background goroutine — fetches and populates. Two rules keep the cache racing toward "cold," never "stale": a failed `Get` is never cached, so a `Create` landing after a failed lookup is not masked by a negative result; and `Create`/`Update`/`Delete` write through and then *invalidate* the entry rather than pre-populating it. `Invalidate(id)` is also exposed directly.

## Public API

```go
package secrets

import (
    "context"
    "errors"
    "time"

    yukimierrors "github.com/allianz/yukimi/internal/errors"
)

// ErrPendingDeletion marks a Create failure caused by an identifier occupied
// by a secret scheduled for deletion rather than a live one. It is identity
// only: the failure it wraps is still an ordinary system error by
// default; a caller with more context may catch it via errors.Is and
// classify it differently.
var ErrPendingDeletion = errors.New("secrets: identifier pending deletion")

// KeyStore is a string-valued keystore. It never parses a credential, never
// caches, and never logs — every method reports failure as an ordinary error
// whose message names the identifier it failed on, and no caller branches on
// an error's identity. How the value string is persisted is each
// implementation's own choice.
type KeyStore interface {
    // Get returns the value stored at id, along with the time the backend
    // last wrote that value — creation time if never overwritten,
    // modification time otherwise. It fails if nothing is stored there, and
    // it fails if the store cannot be read; the returned time is the zero
    // value on error.
    Get(ctx context.Context, id Identifier) (string, time.Time, error)

    // Create stores value at id. It fails if id is already occupied, and
    // leaves the occupying value untouched when it does — this is the
    // atomicity 012 depends on to never silently overwrite a live account's
    // credential on a retried request. If the occupying secret is scheduled
    // for deletion rather than live, the returned error also wraps
    // ErrPendingDeletion.
    Create(ctx context.Context, id Identifier, value string) error

    // Update overwrites the value already stored at id. It fails if nothing
    // is stored there — Update never creates.
    Update(ctx context.Context, id Identifier, value string) error

    // Delete removes id. Nothing in this package reads a deleted identifier
    // afterwards.
    //
    // An implementation that schedules the removal instead of performing it must
    // keep that window within whatever account grace period it was constructed
    // with (002), by whatever means suits its own store — this package
    // prescribes no shared mechanism for that decision. While the removal is
    // pending, id stays occupied: Get and Update fail on it and so does
    // Create, since the store has not released the name yet.
    Delete(ctx context.Context, id Identifier) error
}

// Identifier is an opaque, pre-validated secret identifier. The zero value is
// not valid; only NewTenantIdentifier and NewOrgAdminIdentifier produce one.
type Identifier struct{ /* unexported */ }

// NewTenantIdentifier builds the tenant platform-credential identifier
// (design.md 3.11.1): yk-<org>--<namespace>--<accountName>.
//
// Parameters:
//   - org: Snowflake organization name (Config.Snowflake.Org, 002)
//   - namespace: Kubernetes namespace — MUST come from metadata.namespace at
//     the call site, never a spec field (design.md 3.11.1)
//   - accountName: the CRD's metadata.name — MUST NOT be the resolved,
//     hash-suffixed Snowflake account name from design.md 3.12
//
// Returns:
//   - User error if any segment is empty, starts with '-'/'_', contains '/',
//     '.', '..', a repeated '-'/'_', or a character outside [A-Za-z0-9_-]
//   - System error if the resulting identifier exceeds the 127-character
//     ceiling every supported secrets-manager backend shares — every length
//     this package can see is already bounded tightly enough elsewhere
//     (006, 012) that this should never actually trigger; firing it means an
//     invariant drifted out of sync upstream, not that the caller passed a
//     fixable bad input
func NewTenantIdentifier(org, namespace, accountName string) (Identifier, error)

// NewOrgAdminIdentifier builds the org-admin credential identifier:
// yk-orgadmin--<org>--<orgAdminAccount>.
//
// Parameters:
//   - org: Config.Snowflake.Org (002)
//   - orgAdminAccount: Config.Snowflake.OrgAdminAccount (002)
//
// Returns:
//   - User error under the same validation rule as NewTenantIdentifier
func NewOrgAdminIdentifier(org, orgAdminAccount string) (Identifier, error)

// String returns the identifier for logging. It never contains secret
// material — only the identifiers that make it up.
func (i Identifier) String() string

// Credentials is the JSON shape a credential is stored in: exactly three
// fields, deliberately no account field (the identifier already identifies
// it).
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

// marshaling and unmarshaling between Credentials and the JSON string a
// KeyStore stores is this package's own internal detail — a caller only ever
// reaches it through KeyManager.CreateCredentials/UpdateCredentials/
// GetCredentials below, never directly.

// KeyManager wraps a KeyStore with an in-memory, TTL-based, lazily-evicted
// cache. Every consumer outside this package holds a *KeyManager, never a
// concrete KeyStore. Unlike KeyStore, it exposes no raw-value Get/Create/
// Update — only the credential-shaped methods below — so a caller outside
// this package can never read or write a value except as a Credentials this
// package itself generated and marshaled.
type KeyManager struct { /* unexported */ }

// NewKeyManager wraps store. Every concrete KeyStore should be wrapped exactly
// once, at construction time in cmd/provider/main.go.
func NewKeyManager(store KeyStore, ttl time.Duration) *KeyManager

// CreateCredentials generates a fresh keypair for username via NewCredentials,
// stores it at id create-only, and returns the generated Credentials — the
// one place a caller still needs the plaintext public key after storing it
// (e.g. to pass into CREATE ACCOUNT).
func (c *KeyManager) CreateCredentials(ctx context.Context, id Identifier, username string) (*Credentials, error)

// UpdateCredentials marshals creds and stores it at id update-only. The
// caller is responsible for generating creds itself (via NewCredentials) —
// this exists for 004's rotation flow, which must push the new public key
// into Snowflake between generating it and persisting it.
func (c *KeyManager) UpdateCredentials(ctx context.Context, id Identifier, creds *Credentials) error

// GetCredentials reads the raw value stored at id and unmarshals it,
// rejecting a value with any of the three JSON fields empty — it does not
// otherwise validate PublicKey or PrivateKey contents. The returned
// Credentials' RotatedAt is the time the backend last wrote the value.
func (c *KeyManager) GetCredentials(ctx context.Context, id Identifier) (*Credentials, error)

// DeleteCredentials removes (or, store-dependent, schedules the removal of)
// the credential at id.
func (c *KeyManager) DeleteCredentials(ctx context.Context, id Identifier) error

// Invalidate clears id's cache entry without touching the underlying
// KeyStore. Exposed for a caller that needs an identifier forced cold without
// going through CreateCredentials/UpdateCredentials/Delete.
func (c *KeyManager) Invalidate(id Identifier)

// FakeKeyStore is an in-memory KeyStore for tests, exported (not a _test.go
// file) so 004, 012, and every other consumer can depend on it without a real
// store. Each hook, if set and returning a non-nil error, short-circuits the
// call before any state mutation — this lets a test flip behavior mid-run
// (e.g. "OnCreate fails once, then is cleared") in a way a construction-time
// option cannot.
type FakeKeyStore struct {
    OnGet    func(id Identifier) error
    OnCreate func(id Identifier) error
    OnUpdate func(id Identifier) error
    OnDelete func(id Identifier) error

    // Clock returns the time recorded against an identifier on Create and
    // Update, and returned by Get. Defaults to time.Now; tests override it
    // for a deterministic RotatedAt.
    Clock func() time.Time

    // SchedulesDeletion makes Delete schedule the removal instead of performing
    // it: the entry becomes unreadable but keeps its identifier occupied until
    // Restore cancels the removal. False — the default — deletes outright, so a
    // consumer that does not care about the pending state sees the simplest
    // possible behavior.
    SchedulesDeletion bool
}

// Restore cancels a pending deletion, making the value readable and the
// identifier writable again — the store-side half of the manual repair 012
// documents.
//
// Returns:
//   - Error if nothing at id is scheduled for deletion, whether because the
//     identifier is empty or because the entry is live
func (f *FakeKeyStore) Restore(id Identifier) error

// NewFakeKeyStore returns an empty FakeKeyStore that deletes outright. Delete
// removes the entry and is idempotent, so a Create on a deleted identifier
// succeeds and a Get on one fails exactly as it would on an identifier
// nothing was ever stored at. Set SchedulesDeletion to exercise the
// pending-deletion state instead.
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
- A `Create` onto an occupied identifier: `secrets: a secret already exists at yk-my-org--finance--analytics-team-eu`. Also wraps `ErrPendingDeletion` when the occupying secret is scheduled for deletion rather than live (Key Concept: A KeyStore Error Taxonomy).
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
- Derives, once and for every store, the recovery window a deleted credential may sit in — never longer than the account grace period it belongs to (002).
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
- **What happens to a tenant secret after `DROP ACCOUNT` (012)?** - `KeyManager.DeleteCredentials` is called on the tenant identifier, which either schedules the removal or performs it outright. Which of the two happens is the concrete store's business, bounded by the recovery-window rule above, and `DeleteCredentials` reports neither — it returns only an error. Nothing in this package reads a deleted identifier afterwards.
- **What if the store's shortest representable window is longer than the account grace period?** - The store destroys the value irreversibly rather than reserving a window that would outlive the account. That is the correct outcome rather than a degradation to report: a credential blocking an identifier whose account is already reusable has no recovery value at all, while a destroyed one only makes a restore need manual repair. How a store recognizes and reports this case is its own concern (003.a); this package prescribes no shared mechanism for it.
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
- **SC-005**: Both identifier constructors return a user error for any empty segment, one starting with `-`/`_`, one containing `/`, `.`, `..`, a repeated `-`/`_`, or a character outside `[A-Za-z0-9_-]`.
- **SC-005a**: Both identifier constructors return a system error, not a user error, for a resulting identifier longer than 127 characters — this is a last-resort invariant check, not a condition a caller's input can normally reach.
- **SC-006**: `Identifier` values are constructible only via `NewTenantIdentifier`/`NewOrgAdminIdentifier` — no exported field or function accepts an arbitrary unvalidated string as an `Identifier`.
- **SC-007**: `Credentials` marshals to JSON with exactly the fields `username`, `public_key`, `private_key` — no `account` field.
- **SC-008**: `GenerateKeyPair` produces a minimum 2048-bit RSA key: PKCS#8-encoded, PEM-wrapped private key; PKIX-encoded, single-line base64 public key with no PEM delimiters.
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
