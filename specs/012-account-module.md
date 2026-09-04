# Specification: Account Module (012)

## Overview

This module creates a brand-new Snowflake account for a tenant and sets up the one service user the
platform uses to manage it afterward. Nothing else in the pipeline that needs a live Snowflake
connection can run until an account actually exists and the platform can log into it — a module needing
no such connection (the quota-check admission gate, 011) may still run earlier. It is needed because
creating an account requires organization-wide privileges that no other part of the system should hold.
The approach is simple: generate a fresh login key, save it safely first, ask Snowflake to create the
account with that key, and then remember the account's unique ID so every later step can find it again.

## Scope

This specification defines the account module that:
- Generates and stores the `platform` service user's RSA keypair, create-only.
- Issues `CREATE ACCOUNT` over the org-admin connection and captures the returned account locator.
- Detects, on every reconcile, whether the account already exists — the pipeline's sole existence signal.
- Publishes the resolved account name and locator onto the shared `ModuleContext` for every later module.
- Tears the account down: `DROP ACCOUNT` over the org-admin connection, eviction of the pooled
  connection to it, and deletion of the stored credential.

**Out of Scope**:
- Authorizing a deletion (019), and the finalizer and conditions around one (020).
- `IdentitySyncRequest` emission (017).
- Drift detection or repair of the account's own parameters or the `platform` key — not until Snowflake
  ships Organization Policies (design.md Appendix B).
- Validating the region's `available` gate (020's validation phase).

## Key Concept: Create-Then-Verify Lifecycle

A tenant's Snowflake account is created exactly once. Before doing anything else, this module checks
whether that account already exists; if it does, the module never creates another and never touches the
stored credential again — it only re-confirms that the platform can still log in. An account that has
gone missing or become unreachable outside the platform's own deletion flow is reported as a problem, not
treated as a reason to try creating it again: doing so could orphan a live account or replace a key it
still depends on.

`Done()` means "verified reachable," not merely "the SQL succeeded." A fresh `CREATE ACCOUNT` therefore
never verifies reachability inline: a Snowflake account takes minutes to become connectable after
`CREATE ACCOUNT` returns, so trying to connect in the same pass — which is what would happen next, since
every later pipeline module needs `TenantDB` — would just produce a predictable string of connection
failures. Instead, a fresh create records the locator and the moment of creation directly on the CRD's
status and returns `Pending(...).Aborting()`, stopping the pipeline for this pass and deferring the first
reachability check to a later reconcile. Both `Observe` and `Apply`'s reconnect path skip attempting a
connection entirely while the account is within its post-create grace period, rather than trying and
leaving a failure in the log — see Key Concept: Post-Create Grace Period.

## Key Concept: Post-Create Grace Period

Every module after this one in the pipeline needs a live connection to the account, so nothing about a
reconcile landing inside the grace period is special to this module alone — it exists to keep every
downstream module from being asked to connect to an account before it's plausibly ready. The anchor is
`status.accountCreatedAt`, a field this module owns and sets exactly once, at the moment `CREATE ACCOUNT`
succeeds — the same "a module may add its own named status field" allowance spec 009 documents for 017's
`identitySyncStartedAt`. Neither the CRD's own `metadata.creationTimestamp` nor a live re-query of
Snowflake's `SHOW ACCOUNTS` `created_on` column can substitute for it: guardrail-check (010) and
quota-check (011) run ahead of this module and can abort the whole pipeline before `CREATE ACCOUNT` ever
runs, so `creationTimestamp` can predate the real creation moment by an unbounded amount whenever
admission was delayed; and re-querying Snowflake would mean reopening the org-admin connection on every
reconcile during the grace window, against this module's own confinement of that connection to the single
`CREATE ACCOUNT` moment (Key Concept: The Only Module With Organization-Wide Privileges).

The grace period's length is `Config.Snowflake.AccountCreationGracePeriod` (002), passed into `New` as a
plain `time.Duration`. A `nil` `status.accountCreatedAt` — an account created before this field existed —
is treated as "the grace period has already elapsed," so a connection is attempted as usual; nothing
changes for accounts that predate this mechanism.

## Key Concept: The Only Module With Organization-Wide Privileges

