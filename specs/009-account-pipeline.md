# Specification: Account Pipeline (009)

## Overview

Provisioning a Snowflake account is not one step but many: creating the account, applying its
parameters, opening network access, binding authentication exceptions, importing identity groups,
and enforcing a credit quota. This package gives each of those steps its own self-contained unit,
called a module, and runs them in a fixed order on every reconcile. It exists so that the controller
for `SnowflakeAccount` stays a thin caller instead of one large function that knows about every
concern at once, and so each concern can be built and tested on its own. A module reports back one of
a small, fixed set of outcomes — done, pending, rejected, or failed — so the controller can turn what
happened into Kubernetes conditions without understanding what any individual module actually did.

## Scope

- An ordered list of modules, run strictly in sequence.
- Two entry points: a read-only `Observe` that mutates nothing in Snowflake, and a mutating `Apply`
  that re-asserts every module's desired state.
- A shared, per-reconcile context that carries what every module needs — the CRD, the resolved
  region, the namespace's labels, a scoped logger, and a lazily-resolved account connection — so no
  module recomputes or disagrees with another about any of it.
- A fixed outcome vocabulary (`Done`, `Pending`, `Rejected`, `Failed`) that every module reports
  through, plus a generic signal any module's outcome can carry to stop the run early.
- Collecting what ran into one `Result`, and a static table deciding which of a module's own
  conditions forces the resource's aggregate `Ready` to `False`.

**Out of Scope**:
- Executing any SQL itself. Every statement belongs to a module (010–013, 015); this package only
  sequences calls into them.
- Classifying any error as a user or system error. Each module is the only code that knows why its
  own call failed, so each module classifies its own failures before reporting an `Outcome`.
- Detecting or repairing drift. No module reads Snowflake state back to compare and repair it;
  Organization Policies will make that state org-owned, so the work would not survive to be used. The
  one read-back sanctioned here is a pruning module's enumeration of objects the CRD no longer lists —
  it drops, it never repairs (see Key Concept below).
- Any teardown. Deletion is a single `DROP ACCOUNT` plus finalizer release owned by 017/018 and
  cascades to every object inside the account — there is no per-module teardown to sequence.
- Guardrail admission (008). That runs as its own gate inside the controller, entirely *before* the
  pipeline is ever invoked — no module, and no field on the shared context, ever carries a guardrail
  verdict.
- Adding any field to the `SnowflakeAccount` CRD's schema. A module may still add its own named
  `status` field where it genuinely needs to remember something across reconciles (015's sync start
  timestamp is the known case) — that is the module's own spec to state, not this package's.

## Key Concept: Sequential Modules, One Abort Signal

Modules run one after another, strictly in the order they were registered, never in parallel and
never calling one another. The first module registered is always the account module (010): its
`Observe` result is the only source of whether the account exists at all, and every later module
needs the live connection only it can establish.

A module's outcome can carry a generic signal that stops the run: if set, no later module runs on
this pass. Nothing about the pipeline or the module contract privileges any particular module for
this — any module's outcome can carry it. In practice, only the account module uses it today, because
it is the only module whose failure makes every later statement meaningless: an account that failed to
create, was rejected outright, or is not yet ready to accept a connection leaves nothing for a network
rule, an auth exception, or a quota to attach to.

**Important**: every other module's outcome, however it turns out, never stops the modules after it.
A rejected network-rule entry must not prevent the auth module, the identity module, or the quota
module from running on the same pass — design's rule that a rejected entry simply leaves the account
on its baseline (§3.8/§3.9) only holds if the modules after the rejecting one still get to run.

## Key Concept: Overwrite Apply, Generation-Gated Re-Apply

`Apply` never diffs against current Snowflake state before acting — no module reads back what it
applied last time. Instead, each module simply re-asserts its whole desired state on every call:
`CREATE OR REPLACE` for a rule or policy, `SET` for a parameter, re-binding wherever a binding exists.
This is what makes `Apply` safe to call from both `Create` and `Update` with no other logic in
between (see Public API) — a run interrupted halfway leaves a partially-applied account that the very
next `Apply` finishes re-asserting in full, with nothing to resume and nothing to compensate for.

Because no module diffs to decide whether to act, nothing on the Snowflake side tells the pipeline
*when* to bother calling `Apply` at all. That decision falls to the Kubernetes generation counter instead: the controller only
calls `Apply` when the CRD's generation has moved past what the last successful run recorded, and it
only records a new generation once every module in that run reported `Done`. A `Pending` identity
sync or a `Rejected` network-rule entry therefore keeps the pipeline re-applying on every reconcile
until whatever is wrong clears — which is exactly the retry-until-timeout behavior §4.3 needs, and the
"report until the tenant fixes it" behavior §3.8/§3.9 need.

