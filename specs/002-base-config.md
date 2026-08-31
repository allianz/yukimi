# Specification: Base Config (002)

## Overview

`internal/config/` loads the controller's base configuration — `baseConfig.yaml`, read from a mounted directory at startup — into an immutable `BaseConfig` struct. It carries the Snowflake organization identity plus exactly what's needed to reach the cloud provider the controller runs on and that provider's own secret manager. It is a plain file loader: no Kubernetes API calls, no CRD, no reconciliation. Failing fast here means a misconfigured deployment never reaches a reconcile loop.

## Scope

This specification defines the `internal/config/` package that:
- Loads `<configDir>/baseConfig.yaml` at startup, where `configDir` is a directory path resolved elsewhere (see Integration Points).
- Exposes the parsed, validated result as an immutable `BaseConfig` struct.
- Validates required fields, raising `errors.NewUserError` for missing or malformed values, so the process fails fast at startup rather than once per reconcile.

**Out of Scope**:
- No CRD, no controller, no reconciler, no Kubernetes watch. This is not a Crossplane `ProviderConfig`.
- No interpretation of any field's meaning. Fields owned by other components are checked for existence and shape only; e.g. whether `aws.region` names a real region is 003.a's concern, never this package's.
- No knowledge of environment variables, `.env`, or how a Makefile might materialize `baseConfig.yaml` for local development. `Load` only ever reads a file from disk.
- No credential fields of any kind. Workload identity vs. local environment-variable/profile credentials is resolved entirely inside the cloud SDK's own default credential chain (003.a) — never modeled as a `BaseConfig` field or an explicit "auth mode" switch.
- No check of `CloudProvider()`'s result against the set of backends actually compiled into the binary. That check — and the fatal rejection of a cloud section with no backend — belongs to `cmd/provider/main.go`, not this package.

## Key Concept: Shared Settings, Structural Validation Only

Almost every field in `baseConfig.yaml` belongs to another component — `aws.region` to 003.a, the `snowflake` block to 003, 004 and 006 — as will fields added later, say a `snowflake.maxConnectionPoolSize` for 004. One shared file for the whole controller weakens encapsulation deliberately: this package names fields it never reads, and in return a bad value fails once at startup instead of at each package's first reconcile.

What this package checks is therefore limited to structure: **existence** (present, non-empty) and **shape** (a regex, per the schema table below). Meaning stays with the owner — whether the value names something real, cross-field consistency, anything needing a network call. `Load` rejects `aws.region: "Frankfurt!"` on shape but accepts `aws.region: "xx-nowhere-9"`; only 003.a can reject that.

## Key Concept: Shared `--configDir`, Duplicated Loaders

`baseConfig.yaml` is one of several files this platform reads from a single mounted directory — sibling files will hold the Backplane Config (007) and the Guardrails / Approved Exceptions config (008). All of them are addressed through one directory path, conventionally supplied to `cmd/provider/main.go` via a `--configDir` flag; `internal/config` itself takes only the resolved directory string, not the flag.

Each of those packages reads its own well-known filename from that shared directory independently. `internal/config` defines no shared "multi-file config loader" interface, no common YAML-decoding helper, and no validation framework for the others to build on. Each loader's "open file → parse → validate" logic is fully duplicated across 002, 007, and 008. This is deliberate: the loaders are small, and their validation rules differ enough — different required fields, different failure modes — that a shared abstraction would cost more to maintain than the duplication it would remove.

## Key Concept: Credentials Are Never a `BaseConfig` Field

`Load` behaves identically regardless of where the controller runs — nothing in this package branches on environment. What differs between production and local development is how the cloud SDK resolves credentials underneath the secrets backend (003.a), entirely outside `BaseConfig`'s schema:

- **In-cluster (production)**: the controller runs as a pod with workload identity (IRSA for AWS). The AWS SDK's default credential provider chain picks up the projected service-account token automatically — no configuration from this package is involved.
- **Local development**: the controller runs outside the Kubernetes cluster, so no workload identity exists. Credentials instead come from environment variables (`AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_SESSION_TOKEN`) or `AWS_PROFILE`. The *same* default credential chain simply falls through to them, since no IRSA metadata is present outside the cluster. How those environment variables are populated — including a Makefile copying `.env` values into `baseConfig.yaml` or the shell environment for local runs — is tooling, out of scope for this spec.

Because both cases go through the same SDK-internal chain, the switch between workload identity and local credentials is never a setting anyone writes into `baseConfig.yaml`. `BaseConfig.AWS` carries only `Region` and the optional `KmsKeyId` reference described above — never credentials, never an explicit "auth mode" flag. 003.a's constructor calls the SDK's default chain and nothing else, so the identical code path resolves to workload identity or environment-variable credentials purely based on what the process finds at startup. This package's only responsibility is making sure `baseConfig.yaml` loads the same way no matter which environment the binary runs in.

## Public API

```go
// BaseConfig is the immutable, validated provider-wide configuration loaded at startup.
type BaseConfig struct {
    Snowflake SnowflakeSettings // organization identity plus connection-affecting settings
    AWS       AWSSettings       // consumed by 003.a; checked here for shape only
    Secrets   SecretsSettings   // consumed by whoever wraps a Backend in secrets.NewCachedBackend (003)

    cloudProvider string // resolved by Load from the cloud section present; read via CloudProvider()
}

// CloudProvider returns the name of the cloud section the file carries — "aws", "azure", or
// "gcp" — found by scanning the top-level keys in document order. There is no cloudProvider
// key: an "aws:" section is itself the selection, so the two can never disagree. Resolved once
// by Load, which requires exactly one cloud section, so the result is never empty.
func (c *BaseConfig) CloudProvider() string

// SnowflakeSettings holds the Snowflake organization-level settings used across
// account identifiers, secret paths, and connection host construction.
type SnowflakeSettings struct {
    Org                    string // organization name; used in account identifiers, secret paths, and accountUrl
    OrgAdminAccount        string // account used for org-level operations
    OrgAdminAccountLocator string // Snowflake account locator for OrgAdminAccount (e.g. "xc19114"); static config because, unlike a tenant account, the controller never runs CREATE ACCOUNT for it (design.md 3.6)
    OrgAdminAccountRegion  string // Snowflake region OrgAdminAccount lives in, cloud-region form (e.g. "aws-eu-central-1" or "azure-westeurope"); paired with OrgAdminAccountLocator to build the org-admin connection host (004)
    UsePrivateLink         bool   // affects the connection host (004); defaults to true when omitted
    DisableOCSPChecks      bool   // disables OCSP certificate-revocation checking on Snowflake connections (004); testing/emergency use only. Defaults to false when omitted

    MaxConnectionPoolSize      int           // max open connections per pooled *sql.DB target (004); defaults to 10 when omitted
    MaxIdleConnections         int           // max idle connections kept per pooled *sql.DB target (004); defaults to 2 when omitted
    ConnectionMaxLifetime      time.Duration // max lifetime of a physical connection before it is recycled (004); defaults to 30m when omitted
    ConnectionMaxIdleTime      time.Duration // max time a physical connection may sit idle before being closed (004); defaults to 5m when omitted
    ConnectionProbeTimeout     time.Duration // timeout for the health probe run on first dial (004); defaults to 10s when omitted
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

// Load reads, parses, and validates "<configDir>/baseConfig.yaml".
//
// Parameters:
//   - configDir: directory containing baseConfig.yaml (and, in a full deployment,
//     its sibling config files for 007/008 — this package reads only its own file)
//
// Returns:
//   - *BaseConfig: the validated configuration; never nil on a nil error
//   - User error if the file is missing, unreadable, not valid YAML, a required field
//     (Snowflake.Org, Snowflake.OrgAdminAccount, Snowflake.OrgAdminAccountLocator,
//     Snowflake.OrgAdminAccountRegion) is empty, the file does not carry exactly
//     one cloud section, a field's value does not match its documented format, a pool-tuning
//     integer (MaxConnectionPoolSize, MaxIdleConnections) is out of range, or a duration field
//     (ConnectionMaxLifetime, ConnectionMaxIdleTime, ConnectionProbeTimeout, Secrets.CacheTTL,
//     Secrets.RotationInterval) does not parse as a positive Go duration
//
// Load walks the parsed YAML's top-level keys to find the cloud sections, so a section with
// no Go struct yet (azure:, gcp:) is still recognized rather than silently dropped.
func Load(configDir string) (*BaseConfig, error)
```