Creating a Snowflake account needs privileges that span the whole Snowflake organization, not just one
tenant's account — and organization-wide credentials are exactly what the platform's security model
(design.md §3.11) tries hardest to avoid using routinely. This module is the sole exception: it is the
only place in the whole pipeline that ever connects with those privileges, and only for the two acts that
span the organization rather than one account — creating the account and dropping it. Every other
connection this module makes — including its own later checks — authenticates as the tenant's own account
instead.

## Key Concept: Two Restore Windows

Dropping an account does not erase it. Snowflake keeps it restorable for a grace period, during which the
resolved name stays taken — so re-creating the same resource inside that window collides on the name
instead of getting a fresh account. The credential is deleted as soon as the drop succeeds, and the secret
store holds its own recovery window on that deletion (003.a). The two windows are tied together by one
ops-owned setting, `deletion.gracePeriodDays` (002, default 30, minimum 7): this module renders it
verbatim as `DROP ACCOUNT`'s `GRACE_PERIOD_IN_DAYS`, and the secrets backend caps its own recovery
window at the same number, never exceeding it (003, 003.a).

The tie is one-directional on purpose. A credential window *shorter* than the account's leaves a band of
days on which `UNDROP ACCOUNT` succeeds but the platform credential is already gone — degraded, and
repairable by hand (see below). A credential window *longer* than the account's would be worse than
useless: the account is unrecoverable by then, so nothing can be recovered into the path, while the path
itself stays occupied and blocks re-provisioning under the same `metadata.name`. So the credential window
is as long as the store can represent without ever outliving the grace period, and the binding constraint
on re-provisioning is always Snowflake's grace period alone.

At the default of 30 the two windows coincide exactly — AWS Secrets Manager's ceiling is 30 days — so
there is no repair band at all. Raising `deletion.gracePeriodDays` above 30 opens one. 002's own floor (7)
matches Secrets Manager's minimum, so the credential window can never be forced to zero on the AWS backend
— every grace period this module can ever render still leaves the credential recoverable for at least 7
days.

### Manual repair, when the credential is already gone

Restoring an account whose credential is no longer recoverable takes three operator steps, in order:

1. `UNDROP ACCOUNT <name>` over an org-admin connection, inside the account's grace period.
2. Re-key the account's `platform` user: generate a fresh RSA keypair and
   `ALTER USER platform SET RSA_PUBLIC_KEY = '<new public key>'` from inside the restored account.
3. Store the new credential at the tenant secret path (003), so the next reconcile connects normally.

**Residual risk**: step 2 needs a login to the restored account, and the only human identities that have
one are the GIAM `ACCOUNTADMIN` groups (017). If those groups were de-provisioned along with the tenant,
nobody can perform the re-key and the account cannot be recovered at all — the exposure already recorded
as design.md Appendix B (X1). Nothing in this module mitigates it; it is the reason the default grace
period is set where the credential window can match it exactly.

## Public API

```go
// package account // internal/account/modules/account

// New constructs the account module (design.md 3.6). It implements
// internal/account/pipeline.Module's Observe/Apply/Teardown contract,
// identified by pipeline.AccountModuleName; see Key Concept:
// Create-Then-Verify Lifecycle and Key Concept: Post-Create Grace Period for
// what each method does.
//
// Parameters:
//   - backend: the secrets.Backend (003) the platform keypair is stored through, via Backend.Create
//     and, on teardown, Backend.Delete — this module never calls Update.
//   - org: Config.Snowflake.Org (002), used to build the tenant secret path (003) exactly as
//     internal/snowflake/pool does.
//   - gracePeriod: Config.Snowflake.AccountCreationGracePeriod (002) — how long a fresh account is
//     given to become reachable before the first post-create connection attempt.
//   - deletionGracePeriodDays: Config.Deletion.GracePeriodDays (002) — rendered verbatim as
//     DROP ACCOUNT's GRACE_PERIOD_IN_DAYS on teardown. Not to be confused with gracePeriod
//     above, which is a post-create reachability delay and has nothing to do with deletion.
//     Already bounded to 7-90 by 002's loader, so this module does not
//     re-validate it.
//
// Returns:
//   - pipeline.Module: never nil.
func New(backend secrets.Backend, org string, gracePeriod time.Duration, deletionGracePeriodDays int) pipeline.Module
```

