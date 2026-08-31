# Contributing

Yukimi is built spec-first. `specs/design.md` is the product blueprint (see its §1.2, *AI-First
Engineering*), and features are not coded straight from it. Instead each part of the design travels
through the pipeline below as one numbered spec, and the numbers are written and implemented one at
a time in ascending order. Most steps are driven by a project skill in `.claude/skills/`.

## The pipeline

There are two processes. **Preparation** runs once over the whole product design and cuts it into
numbered scope notes. **Implementation** then runs once per number, in ascending order, and its
input is exactly what preparation produced.

```mermaid
flowchart TB
    %% Two stacked halves. Edges run box-to-box, never from an inner node to the outside: an edge
    %% out of an inner node makes mermaid drop that box's own `direction`, and the directions are
    %% what shape this diagram (design.md above /yukimi.plan, and the feature rows left to right).
    subgraph prep["Preparation — once"]
        direction TB
        design["design.md"] --> plan("/yukimi.plan")
    end

    %% Invisible spacer, same rank as the Preparation box. Mermaid centres every rank, so without
    %% something to its right the small Preparation box floats in the middle of the much wider
    %% implementation half. Its text is transparent — only its width does any work, so add or
    %% remove dots to nudge the box further left or right.
    spacer["...................................................."]

    prep --> impl

    %% Invisible grouping box. Rows are declared NNN-first because the last one renders on top.
    subgraph impl[" "]
        direction LR

        subgraph specn["Implement Feature NNN"]
            cn("/yukimi.clarify NNN") --> sn("/yukimi.specify NNN") --> imn("/yukimi.implement NNN")
        end

        subgraph spec2["Implement Feature 002"]
            c2("/yukimi.clarify 002") --> s2("/yukimi.specify 002") --> i2("/yukimi.implement 002")
        end

        subgraph spec1["Implement Feature 001"]
            c1("/yukimi.clarify 001") --> s1("/yukimi.specify 001") --> i1("/yukimi.implement 001")
        end

        scn["scope-NNN.md"] --> cn
        sc2["scope-002.md"] --> c2
        sc1["scope-001.md"] --> c1
    end

    classDef file fill:transparent,stroke:transparent,stroke-width:0
    classDef skill fill:#e8f4fd,stroke:#22c2ff,color:#123
    class design,scn,sc2,sc1 file
    class plan,cn,sn,imn,c2,s2,i2,c1,s1,i1 skill

    style prep fill:transparent,stroke:#9aa0a6,stroke-dasharray:2 4
    style spacer fill:transparent,stroke:transparent,color:transparent
    style impl fill:transparent,stroke:transparent
    style spec1 fill:transparent,stroke:#9aa0a6,stroke-dasharray:4 3
    style spec2 fill:transparent,stroke:#9aa0a6,stroke-dasharray:4 3
    style specn fill:transparent,stroke:#9aa0a6,stroke-dasharray:4 3
```

Bare labels are files under `specs/`, rounded boxes are skills you invoke in Claude Code. The
`scope-*.md` notes are the handover: `/yukimi.plan` writes one per feature, and each one is the entry
point of its own implementation row. A row runs to completion before the next one starts.

What each step reads and writes:

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
