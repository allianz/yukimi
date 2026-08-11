# Specification Roadmap

## Purpose

This document records the planned decomposition of `specs/design.md` (the authoritative product
design) into numbered per-package specification documents. It captures the *intended scope* of each
spec — including the specs not yet written — so that the planned content of any spec can be
reconstructed later without the conversation that produced it. It also records the decisions taken
while planning the decomposition and the reasoning behind the ordering.

Once a numbered spec file exists, **that file is authoritative for its package** and supersedes the
corresponding entry here. Entries for unwritten specs are the working brief for whoever writes them.

## The ordering rule

**A spec may depend only on specs numbered strictly below it.**

Specs are written and implemented one at a time, in ascending order. When spec N is implemented, the
code for every spec above N does not yet exist, so a dependency on a higher number would be
unbuildable. The numbering below is therefore a topological sort of the real Go import graph — there
are no forward references. Any renumbering must preserve this property.

Two consequences worth stating explicitly:

- Where a package would naturally reach "upward", the dependency is inverted by injection (an
  interface parameter) or deferred to a higher-numbered caller. Several such deferrals are recorded
  in the per-spec entries as explicit out-of-scope notes.
- Two packages carry a hard **prohibition** on importing another package even though it sits below
  them, because the natural import would create a cycle. See "Why the ordering is acyclic".

## Decisions taken

1. **Ignore the `origin/secrets-handling` branch.** That unmerged branch holds a drafted
   `002-secrets-handling.md` plus a `002-a-aws-secrets-backend.md` sub-spec and a full
   `internal/secrets/` implementation. It is treated as abandoned. Secrets is written fresh as a
   single spec, and there is no `NNN-a` sub-spec convention in this numbering.

2. **Provider settings come from a mounted ConfigMap, not a ProviderConfig CRD.** Spec 002 is a
   plain loader — no CRD, no controller, no reconciler, no singleton wiring.

3. **Guardrails, approved exceptions and backplane config also live in mounted ConfigMaps.** Specs
   007 and 008 own no `apis/` types — only a loader plus validation and evaluation logic.

4. **Reconcile-time validation, not an admission webhook.** Invalid input is admitted by the API
   server and then reported as `Synced=False` with a user error. Cheap structural checks — notably
   the 3.11.3 immutability of `region`, `name` and `environment` — use CRD CEL
   `x-kubernetes-validations`. There is no webhook spec.

5. **No separate conditions/events spec.** crossplane-runtime already supplies `xpv1.Available()` /
   `ReconcileSuccess()`, injects an `event.Recorder`, and its managed reconciler sets `Ready` and
   `Synced` itself. Every custom condition (`QuotaAvailable` from 3.10, `IdentitySynced` from 4.3)
   and every custom event (`DeletionBlocked` from 6.3, `QuotaExhausted` from 3.10, `SyncTimeout`
   from 4.3) is SnowflakeAccount-specific. Aggregation rules therefore live in spec 009 and
   reporting in spec 018.

6. **Remove the inherited ProviderConfig boilerplate outright.** `apis/v1alpha1/types.go`,
   `internal/controller/config/` and the four `package/crds/` files that came from the Crossplane
   provider template have been deleted. Decision 2 already commits provider settings to a mounted
   ConfigMap read by `internal/config/` (spec 002) — no CRD, no controller, no reconciler — so the
   ProviderConfig path would never have been revisited. No spec depends on it.

## Why the ordering is acyclic

- **002 is a leaf.** As a ConfigMap loader it imports only `internal/errors` — it constructs no
  backends and runs no controller. Secrets (003) needs its AWS region and the pool (004) needs its
  Snowflake org name, so it must sit below both.

- **003 never imports the pool.** Pushing a rotated public key
  (`ALTER USER … SET RSA_PUBLIC_KEY`) is explicitly out of scope for 003 — the caller does it. The
  pool imports secrets, never the reverse.

- **004 must not import 005.** The pool performs session setup (`USE ROLE`) and health probes using
  the **raw driver** (`db.ExecContext` / `PingContext`). Without this rule an implementer naturally
  reaches for `statement.Execute` and creates 004 → 005 → 004. Spec 004 must state the prohibition.

- **005 takes an injected executor** (`*sql.DB`, or a one-method interface), so it does not import
  the pool either. Position-aware error decoration is testable against any `*sql.DB`.

- **008 does not import 007.** The two perform independent checks: a guardrail `"/16"` constrains
  prefix *width*, while a backplane `maxCidrs: ["172.16.0.0/12"]` constrains *containment*.
  `10.0.0.0/16` passes the first and fails the second, so neither subsumes the other. For the
  `"full"` rule spec 008 only checks that `allowedIPs` is *absent*; resolving `full` into concrete
  CIDRs is deferred to 012, which already holds the region entry.

- **009 sits below the modules (010–013, 015), and `internal/account` must never import
  `internal/account/modules/…`.** Module registration and ordering live in 018. Pipeline tests use
  fake modules. This is the one place in the tree where an import cycle is easy to introduce.

- **014 sits directly below 015, its only consumer.** Nothing in 009–013 touches it, so deferring it
  keeps the identity concern contiguous (emit → wait on `Ready` → import) and avoids building an
  emitter before its consumer exists.

- **017 sits below 018.** Per design 6.3 Phase 3 the account controller both reads the deletion
  warrant and writes its `Consumed` status, so it imports `internal/deletion`. The dependency is
  one-way; no injected interface is needed.

