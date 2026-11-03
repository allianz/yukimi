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
	"strings"
	"testing"
	"time"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
)

func TestGoneTracker(t *testing.T) {
	tr := NewGoneTracker(Limits{Timeout: 10 * time.Minute, ErrorGrace: 2 * time.Minute})
	tr.Deleted(t0, "a", "b")

	still := []v1alpha1.SnowflakeAccount{account("a", "xy1", ready()...), account("b", "xy2", ready()...)}
	if pending, failures := tr.Observe(t0.Add(time.Minute), still); pending != 2 || len(failures) != 0 {
		t.Fatalf("pending = %d, failures = %v", pending, failures)
	}
	// a is gone, b is not.
	if pending, _ := tr.Observe(t0.Add(3*time.Minute), still[1:]); pending != 1 {
		t.Fatalf("pending = %d, want 1", pending)
	}
	if s, ok := tr.Timings()[0].SecondsToGone(); !ok || s != 180 {
		t.Fatalf("seconds to gone = %v, %v, want 180", s, ok)
	}

	// b stays too long.
	_, failures := tr.Observe(t0.Add(11*time.Minute), still[1:])
	if len(failures) != 1 || failures[0].Name != "b" || !strings.Contains(failures[0].Reason, "still there") {
		t.Fatalf("failures = %v", failures)
	}
}

func TestGoneTrackerErrorGrace(t *testing.T) {
	tr := NewGoneTracker(Limits{Timeout: time.Hour, ErrorGrace: 2 * time.Minute})
	tr.Deleted(t0, "a")
	failing := []v1alpha1.SnowflakeAccount{account("a", "xy1", xpv1.ReconcileError(errors.New("drop refused")))}

	tr.Observe(t0.Add(time.Minute), failing)
	_, failures := tr.Observe(t0.Add(4*time.Minute), failing)
	if len(failures) != 1 || !strings.Contains(failures[0].Reason, "drop refused") {
		t.Fatalf("failures = %v, want one with the message", failures)
	}
	if got := tr.Timings()[0].LastError; got != "drop refused" {
		t.Fatalf("last error = %q", got)
	}
}

