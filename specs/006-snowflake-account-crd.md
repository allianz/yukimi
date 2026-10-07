# Specification: SnowflakeAccount CRD & Tenant Helpers (006)

This specification covers two packages: `apis/base/v1alpha1/` (the `SnowflakeAccount` types) and `internal/account/tenant/` (tenant helpers).

## Overview

Teams request Snowflake accounts by creating `SnowflakeAccount` resources in Kubernetes. This spec
defines only the custom resource definition (CRD) for them: the fields a team can set, the status
the platform reports back, and the basic rules a resource must satisfy. It also provides small
helpers that later specs use to work out a tenant's account identity. The spec defines no business
logic: creating and managing the actual Snowflake accounts is the job of later specs.

## Key Concept: Immutable Fields After Creation

Some fields are fixed once the account exists:

- **Name** — identifies the resource in Kubernetes and is part of the account's secret
  identifier, so renaming it would cut the account off from its credentials.
- **Region** — an account cannot move to another region.
- **Environment** — DEV/PROD decides which policies apply, so switching it could leave the account
  violating many of them at once.
- **Description** — Snowflake cannot change it after creation.

## Key Concept: Only Structural Admission Checks

This spec defines only format validation checks in the CRD, such as a pattern for the region.
Whether that region is actually supported is decided later by the business logic. The two kinds of failure look different:

- **Format check fails** — Kubernetes rejects the resource, and it is never stored in the cluster.
- **Business logic fails** — the resource is stored in the cluster and shows a sync error in its
  status.

## Key Concept: Only Ops Controls the Namespace

Platform ops creates each tenant's namespace during onboarding and labels it with onboarding facts
such as department, cost center, and credit quota. Tenants cannot rename the namespace, move
resources into another one, or change its labels. Only ops can. So the platform trusts the namespace
name and labels, but not anything a tenant writes in the `SnowflakeAccount` itself.

The namespace name is part of the secret identifier that holds each account's credentials (003).
Because the controller takes it from where the resource lives, a tenant can only ever reach
credentials of accounts in its own namespace.

## Key Concept: A Plain Kubernetes Resource, Not a Crossplane Provider

This project is not a Crossplane provider; it only borrows Crossplane's reconciler machinery.
So `SnowflakeAccount` is shaped like a plain Kubernetes resource, without Crossplane's `forProvider` / `atProvider` wrappers. It also has no provider-config or connection-secret reference. The resource carries only the Crossplane fields the reconciler needs: management policies and status conditions.

## Public API

