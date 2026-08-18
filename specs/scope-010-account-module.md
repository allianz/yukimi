> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `010`'s intended
> *scope*, not its content. When writing `010-account-module.md`, the sole sources of truth
> are `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or
> discard anything below freely. Please keep this file up to date until
> `010-account-module.md` has been written, then delete it.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003-a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003-a` < `003-b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Package: `internal/account/modules/account/`. Covers design 3.6 step 1.
  Depends on: 003, 004, 005, 006, 007, 009. 007 is needed because `url.go` (006) has to know whether
  the region has PrivateLink enabled, and this module reads that off the region entry 009 puts in the
  shared context rather than loading the backplane config itself. Two things the context also carries
  are deliberately unused here: the guardrail verdict (008) — nothing this module emits is
  guardrailed, the account `COMMENT` comes straight from the CRD's `description` — and the namespace
  labels (006's `labels.go`). Re-checking the region's `available` gate is 018's validation phase, not
  this module's. 005 stays on the list even though 009 dropped it: the pipeline executes no SQL, and
  this module executes its own.
- Scope:
  - Generate the RSA keypair and **store it through 003 before** issuing `CREATE ACCOUNT`, so that a
    failure cannot orphan an account whose credentials were never persisted. Call 003's
    `CreateOrRecover`, not a bare `Create`/`Update` — it already resolves both non-atomic-retry
    collisions 003 defines (delete-then-recreate: purge and generate fresh; interrupted retry: reuse
    what's already stored) and never fails loudly on its own. This module supplies the other half of
    that contract, per 003's own worked example: combine `CreateOrRecover`'s `existed` flag with
    `Observe`'s verdict on whether the account already exists in Snowflake. `existed` with no live
    account is the safe interrupted-retry case (reuse and proceed to `CREATE ACCOUNT`); `existed` with
    a live account is the one case this module itself must fail loudly on, since it means a live
    account's credential is about to be regenerated. Which store is behind 003 is not this module's
    business — 003-a today.
  - `CREATE ACCOUNT '<resolved-name>' ADMIN_NAME='platform'
    ADMIN_RSA_PUBLIC_KEY='<generated>' ADMIN_USER_TYPE='SERVICE' EDITION='ENTERPRISE'
    REGION='<region-from-crd>' COMMENT='<description-from-crd>'`, issued over the **org-admin**
    connection. This is the only module that needs org-level privileges.
  - Capture the account locator and pass it to 006's `url.go` — that spec already assigns the call to
    this module, since the locator is opaque and cannot be derived. How to capture it is a choice for
    spec-writing time: `CREATE ACCOUNT`'s own result set is one source, but its format isn't reliably
    documented, so issuing `SHOW ACCOUNTS LIKE '<resolved-name>'` right after a successful `CREATE
    ACCOUNT` — the same statement `Observe` below already uses — is an equally fine way to get it. The
    resolved name and the built URL are **returned in the module result**, not written to the
    resource: 009 keeps per-module results out of `status` and leaves rendering them into
    `status.accountName`, `status.accountUrl` and the conditions to 018.
  - **Which of 009's four outcomes this module returns**: `Done`, `Failed(systemErr)`, and
    `Rejected(userErr)` for an org-wide account-name collision — the one failure here a tenant can
    fix, by renaming the CRD. `CREATE ACCOUNT` is synchronous, so there is no `Pending`. The module
    does **not** signal an abort: 009 dropped the abort flag from `Failed` and treats a failure of
    this module as the pipeline's single structural stop, so returning the outcome is all it does.
  - Drift / `Observe`: does the account exist under its resolved name.
    - `Observe` is **mandatory** for this module rather than optional (009 leaves that open in
      general): the pipeline's abort decision hangs on its answer. The resolved name itself is
      neither recomputed by this module nor unknown to it — per 009's shared context, it's
      resolved once (006) and passed down the pipeline alongside the CRD and status, and it also
      ends up in `status.accountName` (rendered by 018). But knowing the name doesn't say whether
      an account under it currently exists in Snowflake, which is the one fact no context value or
      status field can stand in for — so existence has to be probed fresh on every reconcile
      regardless of where the name came from.
    - The probe: authenticate as `platform` through 004, not `SHOW ACCOUNTS` over org-admin — org-admin
      is reserved for `CREATE ACCOUNT`/`DROP ACCOUNT` alone ("Not this module's job" below), and every
      module downstream of this one already needs a `platform` connection, so `Observe` reuses that
      same path rather than opening a more-privileged one just to check existence.
    - `Observe` is also the guard that keeps `Apply`'s create-only keypair store from firing against a
      live account, so it must separate three states: no credentials stored at all (nothing to
      authenticate with — create both keypair and account); credentials stored and platform auth
      succeeds (`Done`, no writes); credentials stored but platform auth fails. That third state is
      **not** treated as "the account was deleted, recreate it" — an account disappearing outside the
      platform's own `DROP ACCOUNT` flow (017/018) is not a normal condition, so the correct behavior
      is `Failed(systemErr)`, surfaced on status, never a silent re-`CREATE ACCOUNT` that would risk
      replacing a key a live account may still authenticate with.
- Not this module's job:
  - **No teardown.** It owns no `DROP ACCOUNT` and no keypair disposal, even though it creates both:
    6.3 Phase 3 belongs to 017/018, and 009 states there is no teardown half. Restating 3.11's
    privilege split precisely — this is the only module that takes the org-admin connection, and it
    takes it only for `CREATE ACCOUNT`; `DROP ACCOUNT` runs on the same connection but is issued from
    018.
  - **No `IdentitySyncRequest` emission.** 4.3 wants requests emitted "on first observation, alongside
    bootstrapping", which reads like this module's job, and 009 lists the owner as an open question.
    Ordering settles it: 014 is above 010, so this module cannot import the emitter. Emission — and the
    `status.identitySyncStartedAt` stamp that goes with it — has to land in 015 or 018.
- Note: the `platform` user created here is how the platform reaches the account for every subsequent
  operation. Appendix B X1 records that a tenant holding `ACCOUNTADMIN` can drop or re-key it.
- Open question for this spec: what this module returns for the URL on a reconcile where the account
  already exists. There is no `CREATE ACCOUNT` result then, hence no locator, so it returns no URL and
  018 must not blank an existing `status.accountUrl` — unless the locator is recovered, either by a
  status field of its own (the open question `scope-006` already raises) or by re-deriving it from
  `SHOW ACCOUNTS`.
- Open question for this spec (pre-existing, but newly actionable): the `REGION` literal. 3.6's SQL
  says `REGION='<region-from-crd>'`, but the CRD value is a config key (`aws-eu-central-1`) while
  `CREATE ACCOUNT` expects Snowflake's own region identifier, and neither `design.md` nor 007's schema
  records a mapping. Now that 009 hands this module the region entry, that entry is the natural place
  for such a field — a question for 007 and 010 to settle, not a field to invent here.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).
