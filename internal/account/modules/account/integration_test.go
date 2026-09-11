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
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/joho/godotenv"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/account/pipeline"
	"github.com/allianz/yukimi/internal/config/backplane"
	"github.com/allianz/yukimi/internal/config/base"
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

// TestIntegration_CreateThenDestroy only runs via `make test-integration`
// (skipped whenever tests run with -short). It creates a brand-new Snowflake
// account in the live organization .env describes — the one genuinely
// destructive integration test in this codebase, since 012 is the first
// module that ever mutates organization-wide state — confirms Apply captures
// a locator, then tears it down through the real pipeline.Destroy ->
// (*module).Teardown path (drop account, evict the pooled connection, delete
// the credential) rather than a hand-rolled cleanup, fully covering SC-018's
// create-then-destroy round trip.
//
// It deliberately does not also reconnect to the new account on a second
// ModuleContext before destroying it: a freshly created account was observed
// taking well over two minutes to become reachable even over an
// already-healthy PrivateLink path (confirmed separately against the
// pre-existing sample account), which is Snowflake's own backend
// account-activation lag rather than anything this module controls —
// impractical to wait out in a test.
//
// Requires, in addition to the AWS/SNOWFLAKE_ORG variables every other
// integration test in this repo already uses: SNOWFLAKE_ORG_ADMIN_ACCOUNT,
// SNOWFLAKE_ORG_ADMIN_ACCOUNT_LOCATOR, SNOWFLAKE_ORG_ADMIN_ACCOUNT_REGION
// (the org-admin connection's own locator/region, analogous to
// SAMPLE_CUSTOMER_ACCOUNT_LOCATOR/_REGION for the tenant side). The new
// account is created in SAMPLE_CUSTOMER_ACCOUNT_REGION — the same real, open
// cloud-region the sample tenant account already lives in — rather than a
// dedicated variable.
func TestIntegration_CreateThenDestroy(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — run via `make test-integration`")
	}
	// go test's working directory is this package's own directory
	// (internal/account/modules/account), so the repo-root .env is 4 levels up.
	_ = godotenv.Load("../../../../.env")

	// 30 is base.Config's default deletion grace period (002), which the backend derives its
	// recovery window from; nothing here deletes a secret.
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
		},
		// A zero RotationInterval makes maybeRotateLocked (internal/snowflake/pool/rotate.go)
		// treat the org-admin credential as due on every OrgAdmin call — this test calls it
		// several times (create, then Destroy's drop, then Destroy again on cleanup), and
		// rapid rotations overwrite both of the org-admin user's RSA key slots while the
		// already-open *sql.DB's connector keeps signing with the original, now-orphaned key,
		// so any later physical reconnect fails JWT auth. This test isn't exercising rotation
		// at all, so give it a real interval.
		Secrets: base.SecretsSettings{RotationInterval: 24 * time.Hour},
	}
	p := pool.New(backend, cfg)
	t.Cleanup(func() { _ = p.Close() })

	namespace := os.Getenv("SAMPLE_CUSTOMER_NAMESPACE")
	name := fmt.Sprintf("integration-test-%d", time.Now().Unix())
	region := os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_REGION")
	cr := &v1alpha1.SnowflakeAccount{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: v1alpha1.SnowflakeAccountSpec{
			Region:      region,
			Contact:     "yukimi-integration-test@example.com",
			Description: "yukimi 012 integration test — safe to drop",
		},
	}

	bpConfig := &backplane.Config{Regions: map[string]backplane.Region{region: {Available: true}}}
	m := New(backend, org, 5*time.Minute, 3, bpConfig).(*module)
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

	pl := pipeline.New(m)
	mc1 := pipeline.NewModuleContext(cr, nil, nil, p)

	// Real Destroy, not a hand-rolled cleanup: registered so it still runs
	// even if an assertion below fails early, and doubling as an idempotence
	// check (Teardown must be safe to call twice, SC-025) on the success path
	// below, since it then finds everything already gone.
	t.Cleanup(func() {
		if cr.Status.AccountLocator == "" {
			return
		}
		if err := pl.Destroy(ctx, mc1); err != nil {
			t.Errorf("cleanup: Destroy: %v", err)
		}
	})

	inSync, _ := m.Observe(ctx, mc1)
	if inSync {
		t.Fatal("Observe reported in-sync before the account was ever created")
	}

	outcome := m.Apply(ctx, mc1)
	if outcome.State != pipeline.StatePending || !outcome.Abort {
		t.Fatalf("Apply (fresh create) = %+v, want Pending().Aborting()", outcome)
	}
	if cr.Status.AccountLocator == "" {
		t.Fatal("Apply succeeded but cr.Status.AccountLocator is still empty")
	}
	// Apply sets cr.Status.AccountLocator directly (no separate persist step
	// needed here); the Destroy cleanup above reads it from cr.

	// SC-018's destroy half: tear the freshly created account down through
	// the real Pipeline.Destroy -> Module.Teardown path against the live org
	// and secret store.
	if err := pl.Destroy(ctx, mc1); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	// deleteCredential only schedules removal (a 30-day AWS recovery window
	// derived from gracePeriodDays=30 via secretsaws.New above) — a
	// scheduled-for-deletion path is unreadable immediately, which is enough
	// to prove the delete step ran for real.
	if _, _, err := awsBackend.Get(ctx, secretPath); err == nil {
		t.Error("platform credential still readable after Destroy")
	}

	// The account itself is gone (restorable, not connectable): a fresh
	// connection attempt against the same locator must now fail.
	mc2 := pipeline.NewModuleContext(cr, nil, nil, p)
	if _, err := mc2.TenantDB(ctx); err == nil {
		t.Error("expected the tenant connection to fail after Destroy dropped the account")
	}
}