// deleteEnv returns an Env with n accounts in the namespace. The "operator"
// removes an account as soon as it is deleted, unless it is listed in stuck.
func deleteEnv(t *testing.T, n int, stuck ...string) (*Env, client.Client) {
	t.Helper()
	scheme, err := newScheme()
	if err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			for _, s := range stuck {
				if obj.GetName() == s {
					return nil // the finalizer holds it
				}
			}
			return c.Delete(ctx, obj, opts...)
		},
	}).Build()
	env := &Env{
		Cfg:          &Config{Settings: Settings{Batch: 2, DeleteTimeout: 30 * time.Millisecond, ErrorGrace: 20 * time.Millisecond}},
		Kube:         kube,
		PollInterval: time.Millisecond,
		PermitWait:   time.Millisecond, // nothing processes the permits here
	}
	for i := 1; i <= n; i++ {
		a := newAccount(accountName(i), "aws-eu-central-1")
		makeReady(a)
		if err := kube.Create(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	env.listSnowflake = func(context.Context) ([]string, error) { return nil, nil }
	env.listSecrets = func(context.Context) ([]string, error) { return nil, nil }
	return env, kube
}

func permitCount(t *testing.T, kube client.Client) int {
	t.Helper()
	var permits v1alpha1.DeletionPermitList
	if err := kube.List(context.Background(), &permits, client.InNamespace(Namespace)); err != nil {
		t.Fatal(err)
	}
	return len(permits.Items)
}

func TestDeleteAllRefusesWithoutTheRightConfirmation(t *testing.T) {
	for _, confirm := range []string{"", "3", "five"} {
		env, _ := deleteEnv(t, 5)
		env.Cfg.ConfirmDelete = confirm
		_, err := env.DeleteAll(testCtx(t), func(string, ...any) {})
		if err == nil || !strings.Contains(err.Error(), EnvConfirmDelete+" must be 5") {
			t.Fatalf("confirm %q: err = %v, want a refusal that names 5", confirm, err)
		}
		if n, _ := env.ExistingAccounts(context.Background()); n != 5 {
			t.Fatalf("confirm %q: %d accounts left, want all 5", confirm, n)
		}
	}
}

func TestDeleteAllDeletesEverythingAndVerifies(t *testing.T) {
	env, kube := deleteEnv(t, 5)
	env.Cfg.ConfirmDelete = "5"

	result, err := env.DeleteAll(testCtx(t), func(string, ...any) {})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if result.Accounts != 5 || result.Gone != 5 || len(result.Timings) != 5 {
		t.Fatalf("accounts %d, gone %d, timings %d; want 5, 5, 5", result.Accounts, result.Gone, len(result.Timings))
	}
	if n, _ := env.ExistingAccounts(context.Background()); n != 0 {
		t.Fatalf("%d accounts left", n)
	}
	// A permit was created for every account, and removed again with it.
	if n := permitCount(t, kube); n != 0 {
		t.Fatalf("%d permits left, want none", n)
	}
}

func TestDeleteAllWithNothingToDelete(t *testing.T) {
	env, _ := deleteEnv(t, 0)
	result, err := env.DeleteAll(testCtx(t), func(string, ...any) {})
	if err != nil || result.Accounts != 0 {
		t.Fatalf("accounts %d, err %v; want 0, nil (no confirmation is needed)", result.Accounts, err)
	}
}

func TestDeleteAllStopsAtAStuckAccount(t *testing.T) {
	env, kube := deleteEnv(t, 5, accountName(3))
	env.Cfg.ConfirmDelete = "5"

	result, err := env.DeleteAll(testCtx(t), func(string, ...any) {})
	var failed *FailedError
	if !errors.As(err, &failed) || failed.Failures[0].Name != accountName(3) {
		t.Fatalf("err = %v, want a failure of %s", err, accountName(3))
	}
	if len(result.Failures) != 1 || result.Failures[0].Name != accountName(3) {
		t.Fatalf("failures in the result = %v", result.Failures)
	}
	// Batch 1 (1, 2) is gone, batch 2 (3, 4) was deleted and 3 is stuck, batch 3 was never started.
	if result.Gone != 3 {
		t.Fatalf("gone = %d, want 3", result.Gone)
	}
	if n, _ := env.ExistingAccounts(context.Background()); n != 2 {
		t.Fatalf("%d accounts left, want 2 (the stuck one and the one of the batch that never started)", n)
	}
	// The permits of accounts that are gone are removed, also that of account 4 of the
	// stuck batch; that of the stuck account 3 stays with it.
	var permits v1alpha1.DeletionPermitList
	if err := kube.List(context.Background(), &permits, client.InNamespace(Namespace)); err != nil {
		t.Fatal(err)
	}
	if len(permits.Items) != 1 || permits.Items[0].Spec.TargetRef.Name != accountName(3) {
		t.Fatalf("permits left = %d, want only that of %s", len(permits.Items), accountName(3))
	}
}

func TestDeleteAllReportsWhatIsLeftBehind(t *testing.T) {
	env, _ := deleteEnv(t, 2)
	env.Cfg.ConfirmDelete = "2"
	env.listSnowflake = func(context.Context) ([]string, error) { return []string{"SCALETEST_00001_ABCD"}, nil }
	env.listSecrets = func(context.Context) ([]string, error) { return []string{"yk-org--scaletest--scaletest-00002"}, nil }

	result, err := env.DeleteAll(testCtx(t), func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("err = %v, want an incomplete clean-up", err)
	}
	if len(result.SnowflakeLeft) != 1 || len(result.SecretsLeft) != 1 {
		t.Fatalf("left = %v, %v", result.SnowflakeLeft, result.SecretsLeft)
	}
}

func TestDeleteAllVerifiesAlsoWhenThereIsNothingToDelete(t *testing.T) {
	// A secret left by an earlier, interrupted clean-up is found although no account is left.
	env, _ := deleteEnv(t, 0)
	env.listSecrets = func(context.Context) ([]string, error) { return []string{"yk-org--scaletest--scaletest-00001"}, nil }

	result, err := env.DeleteAll(testCtx(t), func(string, ...any) {})
	if err == nil || len(result.SecretsLeft) != 1 {
		t.Fatalf("err = %v, secrets left = %v; want the leftover reported", err, result.SecretsLeft)
	}
}

func TestDeleteAllVerificationErrors(t *testing.T) {
	env, _ := deleteEnv(t, 1)
	env.Cfg.ConfirmDelete = "1"
	env.listSnowflake = func(context.Context) ([]string, error) { return nil, errors.New("no connection") }
	if _, err := env.DeleteAll(testCtx(t), func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "verifying Snowflake") {
		t.Fatalf("err = %v", err)
	}

	env, _ = deleteEnv(t, 1)
	env.Cfg.ConfirmDelete = "1"
	env.listSecrets = func(context.Context) ([]string, error) { return nil, errors.New("access denied") }
	if _, err := env.DeleteAll(testCtx(t), func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "verifying the secrets") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeleteAllIsInterrupted(t *testing.T) {
	env, kube := deleteEnv(t, 3)
	env.Cfg.ConfirmDelete = "3"
	env.Kube = ctxAware{kube}
	ctx, cancel := context.WithCancel(testCtx(t))
	cancel()

	_, err := env.DeleteAll(ctx, func(string, ...any) {})
	if err == nil || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("err = %v, want an interruption", err)
	}
	if n, _ := env.ExistingAccounts(context.Background()); n != 3 {
		t.Fatalf("%d accounts left, want all 3: an interrupted run deletes nothing more", n)
	}
}

func TestDeleteAllStopsDeletingWhenInterruptedBetweenBatches(t *testing.T) {
	env, kube := deleteEnv(t, 4)
	env.Cfg.ConfirmDelete = "4"
	env.Cfg.BatchInterval = time.Hour
	ctx, cancel := context.WithCancel(testCtx(t))
	defer cancel()
	// Cancelled right after the listing that shows batch 1 gone: the next thing is the pause.
	env.Kube = &onNthList{Client: ctxAware{kube}, n: 2, after: true, fn: cancel}

	result, err := env.DeleteAll(ctx, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("err = %v, want an interruption", err)
	}
	if result.Gone != 2 {
		t.Fatalf("gone = %d, want the first batch only", result.Gone)
	}
	if n, _ := env.ExistingAccounts(context.Background()); n != 2 {
		t.Fatalf("%d accounts left, want the 2 of the batch that never started", n)
	}
}

// processPermits is a kube client whose "operator" marks every new DeletionPermit Active.
type processPermits struct{ client.Client }

func (p processPermits) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if permit, ok := obj.(*v1alpha1.DeletionPermit); ok {
		permit.Status.State = "Active"
	}
	return p.Client.Create(ctx, obj, opts...)
}

