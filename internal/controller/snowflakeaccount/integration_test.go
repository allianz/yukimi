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

package snowflakeaccount

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/joho/godotenv"

	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	accountmodule "github.com/allianz/yukimi/internal/account/modules/account"
	"github.com/allianz/yukimi/internal/account/pipeline"
	"github.com/allianz/yukimi/internal/config/backplane"
	"github.com/allianz/yukimi/internal/config/base"
	"github.com/allianz/yukimi/internal/secrets"
	secretsaws "github.com/allianz/yukimi/internal/secrets/aws"
	"github.com/allianz/yukimi/internal/snowflake/pool"
)

// forceDeleteForTest permanently deletes the secret at path, bypassing AWS
// Secrets Manager's default recovery window — mirrors
// internal/account/modules/account/integration_test.go's own helper of the
// same name, so this test never leaves a throwaway platform-credential
// secret behind even when CREATE ACCOUNT itself never runs or fails.
func forceDeleteForTest(ctx context.Context, t *testing.T, path secrets.Path) {
	t.Helper()

	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(os.Getenv("AWS_REGION")))
	if err != nil {
		t.Logf("cleanup: failed to load AWS SDK config: %v", err)
		return
	}
	c := secretsmanager.NewFromConfig(cfg)
	if _, err := c.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId:                   aws.String(path.String()),
		ForceDeleteWithoutRecovery: aws.Bool(true),
	}); err != nil {
		t.Logf("cleanup: failed to force-delete secret at %s: %v", path, err)
	}
}

