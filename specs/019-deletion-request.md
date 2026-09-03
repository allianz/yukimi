# Specification: SnowflakeDeletionRequest & Deletion Warrants (019)

## Overview

A `SnowflakeDeletionRequest` is the one way to authorize destroying a `SnowflakeAccount` in this
platform — deleting the account's own Git-committed file is not enough on its own. This exists
because losing a data platform account by accident, through a bad merge or a stray `git rm`, is
catastrophic and hard to undo, so destruction is treated as a deliberate, privileged act kept
separate from editing or removing the account's definition. A tenant creates this resource naming
the account they want destroyed, a time-boxed window in which the destruction is allowed, and a
reason for the audit trail; a dedicated controller tracks whether that window is still open. The
`SnowflakeAccount` controller (spec 020) checks for an open window before it will actually drop an
account, and marks the window used once it does. The technical approach is a small, standalone CRD
and controller pair plus a lookup package, entirely independent of the account provisioning
pipeline (009).

## Scope

This specification defines the deletion-warrant subsystem that:
- Defines the `SnowflakeDeletionRequest` CRD type in `apis/base/v1alpha1/` (design.md §6.1): the
  target to destroy, the time-boxed window, and the audit reason.
- Runs a dedicated controller, `internal/controller/snowflakedeletionrequest/`, that computes and
  advances the request's own time-boxed lifecycle (`Active` → `Expired`/`Consumed`), independent of
  any other reconcile loop in this platform.
- Provides `internal/deletion/`'s lookup and consumption API (`FindActiveRequest`, `MarkConsumed`)
  — the only point of contact between this spec and account provisioning, called by 020's deletion
  gate.

**Out of Scope**:
- Intercepting a `SnowflakeAccount`'s own deletion, blocking it without a warrant, emitting the
  `DeletionBlocked` event, or calling `DROP ACCOUNT` — all owned by 020 (design.md §6.3 Phases 2-3).
- Any `targetRef.kind` beyond `SnowflakeAccount` — v1alpha1 accepts only that one kind (see Schema
  Specification); widening later, once a second destructible resource kind exists, is additive.
- Preventing an approved request from being edited after the fact. Nothing enforces this at the
  schema level (see Security Considerations); that control is left to RBAC or a git-review process
  outside this provider's code, and none is defined anywhere in this repository today.
- Any replication-related deletion warrant (021, not yet written).

## Key Concept: The Deletion Warrant's Lifecycle

A deletion warrant moves through three states — `Active`, `Expired`, `Consumed` — only ever
forward, never back. It starts `Active` the moment it's created, carrying an expiry computed once
from its creation time plus its requested window, capped at eight hours. If nothing consumes it
before that window closes, it becomes `Expired` on its own, with no further action from anyone;
from that point it authorizes nothing, and a fresh request is the only way back in. If it's used to
authorize an actual destruction, it becomes `Consumed` instead, permanently. Once a warrant reaches
either terminal state, its recorded expiry freezes at whatever it was at that moment — a later edit
to the requested window can no longer move it. This is why the request keeps existing after its
target is gone: it isn't cleanup residue, it's the permanent record of when a window opened, when
(or whether) it closed, and why.

## Key Concept: One Controller Decides, the Other Trusts

Only one piece of code ever decides whether a warrant is currently valid: its own controller,
which is the sole writer of `status.state`. The deletion gate that later wants to use a warrant
doesn't re-derive that answer itself — it just reads the field. Splitting "decide" and "trust" this
way means there's exactly one place validity logic can be wrong, and everywhere else just consumes
its answer.

**Important**: state transitions are one-way and terminal. A resurrected `duration` value can never
move a warrant back to `Active` once it has reached `Expired` or `Consumed`.

## Public API