**Important**: what a tenant removes from the CRD is removed from Snowflake. Before re-asserting a
tenant-supplied list, `Apply` enumerates the objects that module owns — `SHOW <objects> LIKE '<its own
prefix>'` — and drops every one the CRD no longer lists, unbinding it first. Deleting an entry from
`customNetworkRules` or `customAuthRules` moves the generation like any other edit, so the next
`Apply` prunes it.

Pruning compares names only, never definitions: an object the CRD lists but Snowflake has lost is
simply recreated by the overwrite. Nothing here repairs drift — Organization Policies (design.md
Appendix B) will make this state org-owned and tenant-unmodifiable, so a read-back built now would be
dead code. Only 012 and 013 prune, each naming its own prefix; baseline rules, account parameters and
identity bindings are untouched.

## Public API

```go
package account

// Module is implemented by each pipeline stage (010, 011, 012, 013, 015, 016).
type Module interface {
    Name() string

    // Observe is read-back only; it must mutate nothing in Snowflake.
    Observe(ctx context.Context, mc *ModuleContext) (inSync bool, outcome Outcome)

    // Apply re-asserts this module's full desired state, pruning any object the
    // CRD no longer lists. It must be safe to call repeatedly with no other call
    // in between (Key Concept: Overwrite Apply).
    Apply(ctx context.Context, mc *ModuleContext) Outcome
}

// Pipeline runs an ordered list of modules against one ModuleContext per call.
type Pipeline struct{ /* unexported */ }

// New builds a pipeline from an ordered module list. modules[0] must be the
// account module (010): its Observe result is the sole source of
// Observation.Exists, and every later module depends on the connection it
// establishes.
func New(modules ...Module) *Pipeline

// Observe calls every module's Observe in order and aggregates the result. It
// performs no mutation of its own.
//
// Returns:
//   - error: always nil today. Reserved for a future structural failure inside
//     the pipeline itself; no module can produce one — every failure a module
//     reports already lives in its own Outcome (see Error Classification).
func (p *Pipeline) Observe(ctx context.Context, mc *ModuleContext) (Observation, error)

// Apply calls every module's Apply in order, unconditionally, stopping early
// only if a module's Outcome has Abort set. It is idempotent by construction
// (Key Concept: Overwrite Apply) — callers may call it from both a create and
// an update path with identical behavior.
//
// Returns:
//   - error: always nil today, for the same reason as Observe.
func (p *Pipeline) Apply(ctx context.Context, mc *ModuleContext) (Result, error)

// Observation is Pipeline.Observe's result.
type Observation struct {
    Exists bool // from modules[0]'s Observe alone; no other module contributes to it
    InSync bool // true iff every module's Observe reported inSync == true
}

// State is the fixed vocabulary every Outcome reports through.
type State int

const (
    StateDone     State = iota // fully applied; nothing pending, nothing wrong
    StatePending               // not yet applied; expected to resolve on a later reconcile
    StateRejected              // the tenant's own input was refused
    StateFailed                // an unexpected failure calling out to Snowflake or another system
)

// Outcome is the only channel a module has to report what happened. Modules
// return no separate error — each Outcome is a complete, self-classified
// statement of what this module did on this call.
type Outcome struct {
    State     State
    Reason    string          // Pending only: the operator-visible reason for the wait
    Err       error           // Rejected/Failed only: the module's own classified error
    Abort     bool            // if true, Apply stops after this module on this pass
    Condition *xpv1.Condition // optional: a condition this module owns and wants surfaced
}

func Done() Outcome                // StateDone
func Pending(reason string) Outcome // StatePending
func Rejected(err error) Outcome    // StateRejected; err built with errors.NewUserError
func Failed(err error) Outcome      // StateFailed; err wrapped with fmt.Errorf

// Aborting returns o with Abort set true; every other field is unchanged. Only
// the account module (010) calls this today, on any outcome that is not Done.
func (o Outcome) Aborting() Outcome

// Result is Pipeline.Apply's result.
type Result struct {
    Aborted  bool
    Outcomes []ModuleOutcome // one entry per module that actually ran, in execution order
}

// ModuleOutcome pairs a module's name with the Outcome it returned.
type ModuleOutcome struct {
    Module  string
    Outcome Outcome
}

// AllDone reports whether every module ran and every one reported StateDone.
func (r Result) AllDone() bool

// ModuleContext is built once per reconcile and handed unchanged to every
// module. Everything on it is either immutable for the run or, in the case of
// the account locator, mutated by exactly one module (010).
type ModuleContext struct{ /* unexported */ }

// NewModuleContext builds the shared context for one reconcile.
//
// namespace is the trust anchor (design.md 3.11.1) the resolved account name
// is derived from — callers pass the bare namespace, not a pre-resolved name,
// so ResolvedAccountName() is computed once, here, and no two callers can
// disagree about it. namespaceLabels are the raw namespace labels set at
// onboarding (design.md 2); Department/CostCenter/CreditQuota are read from
// them the same way (internal/tenant, 006). If cr.Status.AccountLocator is
// already set, it seeds Locator() immediately — callers never seed it
// themselves; see SetLocator below for the only other way it changes.
func NewModuleContext(
    cr *v1alpha1.SnowflakeAccount,
    namespace string,
    backplaneRegion *backplane.Region,
    namespaceLabels map[string]string,
    log *logger.Logger,
) *ModuleContext

func (c *ModuleContext) CR() *v1alpha1.SnowflakeAccount
func (c *ModuleContext) ResolvedAccountName() string // tenant.ResolveName(cr.Name, namespace), resolved once
func (c *ModuleContext) BackplaneRegion() *backplane.Region
func (c *ModuleContext) NamespaceLabels() map[string]string
func (c *ModuleContext) Logger() *logger.Logger

// Locator returns the account locator, or "" if the account does not exist
// yet on this reconcile. Seeded by NewModuleContext from
// cr.Status.AccountLocator when already set; see SetLocator for the only way
// it changes afterward.
func (c *ModuleContext) Locator() string

// SetLocator records the locator immediately after CREATE ACCOUNT returns it,
// for the one reconcile where the account did not exist before this call.
// Only the account module (010) calls this.
func (c *ModuleContext) SetLocator(locator string)

// OrgAdminDB returns an org-admin-scoped connection (internal/snowflake/pool, 004).
// Only the account module (010) needs this scope.
func (c *ModuleContext) OrgAdminDB(ctx context.Context) (*sql.DB, error)

// TenantDB returns a connection scoped to this tenant's own account,
// resolved on first call and memoized for the rest of the run.
//
// Returns:
//   - System error if Locator() is still empty — every module after 010 needs
//     a locator, and getting one is the whole point of running 010 first.
func (c *ModuleContext) TenantDB(ctx context.Context) (*sql.DB, error)

// Custom condition types this package defines, plus the static table deciding
// which of them forces the resource's aggregate Ready to False. A module
// attaches its own condition to its Outcome (above); 018 collects and renders
// them, applying this table when aggregating Ready.
const (
    TypeQuotaAvailable xpv1.ConditionType = "QuotaAvailable" // design.md 3.10
    TypeIdentitySynced xpv1.ConditionType = "IdentitySynced" // design.md 4.3
)

var GatesReady = map[xpv1.ConditionType]bool{
    TypeIdentitySynced: true,  // §4.3 — nobody can administer the account until ACCOUNTADMIN is imported
    TypeQuotaAvailable: false, // §3.10 — the account is intact; warehouses are merely suspended
}
```

