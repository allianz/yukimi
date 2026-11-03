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
	"strings"
	"testing"
	"time"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/config/backplane"
)

// fakeEnv returns an Env whose cluster is in memory. The "operator" is the
// interceptor: it sets the status of every account the moment it is created.
func fakeEnv(t *testing.T, accounts int, operator func(*v1alpha1.SnowflakeAccount)) (*Env, client.Client) {
	t.Helper()
	scheme, err := newScheme()
	if err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if a, ok := obj.(*v1alpha1.SnowflakeAccount); ok && operator != nil {
				operator(a)
			}
			return c.Create(ctx, obj, opts...)
		},
	}).Build()

	cfg := &Config{
		Settings:  Settings{Accounts: accounts, Batch: 2, ReadyTimeout: time.Minute, ErrorGrace: 20 * time.Millisecond, DegradeGrace: 20 * time.Millisecond, StartTimeout: 200 * time.Millisecond},
		Backplane: &backplane.Config{Regions: map[string]backplane.Region{"aws-eu-central-1": {Available: true}}},
	}
	return &Env{Cfg: cfg, Kube: kube, PollInterval: time.Millisecond}, kube
}

func makeReady(a *v1alpha1.SnowflakeAccount) {
	a.Status.AccountLocator = "xy12345"
	a.SetConditions(xpv1.Available(), xpv1.ReconcileSuccess())
}

func TestRunCreatesTheMissingAccountsOnly(t *testing.T) {
	env, kube := fakeEnv(t, 5, makeReady)
	logf := func(string, ...any) {}

	// An earlier run left account 2, and it is ready.
	left := newAccount(accountName(2), "aws-eu-central-1")
	makeReady(left)
	if err := kube.Create(context.Background(), left); err != nil {
		t.Fatal(err)
	}

	result, err := env.Run(testCtx(t), logf)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Existing != 1 || result.Applied != 4 || result.Ready != 4 {
		t.Fatalf("result = existing %d, applied %d, ready %d; want 1, 4, 4", result.Existing, result.Applied, result.Ready)
	}
	if result.ReadyTotal != 5 {
		t.Fatalf("%d accounts are ready, want 5", result.ReadyTotal)
	}
	if n, _ := env.ExistingAccounts(context.Background()); n != 5 {
		t.Fatalf("%d accounts exist, want 5", n)
	}

	// The namespace has the labels the controller reads.
	var ns corev1.Namespace
	if err := kube.Get(context.Background(), types.NamespacedName{Name: Namespace}, &ns); err != nil {
		t.Fatal(err)
	}
	if ns.Labels["alpha-tester"] != "true" || ns.Labels["credit-quota"] != "10000" {
		t.Fatalf("labels = %v", ns.Labels)
	}

	// The same run again has nothing to do.
	result, err = env.Run(testCtx(t), logf)
	if err != nil || result.Applied != 0 || result.Existing != 5 {
		t.Fatalf("second run: applied %d, existing %d, err %v; want 0, 5, nil", result.Applied, result.Existing, err)
	}
}

func TestRunStopsAtTheFirstFailedBatchAndDeletesNothing(t *testing.T) {
	failing := func(a *v1alpha1.SnowflakeAccount) {
		if a.Name == accountName(3) {
			a.SetConditions(xpv1.ReconcileError(errors.New("organization limit reached")))
			return
		}
		makeReady(a)
	}
	env, _ := fakeEnv(t, 6, failing)

	result, err := env.Run(testCtx(t), func(string, ...any) {})
	var failed *FailedError
	if !errors.As(err, &failed) || len(failed.Failures) != 1 || failed.Failures[0].Name != accountName(3) ||
		!strings.Contains(failed.Failures[0].Reason, "organization limit reached") {
		t.Fatalf("err = %v, want one failure of %s with its message", err, accountName(3))
	}
	// Batch 1 (accounts 1, 2) was ready, batch 2 (3, 4) was applied and failed,
	// batch 3 was never started. Nothing was deleted.
	if result.Applied != 4 || result.Ready != 3 || result.ReadyTotal != 3 {
		t.Fatalf("applied %d, ready %d, ready in total %d; want 4, 3, 3", result.Applied, result.Ready, result.ReadyTotal)
	}
	// The failure is in the result, not only in the log, with the message of the account.
	if len(result.Failures) != 1 || result.Failures[0].Name != accountName(3) {
		t.Fatalf("failures in the result = %v", result.Failures)
	}
	for _, timing := range result.Accounts {
		if timing.Name == accountName(3) && timing.LastError != "organization limit reached" {
			t.Fatalf("last error of %s = %q", timing.Name, timing.LastError)
		}
	}
	if n, _ := env.ExistingAccounts(context.Background()); n != 4 {
		t.Fatalf("%d accounts exist, want 4", n)
	}
}

