# Specification: Connection Pooling (004)

This specification covers two packages: `internal/snowflake/pool` (pooled Snowflake connections) and `internal/snowflake/host` (host and URL construction). 

## Overview

`internal/snowflake/pool/` keeps one already-authenticated connection open per account it manages for the whole life of the controller process, instead of connecting and disconnecting on every reconcile. It solves a scaling problem: the platform manages many Snowflake accounts at once, and a reconcile's own cadence swings from once every few minutes in steady state down to several times a second during error backoff, so paying a fresh login cost on every call would be both slow and wasteful. It also gives every tenant account its own connection, separate from the one used for account creation and deletion — the security motivation for that split is detailed in the Key Concepts below. The technical approach is to keep one open connection handle per distinct target — one for the powerful connection, one per tenant account — opened the first time it is needed and kept for later reuse rather than closed after use, with credentials read fresh from the secret store only when a handle is first created. Host construction sits in a small leaf package of its own, `internal/snowflake/host`, because 006 builds a tenant's `status.accountUrl` from the same host.

## Scope

This specification defines the `internal/snowflake/pool/` and `internal/snowflake/host/` packages that:
- Maintains pooled `*sql.DB` connections to Snowflake, authenticated with JWT keypair credentials read through the secrets backend interface (003) — never through a concrete backend package.
- Offers two connection scopes reflecting the privilege step-down of design.md 3.11: a single organization-admin connection used only for `CREATE ACCOUNT`/`DROP ACCOUNT`, and a per-tenant-account connection, keyed the same way as a tenant's secret path, used for everything else.
- Builds the Snowflake connection host and account URL from a locator and a cloud-region string in `internal/snowflake/host`, serving `gosnowflake.Config.Host` here and `status.accountUrl` in 006, with the PrivateLink decision passed in by the caller.
- Opens each connection lazily on first use, keeps it open for later reuse rather than closing it after each call, and only ever closes it on explicit eviction or process shutdown.
- Runs a lightweight health probe using the raw driver when a connection is first established, so a bad credential or host fails immediately rather than on some later caller's first real query.
- Introduces this repository's only dependency on the Snowflake Go driver (`gosnowflake`) and registers it.

**Out of Scope**:
- SQL statement semantics, safe rendering, and error decoration — that is 005's job. This package hands 005 a plain `*sql.DB`; it never imports `internal/snowflake/statement`, and 005 never imports this package (see Key Concept below).
- Any concrete secrets backend — this package takes a `secrets.Backend` as a constructor parameter and never imports `internal/secrets/aws` or any other backend package.
- Generating, storing, or rotating credentials — that is 003's job. This package only reads what 003 already stored.
- Anything about which SQL statements run once a connection is obtained — that is every downstream module's business (010–013, 015, 016, 019), never this package's.
- Deciding *whether* PrivateLink is in use: callers pass that flag (today `BaseConfig.Snowflake.UsePrivateLink`, 002), and `internal/snowflake/host` never reads configuration itself.
- The `SnowflakeAccount` status field `accountUrl` (006) — this spec builds the string; 006 owns the field, the CRD schema, and when it is written.

## Key Concept: Two Connection Scopes and the Privilege Step-Down

A `Pool` never exposes more than two kinds of connection, matching design.md 3.11's own split. The **org-admin scope** is a single connection, authenticated as the organization-level credential at the org-admin secret path (003), used only for `CREATE ACCOUNT` and `DROP ACCOUNT` (design.md 3.6, 6.3). The **tenant scope** is one connection per Snowflake account, authenticated as that account's `platform` service user (design.md 3.6, Appendix B X1) at the tenant secret path — the same `(org, namespace, accountName)` tuple 003 already uses to build that path. Every other operation this platform performs — parameters, network rules, identity import, quotas — goes through a tenant connection, never the org-admin one.

**Important**: nothing in this package's public surface lets a caller reach the org-admin connection from a tenant-scoped call, or vice versa. The two scopes are two different methods with two different signatures, not a shared method with a scope flag a caller could get wrong. This is what makes design.md 3.11's step-down a property of the code's shape rather than of callers remembering to ask for the narrow connection.

## Key Concept: Open Lazily, Never Close Until Shutdown or Eviction

A `*sql.DB` is already a connection pool — the standard library multiplexes physical connections underneath one handle and recycles them on its own schedule (idle-time limit, maximum lifetime), configured once at creation. This package's `Pool` type just caches one `*sql.DB` per target and hands back the same one on every call, so the JWT handshake is paid once per target rather than on every reconcile. `Close` is only ever called on eviction or process shutdown — never after ordinary use. Eviction's primary trigger is account deletion: once an account is dropped (design.md 6.3, 017 not yet written), 017 calls `EvictTenant` so that entry is closed and removed rather than left open forever. The same primitive also covers a subtler, automatic case below.

