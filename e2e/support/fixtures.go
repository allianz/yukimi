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

package support

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
)

// A creating a real account takes minutes, so the tests that need one do not
// create it one after the other. A Fixture is such an account, known by name:
// the first test that needs it creates it, every test that needs it waits for
// it to be ready, and the cases that use different accounts run in parallel
// (they call t.Parallel), so all the accounts are created at the same time.
// The clean-up (Env.Cleanup) deletes them like any other account of the run.

// Fixture is a real account that several tests may use.
type Fixture struct {
	env   *Env
	Name  string
	build func() *v1alpha1.SnowflakeAccount

	startOnce sync.Once
	account   *v1alpha1.SnowflakeAccount
	startErr  error

	readyOnce sync.Once
	ready     *v1alpha1.SnowflakeAccount
	readyErr  error

	deleteOnce sync.Once
	deleteErr  error
}

// Fixture returns the fixture with this name. The first call declares it with
// build; later calls, from other tests, get the same fixture.
func (e *Env) Fixture(name string, build func() *v1alpha1.SnowflakeAccount) *Fixture {
	e.fixtureMu.Lock()
	defer e.fixtureMu.Unlock()
	if e.fixtures == nil {
		e.fixtures = map[string]*Fixture{}
	}
	if f, ok := e.fixtures[name]; ok {
		return f
	}
	f := &Fixture{env: e, Name: name, build: build}
	e.fixtures[name] = f
	return f
}

// Start creates the account, once. It refuses to create more real accounts than
// E2E_MAX_ACCOUNTS allows in one run.
func (f *Fixture) Start(ctx context.Context) (*v1alpha1.SnowflakeAccount, error) {
	f.startOnce.Do(func() {
		f.env.fixtureMu.Lock()
		f.env.started++
		n := f.env.started
		f.env.fixtureMu.Unlock()
		if n > f.env.Cfg.MaxAccounts {
			f.startErr = fmt.Errorf("fixture %q would be real account number %d, but E2E_MAX_ACCOUNTS is %d", f.Name, n, f.env.Cfg.MaxAccounts)
			return
		}
		account := f.build()
		if err := f.env.Kube.Create(ctx, account); err != nil {
			f.startErr = fmt.Errorf("creating fixture %q: %w", f.Name, err)
			return
		}
		f.account = account
	})
	return f.account, f.startErr
}

// Ready creates the account if that has not happened yet and waits until it is
// ready (Ready and Synced are true). Every caller gets the same outcome.
func (f *Fixture) Ready(ctx context.Context, timeout time.Duration) (*v1alpha1.SnowflakeAccount, error) {
	account, err := f.Start(ctx)
	if err != nil {
		return nil, err
	}
	f.readyOnce.Do(func() {
		key := types.NamespacedName{Namespace: account.Namespace, Name: account.Name}
		f.readyErr = Poll(ctx, timeout, 10*time.Second, fmt.Sprintf("fixture %q to become ready", f.Name), func(ctx context.Context) (bool, error) {
			var got v1alpha1.SnowflakeAccount
			if err := f.env.Kube.Get(ctx, key, &got); err != nil {
				return false, err
			}
			if got.GetCondition(xpv1.TypeReady).Status == corev1.ConditionTrue &&
				got.GetCondition(xpv1.TypeSynced).Status == corev1.ConditionTrue {
				f.ready = &got
				return true, nil
			}
			return false, fmt.Errorf("not ready yet: %s", DescribeConditions(&got))
		})
	})
	return f.ready, f.readyErr
}

// DescribeConditions lists the conditions of an account, for messages.
func DescribeConditions(a *v1alpha1.SnowflakeAccount) string {
	var parts []string
	for _, c := range a.Status.Conditions {
		parts = append(parts, fmt.Sprintf("%s=%s (%s: %s)", c.Type, c.Status, c.Reason, c.Message))
	}
	if len(parts) == 0 {
		return "no conditions yet"
	}
	return strings.Join(parts, "; ")
}

// Delete deletes the account the way a tenant does and waits until it is gone
// from Kubernetes. Like Start and Ready it runs once, whoever calls it first:
// the cases that continue after the delete (A4, A5) share it.
func (f *Fixture) Delete(ctx context.Context, readyTimeout, deleteTimeout time.Duration) error {
	account, err := f.Ready(ctx, readyTimeout)
	if err != nil {
		return err
	}
	f.deleteOnce.Do(func() {
		if err := f.env.DeleteAccount(ctx, account.Name); err != nil {
			f.deleteErr = err
			return
		}
		key := types.NamespacedName{Namespace: account.Namespace, Name: account.Name}
		f.deleteErr = Poll(ctx, deleteTimeout, 10*time.Second, fmt.Sprintf("fixture %q to be gone", f.Name), func(ctx context.Context) (bool, error) {
			var got v1alpha1.SnowflakeAccount
			err := f.env.Kube.Get(ctx, key, &got)
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			return false, fmt.Errorf("still present: %s", DescribeConditions(&got))
		})
	})
	return f.deleteErr
}
