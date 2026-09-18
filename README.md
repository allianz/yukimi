<br />

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/yukimi_light.png" />
  <source media="(prefers-color-scheme: light)" srcset="docs/yukimi_dark.png" />
  <img src="docs/yukimi_dark_blue.png" alt="yukimi" />
</picture>

<br />
<br />

[![License](https://img.shields.io/badge/License-Apache%202.0-22C2FF.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.26-22C2FF.svg)](https://golang.org/doc/go1.26)
[![GitHub Stars](https://img.shields.io/github/stars/allianz/yukimi?color=22C2FF)](https://github.com/allianz/yukimi/stargazers)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/allianz/yukimi/badge)](https://scorecard.dev/viewer/?uri=github.com/allianz/yukimi)


Yukimi is an open source platform for self-service Snowflake management at enterprise scale. Teams provision new Snowflake accounts and bootstrap new analytics or AI applications without tickets, without waiting, and without depending on a central operations team.


## Overview

In most organizations, provisioning a new Snowflake account is a manual, ticket-driven process that is slow and painful.

Yukimi replaces this process with full automation. This is possible because Yukimi separates infrastructure from tenancy. Network connectivity, SSO, and regional integration are set up once per cloud region — not once per account. When a team creates a new account, it simply attaches to this pre-prepared regional infrastructure.

Beyond speed, Yukimi gives organizations a single point of control to define and enforce security and compliance policies across every Snowflake account — automatically applied when an environment is created, and continuously maintained without manual intervention.

### Key Features

- **🚀 Self-Service**: Teams create and manage their own Snowflake environments without opening a ticket
- **⚡ Fast**: Accounts and applications provisioned in minutes, not weeks
- **🔒 Policy Enforcement**: Security and compliance policies applied automatically across every environment
- **☁️ Multi-Cloud**: Consistent operations across AWS, Azure, and GCP regions
- **📋 Reusable Templates**: Shared blueprints for common application patterns, maintained centrally
- **📊 Audit Trail**: Complete visibility into all provisioning and configuration changes


## Project status

> **Yukimi is pre-release and not yet installable from a published artifact.**

There is no tagged release. The container image `ghcr.io/allianz/yukimi` that
`deploy/install.yaml` references **is not published yet**, so applying the
manifests today gets you a pod in `ImagePullBackOff`. To run it now you must
build the image yourself — see [Building from source](#building-from-source).

Yukimi is built spec-first, and roughly half the design is implemented. Twelve of
the twenty-two numbered specs have landed:

| Area | Status |
| :--- | :--- |
| Errors, logging, configuration, backplane inventory | Implemented |
| Secrets handling + AWS Secrets Manager backend | Implemented |
| Snowflake connection pooling and statement execution | Implemented |
| `SnowflakeAccount` CRD, account creation, controller | Implemented |
| `SnowflakeDeletionRequest` | Implemented |
| Guardrails and quota checks | Not implemented |
| Account parameters, network rules, SSO-only auth baseline | Not implemented |
| Identity sync and group import | Not implemented |
| Replication | Not implemented |

`CHANGELOG.md` has the detail, and the spec table in [CLAUDE.md](CLAUDE.md) maps
each spec to its package. Treat `v0.x` as subject to change: the API and
behaviour may change between minor versions.


## How it works

Yukimi is a Kubernetes controller. An operator prepares each cloud region once —
network connectivity, SSO, regional integrations — and records that inventory in
configuration. A team then requests an account by applying a resource to their own
namespace:

```yaml
apiVersion: base.snowflake.yukimi.io/v1alpha1
kind: SnowflakeAccount
metadata:
  name: analytics-eu
spec:
  description: "Analytics team Snowflake environment for EU operations"
  contact: alice.smith@company.com
  region: aws-eu-central-1
  environment: prod
  creditQuota: 500
```

The controller creates the Snowflake account against the pre-prepared regional
infrastructure, bootstraps a platform service user it uses for all later
operations, and then keeps the organization's baseline applied to the account.
Progress and failures surface as status conditions on the resource:

```bash
kubectl describe snowflakeaccount analytics-eu
```

[`example/snowflakeaccount.yaml`](example/snowflakeaccount.yaml) is a fully
annotated resource showing every field — identity group imports and role
bindings, per-service-user network rules, and SSO exceptions.
[`example/snowflakedeletionrequest.yaml`](example/snowflakedeletionrequest.yaml)
shows how deletion works: it is a positive control, so an account is only ever
deleted when a separate request explicitly authorizes it.


## Installation

Full instructions, including the prerequisites that cause a crash-loop if
missing, are in **[deploy/README.md](deploy/README.md)**. Read that before you
start — startup is deliberately fail-fast.

```bash
cp deploy/config.example.yaml deploy/config.yaml
$EDITOR deploy/config.yaml                # replace the REPLACE_ME values
kubectl apply -f deploy/install.yaml -f deploy/config.yaml
```

`install.yaml` goes first: it creates the `yukimi-system` namespace that the
ConfigMap lands in. The ConfigMap is deliberately not part of `install.yaml` so
that re-applying it to upgrade cannot overwrite your configuration.

### Prerequisites

- A Kubernetes cluster and `kubectl`. The CRDs use CEL validation rules, which
  reached GA in Kubernetes 1.29, so treat 1.29 as the floor. Development and CI
  currently track 1.33.
- A Snowflake organization, and a user holding `GLOBALORGADMIN`.
- AWS Secrets Manager, containing the org-admin credentials at the expected path,
  reachable by the controller's IAM identity. The controller reads and writes
  secrets but never creates the org-admin one —
  `hack/dev/setup_snowflake_credentials.sh` does that.
- Network reachability from the cluster to Snowflake within 30 seconds of
  startup.

[deploy/README.md](deploy/README.md) covers the exact IAM permissions, the
ServiceAccount trust policy, and each failure mode.

### Building from source

Requires Go 1.26+, Docker, and **Linux or macOS** — the Crossplane build
submodule that drives the Makefile does not support Windows hosts.

```bash
git clone --recurse-submodules https://github.com/allianz/yukimi.git
cd yukimi
make build           # binary into _output/bin/
make reviewable      # generate + lint + unit tests, what CI runs
```

For a local cluster with the controller running out-of-cluster against kind:

```bash
cp .env.example .env
$EDITOR .env
make dev             # creates a kind cluster, applies CRDs, runs the controller
make dev-clean
```

See [docs/development/development.md](docs/development/development.md) for the
full set of targets.


## Documentation

| Where | What |
| :--- | :--- |
| [`specs/design.md`](specs/design.md) | The product blueprint: the tenancy model, every resource, and the reasoning behind them |
| [`specs/`](specs/) | One numbered spec per package, authoritative for that package |
| [`deploy/README.md`](deploy/README.md) | Installing, configuring, prerequisites, and what is deliberately absent |
| [`example/`](example/) | Annotated tenant resources |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | How to contribute, and the spec-first workflow the project is built with |
| [`CHANGELOG.md`](CHANGELOG.md) | What has landed |

There is no generated CRD field reference yet. Until there is, the annotated
[`example/snowflakeaccount.yaml`](example/snowflakeaccount.yaml) and
[`specs/006-snowflake-account-crd.md`](specs/006-snowflake-account-crd.md)
together are the field documentation.


## Contributing

Contributions are welcome. [CONTRIBUTING.md](CONTRIBUTING.md) covers how to build
and test, how pull requests are reviewed, and the sign-off requirement — every
commit needs a `Signed-off-by` line, so use `git commit -s`.

Yukimi is built spec-first: `specs/design.md` is the blueprint, and each part of
it becomes a numbered spec implemented in order. If you are changing behaviour in
a package that a spec governs, the spec changes in the same pull request.

By participating you agree to abide by our [Code of Conduct](CODE_OF_CONDUCT.md).


## Security

Please **do not** open a public issue for a vulnerability. Report it through
GitHub's private vulnerability reporting, as described in
[SECURITY.md](SECURITY.md).


## License

Apache License 2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).

Yukimi is scaffolded from the [Crossplane provider template][template] and reuses
its code layout and Go tooling. It is not, however, a general-purpose Crossplane
provider for the wider ecosystem: the CRDs and controllers are shaped around this
platform's own tenant-onboarding and Snowflake-provisioning model.

[template]: https://github.com/crossplane/provider-template
