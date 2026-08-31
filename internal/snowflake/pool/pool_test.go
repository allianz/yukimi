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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/allianz/yukimi/internal/config"
	"github.com/allianz/yukimi/internal/errors"
	"github.com/allianz/yukimi/internal/secrets"
	"github.com/allianz/yukimi/internal/snowflake/host"
)

// --- test fakes -------------------------------------------------------------

// fakeConn is a driver.Conn whose Close is observable and whose error is
// injectable, so tests can assert Pool.Close() actually closes every cached
// *sql.DB and continues past an individual failure.
type fakeConn struct {
	closed   *atomic.Bool
	closeErr error
}

func (c fakeConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c fakeConn) Close() error {
	if c.closed != nil {
		c.closed.Store(true)
	}
	return c.closeErr
}
func (c fakeConn) Begin() (driver.Tx, error) { return nil, driver.ErrSkip }

type fakeConnector struct {
	closed   *atomic.Bool
	closeErr error
}

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) {
	return fakeConn{closed: c.closed, closeErr: c.closeErr}, nil
}
func (c fakeConnector) Driver() driver.Driver { return fakeDriverStub{} }

type fakeDriverStub struct{}

func (fakeDriverStub) Open(string) (driver.Conn, error) { return nil, driver.ErrSkip }

// newFakeDB returns a *sql.DB backed by a fake driver.Conn, forced open via
// Ping so exactly one idle connection is pooled — otherwise db.Close() would
// have nothing to call Close on, since sql.OpenDB never connects eagerly.
func newFakeDB(t *testing.T, closeErr error) (*sql.DB, *atomic.Bool) {
	t.Helper()
	closed := &atomic.Bool{}
	db := sql.OpenDB(fakeConnector{closed: closed, closeErr: closeErr})
	if err := db.Ping(); err != nil {
		t.Fatalf("fake db ping: %v", err)
	}
	return db, closed
}

// fakeDialer is a swappable Pool.dial with call counting, per-call config
// capture, and optional per-call failure injection — reused across this
// file's scenarios.
type fakeDialer struct {
	mu      sync.Mutex
	configs []dialConfig
	errFn   func(callIndex int) error
	delay   time.Duration
}

func (f *fakeDialer) dial(dc dialConfig) (*sql.DB, error) {
	f.mu.Lock()
	idx := len(f.configs)
	f.configs = append(f.configs, dc)
	f.mu.Unlock()

	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.errFn != nil {
		if err := f.errFn(idx); err != nil {
			return nil, err
		}
	}
	db, _ := newFakeDBForDialer()
	return db, nil
}

// newFakeDBForDialer is the fakeDialer's own db factory — it doesn't need a
// *testing.T, unlike newFakeDB, since fakeDialer.dial runs off the test
// goroutine in concurrency tests.
func newFakeDBForDialer() (*sql.DB, *atomic.Bool) {
	closed := &atomic.Bool{}
	db := sql.OpenDB(fakeConnector{closed: closed})
	_ = db.Ping()
	return db, closed
}

func (f *fakeDialer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.configs)
}

func (f *fakeDialer) lastConfig() dialConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.configs[len(f.configs)-1]
}

func testConfig() *config.BaseConfig {
	return &config.BaseConfig{
		Snowflake: config.SnowflakeSettings{
			Org:                    "my_org",
			OrgAdminAccount:        "platform",
			OrgAdminAccountLocator: "xc00000",
			OrgAdminAccountRegion:  "aws-eu-central-1",
			UsePrivateLink:         false,
			MaxConnectionPoolSize:  7,
			MaxIdleConnections:     3,
			ConnectionMaxLifetime:  30 * time.Minute,
			ConnectionMaxIdleTime:  5 * time.Minute,
			ConnectionProbeTimeout: 2 * time.Second,
		},
		Secrets: config.SecretsSettings{
			RotationInterval: 6 * 30 * 24 * time.Hour,
		},
	}
}

// seedCredentials generates a fresh keypair, stores it at path, and returns
// the Credentials for assertions.
func seedCredentials(t *testing.T, backend secrets.Backend, path secrets.Path) *secrets.Credentials {
	t.Helper()
	creds, err := secrets.NewCredentials("platform")
	if err != nil {
		t.Fatalf("NewCredentials: %v", err)
	}
	raw, err := secrets.MarshalCredentials(creds)
	if err != nil {
		t.Fatalf("MarshalCredentials: %v", err)
	}
	if err := backend.Create(context.Background(), path, raw); err != nil {
		t.Fatalf("seeding credentials: %v", err)
	}
	return creds
}