// TestIntegration_CreateThenDestroy proves SC-020's create-then-destroy round
// trip against a live Snowflake organization, a live AWS Secrets Manager, and
// a real Kubernetes API: a single apply() call is enough to run CREATE
// ACCOUNT and populate status.accountLocator/accountName/accountUrl (Teardown
// never depends on a tenant login, only the org-admin connection), then an
// authorizing SnowflakeDeletionRequest plus Delete together remove it. This
// deliberately does not wait for the Ready condition, which additionally
// requires a successful tenant login (mc.TenantDB) once the account's
// post-create grace period elapses — not exercised here. Skipped whenever
// tests run with -short; run via `make test-integration`.
//
// Requires everything internal/account/modules/account's own integration
// test requires (see that file), plus a reachable Kubernetes API — this test
// uses whatever context kubectl's current context/KUBECONFIG points at, and
// expects the SnowflakeAccount/SnowflakeDeletionRequest CRDs already
// installed there (e.g. via `make dev`).
func TestIntegration_CreateThenDestroy(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — run via `make test-integration`")
	}
	// go test's working directory is this package's own directory
	// (internal/controller/snowflakeaccount), so the repo-root .env is 3
	// levels up.
	_ = godotenv.Load("../../../.env")

	restCfg, err := ctrl.GetConfig()
	if err != nil {
		t.Fatalf("ctrl.GetConfig: %v", err)
	}
	scheme := runtime.NewScheme()
	if err := v1alpha1.SchemeBuilder.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to build scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to build scheme: %v", err)
	}
	kube, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}

	awsBackend, err := secretsaws.New(os.Getenv("AWS_REGION"), "", 30)
	if err != nil {
		t.Fatalf("secretsaws.New: %v", err)
	}
	backend := secrets.NewCachedBackend(awsBackend, 5*time.Minute)

	org := os.Getenv("SNOWFLAKE_ORG")
	cfg := &base.Config{
		Snowflake: base.SnowflakeSettings{
			Org:                    org,
			OrgAdminAccount:        os.Getenv("SNOWFLAKE_ORG_ADMIN_ACCOUNT"),
			OrgAdminAccountLocator: os.Getenv("SNOWFLAKE_ORG_ADMIN_ACCOUNT_LOCATOR"),
			OrgAdminAccountRegion:  os.Getenv("SNOWFLAKE_ORG_ADMIN_ACCOUNT_REGION"),
			UsePrivateLink:         os.Getenv("SNOWFLAKE_USE_PRIVATELINK") == "true",
			DisableOCSPChecks:      os.Getenv("SNOWFLAKE_DISABLE_OCSP_CHECKS") == "true",
			ConnectionProbeTimeout: 5 * time.Second,
			// Unused by this test's single apply() call — only read once
			// AccountLocator is already set, on a later reconcile.
			AccountCreationGracePeriod: 5 * time.Second,
		},
		Secrets:  base.SecretsSettings{RotationInterval: 24 * time.Hour},
		Deletion: base.DeletionSettings{GracePeriodDays: 30},
	}
	p := pool.New(backend, cfg)
	t.Cleanup(func() { _ = p.Close() })

	namespace := os.Getenv("SAMPLE_CUSTOMER_NAMESPACE")
	name := fmt.Sprintf("integration-test-%d", time.Now().Unix())
	region := os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_REGION")
	cr := &v1alpha1.SnowflakeAccount{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Generation: 1},
		Spec: v1alpha1.SnowflakeAccountSpec{
			Region:      region,
			Contact:     "yukimi-integration-test@example.com",
			Description: "yukimi 020 integration test — safe to drop",
			IdentityIntegration: v1alpha1.IdentityIntegration{
				RoleBindings: map[string]string{"ACCOUNTADMIN": "acme-admins"},
			},
		},
	}

	bpConfig := &backplane.Config{Regions: map[string]backplane.Region{region: {}}}
	pl := pipeline.New(accountmodule.New(backend, org, cfg.Snowflake.AccountCreationGracePeriod, cfg.Deletion.GracePeriodDays, bpConfig))
	e := &external{
		kube:     kube,
		pool:     p,
		pipeline: pl,
		cfg:      cfg,
		logger:   logging.NewLogrLogger(zap.New(zap.UseDevMode(true)).WithName("test")),
		record:   event.NewNopRecorder(),
	}

	// Registered before apply ever runs: the account module stores this
	// secret create-only, strictly before it opens the org-admin connection,
	// so it can exist even if CREATE ACCOUNT never runs or fails.
	secretPath, err := secrets.NewTenantPath(org, namespace, name)
	if err != nil {
		t.Fatalf("secrets.NewTenantPath: %v", err)
	}
	t.Cleanup(func() { forceDeleteForTest(context.Background(), t, secretPath) })

	// Real Destroy through the pipeline, not a hand-rolled cleanup —
	// registered so it still runs even if an assertion below fails early.
	t.Cleanup(func() {
		if cr.Status.AccountLocator == "" {
			return
		}
		mc := pipeline.NewModuleContext(cr, nil, nil, p)
		if err := pl.Destroy(context.Background(), mc); err != nil {
			t.Errorf("cleanup: Destroy: %v", err)
		}
	})

	if err := e.apply(context.Background(), cr); err != nil {
		t.Logf("apply: %v (expected here — cr was never persisted via kube.Create, so the "+
			"trailing status update fails; the account itself is still created)", err)
	}
	if cr.Status.AccountLocator == "" {
		t.Fatal("expected AccountLocator to be set after apply()")
	}
	if cr.Status.AccountName == "" || cr.Status.AccountURL == "" {
		t.Fatalf("expected accountName/accountUrl to be set, got %+v", cr.Status)
	}

	// SC-020's deletion half: an Active SnowflakeDeletionRequest, created
	// against the real Kubernetes API, authorizes Delete's teardown.
	req := &v1alpha1.SnowflakeDeletionRequest{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-deletion", Namespace: namespace},
		Spec: v1alpha1.SnowflakeDeletionRequestSpec{
			TargetRef: v1alpha1.TargetRef{Kind: v1alpha1.SnowflakeAccountKind, Name: name},
			Duration:  metav1.Duration{Duration: time.Hour},
			Reason:    "yukimi 020 integration test",
		},
	}
	if err := kube.Create(context.Background(), req); err != nil {
		t.Fatalf("failed to create SnowflakeDeletionRequest: %v", err)
	}
	t.Cleanup(func() {
		_ = kube.Delete(context.Background(), req)
	})
	req.Status.State = "Active"
	if err := kube.Status().Update(context.Background(), req); err != nil {
		t.Fatalf("failed to activate SnowflakeDeletionRequest: %v", err)
	}

	if _, err := e.Delete(context.Background(), cr); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var persisted v1alpha1.SnowflakeDeletionRequest
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(req), &persisted); err != nil {
		t.Fatalf("failed to fetch persisted SnowflakeDeletionRequest: %v", err)
	}
	if persisted.Status.State != "Consumed" {
		t.Fatalf("expected the deletion request to become Consumed, got %q", persisted.Status.State)
	}

	// The account itself is gone: a fresh connection attempt against the
	// same locator must now fail.
	mc := pipeline.NewModuleContext(cr, nil, nil, p)
	if _, err := mc.TenantDB(context.Background()); err == nil {
		t.Error("expected the tenant connection to fail after Delete dropped the account")
	}

	// deleteCredential only schedules removal (a 30-day AWS recovery window)
	// — a scheduled-for-deletion path is unreadable immediately, enough to
	// prove the delete step ran for real.
	if _, _, err := awsBackend.Get(context.Background(), secretPath); err == nil {
		t.Error("platform credential still readable after Delete")
	}
}
