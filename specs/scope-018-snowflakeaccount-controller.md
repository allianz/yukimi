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

## Raised by the 009 clarification

Recorded by `/yukimi.clarify 009`; see `specs/wip-009-account-pipeline.md` for the full reasoning.
009 places more obligations on 018 than on any other spec.

- **Two entry points, called from three methods.** `pipeline.Observe(ctx, mc)` from `Observe`;
  `pipeline.Apply(ctx, mc)` from **both** `Create` and `Update` — the two bodies are identical,
  because `Apply` is idempotent by construction. Nothing is threaded from an `Observe` call into the
  following `Apply` (wip-009 D-001, D-011).
- **018 builds one `*account.Context` per reconcile** and hands the same value to every module: the
  CRD (spec **and** status), the resolved account name (006), the region's `*backplane.Region` entry
  already admitted against `Available` (007), the ops-set namespace labels (006), and a
  `*logger.Logger` with the operation already scoped. Resolve once, pass down — no module re-runs
  the region lookup. Guardrails (008) run earlier, in the validation phase, strictly before pipeline
  execution; the context carries nothing derived from them.
- **Seed the account locator from `status.accountLocator`** when it is set. The context late-binds it;
  the structural module publishes it via `SetLocator` after `CREATE ACCOUNT`, so a fresh account is
  created and fully configured inside one `Create` (wip-009 D-013).
- **`ResourceUpToDate` is generation-based**:
  `exists && cr.Status.ObservedGeneration == cr.Generation && allModulesInSync`. Advance the counter
  with `cr.Status.SetObservedGeneration(cr.Generation)` **only** when `result.AllDone()` — so a spec
  edit re-applies once, while an outstanding `Pending` or an uncorrected `Rejected` keeps re-applying
  and keeps reporting (wip-009 D-009). Verified: `status.observedGeneration` already exists via
  `xpv1.ResourceStatus`'s embedded `ObservedStatus`, and the managed reconciler in
  `crossplane-runtime/v2@v2.0.0` never writes it — so no CRD schema change is needed and the field is
  018's to own. Known cost: one persistently rejected module re-runs every module every poll interval
  (wip-009 P-004, accepted).
- **Registration order is 010 → 011 → 012 → 013 → 015 → 016**, with 010 in `New`'s dedicated
  structural slot: `account.New(accountModule, parameterModule, networkModule, authModule,
  identityModule, quotaModule)` (wip-009 D-003). Modules run sequentially in that order; there is no
  parallelism and no per-module timeout (wip-009 D-004).
- **018 calls `logger.Handle` once per carried error** in the result — the pipeline classifies nothing
  and handles nothing itself (wip-009 D-005, D-019).
- **Modules absent from `Result` must be left alone.** When the structural module returns any
  non-`Done` outcome the run aborts, and unrun modules are absent from `Result.Outcomes` entirely —
  not recorded as skipped or unknown. Leave any condition such a module owns exactly as the previous
  reconcile left it: neither blanked nor set to `Unknown` (wip-009 D-006, D-007). "We did not look" is
  not the same claim as "we looked and it is unknown".
- **Do not blank an existing `status.accountUrl` (or `accountName`/`accountLocator`) on an aborted or
  partially-failed run** — same reasoning as above.
- **Render conditions from the pipeline's aggregate.** 009 owns the `QuotaAvailable` /
  `IdentitySynced` type constants and the static table saying which of them gates `Ready`; 018 renders
  the aggregate onto the resource, plus messages and events (wip-009 D-016).
- **Open question 018 must settle (wip-009 O-001):** the managed reconciler marks `xpv1.Creating()`
  and `ReconcileSuccess()` and requeues *after* `Create` returns, so whatever 009 aggregated during
  that call is overwritten until the next `Observe`. Harmless if 018 re-aggregates on every `Observe`,
  but the spec must state it deliberately rather than leave it to be discovered. Related verified
  behaviour: on the up-to-date path the reconciler sets `ReconcileSuccess()` *after* `Observe` returns
  (`pkg/reconciler/managed/reconciler.go:1428`), which is why `Synced` cannot be used to carry a
  drift or status message from `Observe`.
- **Drift is not detected or repaired** anywhere in this pipeline until Snowflake ships Organization
  Policies (wip-009 D-010, P-001). 018 should not add a repair path of its own.