// --- SC-001 ------------------------------------------------------------------

func TestNew_NoNetworkCall(t *testing.T) {
	getCalled := false
	backend := secrets.NewFakeBackend()
	backend.OnGet = func(secrets.Path) error { getCalled = true; return nil }
	dialer := &fakeDialer{}

	p := New(backend, testConfig())
	p.dial = dialer.dial

	if p == nil {
		t.Fatal("New returned nil")
	}
	if getCalled {
		t.Error("New must not read any credential")
	}
	if dialer.callCount() != 0 {
		t.Error("New must not dial")
	}
}

// --- OrgAdmin ----------------------------------------------------------------

// SC-002, SC-015: OrgAdmin's first call reads the org-admin credential via
// NewOrgAdminPath, builds the host, and dials with the right Config fields.
func TestOrgAdmin_FirstCall(t *testing.T) {
	backend := secrets.NewFakeBackend()
	cfg := testConfig()
	path, err := secrets.NewOrgAdminPath(cfg.Snowflake.Org, cfg.Snowflake.OrgAdminAccount)
	if err != nil {
		t.Fatalf("NewOrgAdminPath: %v", err)
	}
	creds := seedCredentials(t, backend, path)

	dialer := &fakeDialer{}
	p := New(backend, cfg)
	p.dial = dialer.dial

	db, err := p.OrgAdmin(context.Background())
	if err != nil {
		t.Fatalf("OrgAdmin: %v", err)
	}
	if db == nil {
		t.Fatal("expected a non-nil *sql.DB")
	}
	if dialer.callCount() != 1 {
		t.Fatalf("dial count = %d, want 1", dialer.callCount())
	}

	dc := dialer.lastConfig()
	wantHost, _ := host.Hostname(cfg.Snowflake.OrgAdminAccountLocator, cfg.Snowflake.OrgAdminAccountRegion, cfg.Snowflake.UsePrivateLink)
	if dc.snowflake.Host != wantHost {
		t.Errorf("Host = %q, want %q", dc.snowflake.Host, wantHost)
	}
	if dc.snowflake.Account != cfg.Snowflake.OrgAdminAccountLocator {
		t.Errorf("Account = %q, want %q", dc.snowflake.Account, cfg.Snowflake.OrgAdminAccountLocator)
	}
	if dc.snowflake.User != creds.Username {
		t.Errorf("User = %q, want %q", dc.snowflake.User, creds.Username)
	}
	if dc.snowflake.PrivateKey == nil {
		t.Error("PrivateKey must be set")
	}
	if dc.snowflake.Role != "ORGADMIN" {
		t.Errorf("Role = %q, want ORGADMIN", dc.snowflake.Role)
	}
	if dc.probeTimeout != cfg.Snowflake.ConnectionProbeTimeout {
		t.Errorf("probeTimeout = %v, want %v", dc.probeTimeout, cfg.Snowflake.ConnectionProbeTimeout)
	}
}

// SC-003: every later OrgAdmin call returns the identical *sql.DB pointer.
func TestOrgAdmin_CachesPointer(t *testing.T) {
	backend := secrets.NewFakeBackend()
	cfg := testConfig()
	path, _ := secrets.NewOrgAdminPath(cfg.Snowflake.Org, cfg.Snowflake.OrgAdminAccount)
	seedCredentials(t, backend, path)

	dialer := &fakeDialer{}
	p := New(backend, cfg)
	p.dial = dialer.dial

	ctx := context.Background()
	first, err := p.OrgAdmin(ctx)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, err := p.OrgAdmin(ctx)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if first != second {
		t.Error("expected the identical *sql.DB pointer on a cache hit")
	}
	if dialer.callCount() != 1 {
		t.Errorf("dial count = %d, want 1", dialer.callCount())
	}
}