## Schema Specification

Every field in `baseConfig.yaml` is freely editable and the whole file is reloaded wholesale on the next pod restart — there is no per-field mutability rule to enforce, so the table below omits a Mutability column.

### Fields (`baseConfig.yaml`)

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
| `snowflake.connectionMaxLifetime` | string (duration) | No | Max lifetime of a physical connection before it is recycled (004). Must be a positive Go duration string (e.g. `30m`) if set. Default: `30m` when omitted. |
| `snowflake.connectionMaxIdleTime` | string (duration) | No | Max time a physical connection may sit idle before being closed (004). Must be a positive Go duration string if set. Default: `5m` when omitted. |
| `snowflake.connectionProbeTimeout` | string (duration) | No | Timeout for 004's health probe run when a connection is first dialed. Must be a positive Go duration string if set. Default: `10s` when omitted. |
| `aws` | object | **Yes**, or another cloud section | The cloud section for AWS. Its presence is what makes `CloudProvider()` return `"aws"`. Exactly one of `aws` / `azure` / `gcp` must be present — none or several is a user error. |
| `aws.region` | string | No | Not required here; if non-empty, matches `^[a-z]{2}(-[a-z]+)+-[0-9]$`. Whether the region exists and whether it is required at all is decided by 003.a's constructor. |
| `aws.kmsKeyId` | string | No | Optional reference to a customer-managed KMS key (key ID, alias, or ARN) used by 003.a when creating/reading secrets in AWS Secrets Manager, in place of the AWS-managed default. Not required here; if non-empty, must match one of the documented KMS identifier forms (bare key ID, `alias/<name>`, key ARN, or alias ARN). Whether the key exists or is usable is 003.a's concern, never this package's. |
| `secrets.cacheTtl` | string (duration) | No | TTL for the in-memory secrets cache (003), applied by whichever code wraps a `Backend` in `secrets.NewCachedBackend` (`cmd/provider/main.go`). Must be a positive Go duration string if set. Default: `5m` when omitted. |
| `secrets.rotationInterval` | string (duration) | No | Age past which 004 rotates a stored Snowflake credential inline. Must be a positive Go duration string if set (e.g. `1s` for tests). Default: `4320h` (~6 months) when omitted. |

## Project Structure

```text
internal/config/
├── config.go        # BaseConfig, SnowflakeSettings, AWSSettings, Load()
└── config_test.go   # Unit tests
```

## Error Classification

