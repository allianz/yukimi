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
	"database/sql"
	stderrors "errors"
	"fmt"
	"math/rand"
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

// sampleTenantDB opens a real connection to the pre-existing sample tenant
// account .env describes — the same fixture every TestIntegration_* in this
// file uses, factored out once they numbered more than one. Callers that
// need session-scoped statements pinned to one physical connection (ALTER
// SESSION SET and friends) should call db.Conn(ctx) themselves, as
// TestIntegration_RunnerAgainstLiveSnowflake does; this alone only
// guarantees a live *sql.DB, not a single session.
func sampleTenantDB(t *testing.T) (db *sql.DB, ctx context.Context) {
	t.Helper()
	// go test's working directory is this package's own directory
	// (internal/snowflake/statement), so the repo-root .env is 3 levels up.
	_ = godotenv.Load("../../../.env")

	// 30 is base.Config's default deletion grace period (002), which the backend derives its
	// recovery window from; nothing here deletes a secret.
	backend, err := secretsaws.New(os.Getenv("AWS_REGION"), "", 30)
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
		// A zero RotationInterval makes maybeRotateLocked treat the sample
		// account's credential as always due, silently rotating it on every
		// call — every test using this helper is documented as read-only, so
		// give it a real interval (see the same note in
		// account/integration_test.go).
		Secrets: base.SecretsSettings{RotationInterval: 24 * time.Hour},
	}
	p := pool.New(cached, cfg)
	t.Cleanup(func() { _ = p.Close() })

	ctx = context.Background()
	db, err = p.TenantAccount(ctx,
		os.Getenv("SAMPLE_CUSTOMER_NAMESPACE"), os.Getenv("SAMPLE_CUSTOMER_ACCOUNT"),
		os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_LOCATOR"), os.Getenv("SAMPLE_CUSTOMER_ACCOUNT_REGION"))
	if err != nil {
		t.Fatalf("TenantAccount: %v", err)
	}
	return db, ctx
}

