# Specification: Statement Execution (005)

## Overview

This specification defines `internal/snowflake/statement/`, the shared mechanics every account-provisioning module (010–013, 015, 016, 019) uses to talk to Snowflake: running one SQL statement at a time against an injected connection, materializing whatever rows come back, and decorating a failure with the driver's own diagnostic fields. It binds values and object names wherever Snowflake accepts a bind, and owns the handful of rendering primitives needed for the few positions that require literal SQL text instead — so escaping is solved once, here, rather than five times over in the modules that actually decide what SQL to emit.

## Scope

This specification defines the `internal/snowflake/statement/` package that:
- Executes SQL against an **injected `Executor`** — the subset of `*sql.DB` this package needs (`ExecContext`, `QueryContext`). It never opens a connection itself and never imports `internal/snowflake/pool` (004); 004 documents the mirror-image rule and never imports this package either.
- Offers **two execution paths**: `Exec`, for statements that return no rows (DDL and non-`SELECT` DML), and `Query`, for statements that do (`SHOW ... LIKE` existence checks, drift read-backs). Which path a given statement uses is each calling module's decision, not this package's.
- **Materializes every row-returning result** before returning it: column names plus rows keyed by column name, values as `any`. Deliberately thin — no accessor or coercion tier. Every caller already knows its own query's shape and casts at the call site with a comma-ok assertion.
- **Binds first, renders only where binding is impossible.** Supplies three rendering primitives — a quoted identifier, a quoted string literal, and a charset-validated bare identifier — for the small set of statement positions that cannot be bound.
- **Runs statements in order, one per call, and stops on the first error.** No batching, no multi-statement execution, no rollback.
- **Decorates a failure with structured fields only**: a caller-supplied label, the statement text, and — when the underlying error is a `*gosnowflake.SnowflakeError` — its `Number`, `SQLState`, and `QueryID`. Never the bound arguments.

**Out of Scope**:
- **Which SQL to emit, and in what order, for any given operation.** That is every downstream module's business (010–013, 015, 016, 019), not this package's.
- **Whether `IDENTIFIER(?)` binding works at a given statement position.** `notes-snowflake-sql-mechanics.md` §7 marks several of these positions "Unconfirmed" — the account name in `CREATE ACCOUNT` (3.6), and the policy-name value in `ALTER USER ... SET NETWORK_POLICY` (3.8) and `ALTER USER ... SET AUTHENTICATION_POLICY` (3.9). This package does not adjudicate those questions. It supplies the rendering primitives and the bind-first policy; the module that actually emits each statement (006, and the network/auth modules of 012/013) decides, at its own spec-writing time, whether to attempt a bind there or go straight to a renderer — including a live check against a real account if it chooses to attempt the bind first. This division is deliberate, not an oversight left for later.
- **Accessor or coercion helpers** on the materialized `Result` beyond a caller's own comma-ok type assertion.
- **Retries.** No retry logic lives in this package or anywhere in this codebase's business logic; a failure is returned as-is and the caller (ultimately Kubernetes/Crossplane, per project-wide policy) decides whether to try again.
- **Connections, credentials, and pooling** — entirely 004's job. This package accepts an already-open `Executor` and never asks how it got that way.

## Key Concept: Bind First, Render Only Where Binding Is Impossible

Snowflake accepts `?` binds for both values and object names (the latter via `IDENTIFIER(?)`) across queries, DML, and DDL alike, so the default shape for every statement in this design is something like `ALTER USER IDENTIFIER(?) SET NETWORK_POLICY = ?` — nothing interpolated, nothing to escape. Interpolating text directly into a statement is the exception, not the default, and this package owns every place it happens so that escaping is solved once rather than re-derived by each module that needs it.

Three rendering primitives cover the known and suspected exceptions, each with a real caller elsewhere in this design:

- **`QuoteIdentifier`** — a double-quoted, escaped object name. For positions where `IDENTIFIER(?)` binding turns out not to be accepted once checked (the `CREATE ACCOUNT` account name, 3.6; the policy-name value positions of 3.8/3.9).
- **`QuoteLiteral`** — a single-quoted, escaped string literal. Its primary caller is `SHOW ... LIKE '<pattern>'`: whether `SHOW` accepts a bind for its pattern at all is unverified, so the pattern is rendered. This is the single most frequent rendering call site in the platform, since every existence check goes through `SHOW`.
- **`BareIdentifier`** — a charset-validated, unquoted token, returned unchanged or rejected outright. Its one known caller is the parameter *name* in `ALTER ACCOUNT SET <param> = <value>` (3.5, 3.6): that position is keyword-like rather than a true object name, so neither `IDENTIFIER()` nor quoting is believed to apply, and operators supply these names arbitrarily. A regex check on the bare token is the only available defense — this is the load-bearing rendering case in this package, not a convenience.