```go
// Package v1alpha1 — apis/base/v1alpha1
package v1alpha1

// SnowflakeAccountSpec defines the desired state of a SnowflakeAccount. Every
// field is a direct sibling under spec — there is no forProvider wrapper
// (Key Concept: A Plain Kubernetes Resource, Not a Crossplane Provider).
type SnowflakeAccountSpec struct {
	// Immutable after creation: Snowflake does not support altering an
	// account's COMMENT after CREATE ACCOUNT (verified directly against
	// Snowflake; design.md does not document this — see Key Concept:
	// Immutable Fields After Creation). Mapped to COMMENT in CREATE ACCOUNT
	// (design.md 3.6).
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="description is immutable"
	Description string `json:"description,omitempty"`

	// +kubebuilder:validation:Pattern=`^[^\s@]+@[^\s@]+\.[^\s@]+$`
	Contact string `json:"contact"`

	// Immutable after creation (design.md 3.11.3). Structural cloud-region
	// shape, checked by the API server before the account ever exists.
	// Reuses internal/config/base.orgAdminRegionPattern's cloud allowlist
	// (002) — aws/azure/gcp are the clouds a Snowflake org's account may
	// live on. Whether the region is actually offered is resolved later
	// against the Backplane Config (007) / Guardrails (008), not here.
	// +kubebuilder:validation:Pattern=`^(aws|azure|gcp)-[a-z][a-z0-9-]*$`
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

// A SnowflakeAccount is the resource a team creates to describe the
// Snowflake account they want (design.md 3.1). Both rules below are
// root-level, not on Spec, because metadata.name isn't a field Spec defines.
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 55",message="metadata.name must be 55 characters or fewer, so the resolved Snowflake account name combined with the organization name (design.md 3.12) stays within Snowflake's 63-character DNS label limit even for a single-character organization name; the account module (012) checks the exact combined length against the real organization name"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z][a-z0-9-]*$')",message="metadata.name must start with a lowercase letter and contain only lowercase letters, digits, and '-', so the resolved Snowflake account name (design.md 3.12) is always a valid Snowflake identifier"
type SnowflakeAccount struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SnowflakeAccountSpec   `json:"spec"`
	Status SnowflakeAccountStatus `json:"status,omitempty"`
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
// Returns: System error if the label is missing or empty — only ops can fix
// it (see Error Classification).
func Department(labels map[string]string) (string, error)

// CostCenter returns the ops-set "cost-center" namespace label (design.md
// chapter 2). No spec currently consumes the returned value; this reader
// exists so that whichever spec adds the first consumer doesn't also need to
// touch this package.
//
// Returns: System error if the label is missing or empty, as for Department.
func CostCenter(labels map[string]string) (string, error)

// CreditQuota returns the ops-set "credit-quota" namespace label (design.md
// chapter 2 and 3.10), parsed to an int.
//
// Returns: System error if the label is missing, empty, or not a valid
// non-negative integer.
func CreditQuota(labels map[string]string) (int, error)

// AlphaTester returns whether the namespace carries the ops-set "alpha-tester"
// label (design.md chapter 2), consumed by the account module (012) to
// bypass a region's Backplane Config (007) availability gate. Unlike
// Department/CostCenter/CreditQuota, this label is optional: most namespaces
// don't carry it, so a missing or empty value means "not an alpha tester"
// rather than an error.
//
// Returns: System error if the label is present but not a valid boolean.
func AlphaTester(labels map[string]string) (bool, error)

// AccountURL returns the SnowflakeAccount's status.accountUrl (design.md
// 7.2): the account's login URL — host.URL's bare host plus
// "/console/login" — built from the locator Snowflake assigned at CREATE
// ACCOUNT (012) and the CRD's region. It never derives from the resolved
// account name, which has no relationship to the locator. Wraps
// internal/snowflake/host.URL (004); adds no validation beyond that call.
//
// Parameters:
//   - locator: the account locator returned by CREATE ACCOUNT (e.g. "xy12345").
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

### Fields (metadata)

| Field Path | Type | Required | Mutability | Validation/Constraints |
|---|---|---|---|---|
| `name` | string | Yes | Immutable (Kubernetes-enforced, no CEL needed) | Two root-level `XValidation` rules: `size(self.metadata.name) <= 55` (63, Snowflake's DNS label limit on `<org>-<resolvedName>`, minus the 6 characters `ResolveName` (§3.12) always appends, minus 1 for the joining `-`, minus 1 for the shortest possible organization name — the exact bound for the real organization name is checked by the account module, 012), and `self.metadata.name.matches('^[a-z][a-z0-9-]*$')` (starts with a lowercase letter, only lowercase letters/digits/`-` after — the shape `ResolveName` needs to always produce a valid Snowflake identifier) |

### Fields (spec)

| Field Path | Type | Required | Mutability | Validation/Constraints |
|---|---|---|---|---|
| `description` | string | No | Immutable | `MaxLength`: 1024 (product choice, not a discovered Snowflake limit); `XValidation`: `self == oldSelf` (Snowflake's `COMMENT` can't be altered post-creation — see Key Concept: Immutable Fields After Creation) |
| `contact` | string | Yes | Mutable | `Pattern`: `` `^[^\s@]+@[^\s@]+\.[^\s@]+$` `` — email shape, checked by the API server; carried into `CREATE ACCOUNT`'s `EMAIL` (012) |
| `region` | string | Yes | Immutable | `Pattern`: `` `^(aws|azure|gcp)-[a-z][a-z0-9-]*$` `` — identical to 002's `base.orgAdminRegionPattern`; region availability enforced by Guardrails (008), not here |
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
| `managementPolicies[]` | string | No | Mutable | crossplane-runtime field; default `["*"]`. No `providerConfigRef` or `writeConnectionSecretToRef` field exists on this type (Key Concept: A Plain Kubernetes Resource, Not a Crossplane Provider) |

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
├── labels.go         # Department, CostCenter, CreditQuota, AlphaTester (chapter 2)
├── labels_test.go
├── url.go            # AccountURL: internal/snowflake/host.URL + "/console/login"
├── url_test.go
└── doc.go
```

`internal/account/tenant` imports nothing beyond the Go standard library and
`internal/snowflake/host` — no Kubernetes client, no Snowflake driver. The label readers take
`map[string]string`, never a `corev1.Namespace`, specifically to keep that boundary intact; the
caller (020) already has the namespace object from its own reconcile and passes its labels in.

## Error Classification

**User Errors** (use `errors.NewUserError()`):
- Malformed region passed to `AccountURL`: `region 'Frankfurt!' does not match the expected cloud-region format (expected: aws-eu-central-1)`. Constructed and classified by `host.URL` (004); this package passes it through unchanged and creates no user errors of its own.

**System Errors** (use `fmt.Errorf("context: %w", err)`):
- Required label missing or empty (`Department`, `CostCenter`, `CreditQuota`): `namespace missing required label 'department'; contact platform ops`
- `credit-quota` label not a non-negative integer: `namespace label 'credit-quota' must be a non-negative integer, got "lots"; contact platform ops`
- `alpha-tester` label (optional) present but not a boolean: `namespace label 'alpha-tester' must be a boolean, got "yes"; contact platform ops`

