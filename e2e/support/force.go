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
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/account/tenant"
	"github.com/allianz/yukimi/internal/secrets"
	"github.com/allianz/yukimi/internal/snowflake/statement"
)

// ForceCleanOptions selects what ForceClean touches and whether it changes anything.
type ForceCleanOptions struct {
	// DryRun only reports what would be done.
	DryRun bool
	// OlderThan spares the namespaces that are younger, for example the one of a
	// run that is still going. Zero takes every run namespace.
	OlderThan time.Duration
}

// forceAccount is one SnowflakeAccount of a run namespace and what belongs to it
// outside Kubernetes.
type forceAccount struct {
	Name         string // metadata.name
	ResolvedName string // the account's name in Snowflake
	SecretID     secrets.Identifier
}

// forceNamespace is one run namespace to be cleaned.
type forceNamespace struct {
	Name     string
	Accounts []forceAccount
}

// planForceClean lists the run namespaces to clean and what they contain. Only
// namespaces that match the run pattern are taken; the controller is not asked
// anything, so an account that is stuck in deletion is found like any other.
func planForceClean(ctx context.Context, kube client.Client, org string, olderThan time.Duration) ([]forceNamespace, error) {
	var namespaces corev1.NamespaceList
	if err := kube.List(ctx, &namespaces); err != nil {
		return nil, fmt.Errorf("listing the namespaces: %w", err)
	}
	var plan []forceNamespace
	for _, ns := range namespaces.Items {
		if !runNamespacePattern.MatchString(ns.Name) || time.Since(ns.CreationTimestamp.Time) < olderThan {
			continue
		}
		var accounts v1alpha1.SnowflakeAccountList
		if err := kube.List(ctx, &accounts, client.InNamespace(ns.Name)); err != nil {
			return nil, fmt.Errorf("listing the accounts of %s: %w", ns.Name, err)
		}
		fn := forceNamespace{Name: ns.Name}
		for _, a := range accounts.Items {
			id, err := secrets.NewTenantIdentifier(org, ns.Name, a.Name)
			if err != nil {
				return nil, fmt.Errorf("secret identifier of %s/%s: %w", ns.Name, a.Name, err)
			}
			fn.Accounts = append(fn.Accounts, forceAccount{
				Name:         a.Name,
				ResolvedName: tenant.ResolveName(a.Name, ns.Name),
				SecretID:     id,
			})
		}
		sort.Slice(fn.Accounts, func(i, j int) bool { return fn.Accounts[i].Name < fn.Accounts[j].Name })
		plan = append(plan, fn)
	}
	sort.Slice(plan, func(i, j int) bool { return plan[i].Name < plan[j].Name })
	return plan, nil
}

// ForceClean removes everything the run namespaces of earlier runs left behind,
// without asking the operator: it drops each account in Snowflake, deletes its
// secret in AWS Secrets Manager at once (no recovery window), strips the
// finalizers of the Kubernetes objects and deletes the namespace. It skips the
// deletion permit gate on purpose; use it only against a test organization.
//
// Every step tolerates that its work is already done, so it can be run again
// after a partial failure. A failing account does not stop the others. The
// lines say what was done (or, in a dry run, would be done); the error is set
// if anything failed.
func (e *Env) ForceClean(ctx context.Context, opts ForceCleanOptions) ([]string, error) {
	plan, err := planForceClean(ctx, e.Kube, e.Cfg.Org(), opts.OlderThan)
	if err != nil {
		return nil, err
	}
	if len(plan) == 0 {
		return []string{"no run namespaces found"}, nil
	}

	var lines, problems []string
	say := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	fail := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		problems = append(problems, msg)
		lines = append(lines, "FAILED: "+msg)
	}
	verb := func(done, planned string) string {
		if opts.DryRun {
			return "would " + planned
		}
		return done
	}

	for _, ns := range plan {
		say("namespace %s: %d account(s)", ns.Name, len(ns.Accounts))
		clean := true
		for _, a := range ns.Accounts {
			if err := e.forceCleanAccount(ctx, ns.Name, a, opts.DryRun, say, verb); err != nil {
				clean = false
				fail("%s/%s: %v", ns.Name, a.Name, err)
			}
		}
		if !clean {
			say("namespace %s kept, because an account could not be cleaned", ns.Name)
			continue
		}
		if err := e.forceDropNamespace(ctx, ns.Name, opts.DryRun); err != nil {
			fail("namespace %s: %v", ns.Name, err)
			continue
		}
		say("%s namespace %s", verb("dropped", "drop"), ns.Name)
	}
	if len(problems) > 0 {
		return lines, fmt.Errorf("%d problem(s):\n  - %s", len(problems), strings.Join(problems, "\n  - "))
	}
	return lines, nil
}

