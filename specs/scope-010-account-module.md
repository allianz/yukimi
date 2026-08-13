> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `010`'s intended
> *scope*, not its content. When writing `010-account-module.md`, the sole sources of truth are
> `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or discard
> anything below freely. This file does not need to be kept up to date, and should be deleted
> once `010-account-module.md` has been written.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003-a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003-a` < `003-b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Package: `internal/account/modules/account/`. Covers design 3.6 step 1.
  Depends on: 003, 004, 005, 006, 009.
- Scope:
  - Generate the RSA keypair and **store it through 003 before** issuing `CREATE ACCOUNT`, so that a
    failure cannot orphan an account whose credentials were never persisted. Use 003's create
    operation, not its update operation: a re-run that finds credentials already at the path must fail
    loudly rather than replace the key a live account authenticates with. Which store is behind 003 is
    not this module's business — 003-a today.
  - `CREATE ACCOUNT '<resolved-name>' ADMIN_NAME='platform'
    ADMIN_RSA_PUBLIC_KEY='<generated>' ADMIN_USER_TYPE='SERVICE' EDITION='ENTERPRISE'
    REGION='<region-from-crd>' COMMENT='<description-from-crd>'`, issued over the **org-admin**
    connection. This is the only module that needs org-level privileges.
  - Capture the account locator `CREATE ACCOUNT` returns and pass it to 006's `url.go`. Write
    `status.accountName` (the resolved name) and `status.accountUrl` (built from the locator).
  - Drift / `Observe`: does the account exist under its resolved name.
- Note: the `platform` user created here is how the platform reaches the account for every subsequent
  operation. Appendix B X1 records that a tenant holding `ACCOUNTADMIN` can drop or re-key it.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).