`Observe`, `Apply` and `Teardown` themselves are unexported methods on the value `New` returns — nothing
outside this module's own tests calls them directly, so their behavior is documented under Key Concept:
Create-Then-Verify Lifecycle and Key Concept: Post-Create Grace Period above rather than here. All three
read `status.accountLocator`/`status.accountCreatedAt` directly through `ModuleContext.CR()`, not through
any `ModuleContext` accessor — `internal/account/pipeline` (009) defines none for either field.

`Teardown` runs three steps in a fixed order: `DROP ACCOUNT ... GRACE_PERIOD_IN_DAYS = <configured>` over
the org-admin connection, then `ModuleContext.EvictTenant()` so no stale pooled connection to a dropped
account survives, then `Backend.Delete` on the tenant secret path. Each step runs only once the one before
it succeeded, and a step whose object is already absent counts as success, so the whole sequence is safe to
re-run.

`Backend.Delete` returns the moment the credential stops being restorable — the zero time when the store
destroyed it outright (003). `Teardown` logs that timestamp and returns nothing but an error, since
`pipeline.Module` gives it no other channel; nothing downstream currently consumes it. The log exists for
an operator to notice a store whose actual deadline differs from what its configured grace period would
suggest, e.g. after someone changed the store's settings underneath the provider.

**Note**: whether `CREATE ACCOUNT` additionally accepts bind parameters for any of its positions (rather
than rendered text, see Security Considerations) is an unverified vendor fact — not needed to build this
module correctly, since the rendering Security Considerations describes is already safe, but worth
confirming opportunistically and recording in a Snowflake SQL-mechanics notes file if one is ever
started.

## Project Structure

```text
internal/account/modules/account/
├── module.go            # module struct, New, Name()
├── observe.go           # Observe: existence probe via the tenant platform connection
├── apply.go             # Apply: keypair generation, credential storage, CREATE ACCOUNT, locator capture
├── teardown.go          # Teardown: DROP ACCOUNT, pool eviction, credential deletion
├── module_test.go
├── observe_test.go
├── apply_test.go
├── teardown_test.go
└── integration_test.go  # live Snowflake + AWS Secrets Manager create-then-destroy round trip
```

## Error Classification

**User Errors**:
- `CREATE ACCOUNT` fails because the resolved account name is already taken by another account org-wide.
- `spec.contacts` is empty when this module reaches its fresh-create path (defense-in-depth backstop;
  Guardrails (008) is expected to already block this at admission).
- The resolved account name does not start with a letter (same backstop).

**System Errors**:
- RSA keypair generation fails.
- The secret store's create-only write fails, for any reason — including the path already being
  occupied.
- The org-admin connection cannot be opened.
- `CREATE ACCOUNT` fails for any reason other than the name collision above.
- The post-create locator lookup finds no matching account despite `CREATE ACCOUNT` having just
  succeeded.
- The platform connection fails when a locator is already known (the account exists but is currently
  unreachable).
- `DROP ACCOUNT` fails for any reason other than the account already being absent.
- The credential's deletion fails for any reason other than the secret path already being absent.

## Edge Cases

- **A crash lands between a successful credential store write (or `CREATE ACCOUNT`) and the locator
  being persisted to status — what happens on the next reconcile?** The next reconcile still has no
  locator, so it repeats the fresh-create path — and the credential store's create-only write now fails,
  because the path is already occupied from the previous attempt. This is accepted as a known, bounded
  operational cost rather than auto-recovered: there is no reliable way to tell "this secret is an orphan
  from a crashed attempt" apart from "this secret is a live account's platform credential", so guessing
  would be unsafe. Recovery is manual: an operator inspects the account directly in Snowflake and either
  patches the resource's status to point at the live account's locator, or deletes both the stray secret
  and any orphaned account so the resource can create cleanly on its next reconcile.
- **Why does `Apply` reconnect instead of failing outright whenever a locator is already known?** A
  locator being known does not by itself mean anything is wrong — it is also true of a perfectly healthy
  account whose `Apply` is running only because some other module further down the pipeline has drifted.
  Reconnecting distinguishes "healthy, nothing to do here" from "the account exists but the platform
  cannot reach it" — the second case, and only the second, is a real failure, and only that case aborts
  the pipeline.
