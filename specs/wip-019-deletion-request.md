> **Clarification record — not a specification.** Produced by `/yukimi.clarify 019` to settle what
> `specs/design.md` intentionally leaves out and `specs/scope-019-deletion-request.md` does not
> cover. It records decisions, not product design — `specs/design.md` remains authoritative and
> always wins, and once `019-deletion-request.md` is written the spec wins over this file too.
> Read it together with the scope note when writing `019-deletion-request.md` (delete the scope
> note then), keep it as supporting detail while `019` is implemented, and delete it once the code
> has landed.

## Clarification runs

- Run 1 — covered: controller ownership/lifecycle mapping, spec-field immutability, the
  Active-warrant trust model for 020's deletion gate, `targetRef.kind` allowlist, state
  monotonicity, condition/status modeling, `internal/deletion` public API. Left open: O-001 (the
  RBAC/process control the "nothing immutable" decision relies on).

## Resolved Decisions

### D-001 — SnowflakeDeletionRequest gets its own dedicated controller

**Question**: CLAUDE.md's package table lists only `apis/base/v1alpha1/` + `internal/deletion/` for
spec 019 — no controller package. But design.md §6.3 Phase 1 describes "the controller" computing
`status.validUntil` and flipping `state` from `Active` to `Expired` as time passes, which needs
something reconciling on a timer, not just on spec changes. Does 019 register its own controller,
does 020 manage this CRD's status as a side effect of its own reconciles, or is status computed
lazily with no controller at all?

**Decision**: 019 registers its own dedicated controller,
`internal/controller/snowflakedeletionrequest/`, wired into `internal/controller/yukimi.go`
separately from 020's SnowflakeAccount controller. It follows CLAUDE.md's "Validation-Only
Controller" pattern (no external system, all logic in `Observe`, `Create`/`Update`/`Delete` are
no-ops, `GetDeletionTimestamp()` triggers `ResourceExists: false` to release the finalizer) — but
with one deliberate deviation from that pattern's stated optimization: `Observe` must **not** skip
work when `Generation` is unchanged, because the `Active` → `Expired` transition is time-driven, not
spec-driven. Every `Observe` call recomputes `status.state` from `status.validUntil` (itself derived
from `metadata.creationTimestamp` + current `spec.duration`) against `time.Now()`.

