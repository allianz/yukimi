# Specification: Base Config (002)

## Overview

`internal/config/base/` loads the controller's base configuration — `base.yaml`, read from a mounted directory at startup — into an immutable `Config` struct. It carries the Snowflake organization identity plus exactly what's needed to reach the cloud provider the controller runs on and that provider's own secret manager. It is a plain file loader: no Kubernetes API calls, no CRD, no reconciliation. Failing fast here means a misconfigured deployment never reaches a reconcile loop.

## Scope

This specification defines the `internal/config/base/` package that:
- Loads `<configDir>/base.yaml` at startup, where `configDir` is a directory path resolved elsewhere (see Integration Points).
- Exposes the parsed, validated result as an immutable `Config` struct.
- Validates required fields, raising `errors.NewUserError` for missing or malformed values, so the process fails fast at startup rather than once per reconcile.

**Out of Scope**:
- No CRD, no controller, no reconciler, no Kubernetes watch. This is not a Crossplane `ProviderConfig`.
- No interpretation of any field's meaning. Fields owned by other components are checked for existence and shape only; e.g. whether `aws.region` names a real region is 003.a's concern, never this package's.
- No knowledge of environment variables, `.env`, or how a Makefile might materialize `base.yaml` for local development. `Load` only ever reads a file from disk.
- No credential fields of any kind, and no "auth mode" switch. Workload identity in-cluster versus environment-variable or profile credentials locally is resolved entirely inside the cloud SDK's own default credential chain (003.a); nothing in this package branches on where the controller runs.
- No check of `CloudProvider()`'s result against the set of backends actually compiled into the binary. That check — and the fatal rejection of a cloud section with no backend — belongs to `cmd/provider/main.go`, not this package.

## Key Concept: Shared Settings, Structural Validation Only

Almost every field in `base.yaml` belongs to another component — `aws.region` to 003.a, the `snowflake` block to 003, 004 and 006 — as will fields added later. One shared file for the whole controller weakens encapsulation deliberately: this package names fields it never reads, and in return a bad value fails once at startup instead of at each package's first reconcile.

What this package checks is therefore limited to structure: **existence** (present, non-empty) and **shape** (a regex, per the schema table below). Meaning stays with the owner — whether the value names something real, cross-field consistency, anything needing a network call. `Load` rejects `aws.region: "Frankfurt!"` on shape but accepts `aws.region: "xx-nowhere-9"`; only 003.a can reject that.

## Key Concept: Shared `--configDir`, Duplicated Loaders

`base.yaml` is one of several files this platform reads from a single mounted directory — sibling files will hold the Backplane Config (007) and the Guardrails config (008). All are addressed through one directory path, conventionally supplied to `cmd/provider/main.go` via a `--configDir` flag; this package takes only the resolved directory string, not the flag.

Each of those packages reads its own well-known filename from that directory independently. This package defines no shared multi-file loader, no common YAML-decoding helper, and no validation framework for the others to build on — the "open file → parse → validate" logic is fully duplicated across 002, 007 and 008. That is deliberate: the loaders are small and their validation rules differ enough that a shared abstraction would cost more than the duplication it removes.

## Public API