```go
// Package v1alpha1 — apis/base/v1alpha1

// SnowflakeDeletionRequestSpec is the deletion warrant a tenant creates to
// authorize destroying one specific target (design.md §6.1). Nothing here
// is immutable after creation (see Security Considerations).
type SnowflakeDeletionRequestSpec struct {
	TargetRef TargetRef `json:"targetRef"`

	// Maintenance window length, capped at 8h on every write.
	// +kubebuilder:validation:XValidation:rule="self > duration('0s') && self <= duration('8h')",message="duration must be greater than 0 and at most 8h"
	Duration metav1.Duration `json:"duration"`

	// Audit trail: why this destruction is authorized (design.md §6.2).
	Reason string `json:"reason"`

	// The only crossplane-runtime managed-resource field this type
	// carries. No ProviderConfigReference, no
	// WriteConnectionSecretToReference.
	// +optional
	// +kubebuilder:default={"*"}
	ManagementPolicies common.ManagementPolicies `json:"managementPolicies,omitempty"`
}

// TargetRef names the one resource this warrant authorizes destroying.
// Name is the CRD name, not the resolved Snowflake account name.
type TargetRef struct {
	// +kubebuilder:validation:Enum=SnowflakeAccount
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// SnowflakeDeletionRequestStatus reports this warrant's time-boxed
// lifecycle. Written only by internal/controller/snowflakedeletionrequest
// and internal/deletion.MarkConsumed.
type SnowflakeDeletionRequestStatus struct {
	xpv1.ResourceStatus `json:",inline"`

	// +optional
	ValidUntil *metav1.Time `json:"validUntil,omitempty"`

	// +kubebuilder:validation:Enum=Active;Expired;Consumed
	// +optional
	State string `json:"state,omitempty"`
}

// SnowflakeDeletionRequest hand-implements GetManagementPolicies,
// SetManagementPolicies, GetCondition, and SetConditions to satisfy
// resource.Managed, identically to SnowflakeAccount (spec 006) and for
// the same reason: angryjet's generator won't recognize a type that
// omits the embedded xpv2.ManagedResourceSpec it requires.
```

```go
// Package deletion — internal/deletion

// FindActiveRequest returns the Active SnowflakeDeletionRequest in
// namespace whose spec.targetRef matches targetKind/targetName, or nil
// if none exists. Trusts status.state as authoritative — performs no
// independent validUntil check. When more than one Active candidate
// matches, returns the one with the earliest creationTimestamp.
//
// Returns: system error if the list call against the Kubernetes API
// fails. Never a user error — there is nothing about the caller's input
// a tenant could fix here.
func FindActiveRequest(ctx context.Context, c client.Client, namespace, targetKind, targetName string) (*v1alpha1.SnowflakeDeletionRequest, error)

// MarkConsumed transitions req's status.state to Consumed and freezes
// its status.validUntil at its current value. Called by 020 after a
// successful DROP ACCOUNT.
//
// Returns: system error if the status update against the Kubernetes API
// fails.
func MarkConsumed(ctx context.Context, c client.Client, req *v1alpha1.SnowflakeDeletionRequest) error
```

```go
// Package snowflakedeletionrequest — internal/controller/snowflakedeletionrequest

// SetupGated adds a controller that reconciles SnowflakeDeletionRequest
// objects with safe-start support, wired into internal/controller/yukimi.go
// alongside every other resource's controller.
func SetupGated(mgr ctrl.Manager, o controller.Options) error
```

## Schema Specification

### Fields (`spec`)

| Field Path | Type | Required | Mutability | Validation / Constraints |
| ---------- | ---- | -------- | ---------- | ------------------------ |
| `targetRef` | object | **Yes** | Mutable | — |
| `targetRef.kind` | string | **Yes** | Mutable | Enum: `SnowflakeAccount` only in v1alpha1 |
| `targetRef.name` | string | **Yes** | Mutable | The target's `metadata.name` (CRD name), not its resolved Snowflake name |
| `duration` | duration | **Yes** | Mutable | `> 0s` and `<= 8h`, enforced by CEL on every write (create and update alike) |
| `reason` | string | **Yes** | Mutable | Non-empty; audit trail only, no length cap |
| `managementPolicies[]` | string | No | Mutable | crossplane-runtime field; default `["*"]`. No `providerConfigRef` or `writeConnectionSecretToRef` field exists on this type |

None of the above carries a `self == oldSelf` immutability rule — see Security Considerations for
why, and for the bound that keeps this acceptable.

### Fields (`status`)

