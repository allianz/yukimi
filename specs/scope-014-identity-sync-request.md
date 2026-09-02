> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `014`'s intended
> *scope*, not its content. When writing `014-identity-sync-request.md`, the sole sources of
> truth are `specs/design.md` and the prompt given at spec-writing time — rework, restructure,
> or discard anything below freely. Please keep this file up to date until
> `014-identity-sync-request.md` has been written, then delete it.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003.a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003.a` < `003.b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Packages: `apis/identity/v1alpha1/` (IdentitySyncRequest), `internal/identitysync/`. Covers design
  4.1–4.3. Depends on: 001, 006, 007.
- Concept: before groups can be imported into a local account they must already exist in the central
  Snowflake **organization** account. Where the enterprise does not sync them there by default, the
  platform emits an `IdentitySyncRequest` as a standardized, decoupled interface. **This platform
  ships the emitting side and the contract only** — a company-specific controller fulfills it, which
  is why the resource lives in its own API group (`base.identity.yukimi.io`) rather than alongside the
  Snowflake kinds.
- Scope — the CRD: `spec.provider` (the integration key from `identityIntegration.groups`) and
  `spec.groups[]`, plus the `Ready` contract through which the fulfilling controller reports.
- Scope — emitter behavior:
  - **Gated on config**: requests are emitted only when the base config's `identitySync.enabled` is true.
    When it is false, groups are assumed to be present org-wide already and are imported directly.
  - **One request per integration key**: each key under `identityIntegration.groups` yields its own
    request, named `<crd-name>-<provider>-identities` in the account's own namespace. It is derived
    from the CRD's `metadata.name` rather than the resolved name, which contains underscores that
    RFC1123 forbids.
  - **Emitted early and never blocking**: requests go out on first observation of the
    SnowflakeAccount, *alongside* bootstrapping rather than after it, so that a sync measured in tens
    of minutes overlaps account creation. The controller returns immediately and picks up progress on
    later reconciles — it is a passive observer and never waits inside a reconcile.
  - **Existence is desired state**: each request carries an owner reference so that it is
    garbage-collected with the account; a request deleted while its groups are still needed is
    recreated; and removing an integration key from the CRD deletes its request for good.
  - **Grace period**: while a request is outstanding and within the base config's `identitySync.timeout` (default 1h)
    the reason is `SyncPending` — an expected provisioning state with **no** warning event. Past the
    timeout it becomes `SyncTimeout` **with** a warning event so that ops can see the stall. The
    clock starts when the account's first request is emitted and is recorded in status. `SyncTimeout`
    is a reporting state, not a stop: reconciliation continues and returns to success on its own if
    the sync lands.
  - Only `Ready=True` is consumed; asynchronous fulfilment is the expected case, not a fault.
- **Blocker**: this group cannot be scaffolded. `hack/helpers/addtype.sh` emits
  `package {{GROUP}}`, so `GROUP=base.identity` yields `package base.identity`, which does not
  compile. Hand-create `apis/identity/v1alpha1/` with `Group = "base.identity.yukimi.io"`. The CRD
  must still ship in `package/crds/` even though no controller in this repository fulfills it.

## Cross-cutting context from the roadmap

- **Why 014 sits directly below 015, its only consumer.** Nothing in specs 009–013 touches it, so
  deferring it keeps the identity concern contiguous (emit → wait on `Ready` → import) and avoids
  building an emitter before its consumer exists.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).

## Raised by the 009 clarification

Recorded by `/yukimi.clarify 009`. `specs/009-account-pipeline.md` now carries the pipeline-wide rules
these points rest on.

- **`BaseConfig` has no `identitySync` section yet, and 014 must add it.** Design 4.3 places
  `identitySync.enabled` and `identitySync.timeout` in `baseConfig.yaml`, but 002 was written without
  them — `internal/config/base/base.go`'s `BaseConfig` carries only `Snowflake`, `AWS` and `Secrets`
  (verified against the code). 014 therefore extends `internal/config/base`, which no earlier spec does.
  Left open by the 009 clarification: decide and state what an absent or disabled `identitySync` means
  for the `IdentitySynced` condition.
- **015 calls the emitter, not 018.** Emission of `IdentitySyncRequest` is owned by the identity
  module, because emission and import are two halves of one concern and share the same
  `Pending`/timeout accounting. 014 owns the CRD contract and the emitter API; it must be callable from
  inside a pipeline module, i.e. from a module `Apply` holding only the shared context.
- **Worth confirming with whoever fulfils the request**: because 015 runs after 010 and 010 aborts the
  run on any non-`Done` outcome, nothing is emitted while `CREATE ACCOUNT` keeps failing. If a request
  naming a not-yet-existing account is harmless to the fulfilling controller, emission could move
  earlier cheaply; if it is not, the current ordering is required rather than merely accepted.