// TestIntegration_RunnerAgainstLiveSnowflake only runs via `make
// test-integration` (skipped whenever tests run with -short). It exercises a
// real AWS Secrets Manager read and a real Snowflake connection against the
// pre-existing sample tenant account .env describes — this test never
// creates or seeds that credential, only reads it.
func TestIntegration_RunnerAgainstLiveSnowflake(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — run via `make test-integration`")
	}
	db, ctx := sampleTenantDB(t)
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

	// QuoteLiteral's only real caller (account/modules/account, 012) always
	// renders it into a LIKE pattern or a plain string literal, never into a
	// position that itself terminates a statement — so an injection-shaped
	// payload rendered by QuoteLiteral must reach Snowflake as inert text: no
	// error, and no row from a database that doesn't exist matches it.
	t.Run("QuoteLiteral neutralizes an injection-shaped SHOW LIKE pattern", func(t *testing.T) {
		payload := `nonexistent' OR '1'='1'; DROP DATABASE YUKIMI_STATEMENT_INTEGRATION_TEST_XYZ; --`
		result, err := r.Query(ctx, "check database exists", "SHOW DATABASES LIKE "+QuoteLiteral(payload))
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(result.Rows) != 0 || result.Columns != nil {
			t.Fatalf("Query() = %+v, want the zero-value Result{} — the injection payload matched no real database", result)
		}
	})

	// ALTER SESSION SET is scoped to one physical connection, but r wraps
	// *sql.DB — whose Exec/Query each may check out a different pooled
	// connection — so verifying anything round-trips through a session
	// parameter needs its own *sql.Conn (which also satisfies Executor, see
	// statement.go) to pin every statement below to the same session.
	// Without that pin, both subtests below would read back an empty tag
	// regardless of what was set, on whichever connection's session
	// default — a false result unrelated to escaping either way.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	defer func() { _ = conn.Close() }()
	connRunner := New(conn)

	// notes-snowflake-sql-mechanics.md §7 (specs/005-statement-execution.md)
	// marks several statement positions "Unconfirmed" for IDENTIFIER(?)/?
	// binding; ALTER SESSION SET's value is not one of the positions it
	// names, but this codebase has no confirmed-safe example of binding into
	// it either. Verified live here: gosnowflake sends ? unbound rather than
	// erroring or substituting, and Snowflake accepts the bare, unquoted `?`
	// character as a one-character string literal — silently setting
	// QUERY_TAG to "?" itself, not the intended value. This is exactly the
	// "render, don't bind" case QuoteLiteral exists for; the subtest below
	// uses it instead.
	t.Run("a bind parameter into ALTER SESSION SET's value is not honored", func(t *testing.T) {
		if err := connRunner.Exec(ctx, "set query tag via unbound placeholder",
			"ALTER SESSION SET QUERY_TAG = ?", "should-be-ignored"); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		result, err := connRunner.Query(ctx, "read back query tag", "SELECT CURRENT_QUERY_TAG() AS TAG")
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(result.Rows) != 1 {
			t.Fatalf("len(Rows) = %d, want 1", len(result.Rows))
		}
		if tag, ok := result.Rows[0]["TAG"].(string); !ok || tag != "?" {
			t.Fatalf(`Rows[0]["TAG"] = %v, want the literal "?" — `+
				`if this ever changes, ALTER SESSION SET's value position may have become bindable`, result.Rows[0]["TAG"])
		}
	})

	// Since binding is not an option here (previous subtest), QuoteLiteral
	// rendering is this codebase's only real defense for a session
	// parameter's value — an injection-shaped payload must round-trip
	// through it byte-for-byte.
	t.Run("QuoteLiteral round-trips an injection-shaped ALTER SESSION SET value", func(t *testing.T) {
		payload := `tag' OR '1'='1'; DROP DATABASE YUKIMI_STATEMENT_INTEGRATION_TEST_XYZ; -- "quoted" /* comment */`
		if err := connRunner.Exec(ctx, "set query tag via QuoteLiteral",
			"ALTER SESSION SET QUERY_TAG = "+QuoteLiteral(payload)); err != nil {
			t.Fatalf("Exec: %v", err)
		}
		result, err := connRunner.Query(ctx, "read back query tag", "SELECT CURRENT_QUERY_TAG() AS TAG")
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(result.Rows) != 1 {
			t.Fatalf("len(Rows) = %d, want 1", len(result.Rows))
		}
		if tag, ok := result.Rows[0]["TAG"].(string); !ok || tag != payload {
			t.Fatalf(`Rows[0]["TAG"] = %v, want %q`, result.Rows[0]["TAG"], payload)
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

// quoteAlphabet is every character QuoteLiteral's and QuoteIdentifier's own
// escaping logic branches on (the two quote characters and the backslash
// that a lone-vs-doubled check exists for), plus one ordinary letter so a
// combination can separate two special characters instead of only ever
// running them together.
var quoteAlphabet = []rune{'\'', '"', '\\', 'a'}

// combinations returns every string of exactly length runes drawn from
// alphabet, in a fixed order — an exhaustive (not sampled) sweep, since at
// these short lengths the whole space is cheap enough to just cover
// completely rather than risk a fuzzer's coverage-guided search skipping the
// one three-character boundary that matters.
func combinations(alphabet []rune, length int) []string {
	if length == 0 {
		return []string{""}
	}
	rest := combinations(alphabet, length-1)
	out := make([]string, 0, len(alphabet)*len(rest))
	for _, r := range alphabet {
		for _, suffix := range rest {
			out = append(out, string(r)+suffix)
		}
	}
	return out
}

// fuzzedTextPayloads returns a large, deterministic set of strings for
// TestIntegration_QuoteLiteralFuzzedRoundTrip and
// TestIntegration_QuoteIdentifierFuzzedRoundTrip: an exhaustive sweep of
// every short combination of quoteAlphabet (exactly the kind of boundary —
// a lone trailing backslash, a quote immediately after a backslash — that
// found QuoteLiteral's backslash-escaping gap), named edge cases layering in
// SQL metacharacters/comment markers/unicode, and long pseudo-random strings
// drawn from a wider "dangerous-character" palette for coverage closer to a
// realistic adversarial free-text field. Deterministic (a fixed
// rand.Source) so a failure reproduces across runs.
func fuzzedTextPayloads() []string {
	var payloads []string
	for length := 1; length <= 3; length++ {
		payloads = append(payloads, combinations(quoteAlphabet, length)...)
	}

	payloads = append(payloads,
		``,
		`'; DROP TABLE X; --`,
		`\'; DROP TABLE X; --`,
		`" OR "1"="1`,
		`/* block comment */ -- line comment`,
		`héllo wörld 日本語 🐍🔥 café naïve`,
		"tab\tnewline\ncarriage\r",
	)

	palette := []rune(`abcXYZ019 '"\;-/*`)
	rnd := rand.New(rand.NewSource(42))
	for range 6 {
		length := 20 + rnd.Intn(60)
		b := make([]rune, length)
		for j := range b {
			b[j] = palette[rnd.Intn(len(palette))]
		}
		payloads = append(payloads, string(b))
	}

	return payloads
}

// TestIntegration_QuoteLiteralFuzzedRoundTrip only runs via `make
// test-integration` (skipped whenever tests run with -short). It runs one
// SQL statement with every reason to expect success — SELECT <literal> AS
// VALUE, the simplest possible vehicle for a string literal — across
// fuzzedTextPayloads()'s sweep, asserting Snowflake returns each payload
// back byte-for-byte. FuzzQuoteLiteral (render_test.go) already proves
// QuoteLiteral's output is self-consistently reversible by this package's
// own decode rule; it can never notice a live disagreement with Snowflake's
// actual grammar — which is exactly how QuoteLiteral's backslash-escaping
// gap went unnoticed until a real account's COMMENT came back corrupted
// (see QuoteLiteral's doc comment, render.go, and
// account/modules/account/integration_test.go's
// TestIntegration_CreateWithFuzzedFields, which first surfaced it).
func TestIntegration_QuoteLiteralFuzzedRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — run via `make test-integration`")
	}
	db, ctx := sampleTenantDB(t)
	r := New(db)

	for _, payload := range fuzzedTextPayloads() {
		t.Run(fmt.Sprintf("%q", payload), func(t *testing.T) {
			result, err := r.Query(ctx, "round-trip a literal", "SELECT "+QuoteLiteral(payload)+" AS VALUE")
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if len(result.Rows) != 1 {
				t.Fatalf("len(Rows) = %d, want 1", len(result.Rows))
			}
			if got, ok := result.Rows[0]["VALUE"].(string); !ok || got != payload {
				t.Fatalf(`Rows[0]["VALUE"] = %v, want %q`, result.Rows[0]["VALUE"], payload)
			}
		})
	}
}

// TestIntegration_QuoteIdentifierFuzzedRoundTrip only runs via `make
// test-integration` (skipped whenever tests run with -short). QuoteIdentifier
// has no production caller yet — its doc comment marks IDENTIFIER(?) binding
// at every position that would use it "Unconfirmed" (render.go) — so this is
// the only live confirmation anywhere in this codebase that its own quoting
// is correct at all, not just self-consistent with its own decode rule the
// way FuzzQuoteIdentifier's round trip (render_test.go) is. SELECT 1 AS
// <identifier> is the simplest SQL with every reason to expect success; a
// quoted alias's column name comes back exactly as written, so
// fuzzedTextPayloads()'s sweep is asserted against Result.Columns instead of
// a row value.
func TestIntegration_QuoteIdentifierFuzzedRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — run via `make test-integration`")
	}
	db, ctx := sampleTenantDB(t)
	r := New(db)

	for _, payload := range fuzzedTextPayloads() {
		if payload == "" {
			// A zero-length delimited identifier is rejected by Snowflake
			// outright — not a QuoteIdentifier escaping question, and
			// already covered in isolation by TestQuoteIdentifier's own
			// "empty" case (render_test.go).
			continue
		}
		t.Run(fmt.Sprintf("%q", payload), func(t *testing.T) {
			result, err := r.Query(ctx, "round-trip an identifier", "SELECT 1 AS "+QuoteIdentifier(payload))
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if len(result.Columns) != 1 {
				t.Fatalf("len(Columns) = %d, want 1", len(result.Columns))
			}
			if result.Columns[0] != payload {
				t.Fatalf("Columns[0] = %q, want %q", result.Columns[0], payload)
			}
		})
	}
}
