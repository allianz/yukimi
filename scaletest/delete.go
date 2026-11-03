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
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
)

// defaultPermitWait is how long the run waits for the operator to process the
// deletion permits of a batch before it deletes the accounts.
const defaultPermitWait = 30 * time.Second

// errNotConfirmed marks the refusal to delete without the right confirmation.
var errNotConfirmed = errors.New("the clean-up is not confirmed")

// DeleteAll deletes every account in the namespace the way a tenant does, batch
// by batch: a DeletionPermit first, then the delete. It waits for each batch to be
// gone before the next one starts, removes the permits of the accounts that are gone, and
// stops at the first batch with a failed account. Whatever the way out, it then removes
// the permits of every account that no longer exists, also those of earlier runs. When all are gone it checks that
// Snowflake no longer lists any account of the scale test and that every credential
// secret is scheduled for deletion. The namespace itself is kept, for the next scale
// test.
//
// It refuses to delete unless SCALETEST_CONFIRM_DELETE holds the number of accounts
// that would be deleted; the error then wraps errNotConfirmed.
func (e *Env) DeleteAll(ctx context.Context, logf func(format string, args ...any)) (*DeleteResult, error) {
	cfg := e.Cfg
	result := &DeleteResult{Started: time.Now()}
	tracker := NewGoneTracker(Limits{Timeout: cfg.DeleteTimeout, ErrorGrace: cfg.ErrorGrace})
	finish := func(err error) (*DeleteResult, error) {
		result.Finished = time.Now()
		result.Summarize(tracker.Timings())
		// Whatever stopped the run, a permit whose account is gone is no use any more. An
		// interrupted run sends nothing more, and a refusal has touched nothing.
		if ctx.Err() == nil && !errors.Is(err, errNotConfirmed) {
			e.removeStalePermits(ctx, logf)
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

	accounts, err := e.listAccounts(ctx)
	if err != nil {
		return finish(requestError(ctx, "listing the accounts", err))
	}
	names := make([]string, 0, len(accounts))
	for _, a := range accounts {
		names = append(names, a.Name)
	}
	slices.Sort(names)
	result.Accounts = len(names)

	if len(names) > 0 && cfg.ConfirmDelete != strconv.Itoa(len(names)) {
		return finish(fmt.Errorf("%w: %s must be %d, the number of accounts in namespace %q, to confirm that all of them are deleted; got %q",
			errNotConfirmed, EnvConfirmDelete, len(names), Namespace, cfg.ConfirmDelete))
	}
	logf("deleting %d accounts in batches of %d", len(names), cfg.Batch)

	suffix := strconv.FormatInt(time.Now().Unix(), 36)
	for i, batch := range batches(names, cfg.Batch) {
		if i > 0 && cfg.BatchInterval > 0 {
			if err := sleep(ctx, cfg.BatchInterval); err != nil {
				return finish(err)
			}
		}
		if err := e.requestDeletion(ctx, batch, suffix, tracker, logf); err != nil {
			return finish(err)
		}
		logf("batch %d: deleted %s .. %s", i+1, batch[0], batch[len(batch)-1])

		if err := e.waitUntilDone(ctx, tracker, "to be gone", logf); err != nil {
			return finish(err)
		}
		e.deletePermits(ctx, batch, suffix, logf)
	}

	// Everything is gone from the cluster; look at what is left outside of it.
	left, err := e.snowflakeAccounts(ctx)
	if err != nil {
		return finish(fmt.Errorf("verifying Snowflake: %w", err))
	}
	secretsLeft, err := e.secretsNotScheduled(ctx)
	if err != nil {
		return finish(fmt.Errorf("verifying the secrets: %w", err))
	}
	result.SnowflakeLeft, result.SecretsLeft = left, secretsLeft
	if len(left) > 0 || len(secretsLeft) > 0 {
		return finish(fmt.Errorf("the clean-up is incomplete: %d accounts are still listed by Snowflake, %d credential secrets are not scheduled for deletion",
			len(left), len(secretsLeft)))
	}
	return finish(nil)
}

// permitPrefix starts the name of every permit this package creates.
const permitPrefix = "delete-" + accountPrefix + "-"

func permitName(account, suffix string) string { return fmt.Sprintf("delete-%s-%s", account, suffix) }

// requestDeletion creates a DeletionPermit for each account of the batch, gives the
// operator a moment to process them, and then deletes the accounts. The permit is
// always created, whether or not the operator's deletion protection is on. Each
// account is followed from the moment its delete was requested.
func (e *Env) requestDeletion(ctx context.Context, batch []string, suffix string, tracker *GoneTracker, logf func(string, ...any)) error {
	permits := make([]types.NamespacedName, 0, len(batch))
	for _, name := range batch {
		permit := &v1alpha1.DeletionPermit{
			ObjectMeta: metav1.ObjectMeta{Name: permitName(name, suffix), Namespace: Namespace},
			Spec: v1alpha1.DeletionPermitSpec{
				TargetRef: v1alpha1.TargetRef{Kind: v1alpha1.SnowflakeAccountKind, Name: name},
				Duration:  metav1.Duration{Duration: 4 * time.Hour},
				Reason:    "scaletest clean-up",
			},
		}
		if err := e.Kube.Create(ctx, permit); err != nil && !apierrors.IsAlreadyExists(err) {
			return requestError(ctx, "creating the deletion permit for "+name, err)
		}
		permits = append(permits, types.NamespacedName{Namespace: Namespace, Name: permit.Name})
	}

	// Best effort: a permit that is not processed in time does not stop the delete,
	// but the log says so.
	deadline := time.Now().Add(e.permitWait())
	var lastErr error
	for {
		var open []types.NamespacedName
		for _, key := range permits {
			var got v1alpha1.DeletionPermit
			if err := e.Kube.Get(ctx, key, &got); err != nil {
				lastErr = err
				open = append(open, key)
			} else if got.Status.State == "" {
				open = append(open, key)
			}
		}
		if permits = open; len(permits) == 0 || !time.Now().Before(deadline) {
			break
		}
		if err := sleep(ctx, e.pollInterval()); err != nil {
			return err
		}
	}
	if len(permits) > 0 {
		reason := ""
		if lastErr != nil {
			reason = fmt.Sprintf(" (last error: %v)", lastErr)
		}
		logf("%d deletion permits were not processed in %s%s; deleting the accounts anyway", len(permits), e.permitWait(), reason)
	}

	for _, name := range batch {
		account := &v1alpha1.SnowflakeAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: Namespace}}
		if err := e.Kube.Delete(ctx, account); err != nil && !apierrors.IsNotFound(err) {
			return requestError(ctx, "deleting account "+name, err)
		}
		tracker.Deleted(time.Now(), name)
	}
	return nil
}

