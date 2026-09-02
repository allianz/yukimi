# Specification: SnowflakeAccount Controller (018)

## Overview

This is the piece of software that actually watches a team's account request inside Kubernetes and
turns it into a real, working Snowflake account — or, later, removes one again. Every earlier piece of
this platform (configuration loading, region lookups, database connections, the account-creation logic
itself) is a library that does nothing on its own; something has to be woken up by Kubernetes, decide
what to do, call into that library, and write down what happened so a team can see whether their
account is ready. This piece is that something, and it is built to stay thin: check a couple of
upfront conditions, hand the real work to the account-provisioning logic already built, and record the
result as status fields and conditions a team can read. Only one of the several provisioning steps the
overall design describes exists as working code today, so this piece runs that single step for now —
but it is wired so that adding each remaining step later changes nothing about its own shape.

## Scope

This specification defines the `internal/controller/snowflakeaccount/` package that:

- Registers a Crossplane-style managed-resource controller for `SnowflakeAccount`, wired from
  `cmd/provider/main.go` through `internal/controller/yukimi.go`.
- Runs a validation phase before any provisioning: resolves the CRD's `region` against the Backplane
  Config and rejects an unknown or not-yet-`available` region.
- Fetches the tenant namespace's labels on every reconcile and threads them, together with the
  resolved region and a scoped logger, into one shared `ModuleContext` per call.
- Calls the account pipeline's `Observe` from its own `Observe`, and the pipeline's `Apply` from both
  `Create` and `Update` (identical bodies, since `Apply` is idempotent by construction).
- Renders the pipeline's aggregate result onto the resource: the `Ready`/`Synced` conditions,
  `status.accountName`, `status.accountLocator`, `status.accountUrl`, and advances
  `status.observedGeneration` exactly when every module in a run reported done.
- Drops the account on deletion with a single, unconditional, idempotent `DROP ACCOUNT IF EXISTS`
  statement, then releases the finalizer.
- Registers exactly one pipeline module today — account bootstrapping (010) — through the same
  generic module-list `internal/account` (009) already supports, so registering 011–013/015/016 later
  is an addition to a list, not a change to this package.

**Out of Scope**:

- Any guardrail check or approved-exceptions lookup (008) — not wired in; a CRD that would fail a
  guardrail is accepted and provisioned as-is today.
- Any credit-quota admission check or enforcement (016) — not wired in; a CRD's `creditQuota` is never
  checked against the namespace ceiling today.
- `IdentitySyncRequest` emission and the `IdentitySynced` condition (014/015) — not wired in.
- The `QuotaAvailable` condition and its exhaustion handling (016) — not wired in.
- Positive-control deletion warrants (017) — `Delete` runs unconditionally; there is no warrant lookup,
  no `Terminating` stall, and no `DeletionBlocked` event anywhere in this package today.
- Executing any SQL beyond the single `DROP ACCOUNT` statement this package issues itself. Every other
  statement belongs to a pipeline module (009–013, 015).
- Drift detection or repair of anything the account module doesn't already re-assert on `Apply`.
- Replication (019) and every other resource type — this package reconciles `SnowflakeAccount` only.

## Key Concept: A Pipeline of One, Built to Grow

The account-provisioning pipeline (009) is generic over an ordered list of modules; nothing about its
contract privileges having more than one. Today this controller constructs that pipeline with exactly
one module — account bootstrapping (010) — because that is the only provisioning step with working
code. The controller's own shape does not know or care how many modules are registered: it calls
`Observe`/`Apply` the same way regardless, and renders whatever the pipeline reports. Adding the
parameter, network, auth, identity, or quota module later is purely an edit to the ordered list this
package constructs at startup — no method on this controller changes.

The same narrowing applies to the validation phase that design.md describes running before
provisioning: guardrails, approved exceptions, and quota admission are all absent today, because the
packages that would perform them don't exist yet. Only the region lookup and its `available` gate (007)
run — both already implemented, and already the two checks this package's `Observe`/`Create`/`Update`
share. A CRD that would fail a not-yet-wired check is accepted and provisioned as if it had passed.

