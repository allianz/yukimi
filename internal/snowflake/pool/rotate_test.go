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
	"database/sql"
	"database/sql/driver"
	stderrors "errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/allianz/yukimi/internal/secrets"
)

// --- rotation test fakes -----------------------------------------------------

// descUserRow is one row a fake DESC USER response yields.
type descUserRow struct {
	property string
	value    string
}

// rotateFakeConn answers DESC USER with canned rows and records every
// query/exec it's asked to run — the existing fakeConn in pool_test.go
// implements neither, since no existing test ever triggers rotation (every
// seeded credential is fresh).
type rotateFakeConn struct {
	mu         *sync.Mutex
	descRows   []descUserRow
	execErr    error
	queryErr   error
	queryCalls *[]string
	execCalls  *[]string
}

func (c rotateFakeConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c rotateFakeConn) Close() error                        { return nil }
func (c rotateFakeConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

func (c rotateFakeConn) Query(query string, _ []driver.Value) (driver.Rows, error) {
	c.mu.Lock()
	*c.queryCalls = append(*c.queryCalls, query)
	c.mu.Unlock()
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	return &fakeDescUserRows{rows: c.descRows}, nil
}

func (c rotateFakeConn) Exec(query string, _ []driver.Value) (driver.Result, error) {
	c.mu.Lock()
	*c.execCalls = append(*c.execCalls, query)
	c.mu.Unlock()
	if c.execErr != nil {
		return nil, c.execErr
	}
	return driver.RowsAffected(0), nil
}

// fakeDescUserRows plays back descRows as a DESC USER-shaped result: four
// columns (property, value, default, description), only the first two ever
// populated.
type fakeDescUserRows struct {
	rows []descUserRow
	idx  int
}

func (r *fakeDescUserRows) Columns() []string {
	return []string{"property", "value", "default", "description"}
}
func (r *fakeDescUserRows) Close() error { return nil }
func (r *fakeDescUserRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	row := r.rows[r.idx]
	r.idx++
	dest[0], dest[1], dest[2], dest[3] = row.property, row.value, "", ""
	return nil
}

type rotateFakeConnector struct{ conn rotateFakeConn }

func (c rotateFakeConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c rotateFakeConnector) Driver() driver.Driver                        { return rotateFakeDriverStub{} }

type rotateFakeDriverStub struct{}

func (rotateFakeDriverStub) Open(string) (driver.Conn, error) { return nil, driver.ErrSkip }

// newRotateFakeDB returns a *sql.DB whose DESC USER answers descRows and
// whose ALTER USER fails with execErr (nil for success), plus the query and
// exec calls it recorded.
func newRotateFakeDB(descRows []descUserRow, execErr error) (db *sql.DB, queryCalls, execCalls *[]string) {
	queryCalls, execCalls = &[]string{}, &[]string{}
	conn := rotateFakeConn{mu: &sync.Mutex{}, descRows: descRows, execErr: execErr, queryCalls: queryCalls, execCalls: execCalls}
	return sql.OpenDB(rotateFakeConnector{conn: conn}), queryCalls, execCalls
}

// newRotateFakeDBWithQueryErr returns a *sql.DB whose DESC USER (any query)
// fails outright with queryErr, exercising targetSlot's own QueryContext
// error path.
func newRotateFakeDBWithQueryErr(queryErr error) *sql.DB {
	conn := rotateFakeConn{mu: &sync.Mutex{}, queryErr: queryErr, queryCalls: &[]string{}, execCalls: &[]string{}}
	return sql.OpenDB(rotateFakeConnector{conn: conn})
}

// --- credentialDue ------------------------------------------------------------

func TestCredentialDue(t *testing.T) {
	if credentialDue(time.Now()) {
		t.Error("a freshly written credential must not be due")
	}
	if credentialDue(time.Now().AddDate(0, -5, 0)) {
		t.Error("a credential younger than six months must not be due")
	}
	if !credentialDue(time.Now().AddDate(0, -7, 0)) {
		t.Error("a credential older than six months must be due")
	}
}

// --- targetSlot ----------------------------------------------------------------

func TestTargetSlot(t *testing.T) {
	const fpA, fpB, fpC = "SHA256:AAA", "SHA256:BBB", "SHA256:CCC"

	cases := []struct {
		name    string
		rows    []descUserRow
		fp      string
		want    string
		wantErr bool
	}{
		{
			name: "current key matches slot 1, target is slot 2",
			rows: []descUserRow{{"RSA_PUBLIC_KEY_FP", fpA}, {"RSA_PUBLIC_KEY_2_FP", fpB}},
			fp:   fpA,
			want: rsaPublicKey2Slot,
		},
		{
			name: "current key matches slot 2, target is slot 1",
			rows: []descUserRow{{"RSA_PUBLIC_KEY_FP", fpA}, {"RSA_PUBLIC_KEY_2_FP", fpB}},
			fp:   fpB,
			want: rsaPublicKeySlot,
		},
		{
			name: "slot 2 never used (empty), still the correct target",
			rows: []descUserRow{{"RSA_PUBLIC_KEY_FP", fpA}, {"RSA_PUBLIC_KEY_2_FP", ""}},
			fp:   fpA,
			want: rsaPublicKey2Slot,
		},
		{
			name:    "current key matches neither slot is an error",
			rows:    []descUserRow{{"RSA_PUBLIC_KEY_FP", fpA}, {"RSA_PUBLIC_KEY_2_FP", fpB}},
			fp:      fpC,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, _, _ := newRotateFakeDB(tc.rows, nil)
			defer db.Close()

			got, err := targetSlot(context.Background(), db, "platform", tc.fp)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// --- rotateCredential ----------------------------------------------------------

func TestRotateCredential_WritesSecretOnlyAfterSuccessfulAlterUser(t *testing.T) {
	backend := secrets.NewFakeBackend()
	path, _ := secrets.NewTenantPath("my_org", "finance", "a")
	original := seedCredentials(t, backend, path)

	key, err := parsePrivateKey(original.PrivateKey)
	if err != nil {
		t.Fatalf("parsePrivateKey: %v", err)
	}
	fp := publicKeyFingerprint(key)

	db, _, execCalls := newRotateFakeDB([]descUserRow{{"RSA_PUBLIC_KEY_FP", fp}, {"RSA_PUBLIC_KEY_2_FP", ""}}, nil)
	defer db.Close()

	p := New(backend, testConfig())
	if err := p.rotateCredential(context.Background(), db, path, original.Username, key); err != nil {
		t.Fatalf("rotateCredential: %v", err)
	}
	if len(*execCalls) != 1 {
		t.Fatalf("expected exactly one ALTER USER, got %v", *execCalls)
	}

	raw, rotatedAt, err := backend.Get(context.Background(), path)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	rotated, err := secrets.UnmarshalCredentials(raw, rotatedAt)
	if err != nil {
		t.Fatalf("UnmarshalCredentials: %v", err)
	}
	if rotated.PrivateKey == original.PrivateKey {
		t.Error("expected the store to hold a freshly rotated key")
	}
}

func TestRotateCredential_FailedAlterUserLeavesStoreUntouched(t *testing.T) {
	backend := secrets.NewFakeBackend()
	path, _ := secrets.NewTenantPath("my_org", "finance", "a")
	original := seedCredentials(t, backend, path)

	key, err := parsePrivateKey(original.PrivateKey)
	if err != nil {
		t.Fatalf("parsePrivateKey: %v", err)
	}
	fp := publicKeyFingerprint(key)

	db, _, _ := newRotateFakeDB([]descUserRow{{"RSA_PUBLIC_KEY_FP", fp}, {"RSA_PUBLIC_KEY_2_FP", ""}}, stderrors.New("boom"))
	defer db.Close()

	p := New(backend, testConfig())
	if err := p.rotateCredential(context.Background(), db, path, original.Username, key); err == nil {
		t.Fatal("expected an error from a failing ALTER USER")
	}

	raw, rotatedAt, err := backend.Get(context.Background(), path)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	stored, err := secrets.UnmarshalCredentials(raw, rotatedAt)
	if err != nil {
		t.Fatalf("UnmarshalCredentials: %v", err)
	}
	if stored.PrivateKey != original.PrivateKey {
		t.Error("a failed ALTER USER must leave the stored credential untouched")
	}
}

// --- maybeRotateLocked, exercised through OrgAdmin/TenantAccount ---------------

func TestOrgAdmin_FreshCredential_NeverAttemptsRotation(t *testing.T) {
	backend := secrets.NewFakeBackend()
	cfg := testConfig()
	path, _ := secrets.NewOrgAdminPath(cfg.Snowflake.Org, cfg.Snowflake.OrgAdminAccount)
	seedCredentials(t, backend, path)

	rotDB, queryCalls, execCalls := newRotateFakeDB(nil, nil)
	p := New(backend, cfg)
	p.dial = func(dialConfig) (*sql.DB, error) { return rotDB, nil }

	if _, err := p.OrgAdmin(context.Background()); err != nil {
		t.Fatalf("OrgAdmin: %v", err)
	}
	if len(*queryCalls) != 0 || len(*execCalls) != 0 {
		t.Errorf("expected no DESC USER or ALTER USER for a fresh credential, got queries=%v execs=%v", *queryCalls, *execCalls)
	}

	// A second call — now a cache hit — must check again and still find
	// nothing due.
	if _, err := p.OrgAdmin(context.Background()); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if len(*queryCalls) != 0 || len(*execCalls) != 0 {
		t.Errorf("a cache hit must still be checked, and still find nothing due")
	}
}

func TestOrgAdmin_StaleCredential_RotatesInline(t *testing.T) {
	backend := secrets.NewFakeBackend()
	staleAt := time.Now().AddDate(0, -7, 0)
	backend.Clock = func() time.Time { return staleAt }

	cfg := testConfig()
	path, _ := secrets.NewOrgAdminPath(cfg.Snowflake.Org, cfg.Snowflake.OrgAdminAccount)
	original := seedCredentials(t, backend, path)
	backend.Clock = time.Now // the rotation write itself gets a fresh timestamp, as it would for real

	key, err := parsePrivateKey(original.PrivateKey)
	if err != nil {
		t.Fatalf("parsePrivateKey: %v", err)
	}
	fp := publicKeyFingerprint(key)

	rotDB, _, execCalls := newRotateFakeDB([]descUserRow{{"RSA_PUBLIC_KEY_FP", fp}, {"RSA_PUBLIC_KEY_2_FP", ""}}, nil)
	p := New(backend, cfg)
	p.dial = func(dialConfig) (*sql.DB, error) { return rotDB, nil }

	db, err := p.OrgAdmin(context.Background())
	if err != nil {
		t.Fatalf("OrgAdmin: %v", err)
	}
	if db != rotDB {
		t.Error("expected OrgAdmin to still return the dialed connection")
	}
	if len(*execCalls) != 1 {
		t.Fatalf("expected exactly one ALTER USER, got %v", *execCalls)
	}

	raw, rotatedAt, err := backend.Get(context.Background(), path)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	rotated, err := secrets.UnmarshalCredentials(raw, rotatedAt)
	if err != nil {
		t.Fatalf("UnmarshalCredentials: %v", err)
	}
	if rotated.PrivateKey == original.PrivateKey {
		t.Error("expected the org-admin credential to have been rotated")
	}
}

// A rotation failure (here: DESC USER reports neither slot matching the
// current key — a drift scenario) never fails the caller's connection
// request, and never reaches the store write.
func TestTenantAccount_RotationFailureDoesNotFailCall(t *testing.T) {
	backend := secrets.NewFakeBackend()
	staleAt := time.Now().AddDate(0, -7, 0)
	backend.Clock = func() time.Time { return staleAt }

	cfg := testConfig()
	path, _ := secrets.NewTenantPath(cfg.Snowflake.Org, "finance", "a")
	original := seedCredentials(t, backend, path)
	backend.Clock = time.Now

	rotDB, _, execCalls := newRotateFakeDB([]descUserRow{
		{"RSA_PUBLIC_KEY_FP", "SHA256:does-not-match-anything"},
		{"RSA_PUBLIC_KEY_2_FP", "SHA256:neither-does-this"},
	}, nil)
	p := New(backend, cfg)
	p.dial = func(dialConfig) (*sql.DB, error) { return rotDB, nil }

	db, err := p.TenantAccount(context.Background(), "finance", "a", "xc19114", "aws-eu-central-1")
	if err != nil {
		t.Fatalf("TenantAccount must not fail even though rotation itself fails: %v", err)
	}
	if db != rotDB {
		t.Error("expected the caller's connection regardless of rotation outcome")
	}
	if len(*execCalls) != 0 {
		t.Error("a failed slot lookup must never reach ALTER USER")
	}

	raw, rotatedAt, err := backend.Get(context.Background(), path)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	stored, err := secrets.UnmarshalCredentials(raw, rotatedAt)
	if err != nil {
		t.Fatalf("UnmarshalCredentials: %v", err)
	}
	if stored.PrivateKey != original.PrivateKey {
		t.Error("a failed rotation must leave the stored credential untouched")
	}
}

// A stored value that fails to unmarshal (or, below, to parse as a private
// key) must never reach DESC USER or ALTER USER — maybeRotateLocked swallows
// both the same way it swallows every other rotation failure.
func TestMaybeRotateLocked_UnmarshalFailure_NeverAttemptsRotation(t *testing.T) {
	backend := secrets.NewFakeBackend()
	staleAt := time.Now().AddDate(0, -7, 0)
	backend.Clock = func() time.Time { return staleAt }

	cfg := testConfig()
	path, _ := secrets.NewTenantPath(cfg.Snowflake.Org, "finance", "a")
	if err := backend.Create(context.Background(), path, "not valid json"); err != nil {
		t.Fatalf("seeding malformed credentials: %v", err)
	}

	db, queryCalls, execCalls := newRotateFakeDB(nil, nil)
	defer db.Close()

	p := New(backend, cfg)
	p.maybeRotateLocked(context.Background(), db, path)

	if len(*queryCalls) != 0 || len(*execCalls) != 0 {
		t.Errorf("a credential that fails to unmarshal must never reach DESC USER or ALTER USER, got queries=%v execs=%v", *queryCalls, *execCalls)
	}
}

func TestMaybeRotateLocked_ParsePrivateKeyFailure_NeverAttemptsRotation(t *testing.T) {
	backend := secrets.NewFakeBackend()
	staleAt := time.Now().AddDate(0, -7, 0)
	backend.Clock = func() time.Time { return staleAt }

	cfg := testConfig()
	path, _ := secrets.NewTenantPath(cfg.Snowflake.Org, "finance", "a")
	raw := `{"username":"platform","public_key":"AAAA","private_key":"not-a-pem"}`
	if err := backend.Create(context.Background(), path, raw); err != nil {
		t.Fatalf("seeding credentials with an unparseable key: %v", err)
	}

	db, queryCalls, execCalls := newRotateFakeDB(nil, nil)
	defer db.Close()

	p := New(backend, cfg)
	p.maybeRotateLocked(context.Background(), db, path)

	if len(*queryCalls) != 0 || len(*execCalls) != 0 {
		t.Errorf("a credential whose private key fails to parse must never reach DESC USER or ALTER USER, got queries=%v execs=%v", *queryCalls, *execCalls)
	}
}

// A failing store write still leaves the ALTER USER already applied —
// rotateCredential reports the error, but never retries or undoes the key
// push, matching the Key Concept's own framing (the slot push, not the
// store write, is the point of no return for Snowflake's own state).
func TestRotateCredential_FailedBackendUpdate_ReturnsErrorAfterAlterUserSucceeded(t *testing.T) {
	backend := secrets.NewFakeBackend()
	path, _ := secrets.NewTenantPath("my_org", "finance", "a")
	original := seedCredentials(t, backend, path)

	key, err := parsePrivateKey(original.PrivateKey)
	if err != nil {
		t.Fatalf("parsePrivateKey: %v", err)
	}
	fp := publicKeyFingerprint(key)

	db, _, execCalls := newRotateFakeDB([]descUserRow{{"RSA_PUBLIC_KEY_FP", fp}, {"RSA_PUBLIC_KEY_2_FP", ""}}, nil)
	defer db.Close()

	backend.OnUpdate = func(secrets.Path) error { return stderrors.New("store unavailable") }

	p := New(backend, testConfig())
	if err := p.rotateCredential(context.Background(), db, path, original.Username, key); err == nil {
		t.Fatal("expected an error from a failing store write")
	}
	if len(*execCalls) != 1 {
		t.Fatalf("expected the ALTER USER to have already run before the failing store write, got %v", *execCalls)
	}
}

func TestTargetSlot_QueryContextFailure(t *testing.T) {
	db := newRotateFakeDBWithQueryErr(stderrors.New("connection reset"))
	defer db.Close()

	if _, err := targetSlot(context.Background(), db, "platform", "SHA256:whatever"); err == nil {
		t.Fatal("expected an error when DESC USER itself fails")
	}
}