Verified: every "Depends on" entry below is strictly lower-numbered.

---

# Per-spec scope

## Spec 001 — `001-error-and-logging.md` — **EXISTS**

- Packages: `internal/errors/`, `internal/logger/`. Covers design chapter 7 (partial).
  Depends on: nothing.
- Already implemented: `NewUserError`, `IsUserError`; `Operation` with
  `OpObserve`/`OpCreate`/`OpUpdate`/`OpDelete`, `Logger`, `New`, `Info`, `Debug`, `Handle`, and
  8-character incident IDs.
- Every later spec uses this for error classification, and this file is the shape reference for all
  new specs.

## Spec 002 — `002-provider-config.md`

- Package: `internal/config/`. Not described in design.md — the schema is derived from
  `.env.example`. Depends on: 001.
- Scope:
  - Load provider-wide settings from a **mounted ConfigMap** at startup and expose them as an
    immutable struct.
  - Fields (from `.env.example`): `SNOWFLAKE_ORG` (the organization name, used in account
    identifiers, ASM secret paths and `accountUrl`), `SNOWFLAKE_ORG_ADMIN_ACCOUNT` (the account used
    for org-level operations), `AWS_REGION` (where AWS Secrets Manager stores credentials), and
    `SNOWFLAKE_USE_PRIVATELINK` (affects the connection host).
  - References to the other three ConfigMaps (backplane, guardrails, exceptions) so that specs 007
    and 008 know what to read.
  - Validation of required fields, raising `errors.NewUserError` for missing or malformed values.
    Fail fast at startup rather than once per reconcile.
- Out of scope: no CRD, no controller, no reconciler, no Kubernetes watch. This is not a Crossplane
  ProviderConfig; the inherited ProviderConfig boilerplate has been removed (decision 6).
- Why it sits here: it is a leaf, and both 003 and 004 need values from it.

## Spec 003 — `003-secrets-handling.md`

- Package: `internal/secrets/`. Covers design 3.11.1, the 3.6 keypair, and Appendix B X1.
  Depends on: 001, 002.
- Scope:
  - **Tenant secret path** (3.11.1), constructed strictly as
    `snowflake/tenant/<snowflake-org-name>/<kubernetes-namespace>/<snowflake-account-name>/platform-credentials`.
    Critical detail: `<snowflake-account-name>` is the CRD's `metadata.name`, **not** the resolved
    Snowflake name from 3.12. Every path segment must derive from Kubernetes identifiers so that the
    namespace remains the trust anchor; cross-tenant access then fails at the AWS IAM level on an
    incorrect path.
  - **Org-admin secret path**: `snowflake/org/<org>/<org-admin-account>/org-admin-credentials`.
  - **RSA keypair generation** for the `platform` service user created by `CREATE ACCOUNT`:
    `crypto/rand`, minimum 2048-bit, PKCS#8 encoding for private keys and PKIX for public keys.
  - Get / store / rotate credential operations against AWS Secrets Manager; an in-memory TTL cache;
    credential types for platform and org-admin credentials.
  - Error mapping: a missing secret or a malformed path is a user error; ASM timeouts and throttling
    are system errors with incident IDs.
- Out of scope: pushing a rotated public key to Snowflake (`ALTER USER … SET RSA_PUBLIC_KEY`) — that
  needs the pool, which does not exist yet. The caller does it once 004 lands.
- Appendix B X1 note: once a tenant holds `ACCOUNTADMIN` they can drop or re-key the `platform`
  user, locking the platform out of the account. Record this as a known gap pending Snowflake
  Organization Policies.

## Spec 004 — `004-connection-pooling.md`

- Package: `internal/snowflake/pool/`. Covers the design 3.11 introduction and 3.6.
  Depends on: 001, 002, 003.
- Scope:
  - Pooled `*sql.DB` connections to Snowflake using **JWT keypair authentication**, with the private
    key read through 003.
  - **Two connection scopes, enforcing the privilege step-down of 3.11**: an org-admin connection
    used only for org-level operations (`CREATE ACCOUNT`, `DROP ACCOUNT`), and a
    per-tenant-account connection acting as that account's `platform` user for everything else. The
    point is to minimize blast radius — org credentials are restricted almost entirely to account
    creation and deletion.
  - Pool keyed by `(org, namespace, account)` — the same tuple as the ASM secret path.
  - Host construction honoring the PrivateLink flag from 002.
  - Session setup on checkout (`USE ROLE` and similar) plus a health probe, both using the raw
    driver.
  - Connection lifecycle: idle eviction, maximum lifetime, concurrency-safe checkout.
- **Hard rule to write into the spec**: this package must not import
  `internal/snowflake/statement`, otherwise 004 → 005 → 004.
- Out of scope: statement semantics and error decoration (005).

## Spec 005 — `005-statement-execution.md`

- Package: `internal/snowflake/statement/`. Covers the SQL mechanics used throughout design 3.6–3.10.
  Depends on: 001.
