# Specification: Backplane Config (007)

## Overview

The Backplane Config is a platform-owned catalog of the networking infrastructure and Snowflake
account parameters that platform operations pre-provisions once per cloud region, outside
Kubernetes. It exists so that creating a new Snowflake account never has to redo region-level
setup work — VPC endpoints, allowed IP ranges, and baseline account parameters are recorded here
once, and every account created afterwards simply attaches to what already exists. It is needed
because later account-provisioning steps depend on details, like VPC endpoint IDs and permitted IP
ranges, that only exist once Ops has finished a region's one-time Terraform rollout. The technical
approach is a small YAML file, read once at startup into an immutable in-memory structure, plus a
few lookup and validation helpers that other packages call directly.

## Scope

This specification defines the `internal/config/backplane/` package that:
- Loads `<configDir>/backplane.yaml` at startup, where `configDir` is the same directory path
  `002` reads `base.yaml` from.
- Exposes the parsed, validated result as an immutable `Config` struct.
- Looks up a region by name, and a connection by name within a region's inventory.
- Provides a CIDR-containment helper reused by the network module (014).
- Validates the file's internal consistency at load time: allowlist entries reference real
  connections, and every CIDR is well-formed and falls within its declared ceiling.

**Out of Scope**:
- Applying any of this configuration to Snowflake — pushing `globalParameters` /
  `regionalParameters` is 013's job, and turning `inventory` / `regionalAllowlist` into network
  rules and policies is 014's.
- Deciding what an unavailable or unknown region means for a given `SnowflakeAccount` request —
  this package only reports what the file says; 009/020 own that admission decision.
- No CRD, no controller, no Kubernetes watch — this is a plain file loader, exactly like `002`.

## Key Concept: The Availability Gate

Each region entry carries an `available` flag with no Snowflake counterpart — it lets Ops stage a
region ahead of time and keep it unofferable until it's ready. Defaults to `false` when omitted.

**TODO:** In the future, select alpha-tester namespaces will be allowed to use a region even while
`available: false`, via a namespace label not yet defined in any spec.

## Key Concept: CIDR Containment

Every ingress path (`connection`) in a region's `inventory` carries `maxCidrs`: the widest IP
range that connection may ever be opened to. `regionalAllowlist` entries — and, later, a tenant's
own `customNetworkRules` (014) — narrow that range with their own `allowedIPs`, but can never
exceed it.

"Contained" means every address a narrower CIDR covers also falls inside at least one of the
wider ranges — equivalently, the narrower CIDR's prefix is at least as long and its network
address sits inside one of the wider blocks. `Load` applies this rule itself when validating
`regionalAllowlist`, using the exact same `ContainsCIDR` helper that 014 will call later for
`customNetworkRules`, so both places agree on what "fits inside the ceiling" means without
duplicating the comparison logic.

## Public API

```go
// Config is the immutable, validated Backplane Config loaded at startup.
type Config struct {
    GlobalParameters map[string]string // org-wide Snowflake account parameters applied to every account (013)
    Regions          map[string]Region // keyed by region name, e.g. "aws-eu-central-1"
}

// Region is one region's backplane entry.
type Region struct {
    Available          bool              // controller-side gate, no Snowflake counterpart; false when omitted
    Inventory          []Connection      // catalog of physical ingress paths for this region
    RegionalParameters map[string]string // region-specific Snowflake account parameters (013)
    RegionalAllowlist  []AllowlistEntry  // baseline network access applied to every account in this region
}

// Connection is one inventory entry: a physical ingress path and the widest range it may ever carry.
type Connection struct {
    Name     string   // e.g. "agn", "dbt-cloud", "public"
    Type     string   // e.g. "AWSVPCEID", "IPV4"; free-form, not interpreted by this package
    VpceID   string   // set for VPCE-typed connections; empty otherwise
    MaxCidrs []string // widest CIDR(s) this connection may ever carry; empty for VPCE-only connections
}

// AllowlistEntry is one entry under a region's regionalAllowlist.
type AllowlistEntry struct {
    Connection string   // must name a Connection present in the region's Inventory
    AllowedIPs []string // narrows Connection's MaxCidrs; empty means inherit the full range
}

// Load reads, parses, and validates "<configDir>/backplane.yaml".
//
// Parameters:
//   - configDir: directory containing backplane.yaml (a sibling of base.yaml, 002)
//
// Returns:
//   - *Config: the validated configuration; never nil on a nil error
//   - User error if the file is missing, unreadable, not valid YAML, an inventory entry is
//     missing its connection name or type, a connection name repeats within one region's
//     inventory, a regionalAllowlist entry names a connection absent from that region's
//     inventory, any maxCidrs/allowedIPs entry is not a valid CIDR, an allowedIPs entry falls
//     outside its connection's maxCidrs, or allowedIPs is set for a connection with no maxCidrs
func Load(configDir string) (*Config, error)

// Region looks up a region by name — typically a SnowflakeAccount's spec.region (design.md 3.1).
// It reports only what the file says; it does not consult Available, leaving what an unavailable
// or unknown region means for the caller's request to the caller (009, 020).
//
// Returns:
//   - User error if no region with this name exists in the loaded config
func (c *Config) Region(name string) (*Region, error)

// Connection looks up a connection by name within this region's inventory.
//
// Returns:
//   - ok: false if no connection with this name exists in the region's inventory
func (r *Region) Connection(name string) (*Connection, bool)

// ContainsCIDR reports whether candidate is fully contained within at least one entry of ranges.
// Pure comparison, no I/O; reused by 014 to validate customNetworkRules allowedIPs entries
// against a connection's MaxCidrs, applying the identical containment rule Load itself uses for
// regionalAllowlist.
//
// Returns:
//   - User error if candidate or any entry of ranges is not a valid CIDR
func ContainsCIDR(ranges []string, candidate string) (bool, error)
```

