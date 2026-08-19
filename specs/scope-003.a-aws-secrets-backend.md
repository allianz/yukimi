> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
<<<<<<< HEAD:specs/scope-003.a-aws-secrets-backend.md
> into numbered specs. It exists only to give a starting-point idea of spec `003.a`'s intended
> *scope*, not its content. When writing `003.a-aws-secrets-backend.md`, the sole sources of truth
> are `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or discard
> anything below freely. This file does not need to be kept up to date, and should be deleted
> once `003.a-aws-secrets-backend.md` has been written.
=======
> into numbered specs. It exists only to give a starting-point idea of spec `003-a`'s intended
> *scope*, not its content. When writing `003-a-aws-secrets-backend.md`, the sole sources of
> truth are `specs/design.md` and the prompt given at spec-writing time — rework, restructure,
> or discard anything below freely. Please keep this file up to date until
> `003-a-aws-secrets-backend.md` has been written, then delete it.
>>>>>>> origin:specs/scope-003-a-aws-secrets-backend.md

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003.a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003.a` < `003.b` < `004`), so
"strictly below" still holds unchanged and no exception is needed. `003.a` is exactly this: an
implementation of the `Backend` interface owned by spec `003`.

## Roadmap's original scope notes

- Package: `internal/secrets/aws/`. Implements 003's `Backend` against AWS Secrets Manager — the
  reference backend for design 3.11.1. Depends on: 001, 002, 003. Nothing depends on it.
- Why a letter and not a whole number: it introduces no platform concept and no new consumer. It is
  one implementation of an interface that already exists, and it must be replaceable
  without renumbering anything downstream. It is written and implemented immediately after 003 — the
  split is about replaceability, not about deferring work, since 003's fake keeps tests green but no
  provider can actually reach Snowflake until this lands.
- Scope:
  - **SDK client construction**: `aws-sdk-go-v2` plus `service/secretsmanager`. This is the only spec
    in the tree that adds an AWS dependency to `go.mod`. Region comes from 002's `AWS_REGION` and an
    empty region is a user error from the constructor, so a mis-set ConfigMap fails at startup rather
    than on the first reconcile. Credentials come from the SDK's default chain and nowhere else, so
    IRSA in-cluster and `AWS_PROFILE` locally are the same code path. The constructor makes no API
    call.
  - **Create-vs-update semantics**, mapped onto AWS's two distinct APIs: `Create` is `CreateSecret`
    and never `PutSecretValue`, so a collision is reported by AWS atomically rather than silently
    overwriting a live tenant's key; `Update` is `PutSecretValue` on an existing secret, which adds a
    version and leaves the previous one reachable as `AWSPREVIOUS` — the property that makes a botched
    rotation survivable. 003's value is a string, so both write APIs carry it in `SecretString` and
    never `SecretBinary`, and `Get` is `GetSecretValue` reading that same `SecretString` back.
  - **Soft delete and the recovery window**: `DeleteSecret` is always called *without*
    `ForceDeleteWithoutRecovery`, leaving the default 30-day window. This backend never calls
    `RestoreSecret` automatically — resurrecting a credential the platform deliberately retired is an
    operator's decision, not a reconcile's. A `CreateSecret` onto a path whose predecessor is still
    inside its window fails and is reported with the path and the vendor's own message — the recovery
    window gets no special-cased handling of its own.
  - **Error reporting**: every failing AWS call is returned as an ordinary error wrapped with the
    operation and the path that produced it, keeping the vendor error in the chain by `%w` so its code
    still reaches the log through 001's incident ID. The backend translates nothing, matches nothing,
    and makes **no** user/system classification decision of its own — 003 treats every store fault as
    a system error.
  - **IAM policy expectations for 3.11.1**, documented rather than shipped: the resource ARN patterns
    the controller's role must be granted per path prefix
    (`arn:aws:secretsmanager:<region>:<account>:secret:snowflake/tenant/<org>/*`, with the org-admin
    path separately so org credentials can be granted more narrowly than tenant ones). Two facts the
    spec must state because both are easy to get wrong and neither fails loudly: ASM appends a random
    six-character suffix to every secret ARN, so a policy resource must end in `-??????` or `*` or it
    matches nothing; and the controller's role is **not** per-tenant scoped — one controller serves
    every namespace — so path-based isolation here defends against the controller constructing a wrong
    path, not against a compromised controller. 3.11.2's OIDC path exists precisely because that
    second guarantee is the weaker one.
  - **Integration-test requirements**: these tests are the only place an AWS account is needed, they
    run under `make test-integration` only (skipped by `-short`), and they are driven by `.env`
    (`AWS_REGION`, `AWS_PROFILE`, `SAMPLE_CUSTOMER_NAMESPACE`, `SAMPLE_CUSTOMER_ACCOUNT`). They must create
    and clean up under a dedicated test path prefix, and they must account for the recovery window: a
    test that deletes and immediately recreates the same path fails, since AWS still holds the
    soft-deleted secret, so either every run uses a unique path or cleanup — and only cleanup — uses
    `ForceDeleteWithoutRecovery`. The tests that cover error reporting need no AWS account; they run
    against a fake of the `secretsmanager` API surface.