- Scope:
  - Execute SQL against an **injected executor** (`*sql.DB`, or a one-method
    `interface{ ExecContext(...) }`). It never constructs its own connection, so it does not import
    004.
  - **Position-aware errors**: Snowflake reports an error position within the submitted statement;
    decorate failures with that position and the offending fragment so operators can locate the
    problem in a long generated statement.
  - Classify Snowflake error codes into user errors (bad identifier, object already exists,
    insufficient privileges on tenant-supplied input) and system errors (network, timeout, internal).
  - Idempotency helpers so that re-running a partially applied module is safe (`IF NOT EXISTS` and
    `CREATE OR REPLACE` conventions), since every module is re-invoked on each reconcile.
  - Multi-statement / ordered batch execution with stop-on-first-error semantics.
- Out of scope: which SQL to emit — that belongs to each module (010–013, 015) and to 016.

## Spec 006 — `006-snowflake-account-crd.md`

- Packages: `apis/base/v1alpha1/` (SnowflakeAccount), `internal/tenant/`. Covers design 3.1, 3.11.3,
  3.12, 7.2 and the chapter 2 namespace labels. Depends on: 001, 002.
- This is **not a Snowflake API** — Snowflake is driven purely by SQL (005). It is the Kubernetes CRD
  plus the namespace-derived identity helpers. It is also **not schema-only**: `internal/tenant/` is
  real, unit-testable code with no Kubernetes or Snowflake dependency.
- Scope — the CRD (the full 3.1 schema):
  - General: `description`, `contacts[]`.
  - Snowflake configuration: `region`, `environment` (`dev` or `prod`, required, immutable).
  - `creditQuota` — this account's share of the namespace allowance.
  - `identityIntegration.groups{<integration-key>: [group names]}`, where the key is free-form rather
    than schema (`giam` is the only one configured today), and
    `identityIntegration.roleBindings{SYSTEM_ROLE: group}` with `ACCOUNTADMIN` strictly required.
  - `customNetworkRules.serviceUsers{<user>: [{connection, allowedIPs[]}]}` and
    `customNetworkRules.accountWide[{connection, allowedIPs[]}]`.
  - `customAuthRules.exceptions[{user, rsaKeyAllowed, patAllowed, reason}]`.
  - Status (7.2): `accountName` (the **resolved** Snowflake name), `accountUrl`, `conditions`.
  - **CEL `x-kubernetes-validations` for the 3.11.3 immutability** of `region`, `name` and
    `environment`. `region` and `name` immutability prevents identity spoofing: create an account,
    let the secret generate, then repoint the CRD at a different target while keeping the
    credentials. `environment` immutability exists for a different reason — it selects which
    guardrails apply, so a mutable field would let an account be created under `prod` and then
    flipped to `dev` for its looser network posture.
- Scope — `internal/tenant/`:
  - `naming.go` (3.12): the resolved Snowflake name is `metadata.name` with every `-` translated to
    `_`, followed by `_` plus the first 5 characters of the base32-encoded SHA-256 of
    `metadata.namespace`. Example: `analytics-team-eu` in namespace `finance` becomes
    `analytics_team_eu_5k3wf`. This is needed because Snowflake account names must be unique
    org-wide, while tenants name freely and cannot see other namespaces — two teams both naming an
    account `dev` is an expected case. It requires no stored state: namespaces cannot be renamed and
    `name` is immutable, so the value is recomputable on every reconcile and stable for the life of
    the account. Only Snowflake-facing values use it (the SQL in 3.6 and 3.8, and the status in 7.2);
    everything else refers to accounts by CRD name.
  - `labels.go`: read the ops-set namespace labels applied during onboarding (chapter 2) —
    `department` (consumed by guardrail targeting in 008), `credit-quota` (consumed by 016), and
    `cost-center`. These are ops-owned and deliberately not CRD fields, so tenants cannot alter them.
  - `url.go`: `accountUrl` is `https://<org>-<resolved-name>.snowflakecomputing.com`.
- **Blocker resolved**: `hack/helpers/apis/GROUP_LOWER/APIVERSION/groupversion_info.go.tmpl` used
  to hardcode `.allianz.io` in both the `+groupName` marker and the `Group` constant; it now emits
  `.yukimi.io`, so `make provider.addtype provider=Snowflake group=base` correctly produces
  `base.snowflake.yukimi.io`.
- Open question for this spec: `cost-center` is read by nothing in design.md. Confirm with ops
  whether it belongs in the account `COMMENT` or as a Snowflake tag; otherwise document it as
  ops-only metadata.

## Spec 007 — `007-backplane-config.md`

- Package: `internal/backplane/`. Covers design 3.5. Depends on: 001, 002.
- Concept: the platform pre-provisions network infrastructure **once per region** as a shared
  "backplane" (PrivateLink per region, wildcard DNS, global SSO, centrally hardened policies), so
  that new accounts attach to already-live infrastructure via SQL instead of requiring a per-account
  infrastructure project. Bringing a region online is a manual ops job: run Terraform, close the DNS
  and VPC-endpoint-acceptance tickets, test, then record the outputs here with `available: true`.
