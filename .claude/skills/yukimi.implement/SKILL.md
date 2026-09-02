---
name: yukimi.implement
description: Plan and implement a numbered yukimi spec (specs/NNN-*.md) — enters plan mode, drafts an implementation plan from the spec, then executes it once the plan is approved.
---

# Implement a numbered spec

Argument: the spec number, e.g. `/yukimi.implement 005` or `/yukimi.implement 003.a`. Accept bare numbers (`5`), zero-padded (`005`), or letter-suffixed (`003.a`) forms; normalize to the zero-padded form used in filenames.

## 1. Enter plan mode — before anything else

Call `EnterPlanMode` as your very first action, before locating the spec file, before any `ls`/`Read`/`grep`. It's read-only, so there's no cost to calling it early, and calling it first removes any ordering ambiguity about when "research" starts. Do not batch it with other tool calls — it must be the sole call in your first turn.

## 2. Resolve, validate, and research the spec

Now that you're in plan mode:

- Find the matching file: `specs/<NNN[.letter]>-*.md` (e.g. `specs/005-statement-execution.md`). If no argument was given, or no file matches, ask the user for the spec number.
- Cross-check the number against the package table in `CLAUDE.md` and note the target package path(s).
- Per `CLAUDE.md`, a spec may depend only on specs numbered strictly below it. Check that every spec listed in this spec's own **Dependencies** section already has its package implemented under `internal/`/`apis/`. If a dependency is missing, stop and tell the user which one — don't silently skip it.
- If the target package/type already exists and looks fully implemented (not just scaffolded), ask the user whether they want a re-implementation, an extension, or a different spec number.
- Read the full spec file end to end (Overview, Scope, Key Concepts, Public API, Schema Specification if present, Project Structure, Error Classification, Edge Cases, Dependencies, Integration Points, Success Criteria, Appendix examples).
- Read the relevant sections of `specs/design.md` for product-level context.
- Read `specs/wip-<NNN>-*.md` if it exists. It is the clarification record behind the spec and is kept until this code lands: it carries the detail the spec leaves out — verified vendor behaviour, full enumerations, worked examples, and the alternatives that were rejected. Use it to resolve anything the spec states abstractly, but the spec wins wherever the two disagree. Note any problem areas (`P-xxx`) or open questions (`O-xxx`) it still lists that touch what you are about to build.
- Read the packages of every spec listed under **Dependencies** to learn the real interfaces you'll call — the existing code is authoritative once it exists, even where the dependency's spec text is less precise.
- Skim sibling packages already implemented (e.g. `internal/errors`, `internal/logger`) to match established conventions: error classification, `Logger`/`Handle` usage, no `types.go`/`_impl.go` files, `loader.go` for CRD/config loading, the dual copyright header block.
- If the spec has a **Schema Specification** section, note CRD/config scaffolding needs (`make provider.addtype`, `hack/helpers/` templates) per `CLAUDE.md`.

## 3. Draft the plan

Write the plan as an ordered list of concrete implementation steps, each scoped to become one task-list entry later — e.g. one step per new file, one step for wiring/registration, one step for tests, one step for `make generate`/`make reviewable`. Base the step list on the spec's **Project Structure** section for file layout and its **Success Criteria** (SC-xxx) for what "done" means; note explicitly which SC items are unit-tested vs. integration-tested. Don't pad the plan with steps the spec doesn't call for.

Call `ExitPlanMode` once the plan is complete and unambiguous. If something about the approach is genuinely undecided (not just unresearched), use `AskUserQuestion` before exiting plan mode, not after.

When you do ask, brief the question in message text first: where in the spec or the dependency code the choice arises, what each option would mean for the resulting package, and which facts already rule options out. You have read the spec and the dependencies; the user has not, so a bare question is one they cannot answer. Put the consequence and the trade-off in each option's `description`, and lead with the recommended option.

## 4. Execute

Once the user approves the plan, work through its steps in order:

- Follow `CLAUDE.md` conventions: business logic in `internal/`, controllers as thin orchestration layers, copyright headers, error classification, package organization rules.
- Run `make generate` after API/CRD changes and `make test` as steps complete, not only at the very end.
- When a step adds an `integration_test.go`, run `make test-integration` as part of that step — `make test`/`make reviewable` pass `-short` and skip it, so integration behaviour stays unproven otherwise. AWS credentials are needed first: check with `aws sts get-caller-identity`, and if it fails, log in with `aws sso login --profile <AWS_PROFILE>`, reading the profile name from the repo's `.env`. That command opens a browser, so if it can't complete from a tool call, ask the user to run `! aws sso login --profile <profile>` and continue once they have.
- If the plan turns out wrong or incomplete mid-execution (missing edge case, wrong file layout), pause, explain the deviation to the user, and adjust the plan rather than silently improvising.

## 5. Wrap up

Run `make reviewable` and report the result. Summarize what was implemented against the spec's Success Criteria, calling out any SC item not yet met and why. Don't commit or push unless the user explicitly asks.

Then delete `specs/wip-<NNN>-*.md` — it exists to support this implementation run and is done. Before deleting, if it still holds something the code and spec do not capture (an unresolved problem area, an open question, a verified vendor fact worth reusing), say what and where it should go — the spec, a `specs/notes-*.md`, or a higher-numbered `specs/scope-<MMM>-*.md`. Report the deletion.
