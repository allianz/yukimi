# Specification: SnowflakeAccount Controller (020)

This specification also finishes `cmd/provider/main.go`'s bootstrap wiring (config, secrets backend,
connection pool) and updates `internal/controller/yukimi.go`'s registration list — neither file is owned
by a numbered spec of its own (design.md §1.2), and this is the first controller that needs any of it.

## Overview

This is the controller that turns a tenant's committed SnowflakeAccount description into an actual
Snowflake account, and later back out of existence again. It sits at the end of the platform's
provisioning chain: configuration loading, secret storage, pooled connections, the step-by-step
provisioning pipeline, and the two-key deletion safeguard all exist so that this controller can call
them. Because it is the first controller in this codebase that needs a live connection to both Snowflake
and a cloud secret store, it is also where the remaining startup wiring gets finished.

## Scope

For this first working version, this controller wires in only the one step that creates and destroys the
account itself — the steps that would enforce naming rules, credit limits, network access,
authentication exceptions, and identity import are deliberately left for later, so an account created
today gets nothing beyond its own existence and a login for the platform's own service user.

This specification defines the `internal/controller/snowflakeaccount` package that:

- Registers a pipeline (009) of exactly one module for this cut — the account module (012) — via
  `pipeline.New`. Guardrail-check, quota-check, parameters, network, auth, identity, and quota-monitor
  (010, 011, 013–015, 017, 018) are not registered because none of them is written yet.
- Calls `Pipeline.Observe` from the controller's `Observe` and `Pipeline.Apply` from both `Create` and
  `Update` (one shared body). Calls `Pipeline.Destroy` from `Delete` — which the managed reconciler only
  ever invokes when the preceding `Observe` reported the account as existing; if none was ever created,
  the reconciler releases the finalizer on its own, without calling `Delete` at all — and only once this
  controller's own deletion gate (below) has separately authorized the destruction.
- Builds one `pipeline.ModuleContext` per reconcile call, passing the target namespace's labels, read
  fresh from the Kubernetes API on every call — `NewModuleContext` takes no backplane config; a module
  that needs one injects its own copy at construction instead.
- Computes and persists `status.accountName`/`status.accountUrl` itself, directly from the
  `ModuleContext` and the CRD's own already-set `status.accountLocator` — never from a module's
  `Outcome`.
- Renders every module's `Outcome.Condition`/`Outcome.Event` onto the resource, advances
  `status.observedGeneration` only once a run's `Result.AllDone()` is true, and returns the pipeline's
  first handled module error from `Create`/`Update` so a rejection or failure lands on `Synced` instead
  of being silently overwritten.
- Implements the deletion gate (design.md §6.3 Phases 2–3): looks up an `Active`
  `SnowflakeDeletionRequest` (019) before honoring a `SnowflakeAccount` deletion, blocks and emits
  `Warning: DeletionBlocked` when none is found, and marks the request `Consumed` once `Pipeline.Destroy`
  succeeds.
- Finishes `cmd/provider/main.go`'s startup wiring: a `--configDir` flag, `base.Load` (002), a
  cloud-provider switch constructing the AWS secrets backend (003.a), `secrets.NewCachedBackend` (003),
  and `pool.New` (004) — then forwards `Config`, the pool, and the cached backend into this package's own
  `SetupGated`. Immediately after constructing the pool, `main.go` also calls `Pool.OrgAdmin` once,
  bounded by a short timeout, and exits fatally if it errors, so a broken AWS session or an unreachable
  org-admin Snowflake connection fails the process at startup rather than on the first reconcile.

**Out of Scope**:

- Guardrail admission (008/010), quota admission (011), account parameters (013), network rules (014),
  auth exceptions (015), identity import (017), quota-monitor enforcement (018), and the backplane
  region's `available` gate (007) — all deliberately absent from this cut, not merely deferred within it;
  see Edge Cases for exactly what that leaves unenforced today.
- Any mechanism that forces a later-registered module to apply itself against a `SnowflakeAccount` that
  already reached `Ready` under today's smaller pipeline — left to each future module's own `Observe`
  (see Edge Cases).
- Any call to `internal/config/backplane`'s `Load`/`Config`/`Region` — nothing registered in this cut
  reads one, so this package has zero callers of that package.
- Executing any SQL, or deciding what SQL to execute — entirely the account module's (012) job, reached
  only through the pipeline (009).
- Drift detection or repair of anything already applied — not attempted anywhere in this pipeline until
  Snowflake ships Organization Policies (design.md Appendix B).

