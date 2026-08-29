> **Clarification record — not a specification.** Produced by `/yukimi.clarify 010` to settle what
> `specs/design.md` intentionally leaves out and `specs/scope-010-account-module.md` does not cover. It
> records decisions, not product design — `specs/design.md` remains authoritative and always wins.
> Read it together with the scope note when writing `010-account-module.md`, then delete both.

## Clarification runs

- Run 1 — covered: the scope note's three explicitly-flagged open questions (`REGION` literal, locator
  capture mechanism, URL on a reconcile where the account already exists); the module-result payload for
  the resolved name/URL; idempotency and partial-failure behavior of the keypair-then-`CREATE ACCOUNT`
  sequence; how `Observe`'s existence check interacts with 009's already-implemented `Pipeline.Observe`;
  condition ownership; SQL-rendering safety for `CREATE ACCOUNT`. Left open: O-001, O-002, O-003 (vendor
  facts Context7 could not confirm this session — service returned 403 on both attempts).
- Run 2 — the user supplied Snowflake's own `CREATE ACCOUNT` doc page
  (https://docs.snowflake.com/en/sql-reference/sql/create-account), fetched directly. Closed O-001
  outright (promoted to D-011) and narrowed O-002/O-003 with verified syntax facts (D-012). Still open:
  the true bind-parameter question in O-002 (SQL-reference docs don't cover driver-level binding
  behavior — expected, this is a `gosnowflake`-driver fact, not a SQL-grammar one) and the full-catalog
  question in O-003.
- Run 3 — Context7 still returned 403; fetched Snowflake's `SHOW ACCOUNTS` reference directly instead to
  pin down D-002's exact mechanism. Found and fixed a real correctness gap in D-002 itself (`LIKE`'s
  wildcard semantics versus the resolved name's own literal underscores — see the amended D-002).
  Reframed O-002/O-003 as accepted, non-blocking residuals rather than spec-writing blockers (D-013): both
  already have fully-specified fallback behavior, so neither actually needs resolving before
  `010-account-module.md` can be written.

## Resolved Decisions

### D-001 — `CREATE ACCOUNT`'s `REGION` literal

**Question**: The CRD/backplane region key (`aws-eu-central-1`) is not Snowflake's own region
identifier (e.g. `AWS_EU_CENTRAL_1`), and neither `design.md` nor 007's schema records a mapping. The
scope note left this open for 007 and 010 to settle jointly.

**Decision**: 010 derives the literal with a pure, in-package transform — uppercase the CRD's region
string and replace every `-` with `_` (`aws-eu-central-1` → `AWS_EU_CENTRAL_1`). No new field on 007's
`backplane.Region`, no change to `backplane.yaml`.

**Rationale**: Put to the user as a genuine choice against adding an ops-supplied `snowflakeRegion`
field to already-shipped spec 007. The user chose the transform. Every region key `design.md` shows
(`aws-eu-central-1`, `aws-eu-west-3`, `azure-westeurope`) satisfies this transform exactly. The residual
risk — an unverified edge case (e.g. government/VPS regions) where Snowflake's real identifier doesn't
follow this pattern — is carried forward as O-003, with an explicit no-special-casing fallback: a
transform that produces a region Snowflake doesn't recognize simply makes `CREATE ACCOUNT` fail, which is
an ordinary `Failed(systemErr)` like any other Snowflake-side rejection, not a distinct error path.

**Affects spec section**: Key Concept (the transform rule), Public API (the function), Edge Cases (the
unrecognized-region fallback).

### D-002 — Locator capture mechanism

**Question**: The scope note leaves how to capture the account locator open ("`CREATE ACCOUNT`'s own
result set is one source, but its format isn't reliably documented... `SHOW ACCOUNTS LIKE` ... is an
equally fine way"), and cross-references this to "the same statement Observe below already uses" —
which, per D-008, turns out not to exist (Observe never issues `SHOW ACCOUNTS`).

**Decision**: Immediately after a successful `CREATE ACCOUNT`, issue
`SHOW ACCOUNTS LIKE '<resolved-name>'` over the same org-admin connection to narrow the result set, read
`account_locator` and `account_name` off every returned row, and **discard any row whose `account_name`
does not exactly equal the resolved name** before trusting its locator. Never parse `CREATE ACCOUNT`'s
own result set.

**Verified** (https://docs.snowflake.com/en/sql-reference/sql/show-accounts, fetched this session):
`SHOW ACCOUNTS`'s output includes an `account_locator` column ("System-assigned identifier of the
account") and an `account_name` column. Its `LIKE` clause "uses case-insensitive pattern matching, with
support for SQL wildcard characters (`%` and `_`)" and its syntax section documents no `ESCAPE` clause or
any way to match a literal `_`/`%` — the docs' own example (`LIKE '%testing%'` ≡ `LIKE '%TESTING%'`)
confirms wildcards apply unconditionally.

**This matters concretely, not hypothetically**: every resolved account name (006's `tenant.ResolveName`)
contains literal `_` characters by construction — from translating every `-` in the CRD name, and from
the fixed `_<hash>` suffix separator. A bare `SHOW ACCOUNTS LIKE '<resolved-name>'` would treat each of
those underscores as a single-character wildcard, so it can return rows for other accounts whose names
merely happen to match at every non-underscore position — a real false-positive risk on essentially
every call, not an edge case. Hence the exact-match re-check in the Decision above is load-bearing, not
defensive style.

**Rationale**: `CREATE ACCOUNT`'s result-set shape isn't documented reliably (the scope note's own
words); `SHOW ACCOUNTS` is a normal, stable read. This call belongs to the create sequence only — it
does not make org-admin a routine dependency of steady-state reconciliation, preserving "this module
takes org-admin only for `CREATE ACCOUNT`" (restated in the scope note's "Not this module's job"). Using
`LIKE` at all (rather than fetching the full org-wide account list and filtering purely client-side) is
still worthwhile as a coarse server-side pre-filter — it just cannot be trusted alone.

**Edge case**: if the exact-match re-check finds no row at all despite `CREATE ACCOUNT` having just
returned success, that is `Failed(systemErr)`, not `Rejected` — `CREATE ACCOUNT`'s own success already
proves the account exists, so a `SHOW ACCOUNTS` read that doesn't yet reflect it is an infrastructure
inconsistency (e.g. read-after-write lag), not a tenant-input problem.

**Affects spec section**: Public API / Key Concept (create-sequence steps; the exact-match re-check is
part of the algorithm, not an aside), Dependencies (004's `OrgAdminDB`), Security Considerations
(false-positive risk from `LIKE`'s wildcard semantics).

### D-003 — No string payload added to `Outcome`; corrects the scope note's 007 dependency rationale

**Question**: The scope note says "the resolved name and the built URL are returned in the module
result, not written to the resource." 009's already-shipped `Outcome` struct (`internal/account/module.go`)
carries only `State/Reason/Err/Abort/Condition` — no field for a string result. Separately, the scope
note justifies depending on spec 007 because "`url.go` (006) has to know whether the region has
PrivateLink enabled, and this module reads that off the region entry."

**Decision**: No field is added to `Outcome`. 018 already owns the same `*ModuleContext` it constructs
and passes into the pipeline; it calls `mc.ResolvedAccountName()` and, after `Apply` returns,
`mc.Locator()` directly, then builds the URL itself via `tenant.AccountURL(locator, region,
usePrivateLink)` (006). 010's own `Outcome` never needs to carry either value.

**Rationale**: This also corrects the scope note's stated reason for the 007 dependency. 007's `Region`
struct (`internal/backplane/backplane.go`, already shipped) has no PrivateLink-related field at all, and
already-shipped 006 documents `usePrivateLink` as a single **provider-wide** `BaseConfig.Snowflake.UsePrivateLink`
flag, explicitly "supplied by the caller (018)... not read from the Backplane Config, which carries no
such field." So 010 does not need `mc.BackplaneRegion()` for URL-building at all — that reasoning in the
scope note was stale relative to how 006/007 actually shipped. (010 may still touch `BackplaneRegion()`
for other reasons the written spec settles, but not this one.)

**Affects spec section**: Public API (`Outcome` unchanged), Dependencies (007's actual role, corrected),
Integration Points (018 renders name/URL/locator directly from `ModuleContext`).

### D-004 — URL on a reconcile where the account already exists

**Question**: The scope note's own open question: on a reconcile where the account already exists,
there's no fresh `CREATE ACCOUNT` result and hence (it assumed) no locator, so 018 might blank an
existing `status.accountUrl` unless the locator is recovered via a new status field or `SHOW ACCOUNTS`.

**Decision**: Not a gap. `NewModuleContext` (009, already shipped, `internal/account/context.go`) seeds
`Locator()` from `cr.Status.AccountLocator` on every single call, regardless of whether this reconcile is
creating the account or not. Once an account has been created once (and its locator persisted, per
D-007's caveat), every later reconcile's `ModuleContext` already carries that locator with zero extra
calls. No new status field, no extra `SHOW ACCOUNTS` lookup for this purpose.

**Affects spec section**: Edge Cases (closes the scope note's open question outright).

### D-005 — No credential reuse or auto-recovery on `Backend.Create` collision

**Question**: What should happen if a retried request finds the RSA-keypair secret path already
occupied — e.g. because a previous attempt stored the key but crashed before `CREATE ACCOUNT` completed?
Should 010 read back and reuse the existing key rather than fail outright?

**Decision**: No. `Backend.Create` hitting an occupied path is always `Failed(systemErr).Aborting()`. The
stored value is never read back, never reused, never overwritten.

**Rationale**: This is what the scope note already states ("this module surfaces that failure as a
system error rather than reusing or replacing what it finds"), and code-reading confirms it's the only
workable choice, not merely the cautious one: `secrets.Backend.Get` (003, and its AWS implementation,
003.a) documents no way to distinguish "nothing stored at this path" from "the store is unreachable" —
both surface as the same plain wrapped system error. There is no reliable signal a "read back and decide
whether to reuse" branch could key off, so attempting one would not actually be safer, just more complex.

**Affects spec section**: Key Concept (ordering: generate → store → `CREATE ACCOUNT`), Error
Classification, Edge Cases.

### D-006 — Classifying a `CREATE ACCOUNT` name collision

**Question**: When `CREATE ACCOUNT` itself fails because the resolved name is already taken, is that
always the tenant-fixable collision the scope note describes (`Rejected(userErr)`, "rename your CRD"), or
could it sometimes be this tenant's own earlier, partially-completed attempt?

**Decision**: Always `Rejected(userErr)`. No lookup or ownership-verification dance is needed to tell the
two apart.

**Rationale**: Given D-005's ordering (keypair stored strictly before `CREATE ACCOUNT` is attempted, and
never reused on retry), reaching the `CREATE ACCOUNT` call at all on this reconcile already proves this
tenant's own secret path was free (`Backend.Create` just succeeded). So a "name already exists" failure
at the `CREATE ACCOUNT` step cannot be this tenant's own prior attempt — that case would have already been
caught by `Backend.Create` failing first (D-005), never reaching `CREATE ACCOUNT` again. It can only be a
genuine collision with something else, which the tenant can resolve by renaming their CRD.

**Affects spec section**: Error Classification.

### D-007 — Accepted cost: the status-persistence crash window

**Question**: A crash between a successful `CREATE ACCOUNT` (or even just a successful `Backend.Create`)
and 018 persisting `status.accountLocator` leaves the next reconcile with `Locator() == ""` again, but a
secret already stored and (possibly) a live account already created. Per D-005, `Backend.Create` will
then fail on retry (occupied path) — permanently, since nothing here reads back or repairs it. Should
010 do something better?

**Decision**: No — this is accepted as a known, bounded operational cost, not solved automatically.

**Rationale**: D-005 already establishes that there's no reliable way to distinguish "this secret is an
orphan from a crashed attempt" from "this secret is a live account's platform credential" (`Backend.Get`
can't tell "not found" from "store is down," and even if it could, the module still couldn't verify
*whose* account it belongs to without risk). Given that, the only two honest choices are "guess" (unsafe)
or "surface and wait for an operator" (safe). This mirrors 009's own treatment of other permanently-stuck
states ("cheap-but-unbounded... accepted rather than solved here"). Recovery is manual: an operator
inspects the account via `SHOW ACCOUNTS`/`DESC USER`, and either patches `status.accountLocator` by hand
to match a live account, or deletes both the stray secret and the (if any) orphaned Snowflake account so
the CRD can create cleanly.

**Affects spec section**: Edge Cases (documented explicitly, including the manual-recovery steps an
operator would take).

### D-008 — `Observe`'s existence signal, forced by 009's actual `Pipeline.Observe` code

**Question**: The scope note wants `Observe` to distinguish three states (no credentials; credentials +
auth succeeds; credentials + auth fails) and treat the third as `Failed(systemErr)` surfaced on status,
never a silent recreate. But 009's already-implemented `Pipeline.Observe`
(`internal/account/pipeline.go`) computes `Observation.Exists` **directly** from module 0's `inSync`
return value (`obs.Exists = inSync` when `i == 0`) and **discards module 0's `Outcome` entirely** — there
is no channel left for "exists, but broken" to differ from "doesn't exist" at the `Observe` layer.

**Decision**:
- **`Observe`**: if `mc.Locator() == ""`, always return `inSync=false` — no connection attempt, no
  org-admin use, matches "the very first reconcile" case from 009 directly. If `mc.Locator() != ""`,
  attempt `mc.PlatformDB(ctx)`: success → `inSync=true`; failure → `inSync=false`.
- **`Apply`**: the create path (generate keypair, `Backend.Create`, `CREATE ACCOUNT`) runs **only** when
  `mc.Locator() == ""`. When `mc.Locator() != ""` — meaning `Observe` reported not-exists solely because
  auth is currently broken, not because the account was never created — `Apply` short-circuits
  immediately to `Failed(systemErr).Aborting()` without ever calling `Backend.Create` or `CREATE ACCOUNT`
  again.

**Rationale**: Reporting `inSync=false` when auth fails is, at the `Observe` layer, indistinguishable
from "doesn't exist" — that's an accepted mislabel, not a bug, because `Create` and `Update` both resolve
to the identical `apply()` body in 018 (009 Example 2), which *does* render `Outcome.Err` into conditions.
So misrouting through `Create` instead of `Update` costs nothing except vocabulary; what actually prevents
a wrongful re-`CREATE ACCOUNT` is `Apply` checking `mc.Locator()` itself before ever touching `Backend` or
issuing SQL — not what `Observe` reported. Without `Observe` re-probing `PlatformDB` on every reconcile
(rather than trusting `observedGeneration == Generation`), a tenant dropping/re-keying the `platform` user
after admission (Appendix B X1) would never be re-detected once the pipeline reaches a fully-`Done` state,
since no other module ever calls `PlatformDB` again once nothing regenerates.

**Affects spec section**: Public API (`Observe`/`Apply` bodies), Key Concept (why `Observe` re-probes
every reconcile even at steady state), Edge Cases.

### D-009 — No dedicated condition type

**Question**: Does 010 own a custom `xpv1.ConditionType` the way `QuotaAvailable`/`IdentitySynced` (009)
do?

**Decision**: No. `Outcome.Condition` is always left `nil`. Failures surface exclusively through
`Outcome.Err`, rendered by 018 via `log.Handle` plus the aggregate `Ready`/`Synced`.

**Rationale**: 009's `conditions.go` defines exactly two module-owned condition types, neither belonging
to the account module; nothing in `design.md` calls for a distinct condition here beyond the standard
`Ready`/`Synced` model (§7.1).

**Affects spec section**: Public API, Error Classification.

### D-010 — Empty `COMMENT`

**Decision**: When `spec.description` is empty, omit the `COMMENT=` clause from the rendered `CREATE
ACCOUNT` statement entirely, rather than rendering `COMMENT=''`.

**Rationale**: Simpler generated SQL; `description` is optional per 006's schema with no default.

**Affects spec section**: Public API / Key Concept (statement construction).

### D-011 — `EMAIL` is required; sourced from `spec.contacts[0]`

**Question**: Closes former O-001. Does `CREATE ACCOUNT` require `EMAIL`? `design.md`'s illustrative SQL
(§3.6) omits it.

**Decision**: `EMAIL` is required and is populated from `spec.contacts[0]` — the tenant's first
`contacts[]` entry.

**Verified**: Snowflake's own `CREATE ACCOUNT` reference
(https://docs.snowflake.com/en/sql-reference/sql/create-account, fetched 2026-08-29) lists the required
parameters explicitly: `name`, `ADMIN_NAME`, one of `ADMIN_PASSWORD`/`ADMIN_RSA_PUBLIC_KEY`, `EMAIL`, and
`EDITION`. `EMAIL`'s own entry reads: "Email address of the initial administrative user of the account.
This email address is used to send any notifications about the account." No other parameter beyond this
set is required.

**Rationale**: `spec.contacts[]` (006) is the only CRD field carrying an email address at all, and
design.md's own worked example (§3.1) already lists real addresses there. `contacts[0]` is the natural,
unambiguous choice — there's no ranking or "primary contact" field to prefer over plain list order.

**Affects spec section**: Public API (the literal `CREATE ACCOUNT` statement), Dependencies (reads
`spec.contacts`).

### D-012 — Verified shape of every `CREATE ACCOUNT` parameter position

**Question**: Which of `CREATE ACCOUNT`'s parameters are quoted string literals versus bare
tokens/identifiers — the fact `internal/snowflake/statement/render.go` (005) says must be pinned down
before choosing `QuoteLiteral` vs. `BareIdentifier`-style rendering for each.

**Verified** (same source as D-011): the syntax block is explicit about which parameters are
single-quoted string literals and which are bare tokens:

```
CREATE ACCOUNT <name>
      ADMIN_NAME = '<string_literal>'
    { ADMIN_PASSWORD = '<string_literal>' | ADMIN_RSA_PUBLIC_KEY = '<string_literal>' }
    [ ADMIN_USER_TYPE = { PERSON | SERVICE | LEGACY_SERVICE | NULL } ]
      EMAIL = '<string_literal>'
      EDITION = { STANDARD | ENTERPRISE | BUSINESS_CRITICAL }
    [ REGION_GROUP = <region_group_id> ]
    [ REGION = <snowflake_region_id> ]
    [ COMMENT = '<string_literal>' ]
```

- **Quoted string-literal positions**: `ADMIN_NAME`, `ADMIN_RSA_PUBLIC_KEY`, `EMAIL`, `COMMENT`. Of
  these, `EMAIL` (from `spec.contacts[0]`) and `COMMENT` (from `spec.description`) are the two carrying
  tenant-supplied free text — the ones that actually need injection-safe rendering; `ADMIN_NAME` is
  always the fixed literal `platform` and `ADMIN_RSA_PUBLIC_KEY` is controller-generated key material,
  neither ever tenant-controlled.
- **Bare token/keyword positions**: `<name>` (the account name — no quotes in Snowflake's own syntax,
  unlike design.md §3.6's illustrative `'<resolved-account-name>'`, which design.md itself disclaims as
  inexact), `ADMIN_USER_TYPE`, `EDITION`, `REGION_GROUP`, `REGION`. All five are always either a fixed
  literal (`SERVICE`, `ENTERPRISE`) or algorithmically derived (the resolved account name via 006's
  `tenant.ResolveName`; the region literal via D-001's transform) — never raw tenant input — so
  `BareIdentifier`-style charset validation (005's existing pattern for `ALTER ACCOUNT SET`'s parameter
  name) is the appropriate safety net, not literal-quoting.
- **Notably**: the docs' own worked example writes `region = aws_us_west_2` — **unquoted and
  lowercase**. Snowflake tokens are case-insensitive unless double-quoted, so D-001's transform's
  uppercasing step is cosmetic, not required; only the `-`→`_` replacement is load-bearing. This lowers
  O-003's risk (case never matters), though the full-catalog question stays open.

**Still open** (carried into O-002, narrowed): whether any of these positions additionally accept a
driver-level bind placeholder (`?`/`IDENTIFIER(?)`) instead of rendered text at all. Snowflake's SQL
reference is silent on this by design — it documents SQL grammar, not `gosnowflake` driver/bind-protocol
behavior — so this remains a fact to establish empirically (a real connection, or the driver's own docs/
source), not something the CREATE ACCOUNT reference page could ever answer.

**Affects spec section**: Public API (exact rendering per parameter), Error Classification (which
positions ever see tenant input), Security Considerations.

### D-013 — O-002/O-003 do not block writing `010-account-module.md`

**Question**: Do the still-open vendor facts in O-002 (bind-parameter support) and O-003 (full
region-ID catalog) need to be resolved before the spec can be written?

**Decision**: No. 010 renders every `CREATE ACCOUNT` parameter via `statement.QuoteLiteral` (the four
quoted-string positions) or a `BareIdentifier`-style charset check (the five bare-token positions),
per D-012's verified syntax — unconditionally, regardless of whether Snowflake's driver additionally
supports bind placeholders for any of these positions. Likewise, D-001's region transform is applied
unconditionally, and an unrecognized result is simply a `Failed(systemErr)` from `CREATE ACCOUNT` itself
(D-001's existing fallback) — no branching on which regions are "known good."

**Rationale**: Both open questions were originally framed as blockers, but neither actually is: each
already has a fully-specified, safe, correct behavior regardless of the answer. Confirming bind-parameter
support would only let 010 additionally avoid rendering `EMAIL`/`COMMENT` as escaped text (a
defense-in-depth improvement over an already-safe baseline, per `QuoteLiteral`'s own escaping guarantee);
confirming the region catalog would only rule out a failure mode that already has a correct, if slightly
less friendly, fallback. Treating them as "resolve later, opportunistically" rather than "must resolve
now" keeps this clarification from stalling on facts that don't change what gets built.

**Affects spec section**: frames the two Open Questions below as non-blocking.

## Open Questions

Neither of the following blocks writing `010-account-module.md` (D-013) — both already have a fully
specified, safe fallback. They're recorded so whoever implements 010 can opportunistically verify and
tighten them.

- **O-002** (narrowed by D-012, non-blocking per D-013) — Do any of `CREATE ACCOUNT`'s parameter
  positions accept Snowflake's `IDENTIFIER(?)`/bind-parameter binding, as an additional hardening over
  the `QuoteLiteral`/`BareIdentifier` rendering D-013 already mandates? This is a `gosnowflake`
  driver/execution fact, not a SQL-grammar fact — Snowflake's SQL reference is silent on it by design, and
  Context7 was unreachable this session (403 on every attempt across both runs). `internal/snowflake/
  statement/render.go`'s doc comments name this exact statement as the one needing verification, pointing
  at `specs/notes-snowflake-sql-mechanics.md §7`, which doesn't exist yet — **010 is the spec expected to
  create that notes file (or add its §7)** once verified, whether at spec-writing time or later.
- **O-003** (narrowed by D-012, non-blocking per D-013) — D-001's mechanical region transform now only
  needs `-`→`_` (case is confirmed not to matter, per D-012). Whether every Snowflake region ID otherwise
  follows this pattern — in particular any government/VPS region — is still unverified: the `SHOW
  REGIONS` reference page describes the `snowflake_region` column but the fetched content included no
  example rows to check against.

## Forward Contracts

- **008 (guardrails)** — `EMAIL` is a required `CREATE ACCOUNT` parameter, sourced from
  `spec.contacts[0]` (D-011, Verified). 008 must require a non-empty `contacts[]` at admission — 006
  currently leaves `contacts[]` fully optional with no CEL constraint, so without this an account could
  otherwise reach 010 with nothing to put in `EMAIL`. (Appended to `specs/scope-008-guardrails.md`.)
- **018 (controller)** — 018 computes `status.accountName`/`accountUrl`/`accountLocator` directly from
  the `ModuleContext` it already owns (D-003/D-004), never from a module `Outcome` payload; it should
  persist `status.accountLocator` as promptly as possible after `Apply` returns, since every reconcile
  between a successful `CREATE ACCOUNT` and that persist is the D-007 crash window. (Appended to
  `specs/scope-018-snowflakeaccount-controller.md`.)

## References

- **Product design**: `specs/design.md` §3.2, §3.6, §3.11, §3.11.1, §3.12, Appendix B (X1) — the
  authoritative source this scope note and this record were derived from.
- **Scope note**: `specs/scope-010-account-module.md` — the starting point for this run; superseded on
  every point above where the two disagree.
- **Dependency code read in full**: `internal/secrets/backend.go`, `path.go`, `credentials.go`, `fake.go`
  (003); `internal/secrets/aws/backend.go` (003.a); `internal/snowflake/pool/pool.go`, `connect.go` (004);
  `internal/snowflake/host/host.go` (004); `internal/snowflake/statement/statement.go`, `render.go`,
  `errors.go` (005); `internal/tenant/labels.go`, `naming.go`, `url.go` (006, via spec text); `internal/
  backplane/backplane.go` (007); `internal/account/context.go`, `module.go`, `pipeline.go`, `conditions.go`
  (009); `internal/config/config.go` (002).
- **Vendor verification**: Context7 (`mcp__context7__resolve-library-id`) — HTTP 403 on every attempt
  across both runs, unavailable this session. Superseded by Snowflake reference pages fetched directly:
  https://docs.snowflake.com/en/sql-reference/sql/create-account (required/optional parameters, full
  syntax block — D-011, D-012), https://docs.snowflake.com/en/sql-reference/sql/show-regions
  (`snowflake_region` column description only; no example rows available — O-003 stays open), and
  https://docs.snowflake.com/en/sql-reference/sql/show-accounts (`account_locator`/`account_name`
  columns and `LIKE`'s wildcard-only, no-`ESCAPE` semantics — D-002).