- Scope — a ConfigMap loader (`loader.go`) plus lookup and validation over this schema:
  - `globalParameters` — the org-wide security baseline applied to every account (for example
    `PREVENT_UNLOAD_TO_INLINE_URL`, `REQUIRE_STORAGE_INTEGRATION_FOR_STAGE_CREATION`).
  - `identitySync.enabled` (false when the enterprise already syncs every group org-wide) and
    `identitySync.timeout` (default `1h`, and never shorter than the slowest provider — Entra ID
    takes roughly 45 minutes). These are org-level rather than per-region because identity is
    integrated globally.
  - `regions{<region>: …}`, each holding:
    - `available` — a **controller-side gate with no Snowflake counterpart**, letting ops stage a
      region and reject CRDs naming it until it is officially offered.
    - `inventory[{connection, type, vpceId, maxCidrs[]}]` — the catalog of physical ingress paths.
      Listing a connection only makes its handle referenceable and caps how wide it may ever be
      opened; it grants nothing by itself. `type` is for example `AWSVPCEID` or `IPV4`. VPCE-only
      connections (such as `dbt-cloud`) have no `maxCidrs` and no IPs to manage.
    - `regionalParameters` — region-specific account parameters taken from Terraform outputs (for
      example `ENABLE_INTERNAL_STAGES_PRIVATELINK`, `S3_STAGE_VPCE_DNS_NAME`).
    - `regionalAllowlist[{connection, allowedIPs[]}]` — the mandatory baseline access applied to
      every account in the region, guaranteeing basic reachability (browser logins) before any custom
      rules are considered. Omitting `allowedIPs` inherits the connection's full `maxCidrs`.
  - Region lookup by the CRD's `region` field, and connection lookup by name within a region's
    inventory.
  - A **CIDR containment helper** (`allowedIPs` ⊆ `maxCidrs`), reused by 012.
  - Load-time validation: every `regionalAllowlist` connection exists in that region's `inventory`,
    and each `allowedIPs` entry falls inside its connection's `maxCidrs`.
- Out of scope: applying any of it — parameters go to 011 and network rules to 012.

## Spec 008 — `008-guardrails.md`

- Package: `internal/guardrails/`. Covers design 3.3 and 3.4. Depends on: 001, 002, 006.
- Concept: a gatekeeper that validates and modifies tenant input **before** anything reaches
  Snowflake. It applies exclusively to `SnowflakeAccount`; the other kinds validate themselves.
- Scope — a ConfigMap loader plus evaluation:
  - `target` selects which accounts a guardrail applies to; an omitted field or `"*"` matches all.
    Each key has a fixed source: `environment` and `region` come from CRD spec fields, `account`
    comes from `metadata.name` **as the tenant wrote it** (not the resolved name — guardrails run
    before the account exists), and `department` comes from the ops-set namespace label. Because
    `department` is ops-owned, tenants cannot move themselves out of their department's rules;
    `environment`, by contrast, they declare themselves.
  - `constraints` — strict rules the input must pass: an `accountName` regex, a `groupNames` regex
    (applied to every group under `identityIntegration.groups`), `maxCreditQuota`,
    `allowedRegions[]`, and `connections`.
  - `preset` — defaults, with two distinct behaviors that the spec must state explicitly. For CRD
    fields the user omitted (for example `creditQuota`) the value is filled in **and then enforced as
    usual**. For account settings with no corresponding CRD field (for example `timeZone`) it is only
    an initial value: not enforced, and the tenant's account admin may change it later in Snowflake.
  - **Connection constraints**, scoped first by `serviceUsers` / `accountWide` (mirroring the CRD
    keys) and then by connection name, each carrying one of three rules: `"/NN"` means a CIDR is
    required but capped at that maximum width; `"full"` means the user may not specify a range and
    inherits the connection's full predefined range; `"off"` means the connection is forbidden. Any
    connection not explicitly listed falls back to that scope's `"*"` entry.
  - **Top-down merge**: broad global baselines are applied first, then narrower guardrails. A later
    match replaces what it overrides, whether that tightens or loosens the rule.
  - `exceptions.go` (3.4): when a CRD fails its guardrails the controller does **not** reject
    immediately. It first checks the ops-owned exceptions file for a matching approved exception and
    lets the otherwise-failing input through if one exists. Matching is an **exact** `account` match
    on the CRD name with no wildcards, against the full entry verbatim, and is consulted **only
    after** a guardrail has rejected. The approval workflow is email-driven and lives outside the
    platform (customer → ISO → ops edits the file); the file is simply the durable record.
- Deliberately **not** importing 007: guardrails constrain prefix width while the backplane
  constrains containment, and neither subsumes the other. For the `"full"` rule this spec only checks
  that `allowedIPs` is absent; resolving it to concrete CIDRs is 012's job.
- Also out of scope: cross-artifact consistency, such as a guardrail naming a connection absent from
  a region's inventory. That surfaces as a rule-creation failure in 012.
- Note for the spec: because tenants set `environment` themselves, they can choose `dev` and receive
  its looser constraints. The platform does not verify that a `dev` account is really used for
  development; a team running production workloads there carries that risk themselves.

## Spec 009 — `009-account-pipeline.md`

- Package: `internal/account/`. Covers the design 3.2 create flow and the 7.1 aggregation.
  Depends on: 001, 004, 005, 006.
