# Contributing

Yukimi is built spec-first. `specs/design.md` is the product blueprint (see its §1.2, *AI-First
Engineering*), and features are not coded straight from it. Instead each part of the design travels
through the pipeline below as one numbered spec, and the numbers are written and implemented one at
a time in ascending order. Most steps are driven by a project skill in `.claude/skills/`.

## The pipeline

```mermaid
flowchart LR
    design["design.md"] --> plan("/yukimi.plan")

    %% Rows are declared NNN-first on purpose: mermaid stacks the last-declared row at the top,
    %% so this is what puts spec 001 on top of the rendered diagram.
    plan --> scn["scope-NNN.md"]
    plan --> sc2["scope-002.md"]
    plan --> sc1["scope-001.md"]

    subgraph specn["Implement Feature NNN"]
        direction LR
        cn("/yukimi.clarify NNN") --> sn("/yukimi.specify NNN") --> imn("/yukimi.implement NNN")
    end

    subgraph spec2["Implement Feature 002"]
        direction LR
        c2("/yukimi.clarify 002") --> s2("/yukimi.specify 002") --> i2("/yukimi.implement 002")
    end

    subgraph spec1["Implement Feature 001"]
        direction LR
        c1("/yukimi.clarify 001") --> s1("/yukimi.specify 001") --> i1("/yukimi.implement 001")
    end

    scn --> cn
    sc2 --> c2
    sc1 --> c1

    classDef file fill:transparent,stroke:transparent,stroke-width:0
    classDef skill fill:#e8f4fd,stroke:#22c2ff,color:#123
    class design,scn,sc2,sc1 file
    class plan,cn,sn,imn,c2,s2,i2,c1,s1,i1 skill

    style spec1 fill:transparent,stroke:#9aa0a6,stroke-dasharray:4 3
    style spec2 fill:transparent,stroke:#9aa0a6,stroke-dasharray:4 3
    style specn fill:transparent,stroke:#9aa0a6,stroke-dasharray:4 3
```

Bare labels are files under `specs/`, rounded boxes are skills you invoke in Claude Code. One row
per numbered spec, run to completion before the next row starts. What each step reads and writes:

| Step | Skill | Model | Output |
|---|---|---|---|
| Write and iterate the product design | none — direct prompting | Opus 5 or Gemini Pro | `specs/design.md` |
| Break the design into numbered scope notes | `/yukimi.plan` (not built yet) | Opus 5, *think hard* | `specs/scope-NNN-*.md` |
| Settle what the design intentionally leaves out | `/yukimi.clarify NNN` | Sonnet 5 | `specs/wip-NNN-*.md` |
| Write the spec | `/yukimi.specify NNN` | Sonnet 5 | `specs/NNN-*.md` |
| Implement it | `/yukimi.implement NNN` | Sonnet 5 | `apis/`, `internal/` |

A written spec is reviewed by a human and then corrected in place — by hand or by prompting —
before implementation starts. The same is true of `design.md`, which is the product of many
iterations rather than a single pass.

## Notes

- **Run `/clear` before invoking any of the skills.** Each one spends its whole run reasoning about
  a single spec, and unrelated conversation history degrades it. The skills check for this and will
  ask you to clear rather than push on.
- **`/yukimi.plan` does not exist yet.** The diagram shows the intended shape; today's `scope-*`
  notes came out of a single one-off run over a temporary `roadmap.md`. Turning that into a
  repeatable skill is a known gap.
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
