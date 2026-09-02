> **Clarification record — not a specification.** Produced by `/yukimi.clarify 018` to settle what
> `specs/design.md` intentionally leaves out and `specs/scope-018-snowflakeaccount-controller.md`
> does not cover. It records decisions, not product design — `specs/design.md` remains authoritative
> and always wins, and once `018-snowflakeaccount-controller.md` is written the spec wins over this
> file too.
> Read it together with the scope note when writing `018-snowflakeaccount-controller.md` (delete the
> scope note then), keep it as supporting detail while `018` is implemented, and delete it once the
> code has landed.

## Clarification runs

- Run 1 — covered: whether 018 can be written/implemented at all given 008 and 011–017 are still
  unwritten scope notes; `Delete` behavior with no deletion-warrant system (017); the validation
  phase with guardrails (008) and quota admission (016) absent; `Ready`/condition semantics with
  only one pipeline module registered; the exact `DROP ACCOUNT` statement shape. Left open:
  none material — see Problem Areas for lower-priority items deferred to whichever spec lands next.

## Resolved Decisions

### D-001 — 018 is scoped to what is actually implemented today, not to design.md's full picture

**Question**: `scope-018-snowflakeaccount-controller.md` declares `Depends on: 002, 005–017`, but
only 001–007, 009, and 010 are written specs with code. 008 (guardrails), 011/012/013/015
(parameter/network/auth/identity modules), 016 (quota), and 017 (deletion warrants) exist only as
scope notes — no `internal/guardrails/`, `internal/quota/`, `internal/identitysync/`, or
`internal/account/modules/{parameter,network,auth,identity}/` packages exist. Per CLAUDE.md's
ordering rule ("a spec may depend only on specs numbered strictly below it"), 018 as originally
scoped cannot honestly be written yet. Can it be written now anyway, and if so, on what terms?

**Decision**: Write 018 now, but scoped strictly to what exists: `internal/errors`/`internal/logger`
(001), `internal/config` (002), `internal/snowflake/pool`+`host` (004), `internal/snowflake/statement`
(005), the `SnowflakeAccount` CRD and `internal/tenant` (006), `internal/backplane` (007), the
account pipeline (009), and the account module (010). The controller wires `account.New(accountModule)`
— a one-module pipeline — runs no guardrail or quota-admission validation phase, and implements
`Delete` without any warrant lookup (see D-002). Every capability this omits is recorded as a
Forward Contract (below) and propagated to the relevant scope note (see Propagation) so the spec
that eventually adds it knows exactly what in 018 it must change.

**Rationale**: This is workable without contradiction because 009's `Pipeline`/`Module` interface
is already fully generic — `internal/account/pipeline.go`'s `Observe`/`Apply` iterate over whatever
`[]Module` they're constructed with, and 011/012/013/015/016 were each speced (in their own scope
notes, "Raised by the 009 clarification") to implement a no-op `Observe` returning `Done()` — i.e.
the design already anticipated a module being absent from a given pipeline instance. Registering
just `[accountModule]` today is the same shape a five-module pipeline would degrade to if those
five modules' `Observe`/`Apply` were all no-ops; nothing about 009's contract changes. Alternative
considered — describing the full end-state per design.md and marking the rest "not yet wired" inside
the same spec text — was rejected because it would make 018 depend in its own written text on five
specs that don't exist, formally violating the ordering rule and describing modules/APIs
(`account.New(accountModule, parameterModule, ...)`) that would not compile against today's
codebase.

**Affects spec section**: Scope (narrows "Depends on"), Key Concept (new: "Reduced Module Set"),
Project Structure, Integration Points, Dependencies.

### D-002 — `Delete` runs an unconditional, idempotent `DROP ACCOUNT`, no warrant gating

**Question**: design.md §6.3's Positive Control model requires querying for an Active
`SnowflakeDeletionRequest` warrant before dropping an account, and refusing (stalling in
`Terminating`, emitting `Warning: DeletionBlocked`) otherwise. That model needs spec 017
(`SnowflakeDeletionRequest` CRD, `internal/deletion/`), which does not exist. What does 018's
`Delete` do in the meantime?

