# Contributing

Yukimi is built spec-first. `specs/design.md` is the product blueprint (see its §1.2, *AI-First
Engineering*), and features are not coded straight from it. Instead each part of the design travels
through the pipeline below as one numbered spec, and the numbers are written and implemented one at
a time in ascending order. Most steps are driven by a project skill in `.claude/skills/`.

## The AI coding process

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

    %% Left alignment: mermaid centres every rank, so the Preparation box needs something to its
    %% right to be pushed left. `spacer` is a transparent node — only its width does any work —
    %% and the invisible `~~~` link is what pins it to the same rank as the box instead of letting
    %% it float off on its own. Add or remove dots to nudge Preparation further left or right.
    spacer["........................................................................................................................................................................................."]

    %% `anchor` is a second invisible node, one rank below Preparation and nothing else — that is
    %% what makes the arrow drop straight down from the box's centre. It must NOT link to `impl`:
    %% with that link, mermaid balances the anchor between Preparation and the wide box's centre
    %% and the arrow leans right again. `mid` carries the invisible route down to `impl` instead,
    %% which also gives the anchor a rank of its own so the arrow stays short.
    prep --> anchor[" "]
    spacer ~~~ mid[" "]
    mid ~~~ impl

    %% Invisible grouping box. The rendered row order is a right-rotation of the declaration order:
    %% declaring 001, 002, NNN comes out as NNN, 001, 002. Declaring 002, NNN, 001 is therefore what
    %% renders 001 on top and NNN at the bottom. Verified by rendering, so change it with care.
    subgraph impl[" "]
        direction LR

        subgraph spec2["Implement Feature 002"]
            c2("/yukimi.clarify 002") --> s2("/yukimi.specify 002") --> sf2["spec-002.md"] --> i2("/yukimi.implement 002")
        end

        subgraph specn["Implement Feature NNN"]
            cn("/yukimi.clarify NNN") --> sn("/yukimi.specify NNN") --> sfn["spec-NNN.md"] --> imn("/yukimi.implement NNN")
        end

        subgraph spec1["Implement Feature 001"]
            c1("/yukimi.clarify 001") --> s1("/yukimi.specify 001") --> sf1["spec-001.md"] --> i1("/yukimi.implement 001")
        end

        sc1["scope-001.md"] --> c1
        sc2["scope-002.md"] --> c2
        scn["scope-NNN.md"] --> cn
    end

    classDef file fill:transparent,stroke:transparent,stroke-width:0
    classDef skill fill:#e8f4fd,stroke:#22c2ff,color:#123
    class design,scn,sc2,sc1,sfn,sf2,sf1 file
    class plan,cn,sn,imn,c2,s2,i2,c1,s1,i1 skill

    style prep fill:transparent,stroke:#9aa0a6,stroke-dasharray:2 4
    style spacer fill:transparent,stroke:transparent,color:transparent
    style anchor fill:transparent,stroke:transparent,color:transparent
    style mid fill:transparent,stroke:transparent,color:transparent
    style impl fill:transparent,stroke:transparent
    style spec1 fill:transparent,stroke:#9aa0a6,stroke-dasharray:4 3
    style spec2 fill:transparent,stroke:#9aa0a6,stroke-dasharray:4 3
    style specn fill:transparent,stroke:#9aa0a6,stroke-dasharray:4 3
```


What each step reads and writes:

| Step | Skill | Model | Output |
|---|---|---|---|
| Write and iterate the product design | none — direct prompting | Opus 5 or Gemini Pro | `specs/design.md` |
| Break the design into numbered scope notes | `/yukimi.plan` (not built yet) | Opus 5, *think hard* | `specs/scope-NNN-*.md` |
| Settle what the design intentionally leaves out | `/yukimi.clarify NNN` | Sonnet 5 | `specs/wip-NNN-*.md` |
| Write the spec | `/yukimi.specify NNN` | Sonnet 5 | `specs/NNN-*.md` |
| Implement it | `/yukimi.implement NNN` | Sonnet 5 | `apis/`, `internal/` |

A written spec is reviewed by a human and then corrected in place — by hand or by prompting —
before implementation starts.

## Notes

- **Run `/clear` before invoking any of the skills.** Each one spends its whole run reasoning about
  a single spec, and unrelated conversation history degrades it. The skills check for this and will
  ask you to clear rather than push on.
- **Ascending order is a hard rule.** A spec may depend only on specs numbered strictly below it —
  the code for higher numbers does not exist yet. A letter suffix (`003.a`) marks a pluggable
  backend and sorts between `003` and `004`.
  worked detail the spec deliberately omits. Where the two disagree, the spec wins.
- **The spec is authoritative for its package.** Read `specs/NNN-*.md` before changing anything
  under the package it owns; 