// A malformed org-admin region returns a user error before any credential
// read or dial is attempted, mirroring TenantAccount's SC-008 behavior.
func TestOrgAdmin_MalformedRegion_NoConnectionAttempt(t *testing.T) {
	backend := secrets.NewFakeBackend()
	getCalled := false
	backend.OnGet = func(secrets.Path) error { getCalled = true; return nil }

	cfg := testConfig()
	cfg.Snowflake.OrgAdminAccountRegion = "eu-central-1" // missing cloud prefix

	dialer := &fakeDialer{}
	p := New(backend, cfg)
	p.dial = dialer.dial

	_, err := p.OrgAdmin(context.Background())
	if err == nil || !errors.IsUserError(err) {
		t.Fatalf("expected a user error, got %v", err)
	}
	if getCalled {
		t.Error("a malformed region must never reach a credential read")
	}
	if dialer.callCount() != 0 {
		t.Error("a malformed region must never reach a dial")
	}
}

// A malformed Org config value surfaces NewOrgAdminPath's own validation
// error before any credential read.
func TestOrgAdmin_InvalidPathSegment(t *testing.T) {
	backend := secrets.NewFakeBackend()
	cfg := testConfig()
	cfg.Snowflake.Org = "my/org"

	p := New(backend, cfg)
	if _, err := p.OrgAdmin(context.Background()); err == nil || !errors.IsUserError(err) {
		t.Fatalf("expected a user error, got %v", err)
	}
}

// A credential read failure is not cached; the next call retries in full.
func TestOrgAdmin_CredentialReadFailureNotCached(t *testing.T) {
	backend := secrets.NewFakeBackend()
	cfg := testConfig()
	path, _ := secrets.NewOrgAdminPath(cfg.Snowflake.Org, cfg.Snowflake.OrgAdminAccount)
	seedCredentials(t, backend, path)

	attempts := 0
	backend.OnGet = func(secrets.Path) error {
		attempts++
		if attempts == 1 {
			return stderrors.New("boom")
		}
		return nil
	}

	dialer := &fakeDialer{}
	p := New(backend, cfg)
	p.dial = dialer.dial

	if _, err := p.OrgAdmin(context.Background()); err == nil {
		t.Fatal("expected an error on the first, failing credential read")
	}
	if _, err := p.OrgAdmin(context.Background()); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if dialer.callCount() != 1 {
		t.Errorf("dial count = %d, want 1", dialer.callCount())
	}
}

// A stored credential that fails to unmarshal (not valid JSON) is a system
// error, not cached.
func TestOrgAdmin_UnmarshalFailure(t *testing.T) {
	backend := secrets.NewFakeBackend()
	cfg := testConfig()
	path, _ := secrets.NewOrgAdminPath(cfg.Snowflake.Org, cfg.Snowflake.OrgAdminAccount)
	if err := backend.Create(context.Background(), path, "not valid json"); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	p := New(backend, cfg)
	if _, err := p.OrgAdmin(context.Background()); err == nil {
		t.Fatal("expected an error for a credential that does not unmarshal")
	}
}

// A stored credential whose private key does not parse is a system error.
func TestOrgAdmin_ParsePrivateKeyFailure(t *testing.T) {
	backend := secrets.NewFakeBackend()
	cfg := testConfig()
	path, _ := secrets.NewOrgAdminPath(cfg.Snowflake.Org, cfg.Snowflake.OrgAdminAccount)
	creds := &secrets.Credentials{Username: "platform", PublicKey: "irrelevant", PrivateKey: "not a pem key"}
	raw, err := secrets.MarshalCredentials(creds)
	if err != nil {
		t.Fatalf("MarshalCredentials: %v", err)
	}
	if err := backend.Create(context.Background(), path, raw); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	p := New(backend, cfg)
	if _, err := p.OrgAdmin(context.Background()); err == nil {
		t.Fatal("expected an error for a private key that does not parse")
	}
}

// SC-009: a failed dial on OrgAdmin's first call leaves nothing cached; the
// next call retries the credential read and dial from scratch.
func TestOrgAdmin_FailedDialNotCached(t *testing.T) {
	backend := secrets.NewFakeBackend()
	cfg := testConfig()
	path, _ := secrets.NewOrgAdminPath(cfg.Snowflake.Org, cfg.Snowflake.OrgAdminAccount)
	seedCredentials(t, backend, path)

	boom := stderrors.New("boom")
	dialer := &fakeDialer{errFn: func(idx int) error {
		if idx == 0 {
			return boom
		}
		return nil
	}}
	p := New(backend, cfg)
	p.dial = dialer.dial

	ctx := context.Background()
	if _, err := p.OrgAdmin(ctx); err == nil {
		t.Fatal("expected an error on the first, failing dial")
	}
	if p.orgAdminDB != nil {
		t.Error("a failed dial must not be cached")
	}

	db, err := p.OrgAdmin(ctx)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if db == nil {
		t.Fatal("expected a non-nil *sql.DB on retry")
	}
	if dialer.callCount() != 2 {
		t.Errorf("dial count = %d, want 2 (one failed, one retried)", dialer.callCount())
	}
}

