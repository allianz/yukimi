> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `015`'s intended
> *scope*, not its content. When writing `015-identity-module.md`, the sole sources of truth
> are `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or
> discard anything below freely. Please keep this file up to date until
> `015-identity-module.md` has been written, then delete it.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003.a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003.a` < `003.b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Package: `internal/account/modules/identity/`. Covers design 3.7 and the 4.3 waiting rules.
  Depends on: 005, 007, 009, 014.
- Scope:
  - `ALTER ACCOUNT ADD ORGANIZATION USER GROUP '<group>'` per group under
    `identityIntegration.groups`, then `GRANT ROLE <system-role> TO ROLE "<group>"` per `roleBindings`
    entry. This is what lets users log in via SSO with their existing company roles carried over.
  - `ACCOUNTADMIN` must be bound, so that the account is manageable after creation.
  - **Incremental import**: as each `IdentitySyncRequest` reports `Ready=True`, that provider's
    groups are imported and every `roleBindings` entry whose group is now present is granted. A slow
    provider must not hold back a fast one.
  - Returns `Pending` — never `Failed` — while requests are outstanding. These statements only
    succeed once the groups exist in the org account, so the account waits rather than failing.
  - Aggregates the `IdentitySynced` condition: `True` once every request is fulfilled and every group
    imported. Per 009, `IdentitySynced=False` holds `Ready=False`, carrying either `SyncPending` or
    `SyncTimeout` to distinguish a benign wait from a provisioning failure.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).

## Raised by the 009 clarification

Recorded by `/yukimi.clarify 009`. `specs/009-account-pipeline.md` now carries the pipeline-wide rules
these points rest on.

- **015 owns emission of `IdentitySyncRequest`**, closing the question of who calls 014's emitter — no
  spec claimed it before. On each `Apply`: emit the request if one is not already outstanding, stamp
  its start time, then import whatever groups are already `Ready=True`. Emission and import live in one
  module because they share the same `Pending`/timeout accounting.
- **015 may add its own explicit `status` field.** 009 adds no CRD field, but that binds the pipeline,
  not the modules: an `identitySyncStartedAt` (or equivalently named) field on `SnowflakeAccountStatus`
  is expected, since design 4.3's timeout cannot be computed without a start timestamp and there is
  nowhere else to keep it. Name it explicitly; do not introduce a generic per-module state blob.
- **Return `Pending(reason)` while a sync is outstanding, never `Failed`.** An outstanding sync is
  expected, not a failure (design 4.3), and `Pending` exists precisely for it. `Pending` carries no
  requeue hint — the controller's poll interval governs timing, which is fine because the sync horizon
  is tens of minutes.
- **`IdentitySynced=False` forces `Ready=False`.** 009 owns the condition type constant
  (`account.TypeIdentitySynced`) and the static `Ready`-gating table; 015 attaches the condition to its
  outcome and the pipeline applies the table. Do not restate the gating rule here and do not decide it
  per module.
- **Re-application is driven by generation, so `Pending` self-retries.** 018 advances
  `status.observedGeneration` only after a run in which every module returned `Done`, so an outstanding
  sync keeps the resource out-of-date and keeps `Apply` running until the sync lands or times out. 015
  needs no retry loop of its own.
- Known consequence: nothing is emitted while 010 keeps failing, because 015 runs after it, and 010
  aborts the run on any non-`Done` outcome.
- **Identity bindings are never pruned.** Only 012 and 013 prune, each by its own object-name prefix;
  015's group imports and role bindings are re-asserted but never enumerated or dropped.

## Raised by the 018 clarification

Recorded by `/yukimi.clarify 018`. The SnowflakeAccount controller (018) was implemented ahead of
015: it wires `account.New(accountModule)` — a one-module pipeline containing only 010 — because 015
does not exist yet. 018's `Ready` condition today is derived purely from 010's outcome
(`Ready = Available()` iff the pipeline's `Observation.InSync` is true), with no `IdentitySynced`
condition set to any value — true, false, or `Unknown` — since no registered module produces one.

- **015's registration point is additive**, e.g.
  `account.New(accountModule, parameterModule, networkModule, authModule, identityModule)`. No
  change to 018's `Observe`/`Create`/`Update` bodies is needed for the registration itself.
- **015 is the first module 018 registers that sets `Outcome.Condition`.** Once it lands, 018's
  `Observe` needs the per-module condition-rendering loop described in 009's Appendix Example 2
  (walk `Result.Outcomes`, call `cr.SetConditions(*mo.Outcome.Condition)` for any non-nil condition,
  and apply 009's `GatesReady` table to compute `Ready`) — this loop does not exist in 018 yet because
  nothing before 015 needs it. Adding 015 is therefore the point at which 018 must add that loop, not
  a later, separate change.
- **Today, `status.identitySyncStartedAt` (or whatever 015 names it) does not exist**, since nothing
  writes it yet — 018 carries no logic related to identity sync timing at all.