**Important**: none of this is drift or an oversight to be quietly fixed later. Every omitted capability
is a deliberate, recorded reduction of design.md's full picture, each with its own point of entry once
the corresponding spec (008, 011–013, 015, 016, 017) lands.

## Key Concept: Existence Decides Everything, Even Deletion

This controller's `Observe` reports whether the account exists and is reachable — nothing more,
nothing deletion-specific. It does not special-case a resource whose `deletionTimestamp` is set: the
managed reconciler itself decides, from that existence signal plus the deletion timestamp, whether to
call `Create`/`Update` or `Delete` next. Reporting existence identically either way is what lets a real,
reachable account still trigger a deletion attempt instead of having its finalizer silently released.

**Important**: existence and reachability are the same signal here, because the one registered module's
`Observe` can only report "not yet created" and "created but currently unreachable" as the same boolean.
A currently-unreachable account is therefore, from this controller's point of view, indistinguishable
from one that was never created — on deletion, this means the account is never dropped, and the
Kubernetes object simply disappears once its finalizer is released. This is an accepted limitation of
today's one-module pipeline, not a bug to route around here (see Edge Cases).

## Key Concept: Delete Now, Warrant Later

Design.md's Positive Control model (§6.2–6.3) gates account destruction behind a separate deletion
warrant resource that doesn't exist yet (017). Until it does, this controller's `Delete` performs the
destructive action itself, unconditionally: it issues one idempotent `DROP ACCOUNT IF EXISTS` statement
over the org-admin connection and lets the finalizer release, every time it is invoked, whether or not
the account was ever actually created. There is no lock to check, no key to present, and no stall state
— deleting the Kubernetes object is, today, sufficient to destroy the Snowflake account behind it.

**Important**: this is a deliberate, temporary reduction of design.md's own security model, not a
simplification that happens to be safe. When 017 lands, this method is replaced outright — not
extended — with the warrant-gated version design.md describes.

## Public API

```go
package snowflakeaccount

// Dependencies are the runtime collaborators cmd/provider/main.go constructs once at startup and
// injects into this controller. Each is already fully built by the time Setup is called — this
// package never constructs, loads, or owns any of them.
type Dependencies struct {
    Config    *config.BaseConfig      // 002 — read for Snowflake.UsePrivateLink (tenant.AccountURL)
    Backplane *backplane.Config       // 007 — region lookup and its available gate
    Pipeline  *account.Pipeline       // 009 — constructed from account.New(accountmodule.New(...))
                                       // today; grows one argument per module as 011–013/015/016 land
}

// Setup adds a controller that reconciles SnowflakeAccount managed resources.
func Setup(mgr ctrl.Manager, o controller.Options, deps Dependencies) error

// SetupGated adds the same controller with safe-start support, matching every other resource type's
// registration in internal/controller/yukimi.go.
func SetupGated(mgr ctrl.Manager, o controller.Options, deps Dependencies) error
```

`connector` and `external` — the types that actually implement `managed.TypedExternalConnector`/
`TypedExternalClient[*v1alpha1.SnowflakeAccount]` — are unexported, exactly as the project's own
controller scaffold (`hack/helpers/controller/KIND_LOWER/KIND_LOWER.go.tmpl`) generates them; nothing
outside this package calls `Observe`/`Create`/`Update`/`Delete` directly, so their behavior is
documented under the Key Concepts above and the Appendix below rather than as a formal API surface.

## Project Structure

### Source Code

```text
internal/controller/snowflakeaccount/
├── controller.go        # Setup, SetupGated, Dependencies, connector, external, Connect, Disconnect
├── controller_test.go
├── observe.go            # Observe: validation phase, namespace label fetch, pipeline.Observe, Ready
├── observe_test.go
├── apply.go              # apply(): shared Create/Update body — validation phase, pipeline.Apply,
│                          #   status persistence, condition rendering, ObservedGeneration advance
├── apply_test.go
├── delete.go             # Delete: DROP ACCOUNT IF EXISTS, no warrant gating (Key Concept: Delete Now)
├── delete_test.go
├── context.go            # buildModuleContext: region+available validation, namespace label fetch,
│                          #   account.NewModuleContext construction — shared by observe.go/apply.go
├── context_test.go
├── status.go             # persistStatus: accountName/accountLocator/accountUrl from a ModuleContext
├── status_test.go
├── conditions.go         # renderReady: Ready aggregation from Observation/Result and GatesReady
├── conditions_test.go
└── integration_test.go   # TestIntegration...: live create → reconnect → delete round trip
```

