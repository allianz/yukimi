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

package pool

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv"

	"github.com/allianz/yukimi/internal/config"
	"github.com/allianz/yukimi/internal/secrets"
	secretsaws "github.com/allianz/yukimi/internal/secrets/aws"
)

// TestIntegration_TenantAccount only runs via `make test-integration`
// (skipped whenever tests run with -short). It exercises a real AWS Secrets
// Manager read and a real Snowflake connection against the pre-existing
// sample tenant account .env describes — this test never creates or seeds
// that credential, only reads it.
func TestIntegration_TenantAccount(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — run via `make test-integration`")
	}
	// go test's working directory is this package's own directory
	// (internal/snowflake/pool), so the repo-root .env is 3 levels up.
	_ = godotenv.Load("../../../.env")

	backend, err := secretsaws.New(os.Getenv("AWS_REGION"), "")
	if err != nil {
		t.Fatalf("secretsaws.New: %v", err)
	}
	cached := secrets.NewCachedBackend(backend, 5*time.Minute)

	cfg := &config.BaseConfig{
		Snowflake: config.SnowflakeSettings{
			Org:                    os.Getenv("SNOWFLAKE_ORG"),
			UsePrivateLink:         os.Getenv("SNOWFLAKE_USE_PRIVATELINK") == "true",
			DisableOCSPChecks:      os.Getenv("SNOWFLAKE_DISABLE_OCSP_CHECKS") == "true",
			ConnectionProbeTimeout: 5 * time.Second,
		},
	}
	p := New(cached, cfg)
	t.Cleanup(func() { _ = p.Close() })

	ctx := context.Background()
	db, err := p.TenantAccount(ctx,
		os.Getenv("SAMPLE_CUSTOMER_NAMESPACE"), os.Getenv("SAMPLE_CUSTOMER_ACCOUNT"),
		os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_LOCATOR"), os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_REGION"))
	if err != nil {
		t.Fatalf("TenantAccount: %v", err)
	}

	var role string
	if err := db.QueryRowContext(ctx, "SELECT CURRENT_ROLE()").Scan(&role); err != nil {
		t.Fatalf("query failed on a connection TenantAccount reported healthy: %v", err)
	}
	if role != "ACCOUNTADMIN" {
		t.Fatalf("CURRENT_ROLE() = %q, want ACCOUNTADMIN", role)
	}
}
