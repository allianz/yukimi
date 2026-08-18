> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `007`'s intended
> *scope*, not its content. When writing `007-backplane-config.md`, the sole sources of truth are
> `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or discard
> anything below freely. This file does not need to be kept up to date, and should be deleted
> once `007-backplane-config.md` has been written.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003.a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003.a` < `003.b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Package: `internal/backplane/`. Covers design 3.5. Depends on: 001, 002.
- Concept: the platform pre-provisions network infrastructure **once per region** as a shared
  "backplane" (PrivateLink per region, wildcard DNS, global SSO, centrally hardened policies), so
  that new accounts attach to already-live infrastructure via SQL instead of requiring a per-account
  infrastructure project. Bringing a region online is a manual ops job: run Terraform, close the DNS
  and VPC-endpoint-acceptance tickets, test, then record the outputs here with `available: true`.
- Scope — a ConfigMap loader (`loader.go`) plus lookup and validation over this schema:
  - `globalParameters` — the org-wide security baseline applied to every account (for example
    `PREVENT_UNLOAD_TO_INLINE_URL`, `REQUIRE_STORAGE_INTEGRATION_FOR_STAGE_CREATION`).
  - `regions{<region>: …}`, each holding:
    - `available` — a **controller-side gate with no Snowflake counterpart**, letting ops stage a
      region and reject CRDs naming it until it is officially offered.
    - `inventory[{connection, type, vpceId, maxCidrs[]}]` — the catalog of physical ingress paths.
      Listing a connection only makes its handle referenceable and caps how wide it may ever be
      opened; it grants nothing by itself. `type` is for example `AWSVPCEID` or `IPV4`. VPCE-only
      connections (such as `dbt-cloud`) have no `maxCidrs` and no IPs to manage.
    - `regionalParameters` — region-specific account parameters taken from Terraform outputs (for
      example `ENABLE_INTERNAL_STAGES_PRIVATELINK`, `S3_STAGE_VPCE_DNS_NAME`).
    - `regionalAllowlist[{connection, allowedIPs[]}]` — the mandatory baseline access applied to
      every account in the region, guaranteeing basic reachability (browser logins) before any custom
      rules are considered. Omitting `allowedIPs` inherits the connection's full `maxCidrs`.
  - Region lookup by the CRD's `region` field, and connection lookup by name within a region's
    inventory.
  - A **CIDR containment helper** (`allowedIPs` ⊆ `maxCidrs`), reused by 012.
  - Load-time validation: every `regionalAllowlist` connection exists in that region's `inventory`,
    and each `allowedIPs` entry falls inside its connection's `maxCidrs`.
- Out of scope: applying any of it — parameters go to 011 and network rules to 012.

## Cross-cutting context from the roadmap

- **Decision — guardrails, approved exceptions and backplane config all live in mounted ConfigMaps.**
  Specs 007 and 008 own no `apis/` types — only a loader plus validation and evaluation logic.
- **Deliberately unnumbered — 3.11.2 OIDC** (a design TODO). An optional additional authentication
  path: a per-namespace Kubernetes ServiceAccount whose `TokenRequest` JWT maps to a `PLATFORM_OIDC`
  Snowflake user via the `sub` claim, avoiding a secret-store read per connection, with the keypair
  stored through 003 remaining as a fallback. It is blocked on two unresolved questions: the precise
  fallback trigger, and the cluster-scoped RBAC model that would let the controller mint tokens across
  every tenant namespace. It would also add an org-level issuer/JWKS entry to this spec's schema and a
  ServiceAccount lifecycle. It takes the next free spec number when its TODO closes, rather than
  reserving a forward reference now.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).