A fourth known no-bind context, `CREATE SECURITY INTEGRATION` (the account-wide SSO integration of 3.2/3.9, and `PLATFORM_OIDC` of 3.11.2 once built), needs no primitive of its own — binds are prohibited across the whole `CREATE/ALTER INTEGRATION` family, and whatever object names or literals that statement needs reuse `QuoteIdentifier`/`QuoteLiteral` above.

## Key Concept: Ordered Execution, No Rollback

`Exec` and `Query` each run exactly one statement per call. A module that needs several statements — bootstrapping an account is a dozen or more — calls this package once per statement, in order, and stops at the first failure. There is no batching and no multi-statement mode: Snowflake's own multi-statement execution buys no atomicity (each DDL statement is still its own transaction and cannot be rolled back) while costing exact failure attribution, so one statement per call is strictly better for a caller that needs to know precisely which statement failed.

Partial application on failure is therefore the expected outcome of an error, not a defect to guard against here. Snowflake DDL cannot be rolled back at all, so an account that fails halfway through bootstrapping is left with whatever statements already succeeded. Convergence is the next reconcile's job: each module's own SQL is written to be idempotent (`CREATE ... IF NOT EXISTS`, `CREATE OR ALTER`, and similar), so repeating a partially-applied sequence completes it rather than duplicating what already landed. This package has no opinion on idempotency — it only runs what it is given.

## Public API

