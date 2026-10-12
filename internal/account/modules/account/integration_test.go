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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/joho/godotenv"
	"github.com/snowflakedb/gosnowflake/v2"
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

// forceDeleteForTest permanently deletes the secret at id, bypassing AWS
// Secrets Manager's default recovery window — mirrors
// internal/secrets/aws/integration_test.go's own helper of the same name, so
// this test never leaves a throwaway platform-credential secret behind even
// when CREATE ACCOUNT itself never runs or fails.
func forceDeleteForTest(ctx context.Context, t *testing.T, id secrets.Identifier) {
	t.Helper()

	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(os.Getenv("AWS_REGION")))
	if err != nil {
		t.Logf("cleanup: failed to load AWS SDK config: %v", err)
		return
	}

	client := secretsmanager.NewFromConfig(cfg)

	_, err = client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId:                   aws.String(id.String()),
		ForceDeleteWithoutRecovery: aws.Bool(true),
	})
	if err != nil {
		t.Logf("cleanup: failed to force-delete secret at %s: %v", id, err)
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

	// 30 is base.Config's default deletion grace period (002), which the key store derives its
	// recovery window from; nothing here deletes a secret.
	awsStore, err := secretsaws.New(os.Getenv("AWS_REGION"), "", 30)
	if err != nil {
		t.Fatalf("secretsaws.New: %v", err)
	}
	keyManager := secrets.NewKeyManager(awsStore, 5*time.Minute)

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
		// treat the org-admin credential as due on every OrgAdminDB call — this test calls it
		// several times (create, then Destroy's drop, then Destroy again on cleanup), and
		// rapid rotations overwrite both of the org-admin user's RSA key slots while the
		// already-open *sql.DB's connector keeps signing with the original, now-orphaned key,
		// so any later physical reconnect fails JWT auth. This test isn't exercising rotation
		// at all, so give it a real interval.
		Secrets: base.SecretsSettings{RotationInterval: 24 * time.Hour},
	}
	p := pool.New(keyManager, cfg)
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
	m := New(keyManager, org, 5*time.Minute, 3, false, bpConfig).(*module)
	ctx := context.Background()

	// Registered before Apply ever runs: the module stores this secret
	// create-only, strictly before it opens the org-admin connection, so it
	// can exist even if CREATE ACCOUNT never runs or fails (as it did the
	// first time this test hit a live org — see the module's own Edge Cases).
	secretIdentifier, err := secrets.NewTenantIdentifier(org, namespace, name)
	if err != nil {
		t.Fatalf("secrets.NewTenantIdentifier: %v", err)
	}
	t.Cleanup(func() { forceDeleteForTest(ctx, t, secretIdentifier) })

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

	if o := m.Observe(ctx, mc1); o.State != pipeline.StatePending {
		t.Fatalf("Observe before the account was ever created = %+v, want Pending", o)
	}

	outcome := m.Apply(ctx, mc1)
	if outcome.State != pipeline.StatePending {
		t.Fatalf("Apply (fresh create) = %+v, want Pending()", outcome)
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
	// scheduled-for-deletion identifier is unreadable immediately, which is enough
	// to prove the delete step ran for real.
	if _, _, err := awsStore.Get(ctx, secretIdentifier); err == nil {
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
// Deliberately not sized anywhere near the CRD's own 55-character ceiling —
// a first attempt at a longer name discovered the real, tighter Snowflake
// limit the CRD's own ceiling and this module's own length check (apply.go,
// maxAccountLabelLen) now both account for: CREATE ACCOUNT rejects the
// combined "<org>-<resolved-account-name>" once it exceeds 63 characters
// ("...exceeds the maximum DNS label length of 63 characters"), well below
// the resolved name's own 255-character SQL-identifier ceiling. See
// specs/006-snowflake-account-crd.md's Edge Cases and
// specs/012-account-module.md's Key Concept: Account Name Length Limit. Kept
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
//
// It contains no backslash and no doubled single quote: the value is bound,
// and Snowflake interprets backslash escapes and quote-doubling in bound strings (specs/005-statement-execution.md,
// Verified Bind Positions), which TestIntegration_BindProbes_ValueFidelity
// pins on its own.
const fuzzedDescription = `quotes: ' " "" ; -- SQL line comment /* SQL block comment */ ` +
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

	awsStore, err := secretsaws.New(os.Getenv("AWS_REGION"), "", 30)
	if err != nil {
		t.Fatalf("secretsaws.New: %v", err)
	}
	keyManager := secrets.NewKeyManager(awsStore, 5*time.Minute)

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
	p := pool.New(keyManager, cfg)
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
	m := New(keyManager, org, 5*time.Minute, 3, false, bpConfig).(*module)
	ctx := context.Background()

	secretIdentifier, err := secrets.NewTenantIdentifier(org, namespace, fuzzedName)
	if err != nil {
		t.Fatalf("secrets.NewTenantIdentifier: %v", err)
	}
	t.Cleanup(func() { forceDeleteForTest(ctx, t, secretIdentifier) })

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
	if outcome.State != pipeline.StatePending {
		t.Fatalf("Apply (fresh create with fuzzed fields) = %+v, want Pending()", outcome)
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
	comment, ok := result.Rows[0].StringValue("comment")
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

// TestIntegration_BindProbes_CreateAccount pins which CREATE ACCOUNT positions
// bind (specs/005-statement-execution.md, Verified Bind Positions): the
// account name binds only through IDENTIFIER(?) — a bare ? is a syntax error —
// and every value position binds with a plain ?. That is why runCreateAccount
// binds all of them. Only runs via `make test-integration`.
//
// Each case sends an otherwise fully valid CREATE ACCOUNT (a real generated
// admin key, a real region, a unique name) with exactly one position changed,
// so an outcome can only come from that position. Every account a case
// creates is dropped again.
func TestIntegration_BindProbes_CreateAccount(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — run via `make test-integration`")
	}
	_ = godotenv.Load("../../../../.env")

	p := newProbePool(t)

	ctx := context.Background()
	db, err := p.OrgAdminDB(ctx)
	if err != nil {
		t.Fatalf("OrgAdminDB: %v", err)
	}
	runner := statement.New(db)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("x509.MarshalPKIXPublicKey: %v", err)
	}
	publicKey := base64.StdEncoding.EncodeToString(der)
	region := strings.ToUpper(strings.ReplaceAll(os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_REGION"), "-", "_"))

	const email = "yukimi-integration-test@example.com"
	stamp := time.Now().Unix()

	// Each case binds exactly one position of an otherwise valid CREATE
	// ACCOUNT; everything else is rendered. build receives the unique account
	// name and returns the statement plus its bind args. wantNumber 0 means the
	// statement must succeed; otherwise it is the Snowflake error number the
	// server must answer with. Backslash handling in bound strings is pinned
	// separately by TestIntegration_BindProbes_ValueFidelity, so the comment here
	// deliberately contains none.
	const trickyComment = `it's a "test"; -- not a comment /* nor this */ ünïcode 🐍`
	tests := []struct {
		name  string
		build func(acct string) (string, []any)
		// wantComment, when set, is read back after a successful create.
		wantComment string
		wantNumber  int
	}{
		{"name: ?", func(a string) (string, []any) {
			return create("?", "'platform'", q(publicKey), q(email), region), []any{a}
		}, "", 1003},
		{"name: IDENTIFIER(?)", func(a string) (string, []any) {
			return create("IDENTIFIER(?)", "'platform'", q(publicKey), q(email), region), []any{a}
		}, "", 0},
		{"ADMIN_NAME: ?", func(a string) (string, []any) {
			return create(a, "?", q(publicKey), q(email), region), []any{"platform"}
		}, "", 0},
		{"ADMIN_RSA_PUBLIC_KEY: ?", func(a string) (string, []any) {
			return create(a, "'platform'", "?", q(email), region), []any{publicKey}
		}, "", 0},
		{"EMAIL: ?", func(a string) (string, []any) {
			return create(a, "'platform'", q(publicKey), "?", region), []any{email}
		}, "", 0},
		{"REGION: ?", func(a string) (string, []any) {
			return create(a, "'platform'", q(publicKey), q(email), "?"), []any{region}
		}, "", 0},
		{"COMMENT: ?", func(a string) (string, []any) {
			return create(a, "'platform'", q(publicKey), q(email), region) + " COMMENT=?", []any{trickyComment}
		}, trickyComment, 0},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			accountName := fmt.Sprintf("BINDPROBE_%d_%d", stamp, i)
			sql, args := tc.build(accountName)

			err := runner.Exec(ctx, "create account with bound parameter", sql, args...)
			if err == nil {
				t.Cleanup(func() {
					_ = runner.Exec(ctx, "drop probe account", "DROP ACCOUNT IF EXISTS "+accountName+" GRACE_PERIOD_IN_DAYS = 3")
				})
			}
			expectBind(t, err, tc.wantNumber)

			if err == nil && tc.wantComment != "" {
				res, err := runner.Query(ctx, "read back comment", "SHOW ACCOUNTS LIKE ?", accountName)
				if err != nil {
					t.Fatalf("read back: %v", err)
				}
				got, _ := res.FindRow("account_name", accountName).StringValue("comment")
				if got != tc.wantComment {
					t.Errorf("stored comment = %q, want %q", got, tc.wantComment)
				}
			}
		})
	}
}

// create renders a CREATE ACCOUNT from already-rendered (or placeholder)
// fragments, so each probe case differs in exactly one position.
func create(name, adminName, adminKey, email, region string) string {
	return fmt.Sprintf(
		"CREATE ACCOUNT %s ADMIN_NAME=%s ADMIN_RSA_PUBLIC_KEY=%s ADMIN_USER_TYPE=SERVICE EMAIL=%s EDITION=ENTERPRISE REGION=%s",
		name, adminName, adminKey, email, region)
}

func q(s string) string { return statement.QuoteLiteral(s) }

// newProbePool builds a pool against the live org described by .env, with a
// real rotation interval so repeated OrgAdminDB calls never rotate keys.
func newProbePool(t *testing.T) *pool.Pool {
	t.Helper()
	_ = godotenv.Load("../../../../.env")

	awsStore, err := secretsaws.New(os.Getenv("AWS_REGION"), "", 30)
	if err != nil {
		t.Fatalf("secretsaws.New: %v", err)
	}
	keyManager := secrets.NewKeyManager(awsStore, 5*time.Minute)
	cfg := &base.Config{
		Snowflake: base.SnowflakeSettings{
			Org:                    os.Getenv("SNOWFLAKE_ORG"),
			OrgAdminAccount:        os.Getenv("SNOWFLAKE_ORG_ADMIN_ACCOUNT"),
			OrgAdminAccountLocator: os.Getenv("SNOWFLAKE_ORG_ADMIN_ACCOUNT_LOCATOR"),
			OrgAdminAccountRegion:  os.Getenv("SNOWFLAKE_ORG_ADMIN_ACCOUNT_REGION"),
			UsePrivateLink:         os.Getenv("SNOWFLAKE_USE_PRIVATELINK") == "true",
			DisableOCSPChecks:      os.Getenv("SNOWFLAKE_DISABLE_OCSP_CHECKS") == "true",
			ConnectionProbeTimeout: 5 * time.Second,
		},
		Secrets: base.SecretsSettings{RotationInterval: 24 * time.Hour},
	}
	p := pool.New(keyManager, cfg)
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// TestIntegration_BindProbes_ShowAndAlterAccount completes the picture for the
// positions specs/005-statement-execution.md leaves unverified, without
// creating any account: SHOW ... LIKE (org-admin and tenant connection) and
// ALTER ACCOUNT SET <param> = <value>. The ALTER probes run against the sample
// tenant account, only ever set STATEMENT_TIMEOUT_IN_SECONDS to the value it
// already effectively has, and restore the prior state afterwards.
func TestIntegration_BindProbes_ShowAndAlterAccount(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — run via `make test-integration`")
	}
	p := newProbePool(t)
	ctx := context.Background()

	orgDB, err := p.OrgAdminDB(ctx)
	if err != nil {
		t.Fatalf("OrgAdminDB: %v", err)
	}
	tenantDB, err := p.TenantDB(ctx,
		os.Getenv("SAMPLE_CUSTOMER_NAMESPACE"), os.Getenv("SAMPLE_CUSTOMER_ACCOUNT"),
		os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_LOCATOR"), os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_REGION"))
	if err != nil {
		t.Fatalf("TenantDB: %v", err)
	}
	org, tenant := statement.New(orgDB), statement.New(tenantDB)

	t.Run("SHOW ACCOUNTS LIKE ? (org-admin)", func(t *testing.T) {
		res, err := org.Query(ctx, "show accounts like bound", "SHOW ACCOUNTS LIKE ?", os.Getenv("SAMPLE_CUSTOMER_ACCOUNT"))
		expectBind(t, err, 0)
		if err == nil && !res.FindRow("account_name", os.Getenv("SAMPLE_CUSTOMER_ACCOUNT")).Found() {
			t.Errorf("bound LIKE did not return the sample account")
		}
	})
	t.Run("SHOW ACCOUNTS LIKE IDENTIFIER(?) (org-admin)", func(t *testing.T) {
		_, err := org.Query(ctx, "show accounts like identifier", "SHOW ACCOUNTS LIKE IDENTIFIER(?)", os.Getenv("SAMPLE_CUSTOMER_ACCOUNT"))
		expectBind(t, err, 1003)
	})
	t.Run("SHOW PARAMETERS LIKE ? IN ACCOUNT (tenant)", func(t *testing.T) {
		res, err := tenant.Query(ctx, "show parameters like bound", "SHOW PARAMETERS LIKE ? IN ACCOUNT", "STATEMENT_TIMEOUT_IN_SECONDS")
		expectBind(t, err, 0)
		if err == nil && !res.FindRow("key", "STATEMENT_TIMEOUT_IN_SECONDS").Found() {
			t.Errorf("bound LIKE did not return the parameter")
		}
	})

	// pin captures a parameter's account-level state and registers a cleanup
	// that restores it exactly, so the ALTER probes never leave a trace.
	pin := func(param string) (value string) {
		cur, err := tenant.Query(ctx, "read current parameter", "SHOW PARAMETERS LIKE "+statement.QuoteLiteral(param)+" IN ACCOUNT")
		if err != nil {
			t.Fatalf("read current parameter: %v", err)
		}
		row := cur.FindRow("key", param)
		value, _ = row.StringValue("value")
		level, _ := row.StringValue("level")
		t.Logf("current %s=%q level=%q", param, value, level)
		t.Cleanup(func() {
			var err error
			if level == "ACCOUNT" {
				err = tenant.Exec(ctx, "restore parameter", "ALTER ACCOUNT SET "+param+" = "+value)
			} else {
				err = tenant.Exec(ctx, "unset parameter", "ALTER ACCOUNT UNSET "+param)
			}
			if err != nil {
				t.Errorf("cleanup: restoring %s: %v", param, err)
			}
		})
		return value
	}
	intParam, boolParam := "STATEMENT_TIMEOUT_IN_SECONDS", "PREVENT_UNLOAD_TO_INLINE_URL"
	intValue, boolValue := pin(intParam), pin(boolParam)

	t.Run("ALTER ACCOUNT SET <int param> = ?", func(t *testing.T) {
		expectBind(t, tenant.Exec(ctx, "set int param, bound value", "ALTER ACCOUNT SET "+intParam+" = ?", intValue), 1008)
	})
	t.Run("ALTER ACCOUNT SET <bool param> = ? (string arg)", func(t *testing.T) {
		expectBind(t, tenant.Exec(ctx, "set bool param, bound value", "ALTER ACCOUNT SET "+boolParam+" = ?", boolValue), 1008)
	})
	t.Run("ALTER ACCOUNT SET <bool param> = ? (bool arg)", func(t *testing.T) {
		expectBind(t, tenant.Exec(ctx, "set bool param, bound bool", "ALTER ACCOUNT SET "+boolParam+" = ?", boolValue == "true"), 1008)
	})
	t.Run("ALTER ACCOUNT SET ? = <literal>", func(t *testing.T) {
		expectBind(t, tenant.Exec(ctx, "set param, bound name", "ALTER ACCOUNT SET ? = "+intValue, intParam), 1003)
	})
	t.Run("ALTER ACCOUNT SET IDENTIFIER(?) = <literal>", func(t *testing.T) {
		expectBind(t, tenant.Exec(ctx, "set param, identifier name", "ALTER ACCOUNT SET IDENTIFIER(?) = "+intValue, intParam), 1003)
	})
}