Two existing files are modified, not created:

- `internal/controller/yukimi.go` — `SetupGated` gains a parameter carrying this package's
  `Dependencies` (and, later, one field per additional resource type) and forwards it into
  `snowflakeaccount.SetupGated`.
- `cmd/provider/main.go` — gains the `--configDir` flag `specs/002-base-config.md` already specifies,
  loads `config.BaseConfig` (002) and `backplane.Config` (007), constructs the AWS secrets backend
  (003.a) wrapped in `secrets.NewCachedBackend` (003), constructs the connection `pool.Pool` (004),
  builds the one-module `account.Pipeline` (009) from `accountmodule.New` (010), assembles
  `snowflakeaccount.Dependencies`, and calls `yukimi.SetupGated` with it. Also closes the `Pool` on
  shutdown.

## Error Classification

**User Errors** (use `errors.NewUserError()`):
- `spec.region` names a region absent from the Backplane Config (007's own `Region()` error, bubbled
  unchanged).
- `spec.region` names a region present in the Backplane Config but with `available: false` — this
  package's own user error, since 007 deliberately leaves that decision to its caller.
- Any `Rejected` outcome surfaced through a module's `Outcome.Err` — already classified by that module
  (010 today); this package renders it, never reclassifies it.

**System Errors** (use `fmt.Errorf("context: %w", err)`):
- Fetching the tenant namespace (for its labels) via the Kubernetes API fails.
- Any `Failed` outcome surfaced through a module's `Outcome.Err` — already classified by that module.
- The `DROP ACCOUNT IF EXISTS` statement fails, for any reason — including a retention-locked snapshot
  blocking the drop; this package applies no special classification of its own to any `DROP ACCOUNT`
  failure (Key Concept: Delete Now, Warrant Later).
- `tenant.AccountURL` fails after a successful `Apply` (malformed region) — logged, but does not fail
  the reconcile; see Edge Cases.

## Edge Cases

- **A resource is deleted before its account was ever created (no locator).** `Observe` already
  reports `ResourceExists: false` for this resource, so the managed reconciler never calls `Delete` at
  all — it removes the finalizer directly. There is nothing to drop.
- **A resource is deleted while its account exists but is currently unreachable.** `Observe` reports
  the same `ResourceExists: false` as the case above — 009's `Pipeline.Observe` collapses both into one
  boolean (Key Concept: Existence Decides Everything). The managed reconciler again skips `Delete` and
  releases the finalizer: the Kubernetes object disappears, but the live Snowflake account is not
  dropped. This is an accepted gap on top of the already-accepted absence of warrant gating (D-002);
  both close together once 017 replaces `Delete` outright.
