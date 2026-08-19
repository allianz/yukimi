> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `008`'s intended
> *scope*, not its content. When writing `008-guardrails.md`, the sole sources of truth are
> `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or
> discard anything below freely. Please keep this file up to date until `008-guardrails.md`
> has been written, then delete it.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003-a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003-a` < `003-b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Package: `internal/guardrails/`. Covers design 3.3 and 3.4. Depends on: 001, 002, 006.
- Concept: a gatekeeper that validates and modifies tenant input **before** anything reaches
  Snowflake. It applies exclusively to `SnowflakeAccount`; the other kinds validate themselves.
- Scope — a ConfigMap loader plus evaluation:
  - `target` selects which accounts a guardrail applies to; an omitted field or `"*"` matches all.
    Each key has a fixed source: `environment` and `region` come from CRD spec fields, `account`
    comes from `metadata.name` **as the tenant wrote it** (not the resolved name — guardrails run
    before the account exists), and `department` comes from the ops-set namespace label. Because
    `department` is ops-owned, tenants cannot move themselves out of their department's rules;
    `environment`, by contrast, they declare themselves.
  - `constraints` — strict rules the input must pass: an `accountName` regex, a `groupNames` regex
    (applied to every group under `identityIntegration.groups`), `maxCreditQuota`,
    `allowedRegions[]`, and `connections`.
  - `preset` — defaults, with two distinct behaviors that the spec must state explicitly. For CRD
    fields the user omitted (for example `creditQuota`) the value is filled in **and then enforced as
    usual**. For account settings with no corresponding CRD field (for example `timeZone`) it is only
    an initial value: not enforced, and the tenant's account admin may change it later in Snowflake.
  - **Connection constraints**, scoped first by `serviceUsers` / `accountWide` (mirroring the CRD
    keys) and then by connection name, each carrying one of three rules: `"/NN"` means a CIDR is
    required but capped at that maximum width; `"full"` means the user may not specify a range and
    inherits the connection's full predefined range; `"off"` means the connection is forbidden. Any
    connection not explicitly listed falls back to that scope's `"*"` entry.
  - **Top-down merge**: broad global baselines are applied first, then narrower guardrails. A later
    match replaces what it overrides, whether that tightens or loosens the rule.
  - `exceptions.go` (3.4): when a CRD fails its guardrails the controller does **not** reject
    immediately. It first checks the ops-owned exceptions file for a matching approved exception and
    lets the otherwise-failing input through if one exists. Matching is an **exact** `account` match
    on the CRD name with no wildcards, against the full entry verbatim, and is consulted **only
    after** a guardrail has rejected. The approval workflow is email-driven and lives outside the
    platform (customer → ISO → ops edits the file); the file is simply the durable record.
- Deliberately **not** importing 007: guardrails constrain prefix width while the backplane
  constrains containment, and neither subsumes the other. For the `"full"` rule this spec only checks
  that `allowedIPs` is absent; resolving it to concrete CIDRs is 012's job.
- Also out of scope: cross-artifact consistency, such as a guardrail naming a connection absent from
  a region's inventory. That surfaces as a rule-creation failure in 012.
- Note for the spec: because tenants set `environment` themselves, they can choose `dev` and receive
  its looser constraints. The platform does not verify that a `dev` account is really used for
  development; a team running production workloads there carries that risk themselves.

## Cross-cutting context from the roadmap

- **Decision — guardrails, approved exceptions and backplane config all live in mounted ConfigMaps.**
  Specs 007 and 008 own no `apis/` types — only a loader plus validation and evaluation logic.
- **Decision — reconcile-time validation, not an admission webhook.** Invalid input is admitted by
  the API server and then reported as `Synced=False` with a user error. Cheap structural checks —
  notably the 3.11.3 immutability of `region`, `name` and `environment` — use CRD CEL
  `x-kubernetes-validations` instead (owned by 006). There is no webhook spec.
- **Why 008 does not import 007.** The two perform independent checks: a guardrail `"/16"` constrains
  prefix *width*, while a backplane `maxCidrs: ["172.16.0.0/12"]` constrains *containment*.
  `10.0.0.0/16` passes the first and fails the second, so neither subsumes the other. For the
  `"full"` rule spec 008 only checks that `allowedIPs` is *absent*; resolving `full` into concrete
  CIDRs is deferred to 012, which already holds the region entry.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).