// forceCleanAccount drops the Snowflake account, then deletes its secret. The
// secret goes second: it must not be lost while the account may still be alive.
func (e *Env) forceCleanAccount(ctx context.Context, namespace string, a forceAccount, dryRun bool,
	say func(string, ...any), verb func(done, planned string) string) error {

	if dryRun {
		row, err := e.AccountRow(ctx, a.ResolvedName)
		if err != nil {
			return fmt.Errorf("looking the account up in Snowflake: %w", err)
		}
		say("  %s: Snowflake account %s %s", a.Name, a.ResolvedName, map[bool]string{true: "exists", false: "does not exist"}[row.Found()])
		say("  %s: would drop it and delete the secret %s", a.Name, a.SecretID)
		return nil
	}

	db, err := e.orgAdminDB(ctx)
	if err != nil {
		return fmt.Errorf("connecting to Snowflake as the org admin: %w", err)
	}
	// Same statement as the operator's teardown; IF EXISTS makes a dropped account a no-op.
	sql := fmt.Sprintf("DROP ACCOUNT IF EXISTS IDENTIFIER(?) GRACE_PERIOD_IN_DAYS = %d", e.Cfg.Base.Deletion.GracePeriodDays)
	if err := statement.New(db).Exec(ctx, "e2e: force drop the account", sql, a.ResolvedName); err != nil {
		return fmt.Errorf("dropping the Snowflake account %s: %w", a.ResolvedName, err)
	}
	say("  %s: %s Snowflake account %s", a.Name, verb("dropped", "drop"), a.ResolvedName)

	_, err = secretsmanager.NewFromConfig(e.AWS).DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId:                   aws.String(a.SecretID.String()),
		ForceDeleteWithoutRecovery: aws.Bool(true),
	})
	var notFound *smtypes.ResourceNotFoundException
	if err != nil && !errors.As(err, &notFound) {
		return fmt.Errorf("deleting the secret %s: %w", a.SecretID, err)
	}
	say("  %s: %s secret %s", a.Name, verb("deleted", "delete"), a.SecretID)
	return nil
}

// forceDropNamespace removes the finalizers of the accounts and permits in the
// namespace, which would otherwise keep the namespace in Terminating, and
// deletes the namespace.
func (e *Env) forceDropNamespace(ctx context.Context, namespace string, dryRun bool) error {
	if dryRun {
		return nil
	}
	var accounts v1alpha1.SnowflakeAccountList
	if err := e.Kube.List(ctx, &accounts, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("listing the accounts: %w", err)
	}
	for i := range accounts.Items {
		if err := e.stripFinalizers(ctx, &accounts.Items[i]); err != nil {
			return err
		}
	}
	var permits v1alpha1.DeletionPermitList
	if err := e.Kube.List(ctx, &permits, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("listing the deletion permits: %w", err)
	}
	for i := range permits.Items {
		if err := e.stripFinalizers(ctx, &permits.Items[i]); err != nil {
			return err
		}
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
	if err := e.Kube.Delete(ctx, ns); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting the namespace: %w", err)
	}
	return nil
}

// stripFinalizers clears metadata.finalizers with a merge patch. A patch is
// validated against what it changes, so it also works on an object that the
// current schema would reject as a whole.
func (e *Env) stripFinalizers(ctx context.Context, obj client.Object) error {
	if len(obj.GetFinalizers()) == 0 {
		return nil
	}
	patch := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"finalizers":null}}`))
	if err := e.Kube.Patch(ctx, obj, patch); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("removing the finalizers of %s: %w", obj.GetName(), err)
	}
	return nil
}