```go
// Config is the immutable, validated provider-wide configuration loaded at startup.
type Config struct {
    Snowflake SnowflakeSettings // organization identity plus connection-affecting settings
    AWS       AWSSettings       // consumed by 003.a; checked here for shape only
    Secrets   SecretsSettings   // consumed by whoever wraps a Backend in secrets.NewCachedBackend (003)
    Deletion  DeletionSettings  // the single deletion window every store derives its own from (003, 012)

    cloudProvider string // resolved by Load from the cloud section present; read via CloudProvider()
}

// CloudProvider returns the name of the cloud section the file carries — "aws", "azure", or
// "gcp" — found by scanning the top-level keys in document order. There is no cloudProvider
// key: an "aws:" section is itself the selection, so the two can never disagree. Resolved once
// by Load, which requires exactly one cloud section, so the result is never empty.
func (c *Config) CloudProvider() string

// SnowflakeSettings holds the Snowflake organization-level settings used across
// account identifiers, secret paths, and connection host construction.
type SnowflakeSettings struct {
    Org                    string // organization name; used in account identifiers, secret paths, and accountUrl
    OrgAdminAccount        string // account used for org-level operations
    OrgAdminAccountLocator string // Snowflake account locator for OrgAdminAccount (e.g. "xc19114"); static config because, unlike a tenant account, the controller never runs CREATE ACCOUNT for it (design.md 3.6)
    OrgAdminAccountRegion  string // Snowflake region OrgAdminAccount lives in, cloud-region form (e.g. "aws-eu-central-1" or "azure-westeurope"); paired with OrgAdminAccountLocator to build the org-admin connection host (004)
    UsePrivateLink         bool   // affects the connection host (004); defaults to true when omitted
    DisableOCSPChecks      bool   // disables OCSP certificate-revocation checking on Snowflake connections (004); testing/emergency use only. Defaults to false when omitted

    MaxConnectionPoolSize  int           // max open connections per pooled *sql.DB target (004); defaults to 10 when omitted
    MaxIdleConnections     int           // max idle connections kept per pooled *sql.DB target (004); defaults to 2 when omitted
    ConnectionMaxLifetime  time.Duration // max lifetime of a physical connection before it is recycled (004); defaults to 30m when omitted
    ConnectionMaxIdleTime  time.Duration // max time a physical connection may sit idle before being closed (004); defaults to 5m when omitted
    ConnectionProbeTimeout time.Duration // timeout for the health probe run on first dial (004); defaults to 10s when omitted

    AccountCreationGracePeriod time.Duration // how long a fresh account is given to become reachable before the first post-create connection attempt (012); defaults to 5m when omitted
}

// AWSSettings holds AWS-specific settings, consumed only by 003.a.
type AWSSettings struct {
    Region   string // optional here, shape-checked if set; an empty region is a user error in 003.a, not here
    KmsKeyId string // optional; reference to a customer-managed KMS key for encrypting/decrypting
                    // secrets in AWS Secrets Manager (003.a); shape-checked here only, not interpreted
}

// SecretsSettings holds settings for the secrets cache decorator (003), consumed by whoever
// wraps a Backend in secrets.NewCachedBackend — today cmd/provider/main.go.
type SecretsSettings struct {
    CacheTTL         time.Duration // TTL for the in-memory secrets cache (003); defaults to 5m when omitted
    RotationInterval time.Duration // age past which OrgAdmin/TenantAccount rotate a stored credential inline (004); defaults to 4320h (~6 months) when omitted
}

// DeletionSettings holds the one operator-owned deletion window. Both stores that reserve a
// tenant's deterministic identifier derive their own clock from it (003, 012), so a credential
// never outlives the account it belongs to.
type DeletionSettings struct {
    GracePeriodDays int // days a dropped account and its credential stay restorable (003, 012); defaults to 30 when omitted, allowed range 3-90
}

// Load reads, parses, and validates "<configDir>/base.yaml".
//
// Parameters:
//   - configDir: directory containing base.yaml (and, in a full deployment,
//     its sibling config files for 007/008 — this package reads only its own file)
//
// Returns:
//   - *Config: the validated configuration; never nil on a nil error
//   - User error if the file is missing, unreadable or not valid YAML; if the file does not
//     carry exactly one cloud section; or if any field violates the schema table below —
//     a required field empty, a value not matching its documented format, an integer or
//     day count out of range, or a duration that does not parse as a positive Go duration
//
// Load walks the parsed YAML's top-level keys to find the cloud sections, so a section with
// no Go struct yet (azure:, gcp:) is still recognized rather than silently dropped.
func Load(configDir string) (*Config, error)
```

## Schema Specification

### Fields (`base.yaml`)