<br/><br/><br/><br/><br/>

================

## Appendix: Code Generation Details

## Scope

- The `SnowflakeAccount` CRD type in `apis/base/v1alpha1/`, group `base.snowflake.yukimi.io`,
  version `v1alpha1` (design.md §3.1).
- Structural validation drawn directly from this chapter, expressed as
  `x-kubernetes-validations` CEL rules: `region`/`environment` immutability after creation
  (§3.11.3), the `environment` enum, `identityIntegration.roleBindings` requiring an
  `ACCOUNTADMIN` entry (§3.7), and a `customAuthRules.exceptions` entry naming at least one of
  `rsaKeyAllowed`/`patAllowed` (§3.9). `region` additionally carries a `Pattern` marker rejecting
  a value with no valid cloud-region shape (e.g. `aaa`) or an unrecognized cloud, reusing spec
  002's `base.orgAdminRegionPattern` allowlist; a root-level rule on
  `metadata.name` rejects a name too long for the resolved Snowflake account name (§3.12) to fit
  Snowflake's identifier limit (see Key Concept: Only Structural Admission Checks).
- The `status.accountName` / `accountLocator` / `accountUrl` / `conditions` shape (§7.2).
- The `internal/account/tenant/` package: `ResolveName` (§3.12), the `Department`/`CostCenter`/
  `CreditQuota`/`AlphaTester` namespace-label readers (chapter 2), and `AccountURL` (§7.2, built on
  spec 004's host package).

### Out of Scope

- Guardrails constraint/preset enforcement (naming patterns, credit ceilings, network CIDR
  limits, *which* regions are actually allowed/available for a given account) — spec 008. This
  spec only rejects a `region` that is syntactically impossible; whether a well-formed region is
  offered at all is entirely Guardrails'/Backplane Config's call (007/008).
- Backplane Config lookups and any bootstrapping, network, or auth SQL (§3.6, §3.8, §3.9) — specs
  007, 012, 014, 015.
- Controller reconciliation: `Observe`/`Create`/`Update`/`Delete`, condition-setting, finalizers —
  spec 020.
- Quota admission math (011), quota enforcement (018), identity sync (016/017), deletion requests
  (019), replication (021).
- Duplicate-connection detection within `customNetworkRules` (§3.8). Expressing "no repeated
  connection name in this list" in CEL is disproportionately complex for a check that's simple to
  make once a Go module exists to call it; deferred to the network module (014).
- Cross-referencing `identityIntegration.roleBindings` values against `identityIntegration.groups`
  entries — deferred to the identity module (017), which is the first module that actually needs
  both sides of that mapping to be true.

## Edge Cases

- **Two tenants both name an account `dev` — does `ResolveName` collide?** No: the namespace-hash
  suffix is derived from `metadata.namespace`, which differs between tenants by construction, so
  `dev` in `finance` and `dev` in `analytics` resolve to different Snowflake names.
- **A namespace is missing `department`/`cost-center`/`credit-quota` entirely — does the CRD fail
  validation?** No — these are namespace labels, not CRD fields, so nothing about the
  `SnowflakeAccount` resource itself is invalid. The failure surfaces only when a caller (008, 011,
  018) invokes the corresponding `internal/account/tenant` reader and gets a system error back (see Error
  Classification).
- **Do the `region`/`environment` CEL rules block the first `CREATE`?** No — `oldSelf` doesn't
  exist yet on create, so both rules only evaluate (and can only fail) on `UPDATE`.
- **Why is `description` immutable when design.md §3.11.3 doesn't list it alongside
  `region`/`name`/`environment`?** Because design.md is silent on `description` mutability
  entirely, not because it calls for it to be mutable. Direct verification against Snowflake found
  that `COMMENT` (`description`'s target, design.md §3.6) cannot be altered once `CREATE ACCOUNT`
  has run.
- **Is a malformed `region` (e.g. `aaa`) rejected the same way as a Guardrails violation?** No —
  the `Pattern` marker is a schema check, so the API server itself rejects the write before the
  object is ever persisted; the controller never observes it, never reconciles it, and never gets a
  chance to report it on `Synced` (design.md §3.3). A `region` that is well-formed but simply not
  offered is a different failure entirely: it persists, reconciles, and is rejected later by
  Guardrails (008) with a message on `Synced`.
- **Why no *immutability* CEL rule for `metadata.name`?** Kubernetes already rejects any attempt
  to change an object's `name`; there's nothing left for this CRD's schema to enforce there. The
  length rule is a separate, unrelated concern (Key Concept: Only Structural Admission Checks) — it
  fires on create too, not just update.
- **A `metadata.name` at or over the 55-character ceiling — rejected the same way as the length
  problem found in testing?** No, and that's the point: the root-level `XValidation` rejects it at
  admission, before the object is ever persisted, so the controller never observes it and never
  attempts to write platform credentials or call `CREATE ACCOUNT` for it. Before this rule existed,
  an over-long name reached a real `CREATE ACCOUNT` call, which Snowflake itself rejected — not for
  exceeding a flat 255-character bare-identifier limit, but for exceeding the 63-character DNS
  label limit on `<org>-<resolvedName>`, the combined form Snowflake actually validates — but only
  *after* the account module had already written credentials to the secret store, permanently
  wedging the resource on retry. 55 is the largest `metadata.name` that stays within that
  63-character limit for any organization name, however short; the account module (012) still
  re-checks the exact combined length against the real organization name, since this CRD has no
  visibility into it. The secret-write/retry behavior itself is a separate, known gap this spec
  does not fix.
- **A `metadata.name` with a leading digit (e.g. `9-team`) or a dot (e.g. `my.team`) — does
  Kubernetes already reject these?** No — both are legal under Kubernetes' own DNS-1123-subdomain
  name validation, and `ResolveName` never translates either away (only `-` becomes `_`). Before
  the shape `XValidation` rule existed, both reached a live `CREATE ACCOUNT` call and failed there
  (a leading digit fails Snowflake's bare-identifier rule; a dot is never a valid identifier
  character) — the same wedge-bug shape as the length case above. The shape rule rejects both at
  admission instead.

## Dependencies

- **internal/snowflake/host (004)** - Used APIs: `host.URL(locator, region, usePrivateLink)` -
  Contract: `internal/account/tenant/url.go` calls `host.URL` and appends `/console/login`; no validation
  of its own, and the user error `host.URL` produces for a malformed region passes through unchanged.

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
- **SC-003a**: `region` carries a `Pattern` marker, identical to spec 002's
  `base.orgAdminRegionPattern`, that rejects a value with no valid cloud-region shape (e.g. `aaa`)
  or an unrecognized cloud (e.g. `oracle-eu-1`) on both create and update.
- **SC-003b**: a root-level `XValidation` rule on `SnowflakeAccount` rejects a `metadata.name`
  longer than 55 characters on both create and update; a name of exactly 55 characters is
  accepted.
- **SC-003d**: a second root-level `XValidation` rule rejects a `metadata.name` that starts with a
  digit (e.g. `9-team`) or contains a dot (e.g. `my.team`) on both create and update;
  `analytics-team-eu` is accepted.
- **SC-003c**: `description` carries a `MaxLength` marker that rejects a value longer than 1024
  characters; a description of exactly 1024 characters is accepted.
- **SC-003e**: `description` carries an `x-kubernetes-validations` CEL rule that rejects a changed
  value on update but imposes no constraint on create, matching Snowflake's inability to alter
  `COMMENT` after account creation.
- **SC-004**: attempting to change an existing `SnowflakeAccount`'s `metadata.name` is rejected by
  the Kubernetes API server itself — no CEL rule needed or present for that.
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
  system error (not a user error) when their label is absent or empty from the input map.
- **SC-012**: `tenant.CreditQuota` returns a system error for a non-integer or negative label value,
  and the parsed `int` otherwise.
- **SC-012a**: `tenant.AlphaTester` returns `false, nil` when the label is absent or empty, the
  parsed `bool` for a valid `"true"`/`"false"` value, and a system error for any other present value.
- **SC-013**: `tenant.AccountURL`'s error path matches spec 004's `host.URL` error path exactly —
  verified by a shared test case, not a re-implementation.
- **SC-014**: `internal/account/tenant` imports nothing beyond the Go standard library
  and `internal/snowflake/host` (grep-provable — no Kubernetes or Snowflake-driver import).
- **SC-015**: unit test coverage exceeds 95% for `internal/account/tenant`.
- **SC-016**: `make generate` produces a valid CRD manifest and `make reviewable` passes.

## Security Considerations

- Per §3.11.1, the namespace remains the sole trust anchor for tenancy: no field on
  `SnowflakeAccountSpec` lets a tenant name or override their own namespace or account identity.
- `department`, `cost-center`, and `credit-quota` stay namespace labels, never CRD fields, so a
  tenant cannot self-escalate their department's guardrail scope or their credit ceiling by
  editing their `SnowflakeAccount`.
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
url, err := tenant.AccountURL("xy12345", "aws-eu-central-1", true)
if err != nil {
    return err // user error: malformed region, per spec 004
}
// url == "https://xy12345.eu-central-1.privatelink.snowflakecomputing.com/console/login"
```

**Example 3: Reading onboarding metadata from a namespace's labels**

```go
department, err := tenant.Department(ns.Labels)
if err != nil {
    return log.Handle(err) // system error: ops must fix the namespace labels (see Error Classification)
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
  contact: alice.smith@company.com
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