## Project Structure

```
internal/account/
├── module.go       # Module interface, Outcome, State, Done/Pending/Rejected/Failed, Aborting
├── pipeline.go     # Pipeline, New, Observe, Apply, Observation, Result, ModuleOutcome, AllDone
├── context.go      # ModuleContext, NewModuleContext, Locator/SetLocator, OrgAdminDB/TenantDB
└── conditions.go   # TypeQuotaAvailable, TypeIdentitySynced, GatesReady
```

## Error Classification

**User Errors**: this package produces none of its own. Each module classifies its own user errors
with `errors.NewUserError` before wrapping the result in `Rejected(err)` — a rejected network-rule
entry (§3.8) or a rejected auth exception (§3.9) are both the module's own classification, never this
package's.

**System Errors**: likewise none of this package's own, with one exception. Every module wraps its
own system failures with `fmt.Errorf("...: %w", err)` before returning `Failed(err)`. The one system
error this package itself can produce is `ModuleContext.TenantDB`'s error when `Locator()` is still
empty — every other failure surfacing from `OrgAdminDB`/`TenantDB` is `internal/snowflake/pool`'s
(004) own error, passed through unwrapped for the calling module to classify.

## Edge Cases

- **What does the reconciler do if it calls `Observe` without ever calling `Apply` afterward?** -
  Nothing breaks. The two entry points share no state (`ModuleContext` is rebuilt per call), and
  `Observe` performs no mutation, so the up-to-date path never touches Snowflake.