| Field Path | Type | Description |
| ---------- | ---- | ----------- |
| `validUntil` | string (timestamp) | `metadata.creationTimestamp` + `spec.duration` while `Active`; frozen at its terminal value once `Expired` or `Consumed`. Set by `internal/controller/snowflakedeletionrequest`. |
| `state` | string (enum) | `Active`, `Expired`, or `Consumed`; monotonic, never reverts. Set by `internal/controller/snowflakedeletionrequest` (Active/Expired) and `internal/deletion.MarkConsumed` (Consumed). |
| `conditions[]` | Condition | Standard `Ready`/`Synced` per design.md §7.1. `Ready` becomes `True` once `Observe` succeeds — there is no failure mode here beyond a Kubernetes API error. |

## Project Structure

### Source Code

```text
apis/base/v1alpha1/
└── snowflakedeletionrequest_types.go   # Spec/Status types, CEL markers, hand-implemented resource.Managed methods

internal/deletion/
├── doc.go
├── lookup.go           # FindActiveRequest: List + Active filter + earliest-creationTimestamp tie-break
├── lookup_test.go      # Unit tests using controller-runtime's fake client (no real cluster needed)
├── consume.go          # MarkConsumed: freeze validUntil, set state=Consumed
└── consume_test.go

internal/controller/snowflakedeletionrequest/
├── doc.go
├── reconciler.go        # SetupGated/Setup, connector, external — Observe recomputes state every call
└── reconciler_test.go
```

No `integration_test.go` anywhere in this spec: `internal/deletion`'s only external dependency is
the Kubernetes API, fully exercised through `controller-runtime`'s in-process fake client;
CLAUDE.md's `TestIntegration...`/`make test-integration` convention is scoped to real AWS and
Snowflake access, neither of which this spec touches.

## Error Classification

**User Errors**: none originate in this spec's Go code. `duration`'s bound and `reason`'s
non-empty requirement are both CEL rules enforced at admission — by the time any object reaches
`internal/deletion` or the controller, both are already guaranteed to hold.

**System Errors** (use `fmt.Errorf("context: %w", err)`):
- `internal/deletion.FindActiveRequest` wraps a failure listing `SnowflakeDeletionRequest` objects
  against the Kubernetes API.
- `internal/deletion.MarkConsumed` wraps a failure updating a request's status against the
  Kubernetes API.
- No system error originates in `internal/controller/snowflakedeletionrequest` — its
  `Observe`/`Create`/`Update`/`Delete` compute only from fields already present on the object handed
  to them; nothing they do can fail.

## Edge Cases

- **Can `spec.duration`/`targetRef`/`reason` be edited after the request already exists?** Yes —
  nothing on the spec is immutable. The standing CEL bound (`> 0s`, `<= 8h`) still re-evaluates on
  every edit, and `validUntil` derives from the Kubernetes-immutable `creationTimestamp`, so no
  sequence of edits can push the authorized window past `creationTimestamp + 8h`.
- **Does editing `spec.duration` after a request has already `Expired` or been `Consumed` revive
  it?** No. State transitions are monotonic and terminal: once `Expired` or `Consumed`, the
  controller stops recomputing `validUntil` from `spec.duration` and freezes it at the terminal
  value.
- **What if two `Active` requests target the same resource?** Both are legitimate — nothing in this
  platform enforces cross-object uniqueness. `FindActiveRequest` deterministically returns the one
  with the earliest `creationTimestamp`; the other simply remains unconsumed and eventually expires
  on its own, with no effect on the outcome.
- **How stale can `status.state` be relative to `validUntil`?** Bounded by the manager's poll
  interval (`--poll`, default `1m`), because `Observe` recomputes `state` on every call regardless of
  `Generation`. `FindActiveRequest` trusts `state` directly and performs no live `validUntil` check.
- **What actually stops someone from editing an approved request to quietly retarget it or widen its
  window?** Nothing at the schema level. That's left to an RBAC or git-review process outside this
  provider's code, and none is defined anywhere in this repository today — this spec's guarantees
  hold only as far as that external control actually exists.
- **What happens to a `SnowflakeDeletionRequest` when its target is destroyed?** Nothing — it carries
  no owner reference to its target and no finalizer of its own, so it outlives the target by design,
  forming the durable audit trail (design.md §6.2).

## Dependencies

