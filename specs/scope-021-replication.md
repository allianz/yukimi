> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `021`'s intended
> *scope*, not its content. When writing `021-replication.md`, the sole sources of truth are
> `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or
> discard anything below freely. Please keep this file up to date until `021-replication.md`
> has been written, then delete it.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003.a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003.a` < `003.b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Packages: `apis/base/v1alpha1/` (SnowflakeReplication), `internal/replication/`,
  `internal/controller/snowflakereplication/`. Covers design 5.1–5.4.
  Depends on: 001, 004, 005, 006.
- Scope:
  - CRD: `description`, `accounts[]` (SnowflakeAccount **CRD** names, not resolved names),
    `primaryAccount`, `objectTypes[]` (for example DATABASES, WAREHOUSES), `databases[]` (supporting
    wildcards such as `PROD_*`), and `schedule` (for example `"10 MINUTE"`). Exactly one primary.
  - **Validation**: every account under `accounts` must declare the same `environment`; a mismatch is
    rejected, because linking a `prod` account to a `dev` one would replicate production data into an
    account held to the looser `dev` network posture. Since `environment` is immutable, a group that
    validates at setup cannot later drift into a mixed state.
  - **No region-pair validation** (5.2), deliberately. Each linked account was already restricted to
    a legally permitted region by the guardrails' `allowedRegions` at creation time, so an illegal
    pair cannot arise — it would require an account to exist in a region guardrails would have
    rejected.
  - **Infrastructure is never replicated** — only customer data and logical objects. Network rules
    and endpoints stay regional, or regional connectivity breaks.
  - **Native Snowflake execution**: the controller does not manage the ongoing sync. At setup it
    provisions a stored procedure and a scheduled task inside the primary account and hands over.
    That native setup runs on the schedule, resolves database wildcards, and updates the replication
    group as the environment changes.
  - **Auto-repair**: tenants have access to their own Snowflake environment and can break the
    replication code. On detecting errors or drift, the controller repairs by completely removing and
    recreating the stored procedure and the task.
  - **Manual failover only**: a failover happens only when a tenant explicitly changes
    `primaryAccount` in Git, which prompts the controller to promote the new primary. Never
    automatic — that would risk split-brain corruption from a transient network blip.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).