**Decision**: `Delete` always executes
`DROP ACCOUNT IF EXISTS <resolvedName> GRACE_PERIOD_IN_DAYS = 3` over the org-admin connection
(`tenant.ResolveName` for `<resolvedName>`, the same name 010 used to create it), then lets the
finalizer release. No warrant lookup, no blocking path, no `DeletionBlocked` event. On a SQL error,
`log.Handle(err)` and return the error per CLAUDE.md's Create/Update/Delete pattern — the framework
sets the failure condition; 018 does not set one itself. The statement always runs regardless of
whether `status.accountLocator` was ever populated (i.e. even if `Create` never succeeded) —
`IF EXISTS` makes that safe.

**Rationale**: The user explicitly chose this over both a fail-closed stub (always refuse — closest
to design.md's intent, but the user rejected it) and a silent-orphan release (finalizer released
without dropping). `IF EXISTS` plus always-attempt make the statement idempotent and crash-safe
without needing to special-case "was the account ever created" — a retry after a crash mid-`Delete`
just re-issues the same statement and gets a no-op. `GRACE_PERIOD_IN_DAYS = 3` is not a policy choice
this spec is making: it is Snowflake's enforced minimum (verified below), so it is simply the fastest
legal deletion, not a deliberately soft one. The positive-control/warrant gate becomes spec 017's job
entirely — see Forward Contract to 017.