```go
// Package statement executes SQL against an injected Executor, one
// statement per call, materializing rows and decorating failures with
// structured driver fields. It never opens a connection itself (004's job)
// and never decides what SQL to run (every downstream module's job).
package statement

import (
    "context"
    "database/sql"
)

// Executor is the subset of *sql.DB this package needs. *sql.DB, *sql.Conn
// and *sql.Tx all satisfy it, as does DATA-DOG/go-sqlmock's driver in tests.
// This package never imports internal/snowflake/pool (004); 004 hands one
// of these in.
type Executor interface {
    ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
    QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Runner executes statements against a fixed Executor. Safe for concurrent
// use exactly when the Executor is (true of *sql.DB; not of a shared *sql.Tx).
type Runner struct { /* unexported */ }

// New wraps exec for statement execution. Makes no call of its own.
func New(exec Executor) *Runner

// Exec runs one statement that returns no rows (DDL, and non-SELECT DML).
// args are bound positionally with ? or IDENTIFIER(?); use the renderers
// below only where binding a given position is not possible.
//
// Parameters:
//   - label: short human string identifying the statement, e.g. "create
//     account" — carried into the returned error; never appears in the
//     statement text itself
//   - sql: the statement text, with ? / IDENTIFIER(?) placeholders
//   - args: bind values, positional
//
// Returns:
//   - nil on success; otherwise a *Error wrapping the Executor's failure
func (r *Runner) Exec(ctx context.Context, label, sql string, args ...any) error

// Result is a materialized row-returning query result. Deliberately thin:
// no accessor or coercion methods. Every caller already knows its own
// query's column shape and casts at the call site, comma-ok
// (e.g. name, ok := row["NAME"].(string)).
type Result struct {
    Columns []string
    Rows    []map[string]any
}

// Query runs one row-returning statement (SHOW ... LIKE existence checks,
// drift read-backs) and materializes every row before returning, so callers
// never drive rows.Next()/rows.Err() themselves. Omitting rows.Err() after
// Next() returns false is a real bug class: it makes a mid-iteration
// failure indistinguishable from a genuinely empty result. See Exec for
// label/args and error-decoration behavior.
//
// Returns:
//   - Result{}, nil for a query that matches no rows — not an error
//   - otherwise a *Error wrapping the Executor's or the row scan's failure
func (r *Runner) Query(ctx context.Context, label, sql string, args ...any) (Result, error)

// QuoteIdentifier double-quotes name for use as a rendered SQL identifier,
// doubling any embedded double quote. Use only where IDENTIFIER(?) binding
// has been confirmed, at the calling module's spec-writing time, not to
// work for that statement position (e.g. CREATE ACCOUNT's account name, if
// found unsupported there — notes-snowflake-sql-mechanics.md §7).
func QuoteIdentifier(name string) string

// QuoteLiteral single-quotes s for use as a rendered SQL string literal,
// doubling any embedded single quote. Its primary caller is
// SHOW ... LIKE '<pattern>', since whether SHOW accepts a bind for its
// pattern at all is unverified (notes-snowflake-sql-mechanics.md §7) —
// assume rendered.
func QuoteLiteral(s string) string

// BareIdentifier validates name as a bare, unquoted SQL token and returns
// it unchanged, or a user error if it does not match the expected charset.
// Its one known caller is the parameter name in ALTER ACCOUNT SET <param> =
// <value>: that position is keyword-like rather than a true object name, so
// neither IDENTIFIER(?) nor quoting is believed to apply
// (notes-snowflake-sql-mechanics.md §7) — this check is the only defense
// against an operator-supplied parameter name reaching SQL text unescaped,
// and is the load-bearing rendering case in this package.
//
// Returns:
//   - name unchanged if it matches ^[A-Za-z][A-Za-z0-9_]*$
//   - otherwise a user error (errors.NewUserError)
func BareIdentifier(name string) (string, error)

// Error decorates a statement failure with structured context, never with
// the bound args — bind-first keeps sensitive values (an RSA public key
// today, more later) out of statement text, so putting them back into the
// error for "debuggability" is the wrong turn this type exists to close off.
//
// Number, SQLState and QueryID are populated only when the underlying error
// is a *gosnowflake.SnowflakeError (checked via errors.As); any other
// failure — a context deadline, a network error — leaves them zero/empty
// and this decorates with Label and Statement alone.
type Error struct {
    Label     string // caller-supplied, e.g. "create account"
    Statement string // the statement text; safe to log, args are bound separately
    QueryID   string // gosnowflake.SnowflakeError.QueryID; empty if Err isn't one
    Number    int    // gosnowflake.SnowflakeError.Number; 0 if Err isn't one
    SQLState  string // gosnowflake.SnowflakeError.SQLState; empty if Err isn't one
    Err       error  // the error returned by Executor or by row scanning
}

// Error renders a one-line summary — label, driver code when present, and
// the underlying message — and never the bound args, e.g.:
//   create account: failed (Number=2003 SQLState=42710): SQL compilation error: ...
//   create account: failed: context deadline exceeded
func (e *Error) Error() string

// Unwrap returns Err, so errors.Is and errors.As reach the original
// Executor/scan error through this wrapper — e.g. a caller can still do
// var sfErr *gosnowflake.SnowflakeError; errors.As(err, &sfErr) against an
// error returned by Exec or Query.
func (e *Error) Unwrap() error
```

## Project Structure

```text
internal/snowflake/statement/
├── statement.go         # Executor, Runner, New, Exec, Query, Result
├── statement_test.go    # sqlmock-driven tests
├── integration_test.go  # live-Snowflake test via a real 004 Pool.TenantAccount connection
├── render.go             # QuoteIdentifier, QuoteLiteral, BareIdentifier
├── render_test.go
├── errors.go             # Error, Error(), Unwrap()
├── errors_test.go
└── doc.go
```

Production code here depends only on `internal/errors` (001) and never imports `internal/snowflake/pool` (004) — `integration_test.go` is the sole exception, importing 004 to get a real `*sql.DB` for testing.

## Error Classification

**User Errors** (via `errors.NewUserError()`):
- `BareIdentifier` rejects a name outside its charset: `Parameter name 'STATEMENT_TIMEOUT; DROP TABLE X' is not a valid bare identifier (expected: letters, digits, underscore, starting with a letter)`

**System Errors** (as `*Error`, from `Exec`/`Query`):
- Any failure the `Executor` returns — a compilation error, a permissions error, a network failure, a context deadline — arrives wrapped in `*Error` with `Label` and `Statement` always set, and `Number`/`SQLState`/`QueryID` set when the underlying error is a `*gosnowflake.SnowflakeError`.

## Edge Cases

