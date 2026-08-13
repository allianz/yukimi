> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `003`'s intended
> *scope*, not its content. When writing `003-secrets-handling.md`, the sole sources of truth are
> `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or discard
> anything below freely. This file does not need to be kept up to date, and should be deleted
> once `003-secrets-handling.md` has been written.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003-a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003-a` < `003-b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Package: `internal/secrets/`. Covers design 3.11.1, the 3.6 keypair, and Appendix B X1.
  Depends on: 001, 002.
- **Backend-agnostic by construction.** This package names no secret store. It defines the `Backend`
  interface a store implements and writes everything above it — paths, credentials, keys, cache,
  classification — against that interface. AWS Secrets Manager is the reference implementation (003-a)
  because it is what the platform runs on; design 3.11.1's path-based isolation is a requirement any
  backend must satisfy, not an AWS feature.
- Scope:
  - **The `Backend` interface**, deliberately narrow — an opaque byte-blob keystore and nothing more:
    `Get(ctx, path) ([]byte, error)`; `Create(ctx, path, value) error`, which must fail if something is
    already there; `Update(ctx, path, value) error`, which must fail if nothing is there; and
    `Delete(ctx, path) error`. A backend sees paths and bytes. It never parses a credential, never
    caches, never builds a path, and never logs — it returns errors and lets 001 do the reporting.
    `Create` and `Update` are separate rather than one upsert on purpose: 010 must store a keypair
    *before* `CREATE ACCOUNT`, and "create, failing if occupied" has to be atomic in the store, or a
    re-run against a live account silently overwrites the key the platform authenticates with — a
    read-then-write precondition in this package would be a race with no owner. There is deliberately
    **no** `HealthCheck` method: the startup reachability check is "read the org-admin credentials",
    which exercises credentials, region, network and authorization on a path that must work anyway.
  - **Sentinel errors**, the whole vocabulary in which a backend reports failure, so this package can
    classify without knowing a single vendor code. Each is returned wrapped and matched with
    `errors.Is`: `ErrNotFound` (nothing live at that path); `ErrAlreadyExists` (`Create` found
    something there); `ErrPendingDeletion` (something is at that path but inside a deletion recovery
    window — neither readable as live nor recreatable until restored or purged; backends without soft
    delete never return it, and it is separate from `ErrAlreadyExists` because the operator action
    differs: restore or wait, versus investigate a stale credential); `ErrDenied` (not authorized for
    that path — it must **never** be collapsed into `ErrNotFound`, since under 3.11.1 a denial is the
    expected signal for a wrong-tenant path, and folding the two would turn both a misconfigured IAM
    policy and a genuine cross-tenant attempt into a bland "no credentials for this account"); and
    `ErrUnavailable` (transient — throttling, timeout, 5xx, dropped connection). Anything else a
    backend returns is treated as a permanent, unclassified store fault.
  - **Tenant secret path** (3.11.1), constructed strictly as
    `snowflake/tenant/<snowflake-org-name>/<kubernetes-namespace>/<snowflake-account-name>/platform-credentials`.
    Critical detail: `<snowflake-account-name>` is the CRD's `metadata.name`, **not** the resolved
    Snowflake name from 3.12. Every path segment must derive from Kubernetes identifiers so that the
    namespace remains the trust anchor; cross-tenant access then fails in the store's own
    authorization layer on an incorrect path.
  - **Org-admin secret path**: `snowflake/org/<org>/<org-admin-account>/org-admin-credentials`.
  - **Path validation before any backend call**: reject empty segments, `/`, `.`, `..` and anything
    outside the RFC1123 character set the Kubernetes identifiers already guarantee. This belongs here,
    not in a backend, because a store with a flat key space would otherwise let a crafted identifier
    traverse into another tenant's path — the isolation guarantee has to hold for the weakest possible
    backend, not just for the one with hierarchical IAM.
  - **RSA keypair generation** for the `platform` service user created by `CREATE ACCOUNT`:
    `crypto/rand`, minimum 2048-bit, PKCS#8 encoding for private keys and PKIX for public keys.
  - **Credential types** for platform and org-admin credentials, and their stored JSON encoding: the
    public key single-line base64 without PEM delimiters so it drops straight into `CREATE ACCOUNT`
    and `ALTER USER`, the private key PKCS#8 PEM so it goes straight into the Snowflake driver. Both
    shapes are dictated by Snowflake, not by the store, which is why they sit here.
  - **Get / store / rotate operations** over the interface, plus an **in-memory TTL cache** in front
    of every backend with lazy eviction, keyed by path, invalidated on every write and rotation. The
    cache is core rather than per-backend so no backend can forget it and every backend inherits the
    same freshness semantics; without it a store round-trip lands on every reconcile of every account.
  - **Error classification, stated entirely in terms of the sentinels.** `ErrNotFound` on a read and a
    path that cannot be legally constructed are **user errors** — a CRD naming an account whose
    credentials do not exist, or identifiers that cannot form a path, are both fixed by editing the
    CRD. `ErrDenied`, `ErrUnavailable`, `ErrPendingDeletion`, `ErrAlreadyExists` and any unclassified
    store fault are **system errors** carrying 001's incident IDs, because no CRD edit reaches them.
    Which vendor response produces which sentinel is 003-a's problem; this spec never sees a vendor
    code.
  - **An in-repo in-memory `Backend` for tests**, exported from `internal/secrets` — not hidden in a
    `_test.go` file, because 004, 009 and 010 must import it and a fake in a test file is invisible
    outside its own package. A map plus per-method injectable failures so a caller can force every
    sentinel above. This is what keeps the rest of the tree buildable and testable with no AWS account
    and no SDK.
- Out of scope: any concrete store and any vendor SDK — `go.mod` gains no AWS dependency in this spec;
  constructing or selecting a backend (`cmd/provider/main.go`); and pushing a rotated
  public key to Snowflake (`ALTER USER … SET RSA_PUBLIC_KEY`), which needs the pool that does not
  exist yet — the caller does it once 004 lands.
- Appendix B X1 note: once a tenant holds `ACCOUNTADMIN` they can drop or re-key the `platform`
  user, locking the platform out of the account. Record this as a known gap pending Snowflake
  Organization Policies.

## Cross-cutting context from the roadmap

- **Decision — the `origin/secrets-handling` branch is ignored.** That unmerged branch holds a drafted
  `002-secrets-handling.md`, a `002-a-aws-secrets-backend.md` sub-spec and a full `internal/secrets/`
  implementation. It is treated as abandoned — nothing in it is inherited and its numbering does not
  survive; secrets is 003 here and is written fresh. That the branch also split its backend into a
  sub-spec is convergence, not provenance: the `NNN-a` convention used here is defined independently.
- **Decision — `NNN-a` sub-specs exist, but only for a pluggable backend behind an interface owned by
  `NNN`.** Yukimi is open source, and the secret store is the one dependency an adopter is most likely
  to be unable to keep: an operator outside AWS must be able to take the whole platform and replace
  only the place credentials are written. Spec 003 therefore owns `internal/secrets/` — the `Backend`
  interface, the path grammar, the credential types, keypair generation, the cache and the
  error-classification policy — and names no product anywhere; spec 003-a owns `internal/secrets/aws/`
  and is the only place an AWS SDK enters `go.mod`. A Vault backend would be 003-b in
  `internal/secrets/vault/`, and is out of scope now.

  A letter means one thing only: an implementation of an interface defined by the number it hangs
  off. It sorts immediately after its parent and strictly before the next whole number, so the
  ordering rule needs no exception; it may depend on its parent and on anything below it, never on a
  sibling letter or a higher number; and it uses the same `000-template.md` skeleton, named
  `NNN-a-<slug>.md`. Two consequences matter more than the naming. First, **no numbered spec may
  depend on a letter spec** — 004 and 010 depend on 003's interface, so the pool and the account
  module never learn which store is behind it. Second, the concrete backend is selected exactly once,
  in `cmd/provider/main.go`, which no numbered spec owns: it reads `secretsBackend` from 002,
  constructs the named backend, and injects it. That is what keeps the swap a one-file change.

  A letter is not a device for splitting a spec that grew long. A spec that is merely large stays one
  spec; a spec that is genuinely two concerns takes two numbers.
- **Why 003 never imports the pool.** Pushing a rotated public key (`ALTER USER … SET RSA_PUBLIC_KEY`)
  is explicitly out of scope for 003 — the caller does it. The pool imports secrets, never the reverse.
- **Why `internal/secrets` must never import `internal/secrets/aws`.** 003 defines the backend
  interface, 003-a implements it. The child imports the parent for the interface and the sentinel
  errors, so the reverse import is not merely a layering violation — it is a compile error, and the
  obvious workaround (a registry inside `internal/secrets` that knows its own backends) is the cycle
  wearing a hat. Selection happens once, in `cmd/provider/main.go`, which no numbered spec owns.
- **Why nothing above 003 depends on 003-a.** 004 and 010 take 003's interface, so the pool and the
  account module stay unit-testable against the in-memory fake 003 ships — no AWS account, no SDK, no
  network. An import of `internal/secrets/aws` anywhere outside `cmd/provider/` is a bug, and it is
  the single grep that proves the store is still pluggable.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).
