# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
this project aims to follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
once it is released. While the version is `v0.x`, minor releases may change the
API and behaviour.

## [Unreleased]

Yukimi has **not had a tagged release yet**. Everything below is on `main` and
unreleased; the container image `ghcr.io/allianz/yukimi` referenced by
`deploy/install.yaml` is not published, so a fresh install currently ends in
`ImagePullBackOff`. See [deploy/README.md](deploy/README.md).

### Added

- Open-source community health files: `SECURITY.md`, `CODE_OF_CONDUCT.md`,
  `MAINTAINERS.md`, `DCO`, `CHANGELOG.md`, `.editorconfig`, issue and pull
  request templates, and `CODEOWNERS`. Contributions are certified by DCO
  sign-off (`git commit -s`); there is no CLA.
- `.golangci.yml`. `make lint` previously ran golangci-lint on its defaults only.
  Notably it now enforces `goheader` (the Apache-2.0 header, previously kept
  correct by convention alone) and `nilerr` (the "never return `nil` on error in
  `Observe`" rule).
- `.github/dependabot.yml` for `gomod` and `github-actions`. Neither ecosystem
  had scheduled updates before; every action is pinned by commit SHA, so without
  this they silently rot.
- CodeQL analysis (`security-extended`). The repository ran no SAST at all.
- An OSV scanner workflow, which makes the pre-existing `osv-scanner.toml` and
  its one justified suppression load-bearing rather than decorative.
- A fuzz job running each of the five existing fuzz targets for 30 s, and a
  coverage artifact on every CI run.
- `.gitattributes` normalizing the tree to LF. Without it a Windows clone with
  the default `core.autocrlf=true` makes `gofmt` reject every file in the
  repository while CI sees them as clean.

### Changed

- `package/crossplane.yaml` no longer ships the Crossplane template's
  `maintainer` and `description` in the package metadata.
- `XPKG_REG_ORGS` pointed at `xpkg.upbound.io/crossplane` — the Crossplane
  organization on Upbound — and now points at `ghcr.io/allianz`.
  `XPKG_EXAMPLES_DIR` pointed at a nonexistent `examples/`; this repository's
  directory is `example/`.
- README, `CONTRIBUTING.md` and `docs/development/` rewritten or corrected:
  build and test instructions, the review process, and the stale Go and
  golangci-lint versions.

### Removed

- `hack/helpers/prepare.sh` and `docs/development/archive/provider_checklist.md`,
  both leftovers of the Crossplane provider template. The checklist pointed
  readers at `crossplane-contrib`, Crossplane's Slack and files that do not exist
  here.
- The `e2e.automated` and `e2e.manual` Makefile targets, whose scripts never
  existed in this repository.

### Implemented so far

Twelve of the twenty-two numbered specs are implemented. Each spec is
authoritative for its package; see the table in
[CLAUDE.md](CLAUDE.md) for the full map.

- **Foundations** — user vs system errors with incident IDs and
  operation-scoped logging (001); provider-wide config from a mounted ConfigMap
  (002); per-region backplane inventory (007).
- **Secrets** — backend interface, secret-path grammar, RSA keypairs and a TTL
  cache (003), with an AWS Secrets Manager backend (003.a).
- **Snowflake access** — pooled JWT keypair connections with org-admin and
  per-account scopes, plus connection-host and account-URL construction (004);
  single-statement execution with safe rendering and error decoration (005).
- **Accounts** — the `SnowflakeAccount` CRD, account naming and namespace
  labels (006); the module pipeline with outcome and condition aggregation
  (009); `CREATE ACCOUNT` and platform-user bootstrapping (012); the
  `SnowflakeAccount` controller (020).
- **Deletion** — `SnowflakeDeletionRequest` as a positive control for account
  deletion (019).

### Not implemented yet

Guardrail configuration and its admission check (008, 010), quota check and
quota monitor (011, 018), account parameter enforcement (013), network rules
and policies (014), the SSO-only auth baseline (015), identity sync and group
import (016, 017), and replication (021).

[Unreleased]: https://github.com/allianz/yukimi/commits/main
