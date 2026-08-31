> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `013`'s intended
> *scope*, not its content. When writing `013-auth-module.md`, the sole sources of truth are
> `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or
> discard anything below freely. Please keep this file up to date until `013-auth-module.md`
> has been written, then delete it.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003.a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003.a` < `003.b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Package: `internal/account/modules/auth/`. Covers the design 3.6 authentication policies, 3.9, and
  Appendix B A1/A2. Depends on: 005, 009.
- Scope:
  - Create the **SSO-only baseline** authentication policy, with `AUTHENTICATION_METHODS` restricted
    to `SAML`/`OAUTH`, and bind it to the account. SSO is the only login method for human users; it
    is integrated globally, so this is a per-account binding of a global capability.
  - Create the **three fixed exception policies** — `PLATFORM_AUTH_KEYPAIR`, `PLATFORM_AUTH_PAT` and
    `PLATFORM_AUTH_KEYPAIR_PAT`. The two flags yield three method combinations, so three policies
    cover every possible exception; the both-methods case needs its own policy because a user can
    hold only one `AUTHENTICATION_POLICY` at a time. They are created here, during bootstrapping,
    rather than on demand.
  - Bind users per `customAuthRules.exceptions` with
    `ALTER USER '<user>' SET AUTHENTICATION_POLICY=<matching-fixed-policy>`, overriding the
    account-level baseline for that user only.
  - Validation: an entry naming **neither** method is a validation error. `reason` is recorded in the
    CRD for audit and is deliberately **not** carried into Snowflake. Service users are out of scope
    here — they are governed by their own network policies (012).
  - Rejection behavior mirrors 012: a rejected entry leaves the user on the SSO-only baseline and is
    reported on `Synced`.
- Appendix B A1/A2 note: today an account admin can unset either binding, or alter a policy to
  re-admit a method. Organization Policies will make both the baseline and the per-user overrides
  org-owned.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).

## Raised by the 009 clarification

Recorded by `/yukimi.clarify 009`. `specs/009-account-pipeline.md` now carries the pipeline-wide rules
these points rest on — see its "Key Concept: Overwrite Apply, Generation-Gated Re-Apply" for the
overwrite-and-prune contract, and "Key Concept: Sequential Modules, One Abort Signal" for who may stop
a run.

- **Same overwrite-and-prune contract as 012.** Re-assert auth policies and bindings in full on every
  `Apply`, with no read-back of existing entries to compare against, **and drop the per-user policies
  the CRD no longer lists** — unbinding the user first, so the account falls back to the SSO-only
  baseline rather than keeping an authentication path the CRD no longer grants.
- **Name the enumeration handle.** Pruning works by listing the policies this module owns and taking a
  set difference, so 013 needs its own naming convention for per-user auth policies (012 has `CUSTOM_`;
  013 has nothing sketched yet). Deciding that name is part of writing 013.
- **Drift is still neither detected nor repaired**, because Organization Policies will take away the
  tenant's ability to create it.
- **A rejected exception never stops the run.** Only 010 ever aborts the run; 013 never calls
  `.Aborting()` on its own outcome. Design 3.8/3.9's "a rejected entry leaves the account on its
  baseline" only holds if the modules after the rejecting one still get to run.
- **013 has no guardrail dependency.** Design.md's guardrails section (3.3) does not constrain
  `customAuthRules`, so exceptions are validated only against 006's CRD schema and design 3.9's own
  rule (an entry naming neither method is a validation error).
- 013 implements `Observe(ctx, mc) (bool, Outcome)` returning `true, Done()` today, for the same reason
  011 does: the method exists so a real read-back can be added later without reopening the interface.