// --- TenantAccount -----------------------------------------------------------

// SC-004: TenantAccount builds its secret path via NewTenantPath.
func TestTenantAccount_BuildsPath(t *testing.T) {
	backend := secrets.NewFakeBackend()
	cfg := testConfig()

	var gotPath secrets.Path
	backend.OnGet = func(p secrets.Path) error { gotPath = p; return nil }
	path, _ := secrets.NewTenantPath(cfg.Snowflake.Org, "finance", "analytics-team-eu")
	seedCredentials(t, backend, path)

	dialer := &fakeDialer{}
	p := New(backend, cfg)
	p.dial = dialer.dial

	if _, err := p.TenantAccount(context.Background(), "finance", "analytics-team-eu", "xc19114", "aws-eu-central-1"); err != nil {
		t.Fatalf("TenantAccount: %v", err)
	}
	if gotPath != path {
		t.Errorf("Get called with path %q, want %q", gotPath, path)
	}
}

// SC-005, SC-016: identical calls cache-hit the same pointer; a different
// namespace or accountName dials a distinct one, with Role=ACCOUNTADMIN and
// Account/Host built from the caller-supplied locator/region.
func TestTenantAccount_CachesByFullKey(t *testing.T) {
	backend := secrets.NewFakeBackend()
	cfg := testConfig()
	for _, name := range []string{"a", "b"} {
		path, _ := secrets.NewTenantPath(cfg.Snowflake.Org, "finance", name)
		seedCredentials(t, backend, path)
	}

	dialer := &fakeDialer{}
	p := New(backend, cfg)
	p.dial = dialer.dial

	ctx := context.Background()
	first, err := p.TenantAccount(ctx, "finance", "a", "xc19114", "aws-eu-central-1")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, err := p.TenantAccount(ctx, "finance", "a", "xc19114", "aws-eu-central-1")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if first != second {
		t.Error("expected the identical *sql.DB pointer for an identical key")
	}
	if dialer.callCount() != 1 {
		t.Fatalf("dial count = %d, want 1", dialer.callCount())
	}

	third, err := p.TenantAccount(ctx, "finance", "b", "xc00001", "aws-eu-west-3")
	if err != nil {
		t.Fatalf("third call: %v", err)
	}
	if third == first {
		t.Error("a different accountName must not share a cached connection")
	}
	if dialer.callCount() != 2 {
		t.Fatalf("dial count = %d, want 2", dialer.callCount())
	}

	dc := dialer.lastConfig()
	if dc.snowflake.Role != "ACCOUNTADMIN" {
		t.Errorf("Role = %q, want ACCOUNTADMIN", dc.snowflake.Role)
	}
	if dc.snowflake.Account != "xc00001" {
		t.Errorf("Account = %q, want xc00001 (the caller-supplied locator)", dc.snowflake.Account)
	}
	wantHost, _ := host.Hostname("xc00001", "aws-eu-west-3", cfg.Snowflake.UsePrivateLink)
	if dc.snowflake.Host != wantHost {
		t.Errorf("Host = %q, want %q", dc.snowflake.Host, wantHost)
	}
}

// SC-008: a malformed region returns a user error before any credential read
// or dial.
func TestTenantAccount_MalformedRegion_NoConnectionAttempt(t *testing.T) {
	backend := secrets.NewFakeBackend()
	getCalled := false
	backend.OnGet = func(secrets.Path) error { getCalled = true; return nil }

	dialer := &fakeDialer{}
	p := New(backend, testConfig())
	p.dial = dialer.dial

	_, err := p.TenantAccount(context.Background(), "finance", "a", "xc19114", "eu-central-1")
	if err == nil || !errors.IsUserError(err) {
		t.Fatalf("expected a user error, got %v", err)
	}
	if getCalled {
		t.Error("a malformed region must never reach a credential read")
	}
	if dialer.callCount() != 0 {
		t.Error("a malformed region must never reach a dial")
	}
}

