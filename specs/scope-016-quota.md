> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `016`'s intended
> *scope*, not its content. When writing `016-quota.md`, the sole sources of truth are
> `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or discard
> anything below freely. This file does not need to be kept up to date, and should be deleted
> once `016-quota.md` has been written.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003-a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003-a` < `003-b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Package: `internal/quota/`. Covers design 3.10. Depends on: 005, 006, 009.
- One spec with **three entry points**, because 3.10 spans three phases and splitting them would put
  the same arithmetic in two documents. It lives in `internal/quota/` rather than under `modules/`
  because it is called from two phases, not only from the pipeline, and it is kept out of 018 so that
  the arithmetic is unit-testable without a controller, per CLAUDE.md's thin-controller rule.
- Scope:
  - **`Admit()` — the admission/validation phase**: on every create or update, list all
    `SnowflakeAccount` resources in the namespace, sum their claimed `creditQuota`, and compare the
    total against the namespace's `credit-quota` label (set by ops at onboarding and read through
    `internal/tenant`). Exceeding the allowance is rejected with a validation error. The label is the
    trust anchor: it lives outside the tenant's Git repository, so teams cannot raise it themselves.
  - **First-come-first-served on reductions**: if ops lowers the namespace allowance, existing
    accounts are **never retroactively suspended**, but future creates and updates are blocked until
    the tenant lowers their claims to fit.
  - **`Apply()` — enforcement**: push the approved quota into Snowflake as an account-level resource
    monitor and budget limit. The resource monitor suspends warehouses when the quota is exhausted,
    physically stopping most spend in real time.
  - **`Observe()` — exhaustion**: surface the `QuotaAvailable` condition — `True` while credits
    remain, and on exhaustion `False` with reason `QuotaExhausted` plus a matching warning event.
    This is **not** a provisioning failure: the account remains fully intact and `Ready` stays
    `True`. It clears automatically at the start of the next monthly billing cycle.
- Known gap to record (a design TODO): resource monitors only cover warehouse compute. Serverless
  features and AI functions cannot be suspended this way, so budgets for them are notify-only. The
  options under consideration are waiting for native org-level spending limits, gating access to
  serverless and AI features, or custom privilege-revocation logic. Appendix B S1/S2 track the asks.

## Cross-cutting context from the roadmap

- **Deliberately unnumbered — 3.10's serverless and AI spend cap** (a design TODO). See the "Known
  gap" note above; this takes the next free spec number when the TODO closes, rather than reserving a
  forward reference now.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).
