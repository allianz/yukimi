# Maintainers

Maintainers review and merge pull requests, triage issues, cut releases, and
receive private security reports.

| Maintainer | GitHub | Affiliation |
| :--- | :--- | :--- |
| Christoph Held | [@cheld](https://github.com/cheld) | Allianz |
| Tomislav Letica | [@leticat](https://github.com/leticat) | Allianz |

`AUTHORS` records the contributing legal entities; this file records the people
who can merge.

## Reaching us

- **Bugs and feature requests:** open an [issue](https://github.com/allianz/yukimi/issues).
- **Security vulnerabilities:** follow [SECURITY.md](SECURITY.md) — do not open
  a public issue.
- **Code of Conduct reports:** see [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).

## Becoming a maintainer

Yukimi is young and the maintainer group is currently Allianz-internal. If you
are contributing regularly and want a larger role, say so in an issue or in one
of your pull requests — we would rather grow the group than gatekeep it.

## Review expectations

- Every pull request needs an approving review from a maintainer who is not the
  author.
- Every commit needs a `Signed-off-by` line — see
  [CONTRIBUTING.md](CONTRIBUTING.md) and [DCO](DCO).
- Changes inside a package governed by a spec must be consistent with that spec
  (`specs/NNN-*.md`). Where code and spec disagree, the spec wins; changing the
  behaviour means changing the spec in the same pull request.
