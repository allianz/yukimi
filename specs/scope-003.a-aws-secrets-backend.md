> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `003.a`'s intended
> *scope*, not its content. When writing `003.a-aws-secrets-backend.md`, the sole sources of truth
> are `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or discard
> anything below freely. This file does not need to be kept up to date, and should be deleted
> once `003.a-aws-secrets-backend.md` has been written.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time in ascending order, and **a spec may depend only on
specs numbered strictly below it**. A letter suffix marks a pluggable backend implementing an
interface owned by the bare number, and sorts between its parent and the next whole number
(`003` < `003.a` < `004`). `003.a` is exactly that: one implementation of the `Backend` interface
owned by spec `003`.

## Roadmap's original scope notes

- Package: `internal/secrets/aws/`. Implements 003's `Backend` — four methods, string-valued —
  against AWS Secrets Manager, the reference backend for design 3.11.1. Depends on: 001, 002, 003.
  Nothing depends on it but `cmd/provider/main.go`.
- Why a letter and not a whole number: it introduces no platform concept and no new consumer. It is
  one implementation of an interface that already exists, and it must be replaceable without
  renumbering anything downstream. It is written and implemented immediately after 003 — the split is
  about replaceability, not about deferring work, since 003's fake keeps tests green but no provider
  can actually reach Snowflake until this lands.
- **This is a thin spec by design.** 003 owns every decision worth arguing about; what is left here
  is four method bodies, each a single AWS API call, plus a constructor. If the spec grows a state
  machine, an error-matching table, or a recovery concept, something that belongs to 003 has leaked
  down into it.
- Scope:
  - **SDK client construction**: `aws-sdk-go-v2` plus `service/secretsmanager`. This is the only spec
    in the tree that adds an AWS dependency to `go.mod`. Region comes from 002's
    `BaseConfig.AWS.Region` and an empty region is a user error from the constructor, so a mis-set
    ConfigMap fails at startup rather than on the first reconcile. 002's optional
    `BaseConfig.AWS.KmsKeyId` is carried through to `CreateSecret`'s `KmsKeyId` when non-empty and
    otherwise ignored, so AWS-managed encryption stays the default. Credentials come from the SDK's
    default chain and nowhere else, so IRSA in-cluster and `AWS_PROFILE` locally are the same code
    path. The constructor makes no API call. (002's Example 1 still shows `New(cfg.AWS.Region)` with
    the region alone; the exact signature that also carries the KMS key id is this spec's to settle.)
  - **Method mapping** — the whole implementation: `Get` is `GetSecretValue`, `Create` is
    `CreateSecret`, `Update` is `PutSecretValue`, `Delete` is `DeleteSecret`. 003's value is a string,
    so both write APIs carry it in `SecretString` and never `SecretBinary`, and `Get` reads that same
    `SecretString` back.
  - **Why the mapping needs no error inspection**: each of those APIs already fails under exactly the
    condition 003's per-method contract requires — `CreateSecret` on an occupied name, `GetSecretValue`
    and `PutSecretValue` on a name that does not exist. Since 003 has no sentinel errors and no caller
    branches on an error's identity, this backend never matches an AWS error code: it wraps and
    returns. `Create` must be `CreateSecret` and never `PutSecretValue`, though — that is what makes
    create-if-absent atomic inside AWS rather than a read-then-write in this package, and it is the
    only guard against a retried request overwriting a live tenant's key (003, Security
    Considerations).
  - **Delete**: `DeleteSecret` as-is, leaving AWS's default recovery window; there is no
    `RestoreSecret`, no force-delete, and no recovery-window handling of any kind. 003 makes no
    guarantee about what a deleted path leaves behind and nothing in the tree reads a deleted path
    afterwards, so there is nothing further to implement. One consequence to state plainly rather than
    to handle: a `CreateSecret` onto a path whose predecessor is still inside that window fails and is
    reported like any other AWS fault. An account deleted and recreated under the same
    `metadata.name` within the window therefore needs an operator to clear the old secret — which is
    already what 003 calls clearing a genuinely stale path.
  - **Error reporting**: every failing AWS call is returned as an ordinary error wrapped with the
    operation and the path that produced it, keeping the vendor error in the chain by `%w` so its code
    still reaches the log through 001's incident ID. The backend translates nothing, matches nothing,
    and makes **no** user/system classification decision of its own — 003 treats every store fault as
    a system error.
  - **Testing**: unit tests run against a fake of the `secretsmanager` API surface the backend is
    constructed over — no AWS account — and cover only that each method calls the right API with the
    right path and value, and that a failure comes back wrapped with its operation and path.
    Integration tests are the only place an AWS account is needed: `make test-integration` only
    (skipped by `-short`), driven by `.env` (`AWS_REGION`, `AWS_PROFILE`,
    `SAMPLE_CUSTOMER_NAMESPACE`, `SAMPLE_CUSTOMER_ACCOUNT`), creating and cleaning up under a
    dedicated test path prefix. Because `Delete` leaves the recovery window in place, a test that
    deletes and immediately recreates the same path fails, so either every run uses a unique path or
    cleanup — and only cleanup — uses `ForceDeleteWithoutRecovery`.
- Out of scope: path construction, caching, keypair generation, the credential's own encoding (003
  owns the JSON shape of the string it hands down; this backend only decides how that string is
  persisted) and the user/system classification policy (all 003); soft-delete recovery, restore or
  purge semantics, which 003 no longer asks any backend for and this one does not reintroduce; any
  other backend; backend selection (`cmd/provider/main.go`); and the IAM grants the controller's role
  needs — ops owns those, and no code in this package reads or authors a policy.
- Open question for this spec: whether `CreateSecret` should carry ops-defined tags. No tags are
  adequate for v1alpha1; if ops needs them for cost attribution they land here, and they would become
  another 002 field carried but not interpreted — as `KmsKeyId` already is.

## Cross-cutting context from the roadmap

- **Decision — the `origin/secrets-handling` branch is ignored.** That unmerged branch holds a drafted
  `002-secrets-handling.md`, a `002-a-aws-secrets-backend.md` sub-spec and a full `internal/secrets/`
  implementation. It is treated as abandoned — nothing in it is inherited and its numbering does not
  survive.
- **Decision — `NNN.a` sub-specs exist, but only for a pluggable backend behind an interface owned by
  `NNN`.** Spec 003 owns `internal/secrets/` and names no product anywhere; 003.a owns
  `internal/secrets/aws/` and is the only place an AWS SDK enters `go.mod`. A letter spec may depend
  on its parent and on anything below it, never on a sibling letter or a higher number. **No numbered
  spec may depend on a letter spec** — 004 and 010 depend on 003's interface, so the pool and the
  account module never learn which store is behind it. The concrete backend is selected exactly once,
  in `cmd/provider/main.go`, which no numbered spec owns. That is what keeps the swap a one-file
  change.
- **Why nothing above 003 depends on 003.a.** 004 and 010 take 003's interface, so they stay
  unit-testable against the in-memory fake 003 ships — no AWS account, no SDK, no network. An import
  of `internal/secrets/aws` anywhere outside `cmd/provider/` is a bug, and it is the single grep that
  proves the store is still pluggable. The reverse import — `internal/secrets` reaching for
  `internal/secrets/aws` — is a compile error, not merely a layering violation, since the child
  imports the parent for the interface and the `Path` type.
- **Deliberately unnumbered — a second secret backend.** A Vault-style backend (`003.b`, in
  `internal/secrets/vault/`) is deliberately not planned. This decision reserves its shape but not the
  work: nothing in the tree needs it, and specifying a backend nobody runs would fix 003's interface
  against an imagined store instead of a real one. The interface earns its keep by staying small
  enough that adding one later is a self-contained job behind four methods.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Parent spec**: `specs/003-secrets-handling.md` — owns the `Backend` interface, the path grammar,
  the credential shape, the cache and the error-classification policy this backend inherits whole.
- **Shape reference**: `specs/001-error-and-logging.md` — follow its section skeleton (also given in
  `specs/000-template.md`).