## Key Concept: A Deliberately Partial Pipeline

This controller's pipeline carries one module today: the account module (012), which alone is enough to
create a Snowflake account, give the platform a way to log into it, and drop it again. Every other
capability a `SnowflakeAccount` can describe — network rules, auth exceptions, identity groups, a credit
quota, and the admission checks that would normally gate all of them — is accepted by the CRD's schema
(006) exactly as written, but nothing in this controller's pipeline reads or acts on any of it, because
the module that owns each concern does not exist yet. This also means the "validation phase" earlier
designs imagined ahead of the pipeline has nothing left in it beyond what the CRD's own schema (006)
already enforces at admission, before this controller ever sees the object — no per-reconcile region or
admission check runs here at all.

**Important**: adding a module later is a one-line change to the pipeline's argument list, not a change
to this controller's shape — nothing here needs to anticipate which module lands next or expose an
extension point for it.

## Key Concept: One Shared Context, Three Uniform Entry Points

Every reconcile builds exactly one `pipeline.ModuleContext` and hands it, unchanged, to whichever of the
pipeline's three entry points that reconcile method needs: `Observe` calls `Pipeline.Observe`;
`Create` and `Update` share one internal function that calls `Pipeline.Apply`, since `Apply` is
idempotent by construction and there is nothing for the two methods to do differently; `Delete` calls
`Pipeline.Destroy`, but only once the deletion gate below has cleared. Nothing is threaded from one call
into the next — a fresh context is built every time, from the CRD and namespace labels as they stand at
that moment, and it carries no live connection until a module first asks for one.

## Key Concept: The Ready Latch Lives on the Persisted Condition

Whether an account has ever finished provisioning is not something this controller recomputes from
scratch — it reads the resource's own already-persisted `Ready` condition and treats `True` as
permanent. Only while `Ready` is not yet `True` does a pending module's reason become the message this
controller reports; once `Ready` is `True`, a later module still waiting on something (a newly added
identity group syncing, for instance) is visible only on that module's own condition. This is what stops
a healthy, long-`Ready` account from reverting to unavailable just because some later edit is still
being applied.

**Important**: crossplane-runtime's managed reconciler overwrites `Ready`/`Synced` again right after
`Observe` and after `Create`/`Update` return, on every call — not only the first time. So this controller
recomputes its own view of `Ready` and `Synced` on every single `Observe`, never relying on what a prior
`Apply` already set to still be showing.

## Key Concept: Surfacing a Rejected or Failed Module on `Synced`

A module's own rejection or failure has nowhere to go once the run for that reconcile ends, unless
`Create`/`Update` explicitly returns it. Returning `nil` from either method — even after recording every
module's condition and event correctly — makes the managed reconciler mark the reconcile a success
immediately afterward, overwriting whatever this controller just set. So `Create`/`Update` must return
the pipeline's first handled module error, letting the reconciler render it onto `Synced` itself, exactly
as an ordinary infrastructure failure would be. This never reintroduces a retry storm: the extra retry
this causes only affects the next poll-driven reconcile of an unchanged, still-rejected resource, and a
tenant's own edit reconciles immediately regardless.

## Key Concept: The Deletion Gate

Two separate gates stand between a tenant deleting a `SnowflakeAccount` object and its account actually
being dropped. The first belongs to crossplane-runtime itself, not this controller: `Delete` is only ever
invoked when the immediately preceding `Observe` reported the account as existing; a resource whose
account was never created has its finalizer released directly, with `Delete` never called at all. The
second is this controller's own, and is what design.md §6.3 means by positive control: whenever `Delete`
*is* invoked, it looks for an `Active` `SnowflakeDeletionRequest` (019) naming this specific resource
before doing anything else. Finding none — because none was ever created, or because the one that
existed expired unused — blocks the destruction outright: the resource stalls in `Terminating`, a
`Warning: DeletionBlocked` event is recorded, and `Ready` is set to `False` so that automated deployment
tooling reports the failure rather than the deletion silently hanging. Finding one authorizes tearing the
account down through the pipeline and only then marks that request `Consumed`, so a teardown that fails
partway leaves the request usable for a retry rather than burning the tenant's one authorization on a run
that never finished.

## Public API

