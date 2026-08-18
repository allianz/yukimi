> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `015`'s intended
> *scope*, not its content. When writing `015-identity-module.md`, the sole sources of truth are
> `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or discard
> anything below freely. This file does not need to be kept up to date, and should be deleted
> once `015-identity-module.md` has been written.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003.a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003.a` < `003.b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Package: `internal/account/modules/identity/`. Covers design 3.7 and the 4.3 waiting rules.
  Depends on: 005, 007, 009, 014.
- Scope:
  - `ALTER ACCOUNT ADD ORGANIZATION USER GROUP '<group>'` per group under
    `identityIntegration.groups`, then `GRANT ROLE <system-role> TO ROLE "<group>"` per `roleBindings`
    entry. This is what lets users log in via SSO with their existing company roles carried over.
  - `ACCOUNTADMIN` must be bound, so that the account is manageable after creation.
  - **Incremental import**: as each `IdentitySyncRequest` reports `Ready=True`, that provider's
    groups are imported and every `roleBindings` entry whose group is now present is granted. A slow
    provider must not hold back a fast one.
  - Returns `Pending` — never `Failed` — while requests are outstanding. These statements only
    succeed once the groups exist in the org account, so the account waits rather than failing.
  - Aggregates the `IdentitySynced` condition: `True` once every request is fulfilled and every group
    imported. Per 009, `IdentitySynced=False` holds `Ready=False`, carrying either `SyncPending` or
    `SyncTimeout` to distinguish a benign wait from a provisioning failure.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).
