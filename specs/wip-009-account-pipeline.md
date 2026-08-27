> **Clarification record — not a specification.** Produced by `/yukimi.clarify 009` to settle what
> `specs/design.md` intentionally leaves out and `specs/scope-009-account-pipeline.md` does not cover.
> It records decisions, not product design — `specs/design.md` remains authoritative and always wins.
> Read it together with the scope note when writing `009-account-pipeline.md`, then delete both.

## Clarification runs

- Run 1 — covered: lifecycle completeness (observe/drift/update/teardown), mapping onto the
  Crossplane controller contract, ownership boundaries, idempotency and partial failure, status and
  condition aggregation, error classification, concurrency, forward contracts, and the scope note's
  five own open questions. Left open: P-001 … P-005, O-001, O-002.
- Follow-up (outside `/yukimi.clarify`) — D-003, D-006 and D-008 were amended at the user's request:
  the pipeline constructor no longer splits out a distinguished "structural module" argument; the
  ability to abort the run moved onto `Outcome` itself as a generic field any module may set, though in
  practice only 010 uses it, unchanged from before. The original rationale for rejecting this shape
  (recorded in D-003) is left in place and marked superseded, rather than deleted, so the tradeoff
  stays visible.

## Resolved Decisions

### D-001 — Two entry points on the pipeline, no threading between them

**Question**: How does one pipeline serve `Observe`, `Create` and `Update`, given that 018's three
controller methods have different jobs but only one set of modules exists?

**Decision**: Two public entry points and no state carried between them:

```go
func (p *Pipeline) Observe(ctx context.Context, mc *ModuleContext) (Observation, error)
func (p *Pipeline) Apply(ctx context.Context, mc *ModuleContext) (Result, error)
```

`Observe` is read-back only and mutates nothing in Snowflake. `Apply` runs every registered module
unconditionally. 018 calls `Observe` from `Observe`, and `Apply` from **both** `Create` and `Update` —
the two controller methods have identical bodies, because `Apply` is idempotent by construction
(D-011). Nothing from an `Observe` call is threaded into the following `Apply`; `Apply` re-derives
whatever it needs.

**Rationale**: The rejected alternative was a single `Reconcile(ctx, mc)` that observes and repairs in
one pass. It reads more cleanly but cannot satisfy Crossplane's contract: `Observe` must be free of
side effects so that `ResourceExists: false` can release a finalizer without provisioning anything,
and `managed.ExternalObservation` has to be answerable before any mutation happens. Threading state
was rejected separately: the reconciler may call `Observe` without a following `Apply` (the up-to-date
path) and Crossplane never guarantees the two run against the same in-memory object, so any carried
state would be a correctness trap for the sake of saving one cheap lookup.

**Affects spec section**: Public API; Key Concepts.

### D-002 — The module contract keeps both halves, with `Observe` inert today

**Question**: The scope note asks whether every module needs an `Observe` at all, "or a cheap
idempotent `Apply` makes it optional".

**Decision**: Both halves stay mandatory on every module:

```go
type Module interface {
    Name() string
    Observe(ctx context.Context, mc *ModuleContext) (inSync bool, outcome Outcome)
    Apply(ctx context.Context, mc *ModuleContext) Outcome
}
```

Modules 011–016 implement `Observe` as `return true, Done()` today — they perform no read-back and
never report drift (D-010). The method exists so that real drift detection can be filled in per module
without changing the interface, the registration in 018, or the pipeline's aggregation logic.

**Rationale**: Making `Observe` optional (a separate `Observer` interface the pipeline
type-asserts for) was rejected: the assertion is invisible at registration time, so a module that
silently fails to implement it looks identical to one that is genuinely always in sync. Dropping
`Observe` entirely was rejected because the whole interface would have to be reopened — and every
module spec re-touched — once Organization Policies make read-back worthwhile. An inert method with a
one-line body is the cheaper placeholder.

**Affects spec section**: Public API; Key Concepts.

### D-003 — An ordered module list; the account module runs first, abort is not tied to position

**Question**: The scope note says a failure of 010 is the one case that stops the run. How does the
pipeline know which module is 010 without importing it?

**Decision**: A single ordered constructor, no distinguished slot:

```go
func New(modules ...Module) *Pipeline
```