- **What does `Query` return when the statement matches no rows?** - `Result{}, nil`. A `SHOW ... LIKE` that finds nothing is the routine case for an existence check, not a failure.
- **What happens if `rows.Err()` reports a failure after `rows.Next()` has already returned some rows?** - `Query` returns a `*Error` wrapping that failure, not the rows collected so far. A mid-iteration failure must never look like a smaller, valid result.
- **What if the underlying error isn't a `*gosnowflake.SnowflakeError`?** - `Number`, `SQLState` and `QueryID` stay at their zero values; `Label` and `Statement` are still set, so the decoration degrades gracefully rather than failing to construct.
- **Can bound arguments ever end up in a returned `*Error` or its `Error()` string?** - No, by construction — `Error` has no field for them, and none of this package's code paths reads `args` after passing them to the `Executor`.
- **How does a module run several statements in sequence?** - It calls `Exec`/`Query` once per statement in its own loop and stops at the first non-nil error; this package has no multi-statement call of its own.
- **Does this package decide whether `IDENTIFIER(?)` works for `CREATE ACCOUNT`'s account name, or the policy-name positions of 3.8/3.9?** - No. Those are marked Unconfirmed in `notes-snowflake-sql-mechanics.md` §7 and are left to whichever future module (006 for `CREATE ACCOUNT`; the network/auth modules for 3.8/3.9) emits that statement, at its own spec-writing time.
- **Is a `*Runner` safe for concurrent use?** - Exactly when its `Executor` is — true of a shared `*sql.DB`, not of a shared `*sql.Tx`. `Runner` itself holds no mutable state beyond the `Executor`.

## Dependencies

- `internal/errors` (001) — `BareIdentifier`'s user error.
- `github.com/snowflakedb/gosnowflake` (pinned v1.18.1, added and registered by 004) — this package imports only the `SnowflakeError` type, for error decoration; it registers nothing and opens no connection.

## Integration Points

- **Connection Pool (004)** - `Pool.OrgAdmin`/`Pool.TenantAccount` hand back the `*sql.DB` this package wraps as an `Executor` - Key functions: `statement.New` - Notes: no import in either direction from production code; 004 documents this same rule from its side. `integration_test.go` imports 004 (and 003.a, for the `secrets.Backend` `Pool.TenantAccount` needs) to obtain that real `*sql.DB` under test — a test-only exception, not a production dependency.
- **Account Modules (010–013, 015, 016, 019 — not yet written)** - Call `statement.New` once per connection, then `Exec`/`Query` per statement, reaching for a renderer only at the specific positions their own spec identifies as unbindable - Key functions: `Runner.Exec`, `Runner.Query`, `QuoteIdentifier`, `QuoteLiteral`, `BareIdentifier`.
- **Error Handling (001)** - `logger.Handle`, at the controller layer, classifies and logs whatever this package returns; this package never logs anything itself.
- **Testing** - Module test suites drive the real `statement.New(db)` over `DATA-DOG/go-sqlmock` (`sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual)` for exact statement matching, `.WithArgs(...)` for bind assertions, `mock.ExpectationsWereMet()` for ordering) rather than a hand-rolled fake, exercising the real materializer, renderers and error decoration. `integration_test.go` additionally exercises `Exec`/`Query` against a real `Pool.TenantAccount` connection from the sample tenant account `.env` describes (see `internal/snowflake/pool/integration_test.go` for the same wiring), confirming real `*gosnowflake.SnowflakeError` decoration and real row materialization end to end — skipped under `-short`, run via `make test-integration`.

## Success Criteria

- **SC-001**: `New(exec)` returns a non-nil `*Runner` and makes no call of its own
- **SC-002**: `Exec` returns nil on success
- **SC-003**: `Exec` failure returns a `*Error` whose `Unwrap()` is the `Executor`'s original error
- **SC-004**: `Exec`/`Query` failure against a `*gosnowflake.SnowflakeError` populates `Number`, `SQLState` and `QueryID` from the struct fields directly, not by parsing `Error()`
- **SC-005**: `Exec`/`Query` failure against a non-`SnowflakeError` (e.g. context deadline) leaves `Number`, `SQLState` and `QueryID` at their zero values
- **SC-006**: `*Error.Error()` never includes bound args, even when `Statement` contains placeholders and args were passed
- **SC-007**: `Query` against a statement matching no rows returns `Result{}, nil`
- **SC-008**: `Query` fully materializes `Columns` and `Rows` before returning; no `*sql.Rows` escapes this package
- **SC-009**: A `rows.Err()` failure after partial iteration surfaces as a `*Error`, not a truncated `Result`
- **SC-010**: `QuoteIdentifier` wraps in double quotes and doubles any embedded double quote
- **SC-011**: `QuoteLiteral` wraps in single quotes and doubles any embedded single quote
- **SC-012**: `BareIdentifier` returns its input unchanged when it matches `^[A-Za-z][A-Za-z0-9_]*$`
- **SC-013**: `BareIdentifier` returns a user error (`errors.IsUserError` true) for input containing whitespace, quotes, or other characters outside that charset
- **SC-014**: `*Error.Unwrap()` lets `errors.As` reach the original `*gosnowflake.SnowflakeError` through this package's return value
- **SC-015**: Unit test coverage exceeds 90%