**Verified** (docs.snowflake.com/en/sql-reference/sql/drop-account, fetched during this
clarification): syntax is `DROP ACCOUNT [ IF EXISTS ] <name> GRACE_PERIOD_IN_DAYS = <integer>`;
`GRACE_PERIOD_IN_DAYS` is a **required** clause with no documented default (omitting it is a syntax
error) and a valid range of 3–90 days; only an organization administrator can execute it; an org
admin cannot drop the account they are currently connected to (irrelevant here — the org-admin
connection is never the tenant account being dropped, per spec 004's connection-scope split).
Accounts holding retention-locked, unexpired snapshots cannot be dropped until those expire, even
for a privileged role — 018 does not special-case this; it surfaces as an ordinary `DROP ACCOUNT`
failure, handled like any other statement error.

**Affects spec section**: Key Concept (new: "Unconditional Delete, Pending Positive Control"),
Public API (the `Delete` method), Edge Cases, Error Classification.

### D-003 — Validation phase skips guardrails (008) and quota admission (016); the region gate (007) stays

**Question**: `scope-018`'s roadmap note describes a validation phase run before the pipeline:
guardrails (008) → approved exceptions (008) → backplane region's `available` gate (007) → quota
admission (016) → immutability (mostly CEL, 006). 008 and 016 don't exist. What does 018's
validation phase do today?

**Decision**: 018's validation phase is just the backplane region lookup and `available` check
(007, already implemented) plus whatever CEL/API-server validation the CRD (006) already enforces.
No guardrail check, no approved-exceptions lookup, no quota-admission call happen anywhere in 018.
A CRD that would later fail 008's constraints, or that would exceed a namespace's credit ceiling per
016, is accepted and proceeds straight to the pipeline today.

**Rationale**: User's explicit choice, over adding a throwaway placeholder check (rejected — it
would duplicate logic 008 owns and blur CLAUDE.md's thin-controller boundary for no lasting benefit).
This is a real, accepted gap, not an oversight — see Forward Contract to 008 and 016.

**Affects spec section**: Scope (narrows the validation phase), Key Concept, Edge Cases.

### D-004 — `Ready` is driven solely by the one-module pipeline's outcome; no synthetic conditions

**Question**: 009 defines `TypeIdentitySynced`/`TypeQuotaAvailable` and a `GatesReady` table for
conditions that modules 015/016 would set. With neither module registered, what does `Ready` mean,
and should 018 fake a placeholder condition to signal "not fully provisioned" more honestly?

**Decision**: `Ready` becomes true purely from the account module's (010) outcome via the pipeline:
on `Observe`, `pipeline.Observe(ctx, mc)` returns `Observation{Exists, InSync}`; `Ready = Available()`
iff `Exists && InSync`, else `Unavailable()`. On `Create`/`Update`, `pipeline.Apply(ctx, mc)` runs;
`ObservedGeneration` advances only if `Result.AllDone()` (D-006). No `IdentitySynced`/`QuotaAvailable`
condition is set to any value — true, false, or `Unknown` — until a module that owns one is actually
registered.

**Rationale**: User's explicit choice, consistent with 009's own rule (recorded in scope-018's
"Raised by the 009 clarification" section) that a module absent from `Result.Outcomes` must be left
alone, not set to a synthetic value — "we did not look" is not the same claim as "we looked and it
is unknown." Faking a condition would also need manual removal later and has no mechanical trigger
telling anyone to remove it.

**Affects spec section**: Key Concept, Schema Specification (status/conditions), Edge Cases.

### D-005 — `Observe` re-derives `Ready` every call; it does not carry over `Create`/`Update`'s aggregation

**Question**: 009's clarification (recorded in scope-018) flagged an open question 018 must settle:
the managed reconciler sets `xpv1.Creating()` and `ReconcileSuccess()` and requeues *after* `Create`
returns, overwriting whatever conditions 018 set during that call, until the next `Observe`. Left
unresolved for 018 to state deliberately.

**Decision**: `Observe` always recomputes `Ready` from a fresh `pipeline.Observe` call — it never
assumes the previous `Create`/`Update` call's result is still valid by the time `Observe` next runs.
With only module 010 registered and no module producing a condition of its own (D-004), this reduces
to the single `Ready = Available() iff Observation.InSync` rule in D-004; there is no per-module
condition-rendering loop to run yet. That loop (per 009's Appendix Example 2, walking
`Result.Outcomes` and calling `cr.SetConditions(*mo.Outcome.Condition)`) only becomes live once a
module that sets `Outcome.Condition` (015 or 016) is registered — at that point `Observe` still needs
a way to re-derive those conditions without re-running `Apply`, which is `pipeline.Observe`'s job to
support and is out of scope for this clarification (010's `Observe` already does read-back
verification without mutating anything).

**Rationale**: Answers 009's explicit open question rather than leaving it to be discovered; the
one-module case makes the answer nearly free to implement now, and the rule ("Observe always wins,
never trust Create/Update's leftover aggregation") generalizes cleanly to when more modules land.

**Affects spec section**: Integration Points, Key Concept.

### D-006 — `ResourceUpToDate` and `ObservedGeneration` follow 009's contract unchanged

**Question**: Does the reduced module set change how `ResourceUpToDate` is computed or when
`status.observedGeneration` advances?

**Decision**: No change from 009's contract: `ResourceUpToDate = exists && cr.Status.ObservedGeneration
== cr.Generation && Observation.InSync`. `cr.Status.SetObservedGeneration(cr.Generation)` is called
only when a `pipeline.Apply` pass returns `Result.AllDone() == true`. With one module, `AllDone()` is
just "was module 010's outcome `Done`."

**Rationale**: Straight application of an already-written contract (009); the module count is
incidental to it.

**Affects spec section**: Integration Points.

### D-007 — Namespace labels are fetched into `ModuleContext` but consumed by nothing yet

**Question**: `NewModuleContext` takes `namespaceLabels map[string]string`, read by `internal/tenant`
readers that only 008 (`Department`) and 016 (`CreditQuota`) call today. Should 018 skip fetching the
namespace object entirely (nothing needs it), or fetch it anyway?

**Decision**: 018 fetches the tenant namespace via the k8s client on every reconcile and passes its
`.Labels` into `NewModuleContext` as it always would, even though no registered module reads them
yet. `CostCenter`/`Department`/`CreditQuota` are not called anywhere in 018 itself.

**Rationale**: Keeps `ModuleContext` construction stable — when 008 or 016 land, they read labels
already present on the context 018 built, with no change to 018's `Observe`/`Create`/`Update` shape.
The fetch is cheap (one `Get` per reconcile, already needed for standard multi-tenant namespace
scoping) and avoids a second migration later just to add it back.

**Affects spec section**: Integration Points, Project Structure.

## Problem Areas

None outstanding that block writing 018 at the scope fixed by D-001. Everything design.md's full
picture would add is deliberately deferred and tracked as a Forward Contract below.

## Open Questions

- None material to writing 018 at this scope. Each omitted capability (008, 011–013, 015–017) has an
  owner (its own future clarification) and a stated point of entry via the Forward Contracts below.

## Forward Contracts

- **008 (guardrails)** — 018 as built calls no guardrail check anywhere. When 008 lands, it must be
  inserted as a validation-phase step that runs once, before the pipeline call, in `Observe`
  (read-only check) and `Create`/`Update` (enforced check) — rejecting with a user error before
  `pipeline.Apply` is ever invoked, exactly as scope-008 already describes. 018 does not need to
  change shape to add this; it needs one new call site.
- **011 (parameter module), 012 (network module), 013 (auth module), 015 (identity module)** — 018's
  pipeline is constructed as `account.New(accountModule)`. Each of these, once implemented, is added
  as a further argument in registration order (010 → 011 → 012 → 013 → 015 → 016, per scope-018's
  roadmap note) — no other change to 018's `Observe`/`Create`/`Update` bodies is needed, since the
  pipeline is already generic over its module list.
- **015 (identity module) and 016 (quota)** specifically also introduce the first modules that set
  `Outcome.Condition` (`IdentitySynced`, `QuotaAvailable`). At that point 018's `Observe` needs the
  per-module condition-rendering loop described in D-005 (walking `Result.Outcomes`), which does not
  exist yet because nothing produces a condition to render today.
- **016 (quota)** also owns `Admit()`, called separately from the pipeline. 018 as built calls it
  nowhere. When 016 lands, `Admit()` is inserted into 018's validation phase (alongside guardrails)
  before `pipeline.Apply` runs, per scope-016.
- **017 (deletion request/warrants)** — 018's `Delete` as built (D-002) is
  `DROP ACCOUNT IF EXISTS <resolvedName> GRACE_PERIOD_IN_DAYS = 3` with no warrant lookup. When 017
  lands, `Delete` must be replaced with the warrant-gated version design.md §6.3 describes: query for
  an Active `SnowflakeDeletionRequest` in the same namespace targeting this resource; if found, run
  the drop, release the finalizer, and mark the request `Consumed`; if absent or expired, return a
  user error so the framework's own finalizer handling leaves the resource in `Terminating` — this is
  a full rewrite of `Delete`'s body, not an additive change.

## References

- `specs/design.md` §3.2 (create-flow lifecycle), §6.3 (Positive Control / deletion phases), §7
  (condition model, status example) — authoritative product design.
- `specs/009-account-pipeline.md` — `Module`/`Pipeline`/`Outcome`/`Result` contract; its "Raised by"
  annotations in `specs/scope-018-snowflakeaccount-controller.md` are the direct source for D-001,
  D-004, D-005, D-006.
- `specs/010-account-module.md` and `internal/account/modules/account/apply.go` — create-then-verify
  lifecycle; the existing `contacts[]` empty-check that removes the crash risk D-001's rationale
  relies on.
- `specs/006-snowflake-account-crd.md` — `tenant.ResolveName`, `tenant.AccountURL`, CRD schema
  (confirms `contacts[]` is `+optional` with no `minItems`, i.e. genuinely unenforced without 008).
- `specs/001-error-and-logging.md` and `internal/logger/logger.go` — `Logger`/`Handle` pattern used
  in `Observe`/`Create`/`Update`/`Delete`.
- `specs/scope-008-guardrails.md`, `scope-011-parameter-module.md`, `scope-012-network-module.md`,
  `scope-013-auth-module.md`, `scope-015-identity-module.md`, `scope-016-quota.md`,
  `scope-017-deletion-request.md` — each carries a "Raised by the 018 clarification" section as of
  this run (see Propagation).
- Snowflake docs, `docs.snowflake.com/en/sql-reference/sql/drop-account` (fetched during this
  clarification) — `DROP ACCOUNT` syntax, mandatory `GRACE_PERIOD_IN_DAYS` clause (3–90 days), org-
  admin-only execution. Source for D-002.