## Key Concept: Self-Healing on a Locator Change

A Snowflake account name is unique only while it exists — design.md 6.3's `DROP ACCOUNT` followed by a later resource under the same `metadata.name` and namespace resolves to the same tenant secret path (003) and the same cache key here, but Snowflake assigns the new account a **different locator** on `CREATE ACCOUNT` (design.md 3.6). A cache keyed only on `(namespace, accountName)` would keep serving a connection to an account that no longer exists. To avoid depending on every future caller remembering to evict before reconnecting, the tenant scope's cache entry carries the locator and region it was built with alongside the `*sql.DB`; a call whose locator or region does not match what is cached closes the stale connection and dials again before returning, exactly as if it had been evicted first. This is a correctness property of `TenantAccount` itself, not a workaround callers must apply.

## Key Concept: Host Construction from a Locator and a Cloud-Region String

The connection host is built from the account's locator (design.md 3.6) and its cloud-region string (e.g. `aws-eu-central-1`, design.md 3.1). Most regions repeat the cloud as a trailing segment after the region (`aws-eu-west-3` → `eu-west-3.aws`); `eu-central-1` is the one known exception and needs no suffix, so an unexported `switch` names it and the default case strips the cloud prefix and reattaches it — not a lookup table or Backplane Config (007) entry, since there is one exception to name. The locator leads, and the suffix is `.privatelink.snowflakecomputing.com` or `.snowflakecomputing.com` depending on the flag the caller passes.

`host` is its own leaf package because 006 builds a tenant's `status.accountUrl` from the same host. `URL` is `Hostname` with `https://` prefixed (design.md 7.2).

## Public API

The two packages have a one-way dependency: `internal/snowflake/pool` imports `internal/snowflake/host`; `host` imports nothing internal but `internal/errors`.

### Package `internal/snowflake/host`

Host and URL construction from an account locator and a cloud-region string. No configuration, no credentials, no network — a pure string builder, imported by `pool` here and by `internal/tenant` (006) for `status.accountUrl`.

```go
package host

// Hostname returns the Snowflake connection host for an account, e.g.
// "xc19114.eu-central-1.privatelink.snowflakecomputing.com".
//
// Parameters:
//   - locator: the Snowflake account locator (design.md 3.6), e.g. "xc19114";
//     opaque, and never validated here
//   - region: the account's cloud-region string (e.g. "aws-eu-central-1",
//     design.md 3.1)
//   - usePrivateLink: selects the .privatelink.snowflakecomputing.com suffix
//     over .snowflakecomputing.com; the caller decides (today from
//     BaseConfig.Snowflake.UsePrivateLink, 002), never this package
//
// Returns:
//   - the host, or an empty string and a user error if region does not match
//     the expected cloud-region format
func Hostname(locator, region string, usePrivateLink bool) (string, error)

// URL returns the account's browser URL — Hostname with "https://" prefixed,
// carrying no path (design.md 7.2). Consumed by 006 for status.accountUrl.
//
// Parameters: as Hostname.
//
// Returns:
//   - the URL, or an empty string and the same user error Hostname returns for
//     a malformed region
func URL(locator, region string, usePrivateLink bool) (string, error)
```

### Package `internal/snowflake/pool`

The pooled connections themselves.