This still goes through the same `managed.NewReconciler`/`ExternalClient` machinery every other
controller in this codebase uses (CLAUDE.md's "Shared Across All Controller Types" section), for
consistency — `Connect` is trivial here (no secrets/pool dependency, just whatever local client is
needed), unlike 020's `Connect` which dials Snowflake.

**Rationale**: Design.md's Phase 1 language ("the controller verifies... sets status.state...")
presupposes an active reconcile loop, and CLAUDE.md's Validation-Only Controller pattern already
describes almost exactly this shape (`ExternalObservation`, `ResourceExists`,
`GetDeletionTimestamp()`) — strong evidence it was written with this resource in mind. The
alternatives were rejected: lazy/no-controller computation leaves `kubectl get` showing a stale or
absent `state` and weakens the audit trail's visibility of *when* a transition happened (see D-004);
folding this into 020 couples an unrelated CRD's freshness to whichever SnowflakeAccount happens to
reconcile nearby, and works against CLAUDE.md's "thin orchestration only" framing for 020.

**Affects spec section**: Project Structure, Public API (controller), Key Concept (lifecycle).

### D-002 — No CEL immutability on any spec field

**Question**: `SnowflakeDeletionRequest` is meant to be a one-time "key" tied to a specific target,
window, and reason (design.md §6.2's "two-key" framing). If `spec.targetRef`/`duration`/`reason`
stay mutable, someone could create a request with a short, easily-approved window and quietly widen
it afterward — retroactively expanding an authorization without a fresh audit entry. Should any of
these be made immutable after creation via CEL (`self == oldSelf`, spec 006's pattern), or does
mutability stay unconstrained at the schema level?

**Decision**: Nothing on `SnowflakeDeletionRequestSpec` is immutable. No CEL `self == oldSelf` rule
on `targetRef`, `duration`, or `reason`. Enforcement of "don't edit an approved request" is left to
RBAC/git review process, not the API server.

**Rationale**: User's explicit choice. Two facts found during research bound the residual risk
this accepts, so it is not open-ended (see D-007 and D-004):

1. `duration` carries a standing CEL cap (`> 0` and `<= 8h`) that applies on **every** write, not
   just create (no `oldSelf` guard — there's nothing to compare against on create, and the rule
   re-evaluates unconditionally on update). So no single edit can ever push `duration` above 8h.
2. `validUntil` is always `metadata.creationTimestamp` (K8s-enforced immutable, since it's part of
   object identity) + the *current* `spec.duration`. Because `creationTimestamp` can't move, no
   sequence of edits to `duration` can push a given request's authorized window past
   `creationTimestamp + 8h`. The accepted risk is "can be shrunk or extended within an absolute 8h
   ceiling from creation," not "can be kept alive indefinitely" — materially smaller than it first
   appears.

**Affects spec section**: Schema Specification, Security Considerations, Edge Cases.

### D-003 — 020's lookup trusts `status.state`, does not re-derive from `validUntil`

**Question**: When 020 looks up an Active deletion warrant to authorize `DROP ACCOUNT`, does it
trust the persisted `status.state` field, or does the lookup helper independently re-check
`validUntil > now` at call time regardless of what `state` currently says? This matters because
design.md's stated purpose for time-boxing is preventing "dangling permissions" — if `state` lags
reality even briefly, a window that should be closed could still authorize a deletion.

**Decision**: The lookup (`internal/deletion.FindActiveRequest`, D-009) trusts the persisted
`status.state` field as authoritative. It filters candidates on `status.state == "Active"` directly
and performs no independent `validUntil` comparison.

**Rationale**: User's explicit choice, given the following bound found during research: the
manager's default poll interval is `1m` (`cmd/provider/main.go:60`'s `--poll` flag default), and
crossplane-runtime v2.0.0's `managed.ExternalObservation` has no per-resource requeue override —
every controller in this codebase is bound by the same global poll interval
(`pkg/reconciler/managed/reconciler.go`'s `defaultPollInterval` usage confirmed in
`crossplane-runtime/v2@v2.0.0`). Because D-001's controller recomputes `state` unconditionally on
every `Observe` (not gated by generation), the worst-case staleness between `validUntil` passing and
`state` flipping to `Expired` is on the order of one poll interval (~1 minute) — small relative to
the 8h maximum window design.md permits. The alternative (live re-derivation inside the lookup) was
the safer option in isolation, but the user judged this bound acceptable in exchange for a simpler,
single-source-of-truth lookup.

**Affects spec section**: Public API (`internal/deletion`), Integration Points, Edge Cases.

### D-004 — State transitions are monotonic and terminal

**Question**: Given `duration` is mutable (D-002) and `validUntil` is recomputed live while a
request is `Active` (D-001), what happens if `spec.duration` is edited *after* a request has already
transitioned to `Expired` or `Consumed`, in a way that would move a recomputed `validUntil` back into
the future? Does the request revive to `Active`?

**Decision**: No. `Active` → `{Expired, Consumed}` is one-way. Once `status.state` becomes `Expired`
or `Consumed`, the controller stops recomputing `validUntil` from `spec.duration` — it freezes at the
value in effect at the moment of the terminal transition — and no later edit to `spec.duration` can
move `state` back to `Active`.

**Rationale**: Design.md §6.3 states an expired request "no longer authorizes anything — a new
request is required." That guarantee only holds if edits can't revive it; otherwise "a new request
is required" would be false (you could just edit the old one). Freezing `validUntil` at the terminal
transition also keeps the audit trail meaningful — it records *when* the window actually closed, not
whatever the spec happens to say now. `Consumed` additionally must never revert for safety: once a
`DROP ACCOUNT` has run, there is nothing left to reconsider.

**Affects spec section**: Key Concept (lifecycle), Edge Cases.

### D-005 — `targetRef.kind` allowlist is `SnowflakeAccount` only

**Question**: Design.md §6.2 says the positive-control model protects "every critical resource," but
§6.3 only describes the account interaction. What does `targetRef.kind` accept for v1alpha1?

**Decision**: `+kubebuilder:validation:Enum=SnowflakeAccount` — no other kind is accepted in
v1alpha1.

**Rationale**: `SnowflakeAccount` is the only CRD kind that exists in code today (`apis/base/`
contains no `SnowflakeReplication` type yet — that's spec 021, not yet written or implemented).
`specs/scope-019-deletion-request.md`'s own recommendation (dropping a replication group destroys no
data, so restricting scope now is safe) and design §6.3 only ever describing the account interaction
both point the same way. Widening the enum later, once 021 lands, is purely additive and non-breaking
for existing objects.

**Affects spec section**: Schema Specification.

### D-006 — `status.state` is a plain status field, not a fourth condition type

**Question**: Design.md §7.1 says "every CRD in this platform surfaces" `Ready`/`Synced`, and
individual resource types may add further conditions (`QuotaAvailable`, `IdentitySynced`). Should
`Active`/`Expired`/`Consumed` be modeled as a condition type the same way, or as the plain
`status.state` enum field design §6.1's schema literally shows?

**Decision**: Plain status field. `status.state` (enum: `Active`, `Expired`, `Consumed`), separate
from `status.conditions`. `Ready`/`Synced` still apply unmodified, per the standard boilerplate:
`Ready` becomes `xpv1.Available()` once `Observe` succeeds (there is no failure mode here beyond a
Kubernetes API error), `Synced` follows the same generic rule every controller uses.

**Rationale**: Design.md §6.1's example and §6.3's prose both write `status.validUntil` and
`status.state` as ordinary fields ("It sets `status.state = Active`"), never phrased as a condition.
No re-reading of §7.1 supports treating this differently from what the resource-specific schema
already shows. This needed no user question — the design text already settles it once read
carefully.

**Affects spec section**: Schema Specification (status), Key Concept.

### D-007 — `duration` bounds and `reason` requirement

**Decision**: `spec.duration` (`metav1.Duration`) carries one CEL rule enforcing both bounds:
`self > duration('0s') && self <= duration('8h')`, applied on every write (no `oldSelf` — nothing to
compare against on create, and it must hold on every subsequent edit too, per D-002).
`spec.reason` is a required, non-empty string with no CEL length cap.

**Rationale**: Design.md §6.1's example always includes a `reason` naming a ticket, and §6.2's
"Durable Audit Trail" section frames the whole object's value around linking destruction to "a
reason" — mirrors spec 006's `AuthException.Reason`, which is required for the same audit-value
reasoning. A `duration` floor above zero was not explicit in design.md but is an obvious
correctness gap: an unbounded-below duration would let `validUntil` fall before or at
`creationTimestamp`, authorizing nothing while still claiming to be `Active` immediately upon
creation.

**Verified** (kubernetes.io/docs/reference/using-api/cel/): a CRD schema field with
`format: duration` is exposed to CEL as a native `Duration` type, usable directly in comparisons and
arithmetic (the docs' own worked example, `has(self.expired) && self.created + self.ttl <
self.expired`, uses a duration-typed field directly without an explicit `duration()` conversion). A
string literal like `duration('8h')` converts to the same type for the comparison side. This is the
same mechanism 006 uses for its `self == oldSelf` immutability rules, just with a different
comparison operator — no controller-side runtime check is needed for either bound; both are rejected
at admission.

**Affects spec section**: Schema Specification, Public API (Go type), Error Classification (this
produces zero user/system errors originating in 019's own Go code — the only two constraints are
CEL-enforced at admission, never reaching the controller).

### D-008 — Multiple concurrent Active requests per target: allowed, tie-break by creation time

**Question**: Nothing prevents two `Active` `SnowflakeDeletionRequest` objects from simultaneously
targeting the same resource in the same namespace (no admission-webhook infrastructure exists
anywhere in this platform to enforce cross-object uniqueness, and CEL rules can't see sibling
objects). What does `FindActiveRequest` return when this happens?

**Decision**: Deterministic tie-break by earliest `metadata.creationTimestamp` among matching
`Active` candidates.

**Rationale**: Which one gets marked `Consumed` is inconsequential to the actual outcome — the
target resource is deleted either way, and the "losing" request simply remains `Active` (and
eventually `Expired`) unconsumed. Determinism only matters for testability, not for correctness of
the deletion gate itself. Building real uniqueness enforcement would require a validating webhook,
which no other spec in this platform establishes — out of scope to introduce here for a corner case
with no safety consequence.

**Affects spec section**: Public API (`internal/deletion`), Edge Cases.

### D-009 — `internal/deletion` public API

**Decision**:

```go
package deletion

// FindActiveRequest returns the Active SnowflakeDeletionRequest in namespace whose
// spec.targetRef matches targetKind/targetName, or nil if none exists. Trusts
// status.state as authoritative (D-003) — performs no independent validUntil check.
// When more than one Active candidate matches, returns the one with the earliest
// creationTimestamp (D-008).
//
// Returns: System error if the list call against the Kubernetes API fails. Never a
// user error — there is nothing about the caller's input a tenant could fix here.
func FindActiveRequest(ctx context.Context, c client.Client, namespace, targetKind, targetName string) (*v1alpha1.SnowflakeDeletionRequest, error)

// MarkConsumed transitions req's status.state to Consumed and freezes its
// status.validUntil at its current value (D-004), called by 020 after a successful
// DROP ACCOUNT.
//
// Returns: System error if the status update against the Kubernetes API fails.
func MarkConsumed(ctx context.Context, c client.Client, req *v1alpha1.SnowflakeDeletionRequest) error
```

**Rationale**: 020's scope note describes the lookup and the Consumed write in prose only, with no
concrete signature. Centralizing both in `internal/deletion` (rather than 020 hand-rolling
`client.List`/`client.Status().Update` calls against a CRD it doesn't own) keeps 020 "thin
orchestration only" as CLAUDE.md requires, and keeps the D-003/D-004/D-008 decisions encapsulated in
the one package that owns them. This is the first spec in the codebase needing a
`sigs.k8s.io/controller-runtime` `client.Client` — confirmed via code search that no existing package
does this yet, so there's no established alternate convention to follow instead.

**Affects spec section**: Public API, Project Structure, Integration Points.

### D-010 — Minimal managed-resource surface, same as 006

**Decision**: `SnowflakeDeletionRequestSpec` carries exactly one crossplane-runtime-owned field
(`ManagementPolicies`), no `providerConfigRef`, no `writeConnectionSecretToRef` — identical to spec
006's `SnowflakeAccountSpec`. `SnowflakeDeletionRequest` hand-implements
`GetManagementPolicies`/`SetManagementPolicies`/`GetCondition`/`SetConditions` to satisfy
`resource.Managed`, the same way `SnowflakeAccount` does, rather than relying on angryjet's generator
(which requires an embedded `xpv2.ManagedResourceSpec` this type deliberately omits).

**Rationale**: D-001 established that this type goes through the same
`managed.NewReconciler`/`ExternalClient` machinery as every other controller, which requires
`resource.Managed`. 006 already established why this platform's CRDs carry no provider-config or
connection-secret surface (nothing here needs one either — no credentials, no external client to
point elsewhere), and already worked out that angryjet won't generate the interface methods for a
type shaped this way. No reason for 019 to diverge from that precedent.

**Affects spec section**: Public API (Go type), Key Concept.

## Problem Areas

(none — every gap identified during research either had a clean answer from `specs/design.md`
itself or was resolved via a user decision above)

## Open Questions

- **O-001** — Decision D-002 ("nothing immutable, rely on RBAC/git review") depends on an actual
  RBAC policy or git-review process that prevents editing an approved `SnowflakeDeletionRequest`
  after the fact. No such policy is defined anywhere in this repository yet (no `ClusterRole`
  manifests, no CODEOWNERS-style review gate for this CRD specifically). Needs input from platform
  ops on where that control will actually live — it is an operational decision outside this
  provider's code, so it does not block writing or implementing spec 019, but the spec should say
  plainly that the schema-level guard does not exist and name this as the reason.

## Forward Contracts

- **020 (SnowflakeAccount controller)** — 020's deletion gate (already described in detail in its
  own scope note) can call `internal/deletion.FindActiveRequest`/`MarkConsumed` (D-009) directly,
  with no additional `validUntil` re-checking of its own (D-003) and no need to handle the
  multiple-Active-candidates case itself (D-008 resolves it inside 019's package). See the append to
  `specs/scope-020-snowflakeaccount-controller.md` for the self-contained version of this contract.

## References

- **Product design**: `specs/design.md` §6.1–6.3, §7.1 — the authoritative source for every field,
  state, and behavior in this record.
- **Shape reference**: `specs/001-error-and-logging.md` — section skeleton followed here.
- **CEL immutability and minimal-managed-resource-surface precedent**:
  `specs/006-snowflake-account-crd.md` — followed directly for D-002's mechanism (where used) and
  D-010.
- **Deletion gate consumer**: `specs/scope-020-snowflakeaccount-controller.md` — describes 020's
  side of the Phase 2/3 interaction this record's D-009 API serves.
- **Poll interval**: `cmd/provider/main.go:60` — `--poll` flag, default `1m`, referenced in D-003.
- **crossplane-runtime v2.0.0**: `pkg/reconciler/managed/reconciler.go` (`ExternalObservation`,
  `defaultPollInterval` usage) — confirms no per-resource requeue override exists, referenced in
  D-003.
- **Kubernetes CEL duration support**: `https://kubernetes.io/docs/reference/using-api/cel/` —
  confirms `format: duration` fields are exposed to CEL as a native `Duration` type, referenced in
  D-007.