## Schema Specification

Every field in `backplane.yaml` is freely editable and the whole file is reloaded wholesale on the
next pod restart, exactly like `base.yaml` (002) — no Mutability column.

### Fields (`backplane.yaml`)

| Field Path | Type | Required | Validation / Constraints |
| ---------- | ---- | -------- | ------------------------ |
| `globalParameters` | map[string]string | No | Free-form Snowflake account parameters applied to every account (013). Default: empty map. |
| `regions` | map[string]object | No | Keyed by region name (e.g. `aws-eu-central-1`); the key is a free-form string, not checked against any fixed list. Default: empty map. |
| `regions.<region>.available` | bool | No | Controller-side gate with no Snowflake counterpart (design.md 3.5). Default: `false` when omitted. |
| `regions.<region>.inventory` | []object | No | Catalog of physical ingress paths for this region. Default: empty list. |
| `regions.<region>.inventory[].connection` | string | **Yes** | Non-empty; unique within this region's inventory. |
| `regions.<region>.inventory[].type` | string | **Yes** | Non-empty; free-form (e.g. `AWSVPCEID`, `IPV4`) — meaning is not interpreted by this package. |
| `regions.<region>.inventory[].vpceId` | string | No | Set for VPCE-typed connections; not cross-checked against `type`. |
| `regions.<region>.inventory[].maxCidrs` | []string | No | Widest range(s) this connection may ever carry. Each entry must be a valid CIDR. Empty for VPCE-only connections with nothing to narrow. |
| `regions.<region>.regionalParameters` | map[string]string | No | Region-specific Snowflake account parameters (013). Default: empty map. |
| `regions.<region>.regionalAllowlist` | []object | No | Baseline network access applied to every account in the region. Default: empty list. |
| `regions.<region>.regionalAllowlist[].connection` | string | **Yes** | Must name a connection present in this region's `inventory`. |
| `regions.<region>.regionalAllowlist[].allowedIPs` | []string | No\* | Each entry must be a valid CIDR fully contained within the named connection's `maxCidrs`. Omitted or empty inherits the connection's full `maxCidrs`. <br>*Must be empty if the connection has no `maxCidrs`.* |

## Project Structure

```text
internal/config/backplane/
├── backplane.go        # Config, Region, Connection, AllowlistEntry, Load, Region(), Connection(), ContainsCIDR
└── backplane_test.go   # Unit tests
```

## Error Classification

**User Errors** (use `errors.NewUserError()`):
- Missing file: `backplane.yaml not found in <configDir>`
- Malformed YAML: `failed to parse backplane.yaml: <parse error>`
- Missing inventory field: `regions.aws-eu-central-1.inventory[1].connection is required`
- Missing inventory field: `regions.aws-eu-central-1.inventory[1].type is required`
- Duplicate connection: `regions.aws-eu-central-1.inventory contains connection 'agn' more than once`
- Unknown connection reference: `regions.aws-eu-central-1.regionalAllowlist references unknown connection 'agn'`
- Malformed CIDR: `regions.aws-eu-central-1.inventory[0].maxCidrs '172.16.0.0/99' is not a valid CIDR`
- Containment violation: `regions.aws-eu-central-1.regionalAllowlist connection 'agn' allowedIPs '172.32.0.0/16' is not contained within maxCidrs [172.16.0.0/12]`
- Nothing-to-narrow violation: `regions.aws-eu-central-1.regionalAllowlist connection 'dbt-cloud' specifies allowedIPs but this connection has no maxCidrs to narrow`
- Unknown region: `region 'aws-ap-southeast-1' not found in backplane.yaml` (from `Region()`, not `Load`)

