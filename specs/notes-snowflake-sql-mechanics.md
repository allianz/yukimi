> **Verified vendor reference — not a specification.** This file records what was actually confirmed
> about Snowflake SQL semantics and the Go driver, so that specs `005` and the SQL-emitting modules
> (`011`–`013`, `015`, `016`, `019`) draw on one set of findings instead of each re-deriving them.
> It is **not** authoritative product design — `specs/design.md` is, and always wins. It is not a
> scope note either: nothing here proposes what to build, only what the vendor does.
>
> Every claim below is marked **Verified** (with its source) or **Unconfirmed**. An Unconfirmed claim
> may well be true — it is widely believed and matches observed behaviour — but no documentation was
> found for it, so no spec should depend on it without checking first. Keep this file as long as any
> consumer spec is unwritten; fold the relevant parts into each spec as it lands.
>
> Driver facts were read from **gosnowflake v1.18.1** source. A driver upgrade invalidates them.

## 1. The driver's error type

**Verified** (gosnowflake v1.18.1 `errors.go`). `SnowflakeError` has exactly six fields:

```go
type SnowflakeError struct {
	Number         int
	SQLState       string
	QueryID        string
	Message        string
	MessageArgs    []interface{}
	IncludeQueryID bool // TODO: populate this in connection
}
```

There is **no** `ErrorCode` field and **no** position/line/offset field. `Error()` formats as
`"%06d (%s): %s"` over `Number`, `SQLState`, and the message.

**Verified — leading zeros are lost.** The wire format carries the code as a *string*
(`execResponse.Code string`, `query.go`), and `connection.go` runs it through `strconv.Atoi`. Server
code `"002003"` therefore arrives as `Number == 2003`. The zero padding only reappears in `Error()`
via `%06d`. Any code comparison must use `2003`, not `002003` — a classifier written against the
padded form silently never fires.

**Verified — `QueryID` must be read from the field, not the message.** `populateErrorFields` copies
only `Number`, `SQLState`, `Message`, and `QueryID`; it does not set `IncludeQueryID`, so the `TODO`
in the struct is live and a failed statement's `Error()` string omits the query ID even though the
field is populated. `QueryID` is the useful correlation handle for operators — it looks the statement
up in Snowflake's own query history.

**Verified — exported constants are almost all client-side.** Ranges `260000`–`279301` cover DSN,
transport, auth, OCSP, and query-status conditions. Only three server-side ("GS") codes have exported
constants: `390111 ErrSessionGone`, `390189 ErrRoleNotExist`, `390201 ErrObjectNotExistOrAuthorized`.
Note that `390201` is a **session/GS-level** code and is *not* the same condition as the SQL
compilation "does not exist or not authorized" returned by a failed DDL statement; it must not be
used for statement-level classification.

**Verified — async mode hides the server code.** With `WithAsyncMode`, a failed query returns
`Number = 279001 (ErrQueryStatus)` and stuffs the real server code into the message text
(`"server ErrorCode=%s, ErrorMessage=%s"`), or `Number = 279201`. Any `Number`-based logic breaks
under async execution.

## 2. Error position