- **Why does the post-create locator lookup discard rows that aren't an exact, case-insensitive match?**
  A pattern-matching lookup treats every underscore in the resolved account name as a single-character
  wildcard, since the resolved name always contains underscores by construction. Left unguarded, this
  lookup could return a different account's row whenever that account's name happens to match at every
  other position. The comparison is also case-insensitive because the resolved name is generated in
  lowercase, while Snowflake displays unquoted identifiers uppercased.
- **Why does `Observe` never look accounts up over the org-admin connection?** The org-admin connection
  is reserved for `CREATE ACCOUNT` and `DROP ACCOUNT` alone. Every module downstream of this one already
  needs a connection authenticated as the account's own `platform` user, so `Observe` reuses that same
  path to check existence rather than opening a more privileged one just to look.
- **Does this module need anything from the Backplane Config (007)?** No. The region literal comes
  entirely from the CRD plus a fixed transform, and whether a region is open for new accounts at all is
  checked earlier, during 020's validation phase — not here.
- **A deletion arrives when no locator was ever recorded — what does `Teardown` do?** With no locator
  there is no account to drop and no pooled connection to evict, so both steps are skipped and only the
  credential is deleted. That clears the stray secret a crashed create leaves behind (see above); if an
  account really was created and its locator never persisted, dropping it stays the manual operator job
  that case already describes.
- **The credential path is scheduled for deletion but not yet gone — what does the next reconcile see?**
  Neither present nor absent. A path inside its recovery window cannot be read and cannot be re-created
  (003), so `Observe` cannot treat it as a live credential and `Apply`'s fresh-create path cannot claim it
  either: the create-only write fails on an occupied path exactly as it does for a live credential, and the
  failure is a system error like any other store fault. There is nothing for this module to do about it —
  the state is self-clearing, bounded by the account's own grace period, and re-provisioning under the same
  `metadata.name` was already blocked by the account name for at least as long.
- **Why render the configured grace period rather than Snowflake's minimum of 3?** Because 3 is the value
  that makes recovery least likely to work: it is below the 7-day floor AWS Secrets Manager can represent,
  so the credential would be destroyed outright on every deletion and every restore would need the manual
  repair above. The configured default of 30 is instead the largest value the reference store matches
  exactly.
- **The account, or the secret path, is already gone — does `Teardown` fail?** No. Both count as
  success, so a destruction retried after a partial failure — or after an operator cleaned up by
  hand — converges instead of stalling. The backends do not agree on this themselves (AWS's `Delete`
  errors on a path that does not exist, 003.a), so this module swallows the case rather than relying on
  the backend to.

## Dependencies

- **Base Configuration (002)** — Used APIs: `Config.Snowflake.Org`, `Config.Snowflake.AccountCreationGracePeriod`,
  `Config.Deletion.GracePeriodDays` — Contract: all three passed to `New` as plain values; this module never
  loads the config file itself, and never re-validates `GracePeriodDays`, which 002's loader has already
  bounded to 7-90.
- **Secrets Handling (003)** — Used APIs: `GenerateKeyPair()`/`NewCredentials()`, `MarshalCredentials()`,
  `NewTenantPath()`, `Backend.Create()`, `Backend.Delete()` — Contract: `Create` and `Delete` only,
  never `Update`; the module never reads a credential back. `Delete`'s recovery window is the backend's own
  business — this module passes no window and cannot choose one; it only observes the deadline `Delete`
  reports.
- **Connection Pooling (004)** — Used APIs: `ModuleContext.OrgAdminDB()`, `ModuleContext.TenantDB()`,
  `ModuleContext.EvictTenant()` — Contract: reached only through `ModuleContext`; this module never
  imports `internal/snowflake/pool` or `internal/snowflake/host` directly.
- **Statement Execution (005)** — Used APIs: `statement.New()`, `Runner.Exec()`, `Runner.Query()`,
  `QuoteLiteral()`, `BareIdentifier()`, `*statement.Error` — Contract: every tenant-influenced value is
  rendered through one of these, never concatenated raw.
- **SnowflakeAccount CRD (006)** — Used APIs: `SnowflakeAccountSpec.Description`, `.Contacts`, `.Region`,
  `SnowflakeAccountStatus.AccountLocator`, `.AccountCreatedAt` — Contract: reads the spec fields
  read-only; writes `AccountLocator`/`AccountCreatedAt` directly on `ModuleContext.CR().Status` — the
  only two status fields this module ever sets.
