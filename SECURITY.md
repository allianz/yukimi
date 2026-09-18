# Security Policy

## Reporting a vulnerability

**Please do not report security vulnerabilities through public GitHub issues,
discussions, or pull requests.**

Report them through GitHub's private vulnerability reporting instead:

1. Go to the [Security tab](https://github.com/allianz/yukimi/security) of this
   repository.
2. Click **Report a vulnerability**.
3. Describe the issue, the version or commit you observed it on, and — if you
   have one — a minimal reproduction.

Only the maintainers listed in [MAINTAINERS.md](MAINTAINERS.md) can see these
reports. GitHub will notify you as the report is triaged, and the discussion
stays private until an advisory is published.

If you cannot use GitHub's reporting form, open a regular issue containing **no
technical detail** — just ask a maintainer to get in touch — and we will find a
private channel.

### What to expect

| Stage | Target |
| :--- | :--- |
| Acknowledgement of your report | 5 working days |
| Initial assessment (severity, affected versions) | 10 working days |
| Fix or documented mitigation | depends on severity; we will tell you the plan |

We will credit you in the advisory unless you ask us not to. Please give us a
reasonable opportunity to ship a fix before disclosing publicly.

## Supported versions

Yukimi has **not yet had a tagged release**. Until `v0.1.0` is published, the
only supported version is the current `main` branch, and there are no patch
backports.

| Version | Supported |
| :--- | :--- |
| `main` | Yes |
| `v0.x` (once released) | Latest minor only |

`v0.x` means the API and behaviour may change between minor versions. Do not
treat this project as production-hardened until it reaches `v1.0.0`.

## Scope

Yukimi is a Kubernetes controller that provisions Snowflake accounts. It holds
privileged Snowflake org-admin credentials and reads and writes secrets in a
configured secrets backend, so the following are all in scope:

- Anything that lets a tenant escape the constraints applied to their own
  `SnowflakeAccount` — for example influencing an account owned by another
  namespace, or bypassing an admission check that is implemented. (The
  guardrail and quota checks, specs 008/010/011, are not implemented yet; see
  [Project status](README.md#project-status).)
- Leakage of credentials, private keys, or JWTs into logs, Kubernetes events, or
  resource status.
- SQL injection through any value that reaches a Snowflake statement. Rendering
  rules live in [`specs/005-statement-execution.md`](specs/005-statement-execution.md).
- Privilege escalation via the provider's own RBAC (see `deploy/base/clusterrole.yaml`).
- Vulnerabilities in the published container image or in dependencies we ship.

**Out of scope:**

- Vulnerabilities in Snowflake itself — report those to
  [Snowflake](https://www.snowflake.com/en/trust-center/). Note that
  `specs/design.md` Appendix B already documents, deliberately and publicly,
  where Snowflake's current feature set cannot enforce a control that Yukimi
  wants enforced. Those are known product gaps, not vulnerabilities in Yukimi.
- Misconfiguration of a self-managed deployment — for example granting the
  provider's IAM role more than it needs, or committing a real `deploy/config.yaml`.
- Findings that require an attacker to already hold Snowflake `ACCOUNTADMIN` on
  the account in question, unless Yukimi is what wrongly granted it.

## Security-relevant design

These specs are the authoritative description of the controls, and are useful
context for a report:

- [`specs/003-secrets-handling.md`](specs/003-secrets-handling.md) — secret paths, RSA keypairs, caching
- [`specs/004-connection-pooling.md`](specs/004-connection-pooling.md) — JWT keypair auth, org-admin vs per-account scopes
- [`specs/005-statement-execution.md`](specs/005-statement-execution.md) — safe SQL rendering
- [`specs/001-error-and-logging.md`](specs/001-error-and-logging.md) — what is and is not written to logs