- **internal/errors (001)** - Used APIs: none directly - Contract: `internal/deletion` classifies
  nothing as a user error; every failure it returns is a plain wrapped error, left for whichever
  caller's own `internal/logger.Handle` (020's controller) to classify as a system error.
  `internal/controller/snowflakedeletionrequest` has no error path at all and therefore never
  imports `internal/logger` either — a deliberate divergence from every other controller in this
  codebase, which do have failure modes to report.
- **apis/base/v1alpha1 SnowflakeAccount (006)** - Used APIs: none at the Go-import level - Contract:
  `targetRef.kind`'s enum names `"SnowflakeAccount"` as a literal string matching 006's
  `SnowflakeAccountKind`; the two specs share a naming convention, not a package import.

## Integration Points

- **SnowflakeAccount controller (020)** - calls `internal/deletion.FindActiveRequest` when
  intercepting a `SnowflakeAccount`'s deletion, and `internal/deletion.MarkConsumed` after a
  successful `DROP ACCOUNT` - Key functions: `FindActiveRequest`, `MarkConsumed` - Notes: the
  dependency is one-way; 019 never imports anything from 020.
- **internal/controller/yukimi.go** - registers `snowflakedeletionrequest.SetupGated` in its list of
  controllers alongside every other resource's - Key functions: `SetupGated`.
- **crossplane-runtime's `managed.NewReconciler`** - drives `Observe`/`Create`/`Update`/`Delete` on
  the manager's global poll interval - Notes: no per-resource requeue override exists in
  crossplane-runtime v2.0.0, so this controller shares the same ~1m default poll cadence as every
  other controller in this codebase.

## Success Criteria

- **SC-001**: `apis/base/v1alpha1` registers `SnowflakeDeletionRequest` in group
  `base.snowflake.yukimi.io`, version `v1alpha1`.
- **SC-002**: `SnowflakeDeletionRequestSpec`/`Status` cover every field in the Schema Specification
  tables, with matching JSON names.
- **SC-003**: `targetRef.kind` accepts only `"SnowflakeAccount"`; any other value is rejected at
  admission.
- **SC-004**: `spec.duration` is rejected at admission for any value `<= 0s` or `> 8h`, both on
  create and on every subsequent update.
- **SC-005**: `spec.reason` is required and rejected at admission when empty.
- **SC-006**: no field on `SnowflakeDeletionRequestSpec` carries a `self == oldSelf` CEL rule
  (grep-provable).
- **SC-007**: `SnowflakeDeletionRequestSpec` carries exactly one crossplane-runtime-owned field
  (`managementPolicies`); no `providerConfigRef` or `writeConnectionSecretToRef` field exists
  anywhere in the generated CRD schema.
- **SC-008**: `SnowflakeDeletionRequest` satisfies `resource.Managed` via hand-written methods, the
  same way `SnowflakeAccount` does.
- **SC-009**: `internal/controller/snowflakedeletionrequest` is registered separately in
  `internal/controller/yukimi.go`'s `SetupGated` list, distinct from 020's controller.
- **SC-010**: `Observe` recomputes `status.state` on every reconcile regardless of whether
  `Generation` changed — a request whose `validUntil` has passed flips to `Expired` on the next poll
  with no CRD edit required.
- **SC-011**: a newly created request with a valid `duration` reaches `status.state = Active` with
  `status.validUntil = creationTimestamp + duration` within one reconcile.
- **SC-012**: once `status.state` reaches `Expired` or `Consumed`, no later edit to `spec.duration`
  changes `status.validUntil` or reverts `state` to `Active`.
- **SC-013**: `internal/deletion.FindActiveRequest` returns only requests with
  `status.state == "Active"` matching the given namespace/kind/name, performing no independent
  `validUntil` comparison.
- **SC-014**: when multiple `Active` requests match the same target, `FindActiveRequest`
  deterministically returns the one with the earliest `creationTimestamp`.
- **SC-015**: `internal/deletion.MarkConsumed` sets `status.state = Consumed` and freezes
  `status.validUntil` at its value at call time.
- **SC-016**: `FindActiveRequest`/`MarkConsumed` return a wrapped system error on any Kubernetes API
  failure, never a user error.
- **SC-017**: a `SnowflakeDeletionRequest` with `GetDeletionTimestamp()` set causes `Observe` to
  return `ResourceExists: false`, releasing the finalizer.
- **SC-018**: unit test coverage exceeds 95% for `internal/deletion` and
  `internal/controller/snowflakedeletionrequest`.
