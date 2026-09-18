<!--
Thanks for contributing to Yukimi. A few notes before you submit:

  * Every commit needs a `Signed-off-by` line (DCO). Use `git commit -s`.
    See CONTRIBUTING.md and the DCO file.
  * Yukimi is spec-first. If your change alters behaviour in a package governed
    by a spec (specs/NNN-*.md), update that spec in this same pull request.
    Where code and spec disagree, the spec wins.
-->

## What this changes

<!-- One or two sentences. What is different after this merges? -->

## Why

<!-- The problem this solves. Link the issue if there is one: Fixes #123 -->

## Spec impact

<!-- Delete whichever does not apply. -->

- [ ] No spec governs the code I touched.
- [ ] A spec governs it and this pull request keeps them consistent (spec: `specs/___`).
- [ ] This changes designed behaviour, and the spec is updated here too (spec: `specs/___`).

## Checklist

- [ ] Commits are signed off (`git commit -s`).
- [ ] `make reviewable` passes (generate + lint + unit tests).
      Note: this target requires Linux or macOS; on Windows, let CI run it.
- [ ] `make check-diff` passes — no stale `package/crds/` or `deploy/install.yaml`.
- [ ] New behaviour has unit tests; user-facing errors use `errors.NewUserError`.
- [ ] New files carry the Apache-2.0 copyright header (see CONTRIBUTING.md).

## Anything reviewers should know

<!--
Trade-offs you weighed, things you deliberately left out, follow-up work.
If this needs real Snowflake or AWS access to verify, say so -- CI cannot.
-->