// fuzzedName is metadata.name for TestIntegration_CreateWithFuzzedFields:
// every character class the CRD's own `^[a-z][a-z0-9-]*$` allows, interleaved
// with hyphens, so ResolveName (006) and the BareIdentifier render it passes
// through (apply.go) both see letters, digits and separators mixed rather
// than the short, tidy name every other test in this file uses.
//
// Deliberately not sized anywhere near the CRD's own 249-character ceiling —
// a first attempt at that size discovered a real, tighter Snowflake limit
// this codebase's specs don't mention anywhere: CREATE ACCOUNT rejects the
// combined "<org>-<resolved-account-name>" once it exceeds 63 characters
// ("...exceeds the maximum DNS label length of 63 characters"), well below
// what the resolved name's own 255-character SQL-identifier ceiling
// (snowflakeaccount_types.go's own comment) would suggest is safe. Kept
// short enough here to stay well clear of that limit for any real org name,
// since demonstrating it is not this test's job — reproduce it by making
// this string as long as the CRD alone allows.
var fuzzedName = fmt.Sprintf("fuzz-a1b2-c3d4-e5f6-g7h8-i9j0-%d", time.Now().Unix())

// fuzzedContact is spec.Contact for TestIntegration_CreateWithFuzzedFields:
// every separator character permitted by both the CRD's email Pattern and a
// real email address's syntax (dot, hyphen, plus-tag, underscore, a
// multi-label domain) packed into one address, deliberately without a quote,
// semicolon, or comment marker — those are permitted by the CRD's Pattern
// but not by Snowflake's own server-side EMAIL validation, so including them
// would fail CREATE ACCOUNT for a reason unrelated to SQL escaping (see
// TestIntegration_CreateWithFuzzedFields's own doc comment).
const fuzzedContact = "yukimi.fuzz-test+integration_01@sub-domain.example-test.co.io"

// fuzzedDescription is spec.Description for
// TestIntegration_CreateWithFuzzedFields: as much injection- and
// encoding-shaped content as fits in the CRD's 1024-character MaxLength,
// packed into the one field CREATE ACCOUNT renders via
// statement.QuoteLiteral (runCreateAccount in apply.go) with no format
// constraint of its own — unlike spec.Contact and spec.Region, both
// restricted by a CRD Pattern and, for Contact, by Snowflake's own
// server-side validation too.
const fuzzedDescription = `quotes: ' '' "" ; -- SQL line comment /* SQL block comment */ backslash: \ \\ ` +
	`unicode: héllo wörld 日本語 emoji 🐍🔥 café naïve ` +
	`whitespace: tab->` + "\t" + `<- newline->` + "\n" + `<-end control chars done`