| Field Path | Type | Required | Validation / Constraints |
| ---------- | ---- | -------- | ------------------------ |
| `snowflake.org` | string | **Yes** | Non-empty; matches `^[A-Za-z][A-Za-z0-9_]*$` (Snowflake identifier form, design.md 3.12). Used in account identifiers, secret paths, and `accountUrl` (design.md 3.11.1, 3.12, 7.2). |
| `snowflake.orgAdminAccount` | string | **Yes** | Non-empty; matches `^[A-Za-z][A-Za-z0-9_]*$`. Used in the org-admin secret path (design.md 3.11.1). |
| `snowflake.orgAdminAccountLocator` | string | **Yes** | Non-empty; matches `^[A-Za-z0-9]+$` (Snowflake account locator form, e.g. `xc19114`). Static because, unlike a tenant account, there is no `CREATE ACCOUNT` response to capture it from (design.md 3.6). Paired with `orgAdminAccountRegion` to build the org-admin connection host (004). |
| `snowflake.orgAdminAccountRegion` | string | **Yes** | Non-empty; matches `^(aws\|azure\|gcp)-[a-z][a-z0-9-]*$` — the cloud-region form used by the Backplane Config's region keys and the SnowflakeAccount CRD's `region` field (e.g. `aws-eu-central-1`, `azure-westeurope`; design.md 3.1, 3.5). |
| `snowflake.usePrivateLink` | bool | No | Affects the Snowflake connection host (design.md 3.6). Default: `true` when omitted. |
| `snowflake.disableOcspChecks` | bool | No | Disables OCSP certificate-revocation checking on Snowflake connections (004); testing/emergency use only. Default: `false` when omitted. |
| `snowflake.maxConnectionPoolSize` | int | No | Max open connections per pooled `*sql.DB` target (004). Must be a positive integer if set. Default: `10` when omitted. |
| `snowflake.maxIdleConnections` | int | No | Max idle connections kept per pooled `*sql.DB` target (004). Must not be negative if set. Default: `2` when omitted. |
| `snowflake.connectionMaxLifetime` | string (duration) | No | Max lifetime of a physical connection before it is recycled (004). Positive Go duration string (e.g. `30m`) if set. Default: `30m` when omitted. |
| `snowflake.connectionMaxIdleTime` | string (duration) | No | Max time a physical connection may sit idle before being closed (004). Positive Go duration string if set. Default: `5m` when omitted. |
| `snowflake.connectionProbeTimeout` | string (duration) | No | Timeout for 004's health probe run when a connection is first dialed. Positive Go duration string if set. Default: `10s` when omitted. |
| `snowflake.accountCreationGracePeriod` | string (duration) | No | How long a fresh account (012) is given to become reachable before the first post-create connection attempt. Positive Go duration string if set. Default: `5m` when omitted. |
| `aws` | object | **Yes**, or another cloud section | The cloud section for AWS. Its presence is what makes `CloudProvider()` return `"aws"`. Exactly one of `aws` / `azure` / `gcp` must be present — none or several is a user error. |
| `aws.region` | string | No | Not required here; if non-empty, matches `^[a-z]{2}(-[a-z]+)+-[0-9]$`. Whether the region exists and whether it is required at all is decided by 003.a's constructor. |
| `aws.kmsKeyId` | string | No | Optional reference to a customer-managed KMS key used by 003.a in place of the AWS-managed default. If non-empty, must match one of the documented KMS identifier forms: bare key ID, `alias/<name>`, key ARN, or alias ARN. Whether the key exists or is usable is 003.a's concern. |
| `secrets.cacheTtl` | string (duration) | No | TTL for the in-memory secrets cache (003), applied by whichever code wraps a `Backend` in `secrets.NewCachedBackend`. Positive Go duration string if set. Default: `5m` when omitted. |
| `secrets.rotationInterval` | string (duration) | No | Age past which 004 rotates a stored Snowflake credential inline. Positive Go duration string if set (e.g. `1s` for tests). Default: `4320h` (~6 months) when omitted. |
| `deletion.gracePeriodDays` | int | No | Days a dropped tenant account and its stored credential stay restorable (003, 012). Must be `3`–`90` inclusive if set — Snowflake's own documented bounds. Default: `30` when omitted. Not overridable per request; see 019. |

Every field is freely editable and the whole file is reloaded wholesale on the next pod restart, so there is no per-field mutability rule to enforce.

## Error Classification

**User Errors** (use `errors.NewUserError()`), one per violated schema rule, each naming the field path and the offending value:

| Rule violated | Message form |
| ------------- | ------------ |
| File missing | `base.yaml not found in <configDir>` |
| Not valid YAML | `failed to parse base.yaml: <parse error>` |
| Required field empty or absent | `snowflake.org is required in base.yaml` |
| Value fails its documented regex | `aws.region 'Frankfurt!' does not match the expected format (expected: eu-central-1)` |
| No cloud section | `base.yaml must contain one cloud section (one of: aws, azure, gcp)` |
| Several cloud sections | `base.yaml contains several cloud sections (aws, azure); exactly one is allowed` |
| Integer out of range | `snowflake.maxConnectionPoolSize '0' must be a positive integer`, `snowflake.maxIdleConnections '-1' must not be negative`, `deletion.gracePeriodDays '91' must be between 3 and 90` |
| Duration unparseable | `snowflake.connectionMaxLifetime 'not-a-duration' does not match the expected format (expected: a Go duration string, e.g. 30m)` |
| Duration not positive | `snowflake.connectionMaxLifetime '0s' must be a positive duration` |

The shape message names the format by example, so the expected form is readable without consulting the regex: `orgAdminAccountLocator` suggests `xc19114`, `orgAdminAccountRegion` suggests `aws-eu-central-1 or azure-westeurope`, `kmsKeyId` suggests `a KMS key ID, alias, or ARN, e.g. alias/my-key`.

**System Errors**: this package makes no network calls and has no retryable infrastructure dependency, so it classifies no scenario as a system error on its own. An unexpected filesystem error (e.g. a permissions problem on the mounted volume) surfaces as a raw wrapped error (`fmt.Errorf("reading base.yaml: %w", err)`); the caller's error handling (001) treats it as a system error by default, since `Load` never wraps it in `errors.NewUserError`. This is intentionally minimal — this package does not attempt to distinguish every possible OS-level failure mode.

## Edge Cases

- **What happens when an optional key is omitted?** - It takes the default in the schema table; each field defaults independently. Omitting a whole section (`secrets:`, `deletion:`) is identical to omitting every key in it, and a section present but empty is indistinguishable from absent — the decoder yields the same zero-value struct either way, and there is no reason to tell them apart.
- **What if a field's value is well-formed but wrong — `aws.region: xx-nowhere-9`, a locator that doesn't exist, a region Snowflake doesn't offer, a KMS key that isn't accessible?** - `Load` accepts all of them. Shape is all this package can judge; the owning component fails on first use, and for a locator or region that can only be 004's first connection attempt.
- **What if `aws.region` is absent while `aws:` is present?** - `Load` accepts it; requiring a region is 003.a's call, and its constructor rejects the empty value as a user error.
- **What happens if `deletion.gracePeriodDays` is set below `7`?** - `Load` accepts anything in `3`–`90`; the consequence lands in 003. AWS Secrets Manager's shortest representable recovery window is 7 days, so a shorter grace period leaves it no compliant window at all and it destroys the credential irreversibly rather than outliving the account. That is a deliberate 003 outcome, not a 002 validation error.
- **What happens with an unrecognized YAML key (e.g. a future `timeout`)?** - Ignored by the decoder, not an error. The schema is expected to grow, and unknown keys must not break `Load` on a rolling deployment.
- **What if no cloud section is present, or more than one?** - Both are user errors from `Load`: exactly one of `aws` / `azure` / `gcp` is required, so `CloudProvider()` is always unambiguous.
- **What if the cloud section has no backend compiled in (e.g. `azure:` today)?** - `Load` accepts it and `CloudProvider()` returns `"azure"` — this package has no notion of which backends exist. `cmd/provider/main.go` is the one that fails fast, listing the cloud providers actually compiled in.
- **What differs when the controller runs outside the cluster (local development)?** - Nothing in this package. `Load` reads and validates `base.yaml` identically either way; only the cloud SDK's underlying credential resolution differs beneath 003.a, and that difference is invisible here.

## Project Structure

```text
internal/config/base/
├── base.go        # Config, SnowflakeSettings, AWSSettings, Load()
└── base_test.go   # Unit tests
```

## Dependencies

- **`internal/errors` (001)** - Used APIs: `errors.NewUserError()` - Contract: none; `internal/config/base` has no other internal dependency, making it a leaf package.

## Integration Points