// A malformed namespace/accountName surfaces NewTenantPath's own validation
// error before any credential read.
func TestTenantAccount_InvalidPathSegment(t *testing.T) {
	backend := secrets.NewFakeBackend()
	p := New(backend, testConfig())
	if _, err := p.TenantAccount(context.Background(), "finance/eu", "a", "xc19114", "aws-eu-central-1"); err == nil || !errors.IsUserError(err) {
		t.Fatalf("expected a user error, got %v", err)
	}
}

// A credential read failure is not cached; the next call retries in full.
func TestTenantAccount_CredentialReadFailureNotCached(t *testing.T) {
	backend := secrets.NewFakeBackend()
	cfg := testConfig()
	path, _ := secrets.NewTenantPath(cfg.Snowflake.Org, "finance", "a")
	seedCredentials(t, backend, path)

	attempts := 0
	backend.OnGet = func(secrets.Path) error {
		attempts++
		if attempts == 1 {
			return stderrors.New("boom")
		}
		return nil
	}

	dialer := &fakeDialer{}
	p := New(backend, cfg)
	p.dial = dialer.dial

	ctx := context.Background()
	if _, err := p.TenantAccount(ctx, "finance", "a", "xc19114", "aws-eu-central-1"); err == nil {
		t.Fatal("expected an error on the first, failing credential read")
	}
	if _, err := p.TenantAccount(ctx, "finance", "a", "xc19114", "aws-eu-central-1"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if dialer.callCount() != 1 {
		t.Errorf("dial count = %d, want 1", dialer.callCount())
	}
}

// A stored credential that fails to unmarshal (not valid JSON) is a system
// error, not cached.
func TestTenantAccount_UnmarshalFailure(t *testing.T) {
	backend := secrets.NewFakeBackend()
	cfg := testConfig()
	path, _ := secrets.NewTenantPath(cfg.Snowflake.Org, "finance", "a")
	if err := backend.Create(context.Background(), path, "not valid json"); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	p := New(backend, cfg)
	if _, err := p.TenantAccount(context.Background(), "finance", "a", "xc19114", "aws-eu-central-1"); err == nil {
		t.Fatal("expected an error for a credential that does not unmarshal")
	}
}

// A stored credential whose private key does not parse is a system error.
func TestTenantAccount_ParsePrivateKeyFailure(t *testing.T) {
	backend := secrets.NewFakeBackend()
	cfg := testConfig()
	path, _ := secrets.NewTenantPath(cfg.Snowflake.Org, "finance", "a")
	creds := &secrets.Credentials{Username: "platform", PublicKey: "irrelevant", PrivateKey: "not a pem key"}
	raw, err := secrets.MarshalCredentials(creds)
	if err != nil {
		t.Fatalf("MarshalCredentials: %v", err)
	}
	if err := backend.Create(context.Background(), path, raw); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	p := New(backend, cfg)
	if _, err := p.TenantAccount(context.Background(), "finance", "a", "xc19114", "aws-eu-central-1"); err == nil {
		t.Fatal("expected an error for a private key that does not parse")
	}
}

// SC-009: a failed dial on the first call for a key leaves nothing cached;
// the next call retries the credential read and dial from scratch.
func TestTenantAccount_FailedDialNotCached(t *testing.T) {
	backend := secrets.NewFakeBackend()
	cfg := testConfig()
	path, _ := secrets.NewTenantPath(cfg.Snowflake.Org, "finance", "a")
	seedCredentials(t, backend, path)

	dialer := &fakeDialer{errFn: func(idx int) error {
		if idx == 0 {
			return stderrors.New("boom")
		}
		return nil
	}}
	p := New(backend, cfg)
	p.dial = dialer.dial

	ctx := context.Background()
	if _, err := p.TenantAccount(ctx, "finance", "a", "xc19114", "aws-eu-central-1"); err == nil {
		t.Fatal("expected an error on the first, failing dial")
	}
	if _, ok := p.cachedTenant(tenantKey{"finance", "a"}, "xc19114", "aws-eu-central-1"); ok {
		t.Error("a failed dial must not be cached")
	}

	if _, err := p.TenantAccount(ctx, "finance", "a", "xc19114", "aws-eu-central-1"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if dialer.callCount() != 2 {
		t.Errorf("dial count = %d, want 2", dialer.callCount())
	}
}

// SC-010: concurrent callers for the same key on a cold cache result in
// exactly one dial and one cached *sql.DB, observed by all callers.
func TestTenantAccount_ConcurrentSameKey_OneDial(t *testing.T) {
	backend := secrets.NewFakeBackend()
	cfg := testConfig()
	path, _ := secrets.NewTenantPath(cfg.Snowflake.Org, "finance", "a")
	seedCredentials(t, backend, path)

	dialer := &fakeDialer{delay: 20 * time.Millisecond}
	p := New(backend, cfg)
	p.dial = dialer.dial

	const n = 20
	results := make([]*sql.DB, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = p.TenantAccount(context.Background(), "finance", "a", "xc19114", "aws-eu-central-1")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	for i := 1; i < n; i++ {
		if results[i] != results[0] {
			t.Errorf("goroutine %d got a different *sql.DB than goroutine 0", i)
		}
	}
	if dialer.callCount() != 1 {
		t.Errorf("dial count = %d, want 1", dialer.callCount())
	}
}

// SC-010a: a cold dial for one key never waits on a cold dial for a
// different key — proven by blocking key A's dial and asserting key B's
// completes anyway.
func TestTenantAccount_ConcurrentDifferentKeys_DoNotSerialize(t *testing.T) {
	backend := secrets.NewFakeBackend()
	cfg := testConfig()
	for _, name := range []string{"a", "b"} {
		path, _ := secrets.NewTenantPath(cfg.Snowflake.Org, "finance", name)
		seedCredentials(t, backend, path)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	p := New(backend, cfg)
	p.dial = func(dc dialConfig) (*sql.DB, error) {
		if dc.snowflake.Account == "locatorA" {
			close(started)
			<-release
		}
		db, _ := newFakeDBForDialer()
		return db, nil
	}

	var wgA sync.WaitGroup
	wgA.Add(1)
	go func() {
		defer wgA.Done()
		_, _ = p.TenantAccount(context.Background(), "finance", "a", "locatorA", "aws-eu-central-1")
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("key A's dial never started")
	}

	done := make(chan struct{})
	go func() {
		_, _ = p.TenantAccount(context.Background(), "finance", "b", "locatorB", "aws-eu-central-1")
		close(done)
	}()

	select {
	case <-done:
		// key B completed while key A's dial is still blocked — no
		// pool-wide serialization.
	case <-time.After(2 * time.Second):
		t.Fatal("key B's dial serialized behind key A's in-flight dial")
	}

	close(release)
	wgA.Wait()
}

// SC-011: a call whose locator or region differs from what is cached closes
// the stale *sql.DB and returns a freshly dialed one.
func TestTenantAccount_SelfHealsOnLocatorChange(t *testing.T) {
	backend := secrets.NewFakeBackend()
	cfg := testConfig()
	path, _ := secrets.NewTenantPath(cfg.Snowflake.Org, "finance", "a")
	seedCredentials(t, backend, path)

	dialer := &fakeDialer{}
	p := New(backend, cfg)
	p.dial = dialer.dial

	ctx := context.Background()
	first, err := p.TenantAccount(ctx, "finance", "a", "xc19114", "aws-eu-central-1")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	second, err := p.TenantAccount(ctx, "finance", "a", "xc99999", "aws-eu-central-1")
	if err != nil {
		t.Fatalf("second call (new locator): %v", err)
	}
	if second == first {
		t.Error("a locator change must return a freshly dialed *sql.DB")
	}
	if dialer.callCount() != 2 {
		t.Errorf("dial count = %d, want 2", dialer.callCount())
	}
	if err := first.Ping(); err == nil {
		t.Error("the stale connection should have been closed on self-heal")
	}
}

func TestTenantAccount_SelfHealsOnRegionChange(t *testing.T) {
	backend := secrets.NewFakeBackend()
	cfg := testConfig()
	path, _ := secrets.NewTenantPath(cfg.Snowflake.Org, "finance", "a")
	seedCredentials(t, backend, path)

	dialer := &fakeDialer{}
	p := New(backend, cfg)
	p.dial = dialer.dial

	ctx := context.Background()
	first, err := p.TenantAccount(ctx, "finance", "a", "xc19114", "aws-eu-central-1")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, err := p.TenantAccount(ctx, "finance", "a", "xc19114", "aws-eu-west-3")
	if err != nil {
		t.Fatalf("second call (new region): %v", err)
	}
	if second == first {
		t.Error("a region change must return a freshly dialed *sql.DB")
	}
	if dialer.callCount() != 2 {
		t.Errorf("dial count = %d, want 2", dialer.callCount())
	}
}

// --- EvictTenant --------------------------------------------------------------

// SC-012: EvictTenant closes and removes the cached entry; a following call
// with the same key dials again.
func TestEvictTenant_ClosesAndDialsAgain(t *testing.T) {
	backend := secrets.NewFakeBackend()
	cfg := testConfig()
	path, _ := secrets.NewTenantPath(cfg.Snowflake.Org, "finance", "a")
	seedCredentials(t, backend, path)

	dialer := &fakeDialer{}
	p := New(backend, cfg)
	p.dial = dialer.dial

	ctx := context.Background()
	first, err := p.TenantAccount(ctx, "finance", "a", "xc19114", "aws-eu-central-1")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	p.EvictTenant("finance", "a")

	if _, ok := p.cachedTenant(tenantKey{"finance", "a"}, "xc19114", "aws-eu-central-1"); ok {
		t.Error("expected the entry to be removed after eviction")
	}
	if err := first.Ping(); err == nil {
		t.Error("expected the evicted connection to be closed")
	}

	second, err := p.TenantAccount(ctx, "finance", "a", "xc19114", "aws-eu-central-1")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if second == first {
		t.Error("expected a freshly dialed *sql.DB after eviction")
	}
	if dialer.callCount() != 2 {
		t.Errorf("dial count = %d, want 2", dialer.callCount())
	}
}

// SC-013: evicting a key that was never dialed is a no-op, not an error.
func TestEvictTenant_NeverDialed_NoPanic(t *testing.T) {
	p := New(secrets.NewFakeBackend(), testConfig())
	p.EvictTenant("finance", "never-dialed")
}

// --- Close ---------------------------------------------------------------

// SC-014: Close closes every cached *sql.DB — org-admin and every tenant
// entry — and joins any individual failure without skipping the rest.
func TestClose_ClosesEverythingAndJoinsErrors(t *testing.T) {
	p := New(secrets.NewFakeBackend(), testConfig())

	orgBoom := stderrors.New("org boom")
	orgDB, orgClosed := newFakeDB(t, orgBoom)
	p.orgAdminDB = orgDB

	boom := stderrors.New("boom")
	t1DB, t1Closed := newFakeDB(t, boom)
	t2DB, t2Closed := newFakeDB(t, nil)
	p.entries[tenantKey{"finance", "a"}] = tenantEntry{db: t1DB, locator: "L1", region: "aws-eu-central-1"}
	p.entries[tenantKey{"finance", "b"}] = tenantEntry{db: t2DB, locator: "L2", region: "aws-eu-central-1"}

	err := p.Close()
	if err == nil {
		t.Fatal("expected a non-nil joined error")
	}
	if !stderrors.Is(err, boom) {
		t.Errorf("joined error does not wrap the injected tenant close failure: %v", err)
	}
	if !stderrors.Is(err, orgBoom) {
		t.Errorf("joined error does not wrap the injected org-admin close failure: %v", err)
	}
	if !orgClosed.Load() {
		t.Error("org-admin connection was not closed")
	}
	if !t1Closed.Load() {
		t.Error("tenant a's connection was not closed")
	}
	if !t2Closed.Load() {
		t.Error("tenant b's connection was not closed despite tenant a's close failure")
	}
	if len(p.entries) != 0 {
		t.Error("expected the entries map to be cleared")
	}
	if p.orgAdminDB != nil {
		t.Error("expected orgAdminDB to be cleared")
	}
}

// --- applyPoolSettings -------------------------------------------------------

// SC-021: every *sql.DB this package dials has the four pool-tuning values
// from BaseConfig.Snowflake applied.
func TestApplyPoolSettings_UsesConfig(t *testing.T) {
	cfg := testConfig()
	db, _ := newFakeDB(t, nil)
	defer db.Close()

	applyPoolSettings(db, cfg)

	stats := db.Stats()
	if stats.MaxOpenConnections != cfg.Snowflake.MaxConnectionPoolSize {
		t.Errorf("MaxOpenConnections = %d, want %d", stats.MaxOpenConnections, cfg.Snowflake.MaxConnectionPoolSize)
	}
}
