# Architecture

This document gives a human reader the big picture of Yukimi's architecture: how the parts fit
together, why they are shaped that way, and where to read further.

It is a supplement, not a source of truth. [`specs/design.md`](../specs/design.md) defines what the
platform does, and the numbered specs in [`specs/`](../specs) define what each package promises.
Where this document and either of them disagree, they win. This document summarises and links
instead of restating; it does not repeat schemas, SQL, or package APIs.

Chapter 10 (Quality Requirements) is left out on purpose: the quality goals in
[1.2](#12-quality-goals) and the guarantees in `design.md` cover it, and a separate set of quality
scenarios would only duplicate them.

- [1. Introduction and Goals](#1-introduction-and-goals)
- [3. Context and Scope](#3-context-and-scope)
- [4. Solution Strategy](#4-solution-strategy)
- [5. Building Block View](#5-building-block-view)
- [6. Runtime View](#6-runtime-view)
- [7. Deployment View](#7-deployment-view)
- [8. Cross-cutting Concepts](#8-cross-cutting-concepts)
- [9. Architecture Decisions](#9-architecture-decisions)
- [11. Technical Debt](#11-technical-debt)


## 1. Introduction and Goals

Yukimi is a self-service platform for provisioning and managing Snowflake accounts at enterprise
scale. Every team, called a tenant, gets its own isolated Snowflake account. The account is created
from a declarative request and attached to network, identity and security infrastructure that the
platform sets up once per cloud region and shares between all accounts there.

Yukimi started as an internal platform at Allianz and has since been opened up so that other
organizations can adopt it. Company-specific choices, such as the identity system, network
connections or compliance rules, are configuration and not part of the product.

### 1.1 Requirements Overview

The table summarises the main requirements from a business point of view. The detailed
requirements are in [`specs/design.md`](../specs/design.md), linked in the last column.

| Id | Requirement | Explanation | Details |
| --- | --- | --- | --- |
| R1 | Self-service at global scale | Teams get a new Snowflake environment in minutes instead of weeks, and change it themselves. One service for thousands of teams across business units, clouds and regions, without the platform team growing with them. | [§2](../specs/design.md#2-tenant-onboarding), [§3.2](../specs/design.md#32-domain-concepts) |
| R2 | Secure and compliant by default | Every environment starts from the same hardened, approved baseline, company rules are enforced automatically, and data is protected against accidental loss. | [§3.3](../specs/design.md#33-guardrails)–[§3.11](../specs/design.md#311-security-constraints), [§6](../specs/design.md#6-deletionpermit-resource) |
| R3 | Cost control | Every team has a credit budget it cannot exceed, and spend is attributed to its cost center. | [§3.10](../specs/design.md#310-credit-quota) |
| R4 | Flexible integration | Open by design, so it can be plugged into any existing self-service landscape: GitOps (e.g. Argo CD, Flux), ServiceNow or a custom portal. | [§1.1](../specs/design.md#11-purpose--scope) |
| R5 | AI ready | Users build complete data products by prompting an AI agent, which deploys them directly to production. | – |

### 1.2 Quality Goals

The quality goals that shape the architecture most, in order of priority.

| Priority | Quality goal | What it means for Yukimi |
| --- | --- | --- |
| 1 | Security | Multiple independent lines of defense: no single bug, mistake or compromised tenant is enough to reach another tenant's account. |
| 2 | Reliability | The platform always moves towards the desired state, and a failure never damages an account that already works. |
| 3 | Scalability | Up to 10,000 accounts for 1,000 tenants across 10 regions, without operations effort growing at the same rate. |
| 4 | Usability | Requesting an account is simple, and feedback is clear enough for teams to fix problems themselves. |
| 5 | Performance | A new account is ready in under 60 minutes. Most of that time is spent waiting for the identity sync. |
| 6 | Operability | Operators can quickly diagnose and fix problems, for example by tracing an error to the logs through its incident ID. |
| 7 | Cost efficiency | Running the platform costs less than €50 per month per account. |
| 8 | Adaptability | Non-Allianz organizations can adopt it through configuration, without changing code. |


## 3. Context and Scope

People and the identity sync reach the controller through Kubernetes, and the controller reaches
Snowflake and the secret store directly.

```mermaid
mindmap
  root((Yukimi controller))
    tenant[👤 Tenant team]
    ops[👤 Platform operations]
    snow[Snowflake]
    store[Secret store]
    idsync[Identity sync]
```

| Neighbour | Input and output |
| --- | --- |
| Tenant team | Requests, changes and deletes accounts with `SnowflakeAccount`. |
| Platform operations | Provisions the regional backplane and maintains its network inventory, together with the platform settings, in a ConfigMap. Onboards tenant teams by creating and labelling their namespaces. |
| Snowflake | Provisions and deletes accounts through the central organization account. Configures each tenant account with its own `platform` service user. |
| Secret store | Reads and writes the keypair of each account and of the organization admin. A pluggable interface allows different stores, e.g. AWS Secrets Manager, Azure Key Vault, GCP Secret Manager or HashiCorp Vault. The store stays necessary with OIDC too, because an account can only be created with a keypair. |
| Identity sync | A company-specific controller that syncs the requested groups into the organization account and reports when they have arrived. |

See the [deployment view](#7-deployment-view) for where these run.


## 4. Solution Strategy

Creating an account has to run without a human in the loop, so it contains only steps that
Snowflake SQL can perform. Everything that is hard to automate, because it needs tickets, other
teams or manual testing, is moved out of the account request and done once, in advance, by
operations:

- **Network.** PrivateLink endpoints and DNS wildcards are set up once per region as a shared
  backplane, and tested before the region is offered to tenants. A new account only attaches to
  this backplane with SQL
  ([`design.md` §3.2](../specs/design.md#32-domain-concepts), [§3.5](../specs/design.md#35-backplane-config)).
- **Identity.** SSO is integrated once for the whole organization, in the central organization
  account. A new account does not get its own identity provider integration; it imports the groups
  it needs from the organization account
  ([§3.7](../specs/design.md#37-identity-integration)).

As a result, operations effort grows with the number of regions rather than the number of accounts
([quality goal 3](#12-quality-goals)), and bringing a region online is a project while ordering an
account is not.


## 5. Building Block View

### 5.1 Components

Yukimi is a single controller running in Kubernetes. Tenants reach it only through the Kubernetes
API server, and it reaches Snowflake and the secret store directly.

```mermaid
flowchart LR
    Tenant["Tenant team"]
    Ops["Platform operations"]
    K8s["Kubernetes API server"]
    subgraph Yukimi["Yukimi controller"]
        APIs["APIs"]
        Ctrl["Controllers"]
        Pipe["Account logic"]
        Del["Deletion logic"]
        Cfg["Configuration"]
        Conn["Snowflake connectivity"]
        Sec["Secrets"]
    end
    Snow["Snowflake"]
    Store["Secret store"]

    Tenant -- "account requests" --> K8s
    K8s <--> Ctrl
    Ops -- "configuration files" --> Cfg
    Ctrl --> APIs
    Ctrl --> Pipe
    Ctrl --> Del
    Pipe --> APIs
    Del --> APIs
    Pipe --> Cfg
    Pipe --> Conn
    Pipe --> Sec
    Conn --> Sec
    Conn --> Snow
    Sec --> Store
```

| Component | Specs | Responsibility |
| --- | --- | --- |
| APIs | 006, 016, 019 | Define the custom resources. |
| Controllers | 019, 020 | Reconcile the custom resources and report status back to the tenant. |
| Account logic | 006, 009–018 | Provisions an account step by step, with one module per concern, and derives the tenant's facts from its namespace. |
| Deletion logic | 019 | Lets an account be deleted only with an active permit, and consumes the permit when it is used. |
| Configuration | 002, 007, 008 | Loads the operations-owned base settings, backplane and guardrails. |
| Snowflake connectivity | 004, 005 | Holds the connections to the organization and to each account, and runs SQL on them. |
| Secrets | 003 | Creates, stores and reads the account keypairs in the secret store. |

Error handling and logging (001) are used by every component and are described in
[8.4](#84-error-handling-and-status-reporting).

### 5.2 Packages

Each component breaks down into Go packages. The table lists only packages that exist today. The
numbers refer to the specs in [5.3](#53-dependencies).

| Component | Packages | Description | Spec |
| --- | --- | --- | --- |
| - | `internal/errors/`<br>`internal/logger/` | User and system errors<br>Operation-scoped logging | 001<br>001 |
| APIs | `apis/base/v1alpha1/` | SnowflakeAccount and DeletionPermit types | 006, 019 |
| Controllers | `internal/controller/snowflakeaccount/`<br>`internal/controller/deletionpermit/` | SnowflakeAccount controller<br>DeletionPermit controller | 020<br>019 |
| Account logic | `internal/account/pipeline/`<br>`internal/account/modules/account/`<br>`internal/account/tenant/` | Module interface and outcomes<br>Account creation<br>Account naming and namespace labels | 009<br>012<br>006 |
| Deletion logic | `internal/deletion/` | Permit lookup and consumption | 019 |
| Configuration | `internal/config/base/`<br>`internal/config/backplane/` | Platform-wide settings<br>Regional backplane | 002<br>007 |
| Snowflake connectivity | `internal/snowflake/pool/`<br>`internal/snowflake/host/`<br>`internal/snowflake/statement/` | Pooled connections<br>Hosts and account URLs<br>SQL execution | 004<br>004<br>005 |
| Secrets | `internal/secrets/`<br>`internal/secrets/aws/` | Secret paths, keypairs and cache<br>AWS Secrets Manager backend | 003<br>003.a |

### 5.3 Dependencies

The code is generated feature by feature, and every feature is described by a spec. The specs are
written and implemented in strict numerical order, so a spec may depend only on specs with a lower
number: the higher-numbered ones do not exist yet when it is written. The packages inherit this
order. They have no cyclic dependencies, and the table below lists them in dependency order, with
each row building only on the rows above it. 

Each package is described by exactly one spec, so it is always clear which spec to update when a
package changes. A spec usually describes one package, sometimes a few. 

| Spec | Packages | Description |
| --- | --- | --- |
| [001 Error and Logging](../specs/001-error-and-logging.md) | `internal/errors/`<br>`internal/logger/` | User and system errors, incident IDs, operation-scoped logging |
| [002 Base Config](../specs/002-base-config.md) | `internal/config/base/` | Platform-wide settings from a mounted ConfigMap |
| [003 Secrets Handling](../specs/003-secrets-handling.md) | `internal/secrets/` | Backend interface, secret paths, RSA keypairs, TTL cache |
| [003.a AWS Secrets Backend](../specs/003.a-aws-secrets-backend.md) | `internal/secrets/aws/` | AWS Secrets Manager implementation of the 003 backend |
| [004 Connection Pooling](../specs/004-connection-pooling.md) | `internal/snowflake/pool/`<br>`internal/snowflake/host/` | Pooled keypair connections, connection hosts and account URLs |
| [005 Statement Execution](../specs/005-statement-execution.md) | `internal/snowflake/statement/` | Safe SQL rendering and execution |
| [006 SnowflakeAccount CRD](../specs/006-snowflake-account-crd.md) | `apis/base/v1alpha1/`<br>`internal/account/tenant/` | SnowflakeAccount schema, account naming, namespace labels |
| [007 Backplane Config](../specs/007-backplane-config.md) | `internal/config/backplane/` | Per-region backplane inventory, parameters, allowlist |
| [009 Account Pipeline](../specs/009-account-pipeline.md) | `internal/account/pipeline/` | Module interface, outcomes, condition aggregation |
| [012 Account Module](../specs/012-account-module.md) | `internal/account/modules/account/` | `CREATE ACCOUNT` and platform user bootstrapping |
| [019 Deletion Permit](../specs/019-deletion-permit.md) | `apis/base/v1alpha1/`<br>`internal/deletion/`<br>`internal/controller/deletionpermit/` | DeletionPermit type, its lifecycle controller, permit lookup and consumption |
| [020 SnowflakeAccount Controller](../specs/020-snowflakeaccount-controller.md) | `internal/controller/snowflakeaccount/` | Module wiring, validation, deletion gate, reporting |


## 6. Runtime View

The scenarios below each involve several building blocks. The diagrams show which parts
collaborate and in what order. The detailed behaviour is in `design.md` and the specs.

Every arrow into the controller starts one reconcile pass. A pass never waits: it does what it can,
writes the result onto the resource and returns. Work that is still open is picked up by a later
pass, which Kubernetes triggers on a change or on its regular poll.

### 6.1 Creating an Account

```mermaid
sequenceDiagram
    actor Tenant
    participant GitOps as GitOps<br/>(e.g. Argo CD, Flux)
    participant K8s as API server
    participant Ctrl as Yukimi controller
    participant Store as Secret store
    participant Org as Organization account
    participant Acc as Tenant account

    Tenant->>GitOps: commit SnowflakeAccount
    GitOps->>K8s: sync into the tenant namespace
    K8s->>Ctrl: reconcile
    Note over Ctrl: no account recorded yet,<br/>admission checks pass
    Ctrl->>Store: store new keypair
    Ctrl->>Org: CREATE ACCOUNT (organization admin)
    Ctrl->>K8s: status: account name, URL, Ready=False
    Note over Ctrl,Acc: account comes up
    K8s->>Ctrl: reconcile
    Ctrl->>Store: read keypair
    Ctrl->>Acc: configure as platform user
    Ctrl->>K8s: Ready=True
    GitOps-->>Tenant: account healthy
```

A tenant commits a `SnowflakeAccount` to Git, and the GitOps tool (e.g. Argo CD, Flux) syncs it
into the tenant's namespace. The controller sees that no account has been recorded for it yet and
runs the account pipeline ([009](../specs/009-account-pipeline.md)) module by module. The admission
checks run first. If the request breaks a guardrail or does not fit the credit quota, they stop the
pipeline before anything is created.

The account module ([012](../specs/012-account-module.md)) generates the platform user's keypair,
stores it in the secret store and runs `CREATE ACCOUNT` in the organization account. It then records
the account's name, login URL and identifier on the resource. From then on the account is never
created again. A new account takes a few minutes before anyone can log in to it, so the controller
does not try to connect during a period configured by operations, because a connection would fail
anyway. 

From then on, each pass logs in to the account as the platform user, and the remaining modules
configure parameters, network, authentication, identity and quota through it. The platform user is
the administrator of its own account, but its rights end there: unlike the organization admin, it
cannot reach any other account. The organization admin is used only to create and drop accounts
(see [8.2](#82-privilege-model)).

The account becomes `Ready` the first time every module is done, and stays `Ready` from then on.

### 6.2 Waiting for an Identity Sync

```mermaid
sequenceDiagram
    actor Tenant
    participant K8s as API server
    participant Ctrl as Yukimi controller
    participant IdSync as Identity sync controller
    participant IAM as Enterprise IAM<br/>(e.g. Entra ID, Okta)
    participant Org as Organization account
    participant Acc as Tenant account

    Tenant->>K8s: add group
    K8s->>Ctrl: reconcile
    Ctrl->>K8s: create or update IdentitySyncRequest
    Ctrl->>K8s: IdentitySynced=False (SyncPending)
    K8s->>IdSync: request changed
    IdSync->>IAM: add groups to the Snowflake sync
    IAM->>Org: SCIM provisions groups
    IdSync->>K8s: request Ready=True
    K8s->>Ctrl: reconcile
    Ctrl->>K8s: read request status
    Ctrl->>Acc: import groups, grant roles
    Ctrl->>K8s: IdentitySynced=True
    Tenant->>Acc: group members log in via SSO
```

A tenant gives a company group access to their account by adding it to the `SnowflakeAccount`,
either when the account is created or later. Before the group can be imported into the account, it
must exist in the organization account. Where the company does not sync all of its groups there by
default, the controller asks for them: it keeps one `IdentitySyncRequest` per identity integration
in the tenant's namespace, listing that integration's groups
([`design.md` §4](../specs/design.md#4-identitysyncrequest-resource)). A company-specific controller
fulfils the request: it adds the groups to the Snowflake sync of the enterprise IAM (e.g. Entra
ID, Okta), and SCIM then provisions them into the organization account. Once every group has
arrived, the controller reports `Ready=True` on the request. Yukimi ships only the request, not the
controller that fulfils it.

A sync can take most of an hour, so the controller never waits for it inside a pass. Until the
groups arrive, the identity module reports the account as pending. The account is applied again on
every pass, and the groups of each fulfilled request are imported as soon as it reports `Ready`.
The `IdentitySynced` condition shows the wait. If the wait runs past a configured timeout, the
reason changes from `SyncPending` to `SyncTimeout` with a warning event for operators, but the
controller keeps trying.

The diagram shows a group added to an account that is already `Ready`, which stays `Ready`
throughout. For a new account the first sync also holds `Ready` at false, because nobody can
administer the account before the group bound to `ACCOUNTADMIN` has been imported.

### 6.3 Deleting an Account

```mermaid
sequenceDiagram
    actor Tenant
    participant K8s as API server
    participant Ctrl as Yukimi controller
    participant Store as Secret store
    participant Org as Organization account

    Tenant->>K8s: delete SnowflakeAccount
    K8s->>Ctrl: reconcile
    Ctrl->>K8s: look up active permit: none
    Ctrl->>K8s: DeletionBlocked event, Ready=False
    Tenant->>K8s: create DeletionPermit
    K8s->>Ctrl: reconcile
    Ctrl->>K8s: look up active permit: found
    Ctrl->>Org: DROP ACCOUNT (organization admin)
    Ctrl->>Store: delete keypair
    Ctrl->>K8s: permit Consumed
    Ctrl->>K8s: remove finalizer
```

Deleting a `SnowflakeAccount`, for example by removing its file from Git, does not delete the
account by itself. A finalizer holds the resource in `Terminating`, and the controller looks for an
active `DeletionPermit` for it in the same namespace ([019](../specs/019-deletion-permit.md)). If
there is none, the controller refuses, records a `DeletionBlocked` warning event and sets
`Ready=False`, which the GitOps tool reports as a failure. The tenant then either restores the file or
creates a permit.

A permit names the account, gives a reason and is valid for at most eight hours. A separate
controller marks it `Expired` when the window closes unused. With an active permit, the pipeline
tears the account down, running the modules in reverse. The account is dropped last, and the
platform user's key is deleted with it. Both can be restored for a period configured by operations.
Only after the teardown has succeeded is the permit marked `Consumed` and the finalizer removed, so
a failed teardown can be retried with the same permit while it is still active. The consumed permit stays as an audit record.

A resource whose account was never created, for example because its name failed validation, is
removed without a permit. Operations can also switch deletion protection off for the whole platform
in the base configuration.


## 7. Deployment View

One controller in one Kubernetes cluster serves every region. Tenants share the cluster, each in
their own namespace, and the controller reaches the secret store and every Snowflake region from
there. The diagram shows the AWS reference setup.

```mermaid
architecture-beta
    group k8s(cloud)[Kubernetes cluster]
    group sys(server)[Namespace yukimi-system] in k8s
    group ten(server)[Tenant namespaces] in k8s
    group aws(cloud)[AWS account]
    group sf(cloud)[Snowflake organization]
    group r1(cloud)[Region aws-ap-southeast-2] in sf
    group r2(cloud)[Region gcp-us-east4] in sf

    service cfg(disk)[ConfigMap yukimi-config] in sys
    service ctrl(server)[Yukimi controller] in sys
    service crs(disk)[Custom resources] in ten
    service iam(server)[IAM role] in aws
    service sm(database)[Secrets Manager] in aws
    service pl1(internet)[PrivateLink endpoint] in r1
    service acc1(database)[Tenant accounts] in r1
    service pl2(internet)[PrivateLink endpoint] in r2
    service acc2(database)[Tenant accounts] in r2

    cfg:R --> L:ctrl
    ctrl:T <--> B:crs
    ctrl:R --> L:iam
    iam:R --> L:sm
    ctrl:B --> T:pl1
    pl1:B --> T:acc1
    ctrl:B --> T:pl2
    pl2:B --> T:acc2
```

| Element | What runs or lives there | Provided by |
| --- | --- | --- |
| `yukimi-system` namespace | The controller deployment, its service account and RBAC. | `install.yaml`, rendered from [`package/`](../package) |
| `yukimi-config` ConfigMap | `base.yaml` ([002](../specs/002-base-config.md)) and `backplane.yaml` ([007](../specs/007-backplane-config.md)), mounted read-only. | Operations, applied separately |
| Tenant namespaces | The tenants' custom resources, and the namespace labels set at onboarding. | Operations at onboarding, then GitOps |
| IAM role | The controller's only cloud credential, limited to its own secret paths. | Operations |
| Secrets Manager | The organization admin keypair and one keypair per tenant account. | Operations for the organization admin, the controller for the rest |
| Snowflake regions | One PrivateLink endpoint and DNS wildcard per region, shared by all its accounts. | Operations, once per region via Terraform |

`install.yaml` holds the CRDs and the controller manifests and contains nothing site specific.
Each installation adds a small kustomize overlay for its image and the IAM role annotation on the
service account, and applies the `yukimi-config` ConfigMap on its own, so an upgrade never
overwrites real values with placeholders. The controller reads its configuration once at startup,
so a configuration change takes effect only after a restart.

The controller runs as a single replica with leader election, and a rollout stops the old pod
before it starts the new one. `CREATE ACCOUNT` is not idempotent, so two active controllers must
never act on the same resource. Nothing is lost during the short gap, because the next pass
resumes from the resource's actual state. The pod runs unprivileged under the `restricted` Pod
Security profile, and it has no access to Kubernetes secrets or ConfigMaps through the API: its
configuration arrives as a mounted volume, and its credentials come from the pod's cloud identity.

Before it starts reconciling, the controller checks its configuration and connects to the
organization account. If either fails, the pod does not become ready, so a broken installation
shows up at deployment time rather than on the first tenant request.

The cluster's network must reach the PrivateLink endpoint of every region the backplane lists,
because the controller talks to all accounts directly. Bringing a new region online is therefore an
infrastructure task for operations followed by a configuration entry
([`design.md` §3.5](../specs/design.md#35-backplane-config)), not a new deployment. The identity
sync controller is deployed separately by the company and needs only the same API server.


## 8. Cross-cutting Concepts

_To write:_ concepts that span several packages, each as a short summary that links to the spec or
`design.md` section that owns it.

### 8.1 Tenancy and Isolation

_To write:_ the namespace trust anchor, secret paths, and why a controller bug yields a denied
request rather than cross-tenant access.

### 8.2 Privilege Model

_To write:_ org-admin versus per-account credentials, and when each is used.

### 8.3 Configuration Ownership

_To write:_ the inputs and who owns each: CRD (tenant), guardrails (OEs), exceptions (ISO and ops),
backplane and base config (ops), namespace labels (ops at onboarding).

### 8.4 Error Handling and Status Reporting

_To write:_ user versus system errors, incident IDs, and the condition model (`Ready`, `Synced` and
resource-specific conditions).

### 8.5 Reconciliation Model

_To write:_ idempotent, level-triggered reconciliation, no retries in controller code, and the
latching `Ready` condition.

### 8.6 Naming and Identifiers

_To write:_ CRD name versus resolved Snowflake name, and which identifier is used where.


## 9. Architecture Decisions

_To write:_ the significant decisions, one short entry each: context, decision, consequence, and a
link to where it is specified. Candidates: Crossplane runtime without being a community provider,
spec-driven development, namespace-hash account naming, positive-control deletion,
`IdentitySyncRequest` in its own API group, a pluggable secret store backend.


## 11. Technical Debt

- **OIDC authentication.** The controller reaches tenant accounts only with the `platform` user's
  key pair from the secret store. The optional OIDC path, which avoids reading that key on every
  connection, is still open ([`design.md` 3.11.2](../specs/design.md#3112-oidc-authentication-optional-todo)).
