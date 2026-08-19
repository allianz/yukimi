> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `018`'s intended
> *scope*, not its content. When writing `018-snowflakeaccount-controller.md`, the sole
> sources of truth are `specs/design.md` and the prompt given at spec-writing time — rework,
> restructure, or discard anything below freely. Please keep this file up to date until
> `018-snowflakeaccount-controller.md` has been written, then delete it.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003.a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003.a` < `003.b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Package: `internal/controller/snowflakeaccount/`. Covers design 3.2, the 3.3–3.4 enforcement
  points, 6.3 Phases 2–3, and chapter 7. Depends on: 002, 005–017.
- **Thin orchestration only** — no business logic of its own. Every Snowflake interaction lives in a
  module (010–013, 015) or in quota (016), and validation lives in 007 and 008.
- Scope:
  - **Module registration and ordering.** This is where the concrete modules are wired, which is what
    keeps `internal/account` free of any import of `modules/`. The order follows the 3.2 create flow:
    account bootstrapping (010) → parameters (011) → network (012) → auth (013) → identity (015) →
    quota (016). Note that 3.2's diagram shows identity before network; identity requests are emitted
    early and are non-blocking, so the import step's position is flexible while request emission
    happens alongside bootstrapping per 4.3.
  - Not here: the secret backend. It is constructed in `cmd/provider/main.go` from 002's
    `secretsBackend` before any controller is set up, and injected. This spec wires
    modules, not infrastructure.
  - **The validation phase**, in order: guardrails (008) → approved exceptions on rejection (008) →
    the region's `available` gate (007) → quota admission (016) → immutability, which is mostly
    enforced by CEL in 006.
  - Pipeline execution via 009, then reporting: the aggregated `Ready` and `Synced`, the custom
    `QuotaAvailable` and `IdentitySynced` conditions, `status.accountName` and `status.accountUrl`,
    and the warning events (`QuotaExhausted`, `SyncTimeout`, `DeletionBlocked`) via
    crossplane-runtime's injected `event.Recorder`.
  - **The deletion gate (6.3 Phases 2–3)**: on `deletionTimestamp`, query for an Active warrant in
    the same namespace targeting this resource. If one is found, run `DROP ACCOUNT` over the
    org-admin connection, release the finalizer, and mark the request `Consumed`. If it is absent or
    expired, refuse: stall in `Terminating`, emit `Warning: DeletionBlocked`, and set `Ready=False`
    so that ArgoCD reports failure, forcing the user either to restore the file or to create a valid
    request.
  - Error handling per CLAUDE.md: in `Observe`, call `log.Handle(err)`, set
    `xpv1.Unavailable().WithMessage(...)`, and return nil to avoid a retry flood; in `Create`,
    `Update` and `Delete`, return the handled error and let the framework set conditions.

## Cross-cutting context from the roadmap

- **Decision — no separate conditions/events spec.** crossplane-runtime already supplies
  `xpv1.Available()` / `ReconcileSuccess()`, injects an `event.Recorder`, and its managed reconciler
  sets `Ready` and `Synced` itself. Every custom condition (`QuotaAvailable` from 3.10, `IdentitySynced`
  from 4.3) and every custom event (`DeletionBlocked` from 6.3, `QuotaExhausted` from 3.10, `SyncTimeout`
  from 4.3) is SnowflakeAccount-specific. Aggregation rules live in spec 009; reporting lives here in
  spec 018.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).