```go
package pool

import (
    "context"
    "database/sql"
    "time"

    "github.com/allianz/yukimi/internal/config"
    "github.com/allianz/yukimi/internal/secrets"
)

// Pool caches one *sql.DB per connection target and hands back the same one
// on every subsequent call. Every cached *sql.DB stays open until Close or an
// explicit eviction — never after ordinary use.
type Pool struct { /* unexported */ }

// New constructs a Pool. It makes no connection attempt itself: every
// *sql.DB is opened lazily, on its first OrgAdmin or TenantAccount call.
//
// Parameters:
//   - backend: the secrets.Backend (003) credentials are read through; never
//     a concrete backend package
//   - cfg: BaseConfig (002) — Snowflake.Org, OrgAdminAccount,
//     OrgAdminAccountLocator, OrgAdminAccountRegion, UsePrivateLink,
//     DisableOCSPChecks, MaxConnectionPoolSize, MaxIdleConnections,
//     ConnectionMaxLifetime, ConnectionMaxIdleTime, ConnectionProbeTimeout
//
// Returns:
//   - *Pool: never nil
func New(backend secrets.Backend, cfg *config.BaseConfig) *Pool

// OrgAdmin returns the single org-admin *sql.DB, used only for CREATE ACCOUNT
// and DROP ACCOUNT (design.md 3.6, 6.3, 3.11 intro). The credential is read
// from the org-admin secret path (003) and the connection is authenticated
// with the ORGADMIN role. Opened on first call; every later call returns the
// same *sql.DB.
//
// Returns:
//   - System error if the org-admin credential cannot be read, does not
//     parse as a valid private key, or the connection cannot be established
func (p *Pool) OrgAdmin(ctx context.Context) (*sql.DB, error)

// TenantAccount returns the per-tenant *sql.DB, authenticated as that
// account's platform service user with the ACCOUNTADMIN role (design.md
// 3.6, 3.11, Appendix B X1). Keyed by (org, namespace, accountName) — the
// same tuple as the tenant secret path (003) — plus the account's current
// locator and region: a mismatch against a cached entry's locator or region
// closes it and dials again (see Key Concept: Self-Healing).
//
// Parameters:
//   - namespace: metadata.namespace at the call site, never a spec field
//     (design.md 3.11.1)
//   - accountName: the CRD's metadata.name, never the resolved, hash-suffixed
//     Snowflake account name (design.md 3.12) — matches the tenant secret path
//   - locator: the Snowflake account locator captured from CREATE ACCOUNT
//     (design.md 3.6); this package never runs CREATE ACCOUNT itself, so the
//     caller supplies it
//   - region: the account's cloud-region string (e.g. "aws-eu-central-1",
//     design.md 3.1)
//
// Returns:
//   - User error from host.Hostname if region does not match the expected
//     cloud-region format
//   - System error if the tenant credential cannot be read, does not parse
//     as a valid private key, or the connection cannot be established
func (p *Pool) TenantAccount(ctx context.Context, namespace, accountName, locator, region string) (*sql.DB, error)

// EvictTenant closes and removes the cached *sql.DB for (namespace,
// accountName), if one exists. Called once an account is dropped (017, not
// yet written) so a deleted tenant's connection does not linger for the rest
// of the process's life. A key never dialed is a no-op, not an error.
func (p *Pool) EvictTenant(namespace, accountName string)

// Close closes every cached *sql.DB — the org-admin connection, if opened,
// and every tenant connection. Called exactly once, from
// cmd/provider/main.go on shutdown.
//
// Returns:
//   - System error joining any individual Close failure; every entry is
//     still attempted even if an earlier one fails
func (p *Pool) Close() error
```

## Project Structure

```text
internal/snowflake/host/
├── host.go          # regionSegment (the region/cloud switch), Hostname, URL
├── host_test.go
└── doc.go

internal/snowflake/pool/
├── pool.go          # Pool, New, OrgAdmin, TenantAccount, EvictTenant, Close, cache keys
├── pool_test.go
├── connect.go       # dialFunc seam, defaultDial, gosnowflake.Config construction, health probe
├── connect_test.go
└── doc.go
```

`internal/snowflake/host` imports only the standard library and `internal/errors` (001) — never `internal/config`, never `github.com/snowflakedb/gosnowflake`, never `internal/snowflake/pool`. That leaf position is what lets `internal/tenant` (006) build `status.accountUrl` from the same code without inheriting a driver, a secret store, or configuration.

`internal/snowflake/pool` must never import `internal/snowflake/statement` (005) or `internal/secrets/aws` (003.a). The only imports outside the standard library are `internal/snowflake/host`, `internal/config` (002), `internal/secrets` (003), `internal/errors` (001), and `github.com/snowflakedb/gosnowflake`, pinned at **v1.18.1** — the version `specs/notes-snowflake-sql-mechanics.md`'s driver findings were verified against; an upgrade means re-verifying those findings before relying on them.

## Error Classification

**User Errors** (use `errors.NewUserError()`):
- Malformed cloud-region string: `region 'Frankfurt!' does not match the expected cloud-region format (expected: aws-eu-central-1)`

`host.Hostname` and `host.URL` raise this, and `Pool.TenantAccount` surfaces it unchanged from its own call to `host.Hostname`. Validating in `host` means every consumer — this package and 006 — rejects the same regions with the same message, rather than each deciding for itself.