**System Errors**: like `002`, this package makes no network calls and classifies nothing as a
system error on its own. An unexpected filesystem error surfaces as a raw wrapped error
(`fmt.Errorf("reading backplane.yaml: %w", err)`); the caller's error handling (001) treats it as
a system error by default, since `Load` never wraps it in `errors.NewUserError`.

## Edge Cases

- **What happens if `globalParameters` is omitted?** - Defaults to an empty map; no accounts get
  any org-wide parameter from this source until Ops adds entries.
- **What happens if a region's `inventory` is empty?** - Accepted. That region simply has no
  connection any `regionalAllowlist` entry or, later, `customNetworkRules` entry (014) can
  reference — any attempt to do so fails as "unknown connection".
- **What happens if a region's `regionalAllowlist` is empty or omitted?** - Accepted; this package
  does not require an available region to grant any baseline access. Whether that is operationally
  sound (e.g. nobody could log in) is an Ops discipline concern, not something `Load` enforces.
- **What happens when a region is marked `available: false`?** - `Load` validates it exactly as
  strictly as an available one; nothing about validation depends on the flag. Only the caller
  (009/020) decides whether to admit a `SnowflakeAccount` naming this region.
- **What if a connection carries both a `vpceId` and `maxCidrs` (e.g. `agn`)?** - Accepted; the two
  are independent optional fields, never treated as mutually exclusive.
- **What if the same connection name appears in two different regions?** - Accepted; connection
  names are unique only within one region's own inventory, not globally.
- **What if `Region()` or `Connection()` is called with an empty string?** - Treated like any other
  unknown name: `Region("")` returns a user error, `Connection("")` returns `ok == false`.

## Dependencies

- **`internal/errors` (001)** - Used APIs: `errors.NewUserError()` - Contract: none;
  `internal/config/backplane` has no other internal dependency and does **not** import `internal/config/base`
  — the two loaders are independently duplicated, per 002's "Shared `--configDir`, Duplicated
  Loaders" key concept.

## Integration Points

- **`cmd/provider/main.go`** - Calls `backplane.Load(configDir)` once at startup, alongside (and
  independently of) `config.Load(configDir)` - Key functions: `backplane.Load()`.
- **`internal/account/modules/account` (012)** - Consumes a region's `Inventory` and
  `RegionalAllowlist` during account bootstrapping (design.md 3.6).
- **`internal/account/modules/parameter` (013)** - Consumes `Config.GlobalParameters` and a
  region's `RegionalParameters`.
- **`internal/account/modules/network` (014)** - Consumes a region's `Inventory`, `Connection()`,
  and `ContainsCIDR()` to validate and apply `customNetworkRules` (design.md 3.8).
- **`internal/account` (009) / `internal/controller/snowflakeaccount` (020)** - Calls
  `Config.Region()` and inspects `Region.Available` as part of admitting or rejecting a
  `SnowflakeAccount` naming that region.

## Success Criteria

- **SC-001**: `Load` returns a populated `*Config` for a well-formed `backplane.yaml`.
- **SC-002**: `Load` returns a user error when `<configDir>/backplane.yaml` does not exist.
- **SC-003**: `Load` returns a user error when the file is not valid YAML.
- **SC-004**: `Load` accepts an empty or omitted `regions` map.
- **SC-005**: A region's `Available` defaults to `false` when `available` is omitted, and honors
  an explicit `true` or `false`.
- **SC-006**: `Region(name)` returns the matching `*Region` for a known region, and a user error
  for an unknown one.
- **SC-007**: `Connection(name)` returns the matching `*Connection` and `ok == true` for a known
  connection, and `ok == false` for an unknown one.
- **SC-008**: `Load` returns a user error when the same connection name appears twice in one
  region's `inventory`.
- **SC-009**: `Load` returns a user error when an inventory entry is missing `connection` or `type`.
- **SC-010**: `Load` returns a user error when a `regionalAllowlist` entry references a connection
  absent from that region's `inventory`.
- **SC-011**: `Load` accepts a `regionalAllowlist` entry with no `allowedIPs`, treating it as
  inheriting the connection's full `maxCidrs`.
- **SC-012**: `Load` returns a user error when a `regionalAllowlist` entry's `allowedIPs` falls
  outside its connection's `maxCidrs`.
