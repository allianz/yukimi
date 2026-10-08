# Specification: Statement Execution (005)

This specification covers the package: `internal/snowflake/statement/`.

## Overview

This package is the platform's single path for executing SQL against Snowflake. It protects against SQL injection by ensuring that tenant-supplied input cannot alter the meaning of a statement. It also owns the lifecycle of query results, so callers never hold an open cursor and cannot leak connections. Callers receive fully loaded results that can be searched in memory, and any failure identifies the exact statement that caused it.

## Key Concept: Bind Inputs Where Snowflake Allows

Snowflake usually accepts input as a bound parameter, separate from the SQL command, so that input cannot change the command's meaning. Its documentation does not reliably identify every place binding works, but tests against a live account show it works in nearly all positions checked so far. When Snowflake does not accept a bound parameter, this package provides helpers that safely quote or validate the input before it becomes part of the command text.

| Statement | Approach | Example |
|---|---|---|
| `CREATE ACCOUNT` | Bound parameters: Snowflake accepts binds here | `r.Exec(ctx, "create account", "CREATE ACCOUNT IDENTIFIER(?) ADMIN_NAME = ?", name, admin)` |
| `ALTER ACCOUNT SET` | Rendered text: Snowflake rejects binds here, so a helper quotes the value | `r.Exec(ctx, "set timezone", "ALTER ACCOUNT SET TIMEZONE = "+statement.QuoteLiteral(tz))` |

## Key Concept: No Transactions — Callers Make Their SQL Idempotent

Snowflake commits every DDL statement immediately and cannot roll it back, so this package offers no transactions and runs exactly one statement per call. A caller that needs several statements runs them in order and stops at the first failure, which identifies exactly which statement failed. Statements that already succeeded stay applied. Callers therefore write idempotent SQL, so the next attempt completes a partially applied sequence instead of repeating it.

## Key Concept: Fully Loaded Results, Searched in Memory

A query reads every row into memory before it returns, and the connection is released straight away. Callers therefore never hold an open cursor: they cannot forget to close it and leak a pooled connection, and they cannot mistake a read that failed halfway through for an empty result. The queries in this design are small existence checks and read-backs of a few rows, so keeping the whole result in memory costs nothing.

The result is an in-memory table of rows and named columns. Reading a single cell from it by hand means looping over the rows, comparing a column on each, and then pulling the value out of the matching row — boilerplate that every query would otherwise repeat. Helpers replace it, for instance to retrieve a specific row and read a column value from it. For example, looking up an account's locator:

```go
result, err := r.Query(ctx, "show accounts", "SHOW ACCOUNTS LIKE ?", "TEAM_A")
locator, ok := result.FindRow("account_name", "TEAM_A").StringValue("account_locator")
```

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

// Row is one materialized row, keyed by column name as the driver reported
// it. A nil Row means "no such row": every accessor below returns its zero
// value and false on it, so lookups chain without intermediate checks.
// Because Row is a map, callers may still index or range over it directly.
type Row map[string]any

// Result is a materialized row-returning query result.
type Result struct {
    Columns []string
    Rows    []Row
}

// FindRow returns the first row whose column holds a string equal to value,
// or nil if there is none. Both the column name and value are compared
// ignoring case (the way Snowflake treats unquoted identifiers), so
// FindRow("name", "MY_POLICY") finds a row whose NAME is "my_policy". It is
// the exact-match step every SHOW ... LIKE check needs, since LIKE treats
// "_" as a wildcard. It never returns an error: if several rows match
// ignoring case, the first wins.
func (r Result) FindRow(column, value string) Row

// Found reports whether the row exists, i.e. whether FindRow matched.
func (r Row) Found() bool

// StringValue returns the column's value if it is a string. The column name
// is matched ignoring case. ok is false for a nil Row, a missing column, a
// NULL, or a non-string value; an empty string is returned as ("", true), so
// a caller that treats empty as absent adds its own `&& value != ""`.
func (r Row) StringValue(column string) (value string, ok bool)

// Value returns the column's raw value, for types StringValue does not
// cover (e.g. a timestamp). Same column-name matching and nil-Row behavior
// as StringValue; ok is false only for a nil Row or a missing column — a
// NULL is (nil, true).
func (r Row) Value(column string) (value any, ok bool)

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
// doubling any embedded double quote. Binding the name via IDENTIFIER(?)
// must be tried first; use this only once an integration test shows that
// bound attempt failing against live Snowflake (see Verified Bind
// Positions). No such position is confirmed yet.
func QuoteIdentifier(name string) string

