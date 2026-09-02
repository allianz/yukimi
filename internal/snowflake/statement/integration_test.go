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

package statement

import (
	"context"
	stderrors "errors"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"github.com/snowflakedb/gosnowflake"

	"github.com/allianz/yukimi/internal/config/base"
	"github.com/allianz/yukimi/internal/secrets"
	secretsaws "github.com/allianz/yukimi/internal/secrets/aws"
	"github.com/allianz/yukimi/internal/snowflake/pool"
)

// TestIntegration_RunnerAgainstLiveSnowflake only runs via `make
// test-integration` (skipped whenever tests run with -short). It exercises a
// real AWS Secrets Manager read and a real Snowflake connection against the
// pre-existing sample tenant account .env describes — this test never
// creates or seeds that credential, only reads it.
func TestIntegration_RunnerAgainstLiveSnowflake(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — run via `make test-integration`")
	}
	// go test's working directory is this package's own directory
	// (internal/snowflake/statement), so the repo-root .env is 3 levels up.
	_ = godotenv.Load("../../../.env")

	backend, err := secretsaws.New(os.Getenv("AWS_REGION"), "")
	if err != nil {
		t.Fatalf("secretsaws.New: %v", err)
	}
	cached := secrets.NewCachedBackend(backend, 5*time.Minute)

	cfg := &base.Config{
		Snowflake: base.SnowflakeSettings{
			Org:                    os.Getenv("SNOWFLAKE_ORG"),
			UsePrivateLink:         os.Getenv("SNOWFLAKE_USE_PRIVATELINK") == "true",
			DisableOCSPChecks:      os.Getenv("SNOWFLAKE_DISABLE_OCSP_CHECKS") == "true",
			ConnectionProbeTimeout: 5 * time.Second,
		},
	}
	p := pool.New(cached, cfg)
	t.Cleanup(func() { _ = p.Close() })

	ctx := context.Background()
	db, err := p.TenantAccount(ctx,
		os.Getenv("SAMPLE_CUSTOMER_NAMESPACE"), os.Getenv("SAMPLE_CUSTOMER_ACCOUNT"),
		os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_LOCATOR"), os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_REGION"))
	if err != nil {
		t.Fatalf("TenantAccount: %v", err)
	}

	r := New(db)

	t.Run("Query materializes real rows", func(t *testing.T) {
		result, err := r.Query(ctx, "select current role", "SELECT CURRENT_ROLE() AS ROLE")
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(result.Rows) != 1 {
			t.Fatalf("len(Rows) = %d, want 1", len(result.Rows))
		}
		if role, ok := result.Rows[0]["ROLE"].(string); !ok || role != "ACCOUNTADMIN" {
			t.Fatalf(`Rows[0]["ROLE"] = %v, want "ACCOUNTADMIN"`, result.Rows[0]["ROLE"])
		}
	})

	t.Run("Query against no matching rows returns the zero Result", func(t *testing.T) {
		pattern := QuoteLiteral("yukimi_statement_integration_test_nonexistent_db_xyz")
		result, err := r.Query(ctx, "check database exists", "SHOW DATABASES LIKE "+pattern)
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(result.Rows) != 0 || result.Columns != nil {
			t.Fatalf("Query() = %+v, want the zero-value Result{} for no matching rows", result)
		}
	})

	t.Run("Exec succeeds against a real connection", func(t *testing.T) {
		if err := r.Exec(ctx, "set query tag",
			"ALTER SESSION SET QUERY_TAG = ?", "yukimi-statement-integration-test"); err != nil {
			t.Fatalf("Exec: %v", err)
		}
	})

	t.Run("a real compilation error decorates as *Error with driver fields", func(t *testing.T) {
		_, err := r.Query(ctx, "select from missing table",
			"SELECT * FROM YUKIMI_STATEMENT_INTEGRATION_TEST_NONEXISTENT_TABLE_XYZ")
		if err == nil {
			t.Fatal("Query against a nonexistent table returned nil error")
		}

		var stmtErr *Error
		if !stderrors.As(err, &stmtErr) {
			t.Fatalf("error is not a *statement.Error: %v", err)
		}
		if stmtErr.Number == 0 || stmtErr.SQLState == "" {
			t.Errorf("expected real driver fields populated, got Number=%d SQLState=%q", stmtErr.Number, stmtErr.SQLState)
		}

		var sfErr *gosnowflake.SnowflakeError
		if !stderrors.As(err, &sfErr) {
			t.Errorf("errors.As did not reach the underlying *gosnowflake.SnowflakeError through Unwrap")
		}
	})
}