func TestRunStopsWhenInterrupted(t *testing.T) {
	env, kube := fakeEnv(t, 4, func(a *v1alpha1.SnowflakeAccount) {}) // the operator never reacts
	env.Cfg.StartTimeout = time.Hour
	ctx, cancel := context.WithCancel(testCtx(t))
	// Cancelled right after the listing of the first wait: the run is then waiting for batch 1.
	env.Kube = &onNthList{Client: kube, n: 2, after: true, fn: cancel}

	_, err := env.Run(ctx, func(string, ...any) {})
	if err == nil || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("err = %v, want an interruption", err)
	}
	if n, _ := env.ExistingAccounts(context.Background()); n != 2 {
		t.Fatalf("%d accounts exist, want the 2 of the first batch", n)
	}
}

func TestRunWithoutAnAvailableRegion(t *testing.T) {
	env, _ := fakeEnv(t, 2, makeReady)
	env.Cfg.Backplane = &backplane.Config{}
	if _, err := env.Run(testCtx(t), func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "no region") {
		t.Fatalf("err = %v, want one about the missing region", err)
	}
}

func TestEnsureNamespaceFixesLabelsOnly(t *testing.T) {
	env, kube := fakeEnv(t, 1, nil)
	ctx := testCtx(t)
	ns := &corev1.Namespace{}
	ns.Name = Namespace
	ns.Labels = map[string]string{"alpha-tester": "false", "owner": "someone"}
	if err := kube.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}

	if err := env.EnsureNamespace(ctx); err != nil {
		t.Fatal(err)
	}
	var got corev1.Namespace
	if err := kube.Get(ctx, types.NamespacedName{Name: Namespace}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Labels["alpha-tester"] != "true" || got.Labels["owner"] != "someone" {
		t.Fatalf("labels = %v, want alpha-tester fixed and owner kept", got.Labels)
	}
	if err := env.EnsureNamespace(ctx); err != nil { // nothing to change the second time
		t.Fatal(err)
	}
}

func TestFailedErrorShortensALongList(t *testing.T) {
	var failures []Failure
	for i := 1; i <= 8; i++ {
		failures = append(failures, Failure{Name: accountName(i), Reason: "x"})
	}
	msg := (&FailedError{Failures: failures}).Error()
	if !strings.Contains(msg, "8 accounts failed") || !strings.Contains(msg, "and 3 more") {
		t.Fatalf("message = %q", msg)
	}
}

func TestRunWaitsForAccountsOfAnEarlierRunThatAreNotReady(t *testing.T) {
	// The operator never reacts to account 2, as in an earlier run that stopped with it broken.
	env, kube := fakeEnv(t, 3, func(a *v1alpha1.SnowflakeAccount) {
		if a.Name != accountName(2) {
			makeReady(a)
		}
	})
	env.Cfg.StartTimeout = 50 * time.Millisecond
	ctx := testCtx(t)

	for _, n := range []int{1, 2} {
		if err := kube.Create(ctx, newAccount(accountName(n), "aws-eu-central-1")); err != nil {
			t.Fatal(err)
		}
	}

	// Still broken: the run must not report success, and must not create account 3.
	result, err := env.Run(ctx, func(string, ...any) {})
	var failed *FailedError
	if !errors.As(err, &failed) || failed.Failures[0].Name != accountName(2) {
		t.Fatalf("err = %v, want a failure of %s", err, accountName(2))
	}
	if result.Existing != 2 || result.Applied != 1 || result.ReadyTotal != 1 {
		t.Fatalf("existing %d, applied %d, ready in total %d; want 2, 1, 1", result.Existing, result.Applied, result.ReadyTotal)
	}
	if n, _ := env.ExistingAccounts(ctx); n != 2 {
		t.Fatalf("%d accounts exist, want 2: nothing is created while an earlier account is broken", n)
	}

	// Fixed: the run waits for it, creates the missing one and succeeds.
	var fixed v1alpha1.SnowflakeAccount
	if err := kube.Get(ctx, types.NamespacedName{Namespace: Namespace, Name: accountName(2)}, &fixed); err != nil {
		t.Fatal(err)
	}
	makeReady(&fixed)
	if err := kube.Update(ctx, &fixed); err != nil {
		t.Fatal(err)
	}
	result, err = env.Run(ctx, func(string, ...any) {})
	if err != nil || result.ReadyTotal != 3 {
		t.Fatalf("err = %v, ready in total %d; want nil, 3", err, result.ReadyTotal)
	}
}