// QuoteLiteral single-quotes s for use as a rendered SQL string literal,
// doubling any embedded single quote. Binding the value with ? must be
// tried first; use this only once an integration test shows that bound
// attempt failing against live Snowflake (see Verified Bind Positions).
func QuoteLiteral(s string) string

// BareIdentifier validates name as a bare, unquoted SQL token and returns
// it unchanged, or a user error if it does not match the expected charset.
// Its one confirmed caller is the parameter name in ALTER ACCOUNT SET <param>
// = <value>: an integration test shows that both ? and IDENTIFIER(?) fail
// there with a syntax error. That position is keyword-like rather than a true
// object name, so this check is the only defense against an operator-supplied
// parameter name reaching SQL text unescaped. Like the other renderers, it
// must not be used at any position without such a failing test.
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
├── statement.go         # Executor, Runner, New, Exec, Query
├── statement_test.go    # sqlmock-driven tests
├── result.go            # Result, Row, FindRow, Found, StringValue, Value
├── result_test.go
├── integration_test.go  # live-Snowflake test via a real 004 Pool.TenantDB connection
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

<br/><br/><br/><br/><br/>

================

## Appendix: Code Generation Details

## Scope

This specification defines the `internal/snowflake/statement/` package that:
- Executes SQL against an **injected `Executor`** — the subset of `*sql.DB` this package needs (`ExecContext`, `QueryContext`). It never opens a connection itself and never imports `internal/snowflake/pool` (004); 004 documents the mirror-image rule and never imports this package either.
- Offers **two execution paths**: `Exec`, for statements that return no rows (DDL and non-`SELECT` DML), and `Query`, for statements that do (`SHOW ... LIKE` existence checks, drift read-backs). Which path a given statement uses is each calling module's decision, not this package's.
- **Materializes every row-returning result** before returning it: column names plus rows keyed by column name, values as `any`.
- **Offers chainable, error-free lookups on that result**: find a row by a string column's value, then read another column by name. A missing row is a nil `Row`, not an error. Column names and the searched value match ignoring case. Only string reads are provided; anything else is read raw and converted by the caller.
- **Binds first, renders only where binding is impossible.** Supplies three rendering primitives — a quoted identifier, a quoted string literal, and a charset-validated bare identifier — for the small set of statement positions where an integration test has proven that binding fails.
- **Runs statements in order, one per call, and stops on the first error.** No batching, no multi-statement execution, no rollback.
- **Decorates a failure with structured fields only**: a caller-supplied label, the statement text, and — when the underlying error is a `*gosnowflake.SnowflakeError` — its `Number`, `SQLState`, and `QueryID`. Never the bound arguments.

**Out of Scope**:
- **Which SQL to emit, and in what order, for any given operation.** That is every downstream module's business (012–015, 017, 018, 021), not this package's.
- **Running the bind tests for a given statement.** This package supplies the rendering primitives and the policy that governs their use; the module that emits each statement owns the failing integration test that justifies rendering one of its positions, next to the code that renders it. Positions not yet checked — the policy-name values in `ALTER USER ... SET NETWORK_POLICY` (3.8) and `ALTER USER ... SET AUTHENTICATION_POLICY` (3.9) — are bound until that module's test shows otherwise.
- **Type coercion on the materialized `Result`** beyond string reads — no integer, boolean or timestamp accessors until a module actually needs one. Callers read the raw value and convert it themselves.
- **Retries.** No retry logic lives in this package or anywhere in this codebase's business logic; a failure is returned as-is and the caller (ultimately Kubernetes/Crossplane, per project-wide policy) decides whether to try again.
- **Connections, credentials, and pooling** — entirely 004's job. This package accepts an already-open `Executor` and never asks how it got that way.

## Verified Bind Positions

Results of live probes against the sample organization (2026-10-08), as run by the account module's integration tests. "Binds" means the bound form executed successfully; "fails" is the server's rejection, which is what justifies a renderer.

