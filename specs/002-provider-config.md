# Specification: Provider Config (002)

## Overview

`internal/config/` loads the controller's base configuration — `baseConfig.yaml`, read from a mounted directory at startup — into an immutable `BaseConfig` struct. It carries the Snowflake organization identity plus exactly what's needed to reach the cloud provider the controller runs on and that provider's own secret manager. It is a plain file loader: no Kubernetes API calls, no CRD, no reconciliation. Failing fast here means a misconfigured deployment never reaches a reconcile loop.

## Scope

This specification defines the `internal/config/` package that:
- Loads `<configDir>/baseConfig.yaml` at startup, where `configDir` is a directory path resolved elsewhere (see Integration Points).
- Exposes the parsed, validated result as an immutable `BaseConfig` struct.
- Validates required fields, raising `errors.NewUserError` for missing or malformed values, so the process fails fast at startup rather than once per reconcile.

**Out of Scope**:
- No CRD, no controller, no reconciler, no Kubernetes watch. This is not a Crossplane `ProviderConfig`.
- No interpretation of cloud-specific fields beyond carrying them verbatim — e.g. `aws.region` is validated by 003-a's constructor, never by this package.
- No knowledge of environment variables, `.env`, or how a Makefile might materialize `baseConfig.yaml` for local development. `Load` only ever reads a file from disk.
- No credential fields of any kind. Workload identity vs. local environment-variable/profile credentials is resolved entirely inside the cloud SDK's own default credential chain (003-a) — never modeled as a `BaseConfig` field or an explicit "auth mode" switch.
- No support for non-cloud-native secret stores (e.g. HashiCorp Vault). This platform only ever talks to a cloud provider's own secret manager, so that option space is deliberately excluded, not deferred.
- No enum validation of `CloudProvider` against the set of backends actually compiled into the binary. That check — and the fatal rejection of an unrecognized value — belongs to `cmd/provider/main.go`, not this package.

## Key Concept: One Selector, Not Two

An earlier draft of this spec carried a `secretsBackend` field, independent of any notion of "cloud provider," on the theory that the secret store might be swapped out on its own (e.g. Vault). That independence doesn't hold on this platform: this platform only ever talks to a cloud provider's own secret manager, so which cloud the controller runs on and which secret manager it uses are always the same fact, expressed once.