// failingCreate fails the create of one account, like an API error in the middle of a batch.
type failingCreate struct {
	client.Client
	name   string
	onFail func() // called when the create fails, for example to cancel the context
}

func (f failingCreate) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if obj.GetName() == f.name {
		if f.onFail != nil {
			f.onFail()
		}
		return errors.New("the API server said no")
	}
	return f.Client.Create(ctx, obj, opts...)
}

func TestRunKeepsTheAccountsOfAHalfCreatedBatch(t *testing.T) {
	env, kube := fakeEnv(t, 6, makeReady)
	env.Cfg.Batch = 3
	env.Kube = failingCreate{Client: kube, name: accountName(2)}

	result, err := env.Run(testCtx(t), func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "creating "+accountName(2)) {
		t.Fatalf("err = %v, want one about creating %s", err, accountName(2))
	}
	// Account 1 was created before the error, so it is in the result.
	if result.Applied != 1 || result.Accounts[0].Name != accountName(1) {
		t.Fatalf("applied %d, accounts %v; want account 1", result.Applied, result.Accounts)
	}
}

func TestRunReportsInterruptionDuringACreate(t *testing.T) {
	env, kube := fakeEnv(t, 4, makeReady)
	ctx, cancel := context.WithCancel(testCtx(t))
	defer cancel()
	// The request that is under way when Ctrl-C comes fails; the run says it was interrupted.
	env.Kube = failingCreate{Client: ctxAware{kube}, name: accountName(2), onFail: cancel}

	result, err := env.Run(ctx, func(string, ...any) {})
	if err == nil || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("err = %v, want an interruption, not a plain create error", err)
	}
	if result.Applied != 1 {
		t.Fatalf("applied %d, want account 1, which was created before the interruption", result.Applied)
	}
}

func TestRunReportsInterruptionAtStartUp(t *testing.T) {
	env, kube := fakeEnv(t, 2, makeReady)
	env.Kube = ctxAware{kube}
	ctx, cancel := context.WithCancel(testCtx(t))
	cancel()

	if _, err := env.Run(ctx, func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("err = %v, want an interruption, not \"reading namespace: context canceled\"", err)
	}
}

func TestRunWatchesTheAccountsItFoundReady(t *testing.T) {
	// Nothing is left to create: all the run can do is watch. Account 1 was ready at the
	// start (an earlier run), and breaks while the run takes its last look.
	env, kube := fakeEnv(t, 2, makeReady)
	ctx := testCtx(t)
	for n := 1; n <= 2; n++ {
		if err := kube.Create(ctx, newAccount(accountName(n), "aws-eu-central-1")); err != nil {
			t.Fatal(err)
		}
	}
	// List 1 is the start of the run, list 2 the first of the last look.
	env.Kube = &onNthList{Client: kube, n: 2, fn: func() {
		var a v1alpha1.SnowflakeAccount
		if err := kube.Get(ctx, types.NamespacedName{Namespace: Namespace, Name: accountName(1)}, &a); err != nil {
			t.Error(err)
			return
		}
		a.SetConditions(xpv1.ReconcileError(errors.New("rate limited")))
		if err := kube.Update(ctx, &a); err != nil {
			t.Error(err)
		}
	}}

	result, err := env.Run(ctx, func(string, ...any) {})
	var failed *FailedError
	if !errors.As(err, &failed) || failed.Failures[0].Name != accountName(1) || result.Applied != 0 {
		t.Fatalf("err = %v, applied %d; want a failure of %s, found by watching it", err, result.Applied, accountName(1))
	}
}

