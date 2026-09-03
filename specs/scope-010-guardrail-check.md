> **Scope context only — not a specification.** This file gives a starting-point idea of spec `010`'s
> intended *scope*, not its content. When writing `010-guardrail-check.md`, the sole sources of truth
> are `specs/design.md`, `specs/scope-008-guardrails.md`, and the prompt given at spec-writing time —
> rework, restructure, or discard anything below freely. Please keep this file up to date until
> `010-guardrail-check.md` has been written, then delete it.
>
> This scope note is written alongside a renumbering: guardrail-check and quota-check both move to sit
> directly after the pipeline spec (009) and before the account module, matching the order they
> actually run in at runtime. Quota-check itself relocates from spec 016 to spec 011 — see
> `specs/scope-011-quota-check.md`, its sibling in this same reshuffle.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003.a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003.a` < `003.b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

Spec numbers track dependency/write order, not pipeline runtime order — but for this module and its
sibling quota-check, the two now coincide: both need no Snowflake connection, both can abort the run
before the account module ever creates anything, and both are now numbered accordingly, directly after
the pipeline spec they depend on.

## Scope

- Package: `internal/account/modules/guardrailcheck/` (mirrors `quotacheck`'s compound naming — both
  are admission-only pipeline modules). Depends on: 008 (guardrails — the ConfigMap loader, top-down
  merge, constraint evaluator, and approved-exceptions fallback), 009 (pipeline `Module` interface,
  `Outcome`, `Rejected`, `Aborting`).
- **The check**: on `Apply`, call 008's single evaluate-and-check-exceptions function against this
  CRD and the namespace's `department` label (design 3.3/3.4). A rejection that survives the
  exceptions-file fallback is `Rejected(err).Aborting()` — `Rejected` because it's the tenant's own
  input being refused, `Aborting` because nothing after this module should run against input that
  never should have reached Snowflake.
- **Registered first**, ahead of quota-check (011) and everything else. Guardrails is the more
  fundamental gate — it rejects structurally invalid input (bad region, malformed CIDR, missing
  contacts, etc.) — so it should run before quota-check's sibling-CR list-and-sum work, which is only
  worth doing once the input itself is known-good.
- **`Observe` is a no-op**: always `true, Done()`. No read-back, no drift detection — 008's evaluation
  is a pure function of the CRD, the loaded config/exceptions, and the namespace's `department` label;
  there is nothing Snowflake-side to observe and nothing that can drift.
- Needs no k8s lister and no `OrgAdminDB`/`TenantDB` — only `ModuleContext.CR()` and
  `NamespaceLabels()`, plus the loaded 008 config/exceptions instance injected into this module's own
  constructor (the same pattern the account module's constructor uses for `secrets.Backend` and
  `Config.Snowflake.Org`).

## Decisions from design conversation

Recorded from a direct conversation (no `/yukimi.clarify` run) that resolved how guardrail admission
should be wired, mirroring the earlier quota conversation recorded in
`specs/scope-011-quota-check.md` (old `scope-016-quota-check.md`):

- **Guardrail admission becomes an ordinary pipeline module, not the controller's own pre-pipeline
  gate.** The old framing — recorded in `specs/scope-008-guardrails.md`'s "Relationship to the account
  pipeline" section, `specs/009-account-pipeline.md`'s Out-of-Scope/Dependencies/Integration-Points
  sections, `specs/012-account-module.md` (old `010`), and `specs/scope-020-snowflakeaccount-controller.md`
  (old `scope-019-...`) — had guardrails run "in full, once, inside the controller's validation phase,
  strictly before the account pipeline is ever invoked." That's the same shape quota's `Admit()` used to
  have before it moved into the pipeline as quota-check (011); the same reasoning applies here, and all
  of those files were updated in the same pass that introduced this scope note.
- **008 itself is untouched.** It stays a pure ConfigMap loader, top-down merge, and evaluation
  function (plus the approved-exceptions fallback) with zero dependency on 009 or any module. Only this
  new module depends on both 008 and 009 — the same one-way relationship quota-check already has.
- **Registered ahead of quota-check**, not after: guardrails is a cheaper, more fundamental check with
  no k8s list call, so it should reject bad input first.

## References

- **Product design**: `specs/design.md` §3.3 (Guardrails), §3.4 (Approved Exceptions).
- **Shape reference**: `specs/scope-011-quota-check.md` (old `scope-016-quota-check.md`) — the sibling
  admission module this one is modeled on; follow its section skeleton.
- **Dependency specs**: `specs/scope-008-guardrails.md` (the evaluator this module wraps),
  `specs/009-account-pipeline.md` (the `Module` contract, `Outcome`, `Rejected`, `Aborting`).
