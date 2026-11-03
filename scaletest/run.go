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
	"fmt"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
)

const (
	// defaultPollInterval is how often the accounts of the namespace are listed.
	defaultPollInterval = 10 * time.Second
	// maxListFailures is how many listings in a row may fail before a wait gives up.
	// A listing of thousands of accounts can time out once in a while.
	maxListFailures = 5
)

// observer follows accounts from one listing of the namespace to the next.
type observer interface {
	Observe(now time.Time, accounts []v1alpha1.SnowflakeAccount) (pending int, failures []Failure)
}

// Run brings the number of accounts in the namespace up to the target, one batch
// after the other. Accounts of an earlier run that are not ready yet are waited for
// first. It creates only the missing accounts, waits for each batch to become ready,
// and stops at the first batch that has a failed account. All accounts among
// 1..target stay watched while the later batches are created: one that stops being
// ready, or disappears, stops the run too, and at the end all of them must be ready.
// It never deletes anything, so what broke stays for inspection. The result holds what
// was observed up to the end or the failure, and the error says why the run stopped.
func (e *Env) Run(ctx context.Context, logf func(format string, args ...any)) (*Result, error) {
	cfg := e.Cfg
	result := &Result{Started: time.Now(), Target: cfg.Accounts}
	tracker := NewTracker(Limits{Timeout: cfg.ReadyTimeout, ErrorGrace: cfg.ErrorGrace, DegradeGrace: cfg.DegradeGrace, StartTimeout: cfg.StartTimeout})
	finish := func(err error) (*Result, error) {
		result.Finished = time.Now()
		result.Summarize(tracker.Timings())
		n, countErr := e.readyCount(cfg.Accounts)
		result.ReadyTotal = n
		if countErr != nil {
			result.ReadyTotal = -1 // unknown
			logf("could not count the ready accounts: %v", countErr)
			if err == nil {
				err = countErr
			}
		}
		var failed *FailedError
		if errors.As(err, &failed) {
			result.Failures = failed.Failures
		}
		if err != nil {
			result.Error = err.Error()
		}
		return result, err
	}

	region, err := cfg.AvailableRegion()
	if err != nil {
		return finish(err)
	}
	if err := e.EnsureNamespace(ctx); err != nil {
		return finish(requestError(ctx, "preparing the namespace", err))
	}
	existing, err := e.listAccounts(ctx)
	if err != nil {
		return finish(requestError(ctx, "listing the accounts", err))
	}
	result.Existing = len(existing)

	have := map[string]bool{}
	wanted := rangeSet(cfg.Accounts)
	var ready, notReady, deleting []string
	for i := range existing {
		name := existing[i].Name
		have[name] = true
		switch {
		case !wanted[name]:
		case existing[i].DeletionTimestamp != nil:
			deleting = append(deleting, name)
		case isReady(&existing[i]):
			ready = append(ready, name)
		default:
			notReady = append(notReady, name)
		}
	}
	if len(deleting) > 0 {
		slices.Sort(deleting)
		return finish(fmt.Errorf("%d accounts are being deleted (%s ...); wait until they are gone, then run again", len(deleting), deleting[0]))
	}
	slices.Sort(notReady)
	missing := missingNames(have, cfg.Accounts)
	logf("%d accounts exist, %d of the first %d are not ready, %d to create in batches of %d",
		len(existing), len(notReady), cfg.Accounts, len(missing), cfg.Batch)

	tracker.Watch(ready...)
	// A run that continues an earlier one must not report success while accounts
	// of that run are still broken.
	if len(notReady) > 0 {
		tracker.Resumed(time.Now(), notReady...)
		if err := e.waitUntilDone(ctx, tracker, "to be ready", logf); err != nil {
			return finish(err)
		}
	}

	for i, batch := range batches(missing, cfg.Batch) {
		if i > 0 && cfg.BatchInterval > 0 {
			if err := sleep(ctx, cfg.BatchInterval); err != nil {
				return finish(err)
			}
		}
		for _, name := range batch {
			err := e.Kube.Create(ctx, newAccount(name, region))
			if err != nil && !apierrors.IsAlreadyExists(err) {
				return finish(requestError(ctx, "creating "+name, err))
			}
			tracker.Applied(time.Now(), name) // as soon as it exists, so a stop in the middle of a batch loses none
		}
		logf("batch %d: applied %s .. %s", i+1, batch[0], batch[len(batch)-1])

		if err := e.waitUntilDone(ctx, tracker, "to be ready", logf); err != nil {
			return finish(err)
		}
	}

	// The last look: an account that is still recovering gets the error grace.
	return finish(e.waitUntilDone(ctx, tracker, "to recover", logf))
}

// readyCount counts the accounts among 1..target that are ready now. The run may
// have been interrupted, so it uses a context of its own. The count is -1 in the
// result when it fails.
func (e *Env) readyCount(target int) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	accounts, err := e.listAccounts(ctx)
	if err != nil {
		return 0, fmt.Errorf("counting the ready accounts: %w", err)
	}
	wanted := rangeSet(target)
	n := 0
	for i := range accounts {
		if wanted[accounts[i].Name] && isReady(&accounts[i]) {
			n++
		}
	}
	return n, nil
}

// waitUntilDone lists the accounts until obs has none pending, or one has failed.
// waiting completes the progress message: "waiting for 3 accounts <waiting>". A
// failed listing is tried again; only several in a row stop the wait.
func (e *Env) waitUntilDone(ctx context.Context, obs observer, waiting string, logf func(string, ...any)) error {
	lastPending, listFailures := -1, 0
	for {
		accounts, err := e.listAccounts(ctx)
		if err != nil {
			if listFailures++; listFailures >= maxListFailures || ctx.Err() != nil {
				return requestError(ctx, "waiting", err)
			}
			logf("listing the accounts failed (%d of %d tries): %v", listFailures, maxListFailures, err)
		} else {
			listFailures = 0
			pending, failures := obs.Observe(time.Now(), accounts)
			if len(failures) > 0 {
				return &FailedError{Failures: failures}
			}
			if pending == 0 {
				return nil
			}
			if pending != lastPending {
				logf("waiting for %d accounts %s", pending, waiting)
				lastPending = pending
			}
		}
		if err := sleep(ctx, e.pollInterval()); err != nil {
			return err
		}
	}
}

// requestError wraps a failed request, and says so if the run was interrupted.
func requestError(ctx context.Context, what string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("interrupted: %w", ctx.Err())
	}
	return fmt.Errorf("%s: %w", what, err)
}

// FailedError is returned when accounts failed. It carries all of them.
type FailedError struct{ Failures []Failure }

func (e *FailedError) Error() string {
	msg := fmt.Sprintf("%d accounts failed; the run stopped here", len(e.Failures))
	for i, f := range e.Failures {
		if i == 5 {
			msg += fmt.Sprintf("\n  ... and %d more (see the result file)", len(e.Failures)-i)
			break
		}
		msg += fmt.Sprintf("\n  %s: %s", f.Name, f.Reason)
	}
	return msg
}

func (e *Env) listAccounts(ctx context.Context) ([]v1alpha1.SnowflakeAccount, error) {
	var list v1alpha1.SnowflakeAccountList
	if err := e.Kube.List(ctx, &list, client.InNamespace(Namespace)); err != nil {
		return nil, fmt.Errorf("listing the accounts in namespace %q: %w", Namespace, err)
	}
	return list.Items, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("interrupted: %w", ctx.Err())
	case <-t.C:
		return nil
	}
}