`host` validates the region shape independently of whatever validation the caller (a guardrail, 008, or 002's own shape check on `OrgAdminAccountRegion`) already performed — the same reasoning 003 gives for re-validating every secret path segment independently of the caller.

**System Errors** (use `fmt.Errorf("context: %w", err)`):
- Credential read failure: `failed to read org-admin credentials: %w` / `failed to read tenant credentials for finance/analytics-team-eu: %w`
- Stored private key does not parse: `failed to parse private key for finance/analytics-team-eu: %w`
- Connection cannot be established or the health probe fails: `failed to connect to xc19114.eu-central-1.privatelink.snowflakecomputing.com: %w`

## Edge Cases

- **What happens on the very first call for a key that fails to connect?** - Nothing is cached. `OrgAdmin`/`TenantAccount` returns the error, and the next call retries the credential read and dial from scratch — mirroring 003's rule that a failed `Get` is never cached.
- **What happens if two goroutines call `TenantAccount` for the same key at the same time on a cold cache?** - Both block behind that key's own lock, acquired per-key rather than pool-wide; the first to acquire it dials and caches, the second observes the now-populated cache and returns the same `*sql.DB` without dialing a second time. A cold dial for a *different* key never waits on this — see Edge Cases below on running at a few thousand accounts.
- **Does a pool-wide lock serialize connecting a few thousand accounts, e.g. right after the process starts?** - No — the lock is per key, so cold dials for different accounts proceed concurrently; only two callers racing for the *same* account's first connection serialize. A shared pool-wide lock would have made a cold start across thousands of accounts take minutes of pure lock contention instead of running them in parallel, so this is a hard design requirement, not an implementation detail. The map holding thousands of cached entries costs a small, fixed amount of memory per entry (map slot plus an idle `*sql.DB`) — negligible at this scale. What ops must size for instead is the pod's open-file-descriptor limit: with per-account idle-connection limits set (see below), a few thousand actively-reconciled accounts can hold a correspondingly large number of idle TCP connections at once, all to different Snowflake accounts rather than concentrated on one, so no single account's own connection limit is at risk.
- **What happens when an account is dropped and later recreated under the same CRD name and namespace?** - See Key Concept: Self-Healing. The new locator no longer matches the cached entry, so the stale connection is closed and a fresh one dialed automatically, without requiring 017 to call `EvictTenant` first — though 017 calls it anyway, immediately after `DROP ACCOUNT`, so the cache never briefly serves a connection to an account already gone.
- **What tunes the underlying `*sql.DB`'s own connection limits, idle timeout, and maximum lifetime?** - `BaseConfig.Snowflake` (002): `MaxConnectionPoolSize`, `MaxIdleConnections`, `ConnectionMaxLifetime`, `ConnectionMaxIdleTime`, each with a documented default when omitted from `baseConfig.yaml`. `New` reads them once and applies them via `SetMaxOpenConns`/`SetMaxIdleConns`/`SetConnMaxLifetime`/`SetConnMaxIdleTime` to every `*sql.DB` this package dials.
- **Does the health probe run on every `OrgAdmin`/`TenantAccount` call, or only when a new connection is dialed?** - Only when a new connection is dialed (a cold cache, or after eviction/self-healing). A cache hit returns the already-cached `*sql.DB` with no probe and no other network call — probing on every call would defeat the point of caching.
- **Why does session role scoping use a `Config` field instead of a runtime `USE ROLE` statement?** - The Snowflake Go driver accepts a `Role` at connection construction time, applied automatically to every physical connection the driver opens underneath the cached `*sql.DB` — this needs no SQL statement and therefore no dependency on 005's statement execution. If a future need arises for session setup `Config` cannot express, it is done with the raw driver (`db.ExecContext`) directly in this package — never via `internal/snowflake/statement` (005), which is exactly the dependency direction this package must not create (005 already depends on the connection this package hands it; the reverse would be a cycle).
- **What if the region passed to `TenantAccount` is well-formed but the cloud prefix is one this package has never seen (say a future `gcp-`)?** - Accepted. `host.regionSegment`'s default case strips whatever prefix precedes the first `-` and reattaches it as a trailing segment — it has no allowlist of recognized clouds, only a named exception for `aws-eu-central-1`. Whether the resulting host is real is discovered on connect, exactly like an unverified region in 002.
- **Why does `host` take a PrivateLink bool instead of reading `BaseConfig.Snowflake.UsePrivateLink` (002) itself?** - To stay reusable: 006 builds a tenant's `status.accountUrl` from the same host, and a configuration-free leaf can be imported by `internal/tenant` without dragging `internal/config` in with it. Callers pass the flag, so its origin can change without touching this package.
- **Does `host.URL` include a path such as `/console/login`?** - No. design.md 7.2 specifies `status.accountUrl` as scheme plus host, and Snowflake redirects a bare host to the login console on its own. If an explicit console link is ever wanted, it is a new exported function in `host` rather than a change to `URL`, so a tenant's status URL keeps the form 7.2 documents.

## Dependencies

- **`internal/errors` (001)** - Used APIs: `errors.NewUserError()` - Contract: used by both packages; in `host` for the one region-format validation above, in `pool` nowhere else.
- **`internal/config` (002)** - Read by `pool` only; `host` never imports it - Used APIs: `config.BaseConfig`, `Snowflake.Org`, `Snowflake.OrgAdminAccount`, `Snowflake.OrgAdminAccountLocator`, `Snowflake.OrgAdminAccountRegion`, `Snowflake.UsePrivateLink`, `Snowflake.DisableOCSPChecks`, `Snowflake.MaxConnectionPoolSize`, `Snowflake.MaxIdleConnections`, `Snowflake.ConnectionMaxLifetime`, `Snowflake.ConnectionMaxIdleTime`, `Snowflake.ConnectionProbeTimeout` - Contract: `Pool` reads these once at construction and treats them as fixed for the process's life, matching `BaseConfig`'s own immutability.
- **`internal/secrets` (003)** - Used APIs: `secrets.Backend`, `NewOrgAdminPath()`, `NewTenantPath()`, `UnmarshalCredentials()` - Contract: takes a `secrets.Backend` as a constructor parameter, satisfied by whatever concrete backend `cmd/provider/main.go` wired up and wrapped in `secrets.NewCachedBackend`; never imports a concrete backend itself.
- **`github.com/snowflakedb/gosnowflake` v1.18.1** - the only Snowflake driver dependency in the tree; this is the spec that adds it to `go.mod` (see Project Structure).

## Integration Points

- **`cmd/provider/main.go`** - Constructs the `Pool` once via `pool.New(cachedBackend, cfg)` after building the secrets backend (003.a) and loading `BaseConfig` (002), and calls `Pool.Close()` on shutdown - Key functions: `pool.New()`, `Pool.Close()`.
- **`internal/snowflake/statement` (005, not yet written)** - Takes the `*sql.DB` this package returns as its injected executor and never imports this package directly; this package never imports it either, so the two-way avoidance is enforced from both sides.
- **`internal/account/modules/account` (010, not yet written)** - Calls `Pool.OrgAdmin()` to run `CREATE ACCOUNT` and reads back its response's locator for status (design.md 3.6, 7.2) - Key functions: `Pool.OrgAdmin()`.
- **Every other account module (011–013, 015) and the account pipeline/controller (009, 018, not yet written)** - Call `Pool.TenantAccount()` to reach an account's own connection for parameters, network rules, identity import, and auth rules - Key functions: `Pool.TenantAccount()`.
- **`internal/deletion` (017, not yet written)** - Calls `Pool.OrgAdmin()` to run `DROP ACCOUNT` and `Pool.EvictTenant()` immediately afterward so the cache does not keep serving a connection to a dropped account - Key functions: `Pool.OrgAdmin()`, `Pool.EvictTenant()`.
- **`internal/tenant` (006, not yet written)** - Calls `host.URL()` to build `status.accountUrl` (design.md 7.2), passing the locator 010 captures from `CREATE ACCOUNT`, the account's region, and the PrivateLink flag its caller (018) reads from `BaseConfig` (002). It never calls `Pool`, and `host` never imports `internal/tenant`, so the boundary holds from both sides - Key functions: `host.URL()`.

## Success Criteria

- **SC-001**: `New` returns a non-nil `*Pool` and makes no network call.
- **SC-002**: `OrgAdmin`'s first call reads the org-admin credential via `secrets.Backend.Get`/`NewOrgAdminPath`, builds the host from `OrgAdminAccountLocator`/`OrgAdminAccountRegion`/`UsePrivateLink`, and returns a `*sql.DB`.
- **SC-003**: Every later `OrgAdmin` call returns the identical `*sql.DB` pointer from the first call, without re-reading the credential or dialing again.
- **SC-004**: `TenantAccount` builds its secret path via `NewTenantPath(org, namespace, accountName)`, using `BaseConfig.Snowflake.Org` and the caller-supplied `namespace`/`accountName`.
- **SC-005**: Two `TenantAccount` calls with identical `namespace`/`accountName`/`locator`/`region` return the identical `*sql.DB` pointer; a call with a different `namespace` or `accountName` returns a distinct one.
- **SC-006**: `host.regionSegment("aws-eu-central-1")` returns `"eu-central-1"`; `host.regionSegment("aws-eu-west-3")` returns `"eu-west-3.aws"`.
- **SC-007**: `host.Hostname` appends `.privatelink.snowflakecomputing.com` when `usePrivateLink` is true and `.snowflakecomputing.com` when false, and the locator forms the leading label in both.
- **SC-007a**: `host.URL("xc19114", "aws-eu-central-1", true)` returns `https://xc19114.eu-central-1.privatelink.snowflakecomputing.com` — design.md 7.2's example verbatim, with no trailing path.
- **SC-008**: `TenantAccount` returns a user error for a `region` missing its cloud prefix (e.g. `"eu-central-1"`) or otherwise malformed, and never attempts a connection in that case — satisfied by its call to `host.Hostname` preceding any credential read or dial.
- **SC-008a**: `host.Hostname` and `host.URL` both return an empty string and a user error for a region missing its cloud prefix or otherwise malformed.
- **SC-009**: A failed credential read, key parse, dial, or health probe on the first call for a key leaves nothing cached — the next call for the same key retries in full.
- **SC-010**: Concurrent goroutines calling `TenantAccount` with the same key against a cold cache result in exactly one dial and one cached `*sql.DB`, observed by all callers.
- **SC-010a**: Concurrent cold dials for *different* keys do not serialize behind a single pool-wide lock — locking is scoped per key, provable by a test that blocks one key's dial and asserts a second key's dial still completes.
- **SC-011**: A `TenantAccount` call whose `locator` or `region` differs from what is cached for that `(namespace, accountName)` closes the stale `*sql.DB` and returns a freshly dialed one.
- **SC-012**: `EvictTenant` closes and removes the cached entry for `(namespace, accountName)`; a following `TenantAccount` call with the same key dials again.
- **SC-013**: `EvictTenant` on a key never dialed does not error and does not panic.
- **SC-014**: `Close` closes every cached `*sql.DB` — org-admin, if opened, and every tenant entry — and returns a joined error if any individual close fails, without skipping the rest.
- **SC-015**: The `gosnowflake.Config` built for `OrgAdmin` sets `Authenticator` to `AuthTypeJwt`, `User` and `PrivateKey` from the stored org-admin credential, `Role` to `ORGADMIN`, and `Account`/`Host` from `OrgAdminAccountLocator`/`OrgAdminAccountRegion`.
- **SC-016**: The `gosnowflake.Config` built for `TenantAccount` sets `Role` to `ACCOUNTADMIN` and `Account`/`Host` from the caller-supplied `locator`/`region`.
- **SC-017**: `internal/snowflake/pool` imports `internal/snowflake/host`, `internal/config`, `internal/secrets`, `internal/errors`, and `github.com/snowflakedb/gosnowflake` among dependencies with an `internal/` boundary or a new `go.mod` entry — never `internal/secrets/aws` and never `internal/snowflake/statement`, grep-provable.
- **SC-017a**: `internal/snowflake/host` imports only the standard library and `internal/errors` — never `internal/config`, `internal/secrets`, `internal/snowflake/pool`, or `github.com/snowflakedb/gosnowflake`, grep-provable.
- **SC-018**: `go.mod` pins `github.com/snowflakedb/gosnowflake` at `v1.18.1`.
- **SC-019**: The dial step is reachable through an unexported, swappable seam so unit tests exercise `Pool`'s caching, eviction, self-healing, and concurrency behavior without a real Snowflake account, a real network call, or the real driver.
- **SC-020**: Unit test coverage exceeds 95% for both packages.
- **SC-021**: Every `*sql.DB` this package dials has `SetMaxOpenConns`, `SetMaxIdleConns`, `SetConnMaxLifetime`, and `SetConnMaxIdleTime` applied from `cfg.Snowflake.MaxConnectionPoolSize`/`MaxIdleConnections`/`ConnectionMaxLifetime`/`ConnectionMaxIdleTime`, and the health probe's context deadline is `cfg.Snowflake.ConnectionProbeTimeout`.
- **SC-022**: The `gosnowflake.Config` built for both `OrgAdmin` and `TenantAccount` sets `DisableOCSPChecks` from `cfg.Snowflake.DisableOCSPChecks`.

## Security Considerations

- **Privilege step-down is structural, not conventional** (design.md 3.11): `OrgAdmin` and `TenantAccount` are two different methods with two different signatures; there is no shared method with a scope parameter a caller could pass incorrectly, and no code path anywhere in this package derives one scope's connection from the other's credential or cache entry.
- **Credentials never touch a concrete backend from this package's own code**: this package depends only on `secrets.Backend` (003), constructed and wrapped elsewhere; it cannot be the place a future backend-specific bug leaks a credential, because it never imports one.
- **Role is set explicitly, not inherited**: both scopes set `Role` on every connection they dial (`ORGADMIN`, `ACCOUNTADMIN`) rather than relying on whatever a user's default role happens to be — matching design.md 3.11's framing of the platform "impersonating the accountadmin role exclusively for that specific tenant" as a deliberate choice, not an accident of account defaults.
- **No credential material in an error message**: every error this package produces is built from a path's identifiers (namespace, account name, org-admin account), a host, and the underlying error — never a private key or any other credential content, matching 003's own rule for the paths it hands this package.
- **OCSP checking defaults to on**: `Snowflake.DisableOCSPChecks` (002) defaults to `false`; disabling it is a deliberate, narrow escape hatch for local/integration testing and emergencies where the OCSP responder's network path is broken — never a routine production setting.
- **The host a tenant is told to visit is the host the platform dials**: `host` handles no credentials and opens no connection, and both the `gosnowflake.Config.Host` here and `status.accountUrl` in 006 come out of the same function. A tenant's published URL therefore cannot name a host the platform does not itself connect to — a divergence that would otherwise send users to an endpoint outside the region's PrivateLink path while reconciliation reported success.
- **Health-probe failures surface immediately, not on a tenant's first real query**: probing a newly dialed connection turns a bad credential or an unreachable host into a system error at the moment this package first tries it, rather than letting it surface later inside whichever module (010–013, 015) happens to run the first real statement.

## Performance Considerations

- One dial and one health probe per distinct connection target for the life of the process — not per reconcile — regardless of whether the calling cadence for that target is minutes apart in steady state or sub-second during exponential backoff.
- The underlying `*sql.DB`'s own connection limits, idle timeout, and maximum lifetime are set explicitly (`BaseConfig.Snowflake`, see Edge Cases) rather than left at the standard library's unbounded defaults, so a process managing many tenant accounts at once has a bounded number of idle physical connections per account rather than an unbounded one.
- Cache lookups for an already-dialed key are lock-protected map reads with no network call. Locking is per key, never pool-wide: a network operation (a cold dial or a self-healing redial) blocks only other callers asking for that same key, so connecting a few thousand distinct accounts proceeds in parallel rather than serializing behind one lock.
- At a few thousand managed accounts, the cache itself (one map entry per account) costs a small, fixed amount of memory per entry — not a scaling concern on its own. The real capacity-planning input is the pod's open-file-descriptor limit, since each account's idle connection allowance (`BaseConfig.Snowflake.MaxIdleConnections`, see Edge Cases) multiplies by the number of actively-reconciled accounts.

## References

- **Product design**: `specs/design.md`, §3.6 (`CREATE ACCOUNT`, the locator, PrivateLink), §3.11 (organization vs. account-level privilege step-down), §3.11.1 (the tenant secret path this package's cache key mirrors), §3.12 (CRD name vs. resolved Snowflake name), §6.3 (`DROP ACCOUNT`), §7.2 (`status.accountUrl`, the form `host.URL` produces), Appendix B X1 (the `platform` service user).
- **SnowflakeAccount CRD (006, not yet written)**: `specs/scope-006-snowflake-account-crd.md` - `internal/tenant`, the second consumer of `internal/snowflake/host`, which builds `status.accountUrl` from `host.URL`.
- **Secrets Handling (003)**: `specs/003-secrets-handling.md` - `Backend`, `Path`, `NewOrgAdminPath()`, `NewTenantPath()`, `Credentials`, `UnmarshalCredentials()`.
- **Base Config (002)**: `specs/002-base-config.md` - `SnowflakeSettings`, in particular `OrgAdminAccountLocator`, `OrgAdminAccountRegion`, `UsePrivateLink`.
- **Snowflake SQL mechanics**: `specs/notes-snowflake-sql-mechanics.md` - the gosnowflake version (v1.18.1) this spec pins, and the reasoning 005 relies on for why it must consume this package's `*sql.DB` rather than construct its own.
- **Driver documentation**: `github.com/snowflakedb/gosnowflake` (`godoc`) - `Config`, `NewConnector`, `DSN`, `AuthTypeJwt`; consult the pinned version's source before implementation, per this repo's own convention of verifying vendor behavior rather than assuming it (see the Snowflake SQL mechanics note above).

<br/><br/><br/><br/><br/>

================

## Appendix: Usage Examples

### Example 1: Wiring the Pool in `cmd/provider/main.go` and Using a Tenant Connection (Primary Use Case)

```go
import (
    "context"
    "log"
    "time"

    "github.com/allianz/yukimi/internal/config"
    "github.com/allianz/yukimi/internal/secrets"
    secretsaws "github.com/allianz/yukimi/internal/secrets/aws"
    "github.com/allianz/yukimi/internal/snowflake/pool"
)

func main() {
    cfg, err := config.Load(*configDirFlag)
    if err != nil {
        log.Fatalf("failed to load base config: %v", err)
    }

    backend, err := secretsaws.New(cfg.AWS.Region, cfg.AWS.KmsKeyId)
    if err != nil {
        log.Fatalf("failed to construct AWS secrets backend: %v", err)
    }
    cached := secrets.NewCachedBackend(backend, 5*time.Minute)

    p := pool.New(cached, cfg)
    defer p.Close() // only Close() called here; nothing else in the process closes a pooled *sql.DB

    // ... later, inside a controller's Observe/Create/Update, once the account's
    // locator is known (design.md 3.6, 7.2):
    db, err := p.TenantAccount(context.Background(), "finance", "analytics-team-eu", "xc19114", "aws-eu-central-1")
    if err != nil {
        log.Fatalf("failed to get tenant connection: %v", err)
    }
    // db is a plain *sql.DB — usable directly today, and 005's injected
    // executor once that spec is written.
    row := db.QueryRowContext(context.Background(), "SELECT CURRENT_ROLE()")
    var role string
    _ = row.Scan(&role)
}
```

### Example 2: Evicting a Dropped Account's Connection (017's Integration)

```go
// In internal/deletion (017, not yet written), immediately after DROP ACCOUNT succeeds
import "github.com/allianz/yukimi/internal/snowflake/pool"

func (m *Module) dropAccount(ctx context.Context, p *pool.Pool, orgAdminDB *sql.DB, namespace, accountName, resolvedName string) error {
    if _, err := orgAdminDB.ExecContext(ctx, "DROP ACCOUNT "+resolvedName); err != nil {
        return fmt.Errorf("failed to drop account: %w", err)
    }
    p.EvictTenant(namespace, accountName) // no-op if never dialed; closes it if it was
    return nil
}
```

### Example 3: Testing `Pool` Against an Injected Dialer

```go
package pool

import (
    "context"
    "database/sql"
    "testing"

    "github.com/allianz/yukimi/internal/secrets"
)

// In pool_test.go: the package's own tests substitute the unexported dial
// seam so caching, eviction, and self-healing are testable without a real
// Snowflake account, network call, or driver.
func TestTenantAccount_CachesByKey(t *testing.T) {
    p := New(secrets.NewFakeBackend(), testConfig())
    dialCount := 0
    p.dial = func(cfg dialConfig) (*sql.DB, error) {
        dialCount++
        return sql.OpenDB(fakeConnector{}), nil // never actually connects
    }

    seedTenantCredential(t, p, "finance", "analytics-team-eu")

    ctx := context.Background()
    first, err := p.TenantAccount(ctx, "finance", "analytics-team-eu", "xc19114", "aws-eu-central-1")
    if err != nil {
        t.Fatalf("first call: %v", err)
    }
    second, err := p.TenantAccount(ctx, "finance", "analytics-team-eu", "xc19114", "aws-eu-central-1")
    if err != nil {
        t.Fatalf("second call: %v", err)
    }
    if first != second {
        t.Fatal("expected the same *sql.DB on a cache hit")
    }
    if dialCount != 1 {
        t.Fatalf("dialCount = %d, want 1", dialCount)
    }
}
```

### Example 4: Building `status.accountUrl` from the Same Host (006's Integration)

```go
// In internal/tenant (006, not yet written). The locator comes from CREATE ACCOUNT
// (010, design.md 3.6); the PrivateLink flag from BaseConfig (002), passed down by
// the controller (018). No pool, no driver, no configuration import.
import "github.com/allianz/yukimi/internal/snowflake/host"

func accountURL(locator, region string, usePrivateLink bool) (string, error) {
    url, err := host.URL(locator, region, usePrivateLink)
    if err != nil {
        return "", err // user error for a malformed region, reported on the CRD
    }
    return url, nil // https://xc19114.eu-central-1.privatelink.snowflakecomputing.com
}
```
