# Specification: SnowflakeAccount CRD & Tenant Helpers (006)

## Overview

`SnowflakeAccount` is the resource a team commits to Git to describe the Snowflake account they
want. This spec defines exactly what that resource looks like — its fields, its status shape, and
the handful of rules a submitted resource must already satisfy before anything downstream touches
it — plus a small package of pure helper functions every later spec needs to resolve a tenant's
identity: turning the name a team chose into the account's actual Snowflake name, reading the few
pieces of onboarding metadata operations attached to the team's namespace, and building the URL a
tenant uses once their account exists. This spec covers definitions and helpers only. It defines no
controller: nothing here talks to the Kubernetes API at runtime, and nothing here talks to
Snowflake at all — later specs read these fixed shapes, they don't extend them.

## Scope

- The `SnowflakeAccount` CRD type in `apis/base/v1alpha1/`, group `base.snowflake.yukimi.io`,
  version `v1alpha1` (design.md §3.1).
- Structural validation drawn directly from this chapter, expressed as
  `x-kubernetes-validations` CEL rules: `region`/`environment` immutability after creation
  (§3.11.3), the `environment` enum, `identityIntegration.roleBindings` requiring an
  `ACCOUNTADMIN` entry (§3.7), and a `customAuthRules.exceptions` entry naming at least one of
  `rsaKeyAllowed`/`patAllowed` (§3.9).
- The `status.accountName` / `accountLocator` / `accountUrl` / `conditions` shape (§7.2).
- The `internal/account/tenant/` package: `ResolveName` (§3.12), the `Department`/`CostCenter`/
  `CreditQuota` namespace-label readers (chapter 2), and `AccountURL` (§7.2, built on spec 004's
  host package).

### Out of Scope

- Guardrails constraint/preset enforcement (naming patterns, credit ceilings, network CIDR
  limits, allowed regions) — spec 008.
- Backplane Config lookups and any bootstrapping, network, or auth SQL (§3.6, §3.8, §3.9) — specs
  007, 012, 014, 015.
- Controller reconciliation: `Observe`/`Create`/`Update`/`Delete`, condition-setting, finalizers —
  spec 020.
- Quota admission math (011), quota enforcement (018), identity sync (016/017), deletion warrants
  (019), replication (021).
- Duplicate-connection detection within `customNetworkRules` (§3.8). Expressing "no repeated
  connection name in this list" in CEL is disproportionately complex for a check that's simple to
  make once a Go module exists to call it; deferred to the network module (014).
- Cross-referencing `identityIntegration.roleBindings` values against `identityIntegration.groups`
  entries — deferred to the identity module (017), which is the first module that actually needs
  both sides of that mapping to be true.

## Key Concept: Immutable Identity

Design.md §3.11.3 asks for `region`, `name`, and `environment` to be immutable, so that a tenant
can't create an account, let its credentials generate, and then repoint the resource at a
different target while keeping them. `metadata.name` needs no work here at all: Kubernetes already
refuses to change an object's `name` after creation — it's part of the object's identity in the
API server, not an ordinary field. The CEL work in this spec is therefore only two rules, both on
`SnowflakeAccountSpec`: `self.region == oldSelf.region` and
`self.environment == oldSelf.environment`. Both fire only on update, never on create — a tenant is
free to choose either value the first time, just not to change it afterward. `environment`'s
immutability exists for a different reason than `region`'s: it selects which Guardrails baseline
applies (§3.3), so leaving it mutable would let an account be created under `prod` and flipped to
`dev` to pick up its looser network posture.

## Key Concept: Namespace as Trust Anchor & Ops-Owned Labels

Per §3.11.1, the Kubernetes namespace is the sole source of truth for tenancy — it's how secret
paths are constructed and it's derived from the runtime environment, not from anything a tenant
writes. Chapter 2's onboarding script labels that namespace with `department`, `cost-center`, and
`credit-quota` at the same time it's created, and deliberately does not expose any of them as CRD
fields: if they were CRD fields, a tenant could edit their own department or credit ceiling by
committing a YAML change, which defeats the point of having ops set them out-of-band. `internal/
tenant/labels.go` is the one place that reads them back out, as plain `map[string]string` label
data rather than a Kubernetes API type — this is why `internal/account/tenant` needs no Kubernetes client
dependency at all. `Department` is read by Guardrails (008) for target matching; `CreditQuota` is
read by quota-check (011) for admission and quota-monitor (018) for enforcement; `CostCenter` has
no consumer named anywhere in design.md yet, but
the label exists and gets set on every namespace regardless, so its reader is added alongside the
other two now rather than being bolted onto whichever spec first needs it.

