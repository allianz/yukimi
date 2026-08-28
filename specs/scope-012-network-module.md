> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `012`'s intended
> *scope*, not its content. When writing `012-network-module.md`, the sole sources of truth
> are `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or
> discard anything below freely. Please keep this file up to date until
> `012-network-module.md` has been written, then delete it.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003.a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003.a` < `003.b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Package: `internal/account/modules/network/`. Covers design 3.6 steps 2b and 2c, 3.8, and
  Appendix B N1–N3. Depends on: 005, 007, 009.
- Deliberately a single spec: `PLATFORM_ACCOUNT_POLICY` is *created* in 3.6/2c and *appended to* in
  3.8, so splitting would place one spec's object under another spec's mutation. The `CUSTOM_` prefix
  convention that keeps the two rule sets apart must also be defined in one place.
- Scope — the baseline (3.6):
  - One `CREATE NETWORK RULE <connection-name> TYPE=<type-from-inventory> MODE=INGRESS
    VALUE_LIST=(<vpceId and/or resolved allowedIPs>)` per `regionalAllowlist` entry, named by the
    bare connection. Use the entry's `allowedIPs` if given, otherwise inherit the connection's full
    `maxCidrs`.
  - `CREATE NETWORK POLICY PLATFORM_ACCOUNT_POLICY ALLOWED_NETWORK_RULE_LIST=(<all connections>)`,
    followed by `ALTER ACCOUNT SET NETWORK_POLICY='PLATFORM_ACCOUNT_POLICY'`.
- Scope — custom rules (3.8):
  - `accountWide` entries become `CREATE NETWORK RULE CUSTOM_<connection>` followed by
    `ALTER NETWORK POLICY PLATFORM_ACCOUNT_POLICY ADD ALLOWED_NETWORK_RULE_LIST=(…)`. The `CUSTOM_`
    prefix keeps them distinct from the `regionalAllowlist` rules, which are named by bare
    connection.
  - `serviceUsers` entries become one rule per entry in that user's list, collected into a **single
    dedicated policy per user**, followed by
    `ALTER USER '<service-user>' SET NETWORK_POLICY='<user-policy>'`. One policy per user, because a
    user can only have one active `NETWORK_POLICY` at a time and because a user-scoped policy
    **fully overrides** the account-level default — so each user's policy is built exclusively from
    the connections they were explicitly granted. Service users only; never human users.
  - **Deny-by-default**: a service user with no `customNetworkRules.serviceUsers` entry gets an empty
    policy, completely blocking login. Securing service users is the highest-value part of this
    module — their long-lived credentials are the platform's biggest risk, so they must be given
    tight explicit ingress paths rather than the broad ranges intended for humans.
  - **Strict containment**: every `allowedIPs` entry must fall entirely within that connection's
    `maxCidrs`, using 007's helper. Anything broader or outside is rejected.
  - An entry under `customNetworkRules` with no `allowedIPs` means guardrails' `"full"` rule
    already validated it (008) — resolve it here by inheriting the connection's full range from
    the region's inventory (007). This module does not consult guardrails directly: it infers the
    case purely from the CRD's shape, since guardrails would have rejected the CRD otherwise.
  - **No duplicate connections**: a connection may appear at most once per user list and at most once
    under `accountWide`. A repeat within a scope is a validation error, not a silent merge.
  - Rejection behavior: because custom rules run **after** bootstrapping, the account already exists.
    It keeps the 3.6 baseline policy, the offending rule is not created, and the failure is reported
    on `Synced` until the tenant fixes the CRD. Later modules still run.
- Appendix B N1–N3 note: today's `ALTER ACCOUNT SET NETWORK_POLICY` and
  `ALTER USER … SET NETWORK_POLICY` are tenant-alterable. Organization Policies will make the binding
  org-owned and make the empty default native, closing the window between `CREATE USER` and policy
  attachment.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).

## Raised by the 009 clarification

Recorded by `/yukimi.clarify 009`; see `specs/wip-009-account-pipeline.md` for the full reasoning.

- **Updates are a blunt overwrite, not a diff.** Re-assert the full desired state on every `Apply`
  (`CREATE OR REPLACE` plus re-bind), with no read-back of existing entries to compare against.
- **What the CRD no longer lists is pruned.** Enumerate by the `CUSTOM_` prefix
  (`SHOW NETWORK RULES LIKE 'CUSTOM_%'`, plus the matching `SHOW NETWORK POLICIES` for the per-user
  policies), set-difference against the CRD, then unbind before dropping:
  `ALTER USER … UNSET NETWORK_POLICY` for a user policy, or
  `ALTER NETWORK POLICY PLATFORM_ACCOUNT_POLICY REMOVE ALLOWED_NETWORK_RULE_LIST=(…)` for an
  `accountWide` rule, and only then `DROP`. The order is the point — a rule dropped while a live policy
  still references it is the failure mode to avoid.
- **Removing a `serviceUsers` entry must leave that user no ingress the CRD does not grant.** Spell the
  unbind-then-drop order out here: a policy left bound to a user is exactly the security gap pruning
  closes.
- **Baseline `regionalAllowlist` rules are never pruned** — they are named by bare connection, and only
  the `CUSTOM_` prefix is enumerated. That prefix is now load-bearing for correctness, not just
  readability.
- **Drift is still neither detected nor repaired**, because Organization Policies will take away the
  tenant's ability to create it: a `CUSTOM_` rule present in the CRD but missing in Snowflake is simply
  recreated by the overwrite, and nothing outside the `CUSTOM_` prefix is inspected at all.
- **A rejected entry never stops the run.** 012 returning `Rejected(userErr)` leaves the account on
  its baseline and the remaining modules still execute (wip-009 D-008, design 3.8/3.9). Only 010 ever
  aborts the run; 012 never calls `.Aborting()` on its own outcome.
- 012 implements `Observe(ctx, mc) (bool, Outcome)` returning `true, Done()` today (wip-009 D-002).