`CloudProvider` is that single selector — `aws` today, with `azure` and `gcp` reserved for when their backends are built. It determines two things at once:
- The workload-identity assumptions the controller runs under (IRSA for AWS; each cloud's own equivalent later).
- Which secrets backend `cmd/provider/main.go` constructs at startup (see 003, 003-a).

Cloud-specific settings live in their own nested block — `AWS AWSSettings` today, carrying only `Region`. `BaseConfig` carries this block verbatim; it is validated only by that cloud's own backend constructor (003-a for AWS), never by `internal/config` itself. This is the same "carried, not interpreted" principle applied through a nested struct instead of a flat field: a loader that started validating per-backend fields would have to know what backends are, and that knowledge belongs to the backend, not the loader.

**Known, accepted tradeoff**: because `AWSSettings` is a typed nested struct rather than an opaque per-provider blob, adding a future `003-b-azure` backend *will* require a small edit to `BaseConfig` — a new `Azure AzureSettings` field alongside `AWS`. This spec accepts that coupling rather than introducing a generic `map[string]any`/raw-YAML settings mechanism now to avoid it: no second cloud backend exists yet, and decoding a schema-less blob today would trade a rare, mechanical, one-field edit later for a permanent loss of type safety on the one backend that does exist. This is a deliberate choice, not an oversight to fix when 003-b lands.

## Key Concept: Shared `--configDir`, Duplicated Loaders

`baseConfig.yaml` is one of several files this platform reads from a single mounted directory — sibling files will hold the Backplane Config (007) and the Guardrails / Approved Exceptions config (008). All of them are addressed through one directory path, conventionally supplied to `cmd/provider/main.go` via a `--configDir` flag; `internal/config` itself takes only the resolved directory string, not the flag.

Each of those packages reads its own well-known filename from that shared directory independently. `internal/config` defines no shared "multi-file config loader" interface, no common YAML-decoding helper, and no validation framework for the others to build on. Each loader's "open file → parse → validate" logic is fully duplicated across 002, 007, and 008. This is deliberate: the loaders are small, and their validation rules differ enough — different required fields, different failure modes — that a shared abstraction would cost more to maintain than the duplication it would remove.

## Key Concept: Credentials Are Never a `BaseConfig` Field

`Load` behaves identically regardless of where the controller runs — nothing in this package branches on environment. What differs between production and local development is how the cloud SDK resolves credentials underneath the secrets backend (003-a), entirely outside `BaseConfig`'s schema:

- **In-cluster (production)**: the controller runs as a pod with workload identity (IRSA for AWS). The AWS SDK's default credential provider chain picks up the projected service-account token automatically — no configuration from this package is involved.
- **Local development**: the controller runs outside the Kubernetes cluster, so no workload identity exists. Credentials instead come from environment variables (`AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_SESSION_TOKEN`) or `AWS_PROFILE`. The *same* default credential chain simply falls through to them, since no IRSA metadata is present outside the cluster. How those environment variables are populated — including a Makefile copying `.env` values into `baseConfig.yaml` or the shell environment for local runs — is tooling, out of scope for this spec.

Because both cases go through the same SDK-internal chain, the switch between workload identity and local credentials is never a setting anyone writes into `baseConfig.yaml`. `BaseConfig.AWS` carries only `Region` — never credentials, never an explicit "auth mode" flag. 003-a's constructor calls the SDK's default chain and nothing else, so the identical code path resolves to workload identity or environment-variable credentials purely based on what the process finds at startup. This package's only responsibility is making sure `baseConfig.yaml` loads the same way no matter which environment the binary runs in.

## Public API

```go
// BaseConfig is the immutable, validated provider-wide configuration loaded at startup.
type BaseConfig struct {
    SnowflakeOrg             string      // organization name; used in account identifiers, secret paths, and accountUrl
    SnowflakeOrgAdminAccount string      // account used for org-level operations
    SnowflakeUsePrivatelink  bool        // affects the connection host (004); defaults to true when omitted
    CloudProvider            string      // "aws" today; "azure" and "gcp" are reserved values with no backend yet
    AWS                      AWSSettings // carried verbatim; consumed by 003-a, never interpreted here
}

// AWSSettings holds AWS-specific settings, carried by 002 but consumed only by 003-a.
type AWSSettings struct {
    Region string // consumed by 003-a's constructor; an empty region is a user error there, not here
}

// Load reads, parses, and validates "<configDir>/baseConfig.yaml".
//
// Parameters:
//   - configDir: directory containing baseConfig.yaml (and, in a full deployment,
//     its sibling config files for 007/008 — this package reads only its own file)
//
// Returns:
//   - *BaseConfig: the validated configuration; never nil on a nil error
//   - User error if the file is missing, unreadable, not valid YAML, or a required
//     field (SnowflakeOrg, SnowflakeOrgAdminAccount, CloudProvider) is empty
func Load(configDir string) (*BaseConfig, error)
```

## Project Structure

```text
internal/config/
├── config.go        # BaseConfig, AWSSettings, Load()
└── config_test.go   # Unit tests
```

## Error Classification

**User Errors** (use `errors.NewUserError()`):
- Missing file: `baseConfig.yaml not found in <configDir>`
- Malformed YAML: `failed to parse baseConfig.yaml: <parse error>`
- Missing required field: `snowflakeOrg is required in baseConfig.yaml`
- Missing required field: `snowflakeOrgAdminAccount is required in baseConfig.yaml`
- Missing required field: `cloudProvider is required in baseConfig.yaml`

**System Errors**: this package makes no network calls and has no retryable infrastructure dependency, so it classifies no scenario as a system error on its own. An unexpected filesystem error (e.g. a permissions problem on the mounted volume) surfaces as a raw wrapped error (`fmt.Errorf("reading baseConfig.yaml: %w", err)`); the caller's error handling (001) treats it as a system error by default, since `Load` never wraps it in `errors.NewUserError`. This is intentionally minimal — this package does not attempt to distinguish every possible OS-level failure mode.

## Edge Cases

- **What happens if `snowflakeUsePrivatelink` is omitted?** - Defaults to `true`.
- **What happens with an unrecognized YAML key (e.g. a future `timeout`)?** - Ignored by the decoder, not an error. The schema is expected to grow over time (timeouts, pool sizes, and similar settings may be added later as the codebase needs them), and unknown keys must not break `Load` on a rolling deployment.
- **What if the `aws:` block is present but `cloudProvider` is not `"aws"`?** - `Load` does not cross-validate the two. `AWS` is carried verbatim regardless of `CloudProvider`'s value; it is simply unused if the AWS backend isn't the one constructed.
- **What if `cloudProvider` names a cloud with no backend compiled in (e.g. `"azure"` today)?** - `Load` does not reject it — this package has no notion of which backends exist. `cmd/provider/main.go` is the one that fails fast, listing the cloud providers actually compiled in.
- **What differs when the controller runs outside the cluster (local development)?** - Nothing in this package. `Load` reads and validates `baseConfig.yaml` identically either way; only the AWS SDK's underlying credential resolution differs beneath 003-a (see Key Concept above), and that difference is invisible to `internal/config`.

## Dependencies

- **`internal/errors` (001)** - Used APIs: `errors.NewUserError()` - Contract: none; `internal/config` has no other internal dependency, making it a leaf package.

## Integration Points

- **`cmd/provider/main.go`** - Owns the `--configDir` flag and resolves it to a directory path. Calls `config.Load(configDir)` once at startup, then switches on `BaseConfig.CloudProvider` to construct the matching secrets backend, fatally rejecting an unrecognized value by listing the cloud providers compiled in - Key functions: `config.Load()`.
- **`internal/secrets/aws` (003-a)** - Consumes `BaseConfig.AWS.Region` when constructed by `main.go`; rejects an empty region as a user error itself, since 002 does not validate it - Notes: credentials come from the AWS SDK's default chain, never from `BaseConfig`.
- **`internal/snowflake/pool` (004)** - Consumes `BaseConfig.SnowflakeOrg` and `BaseConfig.SnowflakeUsePrivatelink` for connection host construction.
- **`internal/backplane` (007)** / **guardrails loader (008)** - Read their own sibling files (`backplane.yaml`, a guardrails/exceptions file) from the same `--configDir`, with independently implemented loading and validation logic — no code shared with `internal/config`.

## Success Criteria

- **SC-001**: `Load` returns a populated `*BaseConfig` for a well-formed `baseConfig.yaml`.
- **SC-002**: `Load` returns a user error when `<configDir>/baseConfig.yaml` does not exist.
- **SC-003**: `Load` returns a user error when the file is not valid YAML.
- **SC-004**: `Load` returns a user error when `snowflakeOrg` is empty or absent.
- **SC-005**: `Load` returns a user error when `snowflakeOrgAdminAccount` is empty or absent.
- **SC-006**: `Load` returns a user error when `cloudProvider` is empty or absent.
- **SC-007**: `Load` defaults `SnowflakeUsePrivatelink` to `true` when the key is omitted.
- **SC-008**: `Load` does not error on an unrecognized `cloudProvider` value (e.g. `"azure"`) — that rejection is `main.go`'s responsibility, not 002's.
- **SC-009**: `Load` does not error when `cloudProvider` and the presence/absence of the `aws:` block are inconsistent (e.g. `cloudProvider: azure` with an `aws:` block present).
- **SC-010**: `Load` preserves `AWS.Region` verbatim without validating its format or presence.
- **SC-011**: An unrecognized top-level YAML key does not cause `Load` to fail.
- **SC-012**: The returned `*BaseConfig` is safe for concurrent read-only use by multiple goroutines after `Load` returns.
- **SC-013**: `internal/config` imports only `internal/errors` among this repository's packages.
- **SC-014**: Unit test coverage exceeds 95%.

## References

- **Config Package**: `internal/config/config.go` - `BaseConfig`, `AWSSettings`, `Load`
- **Design Doc**: `specs/design.md`, §3.11.1 - the AWS Secrets Manager path grammar that consumes `SnowflakeOrg`
- **Roadmap**: `specs/roadmap.md` - ordering rationale; note that its current 002 summary and decision-7 text still describe an earlier `secretsBackend`/flat-`AWS_REGION` draft this spec supersedes.

<br/><br/><br/><br/>

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
    switch cfg.CloudProvider {
    case "aws":
        backend, err = secretsaws.New(cfg.AWS.Region)
        if err != nil {
            log.Fatalf("failed to construct AWS secrets backend: %v", err)
        }
    default:
        log.Fatalf("unrecognized cloudProvider %q (compiled in: aws)", cfg.CloudProvider)
    }

    // ... wire backend into the pool (004) and start the controller manager
}
```

### Example 2: A `baseConfig.yaml` Fixture

```yaml
snowflakeOrg: my_org_name
snowflakeOrgAdminAccount: my_org_admin_account_name
snowflakeUsePrivatelink: true
cloudProvider: aws
aws:
  region: eu-central-1
```

In local development, this same file is materialized by the Makefile from `.env` values (out of scope for this spec) and read by the exact same `Load` call; in production it is a file inside a mounted ConfigMap volume. Neither `internal/config` nor `Load` can tell the difference.