func TestRunFailsWhenAnEarlierAccountDegradesDuringALaterBatch(t *testing.T) {
	// Creating account 3 (load on the system) breaks account 1, which was ready.
	var kube client.Client
	env, kube := fakeEnv(t, 4, func(a *v1alpha1.SnowflakeAccount) {
		makeReady(a)
		if a.Name == accountName(3) {
			var first v1alpha1.SnowflakeAccount
			if err := kube.Get(context.Background(), types.NamespacedName{Namespace: Namespace, Name: accountName(1)}, &first); err != nil {
				t.Error(err)
				return
			}
			first.SetConditions(xpv1.ReconcileError(errors.New("rate limited")))
			if err := kube.Update(context.Background(), &first); err != nil {
				t.Error(err)
			}
		}
	})

	result, err := env.Run(testCtx(t), func(string, ...any) {})
	var failed *FailedError
	if !errors.As(err, &failed) || len(failed.Failures) != 1 || failed.Failures[0].Name != accountName(1) {
		t.Fatalf("err = %v, want one failure of %s: the run must not end in success", err, accountName(1))
	}
	if result.ReadyTotal != 3 || result.Error == "" || len(result.Failures) != 1 {
		t.Fatalf("ready in total %d, error %q, failures %v", result.ReadyTotal, result.Error, result.Failures)
	}
}

func TestRunRefusesAccountsThatAreBeingDeleted(t *testing.T) {
	env, kube := fakeEnv(t, 3, makeReady)
	being := newAccount(accountName(2), "aws-eu-central-1")
	being.Finalizers = []string{"scaletest/hold"}
	if err := kube.Create(context.Background(), being); err != nil {
		t.Fatal(err)
	}
	if err := kube.Delete(context.Background(), being); err != nil { // the finalizer keeps it
		t.Fatal(err)
	}

	result, err := env.Run(testCtx(t), func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "being deleted") {
		t.Fatalf("err = %v, want a refusal that names the deletion", err)
	}
	if result.Applied != 0 || !strings.Contains(result.Error, "being deleted") {
		t.Fatalf("applied %d, error %q: nothing may be created", result.Applied, result.Error)
	}
}

func TestRunLeavesResumedAccountsOutOfTheDistributions(t *testing.T) {
	env, kube := fakeEnv(t, 2, func(a *v1alpha1.SnowflakeAccount) {
		if a.Name != accountName(1) {
			makeReady(a)
		}
	})
	ctx := testCtx(t)
	if err := kube.Create(ctx, newAccount(accountName(1), "aws-eu-central-1")); err != nil {
		t.Fatal(err)
	}
	// The broken account of the earlier run recovers at the second listing: the first
	// one is that of the start of the run, which finds it broken and resumes it.
	env.Kube = &onNthList{Client: kube, n: 2, fn: func() {
		var a v1alpha1.SnowflakeAccount
		if err := kube.Get(ctx, types.NamespacedName{Namespace: Namespace, Name: accountName(1)}, &a); err != nil {
			t.Error(err)
			return
		}
		makeReady(&a)
		if err := kube.Update(ctx, &a); err != nil {
			t.Error(err)
		}
	}}

	result, err := env.Run(ctx, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if result.Ready != 2 || result.ToReady.Count != 1 {
		t.Fatalf("ready %d, ready distribution %+v; want 2 ready, only the one created now in the distribution", result.Ready, result.ToReady)
	}
	if !result.Accounts[0].Resumed || result.Accounts[1].Resumed {
		t.Fatalf("accounts = %+v", result.Accounts)
	}
}

func TestRunStopsDuringThePauseBetweenBatches(t *testing.T) {
	env, kube := fakeEnv(t, 4, makeReady)
	env.Cfg.BatchInterval = time.Hour
	ctx, cancel := context.WithCancel(testCtx(t))
	// Cancelled right after the listing that shows batch 1 ready: the next thing is the pause.
	env.Kube = &onNthList{Client: kube, n: 2, after: true, fn: cancel}

	result, err := env.Run(ctx, func(string, ...any) {})
	if err == nil || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("err = %v, want an interruption", err)
	}
	if result.Applied != 2 || result.Ready != 2 {
		t.Fatalf("applied %d, ready %d; want the first batch only", result.Applied, result.Ready)
	}
}

// flakyList fails the first failures calls of List.
type flakyList struct {
	client.Client
	failures int
	calls    int
}

func (f *flakyList) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if f.calls++; f.calls <= f.failures {
		return errors.New("the API server timed out")
	}
	return f.Client.List(ctx, list, opts...)
}

