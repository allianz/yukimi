> **Scope context only — not a specification.** This file was split out of the temporary
> `roadmap.md` planning document used to work out how `specs/design.md` should be decomposed
> into numbered specs. It exists only to give a starting-point idea of spec `011`'s intended
> *scope*, not its content. When writing `011-parameter-module.md`, the sole sources of truth are
> `specs/design.md` and the prompt given at spec-writing time — rework, restructure, or discard
> anything below freely. This file does not need to be kept up to date, and should be deleted
> once `011-parameter-module.md` has been written.

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