// deletePermits removes the permits this run created for the accounts of a batch
// that is gone. It needs no listing, so it is cheap enough to run after every batch.
func (e *Env) deletePermits(ctx context.Context, batch []string, suffix string, logf func(string, ...any)) {
	for _, name := range batch {
		permit := &v1alpha1.DeletionPermit{ObjectMeta: metav1.ObjectMeta{Name: permitName(name, suffix), Namespace: Namespace}}
		if err := e.Kube.Delete(ctx, permit); err != nil && !apierrors.IsNotFound(err) {
			logf("could not remove the permit %s: %v", permit.Name, err)
		}
	}
}

// removeStalePermits removes the permits of this package whose account no longer
// exists, whichever run created them, so thousands of them do not stay in the
// namespace and keep the operator busy. A permit that cannot be removed is only
// reported: the clean-up of the accounts is what counts.
func (e *Env) removeStalePermits(ctx context.Context, logf func(string, ...any)) {
	accounts, err := e.listAccounts(ctx)
	if err != nil {
		logf("could not list the accounts to remove the stale permits: %v", err)
		return
	}
	exist := map[string]bool{}
	for _, a := range accounts {
		exist[a.Name] = true
	}
	var permits v1alpha1.DeletionPermitList
	if err := e.Kube.List(ctx, &permits, client.InNamespace(Namespace)); err != nil {
		logf("could not list the permits: %v", err)
		return
	}
	for i := range permits.Items {
		p := &permits.Items[i]
		if !strings.HasPrefix(p.Name, permitPrefix) || exist[p.Spec.TargetRef.Name] {
			continue
		}
		if err := e.Kube.Delete(ctx, p); err != nil && !apierrors.IsNotFound(err) {
			logf("could not remove the permit %s: %v", p.Name, err)
		}
	}
}
