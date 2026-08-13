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
  Depends on: 001, 004, 005, 006.
- Scope:
  - The **module interface**: an `Observe` half for drift detection and an `Apply` half, plus the
    shared context passed to each module (the CRD, the resolved name, the region entry, the
    connections).
  - **Four outcomes, not two**: `Done` | `Pending(reason)` | `Rejected(userErr)` |
    `Failed(systemErr, abort)`. The design forces this: 4.3 requires an outstanding identity sync to
    be *expected* rather than a failure, so it must not abort the pipeline, and 3.8/3.9 require a
    rejected rule to leave the account on its baseline, be reported on `Synced`, and **not** prevent
    later modules from running.
  - Ordered execution with per-module status recorded on the resource.
  - **Non-uniform condition aggregation**, which must be spelled out explicitly:
    `IdentitySynced=False` forces `Ready=False` (4.3 — nobody can administer the account until the
    `ACCOUNTADMIN` group is imported), whereas `QuotaAvailable=False` leaves `Ready=True` (3.10 —
    the account is fully intact and warehouses are merely suspended).
  - The custom condition types themselves (`QuotaAvailable`, `IdentitySynced`) and the mapping from
    module outcomes onto `Ready` and `Synced`.
  - **`Observe` semantics are the most under-specified area of design.md** — it never says what to
    read back (`SHOW PARAMETERS`? `SHOW NETWORK RULES`?). This spec must define the contract; each
    module spec then defines its own drift check within that contract.
- **Hard rule**: `internal/account` must never import `internal/account/modules/…`. There is no
  `DefaultModules()` or `NewPipeline()` convenience constructor — registration lives in 018, and
  pipeline tests use fake modules.

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
