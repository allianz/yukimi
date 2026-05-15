

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/development/yukimi_light.png" />
  <source media="(prefers-color-scheme: light)" srcset="docs/development/yukimi_dark.png" />
  <img src="docs/development/yukimi_dark.png" alt="yukimi" />
</picture>

<br/>
<br/>

[![License](https://img.shields.io/badge/License-Apache%202.0-22C2FF.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.24-22C2FF.svg)](https://golang.org/doc/go1.24)
[![GitHub Stars](https://img.shields.io/github/stars/allianz/yukimi?color=22‚C2FF)](https://github.com/allianz/yukimi/stargazers)


Yukimi is an open source platform for self-service Snowflake management at enterprise scale. Teams provision new Snowflake accounts and bootstrap new analytics or AI applications without tickets, without waiting, and without depending on a central operations team.


## Overview

In most organizations, provisioning a new Snowflake account is a manual, ticket-driven process that is slow and painfull. 

Yukimi replaces this process with full automation. This is possible because Yukimi separates infrastructure from tenancy. Network connectivity, SSO, and regional integration are set up once per cloud region — not once per account. When a team creates a new account, it simply attaches to this pre-prepared regional infrastructure. 

Beyond speed, Yukimi gives organizations a single point of control to define and enforce security and compliance policies across every Snowflake account — automatically applied when an environment is created, and continuously maintained without manual intervention.

### Key Features

- **🚀 Self-Service**: Teams create and manage their own Snowflake environments without opening a ticket
- **⚡ Fast**: Accounts and applications provisioned in minutes, not weeks
- **🔒 Policy Enforcement**: Security and compliance policies applied automatically across every environment
- **☁️ Multi-Cloud**: Consistent operations across AWS, Azure, and GCP regions
- **📋 Reusable Templates**: Shared blueprints for common application patterns, maintained centrally
- **📊 Audit Trail**: Complete visibility into all provisioning and configuration changes

