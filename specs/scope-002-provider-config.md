> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `002`'s intended
> *scope*, not its content. When writing `002-provider-config.md`, the sole sources of truth are
> `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or discard
> anything below freely. This file does not need to be kept up to date, and should be deleted
> once `002-provider-config.md` has been written.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003-a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003-a` < `003-b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Package: `internal/config/`. Not described in design.md — the schema is derived from
  `.env.example`. Depends on: 001.
- Scope:
  - Load provider-wide settings from a **mounted ConfigMap** at startup and expose them as an
    immutable struct.
  - Fields (from `.env.example`): `SNOWFLAKE_ORG` (the organization name, used in account
    identifiers, secret paths and `accountUrl`), `SNOWFLAKE_ORG_ADMIN_ACCOUNT` (the account used for
    org-level operations), `SNOWFLAKE_USE_PRIVATELINK` (affects the connection host), and
    `secretsBackend` — the name of the secret store to construct, `aws` today. The last has no
    `.env.example` counterpart: it is the first field that exists because of the architecture
    rather than because of the integration-test environment.
  - **Backend-specific settings are carried, not interpreted.** `AWS_REGION` stays in the schema but
    is consumed by 003-a, not by 003 and not by 002's own logic: 002 exposes it verbatim and does
    *not* conditionally require it, because a loader that validates per backend name has started to
    know what backends are. 003-a's constructor rejects an empty region as a user error instead, and
    since `cmd/provider/main.go` builds the backend during startup, that is still fail-fast — nothing
    reaches a reconcile. A future `secretsBackend: vault` (003-b) adds its fields the same way.
    `main.go` rejects an unrecognized `secretsBackend` with a fatal user error listing the names
    compiled in.
  - References to the other three ConfigMaps (backplane, guardrails, exceptions) so that specs 007
    and 008 know what to read.
  - Validation of required fields, raising `errors.NewUserError` for missing or malformed values.
    Fail fast at startup rather than once per reconcile.
- Out of scope: no CRD, no controller, no reconciler, no Kubernetes watch. This is not a Crossplane
  ProviderConfig; the inherited ProviderConfig boilerplate has been removed.
- Why it sits here: it is a leaf, and 003, 003-a and 004 all need values from it.
- Open question for this spec: the ConfigMap's key style. `.env.example` uses `SHOUTY_SNAKE`, while the
  other three ConfigMaps (007, 008) use camelCase. `secretsBackend` is the first key with no
  `.env.example` ancestor, so it forces a choice rather than inheriting one. Pick one convention and
  state it; field names in this entry are written in whichever style their origin used and should be
  normalized when the spec is written.

## Cross-cutting context from the roadmap

- **Decision — provider settings come from a mounted ConfigMap, not a ProviderConfig CRD.** Spec 002
  is a plain loader — no CRD, no controller, no reconciler, no singleton wiring.
- **Decision — the inherited ProviderConfig boilerplate was removed outright.** `apis/v1alpha1/types.go`,
  `internal/controller/config/` and the four `package/crds/` files that came from the Crossplane
  provider template have been deleted. Provider settings go to a mounted ConfigMap read by
  `internal/config/` (spec 002) instead — no CRD, no controller, no reconciler — so the
  ProviderConfig path would never have been revisited. No spec depends on it.
- **Why 002 is a leaf in the dependency graph.** As a ConfigMap loader it imports only
  `internal/errors` — it constructs no backends and runs no controller. Secrets (003) needs the
  organization name for its paths, the AWS backend (003-a) needs the region, and the pool (004)
  needs the org name too, so it must sit below all three. It also carries `secretsBackend`, the
  name of the store to build; 002 only *reports* that string, and the switch on it lives in
  `cmd/provider/main.go`.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).