**User Errors** (use `errors.NewUserError()`):
- Missing file: `baseConfig.yaml not found in <configDir>`
- Malformed YAML: `failed to parse baseConfig.yaml: <parse error>`
- Missing required field: `snowflake.org is required in baseConfig.yaml`
- Missing required field: `snowflake.orgAdminAccount is required in baseConfig.yaml`
- Missing required field: `snowflake.orgAdminAccountLocator is required in baseConfig.yaml`
- Missing required field: `snowflake.orgAdminAccountRegion is required in baseConfig.yaml`
- Malformed value: `snowflake.orgAdminAccountLocator 'xc-19114!' does not match the expected format (expected: xc19114)`
- Malformed value: `snowflake.orgAdminAccountRegion 'Frankfurt!' does not match the expected format (expected: aws-eu-central-1 or azure-westeurope)`
- Malformed value: `snowflake.orgAdminAccountRegion 'eu-central-1' does not match the expected format (expected: aws-eu-central-1 or azure-westeurope)` — a region missing its cloud prefix
- Malformed value: `aws.region 'Frankfurt!' does not match the expected format (expected: eu-central-1)` — and likewise for any other field with a documented regex
- Malformed value: `aws.kmsKeyId 'not a key!' does not match the expected format (expected: a KMS key ID, alias, or ARN, e.g. alias/my-key)`
- No cloud section: `baseConfig.yaml must contain one cloud section (one of: aws, azure, gcp)`
- Several cloud sections: `baseConfig.yaml contains several cloud sections (aws, azure); exactly one is allowed`
- Out-of-range pool-tuning integer: `snowflake.maxConnectionPoolSize '0' must be a positive integer`, `snowflake.maxIdleConnections '-1' must not be negative`
- Malformed duration: `snowflake.connectionMaxLifetime 'not-a-duration' does not match the expected format (expected: a Go duration string, e.g. 30m)` — and likewise for `connectionMaxIdleTime`, `connectionProbeTimeout`, and `secrets.cacheTtl`
- Non-positive duration: `snowflake.connectionMaxLifetime '0s' must be a positive duration` — and likewise for `connectionMaxIdleTime`, `connectionProbeTimeout`, and `secrets.cacheTtl`

**System Errors**: this package makes no network calls and has no retryable infrastructure dependency, so it classifies no scenario as a system error on its own. An unexpected filesystem error (e.g. a permissions problem on the mounted volume) surfaces as a raw wrapped error (`fmt.Errorf("reading baseConfig.yaml: %w", err)`); the caller's error handling (001) treats it as a system error by default, since `Load` never wraps it in `errors.NewUserError`. This is intentionally minimal — this package does not attempt to distinguish every possible OS-level failure mode.

## Edge Cases

- **What happens if `snowflake.usePrivateLink` is omitted?** - Defaults to `true`.
- **What happens if `snowflake.disableOcspChecks` is omitted?** - Defaults to `false`; OCSP certificate-revocation checks stay on.
- **What happens if `snowflake.maxConnectionPoolSize`, `maxIdleConnections`, `connectionMaxLifetime`, `connectionMaxIdleTime`, or `connectionProbeTimeout` is omitted?** - Each defaults independently: `10`, `2`, `30m`, `5m`, `10s` respectively.
- **What happens if `secrets.cacheTtl` is omitted?** - Defaults to `5m`.
- **What happens with an unrecognized YAML key (e.g. a future `timeout`)?** - Ignored by the decoder, not an error. The schema is expected to grow over time (timeouts, pool sizes, and similar settings may be added later as the codebase needs them), and unknown keys must not break `Load` on a rolling deployment.
- **What if no cloud section is present, or more than one?** - Both are user errors from `Load`: exactly one of `aws` / `azure` / `gcp` is required, so `CloudProvider()` is always unambiguous.
- **What if the cloud section has no backend compiled in (e.g. `azure:` today)?** - `Load` accepts it and `CloudProvider()` returns `"azure"` — this package has no notion of which backends exist. `cmd/provider/main.go` is the one that fails fast, listing the cloud providers actually compiled in.
- **What if `aws.region` is absent while `aws:` is present?** - `Load` accepts it; requiring a region is 003.a's call, and its constructor rejects the empty value as a user error.
- **What if a field's value is well-formed but wrong (e.g. `aws.region: xx-nowhere-9`)?** - `Load` accepts it. Shape is all this package can judge; the owning component fails on first use.
- **What if `orgAdminAccountLocator`/`orgAdminAccountRegion` is well-formed but not real (e.g. a locator that doesn't exist, or a region Snowflake doesn't offer)?** - `Load` accepts it. Shape is all this package can judge; realness can only be discovered on 004's first connection attempt.
- **What happens if `aws.kmsKeyId` is omitted?** - `Load` accepts it; 003.a passes no `KmsKeyId` to AWS Secrets Manager, which falls back to its AWS-managed default key. The feature is opt-in.
- **What if `aws.kmsKeyId` is malformed (e.g. `aws.kmsKeyId: "not a key!"`)?** - A user error at `Load`, exactly like a malformed `aws.region`. Whether a well-formed but non-existent or inaccessible key is rejected is 003.a's concern at first use, not this package's.
- **What differs when the controller runs outside the cluster (local development)?** - Nothing in this package. `Load` reads and validates `baseConfig.yaml` identically either way; only the AWS SDK's underlying credential resolution differs beneath 003.a (see Key Concept above), and that difference is invisible to `internal/config`.