`modules[0]` must be the account module (010) — 018's registration order enforces this, the same way
it already enforces 011→012→013→015→016 after it (D-004). `Observation.Exists` is read from
`modules[0]`'s `Observe` result alone; no other module contributes to it. The pipeline never learns
*which* module this is beyond its position in the slice, so `internal/account` still imports no
implementor (the scope note's hard rule holds). Whether a module's outcome stops the run is no longer
tied to being first at all — see the revised D-006.

**Rationale**: The original decision split the constructor into `New(structural Module, rest
...Module)` specifically to make "only the first module can abort" unrepresentable otherwise, and at
the time explicitly rejected "a per-outcome abort flag any module could set" for turning "one
architectural fact into a per-call decision each module spec would have to restate." **This is
revised here, at the user's explicit request**, on code-shape grounds: the split-argument constructor
forces the pipeline's `Apply` loop to special-case argument position instead of just consulting each
outcome, and forces every module spec to explain why it either is or isn't "the structural one" rather
than simply stating what its own outcome does. Moving the abort signal onto `Outcome` itself (D-006)
removes the need for the special argument: `modules[0]` still matters for `Observation.Exists` and for
the dependency order every later module already needs (010 must run before anything else can get a
connection, D-013) — but that is now just "first element of an ordered slice," identical in kind to why
011 must run before 012 (D-004), not a second, differently-shaped constructor parameter.

**Affects spec section**: Public API; Key Concepts.

### D-004 — Modules run sequentially in registration order

**Question**: Is there any concurrency inside one pipeline run, and does each module get its own
deadline?

**Decision**: Strictly sequential, in the order 018 registered them. No goroutines, no per-module
timeout — the reconciler's `ctx` is the only deadline, and a module that respects `ctx` is the only
cancellation mechanism. Modules never call one another and hold no state between reconciles.

**Rationale**: Parallelism buys little here (the modules are a handful of round-trips each) and costs
a lot: 011's parameters and 012's network policy both mutate account-level state, and 015's outcome
depends on the account existing. Per-module timeouts were rejected as a knob with no requirement
behind it; a slow module surfaces as a reconcile timeout, which is already visible.

**Affects spec section**: Key Concepts; Edge Cases.

### D-005 — `Outcome` is the only channel; module methods return no `error`

**Question**: The scope note leaves the outcome type's exact shape open. Do modules return
`(Outcome, error)`, or does `Outcome` carry failures itself?

**Decision**: `Outcome` alone. Modules return no `error`, so there is exactly one way to report
anything:

```go
type State int

const (
    StateDone State = iota
    StatePending
    StateRejected
    StateFailed
)

type Outcome struct {
    State     State
    Reason    string           // Pending only: the operator-visible reason
    Err       error            // Rejected/Failed only
    Condition *xpv1.Condition  // optional; see D-016
}

func Done() Outcome
func Pending(reason string) Outcome
func Rejected(err error) Outcome   // err from errors.NewUserError
func Failed(err error) Outcome     // wrapped system error
```

Each module classifies its own failures — it is the only code that knows whether a rejection is the
tenant's CRD or Snowflake being unreachable. The pipeline classifies nothing and calls
`logger.Handle` on nothing; 018 does that per carried error.

**Rationale**: `(Outcome, error)` gives two representations of one fact and immediately raises "what
does `(Rejected, non-nil err)` mean?" — a question with no good answer that every module spec would
have to dodge. Folding the error into the outcome makes the four states exhaustive, which is what
§3.8/§3.9 need: a rejected rule must be reportable *without* being a pipeline failure.

**Affects spec section**: Public API; Error Classification.

### D-006 — Abort is a property of the outcome, not of module position

**Question**: Does only `Failed` from 010 abort, or does any non-`Done` outcome? And, given D-003
drops the distinguished structural slot, what decides whether the pipeline stops running the
remaining modules on this `Apply`?

**Decision**: `Outcome` carries the signal directly, and any module may set it on its own returned
outcome:

```go
type Outcome struct {
    State     State
    Reason    string
    Err       error
    Abort     bool             // if true, Apply stops; later modules do not run this pass
    Condition *xpv1.Condition
}

func (o Outcome) Aborting() Outcome // returns o with Abort set true, otherwise unchanged
```

The pipeline's `Apply` loop checks `outcome.Abort` generically after every module call, regardless of
which module produced it:

```go
for _, m := range p.modules {
    outcome := m.Apply(ctx, mc)
    result.Outcomes = append(result.Outcomes, ModuleOutcome{Module: m.Name(), Outcome: outcome})
    if outcome.Abort {
        result.Aborted = true
        break
    }
}
```

010 calls `.Aborting()` on every outcome that is not `Done` — `Pending` and `Rejected` included, not
just `Failed`. All three mean there is no usable account: `Failed` means `CREATE ACCOUNT` errored,
`Rejected` means the request was refused, and `Pending` means the account is not yet ready to accept a
`platform`-user connection. In every case each later module would fail on its first statement for a
reason that has nothing to do with that module. 011–013/015/016 never call `.Aborting()` (D-008).

**Rationale**: Restricting the abort to `Failed` would let a `Pending` account produce five spurious
`Failed` outcomes downstream, each with its own incident ID — noise that points at the wrong
subsystem; that reasoning is unchanged from the original decision. What changed is *where* the fact
lives: the mechanism itself is now generic — nothing in the pipeline or the `Module` interface
privileges `modules[0]` — while the actual behavior is identical to before: today, only 010 ever
aborts, because it is the only module whose failure makes every later statement meaningless. A future
module that gained its own "nothing downstream can succeed" case (none exists today) could adopt the
same `.Aborting()` call without any pipeline change.

**Affects spec section**: Key Concepts; Edge Cases; Public API.

### D-007 — Modules that did not run are absent from the result, not recorded as unknown

**Question**: When the run aborts, what does `Result` say about the modules that never executed?

**Decision**: They are absent entirely. `Result` records only modules that actually ran:

```go
type Result struct {
    Aborted  bool
    Outcomes []ModuleOutcome // one entry per module that ran, in execution order
}

type ModuleOutcome struct {
    Module  string
    Outcome Outcome
}

func (r Result) AllDone() bool // true iff Outcomes is non-empty, !Aborted, and every state is Done
```

Absence is meaningful to 018: a condition owned by a module missing from `Outcomes` is left exactly as
the previous reconcile left it, neither blanked nor set to `Unknown`.

**Rationale**: A synthetic `StateSkipped` or `Unknown` entry per unrun module would be written into
conditions and destroy real information — the last known `IdentitySynced=True` would degrade to
`Unknown` merely because `CREATE ACCOUNT` hiccuped on this pass. "We did not look" and "we looked and
it is unknown" are different claims, and only the first is true here. Absence expresses the first
without a state that means it.

**Affects spec section**: Public API; Edge Cases.

### D-008 — Only 010 ever sets `Abort`; every other module's outcome never stops the run

**Question**: Does a `Rejected` or `Failed` outcome from 011–016 stop the modules after it?

**Decision**: No. 011–013/015/016 run on every `Apply`, regardless of what the ones before them
returned — none of them ever calls `.Aborting()` (D-006) on its own outcome.

**Rationale**: Directly required by design §3.8/§3.9 — a rejected `customNetworkRules` entry leaves
the account on its baseline, is reported on `Synced`, and must not prevent the remaining
configuration from being applied. Generalizing that from network rules to every module but 010 avoids
a per-module abort policy nobody can keep straight.

**Affects spec section**: Key Concepts; Edge Cases.

### D-009 — `UpToDate` is generation-based, gated on an all-`Done` run

**Question**: Nothing so far tells the pipeline *when* to re-apply. Without drift detection (D-010),
what makes a tenant's CRD edit — a new `identityIntegration` group, a changed `description`,
a raised `creditQuota` — actually reach Snowflake?

**Decision**: The Kubernetes generation counter, which increments on every spec change:

```go
// 009.Observe
upToDate := exists &&
    cr.Status.ObservedGeneration == cr.Generation &&
    allModulesInSync   // every module's Observe returned inSync == true

// 018, after Apply
if result.AllDone() {
    cr.Status.SetObservedGeneration(cr.Generation)
}
```

`Apply` always runs all modules; the generation check only decides whether `Apply` is called at all.
Consequences: a spec edit re-applies exactly once; an outstanding identity sync (`Pending`) keeps
`observedGeneration` behind and so keeps re-applying until the sync lands, which is what §4.3's
retry-until-timeout needs; a `Rejected` entry likewise keeps re-applying and keeps reporting until the
tenant corrects the CRD, which is what §3.8/§3.9 need.

**Rationale**: A per-module fingerprint or a whole-spec hash in `status` was rejected — it invents a
mechanism Kubernetes already provides, and the hash would have to be versioned the moment a module's
rendering changed. Verified: `xpv1.ResourceStatus` embeds `ObservedStatus`, so
`status.observedGeneration` and `SetObservedGeneration`/`GetObservedGeneration` already exist on
`SnowflakeAccount` (`apis/base/v1alpha1/snowflakeaccount_types.go`), and the managed reconciler in
`crossplane-runtime/v2@v2.0.0` never calls them — it stamps generation onto conditions via
`conditions.ObservedGenerationPropagationManager` and leaves the field itself alone. So the field is
free for 018 to own, no CRD schema change is needed, and no new invention enters the tree.

**Affects spec section**: Key Concepts; Public API.

### D-010 — Drift is neither detected nor repaired

**Question**: The scope note calls `Observe` semantics the most under-specified area of design.md —
what should each module read back, and what should it repair?

**Decision**: Nothing, for now. No module performs read-back; no divergence is detected, reported or
repaired. Every `Observe` returns `true, Done()` (D-002).

**Rationale**: Snowflake's Organization Policies (design Appendix B) will make this state org-owned
and tenant-unchangeable, at which point drift becomes structurally impossible and any repair machinery
built now becomes dead code. Until they ship, the platform deliberately does not fight an account
admin who changes what it applied. Appendix B N1/A1/C1/S1/X1 each describe exactly such a case. The
alternative — read back and repair now — would mean writing per-module drift checks against the
current API surface, then deleting them; and a repair loop against a determined account admin is a
fight the platform loses noisily either way. Note this supersedes an earlier position in this same
clarification session ("repair 011's parameters, report the rest"): the reporting half is dropped too,
because `Synced` cannot carry a drift message (Verified: when `ResourceUpToDate` is true the
reconciler marks `xpv1.ReconcileSuccess()` *after* `Observe` returns,
`crossplane-runtime/v2@v2.0.0 pkg/reconciler/managed/reconciler.go:1428`, clobbering any
`Synced=False` the controller set).

**Affects spec section**: Key Concepts; Out of Scope; Edge Cases.

### D-011 — `Apply` overwrites; it does not diff, and it does not prune

**Question**: What does "idempotent re-assertion" mean concretely for each module, now that there is
no read-back to diff against?

**Decision**: Blunt overwrite. Each module re-asserts its full desired state on every `Apply` —
`CREATE OR REPLACE` for rules and policies, `SET` for parameters, re-binding where a binding exists —
without reading current state first. Only three fields need real update handling today: `description`,
`creditQuota` and `identityIntegration`. Everything else (notably `customNetworkRules` and
`customAuthRules`) is simply overwritten. **Nothing is pruned**: objects created for a CRD entry the
tenant has since removed are left in place, still bound.

**Rationale**: Overwrite is trivially idempotent and crash-safe — a run interrupted halfway leaves a
partially-applied account that the next `Apply` re-asserts in full, with no compensating logic and no
resume point to track. That property is what makes D-001's "`Create` and `Update` are the same call"
true. Pruning is deliberately deferred rather than forgotten; see P-002 for what it costs.

**Affects spec section**: Key Concepts; Edge Cases; Security Considerations.

### D-012 — Modules may declare their own `status` fields

**Question**: The scope note states "the `status` **schema** stays 006's; 009 adds no CRD field". Does
that bind the modules too?

**Decision**: It binds 009 — the pipeline itself adds no field — but not the modules. A module spec may
add explicit, named fields to `SnowflakeAccountStatus` where it needs to remember something across
reconciles (015's `identitySyncStartedAt` is the known case). Per-module *outcomes* are still returned
to the caller and never written to the resource by the pipeline; 018 renders them.

**Rationale**: The prohibition exists to stop the pipeline from inventing a parallel status surface,
not to stop a module from recording a fact it genuinely needs. 015 cannot compute §4.3's sync timeout
without a start timestamp, and there is nowhere else to put it. Making each such field explicit and
owned by one module spec keeps the schema reviewable, unlike a generic per-module state blob.

**Affects spec section**: Out of Scope; Integration Points.

### D-013 — Late-bound account locator on the shared context

**Question**: `pool.TenantAccount(ctx, namespace, accountName, locator, region)` needs the account
locator, but the locator is opaque and only exists once `CREATE ACCOUNT` has returned it. How does any
module after 010 get a connection on the very first reconcile?

**Decision**: The context owns the locator and hands out connections lazily:

```go
func (c *ModuleContext) Locator() string
func (c *ModuleContext) SetLocator(locator string)              // 010 only
func (c *ModuleContext) OrgAdminDB(ctx context.Context) (*sql.DB, error)
func (c *ModuleContext) PlatformDB(ctx context.Context) (*sql.DB, error)  // needs Locator()
```

018 seeds the locator from `status.accountLocator` when it is already set. 010 calls
`SetLocator` immediately after `CREATE ACCOUNT` returns. `PlatformDB` resolves on first call and
memoizes for the rest of the run, and returns a system error if the locator is still empty. So a
brand-new account is created, gets its locator, and is fully configured within a single `Create`
call — no reconcile is wasted just to learn the locator.

**Rationale**: The alternatives were passing the locator as an explicit parameter through every module
signature (couples every module to a value only one of them produces) or splitting creation across two
reconciles (a guaranteed-incomplete account between them, and §7.1's conditions would have to describe
that intermediate state). Lazy resolution keeps the locator's existence a fact about the context
rather than a phase of the pipeline.

**Affects spec section**: Public API; Key Concepts; Edge Cases.

### D-014 — What the shared context carries, and who builds it

**Question**: The scope note lists the context's contents. What exactly is in it, who assembles it,
and what may a module recompute?

**Decision**: 018's validation phase builds one `*ModuleContext` per reconcile and hands the same value to
every module. It carries:

- the `*SnowflakeAccount` CRD — **spec and status both**, since 015 reads its own timestamp field back
- the resolved account name (`tenant.ResolveName`, 006)
- the region's `*backplane.Region` entry (007), already looked up and already admitted against
  `Available`
- the ops-set namespace labels (006's `labels.go`: `Department`, `CostCenter`, `CreditQuota`)
- the merged guardrail verdict — a placeholder pending 008 (D-015)
- a `*logger.Logger` created by 018 with the operation already scoped

Modules log through the context's logger but never call `Handle` — 018 owns that, once per carried
error (D-005). No module re-runs the guardrail merge or the region lookup: 012 resolving the `"full"`
rule and 013 validating auth exceptions must read the identical verdict, and two modules must never be
able to disagree about one CRD.

**Rationale**: This is the scope note's "resolve once, pass down", made concrete. Status is included
because excluding it would force 015 to re-fetch the resource. Everything in the list is either
immutable for the run or (D-013) mutated by exactly one module.

**Affects spec section**: Public API; Integration Points.

### D-015 — The guardrail verdict is a documented placeholder; 008 is deferred

**Question**: 009 depends on 008, but 008 is not written. What goes in the context slot?

**Decision**: A placeholder. 009 defines the field and states that 008 will own its type; the pipeline
does nothing with it beyond carrying it. 008 is implemented later, out of ascending order.

**Rationale**: An explicit decision by the user to unblock 009 rather than stall on 008. Recording it
as a named placeholder keeps the seam visible in the spec instead of leaving a silent gap. See P-003
for what stays blocked meanwhile.

**Affects spec section**: Public API; Dependencies; Out of Scope.

### D-016 — 009 owns the custom condition constants and a static `Ready`-gating table

**Question**: §4.3 says `IdentitySynced=False` forces `Ready=False`; §3.10 says `QuotaAvailable=False`
leaves `Ready=True`. Where does that non-uniformity live, so that it is stated once rather than
re-derived by each module?

**Decision**: In 009, as a constant pair plus a static table:

```go
const (
    TypeQuotaAvailable xpv1.ConditionType = "QuotaAvailable"
    TypeIdentitySynced xpv1.ConditionType = "IdentitySynced"
)

// Whether a False condition of this type forces Ready=False.
var gatesReady = map[xpv1.ConditionType]bool{
    TypeIdentitySynced: true,  // §4.3 — nobody can administer the account until ACCOUNTADMIN is imported
    TypeQuotaAvailable: false, // §3.10 — the account is intact; warehouses are merely suspended
}
```

A module attaches its condition to its own `Outcome` (D-005's `Condition` field); the pipeline
collects them and applies the table when aggregating `Ready`. Rendering conditions, messages and
events onto the resource stays 018's.

**Rationale**: The table is the one place the design's non-uniformity is written down, with the
section reference next to each entry. Letting each module decide whether it gates `Ready` would spread
one product rule across two module specs that cannot see each other. A `GatesReady() bool` method on
the module was rejected for the same reason: the answer is a property of the condition type, not of
the code that happens to produce it.

**Affects spec section**: Public API; Key Concepts.

### D-017 — `Pending` carries no requeue hint

**Question**: The scope note asks whether `Pending` carries a requeue hint or 018 owns requeue timing.

**Decision**: No hint. `Pending` carries only its reason string. The controller's poll interval governs
when the next reconcile happens.

**Rationale**: The only real `Pending` today is §4.3's identity sync, whose horizon is tens of minutes
— far longer than any poll interval, so a hint would change nothing. Adding a `RequeueAfter` to the
outcome would also mean the pipeline reconciling competing hints from several modules into one, which
is scheduling policy and belongs to 018 if it is ever needed.

**Affects spec section**: Public API; Edge Cases.

### D-018 — 015 owns `IdentitySyncRequest` emission

**Question**: The scope note's open question — §4.3 requires emission on first observation, but 015
covers import only and 018 wires modules rather than adding steps of its own, so no spec claims
emission today.

**Decision**: The identity module (015) owns it. On each `Apply` it emits the request if it is not yet
outstanding, stamps its start time (D-012), then imports whatever groups are already `Ready=True`.
Emission and import live in one module because they are two halves of one concern and share the same
`Pending`/timeout accounting.

**Rationale**: The alternatives were a separate emitter module (registration order in 018 would then
encode a dependency between two modules that must not know about each other) or 018 emitting directly
(makes the controller a step-adder, contradicting the scope note's "018 wires modules"). Accepted
consequence: because 015 runs after 010, nothing is emitted while `CREATE ACCOUNT`
keeps failing — see P-005.

**Affects spec section**: Integration Points; Out of Scope.

### D-019 — The pipeline executes no SQL and classifies no errors

**Question**: What does the pipeline itself actually do, given that modules own their statements and
their own error classification?

**Decision**: It sequences modules, applies the abort rule (D-006), collects outcomes (D-007) and
aggregates conditions (D-016). It executes no SQL, imports `internal/snowflake/statement` (005)
nowhere, classifies no error as user-versus-system, and calls `logger.Handle` on nothing. It has no
teardown half: design §6.3 Phase 3 is a single `DROP ACCOUNT` plus finalizer release owned by 017/018,
and it cascades to every object inside the account.

**Rationale**: §3.11's privilege step-down means there is no single connection an aggregated batch
could run on — 010 needs org-admin, everything else runs as the account's own
`platform` user, and 015/016 do work that is not SQL at all. Per-module teardown would be code no
requirement exercises. All three restatements come from the scope note; they are recorded here because
they are the kind of boundary a spec draft silently erodes.

**Affects spec section**: Scope; Out of Scope; Dependencies.

## Problem Areas

### P-001 — Drift repair is deferred, not solved

**What is uncertain**: Nothing about the mechanism — D-010 settles that no read-back happens. What is
unresolved is the exposure that leaves behind, and for how long.

**Why it is hard**: Everything the platform applies inside an account is, today, changeable by that
account's own admin, and the platform holds no lever that survives their edit. An account admin can
unset the network-policy binding, unset the auth-policy binding, re-key or unlock the `platform` user,
detach the resource monitor, and change any account parameter — and the platform will neither notice
nor repair. Some of these are security controls, not preferences.

**Options and trade-offs**:
- *Ignore until Organization Policies ship* (chosen): zero code now, zero code to delete later. Costs
  an unbounded, unmonitored window in which applied controls can be silently removed.
- *Detect and report only*: makes removal visible without a repair fight. Costs per-module read-back
  code that Organization Policies will obsolete, and `Synced` cannot carry the message anyway
  (Verified, see D-010) — it would need a custom condition invented for the interim.
- *Detect and repair*: closes the window. Costs the same throwaway code plus a repair loop against an
  admin who can simply re-remove it, which converts a silent gap into a noisy one.

**Current lean**: As decided — ignore. Not re-litigated here.

**What would unblock it**: Snowflake shipping Organization Policies (design Appendix B). Until then,
an interim answer would need an ISO/ops judgement on whether the exposure window is acceptable
unmonitored, which is O-shaped rather than a design choice.

### P-002 — Overwrite-without-pruning leaves reachable orphans

**What is uncertain**: What happens to Snowflake objects whose CRD entry the tenant has deleted.

**Why it is hard**: D-011's overwrite handles changed and added entries correctly and *removed* ones
not at all. Removing a `serviceUsers` entry from `customNetworkRules` leaves that user's network rule
and its policy in place, **still bound to the user** — so the user retains ingress the CRD no longer
grants. That is a security-relevant residue, not untidiness: the tenant's own reading of their CRD no
longer describes who can reach the account. The same applies to a removed `customAuthRules` entry and
its auth-policy binding.

**Options and trade-offs**:
- *Leave it* (current): no code. Costs a divergence between CRD and reality that grows monotonically
  and is invisible to the tenant.
- *Prune by naming convention*: 012's `CUSTOM_` prefix makes platform-created objects enumerable, so
  a `SHOW NETWORK RULES` and a set-difference against the CRD would find orphans. Costs the read-back
  D-010 just removed, and any human-created object matching the prefix gets deleted.
- *Track applied entries in status*: exact, no listing needed. Costs a per-module status list that has
  to stay consistent through partial failures — precisely the bookkeeping D-011 was chosen to avoid.

**Current lean**: Prune by naming convention, once something forces the issue — it is the only option
that is correct without new bookkeeping. Not decided.

**What would unblock it**: A decision on whether the tenant-visible CRD must be authoritative about
access, which is an ISO question, not an engineering one. 012 and 013 must each state the gap
explicitly in their own specs regardless.

### P-003 — 008's absence blocks two module specs, not this one

**What is uncertain**: Nothing in 009 — D-015's placeholder is sufficient for the pipeline. What is
blocked is the two consumers.

**Why it is hard**: 012 must resolve the guardrail `"full"` rule and 013 must validate auth
exceptions against the merged verdict. Neither can be written against a placeholder, because the
verdict's *shape* determines their validation code. 009 can be written and implemented; 012 and 013
cannot be finished before 008 exists.

**Options and trade-offs**:
- *Write 008 before 012* (implied by the deferral): restores ascending order at the point it actually
  matters.
- *Have 012/013 define their own minimal verdict view*: unblocks them, at the cost of two definitions
  008 must then reconcile with.

**Current lean**: Write 008 before 012.

**What would unblock it**: Writing 008.

### P-004 — A rejected entry re-runs the whole pipeline every poll interval

**What is uncertain**: Whether the cost of D-009's coarse gate is acceptable in steady state.

**Why it is hard**: `observedGeneration` advances only on an all-`Done` run, so one persistently
`Rejected` module keeps every module re-applying forever — a tenant who leaves a malformed
`customNetworkRules` entry in place produces a full re-apply, on every poll, indefinitely. Each
re-apply is a handful of overwriting statements, so it is cheap per account, but it is unbounded in
time and scales with the number of misconfigured accounts.

**Options and trade-offs**:
- *Accept it* (current): no code. Bounded per reconcile by the poll interval; work is wasted but
  harmless because every statement is idempotent.
- *Per-module generation record*: skips modules already `Done` at this generation. Costs a status map
  keyed by module name — considered and declined in this session, in favour of not inventing a
  mechanism beside Kubernetes' own counter.

**Current lean**: Accept. Revisit only if account counts make it measurable.

**What would unblock it**: Real numbers on how many accounts sit in a persistently-rejected state.

### P-005 — Nothing is emitted while the account does not exist

**What is uncertain**: Whether emission should really wait for the account.

**Why it is hard**: D-018 puts emission in 015, which runs after 010, and D-006 has 010 abort the run
on any non-`Done` outcome. So a persistently failing `CREATE ACCOUNT` also delays the identity sync —
and §4.3's sync is measured in tens of minutes, so the delay compounds onto an already slow path.
Design §4.3 says the request is "emitted early, never blocking", which this arguably weakens.

**Options and trade-offs**:
- *Accept it* (current): consistent with D-006, no special case. Costs sync latency exactly when the
  account is already in trouble.
- *Emit before 010*: honours "emitted early" literally. Requires either a module ordered before 010
  (which would need its own reason never to abort) or 018 emitting directly — both were rejected in
  D-018.

**Current lean**: Accept; the failing-`CREATE ACCOUNT` case is rare and already visibly broken.

**What would unblock it**: Confirmation from whoever fulfils `IdentitySyncRequest` that a request
naming a not-yet-existing account is harmless — if so, emission could move earlier cheaply.

## Open Questions

- **O-001** — How does the pipeline's aggregated `Ready` coexist with the framework's post-`Create`
  `xpv1.Creating()`? Verified: the managed reconciler marks `Creating()` and `ReconcileSuccess()` and
  requeues after `Create` returns (`crossplane-runtime/v2@v2.0.0
  pkg/reconciler/managed/reconciler.go`), so whatever 009 aggregated during that same call is
  overwritten until the next `Observe`. Harmless if 018 re-aggregates on every `Observe`, but it must
  be stated deliberately — needs input from **018**.
- **O-002** — `identitySync.enabled` and `identitySync.timeout` (design §4.3, placed in
  `baseConfig.yaml`) do not exist in `internal/config`'s `BaseConfig` — 002 was written without them
  (Verified: `internal/config/config.go` has only `Snowflake`, `AWS`, `Secrets`). 014 must extend
  `BaseConfig`, and 015 must decide what an absent/disabled `identitySync` means for `IdentitySynced`
  — needs input from **014**.

## Forward Contracts

- **008 (guardrails)** — owns the merged verdict type that D-015 leaves as a placeholder here. Written
  out of order, after 009; must land before 012.
- **010 (account module)** — must be registered first in 009's module list (D-003). Its `Observe` is
  mandatory and is the sole source of `Observation.Exists`. It must call `mc.SetLocator()` immediately
  after `CREATE ACCOUNT` returns, or every later module loses its connection (D-013). It must call
  `.Aborting()` on any non-`Done` outcome (D-006), so it must not use `Pending` for states that are in
  fact fine.
- **011 (parameter module)** — design's "read the parameters back and re-apply any that diverged" is
  deferred by D-010. For now: re-apply all global and regional parameters unconditionally on every
  `Apply`, no `SHOW PARAMETERS`. Its `Observe` returns `true, Done()`.
- **012 (network module)** — overwrite without pruning (D-011); must state P-002's orphan gap in its
  own spec, specifically that a removed `serviceUsers` entry leaves a rule and a *bound* policy behind.
  Consumes the guardrail verdict for the `"full"` rule, so it needs 008.
- **013 (auth module)** — same overwrite-and-orphan contract as 012, for auth rules and policy
  bindings. Validates exceptions against the same verdict instance 012 uses (D-014); the two must
  never disagree.
- **014 (IdentitySyncRequest)** — must extend `BaseConfig` with `identitySync.enabled` and
  `identitySync.timeout` (O-002). Its emitter is called by 015, not by 018 (D-018).
- **015 (identity module)** — owns emission of `IdentitySyncRequest` and the `identitySyncStartedAt`
  status field (D-012, D-018). Returns `Pending(reason)` while a sync is outstanding and contributes
  the `IdentitySynced` condition; `IdentitySynced=False` gates `Ready` (D-016). Never returns `Failed`
  for an outstanding sync — that is the whole point of `Pending`.
- **016 (quota)** — implements `Module` from outside `modules/`; its `Admit()` stays outside the
  contract and is called separately by 018's validation phase. Contributes `QuotaAvailable`, which
  must **not** gate `Ready` (D-016, design §3.10).
- **017/018 (deletion)** — own the whole teardown path; 009 has no teardown half (D-019), including
  disposal of the RSA keypair in 003's store.
- **018 (controller)** — calls `Observe` from `Observe` and `Apply` from both `Create` and `Update`
  (D-001); builds the `*Context` (D-014); calls `logger.Handle` once per carried error (D-005);
  advances `status.observedGeneration` only when `result.AllDone()` (D-009); leaves conditions owned by
  modules absent from `Result` untouched (D-007); must not blank an existing `status.accountUrl` on an
  aborted run; must resolve O-001. Registration order is 010 → 011 → 012 → 013 → 015 → 016, with 010
  first in `New`'s ordered module list (D-003).
- **019 (replication)** — §5.4's auto-repair is the one place drift repair survives D-010, on a
  different CRD and outside this pipeline.

## References

- **Scope note**: `specs/scope-009-account-pipeline.md` — read together with this file.
- **Product design**: `specs/design.md` §3.2 (create flow), §3.8/§3.9 (rejected entries leave the
  baseline), §3.10 (`QuotaAvailable`, `Ready=True`), §3.11 (privilege step-down), §4.3
  (`IdentitySynced`, emitted early), §6.3 (deletion phases), §7.1/§7.2 (condition and status model),
  Appendix B N1/A1/C1/S1/X1 (Organization Policy requirements).
- **Template**: `specs/000-template.md` — the destination section skeleton every `D-xxx` names.
- **Shape reference**: `specs/007-backplane-config.md` — most recently written spec; Public API and
  Error Classification phrasing to follow.
- **Dependency code**: `internal/snowflake/pool/pool.go` (`OrgAdmin`, `TenantAccount`, `EvictTenant`),
  `internal/tenant/` (`ResolveName`, `Department`, `CostCenter`, `CreditQuota`),
  `internal/backplane/backplane.go` (`Region`, `Connection`, `ContainsCIDR`),
  `internal/logger/logger.go` (`New`, `Handle`), `internal/config/config.go` (`BaseConfig`),
  `apis/base/v1alpha1/snowflakeaccount_types.go` (`SnowflakeAccountStatus`).
- **Vendored behaviour** (Verified): `crossplane-runtime/v2@v2.0.0`
  `pkg/reconciler/managed/reconciler.go` — post-`Create` `Creating()`/`ReconcileSuccess()`, and
  `ReconcileSuccess()` set after `Observe` returns on the up-to-date path;
  `apis/common/observation.go` — `ObservedStatus.ObservedGeneration`, untouched by the reconciler.