**Verified — position exists, but only as prose.** Snowflake reports position in the error message,
for example `"at line 1, position 75"`
(https://docs.snowflake.com/en/developer-guide/sql-api/submitting-multiple-statements). The driver's
wire model has no structured position field, so recovering it means regex over `Message`.

**Unconfirmed** — the exact canonical wording of a syntax error (the widely-seen
`"syntax error line 1 at position 12 unexpected '...'"` shape has no citation found), and whether
position is present on non-syntax errors such as "does not exist or not authorized" or "insufficient
privileges". Do not write logic that requires a position to be present.

## 3. There is no official error-code reference

**Verified — none of the Snowflake documentation enumerates error codes.**

- https://docs.snowflake.com/en/developer-guide/sql-api/handling-errors — documents HTTP 408/422 and
  the existence of a query-failure status; no code list.
- https://docs.snowflake.com/en/developer-guide/sql-api/handling-responses — shows the response shape
  (`code`, `sqlState`, `message`, `statementHandle`) and the success code `"090001"`; no enumeration.
- https://docs.snowflake.com/en/developer-guide/snowflake-scripting/exceptions — documents `SQLCODE`,
  `SQLERRM`, and `SQLSTATE` ("modeled on the ANSI SQL standard SQLSTATE", with additional
  Snowflake values) and explicitly does not list them.

**Unconfirmed — the meanings of `001003`, `002002`, `002003`, `003001`.** These circulate widely as
"syntax error", "object already exists", "object does not exist", and "insufficient privileges"
respectively, and they have been consistent in observed behaviour for years. No official source was
found. Snowflake publishes no compatibility promise on them, so they are not a contract.

Consequence for this platform: a classifier keyed on these codes rests on undocumented behaviour.
Where a module needs to know whether an object exists, a `SHOW … LIKE` query (§6) answers the
question directly instead of inferring it from an error.

## 4. Multi-statement execution

**Verified — the driver mechanism.** `WithMultiStatement(ctx, num)` sets the `MULTI_STATEMENT_COUNT`
request parameter; statements are semicolon-separated in one string, and `num = 0` permits a variable
count. Available since driver 1.3.8. The count is documented as an anti-SQL-injection guard.

**Verified — no per-statement attribution.** `ExecContext` returns "the sum of the number of rows
changed by each individual statement… Individual row counts for individual statements are not
available" (gosnowflake `doc.go`). `QueryContext` returns multiple result sets, walked with
`NextResultSet()`.

**Verified — fail-fast with partial commit.** "The statements before the statement with the error are
executed successfully… The statements after the statement with the error are not executed"
(https://docs.snowflake.com/en/developer-guide/sql-api/submitting-multiple-statements). Earlier
statements are committed and are not rolled back.

**Verified — DDL cannot be rolled back at all.** "Each DDL statement executes as a separate
transaction" and "Because a DDL statement is its own transaction, you cannot roll back a DDL
statement" (https://docs.snowflake.com/en/sql-reference/transactions). A DDL statement inside an open
transaction implicitly commits it. Any undo must be a compensating `DROP`, which for `CREATE ACCOUNT`
is not available on a normal timeline (accounts enter a scheduled-drop grace period).

**Verified — identifying the failing statement is textual.** The docs direct you to the character
position embedded in the message; there is no structured statement-index field. The driver's
`handleMultiExec` does build per-child errors carrying each child's `QueryID`, but only while
iterating the children of an otherwise-successful batch — a mid-batch failure fails the top-level
request and never reaches that loop.

Consequence: batching DDL buys no atomicity, because there is none to gain, and costs exact failure
attribution. One statement per call gives unambiguous attribution and a precise resume point.

## 5. Idempotency, statement by statement

All rows **Verified** against the individual `docs.snowflake.com/en/sql-reference/sql/*` pages.

| Statement | `OR REPLACE` | `IF NOT EXISTS` | Notes |
| --- | --- | --- | --- |
| `CREATE ACCOUNT` | No | No | Neither clause exists. Not idempotent; no safe retry. |
| `CREATE NETWORK RULE` | Yes | No | `CREATE OR ALTER NETWORK RULE` exists and is the idempotent form. |
| `CREATE NETWORK POLICY` | Yes | Yes | `CREATE OR ALTER` also available. See the attachment hazard below. |
| `CREATE USER` | Yes | Yes | No `CREATE OR ALTER USER`. |
| `CREATE RESOURCE MONITOR` | Yes | Yes | `CREATE [OR REPLACE] RESOURCE MONITOR [IF NOT EXISTS] <name> WITH …` |
| `CREATE SECURITY INTEGRATION` | Yes | Yes | No `CREATE OR ALTER` variant. |
| `ALTER NETWORK POLICY … ADD ALLOWED_NETWORK_RULE_LIST` | n/a | `IF EXISTS` on `SET`/`UNSET` only, not on `ADD`/`REMOVE`/`RENAME` | Duplicate-add behaviour **Unconfirmed**. |
| `ALTER ACCOUNT SET <param>` | n/a | No | Naturally idempotent — declarative assignment. |
| `ALTER USER … SET NETWORK_POLICY` | n/a | `ALTER USER [IF EXISTS]` | Naturally idempotent. |
| `ALTER ACCOUNT ADD ORGANIZATION USER GROUP` | n/a | No | Syntax confirmed present; repeat behaviour **Unconfirmed**. |
| `GRANT ROLE` | n/a | n/a | Re-grant behaviour **Unconfirmed** — see below. |

**Verified — `OR REPLACE` and `IF NOT EXISTS` are mutually exclusive** on every statement above, so
the two cannot be combined as a hedge.

**Verified — the network-policy attachment hazard.** "You cannot execute a `CREATE OR REPLACE NETWORK
POLICY` command to replace an existing network policy if that policy is currently assigned to an
account, security integration, or user." The same restriction applies to `CREATE OR ALTER`. So
`OR REPLACE` is not a usable idempotency strategy for an attached policy: it hard-fails. Detaching
first opens a window in which the account or user has no network policy at all — a security
regression, not merely an availability one. This matters directly for `PLATFORM_ACCOUNT_POLICY` and
the per-service-user policies, which are attached and then revisited on every reconcile.

**Verified — a warning that is often misread.** The `ALTER NETWORK POLICY` docs note that `SET` for
the allowed/blocked lists "is not additive (that is, it removes all IP addresses in the existing
lists … and replaces them with the specified lists)". That is a *destructive-versus-additive*
warning, not a non-idempotency warning: re-running the same `SET` converges. The real hazard is using
`SET` where `ADD` was meant and silently wiping entries another process added.

**Unconfirmed — `GRANT ROLE` on an already-granted role.** Widely treated as a no-op success, which
is why `GRANT` is generally considered idempotent, but the documentation page does not say so.

Practical shape: prefer `IF NOT EXISTS` for create-once semantics, `CREATE OR ALTER` where it exists,
and `ALTER … SET` for convergence. Reserve `OR REPLACE` for objects known to be unattached.

## 6. Checking whether an object exists

**Verified — `SHOW … LIKE` is the right tool for account-level objects.** SHOW commands are "Not
required to execute" a warehouse, whereas `INFORMATION_SCHEMA` views require a running warehouse
(https://docs.snowflake.com/en/sql-reference/info-schema). SHOW also filters case-insensitively with
`LIKE`.

**Verified — `ACCOUNT_USAGE` is disqualified for read-after-write, twice over.** Most views carry
**two hours** of latency, the rest 45 minutes to 3 hours, and "account usage views include records
for all objects that have been dropped"
(https://docs.snowflake.com/en/sql-reference/account-usage) — so an existence check there can both
miss a just-created object and report a deleted one as present.

**Verified — `INFORMATION_SCHEMA` has no latency** ("views/table functions in the Snowflake
Information Schema do not have any latency"), but it is database-scoped.

**Unconfirmed** — whether `INFORMATION_SCHEMA` exposes users, network policies, or network rules at
all. It is believed not to, these being account-level rather than database-scoped objects, which
would leave `SHOW … LIKE` as the only real-time option for them. Worth a quick check before relying
on it.

**Verified by construction** — SHOW is not a lock. A check-then-create leaves a TOCTOU window;
`IF NOT EXISTS` and `CREATE OR ALTER` push atomicity into the server and are strictly better as
guards. Use SHOW for reconciliation and reporting, not as a mutual-exclusion primitive.

## 7. Parameter binding

**Verified — object names *can* be bound, via `IDENTIFIER()`.** "You can use literals and variables
(session or bind) in some cases when you need to identify an object by name (queries, DML, DDL, and so
on)", and "you can use bind variables for object identifiers and bind variables for values in the same
query" (https://docs.snowflake.com/en/sql-reference/identifier-literal). Accepted forms are `?`, `:name`,
session variables, string literals, and Snowflake Scripting variables. So `ALTER USER IDENTIFIER(?) SET
NETWORK_POLICY = ?` is the shape to try before interpolating anything. Note `IDENTIFIER()` "isn't a
true function" — it is resolved at parse time.

**Verified — binds are excluded from specific DDL, by statement and by clause**
(https://docs.snowflake.com/en/sql-reference/bind-variables). Prohibited in `CREATE/ALTER INTEGRATION`,
`CREATE/ALTER REPLICATION GROUP`, `CREATE/ALTER PIPE`, `CREATE TABLE … USING TEMPLATE`; in the
`ALTER COLUMN` and `COMMENT ON CONSTRAINT` clauses; and in the `CREDENTIALS`, `DIRECTORY`, `ENCRYPTION`,
`IMPORTS`, `PACKAGES`, `REFRESH` and `TAG` parameter values. Also "bind variables can't replace numbers
that are part of a data type definition (for example, `NUMBER(?)`) or collation specification".

**Inferred, not stated — the integration exclusion hits this platform.** The SSO
`CREATE SECURITY INTEGRATION` of §3.9 is an integration, so the `CREATE/ALTER INTEGRATION` prohibition
should be assumed to apply to it. The docs name the family, not each variant; confirm before relying
either way.

**Unconfirmed — where `IDENTIFIER()` is *not* accepted.** The documented context list is illustrative
(`CREATE`, `SELECT`/`FROM`, `INSERT`, `DROP`, `USE SCHEMA`, `SHOW TABLES`), not exhaustive, and no list
of unsupported contexts is published. Specifically unverified for this design: the account name in
`CREATE ACCOUNT` (§3.6), and the policy-name *value* positions in `ALTER USER … SET NETWORK_POLICY`
(§3.8) and `ALTER USER … SET AUTHENTICATION_POLICY` (§3.9). Each needs a live check.

**Unconfirmed — a parameter name cannot be bound or quoted.** In `ALTER ACCOUNT SET <param> = <value>`
the parameter name is a keyword-like token rather than an object name, so `IDENTIFIER()` does not apply
and `ALTER ACCOUNT SET "STATEMENT_TIMEOUT_IN_SECONDS" = …` is believed invalid. No citation found for
either half. If both hold, a charset-validated bare identifier is the only available defense for the
arbitrary operator-supplied parameter names of §3.5.

## References

- **Product design**: `specs/design.md` — authoritative for what this platform does; this file only
  records what the vendor does.
- **Driver source**: gosnowflake v1.18.1 — `errors.go`, `query.go`, `connection.go`, `util.go`,
  `multistatement.go`, `monitoring.go`, `doc.go`.
- Snowflake documentation URLs are cited inline above.