- **A CRD's region is unknown, or known but not yet `available`.** Both are rendered the same way:
  `Ready=Unavailable()` with no error returned from `Observe` (per CLAUDE.md's error-handling pattern),
  and a returned, framework-classified error from `Create`/`Update` (per CLAUDE.md's Create/Update/
  Delete pattern).
- **A CRD would fail a guardrail, or exceeds its namespace's credit ceiling.** Accepted and provisioned
  as-is; neither check runs anywhere in this package today (D-003). Not a bug — see Key Concept: A
  Pipeline of One.
- **`tenant.AccountURL` fails after `Apply` already persisted a locator.** `status.accountName` and
  `status.accountLocator` are still set; only `status.accountUrl` is left blank, and the failure is
  logged (not returned) — the account itself was created successfully, so a cosmetic URL failure must
  not fail the reconcile or block `Ready`.
- **An account module outcome that isn't `Done` persists across polls.** `status.observedGeneration`
  never advances (009's own accepted cost), so every poll re-runs the pipeline until the tenant or an
  operator resolves it — with one module, this is a reconnect check or, at worst, a repeated
  `CREATE ACCOUNT` attempt that the account module itself refuses to repeat once a locator is known.
- **`IdentitySynced`/`QuotaAvailable` are never set — not even to `Unknown`.** Consistent with 009's own
  rule: an absent module's condition is left exactly as the previous reconcile left it, never faked
  (D-004). Neither condition is rendered by this package today because no registered module owns one.
- **No `QuotaExhausted`, `SyncTimeout`, or `DeletionBlocked` warning event is ever emitted.** Each
  belongs to a gate or module (016, §4.3, 017) not yet wired into this controller; this package holds no
  `event.Recorder` of its own today because it has nothing of its own to emit through one.
- **`Observe` cannot report why the account is unreachable.** 009's `Pipeline.Observe` discards each
  module's per-call `Outcome`, keeping only the aggregate boolean — so a connection failure surfaces
  only as `Ready=Unavailable()` with a fixed, non-specific message during `Observe`. The module's own
  classified message is only available from the next `Apply` call's `Result`, and only once one runs.

## Dependencies

- **`internal/errors` (001)** - Used APIs: `errors.NewUserError()` - Contract: constructed directly for
  the not-yet-`available` region check; every other user error is bubbled from a lower layer unchanged.
- **`internal/logger` (001)** - Used APIs: `logger.New()`, `(*Logger).Handle()` - Contract: one
  `*Logger` per `Observe`/`Create`/`Update`/`Delete` call, scoped with that call's operation.
- **`internal/config` (002)** - Used APIs: `BaseConfig.Snowflake.UsePrivateLink` - Contract: read only
  when computing `status.accountUrl`; this package never calls `config.Load` itself.
- **`internal/backplane` (007)** - Used APIs: `Config.Region()`, `Region.Available` - Contract: called
  once per `Observe`/`Create`/`Update` as the validation phase; this package never calls
  `backplane.Load` itself.
- **`internal/account` (009)** - Used APIs: `Pipeline.Observe()`, `Pipeline.Apply()`,
  `NewModuleContext()`, `ModuleContext.ResolvedAccountName()`/`.Locator()`, `Observation`, `Result`,
  `Result.AllDone()`, `GatesReady` - Contract: one `ModuleContext` built per call, handed to both pipeline
  entry points; never reused across calls.
- **`internal/account/modules/account` (010)** - Used APIs: `New()` - Contract: constructed once, in
  `cmd/provider/main.go`, and registered as the pipeline's sole module; this package never calls its
  `Observe`/`Apply` directly.
- **`internal/tenant` (006)** - Used APIs: `AccountURL()` - Contract: called once per successful
  `Apply`, from the same `ModuleContext` values already resolved; `ResolveName` is called indirectly,
  inside `NewModuleContext`, never by this package itself.
- **`internal/snowflake/pool` (004)** - Used APIs: `Pool.OrgAdmin()` - Contract: the only connection
  scope `Delete` ever opens; every other connection this package uses reaches Snowflake only through
  `ModuleContext`.
- **`internal/snowflake/statement` (005)** - Used APIs: `statement.New()`, `Runner.Exec()`,
  `BareIdentifier()` - Contract: renders `DROP ACCOUNT`'s resolved account name exactly as 010 renders
  `CREATE ACCOUNT`'s.
- **`apis/base/v1alpha1` (006)** - Used APIs: `SnowflakeAccount`, `SnowflakeAccountGroupVersionKind`,
  `SnowflakeAccountGroupKind` - Contract: the managed resource type this controller reconciles.

## Integration Points

- **`cmd/provider/main.go`** - Constructs every member of `Dependencies`, assembles the pipeline in
  registration order, and calls `yukimi.SetupGated` - Key functions: `config.Load()`,
  `backplane.Load()`, `secrets.NewCachedBackend()`, `pool.New()`, `accountmodule.New()`, `account.New()`.
- **`internal/controller/yukimi.go`** - Forwards its own `Dependencies` parameter into
  `snowflakeaccount.SetupGated` alongside every other resource type's registration.
- **`internal/account` (009) / `internal/account/modules/account` (010)** - Every Snowflake-facing
  operation except `Delete`'s `DROP ACCOUNT` runs through the pipeline these specs define; this package
  never issues any other statement itself.
- **Forward contracts** (each is a future spec's job, not a gap this package's shape needs to change to
  accommodate):
  - **008 (guardrails)** - inserted as a validation-phase step before the pipeline is ever invoked, in
    both `Observe` (read-only) and `apply()` (enforced).
  - **011–013, 015 (parameter/network/auth/identity modules)** - each added as a further argument to the
    `account.New(...)` call in `cmd/provider/main.go`, in the fixed order 010→011→012→013→015→016; no
    change to this package's own methods.
  - **015, 016** also introduce the first modules that set `Outcome.Condition`
    (`IdentitySynced`/`QuotaAvailable`) - at that point `renderReady` (conditions.go) gains the
    per-module condition-rendering loop 009's Appendix sketches; it does nothing today because nothing
    produces a condition to render.
  - **016 (quota)** also owns `Admit()`, called separately from the pipeline, in `apply()`'s validation
    phase alongside guardrails once 016 lands.
  - **017 (deletion warrants)** - `Delete` is replaced outright (not extended) with the warrant-gated
    version design.md §6.3 describes.

## Success Criteria

- **SC-001**: `Setup`/`SetupGated` register a controller for `SnowflakeAccountGroupVersionKind` that
  compiles against `managed.NewReconciler` with a `TypedExternalConnector[*v1alpha1.SnowflakeAccount]`.
- **SC-002**: `Observe` returns `ResourceExists: false` with no condition change when the pipeline
  reports the account does not exist, whether because it was never created or because it is currently
  unreachable.
- **SC-003**: `Observe` returns `ResourceExists: true`, `ResourceUpToDate` computed as
  `status.observedGeneration == generation && Observation.InSync`, and `Ready=Available()` when the
  pipeline reports the account exists and in sync.
- **SC-004**: `Observe` sets `Ready=Unavailable()` and returns a nil error when the named region is
  unknown or not `available`.
- **SC-005**: `Observe` and `apply()` each fetch the tenant namespace's labels exactly once per call and
  pass them unchanged into `NewModuleContext`.
- **SC-006**: `Create` and `Update` share one `apply()` body; given the same CRD and Snowflake state,
  both produce identical status and condition outcomes.
- **SC-007**: `apply()` persists `status.accountLocator` immediately after `Pipeline.Apply` returns,
  before rendering any condition or computing `status.accountUrl`.
- **SC-008**: `status.accountName` and `status.accountUrl` are computed from
  `ModuleContext.ResolvedAccountName()`/`.Locator()`, never from any module's `Outcome`.
- **SC-009**: A `tenant.AccountURL` failure leaves `status.accountUrl` blank and is logged, without
  returning an error from `apply()`; `accountName`/`accountLocator` are unaffected.
- **SC-010**: `cr.Status.SetObservedGeneration(cr.Generation)` is called if and only if
  `Result.AllDone()` is true.
- **SC-011**: `Ready` is `Available()` only when every module that ran in the last `Apply`/`Observe`
  reported success and the run did not abort; `Unavailable()` otherwise.
- **SC-012**: `Delete` issues exactly one
  `DROP ACCOUNT IF EXISTS <resolvedName> GRACE_PERIOD_IN_DAYS = 3` statement over the org-admin
  connection, unconditionally.
- **SC-013**: `Delete` performs no warrant lookup, sets no condition of its own, and emits no
  `DeletionBlocked` event.
- **SC-014**: A `DROP ACCOUNT` failure is returned to the framework via `log.Handle` with no additional
  classification by this package.
- **SC-015**: No guardrail check, quota-admission call, or `IdentitySyncRequest` emission occurs
  anywhere in this package (grep-provable: no import of a not-yet-existing 008/015/016 package).
- **SC-016**: No `QuotaAvailable` or `IdentitySynced` condition is ever set by this package.
- **SC-017**: `cmd/provider/main.go` starts successfully with a `--configDir` pointing at a valid
  `baseConfig.yaml` and `backplane.yaml`, and registers the SnowflakeAccount controller.
- **SC-018**: Unit test coverage of `internal/controller/snowflakeaccount` exceeds 95%.
- **SC-019**: `integration_test.go` proves a create → reconnect → delete round trip against a live
  Snowflake organization.

## Security Considerations

- The org-admin connection is opened in this package only inside `Delete`, for `DROP ACCOUNT` alone —
  every other Snowflake operation this package triggers runs through the pipeline's own tenant-scoped
  connection, preserving 010's privilege step-down guarantee (design.md §3.11) at this layer too.
- `Delete`'s unconditional `DROP ACCOUNT` is a deliberate, temporary reduction of design.md §6.2–6.3's
  Positive Control model (Key Concept: Delete Now, Warrant Later) — deleting a `SnowflakeAccount` object
  today immediately and irreversibly destroys the underlying Snowflake account, with no warrant, no
  time-boxed window, and no `DeletionBlocked` stall. Operators relying on design.md's documented
  deletion safeguards must know these do not exist until 017 lands.
- No guardrail or quota-admission check runs before provisioning (D-003): a CRD naming a disallowed
  region pattern, an oversized credit quota, or an out-of-policy network range is accepted and
  provisioned exactly as written today. This is a real, accepted expansion of what a tenant can request
  until 008/016 land — not a defect introduced by this package.
- `DROP ACCOUNT`'s only tenant-influenced value is the resolved account name, rendered as a bare
  identifier (`statement.BareIdentifier`) exactly as 010 renders `CREATE ACCOUNT`'s — never concatenated
  raw into the statement text.

## Performance Considerations

- Every reconcile fetches the tenant namespace (one `Get`) even though nothing in this package's own
  logic reads its labels yet (D-007) — cheap, and avoids a second migration once 008/016 land and need
  them.
- Per-poll cost is bounded entirely by the one registered module today: a reconnect check when already
  in sync, or a handful of statements on a fresh create. No enumeration/pruning query runs until 012/013
  register their own modules.

## References

- **Product design**: `specs/design.md` §3.2 (create-flow lifecycle), §3.5 (region `available` gate),
  §3.6/§3.11/§3.12 (bootstrapping, privilege step-down, naming), §6.2–6.3 (Positive Control, deletion
  phases), §7.1–7.2 (condition and status model).
- **Template**: `specs/000-template.md` — the section skeleton this spec follows.
- **Account Pipeline**: `specs/009-account-pipeline.md` and `internal/account/{module,pipeline,context,
  conditions}.go` — `Module`/`Pipeline`/`Outcome`/`Result` contract this package drives.
- **Account Module**: `specs/010-account-module.md` and `internal/account/modules/account/*.go` — the
  sole registered module today; source of the crash-window note behind SC-007.
- **SnowflakeAccount CRD & tenant helpers**: `specs/006-snowflake-account-crd.md`,
  `apis/base/v1alpha1/snowflakeaccount_types.go`, `internal/tenant/`.
- **Backplane Config**: `specs/007-backplane-config.md`, `internal/backplane/backplane.go`.
- **Base Configuration**: `specs/002-base-config.md` — the `--configDir` flag contract
  `cmd/provider/main.go` already owed and this spec's implementation finally exercises.
- **Statement Execution**: `specs/005-statement-execution.md`, `internal/snowflake/statement/`.
- **Connection Pooling**: `specs/004-connection-pooling.md`, `internal/snowflake/pool/pool.go`.
- **Error/Logging**: `specs/001-error-and-logging.md`, `internal/errors/`, `internal/logger/`.
- **Controller scaffold**: `hack/helpers/controller/KIND_LOWER/KIND_LOWER.go.tmpl` — the
  `TypedExternalConnector`/`TypedExternalClient` shape this package's `connector`/`external` follow.
- **Vendored behavior**: `crossplane-runtime/v2@v2.0.0` `pkg/reconciler/managed/reconciler.go` — confirms
  `Delete` is only invoked when the prior `Observe` reported `ResourceExists: true`; confirms `Create`
  forces `Creating()` after returning while `Update` does not touch `Ready`, the asymmetry behind
  "Observe always wins" (D-005).

<br/><br/><br/><br/><br/>

================

## Appendix: Usage Examples

### Example 1: Wiring `Setup` from `cmd/provider/main.go`

```go
cfg, err := config.Load(*configDir)
kingpin.FatalIfError(err, "cannot load base config")

bp, err := backplane.Load(*configDir)
kingpin.FatalIfError(err, "cannot load backplane config")

backend, err := aws.New(cfg.AWS.Region, cfg.AWS.KmsKeyId)
kingpin.FatalIfError(err, "cannot construct secrets backend")
cached := secrets.NewCachedBackend(backend, cfg.Secrets.CacheTTL)

connPool := pool.New(cached, cfg)
defer connPool.Close()

pipeline := account.New(
    accountmodule.New(cached, cfg.Snowflake.Org),
    // 011-016 join this list as each lands; no other change here.
)

deps := snowflakeaccount.Dependencies{Config: cfg, Backplane: bp, Pipeline: pipeline}
kingpin.FatalIfError(yukimi.SetupGated(mgr, o, deps), "cannot setup Yukimi controllers")
```

### Example 2: `Observe`

```go
func (e *external) Observe(ctx context.Context, cr *v1alpha1.SnowflakeAccount) (managed.ExternalObservation, error) {
    log := logger.New(e.logger, cr.Namespace, "SnowflakeAccount", cr.Name, logger.OpObserve)

    mc, err := e.buildModuleContext(ctx, cr, log)
    if err != nil {
        retryErr := log.Handle(err)
        cr.SetConditions(xpv1.Unavailable().WithMessage(retryErr.Error()))
        return managed.ExternalObservation{}, nil
    }

    obs, _ := e.pipeline.Observe(ctx, mc) // err always nil today (009)
    if !obs.Exists {
        return managed.ExternalObservation{ResourceExists: false}, nil
    }

    if obs.InSync {
        cr.SetConditions(xpv1.Available())
    } else {
        cr.SetConditions(xpv1.Unavailable())
    }

    return managed.ExternalObservation{
        ResourceExists:   true,
        ResourceUpToDate: cr.Status.GetObservedGeneration() == cr.Generation && obs.InSync,
    }, nil
}
```

### Example 3: `apply()`, shared by `Create` and `Update`

```go
func (e *external) apply(ctx context.Context, cr *v1alpha1.SnowflakeAccount, op logger.Operation) error {
    log := logger.New(e.logger, cr.Namespace, "SnowflakeAccount", cr.Name, op)

    mc, err := e.buildModuleContext(ctx, cr, log)
    if err != nil {
        return log.Handle(err)
    }

    result, _ := e.pipeline.Apply(ctx, mc) // err always nil today (009)

    e.persistStatus(cr, mc) // accountLocator first — shrinks the crash window (010)

    for _, mo := range result.Outcomes {
        if mo.Outcome.Err != nil {
            log.Handle(mo.Outcome.Err) // incident-tracked; condition already carries the message
        }
    }
    e.renderReady(cr, result)

    if result.AllDone() {
        cr.Status.SetObservedGeneration(cr.Generation)
    }
    return nil
}

func (e *external) Create(ctx context.Context, cr *v1alpha1.SnowflakeAccount) (managed.ExternalCreation, error) {
    return managed.ExternalCreation{}, e.apply(ctx, cr, logger.OpCreate)
}

func (e *external) Update(ctx context.Context, cr *v1alpha1.SnowflakeAccount) (managed.ExternalUpdate, error) {
    return managed.ExternalUpdate{}, e.apply(ctx, cr, logger.OpUpdate)
}
```

### Example 4: `Delete`

```go
func (e *external) Delete(ctx context.Context, cr *v1alpha1.SnowflakeAccount) (managed.ExternalDelete, error) {
    log := logger.New(e.logger, cr.Namespace, "SnowflakeAccount", cr.Name, logger.OpDelete)

    resolvedName := tenant.ResolveName(cr.Name, cr.Namespace)
    nameToken, err := statement.BareIdentifier(resolvedName)
    if err != nil {
        return managed.ExternalDelete{}, log.Handle(err)
    }

    db, err := e.pool.OrgAdmin(ctx)
    if err != nil {
        return managed.ExternalDelete{}, log.Handle(err)
    }

    runner := statement.New(db)
    sql := fmt.Sprintf("DROP ACCOUNT IF EXISTS %s GRACE_PERIOD_IN_DAYS = 3", nameToken)
    if err := runner.Exec(ctx, "drop account", sql); err != nil {
        return managed.ExternalDelete{}, log.Handle(fmt.Errorf("failed to drop account: %w", err))
    }
    return managed.ExternalDelete{}, nil
}
```
