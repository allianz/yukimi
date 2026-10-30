# End-to-end tests

End-to-end tests of the Yukimi operator: a real Kubernetes API, the real controller, AWS Secrets Manager and
Snowflake. This file lists the test cases: what is tested, in which group, and what is still missing.
The function name of a test starts with its case number.

## What the suite does and does not do

- It takes the cluster that is configured (the default kubeconfig and its current context). It does not create a
  cluster, install CRDs or start an operator; those are preconditions that the suite checks first.
- It creates **one namespace per run** (`e2e-<timestamp>`) with the labels the controller reads. The Snowflake names
  of the accounts in it are unique through the namespace, so accounts need no timestamp.
- It always creates a `DeletionPermit` before it deletes an account, so it works with deletion protection on and
  off. It makes **no assertions about permits**: not that a deletion is blocked, not that a permit is consumed.
- It deletes every account explicitly, waits until it is gone, and only then drops the namespace. Dropping a namespace
  with live accounts in it can leave Snowflake accounts behind (see `support.Env.Cleanup`).
- It is behind the `e2e` build tag, so `make test` never compiles it.

## Running (local mode)

Preconditions, all of which the suite checks before it creates anything:

1. `make dev` is running in another terminal: the kind cluster, the CRDs and the operator. The default kubeconfig
   context points at that cluster.
2. AWS credentials with access to the operator's secrets are available through the default credential chain
   (environment variables, or the profile selected with `AWS_PROFILE`).
3. The operator configuration that `make dev` generated exists in `_output/config`.

```bash
make e2e          # with the AWS profile selected, for example: AWS_PROFILE=<profile> make e2e
```

| Variable | Meaning | Default |
|---|---|---|
| `E2E_CONFIG_DIR` | directory with the operator's `base.yaml` and `backplane.yaml`; the suite reads valid regions, the organization, etc. from it | `_output/config` |
| `E2E_MAX_ACCOUNTS` | most real accounts that exist at the same time without being deleted; at most 10 | 10 |
| `E2E_TIMEOUT` (make) | `go test -timeout`; the tests get this minus 20 minutes, which is kept for the clean-up, so it must be longer than 20m | 60m |
| `E2E_STALE_AFTER` | age from which a namespace of an earlier run counts as abandoned and is cleaned up when a run starts (for example after `kill -9`) | the go test timeout |
| `E2E_RUN` (make) | only the tests matching this `go test -run` expression, for example `'TestB([0-9]\|1[01])_\|TestC'` for the fast cases that create no real account (B12 does create one) | all |
| `E2E_VERBOSE` (make) | also print the full test output | off |

