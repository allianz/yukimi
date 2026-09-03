> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `013`'s intended
> *scope*, not its content. When writing `013-parameter-module.md`, the sole sources of truth
> are `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or
> discard anything below freely. Please keep this file up to date until
> `013-parameter-module.md` has been written, then delete it.

## Ordering rule (context for "Depends on" below)

Specs are written and implemented one at a time, in ascending numeric order, and **a spec may
depend only on specs numbered strictly below it** — a dependency on a higher number would be
unbuildable at the time it's written. A number may carry a letter suffix (`003.a`) marking a
pluggable backend implementing an interface owned by the bare number; a letter sorts after its
parent and strictly before the next whole number (`003` < `003.a` < `003.b` < `004`), so
"strictly below" still holds unchanged and no exception is needed.

## Roadmap's original scope notes

- Package: `internal/account/modules/parameter/`. Covers design 3.6 step 2a and Appendix B C1.
  Depends on: 005, 007, 009.
- Scope:
  - `ALTER ACCOUNT SET <parameter-name> = '<value>'`, one statement per entry in `globalParameters`
    (the org-wide baseline) and **then** one per entry in `regionalParameters`. Order matters: the
    region comes last.
  - Drift: read the parameters back and re-apply any that have diverged.
- Appendix B C1 note: the parameter set is open-ended and operator-owned, so a future Organization
  Policy must be able to pin **arbitrary** parameter names rather than a fixed allowlist.

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource
  schemas and behavior specifications this scope note was derived from.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far; follow its
  section skeleton (also given in `specs/000-template.md`).

## Raised by the 009 clarification

Recorded by `/yukimi.clarify 009`. `specs/009-account-pipeline.md` now carries the pipeline-wide rules
these points rest on — see its "Key Concept: Overwrite Apply, Generation-Gated Re-Apply".

- **Design's "read the parameters back and re-apply any that diverged" is deferred.** Drift detection
  is switched off across the whole pipeline until Snowflake ships Organization Policies (design.md
  Appendix B), because that will make this state org-owned and tenant-unchangeable, and any read-back
  built now becomes dead code. For now: **re-apply all global and regional parameters unconditionally
  on every `Apply`**, with no `SHOW PARAMETERS` and no comparison.
- **013 still implements the full module contract**, including `Observe(ctx, mc) (bool, Outcome)`,
  which returns `true, Done()` today. The method exists so the real read-back can be filled in later
  without reopening the interface — this spec should say so explicitly rather than leaving the inert
  body looking like an oversight.
- Unconditional re-application is what makes the module crash-safe: a run interrupted halfway is
  simply re-asserted in full on the next `Apply`, with no resume point to track and nothing to
  compensate for.
- **Account parameters are never pruned.** Only 014 and 015 prune, each by its own object-name prefix;
  013's parameters are re-asserted but never enumerated or dropped.
