> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `009`'s intended
> *scope*, not its content. When writing `009-account-pipeline.md`, the sole sources of truth are
> `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or discard
> anything below freely. This file does not need to be kept up to date, and should be deleted
> once `009-account-pipeline.md` has been written.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003-a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003-a` < `003-b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Package: `internal/account/`. Covers the design 3.2 create flow and the 7.1 aggregation.
  Depends on: 001, 004, 006, 007, 008.
- Scope:
  - The **module contract**: an `Observe` half for drift detection and an `Apply` half. There is
    no teardown half — see the deletion bullet below.
  - The **shared context** handed to each module: the CRD, the resolved name (006), the region's
    backplane entry (007), the merged guardrail result (008), the ops-set namespace labels (006's
    `labels.go`), and the connection handles (004).
  - **Resolve once, pass down.** Guardrails and the region entry are evaluated once, by 018's
    validation phase, and handed to every module through the context. No module re-runs the
    guardrail merge: 012 resolves the `"full"` rule and 013 validates auth exceptions against the
    same verdict, and two modules must never be able to disagree about one CRD.
  - **Modules execute their own SQL; the pipeline executes none and imports 005 nowhere.** 3.11's
    privilege step-down keeps org-admin to `CREATE ACCOUNT`/`DROP ACCOUNT` (010 alone) while every
    other module runs as the account's own `platform` user, so there is no single connection an
    aggregated batch could run on. 010 must also read back the account locator `CREATE ACCOUNT`
    returns, and 015 and 016 do work that is not SQL at all.
  - **Four outcomes, not two**: `Done` | `Pending(reason)` | `Rejected(userErr)` |
    `Failed(systemErr)`. The design forces this: 4.3 requires an outstanding identity sync to be
    *expected* rather than a failure, so it must not abort the pipeline, and 3.8/3.9 require a
    rejected rule to leave the account on its baseline, be reported on `Synced`, and **not**
    prevent later modules from running.
  - **No teardown half.** 6.3 Phase 3 is a single `DROP ACCOUNT` over the org-admin connection
    plus finalizer release, owned by 017 and 018, and it cascades to every object inside the
    account — per-module teardown would be code no requirement exercises. The one artifact living
    outside the account is the RSA keypair in 003's store; disposing of it is 017/018's job, not a
    module's.
  - Ordered execution, with the single structural dependency stated: nothing can run before the
    account exists, so a failure of 010 is the one case that stops the run.
  - **Non-uniform condition aggregation**, which must be spelled out explicitly:
    `IdentitySynced=False` forces `Ready=False` (4.3 — nobody can administer the account until the
    `ACCOUNTADMIN` group is imported), whereas `QuotaAvailable=False` leaves `Ready=True` (3.10 —
    the account is fully intact and warehouses are merely suspended).
  - The custom condition type constants (`QuotaAvailable`, `IdentitySynced`) and the mapping from
    module outcomes onto `Ready` and `Synced`. The `status` **schema** stays 006's; 009 adds no CRD
    field.
  - **Per-module results are returned to the caller, not written to the resource.** 7.2's status is
    `accountName`, `accountUrl` and `conditions`; rendering outcomes into conditions, messages and
    events is 018's.
  - 009 returns aggregated outcomes and conditions; **018** maps them onto
    `managed.ExternalObservation`.
  - **`Observe` semantics are the most under-specified area of design.md** — it never says what to
    read back (`SHOW PARAMETERS`? `SHOW NETWORK RULES`?). Design gives one concrete instruction
    (011: read the parameters back and re-apply any that diverged) and one pattern (5.4's
    auto-repair: remove and recreate wholesale). This spec must define the contract; each module
    spec then defines its own drift check within it.
- **Hard rules**:
  - `internal/account` must never import an implementor — neither `internal/account/modules/…` nor
    `internal/quota` (016). There is no `DefaultModules()` or `NewPipeline()` convenience
    constructor: registration lives in 018, and pipeline tests use fake modules.
  - The contract must be implementable from **outside** `modules/`: 016 lives in `internal/quota/`
    and participates in the pipeline. Its `Admit()` is a validation-phase entry point outside the
    contract, called separately by 018.
  - Modules never call one another, and hold no state between reconciles. Single-threaded
    reconciliation per resource is the only concurrency guarantee.
- **Out of scope**: the module implementations (010–013, 015) and quota (016); registration and
  ordering (018); condition and event *reporting* (018); the validation phase — guardrails and
  approved exceptions (008), the region `available` gate (007), quota admission (016), immutability
  CEL (006); deletion (017, 018); SQL mechanics and idempotency helpers (005); the CRD schema (006).
- **Open questions for this spec**: the outcome type's exact shape, and whether `Failed` carries a
  per-module abort flag or the pipeline treats 010 as the single structural abort; whether every
  module needs an `Observe` or a cheap idempotent `Apply` makes it optional; whether `Pending`
  carries a requeue hint or 018 owns requeue timing; and **who calls 014's emitter** — 4.3 requires
  emission on first observation alongside bootstrapping, but 015 covers import only and 018 wires
  modules rather than adding steps of its own, so no spec claims emission today.

## Cross-cutting context from the roadmap

- **Decision — no separate conditions/events spec.** crossplane-runtime already supplies
  `xpv1.Available()` / `ReconcileSuccess()`, injects an `event.Recorder`, and its managed reconciler
  sets `Ready` and `Synced` itself. Every custom condition (`QuotaAvailable` from 3.10, `IdentitySynced`
  from 4.3) and every custom event (`DeletionBlocked` from 6.3, `QuotaExhausted` from 3.10, `SyncTimeout`
  from 4.3) is SnowflakeAccount-specific. Aggregation rules therefore live here in spec 009 and
  reporting in spec 018.
- **Why 009 sits below the modules (010–013, 015), and `internal/account` must never import
  `internal/account/modules/…`.** Module registration and ordering live in 018. Pipeline tests use
  fake modules. This is the one place in the tree where an import cycle is easy to introduce.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).