// failingPermitGet fails every Get of a DeletionPermit.
type failingPermitGet struct{ client.Client }

func (f failingPermitGet) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*v1alpha1.DeletionPermit); ok {
		return errors.New("permit lookup failed")
	}
	return f.Client.Get(ctx, key, obj, opts...)
}

func TestDeleteAllSaysWhenPermitsAreNotProcessed(t *testing.T) {
	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }

	// Nobody processes the permits: the delete goes on, and the log says why it may be refused.
	env, _ := deleteEnv(t, 2)
	env.Cfg.ConfirmDelete = "2"
	if _, err := env.DeleteAll(testCtx(t), logf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(logged, "\n"), "were not processed") {
		t.Fatalf("log = %v, want a warning about the permits", logged)
	}

	// The operator processes them: no warning.
	logged = nil
	env, kube := deleteEnv(t, 2)
	env.Cfg.ConfirmDelete = "2"
	env.Kube = processPermits{Client: kube}
	if _, err := env.DeleteAll(testCtx(t), logf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(logged, "\n"), "were not processed") {
		t.Fatalf("log = %v, want no warning", logged)
	}

	// A permit that cannot even be read is reported with the error.
	logged = nil
	env, kube = deleteEnv(t, 1)
	env.Cfg.ConfirmDelete = "1"
	env.Kube = failingPermitGet{Client: kube}
	if _, err := env.DeleteAll(testCtx(t), logf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(logged, "\n"), "permit lookup failed") {
		t.Fatalf("log = %v, want the lookup error", logged)
	}
}