- Out of scope: path construction, caching, keypair generation, the credential's own encoding (003
  owns the JSON shape of the string it hands down; this backend only decides how that string is
  persisted) and the user/system classification policy (all 003); any other backend; backend selection
  (`cmd/provider/main.go`); and the IAM policy as a deployable artifact — ops owns that, this spec
  documents the grants it requires.
- Open question for this spec: whether `CreateSecret` should pass a customer-managed `KmsKeyId` and
  ops-defined tags. AWS-managed encryption and no tags are adequate for v1alpha1, but if ops needs
  either for key rotation or cost attribution, this is where it lands — and `KmsKeyId` would become
  another 002 field carried but not interpreted.

## Cross-cutting context from the roadmap

- **Decision — the `origin/secrets-handling` branch is ignored.** That unmerged branch holds a drafted
  `002-secrets-handling.md`, a `002-a-aws-secrets-backend.md` sub-spec and a full `internal/secrets/`
  implementation. It is treated as abandoned — nothing in it is inherited and its numbering does not
  survive.
- **Decision — `NNN.a` sub-specs exist, but only for a pluggable backend behind an interface owned by
  `NNN`.** Spec 003 owns `internal/secrets/` — the `Backend` interface, path grammar, credential
  types, keypair generation, cache and error-classification policy — and names no product anywhere;
  spec 003.a owns `internal/secrets/aws/` and is the only place an AWS SDK enters `go.mod`. A Vault
  backend would be 003.b in `internal/secrets/vault/`, and is out of scope now.

  A letter means one thing only: an implementation of an interface defined by the number it hangs
  off. It sorts immediately after its parent and strictly before the next whole number; it may depend
  on its parent and on anything below it, never on a sibling letter or a higher number; and it uses
  the same `000-template.md` skeleton, named `NNN.a-<slug>.md`. **No numbered spec may depend on a
  letter spec** — 004 and 010 depend on 003's interface, so the pool and the account module never
  learn which store is behind it. The concrete backend is selected exactly once, in
  `cmd/provider/main.go`, which no numbered spec owns: it reads `secretsBackend` from 002, constructs
  the named backend, and injects it. That is what keeps the swap a one-file change.
- **Why `internal/secrets` must never import `internal/secrets/aws`.** The child imports the parent
  for the interface and the `Path` type, so the reverse import is a compile error, not merely a
  layering violation — and the obvious workaround (a registry inside `internal/secrets` that knows its
  own backends) is the cycle wearing a hat.
- **Why nothing above 003 depends on 003.a.** 004 and 010 take 003's interface, so the pool and the
  account module stay unit-testable against the in-memory fake 003 ships — no AWS account, no SDK, no
  network. An import of `internal/secrets/aws` anywhere outside `cmd/provider/` is a bug, and it is
  the single grep that proves the store is still pluggable.
- **Deliberately unnumbered — a second secret backend.** A Vault-style backend (`003.b`, in
  `internal/secrets/vault/`) is deliberately not planned. This decision reserves its shape but not the
  work: nothing in the tree needs it, and specifying a backend nobody runs would fix 003's interface
  against an imagined store instead of a real one. The interface earns its keep by staying small
  enough that adding one later is a self-contained job behind four methods.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).