- **Account Pipeline (009)** — Used APIs: `account.Module`, `Done()`/`Pending()`/`Rejected()`/`Failed()`,
  `Outcome.Aborting()`, `ModuleContext.CR()`, `.ResolvedAccountName()`, `.OrgAdminDB()`, `.TenantDB()`,
  `.EvictTenant()` — Contract: `Name()` returns `pipeline.AccountModuleName`, which is how
  `Pipeline.Observe` finds `Observation.Exists` regardless of registration position; calls `.Aborting()`
  on every outcome that is not `Done`; `Teardown` returns a plain classified error, not an `Outcome`, and
  is reached only through `Pipeline.Destroy`.

## Integration Points

- **SnowflakeAccount Controller (020)** — Registers this module in the pipeline via
  `account.New(secretsBackend, baseConfig.Snowflake.Org, baseConfig.Snowflake.AccountCreationGracePeriod,
  baseConfig.Deletion.GracePeriodDays)`,
  after the guardrail-check (010) and quota-check (011) modules. After `Pipeline.Apply` returns, reads
  `ModuleContext.ResolvedAccountName()` directly — never from this module's `Outcome` — plus
  `cr.Status.AccountLocator`, which this module has already set directly on the CRD, to render
  `status.accountName` and (via `internal/account/tenant.AccountURL`) `status.accountUrl`.
  `status.accountLocator` and `status.accountCreatedAt` need no separate persist step: this module writes
  them straight onto the same `*v1alpha1.SnowflakeAccount` the controller already holds and will persist
  when the reconcile returns. Minimizing how long that persist is deferred is still 020's responsibility,
  since every reconcile between a successful `CREATE ACCOUNT` and the actual API-server write is the
  crash window described above. Reaches the drop only through `Pipeline.Destroy`, once an active deletion
  request (019) has authorized it — never by calling this module directly.

## Success Criteria

- **SC-001**: `Observe` returns not-in-sync with no connection attempt when no locator is known.
- **SC-002**: `Observe` returns in-sync once a known locator's platform connection succeeds.
- **SC-003**: `Observe` returns not-in-sync, with a system error, when a known locator's platform
  connection fails.
- **SC-004**: `Apply` returns `Done()` without touching the credential store or issuing `CREATE ACCOUNT`
  when a locator is already known, the grace period (if any) has elapsed, and the platform connection
  succeeds.
- **SC-005**: `Apply` aborts with a system error, and issues no SQL, when a locator is already known, the
  grace period (if any) has elapsed, but the platform connection fails.
- **SC-006**: A fresh create generates a keypair, stores it create-only, then issues `CREATE ACCOUNT` —
  in that order, and only in that order.
- **SC-007**: A fresh create aborts with a system error, generating no keypair and issuing no SQL, when
  the resolved secret path is already occupied.
- **SC-008**: `CREATE ACCOUNT`'s `REGION` literal is the CRD's region uppercased with every `-` replaced
  by `_`.
- **SC-009**: `CREATE ACCOUNT`'s `COMMENT` clause is omitted entirely when `spec.description` is empty.
- **SC-010**: `CREATE ACCOUNT`'s `EMAIL` is always `spec.contacts[0]`.
- **SC-011**: A fresh create aborts with a user error, generating no keypair, when `spec.contacts` is
  empty.
- **SC-012**: A `CREATE ACCOUNT` failure due to an org-wide name collision is classified as a user error;
  every other `CREATE ACCOUNT` failure is classified as a system error.
- **SC-013**: The post-create locator lookup discards a row whose account name is not an exact,
  case-insensitive match to the resolved name, even when the lookup's own pattern matching returns it.
- **SC-014**: A fresh create aborts with a system error when the post-create locator lookup finds no
  matching row.
- **SC-015**: A successful fresh create sets `cr.Status.AccountLocator` to the looked-up locator and
  `cr.Status.AccountCreatedAt` to the current time, directly on the CRD, before returning
  `Pending(...).Aborting()` — never `Done()`.
- **SC-016**: Every outcome other than `Done()` carries `Abort == true`.
- **SC-017**: Unit test coverage exceeds 95%.
- **SC-018**: Integration test coverage includes a full create-then-reconnect-then-destroy round trip
  against a live Snowflake organization and a live secrets backend.