- **SC-013**: `Load` returns a user error when `allowedIPs` is set for a connection with no
  `maxCidrs`.
- **SC-014**: `Load` returns a user error when any `maxCidrs` or `allowedIPs` entry is not a valid
  CIDR.
- **SC-015**: `ContainsCIDR` returns `true` when candidate is fully inside one of ranges (including
  when it equals a range exactly), and `false` when it exceeds every range or matches none.
- **SC-016**: `ContainsCIDR` returns a user error when candidate or any entry of ranges is not a
  valid CIDR.
- **SC-017**: `internal/config/backplane` imports only `internal/errors` among this repository's packages.
- **SC-018**: The returned `*Config` is safe for concurrent read-only use by multiple goroutines
  after `Load` returns.
- **SC-019**: Unit test coverage exceeds 95%.

## Security Considerations

- `maxCidrs` is an absolute ceiling enforced structurally: `Load` rejects any `allowedIPs` entry
  that is not fully contained within it, so a malformed `backplane.yaml` can never itself grant
  wider network access than the ceiling Ops recorded.
- `ContainsCIDR` is a pure, I/O-free comparison with no side effects — reusing it from 014
  guarantees the containment rule applied to `customNetworkRules.*.allowedIPs` is bit-for-bit the
  same rule `Load` already applies to `regionalAllowlist`, rather than a second implementation that
  could silently drift apart.

## References

- **Backplane Package**: `internal/config/backplane/backplane.go` - `Config`, `Region`, `Connection`,
  `AllowlistEntry`, `Load`, `Region()`, `Connection()`, `ContainsCIDR()`
- **Design Doc**: `specs/design.md`, §3.5 - Backplane Config concept, schema, and example
- **Shape Reference**: `specs/002-base-config.md` - loader pattern this spec duplicates
  independently (own `Load(configDir)`, own raw-decode structs, own validation)

<br/><br/><br/><br/><br/>

================

## Appendix: Usage Examples

### Example 1: Loading Config at Startup (Primary Use Case)

```go
// In cmd/provider/main.go, alongside config.Load (002) — two independent calls against the
// same --configDir, each reading its own file.
cfg, err := config.Load(*configDir)
if err != nil {
    log.Fatalf("failed to load base config: %v", err)
}

bp, err := backplane.Load(*configDir)
if err != nil {
    log.Fatalf("failed to load backplane config: %v", err)
}
```

### Example 2: Region and Connection Lookup (012, 013, 014)

```go
region, err := bp.Region(cr.Spec.Region)
if err != nil {
    return err // unknown region named in the CRD
}
if !region.Available {
    return errors.NewUserError(fmt.Sprintf("region '%s' is not yet available", cr.Spec.Region))
}

for _, entry := range region.RegionalAllowlist {
    conn, ok := region.Connection(entry.Connection)
    if !ok {
        continue // Load already rejects this at startup; defensive only
    }
    // ... build a CREATE NETWORK RULE statement from conn.Type, conn.VpceID, entry.AllowedIPs
}
```

### Example 3: CIDR Containment Reused by 014

```go
ok, err := backplane.ContainsCIDR(conn.MaxCidrs, requestedCIDR)
if err != nil {
    return errors.NewUserError(err.Error()) // malformed CIDR in the tenant's CRD
}
if !ok {
    return errors.NewUserError(fmt.Sprintf(
        "allowedIPs '%s' is not contained within maxCidrs %v", requestedCIDR, conn.MaxCidrs))
}
```

### Example 4: `backplane.yaml`

```yaml
backplane:
  globalParameters:
    PREVENT_UNLOAD_TO_INLINE_URL: "true"
    REQUIRE_STORAGE_INTEGRATION_FOR_STAGE_CREATION: "true"

  regions:
    aws-eu-central-1:
      available: true
      inventory:
        - connection: agn
          type: "AWSVPCEID"
          vpceId: "vpce-00006900000000001"
          maxCidrs: ["172.16.0.0/12"]
        - connection: dbt-cloud
          type: "AWSVPCEID"
          vpceId: "vpce-00006900000000004"
        - connection: public
          type: "IPV4"
          maxCidrs: ["0.0.0.0/0"]
      regionalParameters:
        ENABLE_INTERNAL_STAGES_PRIVATELINK: "true"
        S3_STAGE_VPCE_DNS_NAME: "*.vpce-sd98fs0d9f8g.s3.eu-central-1.vpce.amazonaws.com"
      regionalAllowlist:
        - connection: agn
        - connection: public
          allowedIPs: ["203.0.113.0/24"]

    azure-westeurope:
      available: false
      # ... inventory, regionalParameters, regionalAllowlist as above
```
