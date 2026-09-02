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
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/joho/godotenv"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	coreaccount "github.com/allianz/yukimi/internal/account"
	accountmodule "github.com/allianz/yukimi/internal/account/modules/account"
	"github.com/allianz/yukimi/internal/backplane"
	"github.com/allianz/yukimi/internal/config"
	"github.com/allianz/yukimi/internal/secrets"
	secretsaws "github.com/allianz/yukimi/internal/secrets/aws"
	"github.com/allianz/yukimi/internal/snowflake/pool"
)

// forceDeleteForTest mirrors internal/account/modules/account/integration_test.go's
// own helper of the same name, so this test never leaves a throwaway
// platform-credential secret behind even if Create never runs or fails.
func forceDeleteForTest(ctx context.Context, t *testing.T, path secrets.Path) {
	t.Helper()

	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(os.Getenv("AWS_REGION")))
	if err != nil {
		t.Logf("cleanup: failed to load AWS SDK config: %v", err)
		return
	}

	client := secretsmanager.NewFromConfig(cfg)
	_, err = client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId:                   aws.String(path.String()),
		ForceDeleteWithoutRecovery: aws.Bool(true),
	})
	if err != nil {
		t.Logf("cleanup: failed to force-delete secret at %s: %v", path, err)
	}
}

// TestIntegration_CreateObserveDelete only runs via `make test-integration`
// (skipped whenever tests run with -short). It exercises Create, Observe,
// and Delete against a live Snowflake organization end to end (SC-019).
//
// It does not wait for the freshly created account to become reachable
// before calling Observe: 010's own integration test found this can take
// well over two minutes, which is impractical to wait out here. Observe is
// still called immediately after Create to exercise that code path against
// a live connection attempt — its InSync result is logged, not asserted —
// before Delete runs regardless, so this test never leaves a live account
// behind irrespective of whether the account had become reachable yet.
func TestIntegration_CreateObserveDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — run via `make test-integration`")
	}
	// go test's working directory is this package's own directory
	// (internal/controller/snowflakeaccount), so the repo-root .env is 3 levels up.
	_ = godotenv.Load("../../../.env")

	awsBackend, err := secretsaws.New(os.Getenv("AWS_REGION"), "")
	if err != nil {
		t.Fatalf("secretsaws.New: %v", err)
	}
	backend := secrets.NewCachedBackend(awsBackend, 5*time.Minute)

	org := os.Getenv("SNOWFLAKE_ORG")
	baseCfg := &config.BaseConfig{
		Snowflake: config.SnowflakeSettings{
			Org:                    org,
			OrgAdminAccount:        os.Getenv("SNOWFLAKE_ORG_ADMIN_ACCOUNT"),
			OrgAdminAccountLocator: os.Getenv("SNOWFLAKE_ORG_ADMIN_ACCOUNT_LOCATOR"),
			OrgAdminAccountRegion:  os.Getenv("SNOWFLAKE_ORG_ADMIN_ACCOUNT_REGION"),
			UsePrivateLink:         os.Getenv("SNOWFLAKE_USE_PRIVATELINK") == "true",
			DisableOCSPChecks:      os.Getenv("SNOWFLAKE_DISABLE_OCSP_CHECKS") == "true",
			ConnectionProbeTimeout: 5 * time.Second,
			// Left at zero, MaxIdleConnections forces every query onto a fresh
			// physical connection, and this test opens the org-admin
			// connection twice (Create, then Delete). 004's defaults keep
			// both calls on the same physical connection.
			MaxConnectionPoolSize: 10,
			MaxIdleConnections:    2,
			ConnectionMaxLifetime: 30 * time.Minute,
			ConnectionMaxIdleTime: 5 * time.Minute,
		},
		// RotationInterval left at zero would make every OrgAdmin() call —
		// including the second one, from Delete — treat the credential as
		// due for rotation. Two rotations in the same process is unsafe: the
		// second one reads its "current key" from the secret store (already
		// updated by the first) rather than from the connection's actual
		// live credential, so it ends up overwriting the slot the live
		// connection is still authenticated with, not the free one. Confirmed
		// against a live org: this combination orphaned a test account and
		// forced an extra key rotation before the fix.
		Secrets: config.SecretsSettings{RotationInterval: 24 * time.Hour},
	}
	connPool := pool.New(backend, baseCfg)
	t.Cleanup(func() { _ = connPool.Close() })

	region := os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_REGION")
	bp := &backplane.Config{Regions: map[string]backplane.Region{region: {Available: true}}}
	pipeline := coreaccount.New(accountmodule.New(backend, org))

	namespace := os.Getenv("SAMPLE_CUSTOMER_NAMESPACE")
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
	kube := fake.NewClientBuilder().WithObjects(ns).Build()

	e := &external{
		kube: kube,
		log:  logging.NewNopLogger(),
		deps: Dependencies{Config: baseCfg, Backplane: bp, Pipeline: pipeline, Pool: connPool},
	}

	name := fmt.Sprintf("integration-test-%d", time.Now().Unix())
	cr := &v1alpha1.SnowflakeAccount{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Generation: 1},
		Spec: v1alpha1.SnowflakeAccountSpec{
			Region:      region,
			Contacts:    []string{"yukimi-integration-test@example.com"},
			Description: "yukimi 018 integration test — safe to drop",
		},
	}

	// Registered before Create ever runs: 010's own module stores this
	// secret create-only, strictly before CREATE ACCOUNT, so it can exist
	// even if Create never succeeds.
	secretPath, err := secrets.NewTenantPath(org, namespace, name)
	if err != nil {
		t.Fatalf("secrets.NewTenantPath: %v", err)
	}
	t.Cleanup(func() { forceDeleteForTest(context.Background(), t, secretPath) })

	ctx := context.Background()

	if _, err := e.Create(ctx, cr); err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	if cr.Status.AccountLocator == "" {
		t.Fatal("Create() succeeded but status.accountLocator is empty")
	}
	// Always drop the just-created account, regardless of test outcome
	// below — this is the one genuinely destructive step, and it must run
	// even if the reconnect assertion below is skipped or fails.
	t.Cleanup(func() {
		if _, err := e.Delete(context.Background(), cr); err != nil {
			t.Logf("cleanup: Delete() error = %v", err)
		}
	})

	obs, err := e.Observe(ctx, cr)
	if err != nil {
		t.Fatalf("Observe() error = %v, want nil", err)
	}
	t.Logf("Observe() immediately after Create: ResourceExists=%v ResourceUpToDate=%v (reachability lag is expected)",
		obs.ResourceExists, obs.ResourceUpToDate)
}
