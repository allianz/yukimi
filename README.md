<br />

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/yukimi_light.png" />
  <source media="(prefers-color-scheme: light)" srcset="docs/yukimi_dark.png" />
  <img src="docs/yukimi_dark_blue.png" alt="yukimi" />
</picture>

<br />
<br />

[![License](https://img.shields.io/badge/License-Apache%202.0-22C2FF.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.27-22C2FF.svg)](https://golang.org/doc/go1.27)
[![OpenSSF Scorecard](https://img.shields.io/badge/dynamic/json?url=https%3A%2F%2Fapi.securityscorecards.dev%2Fprojects%2Fgithub.com%2Fallianz%2Fyukimi&query=%24.score&label=OpenSSF%20Scorecard&suffix=%2F10&color=22C2FF)](https://scorecard.dev/viewer/?uri=github.com/allianz/yukimi)
[![GitHub Stars](https://img.shields.io/github/stars/allianz/yukimi?color=22C2FF)](https://github.com/allianz/yukimi/stargazers)


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

Interested in seeing Yukimi in action? Write to
[christoph.held@allianz.de](mailto:christoph.held@allianz.de) to arrange a demo.


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

[`example/snowflakeaccount.yaml`](example/snowflakeaccount.yaml) is a fully
annotated resource showing every field — identity group imports and role
bindings, per-service-user network rules, and SSO exceptions.


## Local development

Requires Go 1.27+, Docker, and **Linux or macOS** — the Crossplane build
submodule that drives the Makefile does not support Windows hosts.

```bash
git clone https://github.com/allianz/yukimi.git
cd yukimi
make submodules      # fetches the Crossplane build submodule the Makefile depends on
cp .env.example .env
$EDITOR .env
make dev             # creates a kind cluster, applies CRDs, runs the controller
```


## Documentation

- [docs/architecture.md](docs/architecture.md) — how the system is designed and documented: the
  product design, one spec per package, and the spec-driven process that produces them
- [docs/development.md](docs/development.md) — setup, make targets,
  testing, local development and scaffolding
- [CONTRIBUTING.md](CONTRIBUTING.md) — reporting issues, which path a change takes, pull requests
  and review

