> **Scope context only — not a specification.** This file gives a starting-point idea of spec `017`'s
> intended *scope*, not its content. When writing `017-quota-monitor.md`, the sole sources of truth are
> `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or discard
> anything below freely. Please keep this file up to date until `017-quota-monitor.md` has been written,
> then delete it.
>
> This file, together with `specs/scope-016-quota-check.md`, supersedes the earlier
> `specs/scope-016-quota.md`. See that file's own header for the split's history.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003.a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003.a` < `003.b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Scope

- Package: `internal/account/modules/quotamonitor/`. Covers design 3.10's enforcement half (the
  "pushes that share into Snowflake as a resource monitor and a budget" half of the §3.2 flow's Credit
  Quota step). Depends on: 005 (statement execution), 006 (tenant), 009 (pipeline `Module` interface).
- **`Apply`** re-asserts the account's resource monitor and budget limit unconditionally —
  `CREATE OR REPLACE`-style, no `SHOW`, no read-back — sized to this account's approved `creditQuota`
  share. It needs `TenantDB`, so it stays registered **after** the account module (010), where the old
  single-module quota plan used to sit at the end of the pipeline (010 → 011 → 012 → 013 → 015 → 017).
- **`Observe`** surfaces the `QuotaAvailable` condition — `True` while credits remain, `False` with
  reason `QuotaExhausted` plus a matching warning event once the resource monitor has suspended
  warehouses. This is **not** a provisioning failure: the account stays fully intact and `Ready` stays
  `True`. 009 owns the `TypeQuotaAvailable` constant and the `GatesReady` table that keeps it non-gating;
  this module only attaches the condition to its `Outcome`. It clears automatically at the next monthly
  billing cycle.
- **Never calls `.Aborting()`.** Only `quotacheck` (016) and the account module (010) can stop the
  pipeline; a `Failed` or `Rejected` outcome from this module must not prevent later modules from
  running, per 009's "non-aborting outcomes don't stop later modules" rule.
- **Known gap to carry over** (a design TODO, not solved here): resource monitors only cover warehouse
  compute. Serverless features and AI functions cannot be suspended this way, so budgets for them are
  notify-only. Options under consideration — native org-level spending limits, gating serverless/AI
  feature access, or custom privilege-revocation logic — are tracked at design.md Appendix B S1/S2.

## References

- **Product design**: `specs/design.md` §3.2, §3.10, Appendix B (S1/S2) — the authoritative product
  requirements this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).
- **Sibling scope note**: `specs/scope-016-quota-check.md` — the admission half this module never
  performs, and the design-conversation decisions behind the split (see its "Decisions from design
  conversation" section).