- **`Apply` aborts after the first of six modules — what does `Result` say about the other five?** -
  They are absent from `Result.Outcomes` entirely, not recorded with any placeholder state. A
  condition owned by an absent module is left exactly as the previous reconcile set it.
- **A module's `Rejected` condition was already surfaced on `Ready`; the tenant fixes the CRD and the
  next run succeeds — does the stale condition linger?** - No. Every module that ran on this pass
  returns a fresh `Outcome`, including a fresh `Condition`; the previous rejection is overwritten the
  moment that module reports `Done` instead.
- **What happens on the very first reconcile, before `CREATE ACCOUNT` has ever returned a locator?** -
  `ModuleContext.Locator()` returns `""`. Only the account module (010) can proceed without one; every
  later module's first call to `TenantDB` fails with a system error until 010 has called
  `SetLocator`, which is why 010 must run first and must abort on anything but `Done`.
- **A module returns `Pending` — who decides when the pipeline is retried?** - Nobody, at this layer.
  `Pending` carries only its reason string, no requeue hint; the controller's own poll interval governs
  when the next reconcile happens.
- **A tenant leaves one module permanently `Rejected` — does the pipeline keep re-running forever?** -
  Yes, by design: `observedGeneration` never advances past a run with any non-`Done` outcome (Key
  Concept: Overwrite Apply), so every poll re-applies every module until the tenant corrects the CRD.
  Each re-apply is a handful of idempotent statements plus one enumeration query per pruning module,
  so this is accepted as cheap-but-unbounded rather than solved here.

## Dependencies

- **`internal/errors` (001)** - Used APIs: `errors.NewUserError()` - Contract: each module calls this
  itself before returning `Rejected`; this package never calls it.
- **`internal/logger` (001)** - Used APIs: `logger.New()`, `(*Logger).Handle()` - Contract:
  `ModuleContext` carries a `*Logger` for modules to log through; only the caller that built the
  context calls `Handle` on a carried error, once per error.
- **`internal/snowflake/pool` (004)** - Used APIs: `Pool.OrgAdmin()`, `Pool.TenantAccount()` - Contract:
  `ModuleContext.OrgAdminDB`/`TenantDB` wrap these; `TenantDB` additionally requires a locator.
- **`internal/tenant` (006)** - Used APIs: `tenant.ResolveName()`, `tenant.Department()`,
  `tenant.CostCenter()`, `tenant.CreditQuota()` - Contract: `NewModuleContext` resolves the account
  name once via `ResolveName`; modules read the label accessors from `NamespaceLabels()` themselves.
- **`internal/config/backplane` (007)** - Used APIs: the `Region` type - Contract: the caller resolves the
  region once and passes it into `NewModuleContext`; this package never looks a region up itself.

No dependency on 008 (guardrails): guardrail admission is resolved by the controller as its own gate
before the pipeline is ever invoked, so this package neither imports nor references it.

## Integration Points

- **`internal/controller/snowflakeaccount` (018)** - Runs guardrail admission (008) as its own gate,
  entirely before building a `ModuleContext` or calling the pipeline at all. Calls `Pipeline.Observe`
  from the controller's own `Observe`, and `Pipeline.Apply` from both `Create` and `Update` with
  identical bodies. Registers modules in the fixed order 010 → 011 → 012 → 013 → 015 → 016. Owns
  rendering `Outcome.Condition` values, `GatesReady` aggregation, and advancing
  `status.observedGeneration`. - Key functions: `account.New()`, `(*Pipeline).Observe`,
  `(*Pipeline).Apply`, `account.NewModuleContext()`.
- **`internal/account/modules/{account,parameter,network,auth,identity}` (010–013, 015)** - Each
  implements `Module` and is registered with `account.New()` by 018.
- **`internal/quota` (016)** - Implements `Module` for the pipeline's purposes, but its admission check
  (`Admit()`) lives outside this contract entirely and is called separately, by 018's own validation
  phase, before the pipeline runs.

## Success Criteria

1. **SC-001**: `New(modules...)` preserves registration order; `Pipeline.Apply` calls each module's
   `Apply` in that exact order.
