> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `020`'s intended
> *scope*, not its content. When writing `020-snowflakeaccount-controller.md`, the sole
> sources of truth are `specs/design.md` and the prompt given at spec-writing time — rework,
> restructure, or discard anything below freely. Please keep this file up to date until
> `020-snowflakeaccount-controller.md` has been written, then delete it.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003.a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003.a` < `003.b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Package: `internal/controller/snowflakeaccount/`. Covers design 3.2, the 3.3–3.4 enforcement
  points, 6.3 Phases 2–3, and chapter 7. Depends on: 002, 005–019.
- **Thin orchestration only** — no business logic of its own. Every Snowflake interaction lives in a
  module (012–015, 017–018), and validation lives in 007 (the region's `available` gate, checked here
  directly) and 008 (guardrails, checked via the guardrail-check module inside the pipeline — not a
  separate controller step; see below).
- Scope:
  - **Module registration and ordering.** This is where the concrete modules are wired, which is what
    keeps `internal/account/pipeline` free of any import of `modules/`. The order is
    `account.New(guardrailCheckModule, quotaCheckModule, accountModule, parameterModule, networkModule,
    authModule, identityModule, quotaMonitorModule)` — i.e. guardrail-check (010) → quota-check (011) →
    account bootstrapping (012) → parameters (013) → network (014) → auth (015) → identity (017) →
    quota-monitor (018). Guardrail-check runs first because it needs no Snowflake connection and should
    reject structurally invalid input before anything else does any work; quota-check runs second, for
    the same no-connection reason, aborting before `CREATE ACCOUNT` when the claimed `creditQuota`
    doesn't fit; quota-monitor stays last because it needs `TenantDB`, same as the old single-module
    quota plan. Note that 3.2's diagram shows identity before network; identity requests are emitted
    early and are non-blocking, so the import step's position is flexible while request emission
    happens alongside bootstrapping per 4.3.
  - Not here: the secret backend. It is constructed in `cmd/provider/main.go` from 002's
    `secretsBackend` before any controller is set up, and injected. This spec wires
    modules, not infrastructure.
  - **The validation phase**, in order: the region's `available` gate (007) → immutability, which is
    mostly enforced by CEL in 006. Neither guardrail admission nor quota admission is a validation-phase
    step anymore — both run inside the pipeline, as guardrail-check (010)'s `Apply` and quota-check
    (011)'s `Apply` respectively, ahead of every other module. Approved exceptions (008) are still
    consulted on a guardrail rejection, but as part of guardrail-check's own evaluation call, not a
    separate controller step.
  - Pipeline execution via 009, then reporting: the aggregated `Ready` and `Synced`, the custom
    `QuotaAvailable` and `IdentitySynced` conditions, `status.accountName` and `status.accountUrl`,
    and the warning events (`QuotaExhausted`, `SyncTimeout`, `DeletionBlocked`) via
    crossplane-runtime's injected `event.Recorder`.
  - **The deletion gate (6.3 Phases 2–3)**: on `deletionTimestamp`, query for an Active request in
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
  spec 020.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).

## Raised by the 009 clarification

Recorded by `/yukimi.clarify 009`. `specs/009-account-pipeline.md` is now written and authoritative —
its "Integration Points" section states 020's obligations directly, and its Appendix examples 1 and 2
sketch the `Observe` and `Create`/`Update` bodies. 009 places more obligations on 020 than on any other
spec.

- **Two entry points, called from three methods.** `pipeline.Observe(ctx, mc)` from `Observe`;
  `pipeline.Apply(ctx, mc)` from **both** `Create` and `Update` — the two bodies are identical,
  because `Apply` is idempotent by construction. Nothing is threaded from an `Observe` call into the
  following `Apply`; `ModuleContext` is rebuilt per call and the two entry points share no state.
- **020 builds one `*account.Context` per reconcile** and hands the same value to every module: the
  CRD (spec **and** status), the resolved account name (006), the region's `*backplane.Region` entry
  already admitted against `Available` (007), the ops-set namespace labels (006), and a
  `*logger.Logger` with the operation already scoped. Resolve once, pass down — no module re-runs
  the region lookup. Guardrails (008) now run *inside* the pipeline, as guardrail-check (010)'s
  `Apply`, not before it; the context still carries nothing guardrail-specific — that module reads the
  CRD and namespace labels already on `ModuleContext` directly, plus the loaded 008 config/exceptions
  instance injected into its own constructor.