- **SC-019**: Both `Observe` and `Apply`'s reconnect path attempt no platform connection, and issue no
  error, while `time.Since(cr.Status.AccountCreatedAt) < gracePeriod`; `Apply` reports `Pending(...).Aborting()`
  and `Observe` reports not-in-sync with no outcome error.
- **SC-020**: `cr.Status.AccountCreatedAt` is set exactly once, on the reconcile that first creates the
  account, and is never touched again on any later reconcile — including one that lands inside the grace
  period.
- **SC-021**: A `nil` `cr.Status.AccountCreatedAt` with a known locator is treated as past the grace
  period: `Observe`/`Apply` attempt a connection exactly as they did before this field existed.
- **SC-022**: `Teardown` renders the resolved account name as a bare identifier and always includes a
  `GRACE_PERIOD_IN_DAYS` clause carrying the value `New` was given, unchanged and unclamped.
- **SC-023**: `Teardown` issues no SQL and evicts nothing when `cr.Status.AccountLocator` is empty, and
  still deletes the credential.
- **SC-024**: `Teardown` returns nil when the account is already absent, and when the secret path is
  already absent.
- **SC-025**: `Teardown` drops the account, then evicts the pooled connection, then deletes the
  credential — in that order, and performs no later step once one has failed.
- **SC-026**: `Teardown` passes `Config.Deletion.GracePeriodDays` straight into the rendered
  `GRACE_PERIOD_IN_DAYS` for every value 002 admits (3 and 90 at the bounds), and derives no window of its
  own for the credential.
- **SC-027**: `Teardown` logs the restorable-until time `Backend.Delete` returned, including the case where
  it is the zero time because the store destroyed the credential outright, and reports it nowhere else —
  `Teardown`'s return value stays a plain error.

## Security Considerations

- The org-admin connection is opened only inside this module, and only on the fresh-create and teardown
  paths — no other module, and no other path through this one, ever requests it.
- This module does not check whether a destruction is authorized: it drops whenever `Teardown` is called.
  The deletion request's two-key gate (019) is what stands between a tenant's `kubectl delete` and that
  call.
- The secret store's create-only write is the sole safeguard against overwriting a live account's
  credential on a retried request; this module never reads a stored credential back to decide whether to
  reuse it.
- The post-create locator lookup's pattern matching is a coarse pre-filter only; the exact,
  case-insensitive re-check is load-bearing, not defensive style, given how often the resolved account
  name's own underscores would otherwise produce a false match.
- **`CREATE ACCOUNT` rendering.** Only two of this statement's values are tenant-supplied free text —
  `EMAIL` and `COMMENT` — and both are rendered as escaped, quoted string literals. Every other position
  is either a fixed controller literal or an algorithmically-derived token, never raw tenant input, and
  is still passed through a bare-identifier charset check as a defense-in-depth backstop rather than
  trusted implicitly. None of these values is ever concatenated into the statement text unescaped.

  | Position | Value | Rendering |
  | --- | --- | --- |
  | account name | the resolved account name (design.md 3.12) | bare identifier |
  | `ADMIN_NAME` | fixed `"platform"` | quoted literal |
  | `ADMIN_RSA_PUBLIC_KEY` | the generated public key | quoted literal |
  | `ADMIN_USER_TYPE` | fixed `SERVICE` | bare token |
  | `EMAIL` | `spec.contacts[0]` (tenant free text) | quoted literal |
  | `EDITION` | fixed `ENTERPRISE` | bare token |
  | `REGION` | `spec.region`, transformed into Snowflake's region-identifier form | bare identifier |
  | `COMMENT` | `spec.description` (tenant free text); clause omitted if empty | quoted literal |

- **`DROP ACCOUNT` rendering.** Its only two positions are the resolved account name, rendered as a bare
  identifier, and `GRACE_PERIOD_IN_DAYS`, an `int` from ops-owned provider configuration (002) that 002's
  loader has already bounded to 7-90. No tenant-supplied text reaches this statement at all, and no tenant
  can influence the grace period — deletion protection would be worthless if the party being protected
  from could shorten the window it is protected by.

## References