## Dependencies

- **`internal/errors` (001)** - Used APIs: `errors.NewUserError()` - Contract: none; `internal/config` has no other internal dependency, making it a leaf package.

## Integration Points

- **`cmd/provider/main.go`** - Owns the `--configDir` flag and resolves it to a directory path. Calls `config.Load(configDir)` once at startup, then switches on `BaseConfig.CloudProvider()` to construct the matching secrets backend, fatally rejecting an unrecognized value by listing the cloud providers compiled in. Also reads `BaseConfig.Secrets.CacheTTL` and passes it to `secrets.NewCachedBackend(backend, cfg.Secrets.CacheTTL)` (003) — `internal/secrets` itself never imports `internal/config` - Key functions: `config.Load()`, `BaseConfig.CloudProvider()`.
- **`internal/secrets/aws` (003.a)** - Consumes `BaseConfig.AWS.Region` when constructed by `main.go`; rejects an empty region as a user error itself, since 002 does not validate it. Also optionally consumes `BaseConfig.AWS.KmsKeyId`, passing it through to `CreateSecret`'s `KmsKeyId` parameter when non-empty, so Secrets Manager encrypts/decrypts with the customer-managed key instead of its AWS-managed default - Notes: credentials come from the AWS SDK's default chain, never from `BaseConfig`.
- **`internal/snowflake/pool` (004)** - Consumes `BaseConfig.Snowflake.Org`, `OrgAdminAccount`, `OrgAdminAccountLocator`, `OrgAdminAccountRegion`, `UsePrivateLink`, and `DisableOCSPChecks` for org-admin connection host/config construction (design.md 3.6, 3.11), plus `MaxConnectionPoolSize`, `MaxIdleConnections`, `ConnectionMaxLifetime`, `ConnectionMaxIdleTime`, and `ConnectionProbeTimeout` to tune every pooled `*sql.DB`.
- **`internal/backplane` (007)** / **guardrails loader (008)** - Read their own sibling files (`backplane.yaml`, a guardrails/exceptions file) from the same `--configDir`, with independently implemented loading and validation logic — no code shared with `internal/config`.

## Success Criteria

