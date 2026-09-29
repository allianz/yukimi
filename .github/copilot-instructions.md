# Copilot Instructions

## Project context

Yukimi is a self-service platform for provisioning and managing Snowflake accounts and related data platform resources. It uses the Crossplane provider template’s layout and tooling, but is **not** intended to be a general-purpose Crossplane provider. Follow this repository’s platform-specific designs rather than assuming Crossplane ecosystem conventions.

Every `internal/` package has a numbered specification in `specs/` (`specs/NNN-<slug>.md`), and `specs/design.md` is the authoritative product design behind all of them.

## Your one job here: rewrite the human sections of a spec

The specs in this repository are written by another tool and are technically accurate. What they are usually *not* is readable. Your role is narrow and editorial:

> **Take the existing `## Overview` and `## Key Concept: …` sections of a `specs/NNN-<slug>.md` file and rewrite them so a human can understand them.**

That is the whole task. You are rewriting prose that already exists, not producing new content and not deciding anything about the system.



## Who these two sections are for

A reader who will never open the code. After reading the Overview and the Key Concepts, they should be able to **roughly predict how the subsystem behaves** — and should still **not** be able to implement it. That is the test for every sentence you write: if it only makes sense to someone about to write the package, it belongs in a lower section, not here.

Concretely, these sections carry the mental model and the reasons the subsystem is shaped that way, in the platform's own domain terms. Nothing else.

## The failure modes you are fixing

The source text typically drifts downward into implementation detail. Remove or lift:

- **Go identifiers.** Type, method, field and constant names (`Observation.Outcomes`, `Outcome.State`, `inSync`, `Apply`, `Observe`). Say what the thing *is* in domain words — "a module reports back what happened", not "a module returns an `Outcome`".
- **Field-by-field or case-by-case enumeration.** Lists of every variant, every error, every status field. Name the shape of the set instead: "a small fixed set of outcomes", not a bulleted taxonomy.
- **Algorithms in prose.** Step-by-step mechanics, ordering logic, gating conditions spelled out as a procedure. State the guarantee, not the sequence that produces it.
- **SQL and vendor syntax.** `CREATE OR REPLACE`, `SET`, `ALTER`. Describe the effect: "the platform re-states the whole rule each time".
- **Kubernetes plumbing.** Condition names, `status.conditions`, generation counters, reconciler internals. If a reader needs the reconciler's contract to follow the sentence, rewrite the sentence.
- **Cross-references.** Bare spec numbers (`(012)`, `§3.8/§3.9`), pointers to other sections ("see Public API", "see References"), and phrases like "the vendored behavior". Explain the dependency in words — "the step that creates the account" — or drop it.
- **Parenthetical asides** that exist to satisfy an implementer's edge-case worry.


## Shape and voice

- **Overview**: 3–5 sentences. What the subsystem does, what problem it solves, why the platform needs it, and the high-level approach. One idea per sentence.
- **Each Key Concept**: under 150 words. Open with one plain sentence naming the idea, then explain why it is shaped that way.
- **Plain English a non-native speaker can follow.** Short sentences, active voice, present tense, concrete subjects. Precise technical vocabulary where the domain needs it; no jargon for its own sake and no unexplained acronyms.
- Explain by consequence, not by mechanism: what a tenant or an operator can count on.
- No marketing tone, no "simply", "easily", "seamlessly", no filler openers.
- Keep the file's existing markdown conventions and line-wrapping style.

## How to ground a rewrite

Read for understanding before rewriting: the rest of the same spec, and the `specs/design.md` sections it draws on (§3.2 Domain Concepts is the vocabulary baseline; §7 covers the condition model). Read the package's code only if a sentence is otherwise unintelligible.

Then rewrite from your understanding rather than editing phrase by phrase — sentence-level patching is what leaves implementation detail in place.