// expectBind asserts the outcome of a probed statement. wantNumber 0 means
// the bound form must execute; any other value is the Snowflake error number
// the server must reject it with. Anything else (a different number, or a
// non-Snowflake error such as a network failure) fails the test, so a probe
// can never pass for an unrelated reason.
func expectBind(t *testing.T, err error, wantNumber int) {
	t.Helper()
	if wantNumber == 0 {
		if err != nil {
			t.Errorf("bound form was rejected, want it to execute: %v", err)
		}
		return
	}
	if err == nil {
		t.Errorf("bound form executed, want rejection with Snowflake error %d", wantNumber)
		return
	}
	var sfErr *gosnowflake.SnowflakeError
	if !errors.As(err, &sfErr) {
		t.Errorf("err = %T (%v), want a *gosnowflake.SnowflakeError", err, err)
		return
	}
	if sfErr.Number != wantNumber {
		t.Errorf("Snowflake error = %d %q, want error %d", sfErr.Number, sfErr.Message, wantNumber)
	}
}

// TestIntegration_BindProbes_UsersAndDrop probes the remaining statements the
// platform emits: SHOW USERS, ALTER USER ... SET EMAIL, the pool's ALTER USER
// ... SET RSA_PUBLIC_KEY[_2] and DESC USER, and DROP ACCOUNT. User statements
// run against a throwaway user in the sample tenant account, never the
// platform user or its keys; DROP ACCOUNT runs against throwaway accounts
// created for the purpose. Everything created is removed again.
func TestIntegration_BindProbes_UsersAndDrop(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — run via `make test-integration`")
	}
	p := newProbePool(t)
	ctx := context.Background()

	orgDB, err := p.OrgAdminDB(ctx)
	if err != nil {
		t.Fatalf("OrgAdminDB: %v", err)
	}
	tenantDB, err := p.TenantDB(ctx,
		os.Getenv("SAMPLE_CUSTOMER_NAMESPACE"), os.Getenv("SAMPLE_CUSTOMER_ACCOUNT"),
		os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_LOCATOR"), os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_REGION"))
	if err != nil {
		t.Fatalf("TenantDB: %v", err)
	}
	org, tenant := statement.New(orgDB), statement.New(tenantDB)

	genKey := func() string {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("rsa.GenerateKey: %v", err)
		}
		der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
		if err != nil {
			t.Fatalf("x509.MarshalPKIXPublicKey: %v", err)
		}
		return base64.StdEncoding.EncodeToString(der)
	}

	// --- throwaway user in the tenant account (setup is rendered on purpose:
	// it is not under test) ---
	user := fmt.Sprintf("BINDPROBE_USER_%d", time.Now().Unix())
	if err := tenant.Exec(ctx, "create probe user", "CREATE USER "+user+" TYPE=SERVICE"); err != nil {
		t.Fatalf("create probe user: %v", err)
	}
	t.Cleanup(func() {
		if err := tenant.Exec(ctx, "drop probe user", "DROP USER IF EXISTS "+user); err != nil {
			t.Errorf("cleanup: drop probe user: %v", err)
		}
	})

	t.Run("SHOW USERS LIKE ?", func(t *testing.T) {
		res, err := tenant.Query(ctx, "show users like bound", "SHOW USERS LIKE ?", user)
		expectBind(t, err, 0)
		if err == nil && !res.FindRow("name", user).Found() {
			t.Errorf("bound LIKE did not return the probe user")
		}
	})
	t.Run("ALTER USER IDENTIFIER(?) SET EMAIL = ?", func(t *testing.T) {
		expectBind(t, tenant.Exec(ctx, "set email, all bound",
			"ALTER USER IDENTIFIER(?) SET EMAIL = ?", user, "yukimi-bind-probe@example.com"), 0)
	})
	t.Run("ALTER USER IDENTIFIER(?) SET EMAIL = <literal>", func(t *testing.T) {
		expectBind(t, tenant.Exec(ctx, "set email, bound name",
			"ALTER USER IDENTIFIER(?) SET EMAIL = 'yukimi-bind-probe@example.com'", user), 0)
	})
	t.Run("ALTER USER <literal> SET EMAIL = ?", func(t *testing.T) {
		expectBind(t, tenant.Exec(ctx, "set email, bound value",
			"ALTER USER "+user+" SET EMAIL = ?", "yukimi-bind-probe@example.com"), 0)
	})
	t.Run("ALTER USER IDENTIFIER(?) SET RSA_PUBLIC_KEY = ?", func(t *testing.T) {
		expectBind(t, tenant.Exec(ctx, "set key, all bound",
			"ALTER USER IDENTIFIER(?) SET RSA_PUBLIC_KEY = ?", user, genKey()), 0)
	})
	t.Run("ALTER USER IDENTIFIER(?) SET RSA_PUBLIC_KEY_2 = <literal>", func(t *testing.T) {
		expectBind(t, tenant.Exec(ctx, "set key 2, bound name",
			"ALTER USER IDENTIFIER(?) SET RSA_PUBLIC_KEY_2 = '"+genKey()+"'", user), 0)
	})
	t.Run("ALTER USER <literal> SET ? = <literal> (slot name)", func(t *testing.T) {
		expectBind(t, tenant.Exec(ctx, "set key, bound slot name",
			"ALTER USER "+user+" SET ? = '"+genKey()+"'", "RSA_PUBLIC_KEY"), 1003)
	})
	t.Run("DESC USER IDENTIFIER(?)", func(t *testing.T) {
		res, err := tenant.Query(ctx, "desc user bound", "DESC USER IDENTIFIER(?)", user)
		expectBind(t, err, 0)
		if err == nil && !res.FindRow("property", "NAME").Found() {
			t.Errorf("DESC USER returned no NAME property")
		}
	})

	// --- DROP ACCOUNT on throwaway accounts ---
	stamp := time.Now().Unix()
	region := strings.ToUpper(strings.ReplaceAll(os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_REGION"), "-", "_"))
	publicKey := genKey()
	newAccount := func(t *testing.T, suffix string) string {
		t.Helper()
		acct := fmt.Sprintf("BINDPROBE_%d_%s", stamp, suffix)
		err := org.Exec(ctx, "create probe account",
			"CREATE ACCOUNT IDENTIFIER(?) ADMIN_NAME = ? ADMIN_RSA_PUBLIC_KEY = ? ADMIN_USER_TYPE = SERVICE EMAIL = ? EDITION = ENTERPRISE REGION = ?",
			acct, "platform", publicKey, "yukimi-integration-test@example.com", region)
		if err != nil {
			t.Fatalf("create probe account: %v", err)
		}
		t.Cleanup(func() {
			_ = org.Exec(ctx, "drop probe account", "DROP ACCOUNT IF EXISTS "+acct+" GRACE_PERIOD_IN_DAYS = 3")
		})
		return acct
	}
	dropped := func(t *testing.T, acct string) bool {
		t.Helper()
		res, err := org.Query(ctx, "check dropped", "SHOW ACCOUNTS HISTORY LIKE ?", acct)
		if err != nil {
			t.Fatalf("check dropped: %v", err)
		}
		d, _ := res.FindRow("account_name", acct).Value("dropped_on")
		return d != nil
	}

	t.Run("DROP ACCOUNT IF EXISTS IDENTIFIER(?) GRACE_PERIOD_IN_DAYS = <literal>", func(t *testing.T) {
		acct := newAccount(t, "dropname")
		expectBind(t, org.Exec(ctx, "drop, bound name",
			"DROP ACCOUNT IF EXISTS IDENTIFIER(?) GRACE_PERIOD_IN_DAYS = 3", acct), 0)
		if !dropped(t, acct) {
			t.Errorf("bound DROP ACCOUNT succeeded but the account was not dropped")
		}
	})
	t.Run("DROP ACCOUNT IF EXISTS <literal> GRACE_PERIOD_IN_DAYS = ?", func(t *testing.T) {
		acct := newAccount(t, "dropdays")
		expectBind(t, org.Exec(ctx, "drop, bound grace period",
			"DROP ACCOUNT IF EXISTS "+acct+" GRACE_PERIOD_IN_DAYS = ?", 3), 1003)
		if dropped(t, acct) {
			t.Errorf("rejected DROP ACCOUNT nevertheless dropped the account")
		}
	})
}

// TestIntegration_BindProbes_ValueFidelity compares what Snowflake stores
// when the same awkward value (quotes, backslashes, comment markers) is bound
// with ? versus rendered through QuoteLiteral, using ALTER USER ... SET
// COMMENT on a throwaway user in the sample tenant account. A bind that
// executes is not necessarily a bind that preserves the value.
func TestIntegration_BindProbes_ValueFidelity(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — run via `make test-integration`")
	}
	p := newProbePool(t)
	ctx := context.Background()
	tenantDB, err := p.TenantDB(ctx,
		os.Getenv("SAMPLE_CUSTOMER_NAMESPACE"), os.Getenv("SAMPLE_CUSTOMER_ACCOUNT"),
		os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_LOCATOR"), os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_REGION"))
	if err != nil {
		t.Fatalf("TenantDB: %v", err)
	}
	tenant := statement.New(tenantDB)

	user := fmt.Sprintf("BINDPROBE_USER_%d", time.Now().Unix())
	if err := tenant.Exec(ctx, "create probe user", "CREATE USER "+user+" TYPE=SERVICE"); err != nil {
		t.Fatalf("create probe user: %v", err)
	}
	t.Cleanup(func() {
		if err := tenant.Exec(ctx, "drop probe user", "DROP USER IF EXISTS "+user); err != nil {
			t.Errorf("cleanup: drop probe user: %v", err)
		}
	})

	stored := func(t *testing.T) string {
		t.Helper()
		res, err := tenant.Query(ctx, "desc user", "DESC USER IDENTIFIER(?)", user)
		if err != nil {
			t.Fatalf("desc user: %v", err)
		}
		v, _ := res.FindRow("property", "COMMENT").StringValue("value")
		return v
	}

	// altered lists the values Snowflake does not store verbatim when bound,
	// with what it stores instead (specs/005, Verified Bind Positions).
	// Security takes priority over fidelity, so this is the accepted behavior.
	// Rendered through QuoteLiteral, every value must round-trip unchanged.
	cases := []struct {
		name, value string
		boundStores string // empty: stored verbatim when bound
	}{
		{"plain", "hello world", ""},
		{"single quote", `it's`, ""},
		{"double quote", `say "hi"`, ""},
		{"trailing backslash", `end\`, ""},
		{"comment markers", `x -- y /* z */ ; w`, ""},
		{"unicode", `ünïcode 🐍 日本語`, ""},
		{"two single quotes", `a''b`, `a'b`},
		{"backslash b", `a\b`, "a\b"},
		{"backslash n", `a\nb`, "a\nb"},
		{"backslash space", `a\ b`, "a b"},
		{"double backslash", `a\\b`, `a\b`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantBound := tc.value
			if tc.boundStores != "" {
				wantBound = tc.boundStores
			}

			expectBind(t, tenant.Exec(ctx, "set comment, bound",
				"ALTER USER IDENTIFIER(?) SET COMMENT = ?", user, tc.value), 0)
			if got := stored(t); got != wantBound {
				t.Errorf("bound: stored %q, want %q", got, wantBound)
			}

			expectBind(t, tenant.Exec(ctx, "set comment, rendered",
				"ALTER USER "+user+" SET COMMENT = "+statement.QuoteLiteral(tc.value)), 0)
			if got := stored(t); got != tc.value {
				t.Errorf("rendered: stored %q, want %q", got, tc.value)
			}
		})
	}
}
