//go:build e2e

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

// Package e2e holds the end-to-end tests of the Yukimi operator. What is tested,
// and the status of every case, is listed in README.md.
package e2e

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"testing"
	"time"

	"github.com/allianz/yukimi/e2e/support"
)

// env is set by TestMain before any test runs.
var env *support.Env

func TestMain(m *testing.M) {
	// An invalid -run expression makes the testing package exit on the spot,
	// without the clean-up. So it is checked before anything is created.
	flag.Parse()
	if f := flag.Lookup("test.run"); f != nil {
		if _, err := regexp.Compile(f.Value.String()); err != nil {
			os.Exit(abort("E2E_RUN / -run", err))
		}
	}
	os.Exit(run(m))
}

// cleanupBudget is the time kept free at the end of the go test timeout for the
// clean-up: the 15 minutes it may wait for the operator, and the checks.
const cleanupBudget = 20 * time.Minute

// suiteBudget is the time the tests may use: the go test timeout (E2E_TIMEOUT)
// minus cleanupBudget, so the clean-up always runs before go test kills the
// process. Zero means there is no timeout.
func suiteBudget() (time.Duration, error) {
	f := flag.Lookup("test.timeout")
	if f == nil {
		return 0, nil
	}
	timeout, ok := f.Value.(flag.Getter).Get().(time.Duration)
	if !ok || timeout == 0 {
		return 0, nil
	}
	if timeout <= cleanupBudget {
		return 0, fmt.Errorf("E2E_TIMEOUT (%s) must be longer than the %s kept for the clean-up", timeout, cleanupBudget)
	}
	return timeout - cleanupBudget, nil
}

// staleAfter is the age from which the namespace of an earlier run counts as
// abandoned: E2E_STALE_AFTER, or else the go test timeout, since a run that is
// still going is younger than that.
func staleAfter() (time.Duration, error) {
	if v := os.Getenv("E2E_STALE_AFTER"); v != "" {
		return time.ParseDuration(v)
	}
	if f := flag.Lookup("test.timeout"); f != nil {
		if d, ok := f.Value.(flag.Getter).Get().(time.Duration); ok && d > 0 {
			return d, nil
		}
	}
	return time.Hour, nil
}

// run performs the start-up checks, creates the namespace of this run, runs the
// tests and cleans up. Nothing is created before the checks pass.
func run(m *testing.M) (code int) {
	ctx := context.Background()

	// The suite stops its tests when it is interrupted (the first Ctrl-C or
	// SIGTERM) or when its time budget is used up. Neither stops the clean-up;
	// after the first Ctrl-C the default behavior is back, so a second one ends the
	// run at once.
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	suiteCtx, cancel := context.WithCancel(sigCtx)
	if budget, err := suiteBudget(); err != nil {
		return abort("time budget", err)
	} else if budget > 0 {
		suiteCtx, cancel = context.WithTimeout(sigCtx, budget)
	}
	defer cancel()
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-finished:
			return
		case <-suiteCtx.Done():
		}
		stop()
		reason := "interrupted"
		if sigCtx.Err() == nil {
			reason = "the time budget is used up (go test -timeout minus the clean-up time)"
		}
		fmt.Fprintf(os.Stderr, "e2e: %s; stopping the tests and cleaning up (press Ctrl-C again to abort without clean-up)\n", reason)
	}()

	cfg, err := support.LoadConfig()
	if err != nil {
		return abort("configuration", err)
	}
	e, err := support.Connect(ctx, cfg)
	if err != nil {
		return abort("connection", err)
	}
	defer e.Close() // runs last: after the cleanup below

	awsAccount, err := e.Check(ctx)
	if err != nil {
		return abort("start-up check", err)
	}

	stale, err := staleAfter()
	if err != nil {
		return abort("E2E_STALE_AFTER", err)
	}
	for _, line := range e.SweepStale(ctx, stale, 15*time.Minute) {
		fmt.Printf("e2e sweep: %s\n", line)
	}

	if err := e.CreateRunNamespace(ctx); err != nil {
		return abort("namespace", err)
	}
	fmt.Printf("e2e target: kube context %q, AWS account %s, organization %q, region %q, configuration %s, namespace %s\n",
		support.KubeContext(), awsAccount, cfg.Org(), e.Region, cfg.Dir, e.Namespace)

	// Accounts first, namespace last (see support.Env.Cleanup).
	defer func() {
		if err := e.Cleanup(ctx, 15*time.Minute); err != nil {
			fmt.Fprintf(os.Stderr, "e2e: cleanup incomplete: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}()

	if err := e.CheckOperatorAlive(ctx, 90*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: start-up check: %v\n", err)
		return 2
	}

	e.Ctx = suiteCtx
	env = e
	return m.Run()
}

func abort(what string, err error) int {
	fmt.Fprintf(os.Stderr, "e2e: %s: %v\n", what, err)
	return 2
}