type donePending struct{}

func (donePending) Observe(time.Time, []v1alpha1.SnowflakeAccount) (int, []Failure) { return 0, nil }

func TestWaitSurvivesAFewFailedListings(t *testing.T) {
	env, kube := fakeEnv(t, 1, nil)
	env.Kube = &flakyList{Client: kube, failures: maxListFailures - 1}
	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, format) }

	if err := env.waitUntilDone(testCtx(t), donePending{}, "x", logf); err != nil {
		t.Fatalf("err = %v, want the wait to survive %d failed listings", err, maxListFailures-1)
	}
	if len(logged) != maxListFailures-1 {
		t.Fatalf("logged %d failed listings, want %d", len(logged), maxListFailures-1)
	}
}

func TestWaitGivesUpAfterSeveralFailedListings(t *testing.T) {
	env, kube := fakeEnv(t, 1, nil)
	env.Kube = &flakyList{Client: kube, failures: 1000}

	err := env.waitUntilDone(testCtx(t), donePending{}, "x", func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "the API server timed out") {
		t.Fatalf("err = %v, want the listing error", err)
	}
}

func TestWaitReportsAnInterruptionDuringAListing(t *testing.T) {
	env, kube := fakeEnv(t, 1, nil)
	env.Kube = &flakyList{Client: kube, failures: 1000}
	ctx, cancel := context.WithCancel(testCtx(t))
	cancel()
	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, format) }

	err := env.waitUntilDone(ctx, donePending{}, "x", logf)
	if err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("err = %v, want an interruption", err)
	}
	// An interruption ends the wait at once; it is not one of the listings to try again.
	if len(logged) != 0 {
		t.Fatalf("logged %v: the wait went on after the interruption", logged)
	}
}

// onNthList runs fn at the n-th List, before it (or after it, if after is set).
type onNthList struct {
	client.Client
	n, calls int
	after    bool
	fn       func()
}

func (o *onNthList) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	o.calls++
	if o.calls == o.n && !o.after {
		o.fn()
	}
	err := o.Client.List(ctx, list, opts...)
	if o.calls == o.n && o.after {
		o.fn()
	}
	return err
}

// failListFrom fails the n-th List and all after it.
type failListFrom struct {
	client.Client
	n, calls int
}

func (f *failListFrom) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if f.calls++; f.calls >= f.n {
		return errors.New("the API server is gone")
	}
	return f.Client.List(ctx, list, opts...)
}

func TestRunLeavesAccountsOutsideTheTargetAlone(t *testing.T) {
	env, kube := fakeEnv(t, 2, makeReady)
	ctx := testCtx(t)
	for n := 1; n <= 4; n++ {
		a := newAccount(accountName(n), "aws-eu-central-1")
		makeReady(a)
		if err := kube.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	// Accounts 3 and 4 are above the target: not touched, not counted, not waited for.
	var broken v1alpha1.SnowflakeAccount
	if err := kube.Get(ctx, types.NamespacedName{Namespace: Namespace, Name: accountName(4)}, &broken); err != nil {
		t.Fatal(err)
	}
	broken.SetConditions(xpv1.ReconcileError(errors.New("not our business")))
	if err := kube.Update(ctx, &broken); err != nil {
		t.Fatal(err)
	}

	result, err := env.Run(ctx, func(string, ...any) {})
	if err != nil {
		t.Fatalf("err = %v, want success: the target is 2", err)
	}
	if result.Existing != 4 || result.Applied != 0 || result.ReadyTotal != 2 {
		t.Fatalf("existing %d, applied %d, ready in total %d; want 4, 0, 2", result.Existing, result.Applied, result.ReadyTotal)
	}
}

func TestRunReportsAFinalCountThatFails(t *testing.T) {
	env, kube := fakeEnv(t, 2, makeReady)
	// Listings: 1 start, 2 wait for the batch, 3 last look, 4 the count.
	env.Kube = &failListFrom{Client: kube, n: 4}

	result, err := env.Run(testCtx(t), func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "counting the ready accounts") || result.Error == "" || result.ReadyTotal != -1 {
		t.Fatalf("err = %v, result error %q, ready in total %d; want the failed count reported as unknown (-1), not a silent 0",
			err, result.Error, result.ReadyTotal)
	}
}
