> **Scope context only — not a specification.** This file gives a starting-point idea of spec `016`'s
> intended *scope*, not its content. When writing `016-quota-check.md`, the sole sources of truth are
> `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or discard
> anything below freely. Please keep this file up to date until `016-quota-check.md` has been written,
> then delete it.
>
> This file, together with `specs/scope-017-quota-monitor.md`, supersedes the earlier
> `specs/scope-016-quota.md`, which planned quota as a single spec with three entry points
> (`Admit`/`Apply`/`Observe`), `Admit` called out-of-band by the controller's (019) validation phase. A design
> conversation split that plan in two: this file covers the admission half, folded into the pipeline
> itself as its own module rather than an out-of-band call.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003.a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003.a` < `003.b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

Spec numbers track dependency/write order, not pipeline runtime order — see the "Decisions from
design conversation" section below for why this module runs *before* the account module (010) in the
pipeline despite carrying a higher spec number.

## Scope

- Package: `internal/account/modules/quotacheck/`. Covers design 3.10's admission half (the "checks the
  share of credits... against the namespace allowance" half of the §3.2 flow's Credit Quota step).
  Depends on: 006 (tenant labels, CRD types), 009 (pipeline `Module` interface).
- **The check**: on `Apply`, sum this CRD's claimed `creditQuota` plus every sibling `SnowflakeAccount`
  CR's claimed `creditQuota` in the same namespace, and compare the total against `tenant.CreditQuota`
  (the namespace's `credit-quota` label, set by ops at onboarding — see design 2 and 3.10). Exceeding the
  allowance is `Rejected(err).Aborting()` — `Rejected` because it's the tenant's own input being refused,
  `Aborting` because nothing after this module should run when it doesn't fit.
- **Registered first**, ahead of the account module (010), in 019's pipeline module list. This is what
  makes the abort meaningful: a create never reaches `CREATE ACCOUNT`, and an update that raises
  `creditQuota` beyond the allowance never reaches any other module's `Apply`, when the claim doesn't
  fit.
- **`Observe` is a no-op**: always `true, Done()`. No read-back, no drift detection — this module only
  ever acts at `Apply` time, on create or on a spec change that moves the generation counter.
- **First-come-first-served on reductions**: if ops lowers the namespace allowance, existing accounts
  are **never retroactively touched** — this module has no repair or suspension path of its own. Only a
  future `Apply` (a new sibling create, or any edit to this CRD that bumps its generation) re-evaluates
  the sum and can block.
- Needs neither `OrgAdminDB` nor `TenantDB` — it never opens a Snowflake connection, which is exactly
  why it can safely run before the account module has produced a locator.

## Open question to raise when writing this spec

Summing sibling `SnowflakeAccount` CRs needs a Kubernetes list capability that `ModuleContext` doesn't
provide today (`internal/account/pipeline/context.go` only wraps `DBPool`). Options to weigh at
spec-writing time:
- Add a small lister interface to `ModuleContext` alongside `DBPool`, mirroring how `DBPool` is defined
  narrowly in `pipeline` rather than imported wholesale from `internal/snowflake/pool`.
- Give this module's own constructor a k8s client directly, the way the account module's constructor
  takes `secrets.Backend` and `Config.Snowflake.Org` (010) — keeping the pipeline package itself free of
  any k8s-client-listing concern.

## Decisions from design conversation

Recorded from a direct conversation (no `/yukimi.clarify` run) that resolved how quota should be split
and ordered, before `specs/scope-016-quota.md` had been formally clarified.

- **Quota becomes two ordinary pipeline modules, not one module plus an out-of-band `Admit()`.** The old
  scope note kept `Admit()` outside the `Module` contract specifically because it was called from two
  phases (019's validation phase, and the pipeline). Once admission is itself a pipeline module, that
  reason no longer applies, so it also moves out of `internal/quota/` into
  `internal/account/modules/quotacheck/`, alongside the other Snowflake-facing modules — even though it
  never touches Snowflake itself.
- **The account module (010) is identified by name, not position.** `internal/account/pipeline` (009,
  already implemented) used to hardcode `modules[0]` as the account module for `Observation.Exists`.
  That invariant is being changed (as part of this same conversation) to identify the account module via
  `Name() == pipeline.AccountModuleName`, specifically so this module can be registered ahead of it
  without breaking `Observe`.
- **`design.md` §3.2's flow diagram was updated** to show Credit Quota as an input feeding the create
  flow (alongside Guardrails, Approved Exceptions and Backplane Config) rather than a step chained after
  Custom Auth Rules — matching this module now running before Account Bootstrapping.
- **Only the admission check aborts.** The exhaustion condition (`QuotaAvailable=False` once credits run
  out mid-lifecycle) must never abort the pipeline or affect `Ready` — design.md §3.10 is explicit that
  the account stays intact and only warehouses suspend. That behavior belongs entirely to
  `specs/scope-017-quota-monitor.md`; this module never touches it.

## References

- **Product design**: `specs/design.md` §3.2, §3.10 — the authoritative product requirements this scope
  note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).
- **Sibling scope note**: `specs/scope-017-quota-monitor.md` — the enforcement half this module never
  performs.
