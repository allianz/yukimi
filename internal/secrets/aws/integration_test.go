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

package secretsaws

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/joho/godotenv"

	"github.com/allianz/yukimi/internal/secrets"
)

// full cleanup of the test secret.
func forceDeleteForTest(ctx context.Context, t *testing.T, path secrets.Path) {
	t.Helper()

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(os.Getenv("AWS_REGION")))
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

// TestIntegration_CreateGetDelete only runs via `make test-integration`
// (skipped whenever tests run with -short). It exercises a real AWS Secrets
// Manager call for each of the four methods.
func TestIntegration_CreateGetDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — run via `make test-integration`")
	}
	// go test's working directory is this package's own directory, so the
	// repo-root .env is 3 levels up; godotenv.Load never overrides a
	// variable already set in the environment, and a missing file (e.g. in
	// CI, where these are injected directly) is not an error.
	_ = godotenv.Load("../../../.env")

	ctx := context.Background()
	backend, err := New(os.Getenv("AWS_REGION"), "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Uses the org and namespace .env defines, so the path matches the
	// tenant grammar exactly as a real controller would build it. The
	// account name is stamped with the current time so consecutive runs
	// never collide with a path still inside AWS's default recovery window
	// from a previous, possibly-interrupted run.
	path, err := secrets.NewTenantPath(
		os.Getenv("SNOWFLAKE_ORG"),
		os.Getenv("SAMPLE_CUSTOMER_NAMESPACE"),
		fmt.Sprintf("integration-test-%d", time.Now().UnixNano()),
	)
	if err != nil {
		t.Fatalf("NewTenantPath: %v", err)
	}

	// Cleanup — uses ForceDeleteWithoutRecovery
	t.Cleanup(func() { forceDeleteForTest(ctx, t, path) })

	if err := backend.Create(ctx, path, "integration-test-value"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := backend.Get(ctx, path)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "integration-test-value" {
		t.Fatalf("Get returned %q", got)
	}
	if err := backend.Delete(ctx, path); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