- **Product design**: `specs/design.md` §3.2, §3.6, §3.11, §3.11.1, §3.12, §6.1–§6.3 (the deletion
  flow this module's teardown is the last phase of), Appendix B (X1).
- **Account Pipeline**: `internal/account/pipeline/module.go`, `context.go`, `pipeline.go` — the `Module`
  interface, `Outcome` vocabulary, and shared `ModuleContext` this module implements against.
- **Secrets Handling**: `internal/secrets/backend.go`, `path.go`, `credentials.go`.
- **Statement Execution**: `internal/snowflake/statement/statement.go`, `render.go`, `errors.go`.
- **Snowflake `CREATE ACCOUNT` reference**: https://docs.snowflake.com/en/sql-reference/sql/create-account
  — required parameters, and which parameter positions are quoted string literals versus bare tokens.
- **Snowflake `SHOW ACCOUNTS` reference**: https://docs.snowflake.com/en/sql-reference/sql/show-accounts
  — the `account_locator`/`account_name` columns, and `LIKE`'s wildcard-only, case-insensitive matching.
- **Snowflake `DROP ACCOUNT` reference**: https://docs.snowflake.com/en/sql-reference/sql/drop-account
  — `GRACE_PERIOD_IN_DAYS` being required, its range of 3-90, and the account staying restorable (and its
  name taken) for that period.
- **Base Configuration**: `specs/002-base-config.md` — `deletion.gracePeriodDays`, the single setting both
  windows derive from.
- **Secrets Handling**: `specs/003-secrets-handling.md` — Key Concept: A Credential May Not Outlive Its
  Account. The concrete window computation lives in `specs/003.a-aws-secrets-backend.md`.

<br/><br/><br/><br/><br/>

================

## Appendix: Usage Examples

### Example 1: Wiring the module into the pipeline (020)

```go
import (
    accountmodule "github.com/allianz/yukimi/internal/account/modules/account"
    "github.com/allianz/yukimi/internal/account/pipeline"
)

pl := pipeline.New(
    guardrailcheckmodule.New(...),                                // 010, runs first, aborts before anything else
    quotacheckmodule.New(...),                                    // 011, runs second, aborts before CREATE ACCOUNT
    // 012 — the two grace periods are unrelated: the duration is a post-create
    // reachability delay, the int is DROP ACCOUNT's GRACE_PERIOD_IN_DAYS.
    accountmodule.New(
        secretsBackend,
        baseConfig.Snowflake.Org,
        baseConfig.Snowflake.AccountCreationGracePeriod,
        baseConfig.Deletion.GracePeriodDays,
    ),
    // ... modules 013-015, 017, 018, in order
)
```

### Example 2: Create, wait out the grace period, then reconnect

```go
mc := pipeline.NewModuleContext(cr, "finance", nil, nsLabels, log, pool)

// First reconcile: no locator yet.
inSync, _ := module.Observe(ctx, mc)   // inSync == false, nothing has been touched yet
outcome := module.Apply(ctx, mc)       // generates keypair, stores it, issues CREATE ACCOUNT,
                                        // sets cr.Status.AccountLocator/.AccountCreatedAt directly,
                                        // returns Pending(...).Aborting() — the pipeline stops here

// A reconcile landing inside the grace period, against the same cr (status.accountLocator and
// status.accountCreatedAt already set by the pass above):
mc2 := pipeline.NewModuleContext(cr, "finance", nil, nsLabels, log, pool)
inSync2, outcome2 := module.Observe(ctx, mc2) // inSync2 == false, StatePending — no connection attempted
_ = module.Apply(ctx, mc2)                    // same skip; Pending(...).Aborting(), no connection attempted

// A later reconcile, once the grace period has elapsed:
mc3 := pipeline.NewModuleContext(cr, "finance", nil, nsLabels, log, pool)
inSync3, _ := module.Observe(ctx, mc3) // reconnects as platform; inSync3 == true
outcome3 := module.Apply(ctx, mc3)     // reconnects again, no SQL issued, returns Done()

// Deletion, reached through Pipeline.Destroy once a deletion request (019) has authorized it:
err := module.Teardown(ctx, mc3)       // DROP ACCOUNT ... GRACE_PERIOD_IN_DAYS = 30, evicts the
                                        // pooled connection, deletes the stored credential — which
                                        // the store keeps restorable for 30 days too, never longer
```