- Scope:
  - The **module interface**: an `Observe` half for drift detection and an `Apply` half, plus the
    shared context passed to each module (the CRD, the resolved name, the region entry, the
    connections).
  - **Four outcomes, not two**: `Done` | `Pending(reason)` | `Rejected(userErr)` |
    `Failed(systemErr, abort)`. The design forces this: 4.3 requires an outstanding identity sync to
    be *expected* rather than a failure, so it must not abort the pipeline, and 3.8/3.9 require a
    rejected rule to leave the account on its baseline, be reported on `Synced`, and **not** prevent
    later modules from running.
  - Ordered execution with per-module status recorded on the resource.
  - **Non-uniform condition aggregation**, which must be spelled out explicitly:
    `IdentitySynced=False` forces `Ready=False` (4.3 — nobody can administer the account until the
    `ACCOUNTADMIN` group is imported), whereas `QuotaAvailable=False` leaves `Ready=True` (3.10 —
    the account is fully intact and warehouses are merely suspended).
  - The custom condition types themselves (`QuotaAvailable`, `IdentitySynced`) and the mapping from
    module outcomes onto `Ready` and `Synced`.
  - **`Observe` semantics are the most under-specified area of design.md** — it never says what to
    read back (`SHOW PARAMETERS`? `SHOW NETWORK RULES`?). This spec must define the contract; each
    module spec then defines its own drift check within that contract.
- **Hard rule**: `internal/account` must never import `internal/account/modules/…`. There is no
  `DefaultModules()` or `NewPipeline()` convenience constructor — registration lives in 018, and
  pipeline tests use fake modules.

## Spec 010 — `010-account-module.md`

- Package: `internal/account/modules/account/`. Covers design 3.6 step 1.
  Depends on: 003, 004, 005, 006, 009.
- Scope:
  - Generate the RSA keypair and **store it in ASM before** issuing `CREATE ACCOUNT`, so that a
    failure cannot orphan an account whose credentials were never persisted.
  - `CREATE ACCOUNT '<resolved-name>' ADMIN_NAME='platform'
    ADMIN_RSA_PUBLIC_KEY='<generated>' ADMIN_USER_TYPE='SERVICE' EDITION='ENTERPRISE'
    REGION='<region-from-crd>' COMMENT='<description-from-crd>'`, issued over the **org-admin**
    connection. This is the only module that needs org-level privileges.
  - Write `status.accountName` (the resolved name) and `status.accountUrl`.
  - Drift / `Observe`: does the account exist under its resolved name.
- Note: the `platform` user created here is how the platform reaches the account for every subsequent
  operation. Appendix B X1 records that a tenant holding `ACCOUNTADMIN` can drop or re-key it.

## Spec 011 — `011-parameter-module.md`

- Package: `internal/account/modules/parameter/`. Covers design 3.6 step 2a and Appendix B C1.
  Depends on: 005, 007, 009.
- Scope:
  - `ALTER ACCOUNT SET <parameter-name> = '<value>'`, one statement per entry in `globalParameters`
    (the org-wide baseline) and **then** one per entry in `regionalParameters`. Order matters: the
    region comes last.
  - Drift: read the parameters back and re-apply any that have diverged.
- Appendix B C1 note: the parameter set is open-ended and operator-owned, so a future Organization
  Policy must be able to pin **arbitrary** parameter names rather than a fixed allowlist.

## Spec 012 — `012-network-module.md`

- Package: `internal/account/modules/network/`. Covers design 3.6 steps 2b and 2c, 3.8, and
  Appendix B N1–N3. Depends on: 005, 007, 009.
- Deliberately a single spec: `PLATFORM_ACCOUNT_POLICY` is *created* in 3.6/2c and *appended to* in
  3.8, so splitting would place one spec's object under another spec's mutation. The `CUSTOM_` prefix
  convention that keeps the two rule sets apart must also be defined in one place.
- Scope — the baseline (3.6):
  - One `CREATE NETWORK RULE <connection-name> TYPE=<type-from-inventory> MODE=INGRESS
    VALUE_LIST=(<vpceId and/or resolved allowedIPs>)` per `regionalAllowlist` entry, named by the
    bare connection. Use the entry's `allowedIPs` if given, otherwise inherit the connection's full
    `maxCidrs`.
  - `CREATE NETWORK POLICY PLATFORM_ACCOUNT_POLICY ALLOWED_NETWORK_RULE_LIST=(<all connections>)`,
    followed by `ALTER ACCOUNT SET NETWORK_POLICY='PLATFORM_ACCOUNT_POLICY'`.
- Scope — custom rules (3.8):
  - `accountWide` entries become `CREATE NETWORK RULE CUSTOM_<connection>` followed by
    `ALTER NETWORK POLICY PLATFORM_ACCOUNT_POLICY ADD ALLOWED_NETWORK_RULE_LIST=(…)`. The `CUSTOM_`
    prefix keeps them distinct from the `regionalAllowlist` rules, which are named by bare
    connection.
  - `serviceUsers` entries become one rule per entry in that user's list, collected into a **single
    dedicated policy per user**, followed by
    `ALTER USER '<service-user>' SET NETWORK_POLICY='<user-policy>'`. One policy per user, because a
    user can only have one active `NETWORK_POLICY` at a time and because a user-scoped policy
    **fully overrides** the account-level default — so each user's policy is built exclusively from
    the connections they were explicitly granted. Service users only; never human users.
  - **Deny-by-default**: a service user with no `customNetworkRules.serviceUsers` entry gets an empty
    policy, completely blocking login. Securing service users is the highest-value part of this
    module — their long-lived credentials are the platform's biggest risk, so they must be given
    tight explicit ingress paths rather than the broad ranges intended for humans.
  - **Strict containment**: every `allowedIPs` entry must fall entirely within that connection's
    `maxCidrs`, using 007's helper. Anything broader or outside is rejected.
  - Resolve the guardrail `"full"` rule into concrete CIDRs here (deferred from 008).
  - **No duplicate connections**: a connection may appear at most once per user list and at most once
    under `accountWide`. A repeat within a scope is a validation error, not a silent merge.
  - Rejection behavior: because custom rules run **after** bootstrapping, the account already exists.
    It keeps the 3.6 baseline policy, the offending rule is not created, and the failure is reported
    on `Synced` until the tenant fixes the CRD. Later modules still run.
