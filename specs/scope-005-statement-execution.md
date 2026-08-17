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

## Scope notes

- Package: `internal/snowflake/statement/`. Covers the SQL mechanics used throughout design 3.6–3.10.
  Depends on: 001.
- Scope:
  - Execute SQL against an **injected executor**. It never constructs its own connection, so it does
    not import 004. The mirror-image rule already lives in `scope-004`: the pool must not import this
    package, or 004 → 005 → 004.
  - **Both an exec path and a row-returning path**, as any wrapper over `database/sql` has. Existence
    checks (`SHOW … LIKE`) and drift read-backs (011) need rows. Which statements use which path is each
    module's business, not 005's.
  - **The row path returns a materialized result, not `*sql.Rows`.** The injected executor keeps the
    `*sql.DB` shape (`ExecContext` plus `QueryContext`, which `*sql.DB`, `*sql.Conn` and `*sql.Tx` all
    satisfy); 005 consumes the `*sql.Rows` itself and hands callers column names plus rows keyed by
    column name, values as `any`. The reason is caller ergonomics, not testability — six modules should
    not each write `rows.Next()` / `defer rows.Close()` / `rows.Err()`, and the last of those is a real
    bug class: without it a mid-iteration failure is indistinguishable from an empty result.
    Deliberately thin: no accessor or coercion tier. Every caller is an in-repo module that knows its
    own query's shape and casts at the call site (comma-ok, so a mismatch becomes a system error rather
    than a panic in a reconcile loop). Every read in this design is a small `SHOW` or `SELECT`, so
    materializing costs nothing.
  - **Bind first; render only where binding is impossible.** Snowflake binds values with `?` and object
    names with `IDENTIFIER(?)`, in DDL as well as DML (notes §7), so the default shape is
    `ALTER USER IDENTIFIER(?) SET NETWORK_POLICY = ?` with nothing interpolated and no escaping to get
    wrong. Interpolation into statement text is the exception, and 005 owns it so that one place gets it
    right rather than five modules each hand-rolling it. Known and suspected exceptions:
    - The parameter *name* in `ALTER ACCOUNT SET <param> = <value>` (3.5, 3.6) — keyword-like rather
      than an object name, so `IDENTIFIER()` does not apply and quoting is believed invalid. Operators
      supply these arbitrarily, so a charset-validated *bare* identifier is the only defense. This is
      the load-bearing case.
    - `CREATE SECURITY INTEGRATION` for SSO (3.9) — binds are prohibited in `CREATE/ALTER INTEGRATION`
      outright.
    - `SHOW … LIKE '<pattern>'` — a string-literal position, but whether `SHOW` accepts binds at all is
      unverified, so assume the pattern is rendered. This is the most frequent rendering call site in the
      platform, since every existence check goes through it.
    - Anywhere `IDENTIFIER(?)` turns out not to be accepted. The documented context list is not
      exhaustive; `CREATE ACCOUNT` (3.6) and the policy-name value positions of 3.8 and 3.9 are
      unverified and need a live check at spec-writing time.

    So all three rendering forms — a quoted identifier, a quoted string literal, and a charset-validated
    bare identifier — have real callers, but they sit behind binding rather than being the primary path.
    Guardrails constrain only `accountName` and `groupNames` (`scope-008`), so a service-user name or
    `customAuthRules.exceptions[].user` reaches SQL unconstrained: harmless once bound, not harmless on
    a rendering fallback. Rendering therefore re-validates input that 006, 007 and 008 have already
    checked, on the same reasoning spec 003 gives for re-validating every path segment "independently of
    whatever validation the caller already did".
  - **Ordered execution, one statement per call, stop on first error.** No batching. The
    spec should state plainly that there is **no rollback** and that partial application is the
    expected outcome of a failure, not a defect: Snowflake DDL is per-statement transactional and
    cannot be rolled back at all. Convergence is the next reconcile's job, which is where idempotency
    actually lives — in the SQL each module emits.
  - **System-error decoration from structured fields only.** Wrap a failure with a caller-supplied
    statement label, the statement text, and the driver's `QueryID`, `Number` and `SQLState`.
    `QueryID` is the field that earns its place: it is the handle an operator uses to find the
    statement in Snowflake's query history. It must be read from the struct field — the driver never
    sets `IncludeQueryID` on the failure path, so the error string omits it.
    **Never the bound arguments.** Bind-first has a useful side effect here: tenant and credential values
    live in the args rather than in the statement text, so the text is safe to put in an operator log,
    while the args are not — 010 alone binds a public key, and later modules bind more sensitive values.
    Adding args to the error for debuggability is the obvious wrong turn to close off explicitly.
  - **No hand-rolled fake — `DATA-DOG/go-sqlmock` at the executor boundary.** Every module in 010–013,
    015, 016 and 019 needs to assert "these statements, in this order" without a Snowflake account.
    sqlmock already is that: strict expectation ordering by default, `WithArgs` for bind values,
    `ExpectationsWereMet()` for the recording assertion, and `sqlmock.QueryMatcherOption(QueryMatcherEqual)`
    to swap its regexp default for exact case-sensitive statement matching. Use it rather than writing
    our own — it is a far more widely understood idiom for anyone reading these tests, and a module test
    then drives the **real** 005 over a mocked driver (`statement.New(db)`), exercising the actual
    materializer, rendering and error decoration instead of a parallel fake that can drift from them.
    This is possible only because 005 takes an injected executor and never opens its own connection.
    It also removes the need for a separate integration test of the scan loop.
    Note this is **not** the situation 003 is in: its `FakeBackend` exists because `Backend` has
    genuinely swappable implementations and 003 had to be testable before 003-a existed. 005 has one
    real implementation, `*sql.DB`, and no unimplemented dependency to stand in for.
    Two things to state plainly in the spec rather than discover in review: sqlmock's driver-level type
    conversions are not gosnowflake's, so a test asserting a read-back value's Go type proves less about
    production than it looks; and sqlmock is test-only, so its README's "looking for maintainers" status
    carries no production risk — if it ever rots, the fallback is the ~100-line `driver.Driver` shim we
    are declining to write today.
- Out of scope: which SQL to emit — that belongs to each module (010–013, 015) and to 016 and 019.

## Settled by ordering: the driver dependency

No Snowflake driver is named anywhere in the repo and `go.mod` has no Snowflake entry; gosnowflake is
the implied candidate. **004 adds it, not 005.** The ordering rule decides this without a judgement
call: 004 is written and implemented first, and it needs the driver unconditionally for the DSN and
`sql.Register`, whereas 005 only consumes the already-registered driver plus its `SnowflakeError` type
for the decoration above. Pin the version in the same deliberate way `scope-003-a` owns the AWS
dependency — `notes-snowflake-sql-mechanics.md` records its findings against **v1.18.1**, and an
upgrade invalidates them.

## Open questions for spec-writing time

- None outstanding. Two items previously listed here are resolved above: what the row path returns
  (materialized `Result`, in Scope) and which spec adds the driver (004, immediately above). What
  remains genuinely unverified is vendor behaviour rather than scope — the `IDENTIFIER()` and
  parameter-name questions marked **Unconfirmed** in `notes-snowflake-sql-mechanics.md` §7, which need a
  live check against an account before the rendering fallbacks are specified.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Snowflake SQL mechanics**: `specs/notes-snowflake-sql-mechanics.md` — the sourced driver and SQL
  findings behind the scope decisions above, marking each claim verified or unconfirmed.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).