- **`cmd/provider/main.go`** - Owns the `--configDir` flag and resolves it to a directory path. Calls `base.Load(configDir)` once at startup, then switches on `Config.CloudProvider()` to construct the matching secrets backend, fatally rejecting an unrecognized value by listing the cloud providers compiled in. Also reads `Config.Secrets.CacheTTL` and passes it to `secrets.NewCachedBackend(backend, cfg.Secrets.CacheTTL)` (003) — `internal/secrets` itself never imports `internal/config/base` - Key functions: `base.Load()`, `Config.CloudProvider()`.
- **`internal/secrets/aws` (003.a)** - Consumes `Config.AWS.Region` when constructed by `main.go`; rejects an empty region as a user error itself, since 002 does not validate it. Also optionally consumes `Config.AWS.KmsKeyId`, passing it through to `CreateSecret`'s `KmsKeyId` parameter when non-empty, so Secrets Manager encrypts/decrypts with the customer-managed key instead of its AWS-managed default - Notes: credentials come from the AWS SDK's default chain, never from `Config`.
- **`internal/snowflake/pool` (004)** - Consumes `Config.Snowflake.Org`, `OrgAdminAccount`, `OrgAdminAccountLocator`, `OrgAdminAccountRegion`, `UsePrivateLink`, and `DisableOCSPChecks` for org-admin connection host/config construction (design.md 3.6, 3.11), plus `MaxConnectionPoolSize`, `MaxIdleConnections`, `ConnectionMaxLifetime`, `ConnectionMaxIdleTime`, and `ConnectionProbeTimeout` to tune every pooled `*sql.DB`, and `Config.Secrets.RotationInterval` to decide when a stored credential is rotated inline.
- **Account Module (012)** - Consumes `Config.Snowflake.AccountCreationGracePeriod`, passed to `accountmodule.New` as a plain `time.Duration` — this module never loads the config file itself. Also consumes `Config.Deletion.GracePeriodDays`, rendered verbatim as `DROP ACCOUNT ... GRACE_PERIOD_IN_DAYS` on teardown.
- **`internal/secrets` (003) / its backends** - Consume `Config.Deletion.GracePeriodDays` as the ceiling for `secrets.DeriveRecoveryWindow`, which each backend calls once at construction with the day band it can represent (003.a: 7–30). `internal/secrets` never imports `internal/config/base`; `main.go` passes the number in.
- **`internal/config/backplane` (007)** / **guardrails loader (008)** - Read their own sibling files (`backplane.yaml`, a guardrails/exceptions file) from the same `--configDir`, with independently implemented loading and validation logic — no code shared with `internal/config/base`.

## Success Criteria

- **SC-001**: `Load` returns a populated `*Config` for a well-formed `base.yaml`.
- **SC-002**: `Load` returns a user error when `<configDir>/base.yaml` does not exist.
- **SC-003**: `Load` returns a user error when the file is not valid YAML.
- **SC-004**: `Load` returns a user error when any required field — `snowflake.org`, `orgAdminAccount`, `orgAdminAccountLocator`, `orgAdminAccountRegion` — is empty or absent.
- **SC-005**: `Load` returns a user error when a required field violates its documented shape: `org` or `orgAdminAccount` outside the Snowflake identifier form (`my-org`), a malformed locator (`xc-19114!`), a malformed region (`Frankfurt!`), or a region missing its cloud prefix (`eu-central-1`). It accepts a well-formed region under any of the three cloud prefixes (e.g. `azure-westeurope`).
- **SC-006**: `Load` returns a user error when the file carries no cloud section, and another when it carries more than one.
- **SC-007**: `CloudProvider()` returns the name of whichever single cloud section is present — including `"azure"`, whose backend is not compiled in — regardless of where that section sits among the file's top-level keys.
- **SC-008**: `Load` accepts an absent `aws.region`, accepts a well-formed but non-existent one (`xx-nowhere-9`), and returns a user error for a malformed one (`Frankfurt!`).
- **SC-009**: `Load` accepts an absent `aws.kmsKeyId`, accepts each well-formed KMS identifier form (bare key ID, `alias/<name>`, key ARN, alias ARN), and returns a user error for a malformed one.
- **SC-010**: `Load` defaults `Snowflake.UsePrivateLink` to `true` and `Snowflake.DisableOCSPChecks` to `false` when the keys are omitted, and honors an explicit value for each when given.
- **SC-011**: For every optional integer and duration field, `Load` applies the schema table's default when the key is omitted and honors an explicit value when given: `MaxConnectionPoolSize` `10`, `MaxIdleConnections` `2`, `ConnectionMaxLifetime` `30m`, `ConnectionMaxIdleTime` `5m`, `ConnectionProbeTimeout` `10s`, `AccountCreationGracePeriod` `5m`, `Secrets.CacheTTL` `5m`, `Secrets.RotationInterval` `4320h`.
- **SC-012**: `Load` returns a user error when `snowflake.maxConnectionPoolSize` is not a positive integer, or when `snowflake.maxIdleConnections` is negative.
- **SC-013**: For every duration field, `Load` returns a user error when the value does not parse as a Go duration string, and another when it parses to a non-positive duration.
- **SC-014**: `Load` defaults `Deletion.GracePeriodDays` to `30` when the `deletion:` section is absent and when it is present but empty, and honors an explicit value at either end of the band (`3`, `90`).
- **SC-015**: `Load` returns a user error when `deletion.gracePeriodDays` lies outside `3`–`90` inclusive (`-1`, `0`, `2`, `91`).
- **SC-016**: An unrecognized top-level YAML key does not cause `Load` to fail.
- **SC-017**: The returned `*Config` is safe for concurrent read-only use by multiple goroutines after `Load` returns.
- **SC-018**: `internal/config/base` imports only `internal/errors` among this repository's packages.
- **SC-019**: Unit test coverage exceeds 95%.