- Appendix B N1–N3 note: today's `ALTER ACCOUNT SET NETWORK_POLICY` and
  `ALTER USER … SET NETWORK_POLICY` are tenant-alterable. Organization Policies will make the binding
  org-owned and make the empty default native, closing the window between `CREATE USER` and policy
  attachment.

## Spec 013 — `013-auth-module.md`

- Package: `internal/account/modules/auth/`. Covers the design 3.6 authentication policies, 3.9, and
  Appendix B A1/A2. Depends on: 005, 009.
- Scope:
  - Create the **SSO-only baseline** authentication policy, with `AUTHENTICATION_METHODS` restricted
    to `SAML`/`OAUTH`, and bind it to the account. SSO is the only login method for human users; it
    is integrated globally, so this is a per-account binding of a global capability.
  - Create the **three fixed exception policies** — `PLATFORM_AUTH_KEYPAIR`, `PLATFORM_AUTH_PAT` and
    `PLATFORM_AUTH_KEYPAIR_PAT`. The two flags yield three method combinations, so three policies
    cover every possible exception; the both-methods case needs its own policy because a user can
    hold only one `AUTHENTICATION_POLICY` at a time. They are created here, during bootstrapping,
    rather than on demand.
  - Bind users per `customAuthRules.exceptions` with
    `ALTER USER '<user>' SET AUTHENTICATION_POLICY=<matching-fixed-policy>`, overriding the
    account-level baseline for that user only.
  - Validation: an entry naming **neither** method is a validation error. `reason` is recorded in the
    CRD for audit and is deliberately **not** carried into Snowflake. Service users are out of scope
    here — they are governed by their own network policies (012).
  - Rejection behavior mirrors 012: a rejected entry leaves the user on the SSO-only baseline and is
    reported on `Synced`.
- Appendix B A1/A2 note: today an account admin can unset either binding, or alter a policy to
  re-admit a method. Organization Policies will make both the baseline and the per-user overrides
  org-owned.

## Spec 014 — `014-identity-sync-request.md`

- Packages: `apis/identity/v1alpha1/` (IdentitySyncRequest), `internal/identitysync/`. Covers design
  4.1–4.3. Depends on: 001, 006, 007.
- Concept: before groups can be imported into a local account they must already exist in the central
  Snowflake **organization** account. Where the enterprise does not sync them there by default, the
  platform emits an `IdentitySyncRequest` as a standardized, decoupled interface. **This platform
  ships the emitting side and the contract only** — a company-specific controller fulfills it, which
  is why the resource lives in its own API group (`base.identity.yukimi.io`) rather than alongside the
  Snowflake kinds.
- Scope — the CRD: `spec.provider` (the integration key from `identityIntegration.groups`) and
  `spec.groups[]`, plus the `Ready` contract through which the fulfilling controller reports.
- Scope — emitter behavior:
  - **Gated on config**: requests are emitted only when `backplane.identitySync.enabled` is true.
    When it is false, groups are assumed to be present org-wide already and are imported directly.
  - **One request per integration key**: each key under `identityIntegration.groups` yields its own
    request, named `<crd-name>-<provider>-identities` in the account's own namespace. It is derived
    from the CRD's `metadata.name` rather than the resolved name, which contains underscores that
    RFC1123 forbids.
  - **Emitted early and never blocking**: requests go out on first observation of the
    SnowflakeAccount, *alongside* bootstrapping rather than after it, so that a sync measured in tens
    of minutes overlaps account creation. The controller returns immediately and picks up progress on
    later reconciles — it is a passive observer and never waits inside a reconcile.
  - **Existence is desired state**: each request carries an owner reference so that it is
    garbage-collected with the account; a request deleted while its groups are still needed is
    recreated; and removing an integration key from the CRD deletes its request for good.
  - **Grace period**: while a request is outstanding and within `identitySync.timeout` (default 1h)
    the reason is `SyncPending` — an expected provisioning state with **no** warning event. Past the
    timeout it becomes `SyncTimeout` **with** a warning event so that ops can see the stall. The
    clock starts when the account's first request is emitted and is recorded in status. `SyncTimeout`
    is a reporting state, not a stop: reconciliation continues and returns to success on its own if
    the sync lands.
  - Only `Ready=True` is consumed; asynchronous fulfilment is the expected case, not a fault.
- **Blocker**: this group cannot be scaffolded. `hack/helpers/addtype.sh` emits
  `package {{GROUP}}`, so `GROUP=base.identity` yields `package base.identity`, which does not
  compile. Hand-create `apis/identity/v1alpha1/` with `Group = "base.identity.yukimi.io"`. The CRD
  must still ship in `package/crds/` even though no controller in this repository fulfills it.

## Spec 015 — `015-identity-module.md`

- Package: `internal/account/modules/identity/`. Covers design 3.7 and the 4.3 waiting rules.
  Depends on: 005, 007, 009, 014.