| Statement position | Form tried | Outcome |
|---|---|---|
| `CREATE ACCOUNT` name | `IDENTIFIER(?)` | Binds |
| `CREATE ACCOUNT` name | `?` | Fails: `1003` / `42000` syntax error |
| `CREATE ACCOUNT` `ADMIN_NAME`, `ADMIN_RSA_PUBLIC_KEY`, `EMAIL`, `REGION`, `COMMENT` | `?` | Binds (`COMMENT` alters backslashes, see below) |
| `SHOW ... LIKE` pattern (`SHOW ACCOUNTS`, `SHOW USERS`, `SHOW PARAMETERS`) | `?` | Binds |
| `SHOW ... LIKE` pattern | `IDENTIFIER(?)` | Fails: `1003` / `42000` syntax error |
| `ALTER USER <name> SET EMAIL = <value>`, name and value | `IDENTIFIER(?)` and `?`, separately and together | Binds |
| `ALTER USER <name> SET RSA_PUBLIC_KEY[_2] = <key>`, name and key | `IDENTIFIER(?)` and `?` | Binds |
| `ALTER USER <name> SET <slot> = <key>`, slot name | `?` | Fails: `1003` / `42000` syntax error |
| `DESC USER <name>` | `IDENTIFIER(?)` | Binds |
| `DROP ACCOUNT IF EXISTS <name> ...`, name | `IDENTIFIER(?)` | Binds; the account is dropped |
| `DROP ACCOUNT ... GRACE_PERIOD_IN_DAYS = <n>`, days | `?` | Fails: `1003` / `42000` syntax error |
| `ALTER ACCOUNT SET <param> = <value>`, value | `?` (integer and boolean parameters; string and `bool` args) | Fails: `1008` / `22023` `invalid value [?] for parameter ...` |
| `ALTER ACCOUNT SET <param>`, parameter name | `?` and `IDENTIFIER(?)` | Fails: `1003` / `42000` syntax error |

Consequences: `CREATE ACCOUNT`, every `SHOW ... LIKE`, `DESC USER`, `ALTER USER ... SET EMAIL` and the key-rotation `ALTER USER` bind throughout. Rendering is justified only for the `ALTER ACCOUNT SET` parameter name and value, the `DROP ACCOUNT` grace period, and the key slot name in `ALTER USER ... SET <slot>`. Snowflake's own documentation excludes the `CREATE/ALTER INTEGRATION` family from binds, which is why `CREATE SECURITY INTEGRATION` is rendered too; that exclusion should still be backed by a test when the first such statement is written. Parenthesized placeholders such as `(?)` are not a supported bind form and are not probed.

### Bound string values are not stored verbatim

A bind that executes is not necessarily lossless. Snowflake interprets backslash escape sequences and quote doubling inside a bound string value, so the stored value can differ from the one sent. Compared against the same value rendered through `QuoteLiteral` (which doubles backslashes), using `ALTER USER ... SET COMMENT` and `CREATE ACCOUNT ... COMMENT`:

| Value sent | Stored when bound with `?` |
|---|---|
| a single `'`, `"`, `--`, `/* */`, `;`, Unicode, a trailing `\` | unchanged |
| `a''b` (doubled single quote) | `a'b` (one quote) |
| `a\b` | `a` + backspace + `b` |
| `a\nb` | `a` + newline + `b` |
| `a\ b` | `a b` (backslash dropped) |
| `a\\b` | `a\b` (one backslash) |

**Decision: security takes priority over fidelity.** A bound value can never leave its position or change the statement's structure, whereas a rendered one depends on escaping being right. Free-text values (an account description, a contact) are therefore bound, and the alteration of backslash sequences and doubled quotes is accepted and documented rather than avoided by rendering. Modules that bind free text must not rely on it round-tripping byte for byte when it contains a backslash or a doubled single quote.

## Edge Cases

