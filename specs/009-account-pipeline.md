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
- Three entry points: a read-only `Observe` that mutates nothing in Snowflake, a mutating `Apply` that
  re-asserts every module's desired state, and a `Destroy` that tears the account down in reverse.
- A shared, per-reconcile context that carries what every module needs — the CRD, the resolved
  region, the namespace's labels, a scoped logger, and a lazily-resolved account connection — so no
  module recomputes or disagrees with another about any of it.
- A fixed outcome vocabulary (`Done`, `Pending`, `Rejected`, `Failed`) that every module reports
  through, plus a generic signal any module's outcome can carry to stop the run early.
- Collecting what ran into one `Result`, and a static table deciding which of a module's own
  conditions forces the resource's aggregate `Ready` to `False`.

**Out of Scope**:
- Executing any SQL itself. Every statement belongs to a module (012–015, 017, 018); this package only
  sequences calls into them. guardrail-check (010) and quota-check (011) execute no SQL at all — neither
  ever opens a Snowflake connection, which is what lets them run ahead of the account module.
- Classifying any error as a user or system error. Each module is the only code that knows why its
  own call failed, so each module classifies its own failures before reporting an `Outcome`.
- Detecting or repairing drift. No module reads Snowflake state back to compare and repair it;
  Organization Policies will make that state org-owned, so the work would not survive to be used. The
  one read-back sanctioned here is a pruning module's enumeration of objects the CRD no longer lists —
  it drops, it never repairs (see Key Concept below).
- Authorizing a destruction. Whether an account may be destroyed at all is decided by the deletion
  request's own two-key gate (019), and the finalizer and conditions around it belong to 020. This
  package only sequences the teardown, once asked.
- Adding any field to the `SnowflakeAccount` CRD's schema. A module may still add its own named
  `status` field where it genuinely needs to remember something across reconciles (017's sync start
  timestamp is the known case) — that is the module's own spec to state, not this package's.

## Key Concept: Sequential Modules, One Abort Signal

Modules run strictly one at a time, in registration order — never in parallel, never calling one
another. One module plays a distinguished role: the account module (012). It alone determines whether
the account exists at all, and every module that needs a live connection to it can only run once the
account module has succeeded. That dependency is about *capability*, not *position* — a module needing
no Snowflake connection of its own can still be registered ahead of the account module. Guardrail-check
(010) and quota-check (011) are the concrete cases: each can reject and stop the whole run before the
account is ever created.