- Scope:
  - `ALTER ACCOUNT ADD ORGANIZATION USER GROUP '<group>'` per group under
    `identityIntegration.groups`, then `GRANT ROLE <system-role> TO ROLE "<group>"` per `roleBindings`
    entry. This is what lets users log in via SSO with their existing company roles carried over.
  - `ACCOUNTADMIN` must be bound, so that the account is manageable after creation.
  - **Incremental import**: as each `IdentitySyncRequest` reports `Ready=True`, that provider's
    groups are imported and every `roleBindings` entry whose group is now present is granted. A slow
    provider must not hold back a fast one.
  - Returns `Pending` — never `Failed` — while requests are outstanding. These statements only
    succeed once the groups exist in the org account, so the account waits rather than failing.
  - Aggregates the `IdentitySynced` condition: `True` once every request is fulfilled and every group
    imported. Per 009, `IdentitySynced=False` holds `Ready=False`, carrying either `SyncPending` or
    `SyncTimeout` to distinguish a benign wait from a provisioning failure.

## Spec 016 — `016-quota.md`

- Package: `internal/quota/`. Covers design 3.10. Depends on: 005, 006, 009.
- One spec with **three entry points**, because 3.10 spans three phases and splitting them would put
  the same arithmetic in two documents. It lives in `internal/quota/` rather than under `modules/`
  because it is called from two phases, not only from the pipeline, and it is kept out of 018 so that
  the arithmetic is unit-testable without a controller, per CLAUDE.md's thin-controller rule.
- Scope:
  - **`Admit()` — the admission/validation phase**: on every create or update, list all
    `SnowflakeAccount` resources in the namespace, sum their claimed `creditQuota`, and compare the
    total against the namespace's `credit-quota` label (set by ops at onboarding and read through
    `internal/tenant`). Exceeding the allowance is rejected with a validation error. The label is the
    trust anchor: it lives outside the tenant's Git repository, so teams cannot raise it themselves.
  - **First-come-first-served on reductions**: if ops lowers the namespace allowance, existing
    accounts are **never retroactively suspended**, but future creates and updates are blocked until
    the tenant lowers their claims to fit.
  - **`Apply()` — enforcement**: push the approved quota into Snowflake as an account-level resource
    monitor and budget limit. The resource monitor suspends warehouses when the quota is exhausted,
    physically stopping most spend in real time.
  - **`Observe()` — exhaustion**: surface the `QuotaAvailable` condition — `True` while credits
    remain, and on exhaustion `False` with reason `QuotaExhausted` plus a matching warning event.
    This is **not** a provisioning failure: the account remains fully intact and `Ready` stays
    `True`. It clears automatically at the start of the next monthly billing cycle.
- Known gap to record (a design TODO): resource monitors only cover warehouse compute. Serverless
  features and AI functions cannot be suspended this way, so budgets for them are notify-only. The
  options under consideration are waiting for native org-level spending limits, gating access to
  serverless and AI features, or custom privilege-revocation logic. Appendix B S1/S2 track the asks.

## Spec 017 — `017-deletion-request.md`

- Packages: `apis/base/v1alpha1/` (SnowflakeDeletionRequest), `internal/deletion/`,
  `internal/controller/snowflakedeletionrequest/`. Covers design 6.1–6.3. Depends on: 001, 006.
- Concept — **Positive Control, a "two-key" system**: a resource cannot be destroyed merely by
  deleting its definition file. Deletion is a privileged operation requiring a dedicated "deletion
  warrant". The lock is a finalizer; the key is a `SnowflakeDeletionRequest` authorizing the
  destruction of one specific target. This exists to prevent catastrophic data loss through
  accidental Git operations.
- Scope:
  - CRD: `spec.targetRef{kind, name}` (the name is the CRD name, not the resolved name),
    `spec.duration` (a maintenance window, maximum 8h), and `spec.reason` (for example a ticket
    number). Status: `validUntil` and `state`.
  - **Phase 1 — validation**: verify `duration` ≤ 8h, compute `status.validUntil` from the creation
    timestamp, and set `status.state = Active`. Once `validUntil` passes unused the state becomes
    `Expired` and it no longer authorizes anything — a new request is required. **Time-boxing**
    prevents long-standing dangling permissions.
  - The lookup used by 018: find an `Active` request in the same namespace targeting a specific
    resource.
  - The status transition to `Consumed` after a successful deletion, which is written by 018.
  - **A durable audit trail**: the request outlives its target, linking the destruction to a reason
    and a timeframe for compliance.
  - Validate `targetRef.kind` against an explicit allowlist. Recommendation: **`SnowflakeAccount`
    only for v1alpha1** — dropping a replication group destroys no data — so that widening later is
    purely additive. Design 6.2 says "every critical resource", but 6.3 only describes the account
    interaction.
- **Open question to raise when writing this spec**: design 6.2 names a `snowflake.finalizer`, but
  Crossplane's managed reconciler already owns `finalizer.managedresource.crossplane.io` and will not
  remove it until `Delete` returns success. The block is therefore naturally implemented as a
  `Delete` that returns a user error when no Active warrant exists — and `Terminating`,
  `DeletionBlocked` and `Ready=False` all follow from that. A second finalizer would add a second
  removal path to get wrong. This is likely a design.md correction.

## Spec 018 — `018-snowflakeaccount-controller.md`

