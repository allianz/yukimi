> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `006`'s intended
> *scope*, not its content. When writing `006-snowflake-account-crd.md`, the sole sources of
> truth are `specs/design.md` and the prompt given at spec-writing time — rework, restructure,
> or discard anything below freely. Please keep this file up to date until
> `006-snowflake-account-crd.md` has been written, then delete it.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003.a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003.a` < `003.b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Packages: `apis/base/v1alpha1/` (SnowflakeAccount), `internal/tenant/`. Covers design 3.1, 3.11.3,
  3.12, 7.2 and the chapter 2 namespace labels. Depends on: 001, 002.
- This is **not a Snowflake API** — Snowflake is driven purely by SQL (005). It is the Kubernetes CRD
  plus the namespace-derived identity helpers. It is also **not schema-only**: `internal/tenant/` is
  real, unit-testable code with no Kubernetes or Snowflake dependency.
- Scope — the CRD (the full 3.1 schema):
  - General: `description`, `contacts[]`.
  - Snowflake configuration: `region`, `environment` (`dev` or `prod`, required, immutable).
  - `creditQuota` — this account's share of the namespace allowance.
  - `identityIntegration.groups{<integration-key>: [group names]}`, where the key is free-form rather
    than schema (`giam` is the only one configured today), and
    `identityIntegration.roleBindings{SYSTEM_ROLE: group}` with `ACCOUNTADMIN` strictly required.
  - `customNetworkRules.serviceUsers{<user>: [{connection, allowedIPs[]}]}` and
    `customNetworkRules.accountWide[{connection, allowedIPs[]}]`.
  - `customAuthRules.exceptions[{user, rsaKeyAllowed, patAllowed, reason}]`.
  - Status (7.2): `accountName` (the **resolved** Snowflake name),`accountLocator`, `accountUrl`, `conditions`.
  - **CEL `x-kubernetes-validations` for the 3.11.3 immutability** of `region`, `name` and
    `environment`. `region` and `name` immutability prevents identity spoofing: create an account,
    let the secret generate, then repoint the CRD at a different target while keeping the
    credentials. `environment` immutability exists for a different reason — it selects which
    guardrails apply, so a mutable field would let an account be created under `prod` and then
    flipped to `dev` for its looser network posture.
- Scope — `internal/tenant/`:
  - `naming.go` (3.12): the resolved Snowflake name is `metadata.name` with every `-` translated to
    `_`, followed by `_` plus the first 5 characters of the base32-encoded SHA-256 of
    `metadata.namespace`. Example: `analytics-team-eu` in namespace `finance` becomes
    `analytics_team_eu_5k3wf`. This is needed because Snowflake account names must be unique
    org-wide, while tenants name freely and cannot see other namespaces — two teams both naming an
    account `dev` is an expected case. It requires no stored state: namespaces cannot be renamed and
    `name` is immutable, so the value is recomputable on every reconcile and stable for the life of
    the account. Only Snowflake-facing values use it (the SQL in 3.6 and 3.8, and the status in 7.2);
    everything else refers to accounts by CRD name.
  - `labels.go`: read the ops-set namespace labels applied during onboarding (chapter 2) —
    `department` (consumed by guardrail targeting in 008), `credit-quota` (consumed by 016), and
    `cost-center`. These are ops-owned and deliberately not CRD fields, so tenants cannot alter them.
  - `url.go`: `accountUrl` is built from the account locator Snowflake assigns at `CREATE ACCOUNT`
    (010), not the resolved name — `https://<locator>.<region>.privatelink.snowflakecomputing.com`
    when the region's backplane config has PrivateLink enabled (for example
    `https://xc19114.eu-central-1.privatelink.snowflakecomputing.com`), or
    `https://<locator>.<region>.snowflakecomputing.com` otherwise. The locator is opaque and has no
    relationship to the resolved name or the CRD, so `url.go` takes it as an input parameter rather
    than deriving it — 010 captures it from `CREATE ACCOUNT`'s result and passes it in, which keeps
    this package free of any Snowflake dependency.
- **Blocker resolved**: `hack/helpers/apis/GROUP_LOWER/APIVERSION/groupversion_info.go.tmpl` used
  to hardcode `.allianz.io` in both the `+groupName` marker and the `Group` constant; it now emits
  `.yukimi.io`, so `make provider.addtype provider=Snowflake group=base` correctly produces
  `base.snowflake.yukimi.io`.
- Open question for this spec: `cost-center` is read by nothing in design.md. Confirm with ops
  whether it belongs in the account `COMMENT` or as a Snowflake tag; otherwise document it as
  ops-only metadata.

## Cross-cutting context from the roadmap

- **Deliberately unnumbered — Chapter 2 (Tenant Onboarding).** Onboarding is out of scope entirely:
  it is ops tooling (bash today, Terraform later) that creates the namespace, labels, repository and
  ArgoCD application. The platform only *reads* the labels it produces (here, in `labels.go`).

## TODO — settled by 004 (apply when writing 006)

The `url.go` bullet above predates `specs/004-connection-pooling.md`, which now owns the
host and URL construction in a leaf package `internal/snowflake/host`. When 006 is
written:

1. `url.go` calls `host.URL(locator, region, usePrivateLink)` from
   `internal/snowflake/host` rather than building the string itself. 004 owns the
   region→host-segment mapping (`aws-eu-central-1` → `eu-central-1`, `aws-eu-west-3` →
   `eu-west-3.aws`) and the `.privatelink.` suffix, so a second implementation here
   would let `status.accountUrl` advertise a host the connection pool never dials.
2. PrivateLink comes from `BaseConfig.Snowflake.UsePrivateLink` (002), supplied by the
   controller (018) — not from the region's backplane config as the bullet above says:
   the Backplane Config schema (design.md 3.5, spec 007) carries no such field, and 007
   sorts above 006 so it could not be read from here in any case. design.md 3.6 states
   this too.
3. "Free of any Snowflake dependency" means no driver, no client, no network. Importing
   `internal/snowflake/host` (standard library plus `internal/errors` only) preserves
   that intent and keeps `internal/tenant` unit-testable without Snowflake access.
4. `status.accountUrl` is scheme plus host per design.md 7.2 — no `/console/login` path.
   Snowflake redirects a bare host to the login console on its own.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).