A module's outcome can carry a signal that stops the pipeline for the rest of that pass. The mechanism
belongs to no module in particular — any module can use it. It exists for the rare module whose own
failure makes the rest of the run pointless: either because later modules depend on something only this
one establishes (the account module's case), or because this module's whole job is deciding whether the
run should happen at all (quota-check's case).

**Important**: every other module's failure must never stop the pipeline — a rejected network rule, a
failed auth exception, a pending identity sync all let later modules keep running. That's what makes
design's "leaves the account on its baseline" guarantee (§3.8/§3.9) hold.

Every module's `Observe` call is likewise recorded in full, not just its `inSync` bool:
`Observation.Outcomes` carries each module's `Outcome` in full (Key Concept: Conditions and Events), so a
module that owns a condition (quota-monitor's `QuotaAvailable`, identity's `IdentitySynced`) can be
re-rendered on every `Observe`, not only after an `Apply`. This is required because the managed
reconciler re-derives `Ready` after every `Observe` on the up-to-date path, never just after `Apply` (see
References, "Vendored behavior").

## Key Concept: Conditions and Events

A module's `Outcome` can carry two optional signals for the controller, independent of `State` and of
each other: a `Condition` it owns and wants reflected in the resource's status, and an `Event` it wants
recorded as a one-off note. Both are values, not live calls — the pipeline forwards them untouched;
turning either into `status.conditions` or an actual Event is the controller's job, not this package's.
A module may set neither, either, or both.

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
dead code. Only 014 and 015 prune, each naming its own prefix; baseline rules, account parameters and
identity bindings are untouched.

## Key Concept: Reverse-Order Teardown

Destroying an account walks the same module list backwards. Each module removes only the state that
does not die with the account itself — most remove nothing at all, because dropping the account takes
every object inside it along. The account's own drop therefore comes last, and the first error stops the
run: nothing further is destroyed while an earlier step is unresolved. Every teardown must be safe to
re-run, since a destruction interrupted anywhere is retried from the beginning.

**Important**: a teardown reaches for the org-admin connection or for no connection at all. Objects
inside the tenant's own account never need removing — they go with it.

A successful `Destroy` means every teardown reported success, not that the external state is gone: a drop
may only start a grace period, during which the account and its platform credential stay restorable
(012, 003). What it guarantees is ordering and idempotence.

## Public API

```go
package pipeline

// Module is implemented by each pipeline stage (010, 011, 012, 013, 014, 015, 017, 018).
type Module interface {
    Name() string

    // Observe is read-back only; it must mutate nothing in Snowflake. Its
    // Outcome is collected into Observation.Outcomes by Pipeline.Observe like
    // any other module's; Outcome.Abort has no effect here — Observe never
    // stops early, unlike Apply — since nothing here mutates and there is
    // therefore nothing an abort would protect against.
    Observe(ctx context.Context, mc *ModuleContext) (inSync bool, outcome Outcome)

    // Apply re-asserts this module's full desired state, pruning any object the
    // CRD no longer lists. It must be safe to call repeatedly with no other call
    // in between (Key Concept: Overwrite Apply).
    Apply(ctx context.Context, mc *ModuleContext) Outcome

    // Teardown removes the state this module leaves outside the tenant's own
    // account, which dropping that account would not take with it. Most
    // modules have none and return nil. It uses OrgAdminDB or no connection at
    // all — never TenantDB — and must be safe to call repeatedly (Key Concept:
    // Reverse-Order Teardown).
    //
    // A nil error means the removal was accepted, not necessarily that the
    // object is gone: a vendor may keep it restorable, and its name reserved,
    // for a grace period. Nothing here reports such a deadline, deliberately.
    Teardown(ctx context.Context, mc *ModuleContext) error
}

// AccountModuleName is the account module's (012) Name(). Pipeline.Observe
// uses it to find which module's Observe result is Observation.Exists,
// regardless of that module's position in the registered list.
const AccountModuleName = "account"

// Pipeline runs an ordered list of modules against one ModuleContext per call.
type Pipeline struct{ /* unexported */ }

// New builds a pipeline from an ordered module list. Registration order is
// execution order for Observe and Apply, and its reverse for Destroy. Exactly one module must be the
// account module, identified by Name() == AccountModuleName: its Observe
// result is the sole source of Observation.Exists, and every module that
// calls ModuleContext.TenantDB must be registered after it, since TenantDB
// requires the locator only its Apply sets. The account module need not be
// registered first overall — a module needing no Snowflake connection (for
// example, an admission gate like guardrail-check or quota-check that must
// abort before the account is ever created) may run earlier.
func New(modules ...Module) *Pipeline

// Observe calls every module's Observe in order and aggregates the result. It
// performs no mutation of its own. Every module's Outcome is recorded in
// Observation.Outcomes regardless of its content — an Outcome.Abort returned
// here is ignored; only Apply honors Abort.
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

// Destroy calls every module's Teardown in reverse registration order, so
// every module registered after the account module tears down before the
// account itself is dropped (Key Concept: Reverse-Order Teardown).
//
// A nil return means every teardown was accepted. It does not mean the
// external state is gone: the account and its credential may both still be
// inside their restore windows (Key Concept: Reverse-Order Teardown).
//
// Returns:
//   - error: the first Teardown error, returned unchanged and already
//     classified by the module that produced it. No later Teardown runs.
func (p *Pipeline) Destroy(ctx context.Context, mc *ModuleContext) error

// Observation is Pipeline.Observe's result.
type Observation struct {
    Exists   bool            // from the account module's Observe alone (Name() == AccountModuleName); no other module contributes to it
    InSync   bool            // true iff every module's Observe reported inSync == true
    Outcomes []ModuleOutcome // one entry per registered module, in registration order — always all of them, since Observe never stops early
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
    Condition *xpv1.Condition // optional (Key Concept: Conditions and Events)
    Event     *event.Event    // optional (Key Concept: Conditions and Events)
}

func Done() Outcome                // StateDone
func Pending(reason string) Outcome // StatePending
func Rejected(err error) Outcome    // StateRejected; err built with errors.NewUserError
func Failed(err error) Outcome      // StateFailed; err wrapped with fmt.Errorf

// Aborting returns o with Abort set true; every other field is unchanged. No
// module is privileged to call this — today the account module (012),
// quota-check (011), and guardrail-check (010) all do, each on any outcome
// that is not Done.
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

// DBPool is the subset of internal/snowflake/pool (004) that ModuleContext
// depends on, declared here so a test can inject a fake. *pool.Pool satisfies
// it implicitly.
type DBPool interface {
    OrgAdmin(ctx context.Context) (*sql.DB, error)
    TenantAccount(ctx context.Context, namespace, accountName, locator, region string) (*sql.DB, error)
    EvictTenant(namespace, accountName string)
}

// ModuleContext is built once per reconcile and handed unchanged to every
// module. Everything on it is either immutable for the run or, in the case of
// the account locator, mutated by exactly one module (012).
type ModuleContext struct{ /* unexported */ }

// NewModuleContext builds the shared context for one reconcile.
//
// namespace is the trust anchor (design.md 3.11.1) the resolved account name
// is derived from — callers pass the bare namespace, not a pre-resolved name,
// so ResolvedAccountName() is computed once, here, and no two callers can
// disagree about it. namespaceLabels are the raw namespace labels set at
// onboarding (design.md 2); Department/CostCenter/CreditQuota are read from
// them the same way (internal/account/tenant, 006). The account locator
// lives on cr.Status.AccountLocator directly — every module reads and
// writes it through CR(), not through a ModuleContext accessor.
func NewModuleContext(
    cr *v1alpha1.SnowflakeAccount,
    namespace string,
    backplaneRegion *backplane.Region,
    namespaceLabels map[string]string,
    log *logger.Logger,
    p DBPool,
) *ModuleContext

func (c *ModuleContext) CR() *v1alpha1.SnowflakeAccount
func (c *ModuleContext) ResolvedAccountName() string // tenant.ResolveName(cr.Name, namespace), resolved once
func (c *ModuleContext) BackplaneRegion() *backplane.Region
func (c *ModuleContext) NamespaceLabels() map[string]string
func (c *ModuleContext) Logger() *logger.Logger

// OrgAdminDB returns an org-admin-scoped connection (internal/snowflake/pool, 004).
// Only the account module (012) needs this scope.
func (c *ModuleContext) OrgAdminDB(ctx context.Context) (*sql.DB, error)

// TenantDB returns a connection scoped to this tenant's own account,
// resolved on first call and memoized for the rest of the run.
//
// Returns:
//   - System error if CR().Status.AccountLocator is still empty — every
//     module after 012 needs a locator, and getting one is the whole point
//     of running 012 first.
func (c *ModuleContext) TenantDB(ctx context.Context) (*sql.DB, error)

// EvictTenant closes and forgets the pooled connection to this tenant's own
// account, keyed exactly as TenantDB resolves it. The account module (012)
// calls it once the account is dropped.
func (c *ModuleContext) EvictTenant()

// Custom condition types this package defines, plus the static table deciding
// which of them forces the resource's aggregate Ready to False. A module
// attaches its own condition to its Outcome (above); 020 collects and renders
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
internal/account/pipeline/
├── module.go       # Module interface, Outcome (Condition/Event), State, Done/Pending/Rejected/Failed, Aborting
├── pipeline.go     # Pipeline, New, Observe, Apply, Destroy, Observation, Result, ModuleOutcome, AllDone
├── context.go      # ModuleContext, NewModuleContext, DBPool, OrgAdminDB/TenantDB/EvictTenant
└── conditions.go   # TypeQuotaAvailable, TypeIdentitySynced, GatesReady
```

## Error Classification

**User Errors**: this package produces none of its own. Each module classifies its own user errors
with `errors.NewUserError` before wrapping the result in `Rejected(err)` — a rejected network-rule
entry (§3.8) or a rejected auth exception (§3.9) are both the module's own classification, never this
package's.

**System Errors**: likewise none of this package's own, with one exception. Every module wraps its
own system failures with `fmt.Errorf("...: %w", err)` before returning `Failed(err)`. The one system
error this package itself can produce is `ModuleContext.TenantDB`'s error when
`CR().Status.AccountLocator` is still empty — every other failure surfacing from `OrgAdminDB`/`TenantDB`
is `internal/snowflake/pool`'s (004) own error, passed through unwrapped for the calling module to
classify. `Destroy` likewise returns a module's `Teardown` error exactly as that module built it.

## Edge Cases

- **What does the reconciler do if it calls `Observe` without ever calling `Apply` afterward?** -
  Nothing breaks. `Observe` and `Apply` share no state (`ModuleContext` is rebuilt per call), and
  `Observe` performs no mutation, so the up-to-date path never touches Snowflake.
- **Does an `Outcome.Abort == true` returned from a module's `Observe` stop later modules from
  running?** - No. `Abort` only has meaning for `Apply` (Key Concept: Sequential Modules, One Abort
  Signal); `Observe` always runs every registered module and records every `Outcome` in
  `Observation.Outcomes`, regardless of any module's `Abort` field.
- **`Apply` aborts after the first of six modules — what does `Result` say about the other five?** -
  They are absent from `Result.Outcomes` entirely, not recorded with any placeholder state. A
  condition owned by an absent module is left exactly as the previous reconcile set it.
- **A module's `Rejected` condition was already surfaced on `Ready`; the tenant fixes the CRD and the
  next run succeeds — does the stale condition linger?** - No. Every module that ran on this pass
  returns a fresh `Outcome`, including a fresh `Condition`; the previous rejection is overwritten the
  moment that module reports `Done` instead.
- **What happens on the very first reconcile, before `CREATE ACCOUNT` has ever returned a locator?** -
  `cr.Status.AccountLocator` is `""`. Only the account module (012) can proceed without one; every
  module that calls `TenantDB` fails with a system error until 012 has set `cr.Status.AccountLocator`
  directly, which is why 012 must run before any such module, and must abort on anything but `Done`. A
  module that never calls `TenantDB` (guardrail-check, 010, or quota-check, 011) has no such constraint
  and may be registered ahead of 012.
- **A module returns `Pending` — who decides when the pipeline is retried?** - Nobody, at this layer.
  `Pending` carries only its reason string, no requeue hint; the controller's own poll interval governs
  when the next reconcile happens.
- **A tenant leaves one module permanently `Rejected` — does the pipeline keep re-running forever?** -
  Yes, by design: `observedGeneration` never advances past a run with any non-`Done` outcome (Key
  Concept: Overwrite Apply), so every poll re-applies every module until the tenant corrects the CRD.
  Each re-apply is a handful of idempotent statements plus one enumeration query per pruning module,
  so this is accepted as cheap-but-unbounded rather than solved here.
- **A resource is deleted before its account was ever created — what does `Destroy` do?** - Every
  teardown finds nothing to remove and returns nil, so the run succeeds and the caller can release its
  finalizer. A resource an admission gate refused is still deletable.
- **`Destroy` fails halfway — is what already ran compensated for?** - No. The error stops the run and
  the next attempt walks the whole list again from the end; every teardown is safe to re-run, so the
  steps that already completed simply report success a second time. A half-failed run can leave a genuinely
  mixed state — the account dropped but still restorable while its credential is already inside its own
  recovery window, or the reverse — and that is fine: both clocks were started by the same configured grace
  period and neither outlives it, so the state converges without intervention. Re-running is still what
  clears the *retryable* part of it.
- **Does a successful `Destroy` mean the caller can safely re-create the same resource immediately?** - No,
  and nothing here promises it. The resolved account name stays reserved for the account's grace period
  (012), so a re-create inside that window collides on the name. `Destroy`'s contract is ordering and
  idempotence, not erasure.

## Dependencies

- **`internal/errors` (001)** - Used APIs: `errors.NewUserError()` - Contract: each module calls this
  itself before returning `Rejected`; this package never calls it.
- **`internal/logger` (001)** - Used APIs: `logger.New()`, `(*Logger).Handle()` - Contract:
  `ModuleContext` carries a `*Logger` for modules to log through; only the caller that built the
  context calls `Handle` on a carried error, once per error.
- **`internal/snowflake/pool` (004)** - Used APIs: `Pool.OrgAdmin()`, `Pool.TenantAccount()`,
  `Pool.EvictTenant()` - Contract: `ModuleContext.OrgAdminDB`/`TenantDB`/`EvictTenant` wrap these;
  `TenantDB` additionally requires a locator.
- **`internal/account/tenant` (006)** - Used APIs: `tenant.ResolveName()`, `tenant.Department()`,
  `tenant.CostCenter()`, `tenant.CreditQuota()` - Contract: `NewModuleContext` resolves the account
  name once via `ResolveName`; modules read the label accessors from `NamespaceLabels()` themselves.
- **`internal/config/backplane` (007)** - Used APIs: the `Region` type - Contract: the caller resolves the
  region once and passes it into `NewModuleContext`; this package never looks a region up itself.
- **`crossplane-runtime/v2` `pkg/event`** - Used APIs: the `event.Event` type - Contract: `Outcome.Event`
  only carries a value of this type (Key Concept: Conditions and Events); this package never constructs
  one itself and never calls a `Recorder`.

No dependency on 008 (guardrails): guardrail admission is resolved by its own pipeline module,
guardrail-check (010), built on top of 008's evaluator — the same one-way relationship quota-check
(011) already has with this package. This package itself still neither imports nor references 008
directly.

## Integration Points

- **`internal/controller/snowflakeaccount` (020)** - Calls `Pipeline.Observe`
  from the controller's own `Observe`, and `Pipeline.Apply` from both `Create` and `Update` with
  identical bodies — no separate guardrail gate runs before either call. Registers modules in the
  fixed order 010 → 011 → 012 → 013 → 014 → 015 → 017 → 018 — guardrail-check (010) first, quota-check
  (011) second, both ahead of the account module, since neither needs a Snowflake connection and both
  must abort before `CREATE ACCOUNT` when their own check fails. Owns rendering
  `Outcome.Condition` and `Outcome.Event` values, `GatesReady` aggregation, and advancing
  `status.observedGeneration`.
  Calls `Pipeline.Destroy` from `Delete`, after the deletion request's gate (019) has authorized the
  destruction and before that request is marked consumed. - Key functions: `pipeline.New()`,
  `(*Pipeline).Observe`, `(*Pipeline).Apply`, `(*Pipeline).Destroy`, `pipeline.NewModuleContext()`.
- **`internal/account/modules/{guardrailcheck,quotacheck,account,parameter,network,auth,identity,quotamonitor}`
  (010–015, 017–018)** - Each implements `Module` in full and is registered with `pipeline.New()` by
  020; none has any out-of-band entry point outside the `Module` contract. guardrail-check (010) and
  quota-check (011) are the two admission checks, registered ahead of the account module;
  quota-monitor (018) is the resource-monitor enforcement and exhaustion condition, registered after it
  in the position the earlier single-module quota plan used to occupy.

## Success Criteria

1. **SC-001**: `New(modules...)` preserves registration order; `Pipeline.Apply` calls each module's
   `Apply` in that exact order.
2. **SC-002**: `Observation.Exists` reflects only the account module's (`Name() == AccountModuleName`)
   `Observe` result, regardless of its position in the registered list or what later modules report.
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
9. **SC-009**: `ModuleContext.TenantDB` returns a system error when `CR().Status.AccountLocator` is
   empty, and never calls the pool when it is.
10. **SC-010**: `ModuleContext.TenantDB` resolves the connection once and returns the same `*sql.DB`
    on every subsequent call within the same context.
11. **SC-011**: `ModuleContext.ResolvedAccountName()` returns the same value `tenant.ResolveName` would
    compute directly from the same CRD name and namespace.
12. **SC-012**: `Pipeline.Destroy` calls each module's `Teardown` in the exact reverse of registration
    order.
13. **SC-013**: `Destroy` stops at the first `Teardown` error, returns it unchanged, and calls
    `Teardown` on no earlier-registered module.
14. **SC-014**: `ModuleContext.EvictTenant` calls the pool with the same namespace and account name
    `TenantDB` resolves its connection under.
15. **SC-015**: Unit test coverage of `internal/account` is at least 95%.
16. **SC-016**: `Destroy` returns nil once every `Teardown` returned nil, and reports nothing else — no
    restore deadline, no partial-erasure signal — so a successful run cannot be mistaken for the external
    state having been erased.
17. **SC-017**: `Observation.Outcomes` contains exactly one entry per registered module, in
    registration order, matching what each module's `Observe` returned.
18. **SC-018**: An `Outcome.Abort == true` returned from any module's `Observe` has no effect on
    `Pipeline.Observe`'s control flow — every later module still runs and is still recorded in
    `Observation.Outcomes`.
19. **SC-019**: An `Outcome.Event`, when set, survives unchanged through `Observation.Outcomes` and
    `Result.Outcomes`, independent of `State` and of whether `Condition` is also set.

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
- Nothing here decides whether a destruction is allowed: `Destroy` runs whenever it is called. The
  authorization for that call is the deletion request's two-key gate (019), which the controller (020)
  clears before calling.
- `Outcome.Event` is a value, not a live call (Key Concept: Conditions and Events) — no module ever holds
  a `Recorder`, so a bug in a module can misreport an event but can never spam or forge one through the
  Kubernetes API directly.

## References

- **Product design**: `specs/design.md` §3.2 (create flow), §3.6-§3.9 (bootstrapping, identity,
  network and auth rules), §3.10 (credit quota), §3.11 (privilege step-down), §4.3 (`IdentitySynced`),
  §6.3 (the deletion flow this package's `Destroy` is Phase 3 of), §7.1/§7.2 (condition and status
  model).
- **Template**: `specs/000-template.md` — the section skeleton this spec follows.
- **Shape reference**: `specs/007-backplane-config.md` — Public API and Error Classification
  phrasing followed here.
- **Dependency code**: `internal/snowflake/pool/pool.go` (`OrgAdmin`, `TenantAccount`, `EvictTenant`),
  `internal/account/tenant/` (`ResolveName`, `Department`, `CostCenter`, `CreditQuota`),
  `internal/config/backplane/backplane.go` (`Region`), `internal/logger/logger.go` (`New`, `Handle`),
  `apis/base/v1alpha1/snowflakeaccount_types.go` (`SnowflakeAccountStatus`).
- **Vendored behavior**: `crossplane-runtime/v2@v2.0.0` `pkg/reconciler/managed/reconciler.go` — the
  managed reconciler sets `Creating()`/`ReconcileSuccess()` after `Create` returns and after
  `Observe` returns on the up-to-date path, so 020 must re-aggregate `Ready` on every `Observe` rather
  than relying on what a prior `Apply` set. On a deleted resource it calls `Delete` only when the
  preceding `Observe` reported `ResourceExists: true`, and otherwise removes the finalizer straight
  away (`reconciler.go:1163,1173,1230`).

<br/><br/><br/><br/><br/>
================

## Appendix: Usage Examples

The Go examples below illustrate call shape and sequencing, not exact compilable code — the precise
condition-rendering and `Ready` aggregation logic belongs to 020, which is not yet written.

### Example 1: The Controller's `Observe`

```go
func (e *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
    cr := mg.(*v1alpha1.SnowflakeAccount)
    log := logger.New(e.logger, cr.Namespace, "SnowflakeAccount", cr.Name, logger.OpObserve)

    // A deleting resource still has to report its account as existing, or the
    // reconciler releases the finalizer without ever calling Delete. Only an
    // account that was never created reports otherwise.
    if cr.GetDeletionTimestamp() != nil {
        return managed.ExternalObservation{
            ResourceExists:   cr.Status.AccountLocator != "",
            ResourceUpToDate: true,
        }, nil
    }

    region, err := e.backplane.Region(cr.Spec.Region)
    if err != nil {
        retryErr := log.Handle(err)
        cr.SetConditions(xpv1.Unavailable().WithMessage(retryErr.Error()))
        return managed.ExternalObservation{}, nil
    }

    mc := pipeline.NewModuleContext(cr, cr.Namespace, region, e.namespaceLabels(cr.Namespace), log, e.pool)

    obs, err := e.pipeline.Observe(ctx, mc)
    if err != nil {
        retryErr := log.Handle(err)
        cr.SetConditions(xpv1.Unavailable().WithMessage(retryErr.Error()))
        return managed.ExternalObservation{}, nil
    }
    if !obs.Exists {
        return managed.ExternalObservation{ResourceExists: false}, nil
    }

    // Same per-outcome condition render/aggregate as Create/Update (Example 2)
    // — the managed reconciler re-derives Ready after every Observe on the
    // up-to-date path, not only after Apply.
    ready := true
    for _, mo := range obs.Outcomes {
        if mo.Outcome.Event != nil {
            e.record.Event(cr, *mo.Outcome.Event)
        }
        if mo.Outcome.Condition == nil {
            continue
        }
        cr.SetConditions(*mo.Outcome.Condition)
        if gatesReady := pipeline.GatesReady[mo.Outcome.Condition.Type]; gatesReady &&
            mo.Outcome.Condition.Status != xpv1.ConditionTrue {
            ready = false
        }
    }

    upToDate := cr.Status.GetObservedGeneration() == cr.Generation && obs.InSync
    if ready {
        cr.SetConditions(xpv1.Available())
    } else {
        cr.SetConditions(xpv1.Unavailable())
    }
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

    mc := pipeline.NewModuleContext(cr, cr.Namespace, region, e.namespaceLabels(cr.Namespace), log, e.pool)

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
        if mo.Outcome.Event != nil {
            e.record.Event(cr, *mo.Outcome.Event)
        }
        if mo.Outcome.Condition == nil {
            continue
        }
        cr.SetConditions(*mo.Outcome.Condition)
        if gatesReady := pipeline.GatesReady[mo.Outcome.Condition.Type]; gatesReady &&
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

### Example 3: The Controller's `Delete`

```go
func (e *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
    cr := mg.(*v1alpha1.SnowflakeAccount)
    log := logger.New(e.logger, cr.Namespace, "SnowflakeAccount", cr.Name, logger.OpDelete)

    // Phase 2: no active request, no destruction — the finalizer stays and the
    // resource stalls in Terminating.
    req, err := deletion.FindActiveRequest(ctx, e.kube, cr.Namespace, "SnowflakeAccount", cr.Name)
    if err != nil {
        return managed.ExternalDelete{}, log.Handle(err)
    }
    if req == nil {
        e.record.Event(cr, event.Warning("DeletionBlocked", errNoActiveRequest))
        return managed.ExternalDelete{}, errNoActiveRequest
    }

    region, err := e.backplane.Region(cr.Spec.Region)
    if err != nil {
        return managed.ExternalDelete{}, log.Handle(err)
    }

    mc := pipeline.NewModuleContext(cr, cr.Namespace, region, e.namespaceLabels(cr.Namespace), log, e.pool)

    // Phase 3: every module's Teardown, in reverse. A failure here keeps the
    // request Active, so the next reconcile retries the whole walk.
    if err := e.pipeline.Destroy(ctx, mc); err != nil {
        return managed.ExternalDelete{}, log.Handle(err)
    }

    return managed.ExternalDelete{}, deletion.MarkConsumed(ctx, e.kube, req)
}
```

### Example 4: Implementing `Module`

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
func (m *Module) Observe(ctx context.Context, mc *pipeline.ModuleContext) (bool, pipeline.Outcome) {
    return true, pipeline.Done()
}

// Apply re-asserts every global and regional parameter unconditionally: no
// SHOW PARAMETERS, no diff against current state.
func (m *Module) Apply(ctx context.Context, mc *pipeline.ModuleContext) pipeline.Outcome {
    db, err := mc.TenantDB(ctx)
    if err != nil {
        return pipeline.Failed(fmt.Errorf("getting platform connection: %w", err))
    }

    params := m.backplane.GlobalParameters
    for name, value := range mc.BackplaneRegion().RegionalParameters {
        params[name] = value
    }
    for name, value := range params {
        if _, err := db.ExecContext(ctx, fmt.Sprintf("ALTER ACCOUNT SET %s = %s", name, value)); err != nil {
            return pipeline.Failed(fmt.Errorf("setting %s: %w", name, err))
        }
    }

    // Contrast: the account module (012) calls outcome.Aborting() on any
    // outcome that is not Done — Pending and Rejected included, not just
    // Failed — because no later module can do anything useful without a live
    // pipeline. This module never does that: a failed parameter must not block
    // the network, auth, identity, or quota modules from still running.
    return pipeline.Done()
}

// Teardown removes nothing: account parameters live inside the account and go
// with it when it is dropped.
func (m *Module) Teardown(ctx context.Context, mc *pipeline.ModuleContext) error {
    return nil
}
```