- **What does `Query` return when the statement matches no rows?** - `Result{}, nil`. A `SHOW ... LIKE` that finds nothing is the routine case for an existence check, not a failure.
- **What happens if `rows.Err()` reports a failure after `rows.Next()` has already returned some rows?** - `Query` returns a `*Error` wrapping that failure, not the rows collected so far. A mid-iteration failure must never look like a smaller, valid result.
- **What if the underlying error isn't a `*gosnowflake.SnowflakeError`?** - `Number`, `SQLState` and `QueryID` stay at their zero values; `Label` and `Statement` are still set, so the decoration degrades gracefully rather than failing to construct.
- **Can bound arguments ever end up in a returned `*Error` or its `Error()` string?** - No, by construction — `Error` has no field for them, and none of this package's code paths reads `args` after passing them to the `Executor`.
- **What does `FindRow` return on an empty `Result`, or when no row matches?** - A nil `Row`. `Found` is false on it and `StringValue`/`Value` return their zero value and false, so the chain never needs an intermediate check.
- **Why not just test `len(result.Rows) > 0` after a `SHOW ... LIKE`?** - `LIKE` treats `_` as a wildcard, so `LIKE 'tenant_a'` also returns `tenantXa`. `FindRow` performs the exact, case-insensitive match that existence checks actually need.
- **What if several rows match `FindRow` ignoring case?** - The first wins. It is not an error, which keeps the chain error-free; quoted identifiers that differ only by case are not expected in this design.
- **What does `StringValue` return for a NULL, a missing column or a non-string value?** - `("", false)` for all three. An empty string is `("", true)`; a caller that treats empty as absent adds its own check.
- **Does `Value` distinguish a NULL from a missing column?** - Yes: a NULL is `(nil, true)`, a missing column or nil `Row` is `(nil, false)`.
- **Does column-name matching depend on the casing the driver reports?** - No. An exact key is tried first, then a case-insensitive scan, so `SHOW` output reported as `account_name` or `ACCOUNT_NAME` is read the same way.
- **How does a module run several statements in sequence?** - It calls `Exec`/`Query` once per statement in its own loop and stops at the first non-nil error; this package has no multi-statement call of its own.
- **May a module render a position because the documentation, or a similar statement, suggests it cannot be bound?** - No. The bound form is tried first; only its failure in an integration test for that exact statement justifies a renderer.
- **What counts as a failing test?** - One that runs the bound form against live Snowflake and asserts the server's own rejection (error number and SQL state, ideally a message fragment). A test that fails for an unrelated reason — a wrong region, a rejected email — proves nothing about binding, so every other part of the statement in the test must be valid.
- **Does `IDENTIFIER(?)` work for `CREATE ACCOUNT`'s account name?** - Yes; see Verified Bind Positions. The account name is bound, not rendered.
- **Is a `*Runner` safe for concurrent use?** - Exactly when its `Executor` is — true of a shared `*sql.DB`, not of a shared `*sql.Tx`. `Runner` itself holds no mutable state beyond the `Executor`.

## Dependencies

- `internal/errors` (001) — `BareIdentifier`'s user error.
- `github.com/snowflakedb/gosnowflake/v2` (added and registered by 004) — this package imports only the `SnowflakeError` type, for error decoration; it registers nothing and opens no connection.

## Integration Points

- **Connection Pool (004)** - `Pool.OrgAdminDB`/`Pool.TenantDB` hand back the `*sql.DB` this package wraps as an `Executor` - Key functions: `statement.New` - Notes: no import in either direction from production code; 004 documents this same rule from its side. `integration_test.go` imports 004 (and 003.a, for the `*secrets.KeyManager` `Pool.TenantDB` needs) to obtain that real `*sql.DB` under test — a test-only exception, not a production dependency.
- **Account Modules (012–015, 017, 018, 021 — not yet written)** - Call `statement.New` once per connection, then `Exec`/`Query` per statement, reaching for a renderer only at positions a failing integration test in their own package has proven unbindable - Key functions: `Runner.Exec`, `Runner.Query`, `QuoteIdentifier`, `QuoteLiteral`, `BareIdentifier`.
- **Error Handling (001)** - `logger.Handle`, at the controller layer, classifies and logs whatever this package returns; this package never logs anything itself.
- **Testing** - Module test suites drive the real `statement.New(db)` over `DATA-DOG/go-sqlmock` (`sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual)` for exact statement matching, `.WithArgs(...)` for bind assertions, `mock.ExpectationsWereMet()` for ordering) rather than a hand-rolled fake, exercising the real materializer, renderers and error decoration. `integration_test.go` additionally exercises `Exec`/`Query` against a real `Pool.TenantDB` connection from the sample tenant account `.env` describes (see `internal/snowflake/pool/integration_test.go` for the same wiring), confirming real `*gosnowflake.SnowflakeError` decoration and real row materialization end to end — skipped under `-short`, run via `make test-integration`.

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
- **SC-015**: `FindRow` returns the first row whose column holds a string equal to the value, ignoring case in both the column name and the value
- **SC-016**: `FindRow` returns a nil `Row` on an empty `Result`, and when only a `LIKE`-style near match exists (e.g. searching `tenant_a` when the only row is `tenantXa`)
- **SC-017**: `Found` is true for a matched row and false for a nil `Row`
- **SC-018**: `StringValue` matches the column name ignoring case, and returns `("", false)` for a nil `Row`, a missing column, a NULL and a non-string value
- **SC-019**: `StringValue` returns `("", true)` for a column holding an empty string
- **SC-020**: `Value` returns the raw value and true for a present column (a NULL as `(nil, true)`), and `(nil, false)` for a nil `Row` or a missing column
- **SC-021**: A chain such as `result.FindRow(...).StringValue(...)` on a `Result` with no match returns `("", false)` without panicking
- **SC-022**: Unit test coverage exceeds 90%
- **SC-023**: Every production call site of `QuoteIdentifier`, `QuoteLiteral` or `BareIdentifier` has an integration test, in the calling module's package, that first sends the bound form of that statement to live Snowflake and asserts the server's rejection

