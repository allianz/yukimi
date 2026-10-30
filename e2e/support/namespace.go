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
	"math/rand"
	"regexp"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/account/tenant"
)

// The labels of the run namespace use the key names the controller reads
// (internal/account/tenant/labels.go). Only alpha-tester is read in this
// version, which also lifts the check that a region is marked available;
// the others are set so checks added later do not break the suite.
var namespaceLabels = map[string]string{
	"alpha-tester": "true",
	"department":   "e2e",
	"cost-center":  "000000",
	"credit-quota": "1000",
}

// CreateRunNamespace creates the one namespace of this run. It carries a
// timestamp; the Snowflake names of the accounts in it are unique through the
// namespace (they contain a hash of it), so accounts need no timestamp.
func (e *Env) CreateRunNamespace(ctx context.Context) error {
	name := fmt.Sprintf("e2e-%s-%04x", time.Now().Format("20060102150405"), rand.Intn(0x10000))
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: namespaceLabels}}
	if err := e.Kube.Create(ctx, ns); err != nil {
		return fmt.Errorf("creating the run namespace %s: %w", name, err)
	}
	e.Namespace = name
	return nil
}

// DeleteAccount deletes an account the way a tenant does: it creates a
// DeletionPermit for it, then deletes the account. The permit is always
// created, whatever the operator's deletion protection is; whether it is used
// is not this helper's business. The helper only waits for the permit to be
// processed, so that the delete does not start before the operator has seen it.
func (e *Env) DeleteAccount(ctx context.Context, name string) error {
	return e.deleteAccountIn(ctx, e.Namespace, name)
}

// deleteAccountIn is DeleteAccount for any namespace of a run. The permit name
// is unique, so a permit left by an earlier delete of the same name (for
// example in case A5) never stands in the way.
func (e *Env) deleteAccountIn(ctx context.Context, namespace, name string) error {
	permit := &v1alpha1.DeletionPermit{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("delete-%s-%04x", name, rand.Intn(0x10000)), Namespace: namespace},
		Spec: v1alpha1.DeletionPermitSpec{
			TargetRef: v1alpha1.TargetRef{Kind: v1alpha1.SnowflakeAccountKind, Name: name},
			Duration:  metav1.Duration{Duration: time.Hour},
			Reason:    "e2e test cleanup",
		},
	}
	if err := e.Kube.Create(ctx, permit); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating the deletion permit for %s: %w", name, err)
	}
	// Best effort: a permit that is not processed in time does not stop the delete.
	_ = Poll(ctx, 30*time.Second, time.Second, "the deletion permit to be processed", func(ctx context.Context) (bool, error) {
		var got v1alpha1.DeletionPermit
		if err := e.Kube.Get(ctx, types.NamespacedName{Namespace: namespace, Name: permit.Name}, &got); err != nil {
			return false, err
		}
		return got.Status.State != "", nil
	})

	acc := &v1alpha1.SnowflakeAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if err := e.Kube.Delete(ctx, acc); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting account %s: %w", name, err)
	}
	return nil
}

// AccountsIn lists the names of the accounts that still exist in the namespace.
func (e *Env) AccountsIn(ctx context.Context) ([]string, error) {
	return e.accountsIn(ctx, e.Namespace)
}

func (e *Env) accountsIn(ctx context.Context, namespace string) ([]string, error) {
	var accounts v1alpha1.SnowflakeAccountList
	if err := e.Kube.List(ctx, &accounts, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(accounts.Items))
	for _, a := range accounts.Items {
		names = append(names, a.Name)
	}
	sort.Strings(names)
	return names, nil
}

// Cleanup deletes every account of the run, waits until all of them are gone
// and only then drops the namespace.
//
// The order matters. Dropping a namespace deletes the deletion permits in it
// as well, while an account that is still alive keeps its finalizer; with
// deletion protection on the account can then no longer be deleted, and a
// namespace that is being deleted accepts no new permit. So an account that is
// still alive blocks the drop: the namespace is left in place, and the error
// names what is left behind.
func (e *Env) Cleanup(ctx context.Context, timeout time.Duration) error {
	if e.Namespace == "" {
		return nil
	}
	return e.cleanupNamespace(ctx, e.Namespace, timeout)
}

