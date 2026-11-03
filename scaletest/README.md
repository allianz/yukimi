# Scale test

Creates SnowflakeAccounts in growing steps, up to 10,000, to find where the system reaches its limits. It works in
steps: scale up until something fails, look at what broke, fix or tune, continue from where it stopped.

- The accounts live in the namespace `scaletest` and stay there. **A run never deletes anything**, also not after a
  failure or Ctrl-C, so the state that broke can be inspected.
- `SCALETEST_ACCOUNTS` is the total number of accounts that should exist, not the number to add. A run creates only
  the missing ones, and first waits for accounts of an earlier run that are not ready.
- It runs against the local `make dev` setup, AWS Secrets Manager and a real Snowflake organization, and is behind
  the `scaletest` build tag, so `make test` never compiles it.

## Running

Preconditions: `make dev` is running (kind cluster, CRDs, operator, generated configuration in `_output/config`),
the current kube context is the one of `make dev` (`kind-yukimi-dev`), and AWS credentials with access to the
operator's secrets are available (`AWS_PROFILE=<profile>`).

```bash
make scaletest-check                                           # checks that a run can start; creates nothing
SCALETEST_ACCOUNTS=10 SCALETEST_CONFIRM=10 make scaletest      # brings the namespace up to 10 accounts
```

The run refuses to use any other kube context, because it creates real accounts on the current cluster. To use
another one on purpose, set `SCALETEST_ALLOW_CONTEXT=<that context>`. The client is pinned to the context that was
checked, so switching the context in another terminal while the run is going changes nothing.

| Variable | Meaning | Default |
|---|---|---|
| `SCALETEST_ACCOUNTS` | total number of accounts that should exist after the run, at most 10000 | required |
| `SCALETEST_CONFIRM` | must repeat `SCALETEST_ACCOUNTS`; without it the run refuses to start | required |
| `SCALETEST_BATCH` | how many accounts are applied at once | 10 |
| `SCALETEST_BATCH_INTERVAL` | pause between two batches, for example `30s` | 0 |
| `SCALETEST_READY_TIMEOUT` | how long an account may take to become ready before the run stops | 30m |
| `SCALETEST_ERROR_GRACE` | how long an account may report an error (`Synced=False`) before the run stops; the operator retries on its own, so a short error is not a failure | 5m |
| `SCALETEST_DEGRADE_GRACE` | how long an account that was ready may stay not ready before the run stops; longer than the error grace, because with thousands of accounts the operator needs minutes to come back to one | 15m |
| `SCALETEST_START_TIMEOUT` | how long an account may have no condition at all before the run stops, which means the operator does not react to it | 5m |
| `SCALETEST_ALLOW_CONTEXT` | a kube context besides `kind-yukimi-dev` that the run may use | none |
| `SCALETEST_CONFIG_DIR` | operator configuration directory | `_output/config` |
| `SCALETEST_TIMEOUT` (make) | `go test -timeout`; 0 means none | 0 |

The full output is also written to `_output/scaletest/run.log`.

The tests that create accounts run only when `SCALETEST_ACTION` is set, which the make targets do. A plain
`go test -tags scaletest ./scaletest` therefore runs only the unit tests.

## What a run does

1. It checks the settings, the operator configuration, the kube context, the cluster and the AWS credentials.
2. It creates the namespace `scaletest` with the labels the controller reads, or fixes the labels of an existing one.
3. It waits for the accounts of an earlier run that are not ready. It refuses to go on while an account is being
   deleted. The resumed accounts are counted, but left out of the timing statistics, since they started long ago.
4. It creates the missing accounts `scaletest-00001`, `scaletest-00002`, ... in batches, and waits for each batch to
   become ready before it applies the next one.
5. It **stops at the first batch with a failed account** and leaves everything as it is. An account has failed when
   it reports an error for longer than `SCALETEST_ERROR_GRACE`, when the operator sets no condition on it within
   `SCALETEST_START_TIMEOUT`, when it is not ready within `SCALETEST_READY_TIMEOUT`, or when it disappeared from the
   cluster. A listing of the accounts that fails is tried again; five failures in a row stop the run. Running the
   same command again continues where the run stopped.
6. All accounts among 1..target stay watched while the later batches are created. One that stops being ready for
   longer than `SCALETEST_DEGRADE_GRACE`, or is gone from the cluster, stops the run too, which is what load can do
   to the earlier accounts. When all batches are ready, or when there was nothing to create, the run waits for any
   account that is still recovering, for the same grace.
7. It prints the time until the operator reacted, until `CREATE ACCOUNT` ran and until the account was ready (p50,
   p95, max), and writes everything per account, with the last error message, the failures and the reason the run
   stopped, to `_output/scaletest/result-<time>.json`. `readyTotal` is the number of ready accounts among 1..target at
   the end, or -1 if they could not be counted. The times are those of the polls (every 10 seconds), so they are
   accurate to that.

## Not done yet

Removing the accounts again, errors grouped by their source (Snowflake, AWS, Kubernetes), a CSV output, and the
metrics of the operator.