- **SC-001**: `Load` returns a populated `*BaseConfig` for a well-formed `baseConfig.yaml`.
- **SC-002**: `Load` returns a user error when `<configDir>/baseConfig.yaml` does not exist.
- **SC-003**: `Load` returns a user error when the file is not valid YAML.
- **SC-004**: `Load` returns a user error when `snowflake.org` is empty or absent.
- **SC-005**: `Load` returns a user error when `snowflake.orgAdminAccount` is empty or absent.
- **SC-006**: `Load` returns a user error when the file carries no cloud section, and another when it carries more than one.
- **SC-007**: `Load` defaults `Snowflake.UsePrivateLink` to `true` when the key is omitted.
- **SC-008**: `CloudProvider()` returns `"aws"` for a file whose only cloud section is `aws:`, and `"azure"` for one whose only cloud section is `azure:` — a section with no compiled-in backend is not rejected here.
- **SC-009**: `CloudProvider()` returns the same value regardless of where the cloud section sits among the file's top-level keys.
- **SC-010**: `Load` accepts an absent `aws.region`, accepts a well-formed but non-existent one (`xx-nowhere-9`), and returns a user error for a malformed one (`Frankfurt!`).
- **SC-010a**: `Load` returns a user error when `snowflake.org` or `snowflake.orgAdminAccount` contains characters outside the Snowflake identifier form (e.g. `my-org`).
- **SC-011**: An unrecognized top-level YAML key does not cause `Load` to fail.
- **SC-012**: The returned `*BaseConfig` is safe for concurrent read-only use by multiple goroutines after `Load` returns.
- **SC-013**: `internal/config` imports only `internal/errors` among this repository's packages.
- **SC-014**: Unit test coverage exceeds 95%.
- **SC-015**: `Load` accepts an absent `aws.kmsKeyId`, accepts each well-formed KMS identifier form (bare key ID, `alias/<name>`, key ARN, alias ARN), and returns a user error for a malformed one.
- **SC-016**: `Load` returns a user error when `snowflake.orgAdminAccountLocator` is empty or absent.
- **SC-017**: `Load` returns a user error when `snowflake.orgAdminAccountRegion` is empty or absent.
- **SC-018**: `Load` returns a user error when `snowflake.orgAdminAccountLocator` or `snowflake.orgAdminAccountRegion` contains characters outside their documented shape, accepts a well-formed region under any of the three recognized cloud prefixes (e.g. `azure-westeurope`), and rejects one missing its cloud prefix (e.g. `eu-central-1`).
- **SC-019**: `Load` defaults `Snowflake.MaxConnectionPoolSize`, `MaxIdleConnections`, `ConnectionMaxLifetime`, `ConnectionMaxIdleTime`, `ConnectionProbeTimeout`, and `Secrets.CacheTTL` to `10`, `2`, `30m`, `5m`, `10s`, and `5m` respectively when each key is omitted, and honors an explicit value for each when given.
- **SC-020**: `Load` returns a user error when `snowflake.maxConnectionPoolSize` is not a positive integer, or when `snowflake.maxIdleConnections` is negative.
- **SC-021**: `Load` returns a user error when `snowflake.connectionMaxLifetime`, `connectionMaxIdleTime`, `connectionProbeTimeout`, or `secrets.cacheTtl` does not parse as a Go duration string, or parses to a non-positive duration.
- **SC-022**: `Load` defaults `Snowflake.DisableOCSPChecks` to `false` when the key is omitted, and honors an explicit `true`/`false` value when given.


## References

- **Config Package**: `internal/config/config.go` - `BaseConfig`, `SnowflakeSettings`, `AWSSettings`, `SecretsSettings`, `Load`
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

    "github.com/allianz/yukimi/internal/config"
    "github.com/allianz/yukimi/internal/secrets"
    secretsaws "github.com/allianz/yukimi/internal/secrets/aws"
)

func main() {
    configDir := flag.String("configDir", "/etc/yukimi/config", "directory containing baseConfig.yaml and sibling config files")
    flag.Parse()

    cfg, err := config.Load(*configDir)
    if err != nil {
        log.Fatalf("failed to load base config: %v", err)
    }

    var backend secrets.Backend
    switch cfg.CloudProvider() {
    case "aws":
        backend, err = secretsaws.New(cfg.AWS.Region)
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

### Example 2: A `baseConfig.yaml` Fixture

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

aws:
  region: eu-central-1
  # kmsKeyId: alias/yukimi-secrets  # optional, customer-managed KMS key

# secrets:
#   cacheTtl: 5m                    # optional, default shown (003)
#   rotationInterval: 4320h         # optional, default shown (004)
```

The `aws:` section is the only cloud section here, so `CloudProvider()` returns `"aws"` — nothing else in the file states the provider.

In local development, this same file is materialized by the Makefile from `.env` values (out of scope for this spec) and read by the exact same `Load` call; in production it is a file inside a mounted ConfigMap volume. Neither `internal/config` nor `Load` can tell the difference.
