# End-to-end tests

These tests run the Yukimi operator the way a tenant meets it: a `SnowflakeAccount` is applied to a real Kubernetes
API server, the real controller picks it up, stores the credentials in AWS Secrets Manager and creates the account in
Snowflake. The tests then check all three places. Unit and integration tests cover the pieces one by one; a case
belongs here only if it needs the whole chain, or if a failure could leave real resources behind.

The cases are grouped by letter, and every test function starts with its case number, for example
`TestC2_UnknownRegionIsRejectedByTheController`. The groups and their cases are listed under [Test cases](#test-cases).

## Running the tests

You need three things, and the suite checks all of them before it creates anything:

1. `make dev` is running in another terminal. It provides the kind cluster, the CRDs and the operator, and the
   default kubeconfig context points at that cluster.
2. AWS credentials that can read the operator's secrets, through the default credential chain: environment
   variables, or the profile selected with `AWS_PROFILE`.
3. The operator configuration that `make dev` generated in `_output/config`.

```bash
AWS_PROFILE=<profile> make e2e
```

The full test output is printed while the tests run, followed by a summary with one line per case and, for every
failed case, the step that failed and what it said. The same output is written to `_output/e2e/run.log`. The real
Snowflake accounts of a run are created at the same time, so a run takes about as long as the slowest of them.

| Variable | What it does | Default |
|---|---|---|
| `E2E_RUN` (make) | Runs only the tests matching this `go test -run` expression. For example `'TestB([0-9]\|1[01])_\|TestC'` runs the fast cases that create no real account (B12 does create one). | all tests |
| `E2E_VERBOSE` (make) | `1` prints the full test output. `0` prints only one line per case as soon as it has finished. The summary comes at the end either way. | `1` |
| `E2E_TIMEOUT` (make) | The `go test -timeout`. The tests stop 20 minutes before it, so the clean-up always has time to run; it must therefore be longer than 20m. | `60m` |
| `E2E_MAX_ACCOUNTS` | The most real accounts the suite may have at the same time; at most 10. | `10` |
| `E2E_CONFIG_DIR` | Where the operator's `base.yaml` and `backplane.yaml` are. The suite reads the organization and the regions from them. | `_output/config` |
| `E2E_STALE_AFTER` | How old the namespace of an earlier run must be to count as abandoned (see below). | the go test timeout |

Running against a deployed cluster, with the configuration taken from the operator's ConfigMap, is not implemented
yet.

## What a run does

1. **Checks.** The Kubernetes API answers and both Yukimi CRDs are installed; the AWS credentials work and reach the
   org-admin secret; the operator is alive; the configuration has an available region. If one fails, the run stops
   with a message and nothing has been created.
2. **Cleans up after earlier runs.** A run that was killed (for example with `kill -9`) cannot clean up after itself.
   So every run first looks for namespaces of earlier runs (`e2e-<timestamp>`) that are older than `E2E_STALE_AFTER`,
   deletes the accounts in them and drops them. Each one is reported in an `e2e sweep:` line. A namespace that is
   younger, such as one of a run that is still going, is left alone.
3. **Creates one namespace for the run**, `e2e-<timestamp>`, with the labels the controller reads. The Snowflake name
   of an account is derived from its namespace, so every run gets new Snowflake names and the accounts themselves need
   no timestamp.
4. **Runs the tests.** The cases that need no real account (groups B and C) finish within seconds. The cases that do
   need one start their accounts at the same time, so a run takes as long as the slowest account, not the
   sum.
5. **Cleans up its own namespace.** Every account is deleted explicitly, and the run waits until it is gone before it
   drops the namespace. Dropping a namespace while accounts are still in it can leave Snowflake accounts behind, because
   the deletion permits would disappear first (see `support.Env.Cleanup`). The same happens after Ctrl-C; a second
   Ctrl-C aborts without the clean-up.

Two things stay behind after every run, by design. Snowflake keeps a dropped account restorable for the drop grace
period (`deletion.gracePeriodDays`, 7 days in the local configuration). And an account's credential secret is only
scheduled for deletion, never deleted at once, so its name cannot be used again until its recovery window ends; that
is what A5 sees.

The suite always creates a `DeletionPermit` before it deletes an account, so it works with deletion protection on and
off. It checks nothing about permits: not that a deletion is blocked, not that a permit is consumed.

## Known bugs and decided behavior

Some cases test behavior that is a known bug, or that looks like one but is correct. They are listed here, so that a
reader (or a reviewer) does not have to work it out from the test.

**Bugs in the operator.** These cases test the wanted behavior, so they fail until the bug is fixed, and a full run
reports `FAILED` until then.

| Case | Bug |
|---|---|
| **B11** | A description can be added to an account that has none. Snowflake cannot change the comment of an account after creation ([Snowflake article](https://community.snowflake.com/s/article/How-to-Update-Account-Comment)), so the CRD must reject it, and its rule `self == oldSelf` says so. The rule is not evaluated when the old value is absent, so the API server accepts the description, which then never reaches Snowflake: the spec and the account drift. That the controller does not write it later is correct (see B12); the bug is that the CRD allows it. |
| **D3** | A contact with non-ASCII characters creates the account. Most mail systems reject such addresses and the operator cannot check delivery, so it must reject them (this may be opened up later). |

**A bug in Snowflake.** The case passes and records what Snowflake does.

| Case | Bug |
|---|---|
| **D2** | Snowflake changes a bound string: it interprets backslash escapes and doubled quotes inside it, so `a''b` is stored as `a'b`. The operator is right to bind the description: spec 005 requires binding, because a bound value can never change the statement, and accepts that the stored text may differ. |

**Correct behavior that looks like a bug.** These cases pass.

| Case | Why it is correct |
|---|---|
| **B12** | The controller writes the description to the Snowflake comment only at `CREATE ACCOUNT`, because Snowflake cannot change the comment of an account later ([Snowflake article](https://community.snowflake.com/s/article/How-to-Update-Account-Comment)). |
| **A5** | A deleted name stays taken while the credential secret is in its recovery window. The message does not say how long that is; this is accepted for now. |

## Test cases

### A. Lifecycle

The life of one account, from creation to deletion, checked in Kubernetes, AWS and Snowflake. A1 to A3 and B12 share
the account `lifecycle`, whose name has exactly the maximum length the operator allows. A2 uses an account of its own,
and A4 and A5 use the account `delete-flow`.

| # | Scenario | Given | When | Then |
|---|---|---|---|---|
| A1 | A valid account becomes ready | a valid account, name at the length limit | it is applied | `status.accountLocator`, `accountName` (the resolved name) and `accountUrl` are set; `Ready=True`, `Synced=True`; `SHOW ACCOUNTS` lists it; the secret `yk-<org>--<ns>--<name>` exists |
| A2 | A contact change reaches Snowflake | a ready account | `spec.contact` is changed | the email of the platform user follows; `Ready` stays `True` |
| A3 | A settled account causes no further change | a ready account | the operator reconciles it for 3 minutes (three poll intervals) | `accountLocator` and `accountCreatedAt` do not change, so there was no second `CREATE ACCOUNT`; Snowflake still lists the account |
| A4 | Deleting an account removes it everywhere | a ready account | a permit is created and the account is deleted | the CR is gone; the account is gone from Snowflake; the secret has a `DeletedDate` |
| A5 | Recreating a deleted name is refused | the account of A4, just deleted | an account with the same name is applied | the controller says the account was deleted recently and is still within its recovery window; no account is created |

### B. Rejected by the API server

The CRD validates an account before the controller ever sees it. These cases send an account with one wrong value
and expect the API server to reject it with the right message. They use a server-side dry run, so nothing is stored
and no real account is needed. There is one representative case per rule; the full rules are in the CRD.

| # | Scenario | Input | Expected |
|---|---|---|---|
| B0 | The unmodified account is accepted | the valid account every other case starts from | accepted. This proves that a rejection below is caused by the one changed value |
| B1 | Non-ASCII name | `metadata.name: テスト` | rejected; the message names the name rule |
| B2 | Name over the CRD limit | 56 characters | rejected; "55 characters or fewer" |
| B3 | Name boundaries | leading digit, uppercase, `_`, leading or trailing `-`, `--`, a SQL-injection-like name | rejected |
| B4 | Region format | `EU-CENTRAL-1`, `eu-central-1` | rejected |
| B5 | Environment | `staging` | unsupported value |
| B6 | Contact | no `@`, whitespace | rejected |
| B7 | Description too long | 1025 characters | too long |
| B8 | Role bindings | no `ACCOUNTADMIN` | "roleBindings must bind ACCOUNTADMIN" |
| B9 | Required fields | empty region, environment or contact; no role bindings | rejected |
| B10 | Region and environment are immutable | a stored account; its `region` or `environment` is changed | "... is immutable" |
| B11 | A description cannot be added later | the same stored account, which has no description; one is added | "description is immutable". **Fails today**, see [Known bugs](#known-bugs-and-decided-behavior) |
| B12 | The controller never changes the Snowflake comment | the `lifecycle` account, ready; a description is added with a real update (accepted today, see B11), and the operator reconciles it | `SHOW ACCOUNTS` still shows the comment from `CREATE ACCOUNT` |

B10 and B11 need an account that is already stored, because immutability is a rule about a change. They use one in a
region the operator does not know: the controller rejects it at once (see C2), so it never becomes a real account.
B12 is the only case of this group that needs a real account.

### C. Accepted by the API server, rejected by the controller

Some rules can only be checked by the controller, because they depend on the operator's configuration. These cases
send an account that the CRD accepts and the controller must refuse. Every case checks that the refusal is readable in
the conditions and that nothing was created: no secret in AWS and no account in Snowflake. The controller runs these
checks before it stores any credential.

| # | Scenario | Input | Expected |
|---|---|---|---|
| C1 | Name above the operator limit | one character more than the operator allows (the limit depends on the length of the organization name, so it is lower than the CRD's 55) | "account name must be N characters or fewer" |
| C2 | Unknown region | a correctly formatted region that is not in the operator's configuration | "region ... is not yet available". The start-up check that the operator is alive uses the same account |
| C3 | A refused account can be deleted | an account the controller has refused (unknown region); a permit is created and the account deleted | the object is gone within two minutes, although no Snowflake account was ever made |

### D. Inputs that only Snowflake can judge

The CRD can check the shape of a value, but not whether Snowflake accepts it and stores it as sent. Each case creates
a real account with one kind of unusual input and reads back what Snowflake stored. The integration tests of the
account module try many more inputs directly against Snowflake; these cases send one of each kind through the whole
chain.

| # | Scenario | Input | Expected |
|---|---|---|---|
| D1 | Japanese and SQL-injection-like description | Japanese text plus `x'); SELECT CURRENT_USER(); --` | the account is created and the comment read back equals the input. The number of accounts in the organization is not checked, because the other cases create accounts at the same time |
| D2 | Quotes and backslashes in the description | `a''b`, `a\nb`, `a\\b` (typed as these characters) | the account is created; the comment holds `a'b`, a newline and `a\b`. See [Known bugs](#known-bugs-and-decided-behavior) |
| D3 | Non-ASCII contact | `日本@example.com`, which the CRD pattern accepts | refused by the API server or the controller with a readable message; no account in Snowflake. **Fails today**, see [Known bugs](#known-bugs-and-decided-behavior) |
| D4 | Description of the maximum length | 1024 characters | the account is created and the comment read back is complete |

### E. Leaks and orphans

Ways a deletion could leave something behind. Neither case is automated yet.

| # | Scenario | Given / When | Expected |
|---|---|---|---|
| E1 | A failed create leaves no secret | a create fails after the secret was stored; then the CR is deleted | no secret remains. Today it appears to stay, because an account without a locator gets no delete call. Not automated: there is no reliable way yet to make a create fail at that point. Snowflake accepted every unusual contact tried; a duplicate account name would work, but the suite would need its own `CREATE ACCOUNT` as org admin (undecided) |
| E2 | Removing `Delete` from `managementPolicies` | a tenant removes `Delete` and deletes the CR | a known gap: the CR disappears and the Snowflake account stays, without any permit. Tested by hand only, because it leaves a real account behind |

### Not covered

- **Unavailable regions.** The operator refuses a region that is not yet available only for regular tenants. The test
  namespace is an alpha tester, for which availability is not enforced, and the local configuration has a single,
  available region.
- **The other spec fields.** The suite does not look at `creditQuota`, `identityIntegration`, `customNetworkRules`
  and `customAuthRules`. The controller does not act on these fields yet, because the modules that would apply them
  are not implemented, so there is nothing to observe in Snowflake. Of the fields that can change after creation,
  only `contact` is tested (A2).

## Writing a test

- Start the function name with the case number, for example
  `func TestC2_UnknownRegionIsRejectedByTheController(t *testing.T)`. The summary lists the cases by it.
- Structure the test with `support.Given`, `support.When` and `support.Then`, so the output reads as the scenario.
- Use `support.Eventually` for waiting; never sleep.
- A case that needs a real account calls `t.Parallel()` and gets it from a fixture (`env.Fixture(name, build)`, then
  `Start` or `Ready`). The accounts of the different cases are then created at the same time. Several cases may share
  a fixture.
