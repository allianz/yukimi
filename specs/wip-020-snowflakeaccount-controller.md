> **Clarification record — not a specification.** Produced by `/yukimi.clarify 020` to settle what
> `specs/design.md` intentionally leaves out and `specs/scope-020-snowflakeaccount-controller.md` does not
> cover. It records decisions, not product design — `specs/design.md` remains authoritative and always
> wins, and once `020-snowflakeaccount-controller.md` is written the spec wins over this file too.
> Read it together with the scope note when writing `020-snowflakeaccount-controller.md` (delete the
> scope note then), keep it as supporting detail while `020` is implemented, and delete it once the code
> has landed.

## Clarification runs

- Run 1 — covered: which pipeline modules 020 registers given 008/010/011/013–018 are deliberately
  skipped for this cut; whether `cmd/provider/main.go`'s still-unfinished bootstrap wiring (`--configDir`,
  base/backplane config, secrets backend, connection pool — nothing in this codebase has needed any of it
  until now) belongs to 020; the admission gap left by skipping guardrail-check (010) and quota-check
  (011); and how a module registered later ever gets applied to a `SnowflakeAccount` that is already
  `Ready` under today's smaller pipeline. Left open: O-001.
- Run 2 — covered: the user asked, as direct follow-up feedback (not a fresh `AskUserQuestion` round), to
  also drop the region `available` gate (007) that D-001 had kept as the one remaining admission check —
  see D-005, which supersedes the relevant parts of D-001 and D-002.

## Resolved Decisions

### D-001 — A deliberately partial pipeline for this cut of 020

**Question**: `specs/scope-020-snowflakeaccount-controller.md`'s "Module registration and ordering"
section documents the full chain — `account.New(guardrailCheckModule, quotaCheckModule, accountModule,
parameterModule, networkModule, authModule, identityModule, quotaMonitorModule)` — built from specs
008/010/011/013–015/017/018, none of which exist yet. The user wants 020 implemented now, ahead of all
of those, with only the account pipeline (009) and the account module (012) wired in, sufficient to
create and destroy a `SnowflakeAccount` end to end. What does 020 actually register, and what happens to
every design.md capability that depends on a skipped module?