- **The account locator lives on `status.accountLocator` directly** — `ModuleContext` carries no
  separate accessor for it; every module, including 012, reads and writes it straight through `CR()`.
  012 no longer configures a fresh account fully inside one `Create`: it sets
  `status.accountLocator`/`status.accountCreatedAt` and aborts the pipeline for that pass, deferring
  verification and every later module to a reconcile once the post-create grace period has elapsed
  (specs/012-account-module.md, Key Concept: Post-Create Grace Period).
- **`ResourceUpToDate` is generation-based**:
  `exists && cr.Status.ObservedGeneration == cr.Generation && allModulesInSync`. Advance the counter
  with `cr.Status.SetObservedGeneration(cr.Generation)` **only** when `result.AllDone()` — so a spec
  edit re-applies once, while an outstanding `Pending` or an uncorrected `Rejected` keeps re-applying
  and keeps reporting. Verified: `status.observedGeneration` already exists via `xpv1.ResourceStatus`'s
  embedded `ObservedStatus`, and the managed reconciler in `crossplane-runtime/v2@v2.0.0` never writes
  it — so no CRD schema change is needed and the field is 020's to own. Known cost, accepted: one
  persistently rejected module re-runs every module every poll interval — a handful of idempotent
  statements plus one enumeration query per pruning module, so cheap-but-unbounded rather than solved.
- **Registration order is 010 → 011 → 012 → 013 → 014 → 015 → 017 → 018**, with guardrail-check (010)
  first and quota-check (011) second — neither is the account module (012) — in `New`'s ordered module
  list: `account.New(guardrailCheckModule, quotaCheckModule, accountModule, parameterModule,
  networkModule, authModule, identityModule, quotaMonitorModule)`.
  `internal/account/pipeline` no longer assumes the first-registered module is the account module; it
  identifies it by `Name() == pipeline.AccountModuleName` instead (see 009). Modules run sequentially in
  registration order; there is no parallelism and no per-module timeout.
- **020 calls `logger.Handle` once per carried error** in the result — the pipeline classifies nothing
  and handles nothing itself.
- **Modules absent from `Result` must be left alone.** When a module's outcome sets `Abort` (in
  practice, only the admission modules — guardrail-check (010), quota-check (011) — and the account
  module (012) do) the run stops, and unrun modules are absent from `Result.Outcomes`
  entirely — not recorded as skipped or unknown. Leave any condition such a module owns exactly as the
  previous reconcile left it: neither blanked nor set to `Unknown`. "We did not look" is not the same
  claim as "we looked and it is unknown".
- **Do not blank an existing `status.accountUrl` (or `accountName`/`accountLocator`) on an aborted or
  partially-failed run** — same reasoning as above.
- **Render conditions from the pipeline's aggregate.** 009 owns the `QuotaAvailable` /
  `IdentitySynced` type constants and the static table saying which of them gates `Ready`; 020 renders
  the aggregate onto the resource, plus messages and events.
- **Open question 020 must settle**, left unresolved by the 009 clarification: the managed reconciler
  marks `xpv1.Creating()`
  and `ReconcileSuccess()` and requeues *after* `Create` returns, so whatever 009 aggregated during
  that call is overwritten until the next `Observe`. Harmless if 020 re-aggregates on every `Observe`,
  but the spec must state it deliberately rather than leave it to be discovered. Related verified
  behaviour: on the up-to-date path the reconciler sets `ReconcileSuccess()` *after* `Observe` returns
  (`pkg/reconciler/managed/reconciler.go:1428`), which is why `Synced` cannot be used to carry a
  drift or status message from `Observe`.
- **Drift is not detected or repaired** anywhere in this pipeline until Snowflake ships Organization
  Policies (design.md Appendix B). 020 should not add a repair path of its own.

## Raised by the 012 clarification

Recorded by `/yukimi.clarify 010` (spec later renumbered to 012). `specs/012-account-module.md` is now
written and authoritative for what the module does and does not carry.

