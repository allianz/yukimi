> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `004`'s intended
> *scope*, not its content. When writing `004-connection-pooling.md`, the sole sources of
> truth are `specs/design.md` and the prompt given at spec-writing time — rework, restructure,
> or discard anything below freely. Please keep this file up to date until
> `004-connection-pooling.md` has been written, then delete it.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003-a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003-a` < `003-b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Package: `internal/snowflake/pool/`. Covers the design 3.11 introduction and 3.6.
  Depends on: 001, 002, 003.
- Scope:
  - Pooled `*sql.DB` connections to Snowflake using **JWT keypair authentication**, with the private
    key read through 003's interface — never through a backend package, so the pool's tests run
    against 003's in-memory fake.
  - **Two connection scopes, enforcing the privilege step-down of 3.11**: an org-admin connection
    used only for org-level operations (`CREATE ACCOUNT`, `DROP ACCOUNT`), and a
    per-tenant-account connection acting as that account's `platform` user for everything else. The
    point is to minimize blast radius — org credentials are restricted almost entirely to account
    creation and deletion.
  - Pool keyed by `(org, namespace, account)` — the same tuple as 003's tenant secret path.
  - Host construction honoring the PrivateLink flag from 002.
  - Session setup on checkout (`USE ROLE` and similar) plus a health probe, both using the raw
    driver.
  - Connection lifecycle: idle eviction, maximum lifetime, concurrency-safe checkout.
- **Hard rules to write into the spec**: this package must not import
  `internal/snowflake/statement`, otherwise 004 → 005 → 004; and it must not import
  `internal/secrets/aws`, or any other backend package — it takes 003's interface as a constructor
  parameter, which is what keeps a pool test from needing an AWS account.
- Out of scope: statement semantics and error decoration (005).

## Cross-cutting context from the roadmap

- **Why 004 must not import 005.** The pool performs session setup (`USE ROLE`) and health probes
  using the **raw driver** (`db.ExecContext` / `PingContext`). Without this rule an implementer
  naturally reaches for `statement.Execute` and creates 004 → 005 → 004. Spec 004 must state the
  prohibition explicitly.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).
