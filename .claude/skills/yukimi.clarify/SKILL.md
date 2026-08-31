---
name: yukimi.clarify
description: Clarify an unwritten yukimi spec before writing it — enters plan mode, reads specs/design.md and specs/scope-NNN-*.md, hunts for underspecified areas and unstated mappings, resolves them with the user, then records the outcome in specs/wip-NNN-<slug>.md.
argument-hint: "<spec number, e.g. 009> — run /clear first"
---

# Clarify a spec before writing it

Argument: the spec number, e.g. `/yukimi.clarify 009` or `/yukimi.clarify 003.a`. Accept bare numbers (`9`), zero-padded (`009`), or letter-suffixed (`003.a`) forms; normalize to the zero-padded form used in filenames.

`specs/design.md` is deliberately incomplete — it explains only what is needed to understand the product, and keeping it short means whole dimensions are left out on purpose. The `scope-NNN-*.md` notes bound scope and dependencies well but are thin on content. This skill is the step between "scope note exists" and "spec can be written": find what is underspecified or genuinely uncertain, settle what can be settled with the user, and record the rest as named problem areas. **The output is a clarification record, not a draft spec** — `specs/wip-NNN-<slug>.md`.

## 1. Enter plan mode — before anything else

Call `EnterPlanMode` as your very first action, before locating any file, before any `ls`/`Read`/`grep`. It's read-only, so there's no cost to calling it early, and calling it first removes any ordering ambiguity about when "research" starts. Do not batch it with other tool calls — it must be the sole call in your first turn.

## 2. Check the context is clean

This skill spends its whole run reasoning about one spec, so unrelated conversation history actively degrades it. You cannot clear context yourself — `/clear` is a command the user types.

So: if the conversation already contains substantial unrelated work, say so plainly, ask the user to run `/clear` and re-invoke `/yukimi.clarify <NNN>`, and stop. Don't push on regardless. A conversation that holds only earlier rounds of this same skill is fine to continue in — that's the resumable case in step 3.

## 3. Resolve the inputs

- Require `specs/scope-<NNN[.letter]>-*.md`. If no argument was given, or no scope note matches, ask the user for the number.
- If `specs/<NNN>-*.md` already exists, the spec has been written and its scope note should have been deleted; its `wip-<NNN>-*.md` normally still exists until the code lands. Say so and ask whether the user wants to clarify a different number, or re-clarify this one because the written spec turned out to have gaps. When re-clarifying a written spec, the spec itself is an input alongside `specs/design.md`, and new decisions append to the existing wip record.
- If `specs/wip-<NNN>-*.md` already exists, **read it first and resume**: skip everything already recorded as a resolved decision, look for problem areas that later reasoning can now close, and append rather than restart. Never silently overwrite prior reasoning.
- Cross-check the number against the package table in `CLAUDE.md` and note the target package path(s).

## 4. Research

- Read the scope note end to end. Its "Covers design X.Y" line tells you which `specs/design.md` sections to read; read those in full, plus §7 (condition model) and Appendix A (Open TODOs) / Appendix B (Organization Policy Requirements) wherever they bear on this number.
- Read `specs/000-template.md`. It is the *destination* shape — a decision is only useful here if it can be written into one of those sections, so use it to keep the clarification actionable.
- Read the two or three highest already-written specs (`specs/<NNN>-*.md`) for precedent: how they phrased Key Concepts, how they classified errors, how much detail Edge Cases carry.
- Read the real code of every spec listed under the scope note's dependencies. Existing code is authoritative once it exists, even where the dependency's spec text is less precise.
- Read any `specs/notes-*.md`. These are verified vendor references shared across specs; they may already answer a question you were about to ask.
- Note any higher-numbered `scope-MMM-*.md` this number's decisions will constrain — you'll append to them in step 8.

## 5. Scan for gaps — the core of this skill

You are hunting for **missing dimensions and unstated mappings**, not for holes in the template. A useful finding is "this whole aspect is absent" or "these two artifacts are never connected", not "the Public API section lacks a field comment". Work through these probes, and for each one ask whether design.md plus the scope note actually determine the answer or merely appear to:

- **Lifecycle completeness.** Creation is nearly always specified; observation, drift detection, update/reconvergence and teardown usually are not. For each thing this subsystem manages: how is actual state read back from the external system, what counts as drift, what is repaired versus only reported, and what is never touched at all?
- **Mapping onto the controller contract.** How does this subsystem's API land on `Observe`/`Create`/`Update`/`Delete` and on `ResourceExists`/`ResourceUpToDate`? Who decides "up to date"? What runs on every reconcile versus once?
- **Ownership boundaries.** What does the platform own and enforce, versus what may a tenant's account admin change afterwards without being reverted? (design.md's `preset`-versus-`constraints` distinction is one instance of this; look for others.) Drift on state the platform does not own must be ignored deliberately, not by accident.
- **Idempotency, ordering and partial failure.** What happens on re-run after a crash mid-sequence? What is left behind? Does order matter? Is each step safe to repeat?
- **Immutability and unsupported change.** What happens when an immutable field changes? What when Snowflake cannot `ALTER` the thing in place and it would need recreation — is that rejected, or done?
- **Status and condition reporting.** What surfaces in `status`, and how do partial outcomes aggregate into conditions (design.md §7)?
- **Input reload.** When a mounted ConfigMap changes, or a cache TTL expires, what is re-evaluated and when?
- **Error classification.** Which failures are user errors the tenant can fix by editing their CRD, and which are system errors? A gap here is usually a gap in understanding who owns the input.
- **Concurrency.** Parallel reconciles across accounts, the shared org-admin connection, rate limits.
- **Forward contracts.** What does this spec assume a higher-numbered spec will do? It cannot import them, so the assumption has to be recorded somewhere or it is lost.
- **Undefined vocabulary.** Terms design.md uses repeatedly without ever defining.

Two rules for handling what you find:

- **Cross-cutting topics.** When a probe turns up a topic that affects several later specs (drift detection is the standard example), and `NNN` is the lowest number that needs it: settle the **generic** rule here *and* the part specific to `NNN`. Do **not** try to settle it for the later specs — handling genuinely differs per module, so each must clarify the topic for itself. What goes into the higher scope notes in step 8 is a marker that the topic needs clarifying there, not a copy of this answer.
- **Vendor-dependent points.** Where the answer hinges on what Snowflake actually permits, look it up (context7, Snowflake docs) *before* asking the user, and label every claim **Verified** (with its source) or **Unconfirmed** — the same discipline `specs/notes-*.md` uses. Don't ask the user to guess something the documentation states.

Never edit `specs/design.md`. Its gaps are intentional and it must stay short; the clarification record is where the omitted detail gets decided.

## 6. Ask the user

Use `AskUserQuestion` in rounds — at most 4 questions per round, 2–4 concrete candidate answers each, the recommended one first with a stated reason. Where a question is really about the shape of something, use option previews.

By this point you have read design.md, the scope note, the dependency code and the vendor docs; the user has not. The standing failure mode of this step is a question that is only answerable from inside that context — terse, correct, and unanswerable. **Every round therefore has two parts: a briefing in message text, then the questions.** The dialog's own fields are far too small to carry context (a 12-character header, labels of a few words), so the briefing is where it has to go.

### Brief before you ask

Write plain message text immediately before the `AskUserQuestion` call — one short block per question, in the same order as the questions, each named so the user can match it to the dialog. Per question, cover:

- **Where it comes from** — the `specs/design.md` section, scope-note line, or code path that raised it, so the user can go and look.
- **What is missing** — the gap in one or two sentences, in the platform's own vocabulary (accounts, modules, tenants, org admin), not in template vocabulary.
- **Why it has to be decided now** — what cannot be written into `<NNN>-<slug>.md` until it is settled.
- **What each option means in practice** — one line per option, stated as the consequence for a tenant or an operator, not as a restatement of the label.
- **The facts that bound it** — anything you looked up, with its source, marked **Verified** or **Unconfirmed**, and explicitly which options Snowflake or an already-written spec rules out. Never make the answer depend on something the user is expected to recall about Snowflake.

Aim for roughly five to ten lines per question. If a question needs much more than that to be answerable, that is the signal it is not ready to ask: either research it further, or record it as a problem area in step 7 instead.

The briefing also audits the question. If writing "where it comes from" and "the facts that bound it" leaves you with a single surviving option, you had research left to do, not a question — do the research and drop it from the round.

### Then make each question self-contained

Assume the dialog is all the user sees — they may return to it after the briefing has scrolled away. So:

- Name the subject in the question text rather than relying on the surrounding turn: "When a tenant narrows an account parameter the platform also enforces, which value wins?", not "Which should win?".
- Put the consequence in each option's `description`, and end it with the trade-off that option accepts. A description that only rephrases its label wastes the one field with room to explain.
- Give the recommended option first, labelled `(Recommended)`, and put the reason for the recommendation in its description.

On a resumed run, don't re-brief ground the earlier rounds already covered — refer to the decision by its `D-xxx` title and brief only what is new.

### Keep the questions high-level

A good question names a missing dimension or an unconnected pair of artifacts and offers real, mutually exclusive positions on it. A bad question asks the user to fill in a field name or pick a log level.

Not every gap reduces to a multiple choice. Where the space is too open, or the trade-offs need to be understood before any option makes sense, do **not** force options — record it as a problem area in step 7 and say why you left it open. But never resolve a real ambiguity silently, and never present your own guess as settled.