```go
package snowflakeaccount // internal/controller/snowflakeaccount

// SetupGated adds a controller that reconciles SnowflakeAccount objects with
// safe-start support, wired into internal/controller/yukimi.go alongside
// every other resource's controller (design.md §3.2, §6.3, §7).
//
// Registers a pipeline (009) of exactly one module for this cut (Key
// Concept: A Deliberately Partial Pipeline):
//
//	pipeline.New(accountmodule.New(
//	    secretsBackend,
//	    cfg.Snowflake.Org,
//	    cfg.Snowflake.AccountCreationGracePeriod,
//	    cfg.Deletion.GracePeriodDays,
//	))
//
// Parameters:
//   - cfg: the provider's base configuration (002) — read for
//     Snowflake.Org, Snowflake.AccountCreationGracePeriod,
//     Snowflake.UsePrivateLink (status.accountUrl), and
//     Deletion.GracePeriodDays.
//   - p: the pooled Snowflake connections (004), satisfying
//     pipeline.DBPool; shared with every other controller that ever needs
//     one.
//   - secretsBackend: the cached secrets backend (003), passed straight
//     into the account module's own constructor (012) — this controller
//     never calls it directly.
//
// Returns:
//   - error if registration with the manager fails; never an error from
//     anything Snowflake- or secrets-related, since SetupGated performs
//     no I/O of its own.
func SetupGated(mgr ctrl.Manager, o controller.Options, cfg *base.Config, p *pool.Pool, secretsBackend secrets.Backend) error
```

## Project Structure

### Source Code

```text
internal/controller/snowflakeaccount/
├── doc.go
├── reconciler.go        # SetupGated/Setup, connector, external — Observe/Create/Update/Delete,
│                         # the deletion gate, and the small namespace-label lookup helper
├── reconciler_test.go
└── integration_test.go  # TestIntegration...: full create-then-destroy round trip against a live
                          # Snowflake org, a live AWS Secrets Manager, and a real Kubernetes API (kind)
```

Modified, pre-existing files (owned by no numbered spec — design.md §1.2):

```text
cmd/provider/main.go            # --configDir flag; base.Load (002); a cloud-provider switch
                                 # constructing the AWS secrets backend (003.a); secrets.NewCachedBackend
                                 # (003); pool.New (004); pool.Close() on shutdown; forwards Config, the
                                 # pool, and the cached backend into internal/controller/yukimi.SetupGated
internal/controller/yukimi.go   # SetupGated gains cfg/pool/secretsBackend parameters and forwards them
                                 # into snowflakeaccount.SetupGated, alongside the unchanged call to
                                 # snowflakedeletionrequest.SetupGated
```

## Error Classification

**User Errors** (use `errors.NewUserError()`):
- No `Active` `SnowflakeDeletionRequest` authorizes a `SnowflakeAccount`'s deletion — the tenant can fix
  this by creating one.
- A module's own rejection, already classified by that module and surfaced unchanged via
  `Result.FirstError()`.

**System Errors** (use `fmt.Errorf("context: %w", err)` or pass through unchanged):
- A Kubernetes API failure reading the target namespace's labels.
- `deletion.FindActiveRequest`/`deletion.MarkConsumed` (019) failures against the Kubernetes API.
- A module's own failure, already classified by that module and surfaced unchanged via
  `Result.FirstError()` or `Pipeline.Destroy`'s returned error.
- `tenant.AccountURL`'s region-format error, on the rare occasion it fires despite `CREATE ACCOUNT`
  having already succeeded with that same region string (see Edge Cases) — logged via `log.Handle`, but
  never allowed to fail the reconcile on its own.

## Edge Cases