## Security Considerations

- The three renderers re-validate their input independently of whatever validation a calling module already performed — the same defense-in-depth reasoning 003 and 004 apply to their own inputs — so a module's own validation bug does not automatically become an injection bug here.
- Bound arguments never appear in a returned `*Error`, and therefore never in an operator log built from one; only the statement text (safe, since it carries placeholders rather than values) and the driver's structured fields do.
- `BareIdentifier`'s regex is the sole defense at its one call site (the `ALTER ACCOUNT SET <param>` parameter name) and must reject anything containing quotes, whitespace, or SQL metacharacters, since that position accepts neither a bind nor an `IDENTIFIER()`-quoted alternative.

## References

- **Product design**: `specs/design.md` 3.5–3.10 — the account bootstrapping, network, auth and identity SQL this package's callers render and bind.
- **Snowflake SQL mechanics**: `specs/notes-snowflake-sql-mechanics.md` — §1 for the verified `SnowflakeError` field set and `QueryID`/`IncludeQueryID` behavior, §4 for the no-batching rationale, §5 for per-statement idempotency, §7 for the verified/unconfirmed binding and rendering facts this spec builds on.
- **Error Handling**: `specs/001-error-and-logging.md` — `errors.NewUserError`, consumed by `BareIdentifier`.
- **Connection Pooling**: `specs/004-connection-pooling.md` — the `Executor`'s production source (`Pool.OrgAdmin`, `Pool.TenantAccount`) and the two-way import-avoidance rule this spec mirrors.

---

<br/><br/><br/><br/>

## Appendix: Usage Examples

### Example 1: Running an Ordered Statement Sequence (Primary Use Case)

```go
import "github.com/allianz/yukimi/internal/snowflake/statement"

func bootstrap(ctx context.Context, db *sql.DB, accountName, publicKey, region string) error {
    r := statement.New(db)

    if err := r.Exec(ctx, "create account",
        `CREATE ACCOUNT IDENTIFIER(?) ADMIN_NAME = 'platform' ADMIN_RSA_PUBLIC_KEY = ? ADMIN_USER_TYPE = 'SERVICE' EDITION = 'ENTERPRISE' REGION = ?`,
        accountName, publicKey, region); err != nil {
        return err // *statement.Error; stop here, next reconcile resumes
    }

    if err := r.Exec(ctx, "set global parameter",
        `ALTER ACCOUNT SET PREVENT_UNLOAD_TO_INLINE_URL = ?`, "true"); err != nil {
        return err
    }

    return nil
}
```

### Example 2: An Existence Check via `Query` and `QuoteLiteral`

```go
func networkPolicyExists(ctx context.Context, r *statement.Runner, name string) (bool, error) {
    // SHOW's pattern position is rendered, not bound (notes §7).
    sql := `SHOW NETWORK POLICIES LIKE ` + statement.QuoteLiteral(name)

    result, err := r.Query(ctx, "check network policy exists", sql)
    if err != nil {
        return false, err
    }
    return len(result.Rows) > 0, nil
}
```

### Example 3: A Bare Identifier for a Parameter Name

```go
func setAccountParameter(ctx context.Context, r *statement.Runner, param, value string) error {
    // ALTER ACCOUNT SET <param> = <value>: param is keyword-like, so it goes
    // through BareIdentifier rather than IDENTIFIER(?) or a bind (notes §7).
    bare, err := statement.BareIdentifier(param)
    if err != nil {
        return err // user error: operator supplied a bad parameter name
    }

    return r.Exec(ctx, "set account parameter",
        `ALTER ACCOUNT SET `+bare+` = ?`, value)
}
```