func TestDeleteAllRemovesThePermitsOfAnEarlierRunToo(t *testing.T) {
	env, kube := deleteEnv(t, 1)
	env.Cfg.ConfirmDelete = "1"
	ctx := testCtx(t)
	for _, name := range []string{
		permitName(accountName(7), "old"), // an account that is long gone: stale
		permitName(accountName(1), "old"), // the account exists now, but is deleted in this run
		"someone-elses-permit",            // not ours
	} {
		target := accountName(7)
		if strings.Contains(name, accountName(1)) {
			target = accountName(1)
		}
		permit := &v1alpha1.DeletionPermit{Spec: v1alpha1.DeletionPermitSpec{TargetRef: v1alpha1.TargetRef{Kind: v1alpha1.SnowflakeAccountKind, Name: target}}}
		permit.Name, permit.Namespace = name, Namespace
		if err := kube.Create(ctx, permit); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := env.DeleteAll(ctx, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	var left v1alpha1.DeletionPermitList
	if err := kube.List(ctx, &left, client.InNamespace(Namespace)); err != nil {
		t.Fatal(err)
	}
	if len(left.Items) != 1 || left.Items[0].Name != "someone-elses-permit" {
		t.Fatalf("permits left = %v, want only the one that is not ours", left.Items)
	}
}

func TestDeleteAllRemovesStalePermitsWhenThereIsNothingToDelete(t *testing.T) {
	env, kube := deleteEnv(t, 0)
	permit := &v1alpha1.DeletionPermit{Spec: v1alpha1.DeletionPermitSpec{TargetRef: v1alpha1.TargetRef{Kind: v1alpha1.SnowflakeAccountKind, Name: accountName(3)}}}
	permit.Name, permit.Namespace = permitName(accountName(3), "old"), Namespace
	if err := kube.Create(context.Background(), permit); err != nil {
		t.Fatal(err)
	}
	if _, err := env.DeleteAll(testCtx(t), func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if n := permitCount(t, kube); n != 0 {
		t.Fatalf("%d permits left", n)
	}
}

func TestDeleteAllWritesTheStopReasonAndTheRefusalIsMarked(t *testing.T) {
	env, _ := deleteEnv(t, 2)
	result, err := env.DeleteAll(testCtx(t), func(string, ...any) {})
	if !errors.Is(err, errNotConfirmed) || result.Error == "" {
		t.Fatalf("err = %v, result error %q; want the refusal, marked and recorded", err, result.Error)
	}

	env, _ = deleteEnv(t, 2, accountName(1))
	env.Cfg.ConfirmDelete = "2"
	result, err = env.DeleteAll(testCtx(t), func(string, ...any) {})
	if err == nil || errors.Is(err, errNotConfirmed) || !strings.Contains(result.Error, "failed") {
		t.Fatalf("err = %v, result error %q; want a failure that is not a refusal", err, result.Error)
	}
}

func TestRemoveStalePermitsKeepsThePermitsOfExistingAccounts(t *testing.T) {
	env, kube := deleteEnv(t, 2) // accounts 1 and 2 exist
	ctx := testCtx(t)
	for name, target := range map[string]int{
		permitName(accountName(1), "old"): 1, // its account exists: the permit is still needed
		permitName(accountName(9), "old"): 9, // its account is gone: stale
		"someone-elses-permit":            9, // not ours, whatever it targets
	} {
		permit := &v1alpha1.DeletionPermit{Spec: v1alpha1.DeletionPermitSpec{
			TargetRef: v1alpha1.TargetRef{Kind: v1alpha1.SnowflakeAccountKind, Name: accountName(target)}}}
		permit.Name, permit.Namespace = name, Namespace
		if err := kube.Create(ctx, permit); err != nil {
			t.Fatal(err)
		}
	}

	env.removeStalePermits(ctx, func(string, ...any) {})

	var left v1alpha1.DeletionPermitList
	if err := kube.List(ctx, &left, client.InNamespace(Namespace)); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, p := range left.Items {
		names[p.Name] = true
	}
	if len(names) != 2 || !names[permitName(accountName(1), "old")] || !names["someone-elses-permit"] {
		t.Fatalf("permits left = %v, want that of the existing account and the one that is not ours", names)
	}
}

func TestDeleteAllRemovesThePermitsOfEachBatchBeforeTheNextOne(t *testing.T) {
	env, kube := deleteEnv(t, 4)
	env.Cfg.ConfirmDelete = "4"
	ctx := testCtx(t)
	permitsDuringBatch2 := -1
	// List 1 is the start, list 2 the wait for batch 1, list 3 the wait for batch 2.
	env.Kube = &onNthList{Client: kube, n: 3, fn: func() { permitsDuringBatch2 = permitCount(t, kube) }}

	if _, err := env.DeleteAll(ctx, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if permitsDuringBatch2 != 2 {
		t.Fatalf("%d permits while batch 2 was deleted, want only its own 2: the permits of batch 1 go once it is gone", permitsDuringBatch2)
	}
}

func TestDeleteAllSendsNothingMoreAfterAnInterruption(t *testing.T) {
	env, kube := deleteEnv(t, 4)
	env.Cfg.ConfirmDelete = "4"
	env.Cfg.BatchInterval = time.Hour
	ctx, cancel := context.WithCancel(testCtx(t))
	defer cancel()
	stale := &v1alpha1.DeletionPermit{Spec: v1alpha1.DeletionPermitSpec{TargetRef: v1alpha1.TargetRef{Kind: v1alpha1.SnowflakeAccountKind, Name: accountName(9)}}}
	stale.Name, stale.Namespace = permitName(accountName(9), "old"), Namespace
	if err := kube.Create(ctx, stale); err != nil {
		t.Fatal(err)
	}
	// This client does not fail requests after the cancel, as the real one does: if the
	// run still sent the clean-up of the permits, it would succeed.
	env.Kube = &onNthList{Client: kube, n: 2, after: true, fn: cancel}

	if _, err := env.DeleteAll(ctx, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("err = %v, want an interruption", err)
	}
	if err := kube.Get(context.Background(), types.NamespacedName{Namespace: Namespace, Name: stale.Name}, &v1alpha1.DeletionPermit{}); err != nil {
		t.Fatalf("the stale permit is gone (%v): an interrupted run must send nothing more", err)
	}
}