After each round, play back in one line per answer what you took the decision to be and what it now implies — that is the user's chance to catch a misread before it hardens into a `D-xxx`. Pay particular attention where the user chose "Other" or attached notes: restate that answer in your own words rather than assuming it maps onto an option you offered.

Loop rounds until nothing material is left that the user can decide in this session.

## 7. Exit plan mode, then write the record

Plan mode forbids writing to `specs/`, so call `ExitPlanMode` with the resolved decisions laid out for review. Write the files only after approval.

Write `specs/wip-<NNN>-<slug>.md`, using the same slug as the scope note. Follow the repo's convention for transient documents (`scope-*`, `notes-*`): open with a status blockquote declaring what the file is, what its sources of truth are, and when it gets deleted.

The record outlives the spec-writing step — it is kept until the code for `<NNN>` is implemented. So write it to be usable by whoever implements the package: keep the supporting detail behind each decision (the verified vendor behaviour, the full enumeration, the worked example, the alternatives rejected and why) here, in full. The spec will state the contract and the mental model and leave that detail out; this is where it stays available.

```markdown
> **Clarification record — not a specification.** Produced by `/yukimi.clarify <NNN>` to settle what
> `specs/design.md` intentionally leaves out and `specs/scope-<NNN>-<slug>.md` does not cover. It
> records decisions, not product design — `specs/design.md` remains authoritative and always wins,
> and once `<NNN>-<slug>.md` is written the spec wins over this file too.
> Read it together with the scope note when writing `<NNN>-<slug>.md` (delete the scope note then),
> keep it as supporting detail while `<NNN>` is implemented, and delete it once the code has landed.

## Clarification runs

- Run 1 — covered: {probes/topics}. Left open: {P-xxx, O-xxx}.

## Resolved Decisions

### D-001 — {one-line title}

**Question**: {the gap, stated as the question it raised}
**Decision**: {what was settled}
**Rationale**: {why — including which alternatives were rejected and why}
**Affects spec section**: {which 000-template.md section this lands in}

## Problem Areas

### P-001 — {one-line title}

**What is uncertain**: {the gap}
**Why it is hard**: {the tension that keeps it open}
**Options and trade-offs**: {each candidate with what it costs}
**Current lean**: {if any — clearly marked as not yet decided}
**What would unblock it**: {the missing fact, decision or experiment}

## Open Questions

- **O-001** — {question} — needs input from {ops / ISO / vendor / a later spec}.

## Forward Contracts

- **{Higher spec number}** — {the obligation this spec places on it, and why it cannot be settled here}.

## References
```

Number entries `D-001`, `P-001`, `O-001` and continue the numbering on a resumed run rather than restarting it. When a later run closes a problem area, keep the `P-xxx` entry and mark it resolved with a pointer to the `D-xxx` that closed it — the reasoning is worth keeping.

## 8. Propagate what belongs to other specs

For every finding that really belongs to a higher-numbered spec — a forward contract, or a cross-cutting topic that each later spec must clarify for itself — append it to that spec's `specs/scope-<MMM>-*.md` under a clearly attributed heading, e.g. `## Raised by the <NNN> clarification`. The scope notes are kept up to date until their spec is written, so this is where such findings survive; `wip-<NNN>-*.md` is deleted once the code for `<NNN>` is implemented and would take them with it.

**Write the appended content to survive that deletion — it must be self-contained.** Assume `wip-<NNN>-*.md` is gone by the time anyone writes `<MMM>`, so never point at it:

- No `see specs/wip-<NNN>-<slug>.md` pointers, and no citations of its entry numbers (`wip-009 D-010`, `(wip-010 D-003, D-004)`). A reader of `scope-<MMM>` cannot resolve either one.
- Attribution names the command, not the file: `Recorded by \`/yukimi.clarify <NNN>\`.` That is all the provenance a deleted file can carry — say it, then stop, rather than pointing at where the reasoning used to be.
- Carry the reasoning itself, not a pointer to it. If a decision's rationale matters to `<MMM>`, state it in the appended bullet. If it doesn't, leave it out.
- Cite only durable sources the same way the record's References section does: `specs/design.md` sections, `specs/notes-*.md`, already-written `specs/<NNN>-*.md`, and real code paths.

Append only. Don't rewrite or reorganize another scope note's existing content.

## 9. Wrap up

Report: how many decisions, problem areas and open questions the record now holds; which higher scope notes you appended to; and what still blocks spec-writing. Say plainly if the spec is not yet clarified enough to write.

Do not write `specs/<NNN>-<slug>.md` — that is a separate step, and the user reviews and edits the record first. Don't commit or push unless the user explicitly asks.