2. **SC-002**: `Observation.Exists` reflects only `modules[0]`'s `Observe` result, regardless of what
   later modules report.
3. **SC-003**: `Observation.InSync` is true iff every module's `Observe` returned `inSync == true`.
4. **SC-004**: An `Outcome` with `Abort == true` stops `Pipeline.Apply` immediately after that module;
   `Result.Aborted` is true and `Result.Outcomes` contains no entry for any later module.
5. **SC-005**: A non-aborting `Outcome` (Rejected, Failed, or Pending) from any module does not prevent
   later modules from running.
6. **SC-006**: `Done()`, `Pending()`, `Rejected()`, `Failed()` construct an `Outcome` with the correct
   `State` and only the fields documented for that state populated.
7. **SC-007**: `Outcome.Aborting()` returns a copy with `Abort` set true and every other field
   unchanged.
8. **SC-008**: `Result.AllDone()` is true iff `Outcomes` is non-empty, `Aborted` is false, and every
   entry's `State` is `StateDone`.
9. **SC-009**: `ModuleContext.TenantDB` returns a system error when `Locator()` is empty, and never
   calls the pool when it is.
10. **SC-010**: `ModuleContext.TenantDB` resolves the connection once and returns the same `*sql.DB`
    on every subsequent call within the same context.
11. **SC-011**: `ModuleContext.ResolvedAccountName()` returns the same value `tenant.ResolveName` would
    compute directly from the same CRD name and namespace.
12. **SC-012**: Unit test coverage of `internal/account` is at least 95%.

## Security Considerations