- **020 computes `status.accountName`/`accountUrl` itself, directly from the `ModuleContext` it already
  built and holds** — `mc.ResolvedAccountName()` and `tenant.AccountURL(locator, region, usePrivateLink)`
  with `usePrivateLink` from `Config.Snowflake.UsePrivateLink` (002) — never from a payload on any
  module's `Outcome`. 012's `Outcome` carries no string result; there is nothing to read off it for this
  purpose. `status.accountLocator` itself needs no separate computation: 012 already sets it directly on
  `cr.Status` (via `ModuleContext.CR()`), so 020 only has to persist the CRD it's already holding.
- **Persist `status.accountLocator` as promptly as possible after `Apply` returns.** Every reconcile
  between a successful `CREATE ACCOUNT` and that persist is a crash window, and 012 has no way to
  recover from it automatically: a crash there permanently strands the resource in `Failed(systemErr)`
  until an operator manually reconciles `status.accountLocator` or the underlying Snowflake account.
  Minimizing how long that window stays open is 020's responsibility, not 012's.

## Raised by the guardrail-check design conversation

Recorded from a direct conversation (no `/yukimi.clarify` run) that gave guardrail admission the same
pipeline-module treatment quota admission already has — see `specs/scope-010-guardrail-check.md` for
the module's own scope, and `specs/scope-008-guardrails.md`'s "Relationship to the account pipeline"
section for why 008 itself is unaffected.

- **This controller no longer runs a separate guardrail gate before the pipeline.** Every earlier
  version of this file (and of `specs/009-account-pipeline.md`) described guardrails as running "in
  full, once, inside 020's validation phase, strictly before the account pipeline is ever invoked."
  That framing is gone: 020 now calls `Pipeline.Observe`/`Apply` uniformly, and guardrail rejection
  surfaces exactly like a quota-check rejection already does — as an aborted, `Rejected` pipeline run.
- **What's left of "the validation phase"** is narrower than it used to be: only the backplane
  region's `available` gate (007) and the CEL-enforced immutability checks (006) — both structural,
  both cheap, both still meaningfully separate from anything a pipeline module could do, since neither
  needs the CRD to already be guardrail-clean.

## Raised by the 019 clarification

Recorded by `/yukimi.clarify 019`. This finalizes the exact API for the deletion gate this file
already describes in prose (Phase 2/3), so 020 has a concrete contract to call rather than needing to
work one out itself.

- **The lookup and consume calls are**:
  ```go
  func FindActiveRequest(ctx context.Context, c client.Client, namespace, targetKind, targetName string) (*v1alpha1.SnowflakeDeletionRequest, error)
  func MarkConsumed(ctx context.Context, c client.Client, req *v1alpha1.SnowflakeDeletionRequest) error
  ```
  Both live in `internal/deletion` and take a `sigs.k8s.io/controller-runtime` `client.Client`
  directly (the manager's client is available via `mgr.GetClient()` in 020's own setup). 020 should
  call these rather than hand-rolling its own `client.List`/`client.Status().Update` against
  `SnowflakeDeletionRequest` — that logic, including how ties are broken, belongs to 019's package.
- **020 does not need to re-check `validUntil` itself.** `FindActiveRequest` trusts the target's
  persisted `status.state` field as authoritative and returns only genuinely `Active` candidates;
  it performs no separate live-time comparison, and 020 doesn't need to add one either. The
  accepted staleness bound is roughly one reconciler poll interval (~1 minute at the default
  `--poll` setting), which is small against the 8-hour maximum window a request can ever authorize.
- **020 does not need to handle multiple concurrently-Active requests targeting the same
  resource itself.** Nothing prevents two `Active` requests from targeting the same
  `SnowflakeAccount` at once (no admission-webhook infrastructure exists in this platform to enforce
  cross-object uniqueness). `FindActiveRequest` already resolves this deterministically (earliest
  `creationTimestamp` wins) before returning, so 020's Phase 2/3 logic can treat the result as a
  single candidate or `nil`, nothing more.
- **`SnowflakeDeletionRequest` has its own controller and finalizer-free lifecycle**, registered
  separately from 020's own controller registration. 020 only ever reads (`FindActiveRequest`) and
  writes (`MarkConsumed`) it as a sibling object — it does not own or drive that CRD's own
  `Observe`/`Create`/`Update`/`Delete`.
