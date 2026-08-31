# Contributing

Yukimi is built spec-first. `specs/design.md` is the product blueprint (see its §1.2, *AI-First
Engineering*), and features are not coded straight from it. Instead each part of the design travels
through the pipeline below as one numbered spec, and the numbers are written and implemented one at
a time in ascending order. Most steps are driven by a project skill in `.claude/skills/`.

## The pipeline

```mermaid
flowchart TD
    design["specs/design.md<br/>product design"]
    scope["specs/scope-NNN-*.md<br/>scope note"]
    wip["specs/wip-NNN-*.md<br/>clarification record"]
    spec["specs/NNN-*.md<br/>the spec"]
    code["apis/ + internal/<br/>implementation"]

    design -->|"break-up plan · no skill yet"| scope
    scope -->|"/yukimi.clarify NNN"| wip
    wip -->|"/yukimi.specify NNN"| spec
    spec -->|"/yukimi.implement NNN"| code

    design -.->|"vibe-coded, many iterations"| design
    spec -.->|"reviewed, vibe-coded fixes"| spec
```

| Step | Skill | Model |
|---|---|---|
| Write and iterate `specs/design.md` | none — direct prompting | Opus 5 or Gemini Pro |
| Break the design into numbered scope notes | none yet — done once, via a temporary `roadmap.md` | Opus 5, *think hard* |
| Settle what the design intentionally leaves out | `/yukimi.clarify NNN` | Sonnet 5 |
| Write the spec | `/yukimi.specify NNN` | Sonnet 5 |
| Implement it | `/yukimi.implement NNN` | Sonnet 5 |

A written spec is reviewed by a human and then corrected in place — by hand or by prompting —
before implementation starts. The same is true of `design.md`, which is the product of many
iterations rather than a single pass.

## Notes

- **Run `/clear` before invoking any of the skills.** Each one spends its whole run reasoning about
  a single spec, and unrelated conversation history degrades it. The skills check for this and will
  ask you to clear rather than push on.
- **The break-up step has no skill yet.** It was a one-off run that produced the `scope-*` notes;
  turning it into a repeatable skill is a known gap.
- **Ascending order is a hard rule.** A spec may depend only on specs numbered strictly below it —
  the code for higher numbers does not exist yet. A letter suffix (`003.a`) marks a pluggable
  backend and sorts between `003` and `004`.
- **The two transient documents are deleted at different points.** `scope-NNN-*.md` goes once the
  spec is written; `wip-NNN-*.md` stays until the code lands, because it holds the research and
  worked detail the spec deliberately omits. Where the two disagree, the spec wins.
- **The spec is authoritative for its package.** Read `specs/NNN-*.md` before changing anything
  under the package it owns; `CLAUDE.md` maps numbers to packages.

## Before you open a PR

Build, test and local-cluster setup are documented in
[`docs/development/development.md`](docs/development/development.md). Make sure `make reviewable`
passes.