## Key Concept: Minimal Managed-Resource Surface

The project's scaffolding (`make provider.addtype`) defaults new managed-resource types to
Crossplane's `spec.forProvider.*` / `status.atProvider.*` convention, plus a full
`ManagedResourceSpec` embed carrying a provider-config reference (defaulted to
`{kind: ClusterProviderConfig, name: default}`) and a connection-secret reference. Per CLAUDE.md,
this project's CRDs aren't shaped around Crossplane ecosystem conventions, and design.md §3.1's
example puts every field directly under `spec` — there is no `forProvider` anywhere in it. This
spec goes further than just dropping the wrapper name: `SnowflakeAccountSpec` carries no
provider-config reference and no connection-secret reference at all, because nothing in this
platform's design needs one — every account's credentials are located by the namespace-derived
secret path in §3.11.1, not by a `ProviderConfig` object a tenant could point elsewhere.

Checking `crossplane-runtime/v2`'s `pkg/resource` package confirms this is safe to do: the
reconciler pattern CLAUDE.md describes for `SnowflakeAccount` ("Standard Controller with External
State") is built on `managed.NewReconciler`, which only requires the base `resource.Managed`
interface — a Kubernetes object that can report `ManagementPolicies` and `Conditions`. It does
**not** require the wider `ModernManaged`/`LegacyManaged` interfaces, which are what pull in a
provider-config reference. So `SnowflakeAccountSpec` carries exactly one field sourced from
crossplane-runtime — `managementPolicies` — and nothing else; `SnowflakeAccountStatus` embeds
`xpv1.ResourceStatus` for conditions, which likewise carries no provider-config field. This is the
minimum needed for the type to compile against `resource.Managed` when spec 020 wires up the
controller, with zero provider-config surface for a tenant to ever set.

## Public API

```go
// Package v1alpha1 — apis/base/v1alpha1
package v1alpha1

// SnowflakeAccountSpec defines the desired state of a SnowflakeAccount. Every
// field is a direct sibling under spec, matching design.md 3.1's example
// exactly — there is no forProvider wrapper (Key Concept: Minimal
// Managed-Resource Surface).
type SnowflakeAccountSpec struct {
	// +optional
	Description string `json:"description,omitempty"`

	// +optional
	Contacts []string `json:"contacts,omitempty"`

	// Immutable after creation (design.md 3.11.3).
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="region is immutable"
	Region string `json:"region"`

	// Immutable after creation (design.md 3.11.3); selects the Guardrails
	// baseline (008).
	// +kubebuilder:validation:Enum=dev;prod
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="environment is immutable"
	Environment string `json:"environment"`

	// This account's share of the namespace's monthly credit allowance
	// (design.md 3.10). Ceiling enforcement is Guardrails'/Quota's job (008/011).
	// +optional
	CreditQuota int32 `json:"creditQuota,omitempty"`

	IdentityIntegration IdentityIntegration `json:"identityIntegration"`

	// +optional
	CustomNetworkRules *CustomNetworkRules `json:"customNetworkRules,omitempty"`

	// +optional
	CustomAuthRules *CustomAuthRules `json:"customAuthRules,omitempty"`

	// The only crossplane-runtime managed-resource field this type carries.
	// No ProviderConfigReference, no WriteConnectionSecretToReference.
	// +optional
	// +kubebuilder:default={"*"}
	ManagementPolicies common.ManagementPolicies `json:"managementPolicies,omitempty"`
}

// IdentityIntegration is design.md 3.1/3.7's identityIntegration block.
type IdentityIntegration struct {
	// Keyed by integration (e.g. "giam"); the key is free-form, not schema.
	// +optional
	Groups map[string][]string `json:"groups,omitempty"`

	// Must contain an ACCOUNTADMIN entry (design.md 3.7).
	// +kubebuilder:validation:XValidation:rule="'ACCOUNTADMIN' in self",message="roleBindings must bind ACCOUNTADMIN"
	RoleBindings map[string]string `json:"roleBindings"`
}

// CustomNetworkRules is design.md 3.1/3.8's customNetworkRules block.
type CustomNetworkRules struct {
	// +optional
	ServiceUsers map[string][]NetworkRule `json:"serviceUsers,omitempty"`

	// +optional
	AccountWide []NetworkRule `json:"accountWide,omitempty"`
}

// NetworkRule is one entry under customNetworkRules (design.md 3.8).
type NetworkRule struct {
	// An inventory connection name from the region's Backplane Config (007);
	// resolved, not validated, here.
	Connection string `json:"connection"`

	// +optional
	AllowedIPs []string `json:"allowedIPs,omitempty"`
}

// CustomAuthRules is design.md 3.1/3.9's customAuthRules block.
type CustomAuthRules struct {
	// +optional
	Exceptions []AuthException `json:"exceptions,omitempty"`
}

// AuthException is one entry under customAuthRules.exceptions (design.md 3.9).
// +kubebuilder:validation:XValidation:rule="self.rsaKeyAllowed || self.patAllowed",message="exception must permit at least one of rsaKeyAllowed or patAllowed"
type AuthException struct {
	User string `json:"user"`

	// +optional
	RSAKeyAllowed bool `json:"rsaKeyAllowed,omitempty"`

	// +optional
	PATAllowed bool `json:"patAllowed,omitempty"`

	// Audit only; never carried into Snowflake.
	Reason string `json:"reason"`
}

// SnowflakeAccountStatus defines the observed state of a SnowflakeAccount.
type SnowflakeAccountStatus struct {
	xpv1.ResourceStatus `json:",inline"`

	// The resolved Snowflake account name (design.md 3.12) — not
	// metadata.name.
	// +optional
	AccountName string `json:"accountName,omitempty"`

	// Captured from CREATE ACCOUNT's result (012).
	// +optional
	AccountLocator string `json:"accountLocator,omitempty"`

	// Set once, on the reconcile that first creates the account (012);
	// anchors the grace period before the first post-create connection
	// attempt.
	// +optional
	AccountCreatedAt *metav1.Time `json:"accountCreatedAt,omitempty"`

	// Built via internal/account/tenant.AccountURL (design.md 7.2).
	// +optional
	AccountURL string `json:"accountUrl,omitempty"`
}
```

```go
// Package tenant — internal/account/tenant
package tenant

// ResolveName derives the Snowflake account name for a SnowflakeAccount CRD:
// metadata.name with every '-' translated to '_', suffixed with '_' plus the
// first 5 characters of the base32-encoded SHA-256 of metadata.namespace
// (design.md 3.12). Requires no stored state — namespaces can't be renamed
// and name is immutable, so the result is stable and recomputable on every
// call.
//
// Parameters:
//   - name: metadata.name of the SnowflakeAccount CRD.
//   - namespace: metadata.namespace of the SnowflakeAccount CRD.
//
// Returns: the resolved Snowflake account name. Never errors — both inputs
// are Kubernetes identifiers already validated by the API server.
func ResolveName(name, namespace string) string

// Department returns the ops-set "department" namespace label (design.md
// chapter 2), consumed by Guardrails target matching (008).
//
// Returns: User error if the label is missing or empty — the tenant can't
// fix this by editing their CRD, but a readable message ("namespace missing
// required label 'department'; contact platform ops") surfaced directly on
// the resource is more useful to them than a system error's incident ID
// (see Error Classification).
func Department(labels map[string]string) (string, error)

// CostCenter returns the ops-set "cost-center" namespace label (design.md
// chapter 2). No spec currently consumes the returned value; this reader
// exists so that whichever spec adds the first consumer doesn't also need to
// touch this package.
//
// Returns: User error if the label is missing or empty, for the same
// readability reason as Department.
func CostCenter(labels map[string]string) (string, error)

// CreditQuota returns the ops-set "credit-quota" namespace label (design.md
// chapter 2 and 3.10), parsed to an int.
//
// Returns: User error if the label is missing, empty, or not a valid
// non-negative integer — same readability reasoning as Department.
func CreditQuota(labels map[string]string) (int, error)

// AccountURL returns the SnowflakeAccount's status.accountUrl (design.md
// 7.2): the account's login URL — host.URL's bare host plus
// "/console/login" — built from the locator Snowflake assigned at CREATE
// ACCOUNT (012) and the CRD's region. It never derives from the resolved
// account name, which has no relationship to the locator. Wraps
// internal/snowflake/host.URL (004); adds no validation beyond that call.
//
// Parameters:
//   - locator: the account locator returned by CREATE ACCOUNT (e.g. "xc19114").
//   - region: the CRD's spec.region (e.g. "aws-eu-central-1").
//   - usePrivateLink: from the controller's base config (002), supplied by
//     the caller (020) — not read from the Backplane Config, which carries
//     no such field.
//
// Returns: User error if region does not match the expected
// "<cloud>-<region...>" shape (bubbled from internal/snowflake/host.URL).
func AccountURL(locator, region string, usePrivateLink bool) (string, error)
```

## Schema Specification

### Fields (spec)

| Field Path | Type | Required | Mutability | Validation/Constraints |
|---|---|---|---|---|
| `description` | string | No | Mutable | — |
| `contacts[]` | string | No | Mutable | — |
| `region` | string | Yes | Immutable | Format/allowlist enforced by Guardrails (008), not here |
| `environment` | string | Yes | Immutable | Enum: `dev`, `prod` |
| `creditQuota` | int32 | No | Mutable | Ceiling enforced by Guardrails/Quota (008/011), not here |
| `identityIntegration` | object | Yes | Mutable | — |
| `identityIntegration.groups` | map[string][]string | No | Mutable | Key is free-form (not schema); `giam` is the only integration configured today |
| `identityIntegration.roleBindings` | map[string]string | Yes | Mutable | Must contain an `ACCOUNTADMIN` key (CEL) |
| `customNetworkRules` | object | No | Mutable | — |
| `customNetworkRules.serviceUsers` | map[string][]NetworkRule | No | Mutable | Duplicate connections within a user's list: out of scope, see 014 |
| `customNetworkRules.accountWide[]` | NetworkRule | No | Mutable | Duplicate connections: out of scope, see 014 |
| `customNetworkRules.*.connection` | string | Yes | Mutable | Resolved against Backplane Config inventory (007), not validated here |
| `customNetworkRules.*.allowedIPs[]` | string | No | Mutable | CIDR containment enforced by Guardrails (008), not here |
| `customAuthRules` | object | No | Mutable | — |
| `customAuthRules.exceptions[]` | AuthException | No | Mutable | — |
| `customAuthRules.exceptions[].user` | string | Yes | Mutable | — |
| `customAuthRules.exceptions[].rsaKeyAllowed` | bool | No | Mutable | At least one of `rsaKeyAllowed`/`patAllowed` required (CEL) |
| `customAuthRules.exceptions[].patAllowed` | bool | No | Mutable | See above |
| `customAuthRules.exceptions[].reason` | string | Yes | Mutable | Audit only; not carried into Snowflake |
| `managementPolicies[]` | string | No | Mutable | crossplane-runtime field; default `["*"]`. No `providerConfigRef` or `writeConnectionSecretToRef` field exists on this type (Key Concept: Minimal Managed-Resource Surface) |

### Fields (status)

| Field Path | Type | Required | Mutability | Validation/Constraints |
|---|---|---|---|---|
| `accountName` | string | No | Controller-set | The resolved name (§3.12), not `metadata.name` |
| `accountLocator` | string | No | Controller-set | Captured from `CREATE ACCOUNT` (012) |
| `accountCreatedAt` | timestamp | No | Controller-set | Set once by 012 when the account is first created; anchors the post-create connection grace period |
| `accountUrl` | string | No | Controller-set | Built via `internal/account/tenant.AccountURL` (§7.2) |
| `conditions[]` | Condition | No | Controller-set | Standard `Ready`/`Synced` per design.md §7.1 |

## Project Structure

### Source Code

```text
apis/base/v1alpha1/
├── base.go                     # group package doc
├── doc.go
├── groupversion_info.go        # Group = base.snowflake.yukimi.io, Version = v1alpha1
└── snowflakeaccount_types.go   # Spec/Status types, CEL markers, +kubebuilder:resource:scope=Namespaced

internal/account/tenant/
├── naming.go        # ResolveName (§3.12)
├── naming_test.go
├── labels.go         # Department, CostCenter, CreditQuota (chapter 2)
├── labels_test.go
├── url.go            # AccountURL: internal/snowflake/host.URL + "/console/login"
├── url_test.go
└── doc.go
```

`internal/account/tenant` imports nothing beyond the Go standard library, `internal/errors`, and
`internal/snowflake/host` — no Kubernetes client, no Snowflake driver. The label readers take
`map[string]string`, never a `corev1.Namespace`, specifically to keep that boundary intact; the
caller (020) already has the namespace object from its own reconcile and passes its labels in.

## Error Classification

- **User Errors**: `Department`, `CostCenter`, and `CreditQuota` all return one when their label
  is missing or empty, and `CreditQuota` additionally returns one for a value that doesn't parse
  as a non-negative integer. This is a deliberate deviation from CLAUDE.md's general rule that a
  user error must be tenant-fixable: onboarding (chapter 2) is supposed to always set these
  labels, so an absent value is genuinely an operator-side problem. A tenant still can't fix it by
  editing their CRD — but classifying it as a system error would surface only an incident ID on
  the resource, forcing the tenant to file a ticket just to find out what's wrong. A user error
  carries the readable message itself (e.g. "namespace missing required label 'department';
  contact platform ops") directly onto the resource's condition, which is more useful to the
  tenant even though they can't act on it alone — and still tells them exactly who to loop in.
  `AccountURL`'s region-format error is a genuine, ordinary user error, constructed and classified
  in spec 004 (`internal/snowflake/host`); `internal/account/tenant` only passes it through unchanged.
- **System Errors**: none originate in `internal/account/tenant`.

## Edge Cases

- **Two tenants both name an account `dev` — does `ResolveName` collide?** No: the namespace-hash
  suffix is derived from `metadata.namespace`, which differs between tenants by construction, so
  `dev` in `finance` and `dev` in `analytics` resolve to different Snowflake names.
- **A namespace is missing `department`/`cost-center`/`credit-quota` entirely — does the CRD fail
  validation?** No — these are namespace labels, not CRD fields, so nothing about the
  `SnowflakeAccount` resource itself is invalid. The failure surfaces only when a caller (008, 011,
  018) invokes the corresponding `internal/account/tenant` reader and gets a user error back (see Error
  Classification for why this is a user error despite being ops-caused).
- **Do the `region`/`environment` CEL rules block the first `CREATE`?** No — `oldSelf` doesn't
  exist yet on create, so both rules only evaluate (and can only fail) on `UPDATE`.
- **Why no CEL rule for `metadata.name`?** Kubernetes already rejects any attempt to change an
  object's `name`; there's nothing left for this CRD's schema to enforce.

## Dependencies

- **internal/errors (001)** - Used APIs: `errors.NewUserError` - Contract: `internal/account/tenant`
  constructs a user error directly for every missing/malformed ops-owned label, prioritizing a
  readable message on the resource over the strict "tenant-fixable" framing CLAUDE.md otherwise
  uses for the user/system split (see Error Classification); it never constructs a system error
  itself, and only re-surfaces the user error `internal/snowflake/host` (004) already produces for
  `AccountURL`'s region-format failure.
- **internal/snowflake/host (004)** - Used APIs: `host.URL(locator, region, usePrivateLink)` -
  Contract: `internal/account/tenant/url.go` calls `host.URL` and appends `/console/login`; no validation
  of its own.

## Integration Points

- **Guardrails (008)** - reads `spec.environment`, `spec.region`, `metadata.name`, and
  `tenant.Department` to select which guardrail rules target an account (design.md §3.3) - Key
  functions: `tenant.Department` - Notes: depends on 006 for both the CRD fields and the label
  reader.
- **Account & Identity modules (009/012/017)** - call `tenant.ResolveName` to build the
  `CREATE ACCOUNT` statement's account name and to import/bind groups - Key functions:
  `tenant.ResolveName` - Notes: called fresh on every reconcile; it's pure and cheap, so nothing
  caches it.
- **Quota-check (011)** - reads `spec.creditQuota` across every `SnowflakeAccount` in a namespace
  and `tenant.CreditQuota` for the namespace ceiling - Key functions: `tenant.CreditQuota` - Notes:
  the admission math itself belongs to 011, not to this spec.
- **Quota-monitor (018)** - reads `spec.creditQuota` and `tenant.CreditQuota` to size the Snowflake
  resource monitor for this account - Key functions: `tenant.CreditQuota` - Notes: the resource-monitor
  arithmetic itself belongs to 018, not to this spec.
- **SnowflakeAccount controller (020)** - wires the CRD into `managed.NewReconciler`, calls every
  `internal/account/tenant` function, and sets `status.accountName`/`accountLocator`/`accountUrl` - Key
  functions: all of `internal/account/tenant`'s public API - Notes: this spec defines the type and helpers
  only; 020 owns `Observe`/`Create`/`Update`/`Delete`.

## Success Criteria

- **SC-001**: `apis/base/v1alpha1` registers group `base.snowflake.yukimi.io`, version `v1alpha1`.
- **SC-002**: `SnowflakeAccountSpec`/`SnowflakeAccountStatus` cover every field in the Schema
  Specification tables above, with matching JSON names.
- **SC-003**: `region` and `environment` carry `x-kubernetes-validations` CEL rules that reject a
  changed value on update but impose no constraint on create.
- **SC-004**: attempting to change an existing `SnowflakeAccount`'s `metadata.name` is rejected by
  the Kubernetes API server itself — no CEL rule needed or present for it.
- **SC-005**: `environment` accepts only `dev` or `prod`; any other value is rejected at admission.
- **SC-006**: a `SnowflakeAccount` whose `identityIntegration.roleBindings` omits `ACCOUNTADMIN` is
  rejected at admission.
- **SC-007**: a `customAuthRules.exceptions` entry naming neither `rsaKeyAllowed` nor `patAllowed`
  is rejected at admission.
- **SC-008**: `SnowflakeAccountSpec` carries exactly one crossplane-runtime-owned field
  (`managementPolicies`); no `providerConfigRef` or `writeConnectionSecretToRef` field exists
  anywhere in the generated CRD schema (grep-provable in `package/crds/*.yaml`).
- **SC-009**: `tenant.ResolveName("analytics-team-eu", "finance")` returns
  `"analytics_team_eu_5k3wf"`, matching design.md §3.12's worked example exactly.
- **SC-010**: `tenant.ResolveName` translates every `-` in `metadata.name` to `_` and is
  deterministic — same inputs always produce the same output, with no stored state.
- **SC-011**: `tenant.Department`, `tenant.CostCenter`, and `tenant.CreditQuota` each return a
  user error (readable message, per Error Classification's deliberate deviation) when their label
  is absent or empty from the input map.
- **SC-012**: `tenant.CreditQuota` returns a user error for a non-integer or negative label value,
  and the parsed `int` otherwise.
- **SC-013**: `tenant.AccountURL`'s error path matches spec 004's `host.URL` error path exactly —
  verified by a shared test case, not a re-implementation.
- **SC-014**: `internal/account/tenant` imports nothing beyond the Go standard library, `internal/errors`,
  and `internal/snowflake/host` (grep-provable — no Kubernetes or Snowflake-driver import).
- **SC-015**: unit test coverage exceeds 95% for `internal/account/tenant`.
- **SC-016**: `make generate` produces a valid CRD manifest and `make reviewable` passes.

## Security Considerations

- Per §3.11.1, the namespace remains the sole trust anchor for tenancy: no field on
  `SnowflakeAccountSpec` lets a tenant name or override their own namespace or account identity.
- `department`, `cost-center`, and `credit-quota` stay namespace labels, never CRD fields, so a
  tenant cannot self-escalate their department's guardrail scope or their credit ceiling by
  editing Git-committed YAML.
- No `providerConfigRef` field exists on this type, so there is no way for a tenant to point the
  controller at credentials or configuration outside their own namespace's trust anchor.

## References

- **Product design**: `specs/design.md` §3.1, §3.6, §3.7, §3.8, §3.9, §3.10, §3.11.1, §3.11.3,
  §3.12, §7.1, §7.2, and chapter 2 — the authoritative source for every field, validation rule, and
  helper behavior in this spec.
- **Shape reference**: `specs/001-error-and-logging.md` - the section skeleton this spec follows,
  per the (now superseded) scope note's own instruction.
- **Host package contract**: `specs/004-connection-pooling.md` - defines
  `internal/snowflake/host.URL`, which `internal/account/tenant.AccountURL` wraps unchanged.
- **crossplane-runtime v2**: `pkg/resource/interfaces.go`, `apis/common/v1/resource.go`,
  `apis/common/v2/resource.go` - confirms `managed.NewReconciler` requires only the base
  `resource.Managed` interface, not a provider-config reference.

<br/>

================

## Appendix: Usage Examples

**Example 1: Resolving an account's Snowflake name**

```go
name := tenant.ResolveName("analytics-team-eu", "finance")
// name == "analytics_team_eu_5k3wf"
```

**Example 2: Building an account's browser login URL**

```go
url, err := tenant.AccountURL("xc19114", "aws-eu-central-1", true)
if err != nil {
    return err // user error: malformed region, per spec 004
}
// url == "https://xc19114.eu-central-1.privatelink.snowflakecomputing.com/console/login"
```

**Example 3: Reading onboarding metadata from a namespace's labels**

```go
department, err := tenant.Department(ns.Labels)
if err != nil {
    return log.Handle(err) // user error: readable message, though ops-caused (see Error Classification)
}

quota, err := tenant.CreditQuota(ns.Labels)
if err != nil {
    return log.Handle(err)
}
```

**Example 4: A `SnowflakeAccount` exercising every field this spec defines**

```yaml
apiVersion: base.snowflake.yukimi.io/v1alpha1
kind: SnowflakeAccount
metadata:
  name: analytics-team-eu      # created in Snowflake as analytics_team_eu_5k3wf (3.12)
spec:
  # --- General metadata ---
  description: "Analytics team Snowflake environment for EU operations"
  contacts:
    - alice.smith@company.com
    - team-analytics@company.com
  # --- Snowflake account configuration ---
  region: aws-eu-central-1
  environment: prod            # dev | prod — required, immutable (3.11.3)
  # --- Share of the namespace's monthly credit allowance (3.10) ---
  creditQuota: 500
  # --- GIAM groups to import and bind to system roles ---
  identityIntegration:
    groups:                          # one group list per identity integration (3.7)
      giam:                          # every group to import; key is free-form, not part of the schema
        - XYZ_DATA_ENGINEERS
        - XYZ_DEVELOPERS
        - XYZ_ANALYSTS
    roleBindings:                    # system role → group it is bound to; ACCOUNTADMIN required
      ACCOUNTADMIN: XYZ_DATA_ENGINEERS
      SYSADMIN: XYZ_DEVELOPERS       # any Snowflake system role may be bound
  # --- Allow custom network rules ---
  customNetworkRules:
    serviceUsers:                # one entry per service user, deny-by-default (3.8)
      tu_airflow:
        - connection: agn        # from the region's inventory in the Backplane Config
          allowedIPs: ["172.16.45.0/24"]
        - connection: dbt-cloud  # VPCE-only: nothing to narrow
    accountWide:                 # added to the account policy (3.6); service users
      - connection: public       # have their own policy and ignore this (3.8)
        allowedIPs: ["192.0.2.14/32", "192.0.2.15/32"]  # /32 only — see 3.3
  # --- Allow human users to bypass SSO (3.9) ---
  customAuthRules:
    exceptions:
      - user: alice.smith
        rsaKeyAllowed: true
        patAllowed: false
        reason: "Legacy desktop tool without SSO support"
```