- **What can a tenant do today that a fully-configured deployment would reject?** Anything: there is no
  region-availability check (007, dropped for this cut), no guardrail check (008/010) and no quota check
  (011), so any syntactically valid `SnowflakeAccount` — any region string, any credit quota, any network
  or auth exception entry — reaches `CREATE ACCOUNT` unfiltered. `customNetworkRules`, `customAuthRules`,
  `identityIntegration.groups`, and `creditQuota` are all accepted by the CRD's schema and stored
  verbatim, but nothing reads them: no network policy beyond Snowflake's own unrestricted default, no
  auth-exception bindings, no group import (so no human identity can log into the account at all — only
  the platform's own service-user key exists), and no resource monitor or budget. This is an accepted,
  temporary gap for a development cluster that is wiped constantly, not a security posture being carried
  forward.
- **A future module (010/011/013–015/017/018) is registered months from now — does it automatically
  apply itself to every `SnowflakeAccount` that already reached `Ready` under today's one-module
  pipeline?** Only if that module's own `Observe` is written to report out-of-sync for an account it has
  never actually asserted its state against. This controller adds no mechanism of its own to force that
  — no module-set version stamped into `status`, nothing beyond the ordinary generation-gated re-apply
  (009) — because there is exactly one module registered today and nothing yet for such a mechanism to
  guard. The obligation this places on every future module's `Observe` is real, though: it must be able
  to tell "never applied" apart from "already correct."
- **`tenant.AccountURL` returns an error even though the account clearly exists (`status.accountLocator`
  is already set)** — possible only because this cut runs no independent region-format check of its own
  (007's gate is gone) and `CREATE ACCOUNT`'s own region transform tolerates strings `AccountURL`'s
  stricter format check does not. When it happens, `status.accountUrl` simply stays unset while
  `accountName`/`accountLocator` and `Ready` are unaffected — a logged system error, not a blocking one.
- **An `Apply` run aborts partway, or one module reports anything other than `Done`** — any status field
  or condition a module in that run's `Outcomes` never touched (`accountUrl`, `accountName`,
  `accountLocator`, or a condition owned by a module that never ran) is left exactly as the previous
  reconcile set it; nothing here blanks or defaults it.
- **`Observe` runs on a resource that is being deleted** — it reports `ResourceExists:
  cr.Status.AccountLocator != ""` and returns immediately, without building a `ModuleContext` or calling
  the pipeline at all; only a resource whose account was never created reports `false` here, releasing
  the finalizer without ever reaching `Delete`.
- **More than one `Active` `SnowflakeDeletionRequest` targets the same resource** — this controller does
  not resolve that itself; `deletion.FindActiveRequest` (019) already returns a single, deterministic
  candidate (earliest `creationTimestamp`), so `Delete` only ever sees one request or `nil`.
- **`Pipeline.Destroy` fails partway through** — the deletion request is left `Active` (never marked
  `Consumed`), so the next reconcile retries the same teardown from the top; every module's `Teardown` is
  safe to re-run (009).
- **The managed reconciler overwrites `Ready`/`Synced` again right after `Observe`, `Create`, and
  `Update` all return, every single time** — this controller relies on nothing from a prior call still
  being in effect; every `Observe` recomputes `Ready`/`Synced` in full, per Key Concept: The Ready Latch
  Lives on the Persisted Condition.

## Dependencies

- **`internal/errors`/`internal/logger` (001)** — Used APIs: `errors.NewUserError()`, `logger.New()`,
  `Logger.Handle()` — Contract: one `*Logger` per reconcile method call; every handled error this
  controller returns has already passed through exactly one `Handle` call.
- **`internal/config/base` (002)** — Used APIs: `base.Load()`, `Config.CloudProvider()`,
  `Config.Snowflake.*`, `Config.Deletion.GracePeriodDays` — Contract: loaded once in `cmd/provider/main.go`
  at startup; this package treats the result as immutable for the process's life.
- **`internal/secrets` (003) / `internal/secrets/aws` (003.a)** — Used APIs: `secrets.Backend`,
  `secrets.NewCachedBackend()`, `secretsaws.New()` — Contract: `main.go` constructs and wraps exactly one
  backend and passes it, already cached, into both `pool.New` and `SetupGated`.
- **`internal/snowflake/pool` (004)** — Used APIs: `pool.New()`, `Pool.Close()`, and, indirectly through
  `pipeline.ModuleContext`, `OrgAdmin()`/`TenantAccount()`/`EvictTenant()` — Contract: one `*pool.Pool`
  constructed in `main.go`, shared by every controller that needs one; closed exactly once, on shutdown.
- **`internal/account/tenant` (006)** — Used APIs: `tenant.AccountURL()` — Contract: called directly by
  this controller once `status.accountLocator` is non-empty; `tenant.ResolveName` is reached only
  indirectly, via `ModuleContext.ResolvedAccountName()`.
- **`apis/base/v1alpha1` (006, 019)** — Used APIs: the `SnowflakeAccount`/`SnowflakeDeletionRequest`
  types, `SnowflakeAccountKind`, `SnowflakeAccountGroupVersionKind` — Contract: this package registers
  the former with `managed.NewReconciler` and reads/writes its `Status` directly.
- **`internal/account/pipeline` (009)** — Used APIs: `pipeline.New()`, `Pipeline.Observe()`,
  `.Apply()`, `.Destroy()`, `pipeline.NewModuleContext()`, `Observation.PendingReason()`,
  `Result.AllDone()`, `.PendingReason()`, `.FirstError()` — Contract: exactly one `*pipeline.Pipeline`
  built once, at `Setup` time, from the module list this spec's Public API section fixes.
- **`internal/account/modules/account` (012)** — Used APIs: `accountmodule.New()` — Contract: the sole
  module registered in this cut; this controller never calls it directly beyond registration.
- **`internal/deletion` (019)** — Used APIs: `deletion.FindActiveRequest()`, `deletion.MarkConsumed()` —
  Contract: called from `Delete` only, around `Pipeline.Destroy`.
- **`crossplane-runtime/v2`** — Used APIs: `managed.NewReconciler`, `managed.TypedExternalClient`,
  `xpv1.Available()`/`Unavailable()`, `event.Recorder` — Contract: standard managed-resource wiring, per
  CLAUDE.md's "Standard Controllers with External State" pattern.

No dependency on `internal/config/backplane` (007), `internal/snowflake/statement` (005 — this controller
issues no SQL of its own), or any of `internal/account/modules/{guardrailcheck,quotacheck,parameter,
network,auth,identity,quotamonitor}` (008/010/011/013–015/017/018) — none of the latter exist yet, and
007 has no caller anywhere in this cut (D-005).

## Integration Points

- **`cmd/provider/main.go`** — Owns the new `--configDir` flag (default `/etc/yukimi/config`), calls
  `base.Load`, switches on `Config.CloudProvider()` to construct the AWS secrets backend (fatally
  rejecting any other value by listing the cloud providers actually compiled in), wraps it in
  `secrets.NewCachedBackend`, constructs the `*pool.Pool`, and forwards `Config`, the pool, and the cached
  backend into `internal/controller/yukimi.SetupGated` — Key functions: `base.Load()`, `secretsaws.New()`,
  `secrets.NewCachedBackend()`, `pool.New()`, `Pool.Close()`.
- **`internal/controller/yukimi.go`** — `SetupGated`'s signature gains the same three parameters and
  forwards them into `snowflakeaccount.SetupGated`, while `snowflakedeletionrequest.SetupGated` keeps its
  existing two-parameter call — Key functions: `SetupGated()`.
- **`internal/account/pipeline` (009) and `internal/account/modules/account` (012)** — This controller is
  their sole caller in the running provider today; nothing else in the codebase constructs a
  `*pipeline.Pipeline` or an `accountmodule.Module`.
- **`internal/deletion` (019)** — This controller is `FindActiveRequest`/`MarkConsumed`'s only caller.

## Success Criteria

- **SC-001**: `internal/controller/yukimi.go`'s `SetupGated` forwards `cfg`, `p`, and `secretsBackend`
  into `snowflakeaccount.SetupGated`, while `snowflakedeletionrequest.SetupGated`'s call keeps its
  existing two-parameter signature.
- **SC-002**: `cmd/provider/main.go` gains a `--configDir` flag defaulting to `/etc/yukimi/config`, calls
  `base.Load`, and exits fatally — listing the cloud providers actually compiled in — when
  `Config.CloudProvider()` names one with no backend compiled in.
- **SC-003**: `cmd/provider/main.go` never calls `backplane.Load`, and this package never imports
  `internal/config/backplane`.
- **SC-004**: the pipeline `SetupGated` builds contains exactly one module, identified by
  `Name() == pipeline.AccountModuleName`.
- **SC-005**: every `pipeline.NewModuleContext` call this controller makes passes no backplane
  config — the constructor itself accepts none.
- **SC-006**: `Observe` on a resource with a non-nil `GetDeletionTimestamp()` reports `ResourceExists:
  cr.Status.AccountLocator != ""` and `ResourceUpToDate: true` without building a `ModuleContext` or
  calling any pipeline method.
- **SC-007**: `Create` and `Update` both delegate to the same internal function, which calls
  `Pipeline.Apply` exactly once per call.
- **SC-008**: whenever `Result.FirstError()` is non-nil, `Create`/`Update` returns the handled error —
  never `nil` — so the managed reconciler renders `ReconcileError` onto `Synced` instead of overwriting it
  with `ReconcileSuccess`.
- **SC-009**: `status.observedGeneration` advances via `SetObservedGeneration` only on a call where
  `Result.AllDone()` is true.
- **SC-010**: a successful `Apply` sets `status.accountName` from `ModuleContext.ResolvedAccountName()`;
  once `status.accountLocator` is non-empty, it also sets `status.accountUrl` from `tenant.AccountURL`
  using `Config.Snowflake.UsePrivateLink`.
- **SC-011**: an aborted or partially-failed `Observe`/`Apply` leaves every status field and condition
  untouched by that run's `Outcomes` exactly as the previous reconcile left it — no blanking, no
  defaulting.
- **SC-012**: every `Outcome.Event`/`Outcome.Condition` present in `Observation.Outcomes`/
  `Result.Outcomes` is rendered onto the resource, in outcome order, on both `Observe` and
  `Create`/`Update`.
- **SC-013**: `Ready` is set `True` for the first time only from the `Create`/`Update` path's
  `Result.AllDone()` branch; `Observe` never performs that transition itself.
- **SC-014**: `Delete` returns a user error and records a `Warning: DeletionBlocked` event when
  `deletion.FindActiveRequest` returns `nil`, and does not release the finalizer.
- **SC-015**: `Delete` calls `Pipeline.Destroy` only once `FindActiveRequest` has returned a non-nil
  request, and calls `deletion.MarkConsumed` only once `Pipeline.Destroy` has returned `nil`.
- **SC-016**: a `Pipeline.Destroy` failure leaves the matched `SnowflakeDeletionRequest` `Active` (never
  calls `MarkConsumed`) and returns a handled error.
- **SC-017**: `internal/controller/snowflakeaccount` imports none of
  `internal/account/modules/{guardrailcheck,quotacheck,parameter,network,auth,identity,quotamonitor}` or
  `internal/config/backplane` (grep-provable — none of the former exist yet).
- **SC-018**: unit test coverage exceeds 95% for `internal/controller/snowflakeaccount`.
- **SC-019**: `make generate` and `make reviewable` both pass with no CRD schema change introduced by this
  spec.
- **SC-020**: `integration_test.go` proves a full create-then-destroy round trip: a `SnowflakeAccount`
  reaches `Ready`, then an authorizing `SnowflakeDeletionRequest` plus its deletion together remove it —
  against a live Snowflake organization, a live AWS Secrets Manager, and a real Kubernetes API.

## Security Considerations

- **No admission control exists in this cut** (D-003/D-005): any syntactically valid `SnowflakeAccount`
  creates a real Snowflake account, in any region, with any credit quota, network, or auth-exception
  entry it names. This is accepted for the development cluster this code runs against today, which is
  wiped constantly, and is expected to close once 008/010/011 land — not a production posture.
- **Deletion's positive control is fully enforced regardless of how small the provisioning pipeline is**
  — the deletion gate depends only on 019's own lifecycle, never on which modules happen to be
  registered, so today's reduced pipeline does not weaken it.
- **The org-admin connection is only ever reached from inside the account module (012), through
  `ModuleContext`** — this controller itself never calls `OrgAdminDB` or holds an org-admin-scoped
  connection.
- **No tenant-controllable value ever reaches `DROP ACCOUNT`'s grace period** — `cfg.Deletion.GracePeriodDays`
  is ops-owned configuration (002), passed into `accountmodule.New` unchanged; nothing here lets a request
  shorten or lengthen it.

## Performance Considerations

- `Apply` only re-asserts state when a resource's generation has moved past what the last all-`Done` run
  recorded (009) — with one module registered, that is a handful of idempotent statements per reconcile,
  not a growing cost as more modules land later.
- Reading the target namespace's labels costs one extra Kubernetes `Get` per `Observe`/`Create`/`Update`/
  `Delete` call. Nothing registered in this cut reads the result, but fetching it now — rather than
  passing an empty map — means a future guardrail-check or quota-check module (010/011) needs no
  additional plumbing change here to start receiving real data.
- Every Snowflake connection is already pooled (004) and reused across reconciles; this controller opens
  none of its own and pays no per-reconcile dial cost.

## References

- **Product design**: `specs/design.md` §3.2 (create flow), §3.3/§3.4 (guardrails/exceptions — deferred),
  §3.6 (account bootstrapping), §3.11/§3.11.3 (privilege step-down, immutability), §3.12 (account naming),
  §6.1–§6.3 (the deletion flow this controller's `Delete` implements Phases 2–3 of), §7.1/§7.2 (condition
  and status model), Appendix A/B.
- **Clarification record**: `specs/wip-020-snowflakeaccount-controller.md` — D-001 through D-005 and
  their rationale; kept until this spec's code lands.
- **Dependency specs**: `specs/002-base-config.md`, `specs/003-secrets-handling.md`,
  `specs/003.a-aws-secrets-backend.md`, `specs/004-connection-pooling.md`,
  `specs/006-snowflake-account-crd.md`, `specs/009-account-pipeline.md` (Appendix Examples 1–3, adapted
  below), `specs/012-account-module.md`, `specs/019-deletion-request.md`.
- **Vendored behavior**: `crossplane-runtime/v2@v2.0.0` `pkg/reconciler/managed/reconciler.go` — the
  managed reconciler sets `Creating()`/`ReconcileSuccess()` after `Create`/`Update` return and after
  `Observe` returns on the up-to-date path (lines 1406,1437,1457,1507,1428), and calls `Delete` only when
  the preceding `Observe` reported `ResourceExists: true` (lines 1163,1173,1230).
- **Current code**: `cmd/provider/main.go`, `internal/controller/yukimi.go`,
  `internal/controller/snowflakedeletionrequest/reconciler.go` — the wiring and validation-only-controller
  patterns this spec's controller extends and diverges from (it manages real external state, unlike
  019's controller).

<br/><br/><br/><br/><br/>

================

## Appendix: Usage Examples

These illustrate call shape and sequencing, adapted from `specs/009-account-pipeline.md`'s own Appendix
for this cut's finalized decisions — no backplane lookup, one registered module, and the deletion gate
wired in. Not exact compilable code.

### Example 1: `Observe`

```go
func (e *external) Observe(ctx context.Context, cr *v1alpha1.SnowflakeAccount) (managed.ExternalObservation, error) {
    log := logger.New(e.logger, cr.Namespace, "SnowflakeAccount", cr.Name, logger.OpObserve)

    if cr.GetDeletionTimestamp() != nil {
        return managed.ExternalObservation{
            ResourceExists:   cr.Status.AccountLocator != "",
            ResourceUpToDate: true,
        }, nil
    }

    labels, err := e.namespaceLabels(ctx, cr.Namespace)
    if err != nil {
        retryErr := log.Handle(err)
        cr.SetConditions(xpv1.Unavailable().WithMessage(retryErr.Error()))
        return managed.ExternalObservation{}, retryErr
    }

    // No backplane config wired into this cut (D-005): no registered module
    // depends on it yet.
    mc := pipeline.NewModuleContext(cr, labels, log, e.pool)
    obs := e.pipeline.Observe(ctx, mc)
    if !obs.Exists {
        return managed.ExternalObservation{ResourceExists: false}, nil
    }

    for _, mo := range obs.Outcomes {
        if mo.Outcome.Event != nil {
            e.record.Event(cr, *mo.Outcome.Event)
        }
        if mo.Outcome.Condition != nil {
            cr.SetConditions(*mo.Outcome.Condition)
        }
    }

    cr.Status.AccountName = mc.ResolvedAccountName()
    if cr.Status.AccountLocator != "" {
        if url, err := tenant.AccountURL(cr.Status.AccountLocator, cr.Spec.Region, e.cfg.Snowflake.UsePrivateLink); err == nil {
            cr.Status.AccountURL = url
        } else {
            log.Handle(err) // logged, never fails Observe (Edge Cases)
        }
    }

    // Observe never flips Ready to True for the first time — only apply()'s
    // AllDone branch (Example 2) does that.
    if cr.GetCondition(xpv1.TypeReady).Status != corev1.ConditionTrue {
        cr.SetConditions(xpv1.Unavailable().WithMessage(obs.PendingReason()))
    }

    upToDate := cr.Status.GetObservedGeneration() == cr.Generation && obs.InSync
    return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: upToDate}, nil
}
```

### Example 2: `Create`/`Update`

```go
func (e *external) Create(ctx context.Context, cr *v1alpha1.SnowflakeAccount) (managed.ExternalCreation, error) {
    return managed.ExternalCreation{}, e.apply(ctx, cr)
}

func (e *external) Update(ctx context.Context, cr *v1alpha1.SnowflakeAccount) (managed.ExternalUpdate, error) {
    return managed.ExternalUpdate{}, e.apply(ctx, cr)
}

func (e *external) apply(ctx context.Context, cr *v1alpha1.SnowflakeAccount) error {
    log := logger.New(e.logger, cr.Namespace, "SnowflakeAccount", cr.Name, logger.OpUpdate)

    labels, err := e.namespaceLabels(ctx, cr.Namespace)
    if err != nil {
        return log.Handle(err)
    }

    mc := pipeline.NewModuleContext(cr, labels, log, e.pool)
    result := e.pipeline.Apply(ctx, mc)

    for _, mo := range result.Outcomes {
        if mo.Outcome.Event != nil {
            e.record.Event(cr, *mo.Outcome.Event)
        }
        if mo.Outcome.Condition != nil {
            cr.SetConditions(*mo.Outcome.Condition)
        }
    }
    firstErr := log.Handle(result.FirstError()) // becomes Synced's message below; log.Handle(nil) == nil

    cr.Status.AccountName = mc.ResolvedAccountName()
    if cr.Status.AccountLocator != "" {
        if url, err := tenant.AccountURL(cr.Status.AccountLocator, cr.Spec.Region, e.cfg.Snowflake.UsePrivateLink); err == nil {
            cr.Status.AccountURL = url
        } else {
            log.Handle(err)
        }
    }

    if result.AllDone() {
        cr.Status.SetObservedGeneration(cr.Generation)
        cr.SetConditions(xpv1.Available())
    }
    if cr.GetCondition(xpv1.TypeReady).Status != corev1.ConditionTrue {
        cr.SetConditions(xpv1.Unavailable().WithMessage(result.PendingReason()))
    }

    // Returning nil here would drop firstErr: the managed reconciler calls
    // status.MarkConditions(xpv1.ReconcileSuccess()) right after Create/Update
    // returns nil, overwriting Synced regardless of what was set above.
    return firstErr
}
```

### Example 3: `Delete` — the Deletion Gate

```go
func (e *external) Delete(ctx context.Context, cr *v1alpha1.SnowflakeAccount) (managed.ExternalDelete, error) {
    log := logger.New(e.logger, cr.Namespace, "SnowflakeAccount", cr.Name, logger.OpDelete)

    // Phase 2: no active request, no destruction.
    req, err := deletion.FindActiveRequest(ctx, e.kube, cr.Namespace, v1alpha1.SnowflakeAccountKind, cr.Name)
    if err != nil {
        return managed.ExternalDelete{}, log.Handle(err)
    }
    if req == nil {
        blockedErr := errors.NewUserError("deletion blocked: no active SnowflakeDeletionRequest authorizes this account")
        e.record.Event(cr, event.Warning("DeletionBlocked", blockedErr))
        cr.SetConditions(xpv1.Unavailable().WithMessage(blockedErr.Error()))
        return managed.ExternalDelete{}, log.Handle(blockedErr)
    }

    labels, err := e.namespaceLabels(ctx, cr.Namespace)
    if err != nil {
        return managed.ExternalDelete{}, log.Handle(err)
    }
    mc := pipeline.NewModuleContext(cr, labels, log, e.pool)

    // Phase 3: every module's Teardown, in reverse — today, just the account
    // module's DROP ACCOUNT and credential cleanup.
    if err := e.pipeline.Destroy(ctx, mc); err != nil {
        return managed.ExternalDelete{}, log.Handle(err)
    }

    return managed.ExternalDelete{}, deletion.MarkConsumed(ctx, e.kube, req)
}
```

### Example 4: Finishing `cmd/provider/main.go`'s Bootstrap Wiring

```go
configDir := app.Flag("configDir", "directory containing base.yaml and sibling config files").Default("/etc/yukimi/config").String()
// ... after flag parsing:

cfg, err := base.Load(*configDir)
kingpin.FatalIfError(err, "failed to load base config")

var backend secrets.Backend
switch cfg.CloudProvider() {
case "aws":
    backend, err = secretsaws.New(cfg.AWS.Region, cfg.AWS.KmsKeyId, cfg.Deletion.GracePeriodDays)
    kingpin.FatalIfError(err, "failed to construct AWS secrets backend")
default:
    kingpin.Fatalf("no secrets backend compiled in for cloud section %q (compiled in: aws)", cfg.CloudProvider())
}
cached := secrets.NewCachedBackend(backend, cfg.Secrets.CacheTTL)

p := pool.New(cached, cfg)
defer p.Close()

// o := controller.Options{...} unchanged from today's construction.
kingpin.FatalIfError(customresourcesgate.Setup(mgr, o), "Cannot setup CRD gate controller")
kingpin.FatalIfError(yukimi.SetupGated(mgr, o, cfg, p, cached), "Cannot setup Yukimi controllers")
kingpin.FatalIfError(mgr.Start(ctrl.SetupSignalHandler()), "Cannot start controller manager")
```