Running against a deployed cluster (configuration from the operator's ConfigMap) is not implemented yet.

## Writing a test

- Start the function name with the case number, for example:
  ```go
  func TestC2_UnknownRegionIsRejectedByTheController(t *testing.T) { ... }
  ```
- Structure the test with `support.Given`, `support.When` and `support.Then`, so the output reads as the scenario.
- Use `support.Eventually` for waiting; never sleep.
- A case that needs a real account calls `t.Parallel()` and gets it from a fixture (`env.Fixture(name, build)`,
  `Start`, `Ready`). The accounts of the different cases are then created at the same time, and a run takes as long
  as the slowest account, not the sum. A fixture may be shared by several cases.

Status values: **planned**, **implemented**, **known-red** (expected to fail today because of a probable defect),
**manual** (not automated).

## Characterizations

Some cases are *characterizations*: they record behavior that is probably a defect and flip when it is fixed.

| Case | What the test records |
|---|---|
| **D2** | A description is not stored as written: Snowflake interprets doubled quotes and backslash escapes in the bound parameter (`a''b` is stored as `a'b`). |
| **B11, B12** | A description can be added to an account that has none, because the immutability rule is not evaluated for an absent old value; and the controller writes the Snowflake comment only at `CREATE ACCOUNT`, so the two drift. |
| **D3** | Japanese characters in `contact` are accepted by the CRD and by Snowflake. |
| **A5** | Recreating a deleted name is refused with a message about the recovery window. |

## Test cases

### A. Lifecycle (1 real account: `lifecycle`, named with exactly the maximum length)
| # | Scenario | Given | When | Then | Status |
|---|---|---|---|---|---|
| A1 | A valid account becomes ready | valid account, name at the length limit | applied | within the timeout `status.accountLocator`, `accountName` (the resolved name) and `accountUrl` are set; `Ready=True`, `Synced=True`; `SHOW ACCOUNTS` lists it; secret `yk-<org>--<ns>--<name>` exists | implemented (`TestA1_AValidAccountBecomesReady`) |
| A2 | A contact change reaches Snowflake | A1 done | `spec.contact` changed | the platform user's email follows; `Ready` stays `True` | implemented (`TestA2_AContactChangeReachesSnowflake`) |
| A3 | A settled account causes no further change | A1 done | the operator reconciles it for 3 minutes (three poll intervals) | `accountLocator` and `accountCreatedAt` unchanged, so no second `CREATE ACCOUNT`; Snowflake still lists the account | implemented (`TestA3_ASettledAccountCausesNoFurtherChange`) |
| A4 | Deleting an account removes it everywhere | A1 done | a permit is created and the account deleted | the CR is gone; the account is gone from Snowflake; the secret has a `DeletedDate`. Nothing is asserted about the permit | implemented (`TestA4_DeletingAnAccountRemovesItEverywhere`) |
| A5 | Recreating a deleted name is refused | A4 done | the same name is applied again | the controller says the account was deleted recently and is still within its deletion recovery window; no account is created | implemented (`TestA5_RecreatingADeletedNameIsRefused`) |

### B. Rejected by the API server (no real account; server-side dry-run, nothing persisted)
One representative case per rule.

| # | Scenario | Input | Expected | Status |
|---|---|---|---|---|
| B0 | The unmodified account is accepted | the valid account used as the starting point of every case | accepted; anchors the cases below, so a rejection is about the one changed value | implemented (`TestB0_AValidAccountIsAccepted`) |
| B1 | Non-ASCII name | `metadata.name: テスト` | invalid; message names the name rule | implemented (`TestB1_NonASCIINameIsRejected`) |
| B2 | Name over the CRD limit | 56 characters | invalid; "55 characters or fewer" | implemented (`TestB2_NameOverTheCRDLimitIsRejected`) |
| B3 | Name boundaries | leading digit, uppercase, `_`, leading or trailing `-`, `--`, SQL-injection-like name | invalid | implemented (`TestB3_NameBoundariesAreRejected`) |
| B4 | Region format | `EU-CENTRAL-1`, `eu-central-1` | invalid | implemented (`TestB4_RegionFormatIsRejected`) |
| B5 | Environment | `staging` | unsupported value | implemented (`TestB5_UnknownEnvironmentIsRejected`) |
| B6 | Contact | no `@`, whitespace | invalid | implemented (`TestB6_InvalidContactIsRejected`) |
| B7 | Description too long | 1025 characters | too long | implemented (`TestB7_TooLongDescriptionIsRejected`) |
| B8 | Role bindings | no `ACCOUNTADMIN` | "roleBindings must bind ACCOUNTADMIN" | implemented (`TestB8_RoleBindingsWithoutAccountAdminAreRejected`) |
| B9 | Required fields | empty region, environment, contact; no role bindings | invalid | implemented (`TestB9_EmptyRequiredFieldsAreRejected`) |
| B10 | Immutable fields | on a stored account in an unknown region (the controller rejects it, so no real account is needed) change `region` or `environment` (dry-run patch) | "... is immutable" | implemented (`TestB10_RegionAndEnvironmentCannotBeChanged`) |
| B11 | `description` can be added later (CRD half of the gap) | add a description to the same stored account, which has none (dry-run patch) | accepted: a CEL transition rule is not evaluated when the old value is absent. Characterization of a gap; flips when fixed | implemented (`TestB11_ADescriptionCanBeAddedLaterButThenNeverChanged`) |
| B12 | The controller never updates the Snowflake comment (drift half of the gap) | on the `lifecycle` account add a description with a real update, wait for the reconcile | `SHOW ACCOUNTS` still shows the old comment, so the spec and Snowflake drift. Characterization; flips when fixed. | implemented (`TestB12_TheSnowflakeCommentIsNotUpdatedFromTheDescription`) |

### C. Accepted by the API server, rejected by the controller (needs the operator; nothing may be created)
Every case asserts: a readable condition message, no secret (`DescribeSecret` not found), no account in Snowflake.
The checks run before any credential is stored (`checkCreatable` runs before `acquireCredentials`).

| # | Scenario | Input | Expected | Status |
|---|---|---|---|---|
| C1 | Name above the operator limit | limit + 1 characters (up to 55) | "account name must be N characters or fewer", N computed from the organization | implemented (`TestC1_NameAboveTheOperatorLimitIsRejectedByTheController`) |
| C2 | Unknown region | valid format, not in the operator's backplane configuration | "region ... is not yet available" (also the basis of the operator-alive check at start-up) | implemented (`TestC2_UnknownRegionIsRejectedByTheController`) |
| C3 | An object that never got a locator can be deleted | an account the controller has rejected (unknown region), a permit created, deleted | the object is gone within two minutes | implemented (`TestC3_AnAccountThatNeverGotALocatorCanBeDeleted`) |

Not covered: an unavailable region is rejected for regular tenants only. The test namespace is an alpha tester (so
availability is not enforced) and the local configuration contains a single, available region.

### D. Inputs that only Snowflake can judge (4 real accounts, one per row)
The CRD regex cannot tell what Snowflake accepts. One pass per input class through the whole chain; the full fuzz set
stays in the existing integration tests.

| # | Scenario | Input | Expected | Status |
|---|---|---|---|---|
| D1 | Japanese and SQL-injection-like description | `description`: Japanese text plus `x'); SELECT CURRENT_USER(); --` | account created; the comment read back equals the input (the count of accounts in the organization is not checked: the cases create accounts at the same time) | implemented (`TestD1_JapaneseAndSQLInjectionLikeDescriptionIsStoredAsText`) |
| D2 | Quote and backslash handling | `a''b`, `a
b`, `a\b` (typed as these characters) | recorded, not wanted: Snowflake interprets them in the bound parameter, so the comment holds `a'b`, a newline and `a`; the description is not stored as the tenant wrote it | implemented, characterization (`TestD2_QuotesAndBackslashesAreInterpretedByTheBoundParameter`) |
| D3 | Unicode in `contact` | `日本@example.com` (passes the CRD pattern) | recorded: Snowflake accepted it and the account was created. The test accepts a failure as well, if it is readable and leaves no Snowflake account | implemented, characterization (`TestD3_UnicodeInTheContactEndsInAKnownOutcome`) |
| D4 | Description of exactly 1024 characters | 1024 characters | created; the comment read back is complete | implemented (`TestD4_ADescriptionOfTheMaximumLengthIsStoredCompletely`) |

### E. Leaks and orphans
| # | Scenario | Given / When | Expected | Status |
|---|---|---|---|---|
| E1 | A failed create leaves no secret | a create that fails after the secret was stored (trigger to be found; D3 may provide it), then the CR is deleted | no secret remains. Today it appears to stay, because no locator means no delete call | known-red, no trigger yet: Snowflake accepted every contact tried (`x@y..z`, `x@-.-`, 1000 characters, a 300 character domain label), and a unique name cannot collide. A deterministic trigger needs a duplicate account name, which the suite can only make with a `CREATE ACCOUNT` of its own as org admin (undecided) |
| E2 | Removing `Delete` from `managementPolicies` | a tenant removes `Delete` and deletes the CR | documented gap: the CR disappears and the account stays, with no permit. Leaves a real account behind | manual, not automated |

### Not covered

Besides the exclusions above, the suite does not look at `creditQuota`, `identityIntegration`, `customNetworkRules`
and `customAuthRules`. The controller does not act on these fields yet, because the modules that would apply them
are not implemented, so there is nothing to observe in Snowflake. Of the fields that can change after creation,
only `contact` is tested (A2).
