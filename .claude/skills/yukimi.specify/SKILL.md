---
name: yukimi.specify
description: Write a numbered yukimi spec from its clarified inputs — enters plan mode, reads specs/design.md, specs/scope-NNN-*.md and specs/wip-NNN-*.md, drafts specs/NNN-<slug>.md following specs/000-template.md, then deletes the scope note. No implementation.
argument-hint: "<spec number, e.g. 009> — run /clear first"
---

# Write a numbered spec from its clarified inputs

Argument: the spec number, e.g. `/yukimi.specify 009` or `/yukimi.specify 003.a`. Accept bare numbers (`9`), zero-padded (`009`), or letter-suffixed (`003.a`) forms; normalize to the zero-padded form used in filenames.

`specs/scope-NNN-*.md` bounds scope and dependencies but is thin on content. `specs/wip-NNN-*.md` is where `/yukimi.clarify` recorded the actual decisions (`D-xxx`) once the scope note's gaps were resolved. This skill is the step in between "clarified" and "implementable": turn those decisions into the numbered spec itself, following `specs/000-template.md`'s shape. **The output is `specs/NNN-<slug>.md` — a contract and a mental model, not a restatement of the wip record's research.** No code, no scaffolding, no implementation planning — that's `/yukimi.implement`'s job.

## 1. Enter plan mode — before anything else

Call `EnterPlanMode` as your very first action, before locating any file, before any `ls`/`Read`/`grep`. It's read-only, so there's no cost to calling it early, and calling it first removes any ordering ambiguity about when "research" starts. Do not batch it with other tool calls — it must be the sole call in your first turn.

## 2. Check the context is clean

This skill spends its whole run drafting one spec, so unrelated conversation history actively degrades it. You cannot clear context yourself — `/clear` is a command the user types.

So: if the conversation already contains substantial unrelated work, say so plainly, ask the user to run `/clear` and re-invoke `/yukimi.specify <NNN>`, and stop. Don't push on regardless. A conversation that holds only earlier rounds of this same skill run (e.g. after an `AskUserQuestion` round in step 5) is fine to continue in.

## 3. Resolve the inputs

- Require `specs/scope-<NNN[.letter]>-*.md`. If no argument was given, or no scope note matches, ask the user for the number.
- Require `specs/wip-<NNN[.letter]>-*.md`. If it doesn't exist, stop and tell the user to run `/yukimi.clarify <NNN>` first — do not draft a spec from the scope note alone. The wip record is where the real content of this spec lives; without it there is nothing to write beyond a restatement of the scope note.
- If `specs/<NNN>-*.md` already exists, the spec has already been written. Say so, and ask the user (via `AskUserQuestion`) whether they want a full rewrite, a targeted revision that incorporates new wip decisions since it was last written, or a different number entirely.
- Cross-check the number against the package table in `CLAUDE.md` and note the target package path(s).

## 4. Research

- Read the scope note end to end. Its "Covers design X.Y" line tells you which `specs/design.md` sections to read; read those in full, plus §7 (condition model) and Appendix A (Open TODOs) / Appendix B (Organization Policy Requirements) wherever they bear on this number.
- Read the wip record end to end. Every `D-xxx` entry is a decision this spec must contain — its "Affects spec section" line tells you where. Note any `P-xxx` (Problem Areas) or `O-xxx` (Open Questions) still listed; check whether each actually bears on what you are about to write, or is scoped to a different, higher-numbered spec.
- Read `specs/000-template.md`. It is the shape you are filling in.
- Read two or three already-written specs (`specs/<NNN>-*.md`) for precedent: how they phrased Key Concepts, how they classified errors, how much detail Edge Cases and Appendix examples carry, how terse or expansive the Overview is.
- Read the real code of every spec listed under the scope note's dependencies. Existing code is authoritative once it exists, even where the dependency's spec text is less precise.
- Read any `specs/notes-*.md` relevant to this number — verified vendor references the wip record may already draw on.

Never edit `specs/design.md`. Its gaps are intentional; the wip record already resolved what mattered for this spec, and this step only turns those resolutions into the spec document.

## 5. Handle leftover gaps

If step 4 turned up a `P-xxx` or `O-xxx` from the wip record that bears directly on this spec — not one properly scoped to a later number — don't silently guess and don't necessarily send the user back for a full `/yukimi.clarify` rerun over one or two loose ends. Ask inline, using the same discipline as `/yukimi.clarify` step 6:

- Brief in message text immediately before the `AskUserQuestion` call: where the gap comes from (the `P-xxx`/`O-xxx` title, the design.md section), what's missing, why it has to be settled before this section of the spec can be written, what each option means in practice, and any verified-vs-unconfirmed facts bounding it.
- Make the dialog question itself self-contained (name the subject, put the consequence in each option's description), with the recommended option first.

If the gap is broad enough that it needs real research or would reopen several decisions at once, that's a sign it belongs back in `/yukimi.clarify`, not here — say so and stop rather than forcing a shallow answer.

## 6. Draft the spec

Write the full content of `specs/<NNN>-<slug>.md` following `specs/000-template.md`'s section order: Overview, Scope, Key Concept(s), Public API, Schema Specification (if the resource has one), Project Structure, Error Classification, Edge Cases, Dependencies, Integration Points, Success Criteria, Security Considerations, Performance Considerations, References, Appendix: Usage Examples.

- Every `D-xxx` in the wip record should land somewhere in this structure — use its "Affects spec section" note as the starting point, but use judgment where a decision spans more than one section.
- State the contract and the mental model; leave the supporting detail (the full enumeration, the worked example, the rejected alternatives) in the wip record rather than reproducing it here — that's why the wip record stays alive until the code lands.
- This step produces prose, schema, and example YAML/Go signatures for the spec document only. Do not scaffold files, run `make provider.addtype`, or write any implementation code.

## 7. Exit plan mode, then write the file

Plan mode forbids writing to `specs/`, so call `ExitPlanMode` with the drafted spec content laid out for review. Write `specs/<NNN>-<slug>.md` only after approval.

## 8. Delete the scope note

Per `CLAUDE.md`'s convention, `scope-<NNN>-<slug>.md` is deleted once `<NNN>-<slug>.md` is written — delete it now. Leave `specs/wip-<NNN>-*.md` in place: it's kept until the code for `<NNN>` is implemented, and is deleted by `/yukimi.implement <NNN>`'s wrap-up step, not this one.

## 9. Wrap up

Report: which `D-xxx` decisions were incorporated and where, any `P-xxx`/`O-xxx` from the wip record that didn't bear on this spec and so are still open there (so they aren't silently lost), and that the scope note was deleted. Don't commit or push unless the user explicitly asks.

The next step is `/yukimi.implement <NNN>`.
