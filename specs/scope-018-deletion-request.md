> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `018`'s intended
> *scope*, not its content. When writing `018-deletion-request.md`, the sole sources of truth
> are `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or
> discard anything below freely. Please keep this file up to date until
> `018-deletion-request.md` has been written, then delete it.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003.a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003.a` < `003.b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Packages: `apis/base/v1alpha1/` (SnowflakeDeletionRequest), `internal/deletion/`,
  `internal/controller/snowflakedeletionrequest/`. Covers design 6.1–6.3. Depends on: 001, 006.
- Concept — **Positive Control, a "two-key" system**: a resource cannot be destroyed merely by
  deleting its definition file. Deletion is a privileged operation requiring a dedicated "deletion
  warrant". The lock is a finalizer; the key is a `SnowflakeDeletionRequest` authorizing the
  destruction of one specific target. This exists to prevent catastrophic data loss through
  accidental Git operations.
- Scope:
  - CRD: `spec.targetRef{kind, name}` (the name is the CRD name, not the resolved name),
    `spec.duration` (a maintenance window, maximum 8h), and `spec.reason` (for example a ticket
    number). Status: `validUntil` and `state`.
  - **Phase 1 — validation**: verify `duration` ≤ 8h, compute `status.validUntil` from the creation
    timestamp, and set `status.state = Active`. Once `validUntil` passes unused the state becomes
    `Expired` and it no longer authorizes anything — a new request is required. **Time-boxing**
    prevents long-standing dangling permissions.
  - The lookup used by 019: find an `Active` request in the same namespace targeting a specific
    resource.
  - The status transition to `Consumed` after a successful deletion, which is written by 019.
  - **A durable audit trail**: the request outlives its target, linking the destruction to a reason
    and a timeframe for compliance.
  - Validate `targetRef.kind` against an explicit allowlist. Recommendation: **`SnowflakeAccount`
    only for v1alpha1** — dropping a replication group destroys no data — so that widening later is
    purely additive. Design 6.2 says "every critical resource", but 6.3 only describes the account
    interaction.
- **Open question to raise when writing this spec**: design 6.2 names a `snowflake.finalizer`, but
  Crossplane's managed reconciler already owns `finalizer.managedresource.crossplane.io` and will not
  remove it until `Delete` returns success. The block is therefore naturally implemented as a
  `Delete` that returns a user error when no Active warrant exists — and `Terminating`,
  `DeletionBlocked` and `Ready=False` all follow from that. A second finalizer would add a second
  removal path to get wrong. This is likely a design.md correction.

## Cross-cutting context from the roadmap

- **Why 018 sits below 019.** Per design 6.3 Phase 3 the account controller both reads the deletion
  warrant and writes its `Consumed` status, so it imports `internal/deletion`. The dependency is
  one-way; no injected interface is needed.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).