## References

- **Config Package**: `internal/config/base/base.go` - `Config`, `SnowflakeSettings`, `AWSSettings`, `SecretsSettings`, `DeletionSettings`, `Load`
- **Design Doc**: `specs/design.md`, §3.11.1 - the AWS Secrets Manager path grammar that consumes `Snowflake.Org`

<br/><br/><br/><br/><br/>

================

## Appendix: Usage Examples

### Example 1: Loading Config and Selecting a Secrets Backend (Primary Use Case)

```go
// In cmd/provider/main.go
import (
    "fmt"
    "log"

    "github.com/allianz/yukimi/internal/config/base"
    "github.com/allianz/yukimi/internal/secrets"
    secretsaws "github.com/allianz/yukimi/internal/secrets/aws"
)

func main() {
    configDir := flag.String("configDir", "/etc/yukimi/config", "directory containing base.yaml and sibling config files")
    flag.Parse()

    cfg, err := base.Load(*configDir)
    if err != nil {
        log.Fatalf("failed to load base config: %v", err)
    }

    var backend secrets.Backend
    switch cfg.CloudProvider() {
    case "aws":
        backend, err = secretsaws.New(cfg.AWS.Region, cfg.AWS.KmsKeyId, cfg.Deletion.GracePeriodDays)
        if err != nil {
            log.Fatalf("failed to construct AWS secrets backend: %v", err)
        }
    default:
        log.Fatalf("no secrets backend compiled in for cloud section %q (compiled in: aws)", cfg.CloudProvider())
    }

    cached := secrets.NewCachedBackend(backend, cfg.Secrets.CacheTTL) // 003

    // ... wire cached into the pool (004) and start the controller manager
}
```

### Example 2: A `base.yaml` Fixture

```yaml
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: aws-eu-central-1
  usePrivateLink: true
  # disableOcspChecks: false        # optional, default shown (004); testing/emergency use only
  # maxConnectionPoolSize: 10       # optional, default shown (004)
  # maxIdleConnections: 2           # optional, default shown (004)
  # connectionMaxLifetime: 30m      # optional, default shown (004)
  # connectionMaxIdleTime: 5m       # optional, default shown (004)
  # connectionProbeTimeout: 10s     # optional, default shown (004)
  # accountCreationGracePeriod: 5m  # optional, default shown (012)

aws:
  region: eu-central-1
  # kmsKeyId: alias/yukimi-secrets  # optional, customer-managed KMS key

# secrets:
#   cacheTtl: 5m                    # optional, default shown (003)
#   rotationInterval: 4320h         # optional, default shown (004)

# deletion:
#   gracePeriodDays: 30             # optional, default shown (003, 012); allowed range 3-90
```

The `aws:` section is the only cloud section here, so `CloudProvider()` returns `"aws"` — nothing else in the file states the provider. The same file and the same `Load` call serve production (a mounted ConfigMap volume) and local development (materialized by the Makefile, out of scope here); neither can tell the difference.
