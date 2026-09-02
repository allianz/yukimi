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

package account

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	coreaccount "github.com/allianz/yukimi/internal/account"
	"github.com/allianz/yukimi/internal/config"
	"github.com/allianz/yukimi/internal/secrets"
	secretsaws "github.com/allianz/yukimi/internal/secrets/aws"
	"github.com/allianz/yukimi/internal/snowflake/pool"
	"github.com/allianz/yukimi/internal/snowflake/statement"
)

// forceDeleteForTest permanently deletes the secret at path, bypassing AWS
// Secrets Manager's default recovery window — mirrors
// internal/secrets/aws/integration_test.go's own helper of the same name, so
// this test never leaves a throwaway platform-credential secret behind even
// when CREATE ACCOUNT itself never runs or fails.
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

// TestIntegration_Create only runs via `make test-integration` (skipped
// whenever tests run with -short). It creates a brand-new Snowflake account
// in the live organization .env describes — the one genuinely destructive
// integration test in this codebase, since 010 is the first module that
// ever mutates organization-wide state — and confirms Apply captures a
// locator (SC-018's create half).
//
// It deliberately does not also reconnect to the new account on a second
// ModuleContext: a freshly created account was observed taking well over two
// minutes to become reachable even over an already-healthy PrivateLink path
// (confirmed separately against the pre-existing sample account), which is
// Snowflake's own backend account-activation lag rather than anything this
// module controls — impractical to wait out in a test.
//
// Requires, in addition to the AWS/SNOWFLAKE_ORG variables every other
// integration test in this repo already uses: SNOWFLAKE_ORG_ADMIN_ACCOUNT,
// SNOWFLAKE_ORG_ADMIN_ACCOUNT_LOCATOR, SNOWFLAKE_ORG_ADMIN_ACCOUNT_REGION
// (the org-admin connection's own locator/region, analogous to
// SAMPLE_CUSTOMER_ACCOUNT_LOCATOR/_REGION for the tenant side). The new
// account is created in SAMPLE_CUSTOMER_ACCOUNT_REGION — the same real, open
// cloud-region the sample tenant account already lives in — rather than a
// dedicated variable.
func TestIntegration_Create(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — run via `make test-integration`")
	}
	// go test's working directory is this package's own directory
	// (internal/account/modules/account), so the repo-root .env is 4 levels up.
	_ = godotenv.Load("../../../../.env")

	awsBackend, err := secretsaws.New(os.Getenv("AWS_REGION"), "")
	if err != nil {
		t.Fatalf("secretsaws.New: %v", err)
	}
	backend := secrets.NewCachedBackend(awsBackend, 5*time.Minute)

	org := os.Getenv("SNOWFLAKE_ORG")
	cfg := &config.BaseConfig{
		Snowflake: config.SnowflakeSettings{
			Org:                    org,
			OrgAdminAccount:        os.Getenv("SNOWFLAKE_ORG_ADMIN_ACCOUNT"),
			OrgAdminAccountLocator: os.Getenv("SNOWFLAKE_ORG_ADMIN_ACCOUNT_LOCATOR"),
			OrgAdminAccountRegion:  os.Getenv("SNOWFLAKE_ORG_ADMIN_ACCOUNT_REGION"),
			UsePrivateLink:         os.Getenv("SNOWFLAKE_USE_PRIVATELINK") == "true",
			DisableOCSPChecks:      os.Getenv("SNOWFLAKE_DISABLE_OCSP_CHECKS") == "true",
			ConnectionProbeTimeout: 5 * time.Second,
			// Left at zero, MaxIdleConnections forces every query onto a
			// fresh physical connection, and this test opens the org-admin
			// connection twice in the same process (Apply, then the
			// t.Cleanup drop below). 004's defaults keep both calls on the
			// same physical connection.
			MaxConnectionPoolSize: 10,
			MaxIdleConnections:    2,
			ConnectionMaxLifetime: 30 * time.Minute,
			ConnectionMaxIdleTime: 5 * time.Minute,
		},
		// RotationInterval left at zero would make every OrgAdmin() call
		// treat the credential as due for rotation. Two rotations in the
		// same process is unsafe: the second one reads its "current key"
		// from the secret store (already updated by the first) rather than
		// from the connection's actual live credential, so it ends up
		// overwriting the slot the live connection is still authenticated
		// with, not the free one — confirmed against a live org while
		// implementing 018's own integration test.
		Secrets: config.SecretsSettings{RotationInterval: 24 * time.Hour},
	}
	p := pool.New(backend, cfg)
	t.Cleanup(func() { _ = p.Close() })

	namespace := os.Getenv("SAMPLE_CUSTOMER_NAMESPACE")
	name := fmt.Sprintf("integration-test-%d", time.Now().Unix())
	cr := &v1alpha1.SnowflakeAccount{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: v1alpha1.SnowflakeAccountSpec{
			Region:      os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_REGION"),
			Contacts:    []string{"yukimi-integration-test@example.com"},
			Description: "yukimi 010 integration test — safe to drop",
		},
	}

	m := New(backend, org).(*module)
	ctx := context.Background()

	// Registered before Apply ever runs: the module stores this secret
	// create-only, strictly before it opens the org-admin connection, so it
	// can exist even if CREATE ACCOUNT never runs or fails (as it did the
	// first time this test hit a live org — see the module's own Edge Cases).
	secretPath, err := secrets.NewTenantPath(org, namespace, name)
	if err != nil {
		t.Fatalf("secrets.NewTenantPath: %v", err)
	}
	t.Cleanup(func() { forceDeleteForTest(ctx, t, secretPath) })

	t.Cleanup(func() {
		if cr.Status.AccountLocator == "" {
			return
		}
		orgAdminDB, err := p.OrgAdmin(ctx)
		if err != nil {
			t.Logf("cleanup: could not open org-admin connection to drop %s: %v", cr.Status.AccountLocator, err)
			return
		}
		resolvedName := coreaccount.NewModuleContext(cr, namespace, nil, nil, nil, p).ResolvedAccountName()
		// GRACE_PERIOD_IN_DAYS is required by current Snowflake versions; 3 is
		// its minimum. This only starts the drop's grace period — the account
		// is gone once the grace period elapses, not immediately.
		if err := statement.New(orgAdminDB).Exec(ctx, "drop integration test account",
			"DROP ACCOUNT "+resolvedName+" GRACE_PERIOD_IN_DAYS = 3"); err != nil {
			t.Logf("cleanup: failed to drop %s: %v", resolvedName, err)
		}
	})

	mc1 := coreaccount.NewModuleContext(cr, namespace, nil, nil, nil, p)

	inSync, _ := m.Observe(ctx, mc1)
	if inSync {
		t.Fatal("Observe reported in-sync before the account was ever created")
	}

	outcome := m.Apply(ctx, mc1)
	if outcome.State != coreaccount.StateDone {
		t.Fatalf("Apply (fresh create) = %+v, want Done()", outcome)
	}
	if mc1.Locator() == "" {
		t.Fatal("Apply succeeded but ModuleContext has no locator")
	}

	// A real 018 would persist status.accountLocator right after Apply
	// returns; set it so the drop-account cleanup above can resolve the
	// account it needs to remove.
	cr.Status.AccountLocator = mc1.Locator()
}