- **SC-019**: `make generate` produces a valid CRD manifest and `make reviewable` passes.

## Security Considerations

- Nothing on `SnowflakeDeletionRequestSpec` is immutable, so the schema alone cannot stop someone
  from editing an approved warrant's target, window, or reason after review. This is deliberate:
  enforcement is left to an RBAC or git-review process outside this provider's code, and no such
  control is defined anywhere in this repository yet — deploying this spec's code is not sufficient
  by itself; that external control must exist too.
- The residual risk from the point above is bounded, not open-ended: `duration`'s CEL ceiling
  (`<= 8h`) applies on every write, and `validUntil` derives from the Kubernetes-immutable
  `creationTimestamp`, so no sequence of edits can push a warrant's authorized window past
  `creationTimestamp + 8h`.
- `targetRef.kind` accepts only `SnowflakeAccount`, so a warrant can never be pointed at a resource
  kind this platform hasn't reasoned about the destructiveness of yet.
- `FindActiveRequest`'s trust in the persisted `status.state` field, rather than a live re-check,
  bounds worst-case staleness to about one poll interval (~1 minute) — small against the 8h maximum
  window it's protecting.

## Performance Considerations

- `Observe` is a pure, in-memory computation from fields already on the object it's handed; it does
  no I/O and stays cheap even though it can't skip work on an unchanged `Generation`.
- `FindActiveRequest` is a single namespaced `List` call, bounded by however many
  `SnowflakeDeletionRequest` objects exist in that namespace — expected to stay small.

## References

- **Product design**: `specs/design.md` §6.1-6.3, §7.1 - the authoritative source for every field,
  state, and behavior in this spec.
- **Shape reference**: `specs/001-error-and-logging.md` - section skeleton followed here.
- **CEL duration bound and minimal-managed-resource-surface precedent**:
  `specs/006-snowflake-account-crd.md` - followed directly for the `duration` CEL mechanism and the
  hand-implemented `resource.Managed` pattern.
- **Pipeline package boundary**: `specs/009-account-pipeline.md` - confirms this spec's controller is
  not a pipeline module; deletion is a single `DROP ACCOUNT` plus finalizer release owned by 019/020,
  with no per-module teardown to sequence.
- **Poll interval**: `cmd/provider/main.go` `--poll` flag, default `1m`.
- **crossplane-runtime v2.0.0**: `pkg/reconciler/managed/reconciler.go` - confirms no per-resource
  requeue override exists.

<br/><br/><br/><br/><br/>

================

## Appendix: Usage Examples

### Example 1: 020's deletion gate looking up an active warrant

```go
req, err := deletion.FindActiveRequest(ctx, kube, cr.Namespace, "SnowflakeAccount", cr.Name)
if err != nil {
    return log.Handle(err) // system error: Kubernetes API failure
}
if req == nil {
    // Block: no open warrant. Emit DeletionBlocked, set Ready=False, stay Terminating.
    return managed.ExternalDelete{}, errors.NewUserError("deletion blocked: no active SnowflakeDeletionRequest authorizes this account")
}
```

### Example 2: 020 marking a warrant used after a successful `DROP ACCOUNT`

```go
if err := dropAccount(ctx, conn, accountName); err != nil {
    return managed.ExternalDelete{}, fmt.Errorf("failed to drop account: %w", err)
}
if err := deletion.MarkConsumed(ctx, kube, req); err != nil {
    return managed.ExternalDelete{}, err // system error: status update failed
}
```

### Example 3: `SnowflakeDeletionRequest` YAML

```yaml
apiVersion: base.snowflake.yukimi.io/v1alpha1
kind: SnowflakeDeletionRequest
metadata:
  name: decommission-analytics-prod
spec:
  targetRef:
    kind: SnowflakeAccount
    name: analytics-team-eu   # CRD name, not the resolved Snowflake name
  duration: 4h                # Maintenance window (max: 8h)
  reason: "Ticket OPS-1234: Project sunsetting, data archived."
status:
  validUntil: "2026-09-03T18:00:00Z"
  state: Active
  conditions:
    - type: Ready
      status: "True"
      reason: "Available"
    - type: Synced
      status: "True"
      reason: "ReconcileSuccess"
```
