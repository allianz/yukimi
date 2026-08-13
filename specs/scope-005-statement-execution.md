> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `005`'s intended
> *scope*, not its content. When writing `005-statement-execution.md`, the sole sources of truth are
> `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or discard
> anything below freely. This file does not need to be kept up to date, and should be deleted
> once `005-statement-execution.md` has been written.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003-a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003-a` < `003-b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Package: `internal/snowflake/statement/`. Covers the SQL mechanics used throughout design 3.6–3.10.
  Depends on: 001.
- Scope:
  - Execute SQL against an **injected executor** (`*sql.DB`, or a one-method
    `interface{ ExecContext(...) }`). It never constructs its own connection, so it does not import
    004.
  - **Position-aware errors**: Snowflake reports an error position within the submitted statement;
    decorate failures with that position and the offending fragment so operators can locate the
    problem in a long generated statement.
  - Classify Snowflake error codes into user errors (bad identifier, object already exists,
    insufficient privileges on tenant-supplied input) and system errors (network, timeout, internal).
  - Idempotency helpers so that re-running a partially applied module is safe (`IF NOT EXISTS` and
    `CREATE OR REPLACE` conventions), since every module is re-invoked on each reconcile.
  - Multi-statement / ordered batch execution with stop-on-first-error semantics.
- Out of scope: which SQL to emit — that belongs to each module (010–013, 015) and to 016.

## Cross-cutting context from the roadmap

- **Why 005 takes an injected executor.** It takes `*sql.DB`, or a one-method interface, so it does
  not import the pool (004) either. Position-aware error decoration is testable against any
  `*sql.DB`.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).
