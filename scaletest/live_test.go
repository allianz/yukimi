//go:build scaletest

/*
Copyright 2026 The Yukimi Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package scaletest

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

// The tests in this file run against the real cluster, AWS and Snowflake. They run
// only through the make targets, which set SCALETEST_ACTION; a plain `go test` or a
// click in the IDE skips them, so they can never create or delete real accounts by
// accident.

// requireAction skips the test unless the run is for one of the given actions. A
// value that is no action at all, such as a typo, fails the test: a run that skips
// everything must not look like a success.
func requireAction(t *testing.T, actions ...string) string {
	t.Helper()
	got := os.Getenv(EnvAction)
	if got != "" && !slices.Contains([]string{"create", "delete"}, got) {
		t.Fatalf("%s is %q; it must be create or delete (the make targets set it)", EnvAction, got)
	}
	if !slices.Contains(actions, got) {
		t.Skipf("%s is %q; the make targets set it to %v", EnvAction, got, actions)
	}
	return got
}

// setup loads the configuration, connects and runs the start-up checks. It
// creates nothing. The test fails, with a message that says what to do, if the
// run cannot start.
func setup(t *testing.T, action string) *Env {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	load := LoadConfig
	if action == "delete" {
		load = LoadDeleteConfig
	}
	cfg, err := load()
	if err != nil {
		t.Fatalf("configuration: %v", err)
	}
	env, err := Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("connection: %v", err)
	}
	awsAccount, err := env.Check(ctx)
	if err != nil {
		t.Fatalf("start-up check: %v", err)
	}
	existing, err := env.ExistingAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("target: kube context %q, AWS account %s, organization %q, configuration %s",
		env.Context, awsAccount, cfg.Org(), cfg.Dir)
	if action == "delete" {
		t.Logf("accounts in namespace %q: %d existing, all of them will be deleted (batch %d, interval %s)",
			Namespace, existing, cfg.Batch, cfg.BatchInterval)
	} else {
		t.Logf("accounts in namespace %q: %d existing, %d wanted (batch %d, interval %s); missing: %d",
			Namespace, existing, cfg.Accounts, cfg.Batch, cfg.BatchInterval, max(cfg.Accounts-existing, 0))
	}
	return env
}

// TestPreflight checks that the run can start: valid settings, the operator
// configuration, the kube context, a reachable cluster and working AWS
// credentials. It creates nothing.
func TestPreflight(t *testing.T) {
	action := requireAction(t, "create", "delete")
	setup(t, action).Close()
}

// TestCreate brings the number of accounts in the namespace up to
// SCALETEST_ACCOUNTS. It creates real accounts and deletes nothing, also not
// after a failure or Ctrl-C, so the state that broke can be inspected. Running it
// again continues where it stopped. It runs the start-up checks of TestPreflight first.
func TestCreate(t *testing.T) {
	requireAction(t, "create")
	env := setup(t, "create")
	defer env.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	result, runErr := env.Run(ctx, t.Logf)

	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	path, err := result.Write(filepath.Join(root, "_output", "scaletest"))
	if err != nil {
		t.Errorf("writing the result: %v", err)
	}

	t.Logf("result: %d of %d accounts ready (%d existed before; %d applied or resumed in this run, %d of them ready); file %s",
		result.ReadyTotal, result.Target, result.Existing, result.Applied, result.Ready, path)
	t.Logf("until the operator reacted: %s", result.ToFirstCondition)
	t.Logf("until CREATE ACCOUNT ran:   %s", result.ToLocator)
	t.Logf("until ready:                %s", result.ToReady)
	if runErr != nil {
		t.Fatalf("the run stopped: %v", runErr)
	}
}

// TestDelete deletes every account in the namespace and checks that Snowflake and
// AWS Secrets Manager are clean afterwards. It is the end of the scale test and runs
// only through `make scaletest-delete`, which needs SCALETEST_CONFIRM_DELETE.
func TestDelete(t *testing.T) {
	requireAction(t, "delete")
	env := setup(t, "delete")
	defer env.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	result, runErr := env.DeleteAll(ctx, t.Logf)

	// A refusal has nothing to report; every other outcome is written down, also a
	// stop before the first delete or a leftover found with no account to delete.
	if !errors.Is(runErr, errNotConfirmed) {
		root, err := repoRoot()
		if err != nil {
			t.Fatal(err)
		}
		path, err := result.Write(filepath.Join(root, "_output", "scaletest"))
		if err != nil {
			t.Errorf("writing the result: %v", err)
		}
		t.Logf("result: %d of %d accounts gone; file %s", result.Gone, result.Accounts, path)
		t.Logf("until gone: %s", result.ToGone)
	}
	logLeft(t, "still listed by Snowflake", result.SnowflakeLeft)
	logLeft(t, "secrets not scheduled for deletion", result.SecretsLeft)
	if runErr != nil {
		t.Fatalf("the run stopped: %v", runErr)
	}
}

// logLeft logs what the clean-up left behind, the first names only; the result file has all of them.
func logLeft(t *testing.T, what string, names []string) {
	t.Helper()
	const shown = 20
	if len(names) == 0 {
		return
	}
	if len(names) > shown {
		t.Logf("%s: %d, the first %d: %v", what, len(names), shown, names[:shown])
		return
	}
	t.Logf("%s: %v", what, names)
}