// cleanupNamespace is Cleanup for any namespace of a run.
func (e *Env) cleanupNamespace(ctx context.Context, namespace string, timeout time.Duration) error {
	names, err := e.accountsIn(ctx, namespace)
	if err != nil {
		return fmt.Errorf("listing the accounts of %s: %w", namespace, err)
	}
	for _, name := range names {
		if err := e.deleteAccountIn(ctx, namespace, name); err != nil {
			return err
		}
	}

	var left []string
	pollErr := Poll(ctx, timeout, 5*time.Second, "all accounts of the run to be gone", func(ctx context.Context) (bool, error) {
		var err error
		left, err = e.accountsIn(ctx, namespace)
		return err == nil && len(left) == 0, err
	})
	if pollErr != nil {
		return fmt.Errorf("%d account(s) still exist in namespace %s (%s); the namespace was NOT dropped, because that would leave the Snowflake account(s) behind. Check the operator and delete them: %w",
			len(left), namespace, strings.Join(left, ", "), pollErr)
	}

	// Kubernetes being clean says nothing about Snowflake and AWS: an account
	// that was deleted before the operator knew its locator is gone from
	// Kubernetes but not from Snowflake.
	problems := e.leftovers(ctx, namespace, names)

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
	if err := e.Kube.Delete(ctx, ns); err != nil && !apierrors.IsNotFound(err) {
		problems = append(problems, fmt.Sprintf("dropping namespace %s: %v", namespace, err))
	}
	if len(problems) > 0 {
		return fmt.Errorf("left behind:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// leftovers checks Snowflake and AWS for what the accounts of the run should no
// longer have, and describes what is still there.
func (e *Env) leftovers(ctx context.Context, namespace string, names []string) []string {
	var problems []string
	for _, name := range names {
		resolved := tenant.ResolveName(name, namespace)
		row, err := e.AccountRow(ctx, resolved)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("cannot check Snowflake for %s: %v", resolved, err))
		case row.Found():
			problems = append(problems, fmt.Sprintf("Snowflake still lists the account %s; drop it as org admin: DROP ACCOUNT %s GRACE_PERIOD_IN_DAYS = 7", resolved, resolved))
		}
		info, err := e.SecretInfoIn(ctx, namespace, name)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("cannot check the AWS secret of %s: %v", name, err))
		case info.Exists && !info.ScheduledForDeletion:
			problems = append(problems, fmt.Sprintf("the AWS secret of %s (namespace %s) still exists and is not scheduled for deletion", name, namespace))
		}
	}
	return problems
}

// runNamespacePattern matches the namespaces CreateRunNamespace makes.
var runNamespacePattern = regexp.MustCompile(`^e2e-[0-9]{14}(-[0-9a-f]{4})?$`)

// SweepStale cleans up the namespaces that earlier runs left behind, for example
// after the process was killed. Only namespaces of the run pattern that are older
// than olderThan are touched, so a run that is still going is left alone; olderThan
// is the go test timeout. Each is cleaned the way a run cleans its own namespace,
// including the check of Snowflake and AWS. The lines it returns say what was
// done or what is left; a problem does not stop the run.
func (e *Env) SweepStale(ctx context.Context, olderThan, timeout time.Duration) []string {
	var namespaces corev1.NamespaceList
	if err := e.Kube.List(ctx, &namespaces); err != nil {
		return []string{fmt.Sprintf("could not list the namespaces of earlier runs: %v", err)}
	}
	var lines []string
	for _, ns := range namespaces.Items {
		if !runNamespacePattern.MatchString(ns.Name) || ns.DeletionTimestamp != nil ||
			time.Since(ns.CreationTimestamp.Time) < olderThan {
			continue
		}
		if err := e.cleanupNamespace(ctx, ns.Name, timeout); err != nil {
			lines = append(lines, fmt.Sprintf("namespace %s of an earlier run: %v", ns.Name, err))
			continue
		}
		lines = append(lines, fmt.Sprintf("cleaned up namespace %s of an earlier run", ns.Name))
	}
	return lines
}