**Decision**: `internal/controller/snowflakeaccount` registers `pipeline.New(accountModule)` — the
account module (012) alone. The controller's own validation phase is limited to 006's already-enforced
CEL rules (immutability, the `environment` enum, `roleBindings` requiring `ACCOUNTADMIN`, an auth
exception naming at least one method) — no guardrail or quota admission runs anywhere, since both are
pipeline modules (010, 011), not controller code, per the guardrail-check design conversation already
recorded in the scope note. **This originally also kept the region's `available` gate (007) as the one
remaining check; D-005 below drops that too, so 006's CEL is now the entirety of the validation phase.**
Every capability tied to a skipped module is
simply inert: `spec.customNetworkRules`, `spec.customAuthRules`, `spec.identityIntegration.groups`, and
`spec.creditQuota` remain exactly as 006 already defines them — nothing about the CRD schema changes —
but nothing reads or acts on them until the module that owns that concern exists. Concretely, a freshly
created account gets `CREATE ACCOUNT` and the `platform` service user (012) and nothing else: no global
or regional parameters (013), no network policy of any kind (014) — Snowflake's own default is unrestricted
access when no `NETWORK_POLICY` is bound, so this is *more* open than the platform's intended baseline,
not less — no auth-exception bindings (015, moot anyway since the account has no SSO-only baseline yet
either), no imported groups or role bindings (017, meaning no human identity can log into the account at
all yet — only the platform's own service-user key exists), and no resource-monitor/budget enforcement
(018, meaning `creditQuota` is accepted but never pushed into Snowflake or capped).

**Rationale**: this is the user's explicit request for this session. It deliberately steps around the
"a spec may depend only on specs numbered strictly below it, implemented in ascending order" convention
stated at the top of every scope note — 020 numerically depends on 008/010/011/013–018, all lower-numbered
than 020 but not yet clarified, specified, or implemented. That's recorded here rather than silently
smoothed over. What makes it safe to do without special-casing 020 itself is design.md §1.2's own stated
philosophy: code may be edited directly and the governing spec updated in the same change. So when each
of 008/010/011/013–018 is eventually clarified and implemented, *that* implementation is what adds itself
to the `pipeline.New(...)` call and updates this spec's own Public API/Dependencies sections — 020 needs
no forward-compatible extension mechanism (a plugin registry, a config-driven module list, etc.) built in
now for a need that doesn't exist yet. Registering a module is one literal argument in one Go function
call.

**Affects spec section**: Scope (state plainly that guardrails/quota/parameters/network/auth/identity/
quota-monitor are out of scope for this revision, distinct from "out of scope forever"), Key Concept
(module registration — much smaller than the scope note's worked example), Public API (the module list
literal), Dependencies (drop 008/010/011/013–015/017/018 from the dependency list; keep only what's
actually imported: 001–007, 009, 012, 019).

### D-002 — `cmd/provider/main.go` bootstrap wiring is in scope of 020

**Question**: `cmd/provider/main.go` today has no `--configDir` flag and never calls `base.Load`,
`backplane.Load`, constructs a secrets backend, or builds a connection pool — nothing in this codebase has
needed any of that until now, since every controller so far (`snowflakedeletionrequest`) needs none of it.
020 is the first controller that does. Is finishing that wiring part of 020's implementation, or a
separate prerequisite?

**Decision**: In scope of 020. Concretely, `main.go` gains:
- A `--configDir` flag (default `/etc/yukimi/config`, per `specs/002-base-config.md`'s own worked
  example), resolved once at startup.
- `cfg, err := base.Load(configDir)` (002) — already fully specified and implemented; 020 adds no new
  loading logic of its own. **`backplane.Load` is *not* called here — see D-005, which drops the region
  `available` gate this would have fed, leaving nothing in this cut that reads `backplane.Config` at
  all.**
- A switch on `cfg.CloudProvider()` to construct the matching `secrets.Backend`. Only `"aws"` is compiled
  in today (`secrets/aws.New(cfg.AWS.Region, cfg.AWS.KmsKeyId, cfg.Deletion.GracePeriodDays)`, 003.a); any
  other value is a fatal startup error listing the cloud providers actually compiled in, per 002's own
  Integration Points section.
- `secrets.NewCachedBackend(backend, cfg.Secrets.CacheTTL)` (003) wrapping that backend.
- `pool.New(cachedBackend, cfg)` (004) built from the same `cfg`.
- `internal/controller/yukimi.go`'s `SetupGated` gains whatever parameters are needed to construct and
  forward `cfg`, the pool, and the cached secrets backend into a new
  `snowflakeaccount.SetupGated(mgr, o, ...)` — no `bp *backplane.Config` parameter, per D-005;
  `snowflakedeletionrequest.SetupGated` keeps its existing two-parameter signature since it needs none of
  this (019's own controller has no external dependency beyond the Kubernetes API).

**Rationale**: no spec number owns `cmd/provider/main.go` itself; specs 002, 003.a and 004 each already
state, in their own already-written Integration Points sections, exactly what main.go must do — there is
nothing left to invent, only to assemble in the order those specs already imply (config, then secrets,
then pool). 007's own Integration Points section also names `cmd/provider/main.go` as its caller, but
D-005 drops that call for this cut since nothing here consumes it yet. Treating any of this as a separate
prerequisite would just mean writing a throwaway stub `Setup` function today and redoing the wiring the
moment 020 actually needs it, for no benefit.

**Affects spec section**: Project Structure (lists `cmd/provider/main.go` and `internal/controller/
yukimi.go` as touched, not just `internal/controller/snowflakeaccount/`), Integration Points.

### D-003 — No interim admission gate

**Question**: with guardrail-check (010) and quota-check (011) both skipped, the only admission control
left was the backplane's `available` region flag (007) and 006's existing CEL rules. Any syntactically
valid `SnowflakeAccount` in an available region creates a real Snowflake account, with no naming-pattern
check, no CIDR/network constraint, and no credit-quota ceiling. Does 020 add any placeholder gate for the
interim, or accept this fully open?

**Decision**: No interim gate. Admission control is deferred entirely to 010/011 landing later. **D-005
below goes one step further and drops the region-`available` check too, so 006's CEL rules end up being
the entirety of what's enforced for this cut — not just the "no naming/CIDR/quota check" gap this
decision originally described.**

**Rationale**: matches the user's explicit plan. The cluster this runs against is wiped constantly during
this development phase, so there's no persistent tenant population for an open admission window to put at
risk yet — this isn't a security trade-off being knowingly carried into a production posture, just an
absent feature in an unfinished platform. Adding a placeholder rule (e.g. a hardcoded `dev`-only
restriction) now would be extra work with no design.md basis, later deliberately undone once 010/011
exist.

**Affects spec section**: Scope / Out of Scope (state plainly that admission control is absent, not
"deferred to a later phase of this same spec"), Edge Cases (a tenant can create an account naming, or
credit quota that a fully-configured deployment would have rejected at admission — expected today; see
D-005 for the region case specifically, which is a slightly different flavor of the same gap since it
also removes a config-file lookup, not just a check).

### D-004 — Retrofitting a later-added module onto pre-existing accounts is that module's own problem

**Question**: `internal/account/pipeline`'s `Apply` only re-runs when a `SnowflakeAccount`'s
`generation` moves past what the last successful run recorded, or when some module's own `Observe`
reports `inSync == false` (009, Key Concept: Overwrite Apply). A `SnowflakeAccount` that reaches `Ready`
today, under the account-only pipeline, has a stable generation from then on. So when, say, the network
module (014) is registered months from now, whether its state ever actually gets asserted against that
*already-existing* account depends entirely on whether 014's own `Observe` is built to notice "I have
never run against this account" and report out-of-sync — 009's own illustrative parameter-module example
(Appendix Example 4) explicitly does *not* do this; it always reports in-sync, by design, since it
declares drift detection out of scope. If a real future module reused that exact pattern, it would
silently never apply to any account that predates its own registration. Does 020 add a mechanism now
(e.g. a module-set hash persisted to `status`, forcing one `Apply` pass whenever the registered module set
changes) to guarantee this regardless of any individual module's own `Observe` design, or leave it to each
future module?

**Decision**: Leave it to each future module's own `Observe`. 020 adds no module-set-version or similar
field to `status` now.

**Rationale**: the user's explicit choice, and it matches CLAUDE.md's "don't design for a hypothetical
future requirement" — there is exactly one module registered today, so there is nothing yet for such a
mechanism to guard. As with D-003, the constantly-wiped development cluster means there's no real
population of `Ready` accounts whose silent non-migration would actually matter right now. The
constraint this decision places on every future module — its `Observe` must be able to tell "never
applied" apart from "already correct" — is real, though, and would otherwise be lost when this file is
deleted; it's propagated to the affected modules' own scope notes (010, 011, 013, 014, 015, 017, 018;
each already carries their own reason to check state anyway, so this should not be a new burden in
practice) rather than left only here. See "Propagated to lower-numbered scope notes" below.

**Affects spec section**: none in 020 itself (no new status field; `apis/base/v1alpha1` schema (006) is
unchanged). Recorded here purely so the reasoning survives, and pushed onward to the specs it actually
constrains.

### D-005 — Also skip the region `available` gate (007) for this cut

**Question**: D-001 kept 007's `available` flag as the one admission check 020 would still run: reject a
`SnowflakeAccount` naming a region that isn't yet open for tenants. Direct follow-up feedback from the
user asked to drop this too — ignore the flag, don't gate on it at all.

**Decision**: The controller's validation phase for this cut is 006's CEL rules alone. `spec.region` is
accepted as-is and never checked against the backplane's `available` flag, nor looked up in
`backplane.Config` at all. Nothing about this needs backplane data anyway: the account module's own
region-literal transform (design.md §3.6, `CREATE ACCOUNT ... REGION='<region-from-crd>'`) already works
directly off `spec.region` with a fixed string transform (uppercase, `-` → `_`), per `specs/
012-account-module.md`'s own Security Considerations table — it was never going to touch
`backplane.Config` even under D-001. Since no other currently-registered module needs a
`*backplane.Region` either, this cut has zero callers of `backplane.Load`/`Config.Region` anywhere:
`cmd/provider/main.go` does not call `backplane.Load` at all (superseding the `bp, err :=
backplane.Load(configDir)` line D-002 originally described), and `pipeline.NewModuleContext` is called
with a `nil` `*backplane.Region` — safe, since nothing registered ever calls
`ModuleContext.BackplaneRegion()`.

**Rationale**: the user's explicit instruction, given directly as follow-up rather than through a fresh
`AskUserQuestion` round. Loading `backplane.Config` only to consult one flag on it, when nothing else in
this cut reads any other field from it, means carrying a real config-file dependency (`backplane.yaml`
must exist and parse at startup) purely to support a check the user has asked to skip entirely — keeping
the load without the gate would be a half-finished middle ground nobody asked for. Dropping it outright is
simpler, and consistent with D-002/D-004's own reasoning (don't build for a need — here, a config file
lookup — that has no consumer yet): the moment a real one exists (the region gate coming back, or 013/014
needing `regionalParameters`/`regionalAllowlist`), `backplane.Load` is one line to add back, in whichever
spec's implementation needs it.

**Affects spec section**: supersedes part of D-001 (validation phase is 006's CEL only, no region gate)
and part of D-002 (`main.go` does not call `backplane.Load`; `SetupGated`'s new parameter list carries no
`bp *backplane.Config`). Edge Cases (a tenant can name any region string at all — including one absent
from the backplane's inventory entirely, or naming a cloud the platform doesn't operate on — and 020 will
still attempt `CREATE ACCOUNT` with it; Snowflake itself is the only thing left that can reject it, with
whatever error message it happens to produce for an unrecognized region).

## Problem Areas

None outstanding that block writing `020-snowflakeaccount-controller.md` — every gap this run turned up
resolved into a decision above.

## Open Questions

- **O-001** — Whether an explicit ops runbook step (e.g. bumping `metadata.generation` via a trivial
  annotation edit on every `SnowflakeAccount`, or some bulk equivalent) is ever needed as a fallback for a
  future module whose `Observe` does *not* self-detect "never applied" (D-004) — needs input from whichever
  of 008/010/011/013–018 first lands with a module that has real state to assert, at which point that
  module's own clarification should either confirm its `Observe` handles this correctly on its own, or
  decide a runbook step is genuinely needed.

## Forward Contracts

- **008/010/011/013–015/017/018** — each, when implemented, adds itself to
  `internal/controller/snowflakeaccount`'s `pipeline.New(...)` call in the position the (now-superseded)
  worked example in `specs/scope-020-snowflakeaccount-controller.md` already lays out
  (guardrail-check → quota-check → account → parameter → network → auth → identity → quota-monitor), and
  updates `specs/020-snowflakeaccount-controller.md`'s own Public API and Dependencies sections in the
  same change (design.md §1.2). None of this needs 020 to expose any extension point beyond the literal
  module list already being a plain Go argument list (D-001).
- **008/010/011/013–018** — each module's own `Observe` must report `inSync == false` for a
  `SnowflakeAccount` it has never actually asserted its state against, rather than skipping drift
  detection unconditionally the way `specs/009-account-pipeline.md`'s illustrative parameter-module
  example does (D-004) — otherwise that module silently never applies to any account that reached `Ready`
  before it was registered.

## Propagated to lower-numbered scope notes

D-004's constraint on future `Observe` implementations was appended, self-contained, to:
`specs/scope-010-guardrail-check.md`, `specs/scope-011-quota-check.md`,
`specs/scope-013-parameter-module.md`, `specs/scope-014-network-module.md`,
`specs/scope-015-auth-module.md`, `specs/scope-017-identity-module.md`,
`specs/scope-018-quota-monitor.md`. This runs in the opposite direction from the skill's usual step 8
(which propagates to *higher*-numbered scope notes because a low-numbered spec's clarification usually
discovers something for its dependents) — here 020's clarification discovered a constraint on its own
not-yet-written *dependencies*. The same reasoning applies either way: these scope notes are still live
(none of 010/011/013–018 has been written yet) and will outlive this file, which is deleted once 020's
code lands — almost certainly before any of them are looked at again.

## References

- **Product design**: `specs/design.md` §1.2 (spec-driven development philosophy — code and spec updated
  together), §3.2 (the create flow this cut only partially implements), §3.6 (account bootstrapping —
  only the `CREATE ACCOUNT` portion is wired; the parameter/network portions of the same design.md section
  belong to 013/014, not 012), §6.3 (the deletion flow, fully wired via 019 regardless of which pipeline
  modules exist).
- **Scope note**: `specs/scope-020-snowflakeaccount-controller.md` — carries the still-valid mechanical
  decisions from prior clarifications of 009, 012, 019, plus the guardrail-check design conversation and
  the Rejected/Synced review; none of it is superseded by this file, only extended.
- **Already-written dependency specs**: `specs/006-snowflake-account-crd.md`,
  `specs/009-account-pipeline.md` (Key Concept: Overwrite Apply, Generation-Gated Re-Apply; Appendix
  Example 4), `specs/012-account-module.md`, `specs/019-deletion-request.md`.
- **Current code confirming the gap**: `cmd/provider/main.go` (no `--configDir`, no config/secrets/pool
  wiring yet), `internal/controller/yukimi.go` (`SetupGated`'s current two-parameter signature),
  `internal/config/base/base.go` (`Config`, `Load`), `internal/secrets/aws/backend.go` (`New`),
  `internal/secrets/cache.go` (`NewCachedBackend`), `internal/snowflake/pool/pool.go` (`New`).
  `internal/config/backplane/backplane.go` (`Config`, `Load`, `Region`) is unchanged and fully
  implemented, but has no caller anywhere in this cut per D-005.