- Package: `internal/controller/snowflakeaccount/`. Covers design 3.2, the 3.3–3.4 enforcement
  points, 6.3 Phases 2–3, and chapter 7. Depends on: 002, 005–017.
- **Thin orchestration only** — no business logic of its own. Every Snowflake interaction lives in a
  module (010–013, 015) or in quota (016), and validation lives in 007 and 008.
- Scope:
  - **Module registration and ordering.** This is where the concrete modules are wired, which is what
    keeps `internal/account` free of any import of `modules/`. The order follows the 3.2 create flow:
    account bootstrapping (010) → parameters (011) → network (012) → auth (013) → identity (015) →
    quota (016). Note that 3.2's diagram shows identity before network; identity requests are emitted
    early and are non-blocking, so the import step's position is flexible while request emission
    happens alongside bootstrapping per 4.3.
  - **The validation phase**, in order: guardrails (008) → approved exceptions on rejection (008) →
    the region's `available` gate (007) → quota admission (016) → immutability, which is mostly
    enforced by CEL in 006.
  - Pipeline execution via 009, then reporting: the aggregated `Ready` and `Synced`, the custom
    `QuotaAvailable` and `IdentitySynced` conditions, `status.accountName` and `status.accountUrl`,
    and the warning events (`QuotaExhausted`, `SyncTimeout`, `DeletionBlocked`) via
    crossplane-runtime's injected `event.Recorder`.
  - **The deletion gate (6.3 Phases 2–3)**: on `deletionTimestamp`, query for an Active warrant in
    the same namespace targeting this resource. If one is found, run `DROP ACCOUNT` over the
    org-admin connection, release the finalizer, and mark the request `Consumed`. If it is absent or
    expired, refuse: stall in `Terminating`, emit `Warning: DeletionBlocked`, and set `Ready=False`
    so that ArgoCD reports failure, forcing the user either to restore the file or to create a valid
    request.
  - Error handling per CLAUDE.md: in `Observe`, call `log.Handle(err)`, set
    `xpv1.Unavailable().WithMessage(...)`, and return nil to avoid a retry flood; in `Create`,
    `Update` and `Delete`, return the handled error and let the framework set conditions.

## Spec 019 — `019-replication.md`

- Packages: `apis/base/v1alpha1/` (SnowflakeReplication), `internal/replication/`,
  `internal/controller/snowflakereplication/`. Covers design 5.1–5.4.
  Depends on: 001, 004, 005, 006.
- Scope:
  - CRD: `description`, `accounts[]` (SnowflakeAccount **CRD** names, not resolved names),
    `primaryAccount`, `objectTypes[]` (for example DATABASES, WAREHOUSES), `databases[]` (supporting
    wildcards such as `PROD_*`), and `schedule` (for example `"10 MINUTE"`). Exactly one primary.
  - **Validation**: every account under `accounts` must declare the same `environment`; a mismatch is
    rejected, because linking a `prod` account to a `dev` one would replicate production data into an
    account held to the looser `dev` network posture. Since `environment` is immutable, a group that
    validates at setup cannot later drift into a mixed state.
  - **No region-pair validation** (5.2), deliberately. Each linked account was already restricted to
    a legally permitted region by the guardrails' `allowedRegions` at creation time, so an illegal
    pair cannot arise — it would require an account to exist in a region guardrails would have
    rejected.
  - **Infrastructure is never replicated** — only customer data and logical objects. Network rules
    and endpoints stay regional, or regional connectivity breaks.
  - **Native Snowflake execution**: the controller does not manage the ongoing sync. At setup it
    provisions a stored procedure and a scheduled task inside the primary account and hands over.
    That native setup runs on the schedule, resolves database wildcards, and updates the replication
    group as the environment changes.
  - **Auto-repair**: tenants have access to their own Snowflake environment and can break the
    replication code. On detecting errors or drift, the controller repairs by completely removing and
    recreating the stored procedure and the task.
  - **Manual failover only**: a failover happens only when a tenant explicitly changes
    `primaryAccount` in Git, which prompts the controller to promote the new primary. Never
    automatic — that would risk split-brain corruption from a transient network blip.

---

## Deliberately unnumbered

- **3.11.2 OIDC** (a design TODO). An optional additional authentication path: a per-namespace
  Kubernetes ServiceAccount whose `TokenRequest` JWT maps to a `PLATFORM_OIDC` Snowflake user via the
  `sub` claim, avoiding an ASM read per connection, with the ASM keypair remaining as a fallback. It
  is blocked on two unresolved questions: the precise fallback trigger, and the cluster-scoped RBAC
  model that would let the controller mint tokens across every tenant namespace. It would also add an
  org-level issuer/JWKS entry to 007's schema and a ServiceAccount lifecycle.
- **3.10's serverless and AI spend cap** (a design TODO) — see spec 016.
- Both take the next free number when their TODO closes, rather than reserving a forward reference
  now.
- **Chapter 2 (Tenant Onboarding)** is out of scope entirely: it is ops tooling (bash today,
  Terraform later) that creates the namespace, labels, repository and ArgoCD application. The
  platform only *reads* the labels it produces (006).

## References

- **Product design**: `specs/design.md` — the authoritative product requirements, resource schemas
  and behavior specifications this roadmap decomposes.
- **Spec template**: `specs/000-template.md` — the section skeleton every numbered spec follows.
- **Shape reference**: `specs/001-error-and-logging.md` — the one spec written so far.