- The pipeline itself never holds a Snowflake connection or executes a statement — each module
  requests exactly the connection scope it needs through `ModuleContext` (org-admin only for the
  account module, the tenant's own account scope for every other module). A bug in sequencing can
  therefore reorder or skip a module's *work*, but cannot hand any module a broader connection than
  the one it explicitly asked for.
- Pruning (Key Concept: Overwrite Apply) keeps the CRD an honest record of who can reach the account:
  an entry the tenant deletes takes its rule, policy and binding with it, so no live access outlives
  the text that granted it. What remains is access created outside a pruning module's prefix — a policy
  the tenant names freely and binds by hand is neither enumerated nor dropped. Organization Policies
  close that residue by making the state org-owned.

## References

- **Product design**: `specs/design.md` §3.2 (create flow), §3.6-§3.9 (bootstrapping, identity,
  network and auth rules), §3.10 (credit quota), §3.11 (privilege step-down), §4.3 (`IdentitySynced`),
  §7.1/§7.2 (condition and status model).
- **Template**: `specs/000-template.md` — the section skeleton this spec follows.
- **Shape reference**: `specs/007-backplane-config.md` — Public API and Error Classification
  phrasing followed here.
- **Dependency code**: `internal/snowflake/pool/pool.go` (`OrgAdmin`, `TenantAccount`),
  `internal/tenant/` (`ResolveName`, `Department`, `CostCenter`, `CreditQuota`),
  `internal/config/backplane/backplane.go` (`Region`), `internal/logger/logger.go` (`New`, `Handle`),
  `apis/base/v1alpha1/snowflakeaccount_types.go` (`SnowflakeAccountStatus`).
- **Vendored behavior**: `crossplane-runtime/v2@v2.0.0` `pkg/reconciler/managed/reconciler.go` — the
  managed reconciler sets `Creating()`/`ReconcileSuccess()` after `Create` returns and after
  `Observe` returns on the up-to-date path, so 018 must re-aggregate `Ready` on every `Observe` rather
  than relying on what a prior `Apply` set.

<br/><br/><br/><br/><br/>
================

## Appendix: Usage Examples

The Go examples below illustrate call shape and sequencing, not exact compilable code — the precise
condition-rendering and `Ready` aggregation logic belongs to 018, which is not yet written.

### Example 1: The Controller's `Observe`

```go
func (e *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
    cr := mg.(*v1alpha1.SnowflakeAccount)
    log := logger.New(e.logger, cr.Namespace, "SnowflakeAccount", cr.Name, logger.OpObserve)

    if cr.GetDeletionTimestamp() != nil {
        return managed.ExternalObservation{ResourceExists: false}, nil
    }

    region, err := e.backplane.Region(cr.Spec.Region)
    if err != nil {
        retryErr := log.Handle(err)
        cr.SetConditions(xpv1.Unavailable().WithMessage(retryErr.Error()))
        return managed.ExternalObservation{}, nil
    }

    mc := account.NewModuleContext(cr, cr.Namespace, region, e.namespaceLabels(cr.Namespace), log)

    obs, err := e.pipeline.Observe(ctx, mc)
    if err != nil {
        retryErr := log.Handle(err)
        cr.SetConditions(xpv1.Unavailable().WithMessage(retryErr.Error()))
        return managed.ExternalObservation{}, nil
    }
    if !obs.Exists {
        return managed.ExternalObservation{ResourceExists: false}, nil
    }

    upToDate := cr.Status.GetObservedGeneration() == cr.Generation && obs.InSync
    cr.SetConditions(xpv1.Available())
    return managed.ExternalObservation{
        ResourceExists:   true,
        ResourceUpToDate: upToDate,
    }, nil
}
```

### Example 2: The Controller's `Create`/`Update`, and Mapping `Result`

```go
// Create and Update share one body: Pipeline.Apply is idempotent by construction
// (Key Concept: Overwrite Apply), so there is nothing for either method to do
// differently.
func (e *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
    return managed.ExternalCreation{}, e.apply(ctx, mg)
}

func (e *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
    return managed.ExternalUpdate{}, e.apply(ctx, mg)
}

func (e *external) apply(ctx context.Context, mg resource.Managed) error {
    cr := mg.(*v1alpha1.SnowflakeAccount)
    log := logger.New(e.logger, cr.Namespace, "SnowflakeAccount", cr.Name, logger.OpUpdate)

    region, err := e.backplane.Region(cr.Spec.Region)
    if err != nil {
        return log.Handle(err)
    }

    mc := account.NewModuleContext(cr, cr.Namespace, region, e.namespaceLabels(cr.Namespace), log)

    result, err := e.pipeline.Apply(ctx, mc)
    if err != nil {
        return log.Handle(err)
    }

    // Render each module's own condition; a module absent from result.Outcomes
    // (because the run aborted before reaching it) leaves its condition untouched.
    ready := true
    for _, mo := range result.Outcomes {
        if mo.Outcome.Err != nil {
            log.Handle(mo.Outcome.Err) // incident-tracked; the condition already carries the user-facing message
        }
        if mo.Outcome.Condition == nil {
            continue
        }
        cr.SetConditions(*mo.Outcome.Condition)
        if gatesReady := account.GatesReady[mo.Outcome.Condition.Type]; gatesReady &&
            mo.Outcome.Condition.Status != xpv1.ConditionTrue {
            ready = false
        }
    }

    if result.AllDone() {
        cr.Status.SetObservedGeneration(cr.Generation) // only an all-Done run advances the gate
    }
    if ready {
        cr.SetConditions(xpv1.Available())
    } else {
        cr.SetConditions(xpv1.Unavailable())
    }

    return nil
}
```

### Example 3: Implementing `Module`

```go
package parameter

// Module applies the account's global and regional Snowflake parameters
// (design.md 3.5, 3.6).
type Module struct {
    backplane *backplane.Config
}

func New(bp *backplane.Config) *Module {
    return &Module{backplane: bp}
}

func (m *Module) Name() string { return "parameter" }

// Observe never reads parameters back — drift detection is deferred (Key
// Concept: Overwrite Apply) — so this module is always reported in sync.
func (m *Module) Observe(ctx context.Context, mc *account.ModuleContext) (bool, account.Outcome) {
    return true, account.Done()
}

// Apply re-asserts every global and regional parameter unconditionally: no
// SHOW PARAMETERS, no diff against current state.
func (m *Module) Apply(ctx context.Context, mc *account.ModuleContext) account.Outcome {
    db, err := mc.TenantDB(ctx)
    if err != nil {
        return account.Failed(fmt.Errorf("getting platform connection: %w", err))
    }

    params := m.backplane.GlobalParameters
    for name, value := range mc.BackplaneRegion().RegionalParameters {
        params[name] = value
    }
    for name, value := range params {
        if _, err := db.ExecContext(ctx, fmt.Sprintf("ALTER ACCOUNT SET %s = %s", name, value)); err != nil {
            return account.Failed(fmt.Errorf("setting %s: %w", name, err))
        }
    }

    // Contrast: the account module (010) calls outcome.Aborting() on any
    // outcome that is not Done — Pending and Rejected included, not just
    // Failed — because no later module can do anything useful without a live
    // account. This module never does that: a failed parameter must not block
    // the network, auth, identity, or quota modules from still running.
    return account.Done()
}
```