// TestIntegration_CreateWithFuzzedFields only runs via `make
// test-integration` (skipped whenever tests run with -short). It creates a
// second real, throwaway Snowflake account exactly like
// TestIntegration_CreateThenDestroy's happy path, except every field this
// module actually renders into CREATE ACCOUNT — metadata.name (via
// ResolveName, 006), spec.Contact, and spec.Description — is filled with as
// much fuzz as each field's own validation allows while still expecting
// CREATE ACCOUNT to succeed; spec.Region and the namespace stay fixed since
// they must match the real Backplane Config (007) and test infrastructure
// this test runs against, not something safe to fuzz. A real CREATE ACCOUNT
// succeeding at all already shows fuzzedDescription didn't break out of its
// literal and corrupt the statement; reading COMMENT back through SHOW
// ACCOUNTS additionally proves Snowflake stored the exact bytes this test
// sent, not a truncated or mangled variant. Otherwise mirrors
// TestIntegration_CreateThenDestroy's env vars, config, and teardown.
func TestIntegration_CreateWithFuzzedFields(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — run via `make test-integration`")
	}
	_ = godotenv.Load("../../../../.env")

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
		},
		Secrets: base.SecretsSettings{RotationInterval: 24 * time.Hour},
	}
	p := pool.New(backend, cfg)
	t.Cleanup(func() { _ = p.Close() })

	namespace := os.Getenv("SAMPLE_CUSTOMER_NAMESPACE")
	region := os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_REGION")
	cr := &v1alpha1.SnowflakeAccount{
		ObjectMeta: metav1.ObjectMeta{Name: fuzzedName, Namespace: namespace},
		Spec: v1alpha1.SnowflakeAccountSpec{
			Region:      region,
			Contact:     fuzzedContact,
			Description: fuzzedDescription,
		},
	}

	bpConfig := &backplane.Config{Regions: map[string]backplane.Region{region: {Available: true}}}
	m := New(backend, org, 5*time.Minute, 3, bpConfig).(*module)
	ctx := context.Background()

	secretPath, err := secrets.NewTenantPath(org, namespace, fuzzedName)
	if err != nil {
		t.Fatalf("secrets.NewTenantPath: %v", err)
	}
	t.Cleanup(func() { forceDeleteForTest(ctx, t, secretPath) })

	pl := pipeline.New(m)
	mc := pipeline.NewModuleContext(cr, nil, nil, p)

	t.Cleanup(func() {
		if cr.Status.AccountLocator == "" {
			return
		}
		if err := pl.Destroy(ctx, mc); err != nil {
			t.Errorf("cleanup: Destroy: %v", err)
		}
	})

	outcome := m.Apply(ctx, mc)
	if outcome.State != pipeline.StatePending || !outcome.Abort {
		t.Fatalf("Apply (fresh create with fuzzed fields) = %+v, want Pending().Aborting()", outcome)
	}
	if cr.Status.AccountLocator == "" {
		t.Fatal("Apply succeeded but cr.Status.AccountLocator is still empty")
	}

	orgAdminDB, err := mc.OrgAdminDB(ctx)
	if err != nil {
		t.Fatalf("OrgAdminDB: %v", err)
	}
	result, err := statement.New(orgAdminDB).Query(ctx, "read back created account",
		"SHOW ACCOUNTS LIKE "+statement.QuoteLiteral(mc.ResolvedAccountName()))
	if err != nil {
		t.Fatalf("SHOW ACCOUNTS: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("len(Rows) = %d, want 1", len(result.Rows))
	}
	comment, ok := commentValue(result.Rows[0])
	if !ok {
		t.Fatal("SHOW ACCOUNTS row has no comment column")
	}
	if comment != fuzzedDescription {
		t.Fatalf("stored comment = %q, want %q — the fuzzed payload was corrupted or truncated in storage", comment, fuzzedDescription)
	}

	if err := pl.Destroy(ctx, mc); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
}

// commentValue extracts the comment column from a SHOW ACCOUNTS row,
// matching the key case-insensitively for the same reason
// accountNameAndLocator does in apply.go.
func commentValue(row map[string]any) (string, bool) {
	for key, value := range row {
		if strings.EqualFold(key, "comment") {
			s, ok := value.(string)
			return s, ok
		}
	}
	return "", false
}