## Security Considerations

- The three renderers re-validate their input independently of whatever validation a calling module already performed — the same defense-in-depth reasoning 003 and 004 apply to their own inputs — so a module's own validation bug does not automatically become an injection bug here.
- Bound arguments never appear in a returned `*Error`, and therefore never in an operator log built from one; only the statement text (safe, since it carries placeholders rather than values) and the driver's structured fields do.
- `BareIdentifier`'s regex is the sole defense at its one call site (the `ALTER ACCOUNT SET <param>` parameter name) and must reject anything containing quotes, whitespace, or SQL metacharacters, since an integration test shows that position accepts neither a bind nor an `IDENTIFIER()` alternative.

## References

- **Product design**: `specs/design.md` 3.5–3.10 — the account bootstrapping, network, auth and identity SQL this package's callers render and bind.
- **Error Handling**: `specs/001-error-and-logging.md` — `errors.NewUserError`, consumed by `BareIdentifier`.
- **Connection Pooling**: `specs/004-connection-pooling.md` — the `Executor`'s production source (`Pool.OrgAdminDB`, `Pool.TenantDB`) and the two-way import-avoidance rule this spec mirrors.

---

<br/><br/><br/><br/>

## Appendix: Usage Examples

### Example 1: Running an Ordered Statement Sequence (Primary Use Case)

```go
import "github.com/allianz/yukimi/internal/snowflake/statement"

func bootstrap(ctx context.Context, db *sql.DB, accountName, publicKey, region string) error {
    r := statement.New(db)

    // CREATE ACCOUNT binds everywhere, including the name via IDENTIFIER(?).
    if err := r.Exec(ctx, "create account",
        `CREATE ACCOUNT IDENTIFIER(?) ADMIN_NAME = ? ADMIN_RSA_PUBLIC_KEY = ? ADMIN_USER_TYPE = SERVICE EDITION = ENTERPRISE REGION = ?`,
        accountName, "platform", publicKey, region); err != nil {
        return err // *statement.Error; stop here, next reconcile resumes
    }

    return nil
}
```

### Example 2: An Existence Check via `Query` and a Bound `LIKE`

```go
func networkPolicyExists(ctx context.Context, r *statement.Runner, name string) (bool, error) {
    // SHOW's pattern position binds (see Verified Bind Positions).
    result, err := r.Query(ctx, "check network policy exists",
        `SHOW NETWORK POLICIES LIKE ?`, name)
    if err != nil {
        return false, err
    }
    // Not len(result.Rows) > 0: LIKE treats "_" as a wildcard, so a row can
    // come back that is not an exact match for name.
    return result.FindRow("name", name).Found(), nil
}
```

### Example 3: Rendering Where Binding Is Proven Impossible

```go
func setAccountParameter(ctx context.Context, r *statement.Runner, param, value string) error {
    // ALTER ACCOUNT SET <param> = <value>: both positions are proven
    // unbindable (see Verified Bind Positions), so both are rendered.
    bare, err := statement.BareIdentifier(param)
    if err != nil {
        return err // user error: operator supplied a bad parameter name
    }

    // Whether the server accepts a quoted literal for every parameter type
    // (integer, boolean) is for the parameter module (013) to confirm.
    return r.Exec(ctx, "set account parameter",
        `ALTER ACCOUNT SET `+bare+` = `+statement.QuoteLiteral(value))
}
```

### Example 4: Reading a Value from a Matched Row

```go
func accountLocator(ctx context.Context, r *statement.Runner, resolvedName string) (string, bool, error) {
    result, err := r.Query(ctx, "look up account",
        `SHOW ACCOUNTS LIKE ?`, resolvedName)
    if err != nil {
        return "", false, err
    }

    // One chained expression: no loop, no per-step error. No matching row
    // simply yields ("", false).
    locator, ok := result.FindRow("account_name", resolvedName).StringValue("account_locator")
    return locator, ok && locator != "", nil
}
```

### Example 4: Reading a Value from a Matched Row

```go
func accountLocator(ctx context.Context, r *statement.Runner, resolvedName string) (string, bool, error) {
    result, err := r.Query(ctx, "look up account",
        `SHOW ACCOUNTS LIKE `+statement.QuoteLiteral(resolvedName))
    if err != nil {
        return "", false, err
    }

    // One chained expression: no loop, no per-step error. No matching row
    // simply yields ("", false).
    locator, ok := result.FindRow("account_name", resolvedName).StringValue("account_locator")
    return locator, ok && locator != "", nil
}
```
