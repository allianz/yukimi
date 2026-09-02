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

	"github.com/allianz/yukimi/internal/config/base"
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

	cfg := &base.Config{
		Snowflake: base.SnowflakeSettings{
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

// TestIntegration_TenantAccount_RotatesStaleCredential only runs via `make
// test-integration` (skipped whenever tests run with -short). Unlike
// TestIntegration_TenantAccount, this test does mutate the pre-existing
// sample tenant credential .env describes: it configures an
// effectively-zero Secrets.RotationInterval so the stored credential is
// always "due", then confirms Pool pushes a fresh key into Snowflake's spare
// key slot (Key Concept: Inline Rotation), records the new credential in the
// secret store, and — the part that actually proves the new key is valid,
// not just written — that a brand-new connection dialed after evicting the
// cache authenticates with it. Rotation is designed to be repeatable and
// self-healing (it only ever touches the slot not currently in use), so
// running this test more than once against the same sample account is safe.
func TestIntegration_TenantAccount_RotatesStaleCredential(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — run via `make test-integration`")
	}
	_ = godotenv.Load("../../../.env")

	backend, err := secretsaws.New(os.Getenv("AWS_REGION"), "")
	if err != nil {
		t.Fatalf("secretsaws.New: %v", err)
	}

	cfg := &base.Config{
		Snowflake: base.SnowflakeSettings{
			Org:                    os.Getenv("SNOWFLAKE_ORG"),
			UsePrivateLink:         os.Getenv("SNOWFLAKE_USE_PRIVATELINK") == "true",
			DisableOCSPChecks:      os.Getenv("SNOWFLAKE_DISABLE_OCSP_CHECKS") == "true",
			ConnectionProbeTimeout: 5 * time.Second,
		},
		// A near-zero interval means any stored credential, however
		// recently rotated, is immediately "due" — the short-interval
		// trick suggested in specs/004-connection-pooling.md's own
		// rotation design, exercised here for real instead of against a
		// fake connection.
		Secrets: base.SecretsSettings{RotationInterval: time.Nanosecond},
	}
	p := New(backend, cfg)
	t.Cleanup(func() { _ = p.Close() })

	namespace := os.Getenv("SAMPLE_CUSTOMER_NAMESPACE")
	accountName := os.Getenv("SAMPLE_CUSTOMER_ACCOUNT")
	locator := os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_LOCATOR")
	region := os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_REGION")

	path, err := secrets.NewTenantPath(cfg.Snowflake.Org, namespace, accountName)
	if err != nil {
		t.Fatalf("NewTenantPath: %v", err)
	}

	ctx := context.Background()

	beforeRaw, beforeRotatedAt, err := backend.Get(ctx, path)
	if err != nil {
		t.Fatalf("reading the credential before rotation: %v", err)
	}
	before, err := secrets.UnmarshalCredentials(beforeRaw, beforeRotatedAt)
	if err != nil {
		t.Fatalf("UnmarshalCredentials (before): %v", err)
	}

	db, err := p.TenantAccount(ctx, namespace, accountName, locator, region)
	if err != nil {
		t.Fatalf("TenantAccount: %v", err)
	}
	var role string
	if err := db.QueryRowContext(ctx, "SELECT CURRENT_ROLE()").Scan(&role); err != nil {
		t.Fatalf("query failed on the connection rotation ran over: %v", err)
	}
	if role != "ACCOUNTADMIN" {
		t.Fatalf("CURRENT_ROLE() = %q, want ACCOUNTADMIN", role)
	}

	afterRaw, afterRotatedAt, err := backend.Get(ctx, path)
	if err != nil {
		t.Fatalf("reading the credential after rotation: %v", err)
	}
	after, err := secrets.UnmarshalCredentials(afterRaw, afterRotatedAt)
	if err != nil {
		t.Fatalf("UnmarshalCredentials (after): %v", err)
	}
	if after.PrivateKey == before.PrivateKey {
		t.Fatal("expected rotation to have replaced the stored private key, but it is unchanged")
	}
	if !afterRotatedAt.After(beforeRotatedAt) {
		t.Fatalf("afterRotatedAt = %v, want it after beforeRotatedAt = %v", afterRotatedAt, beforeRotatedAt)
	}

	// The real test of rotation: evict the cached connection (dialed with
	// the pre-rotation key) and dial fresh. If the new key Snowflake now
	// holds were not actually valid, this authentication would fail.
	p.EvictTenant(namespace, accountName)
	freshDB, err := p.TenantAccount(ctx, namespace, accountName, locator, region)
	if err != nil {
		t.Fatalf("TenantAccount with the rotated credential: %v", err)
	}
	var roleAfterRotation string
	if err := freshDB.QueryRowContext(ctx, "SELECT CURRENT_ROLE()").Scan(&roleAfterRotation); err != nil {
		t.Fatalf("query failed on a connection dialed with the rotated key: %v", err)
	}
	if roleAfterRotation != "ACCOUNTADMIN" {
		t.Fatalf("CURRENT_ROLE() = %q, want ACCOUNTADMIN", roleAfterRotation)
	}
}
