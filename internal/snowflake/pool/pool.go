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
	stderrors "errors"
	"fmt"
	"sync"

	"github.com/allianz/yukimi/internal/config/base"
	"github.com/allianz/yukimi/internal/secrets"
	"github.com/allianz/yukimi/internal/snowflake/host"
)

// tenantKey identifies a tenant connection target — the same (namespace,
// accountName) tuple the tenant secret path (003) is built from.
type tenantKey struct {
	namespace   string
	accountName string
}

// tenantEntry is a cached tenant connection plus the locator/region it was
// built with. Stored by value: self-healing always constructs a brand-new
// entry and replaces the map slot wholesale rather than mutating one in
// place, so a reader that already copied out an entry can never observe a
// partial update.
type tenantEntry struct {
	db      *sql.DB
	locator string
	region  string
}

// Pool caches one *sql.DB per connection target and hands back the same one
// on every subsequent call. Every cached *sql.DB stays open until Close or an
// explicit eviction — never after ordinary use.
type Pool struct {
	backend secrets.Backend
	cfg     *base.BaseConfig
	dial    dialFunc

	orgAdminMu sync.Mutex
	orgAdminDB *sql.DB

	entriesMu sync.RWMutex
	entries   map[tenantKey]tenantEntry

	locksMu  sync.Mutex
	keyLocks map[tenantKey]*sync.Mutex
}

// New constructs a Pool. It makes no connection attempt itself: every
// *sql.DB is opened lazily, on its first OrgAdmin or TenantAccount call.
//
// Parameters:
//   - backend: the secrets.Backend (003) credentials are read through; never
//     a concrete backend package
//   - cfg: BaseConfig (002) — Snowflake.Org, OrgAdminAccount,
//     OrgAdminAccountLocator, OrgAdminAccountRegion, UsePrivateLink,
//     DisableOCSPChecks, MaxConnectionPoolSize, MaxIdleConnections,
//     ConnectionMaxLifetime, ConnectionMaxIdleTime, ConnectionProbeTimeout,
//     Secrets.RotationInterval
//
// Returns:
//   - *Pool: never nil
func New(backend secrets.Backend, cfg *base.BaseConfig) *Pool {
	return &Pool{
		backend:  backend,
		cfg:      cfg,
		dial:     defaultDial,
		entries:  make(map[tenantKey]tenantEntry),
		keyLocks: make(map[tenantKey]*sync.Mutex),
	}
}

// keyLock returns key's own *sync.Mutex, creating it under a brief pool-wide
// lock if this is the first time key has been seen. The mutex itself is never
// held here — only locksMu, and only long enough to get-or-create the entry —
// so a cold dial for one key never blocks a cold dial for a different key.
func (p *Pool) keyLock(key tenantKey) *sync.Mutex {
	p.locksMu.Lock()
	defer p.locksMu.Unlock()
	l, ok := p.keyLocks[key]
	if !ok {
		l = &sync.Mutex{}
		p.keyLocks[key] = l
	}
	return l
}

// OrgAdmin returns the single org-admin *sql.DB, used only for CREATE ACCOUNT
// and DROP ACCOUNT (design.md 3.6, 6.3, 3.11 intro). The credential is read
// from the org-admin secret path (003) and the connection is authenticated
// with the GLOBALORGADMIN role. Opened on first call; every later call returns the
// same *sql.DB. Also rotates the credential inline once it is older than
// cfg.Secrets.RotationInterval (see specs/004-connection-pooling.md,
// Key Concept: Inline Rotation); a rotation failure never fails this call.
//
// Returns:
//   - System error if the org-admin credential cannot be read, does not
//     parse as a valid private key, or the connection cannot be established
func (p *Pool) OrgAdmin(ctx context.Context) (*sql.DB, error) {
	p.orgAdminMu.Lock()
	defer p.orgAdminMu.Unlock()

	sf := &p.cfg.Snowflake

	path, err := secrets.NewOrgAdminPath(sf.Org, sf.OrgAdminAccount)
	if err != nil {
		return nil, err
	}

	if p.orgAdminDB != nil {
		p.maybeRotateLocked(ctx, p.orgAdminDB, path)
		return p.orgAdminDB, nil
	}

	hostname, err := host.Hostname(sf.OrgAdminAccountLocator, sf.OrgAdminAccountRegion, sf.UsePrivateLink)
	if err != nil {
		return nil, err
	}

	raw, rotatedAt, err := p.backend.Get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("failed to read org-admin credentials: %w", err)
	}
	creds, err := secrets.UnmarshalCredentials(raw, rotatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to read org-admin credentials: %w", err)
	}
	key, err := parsePrivateKey(creds.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key for org-admin: %w", err)
	}

	sfCfg := buildSnowflakeConfig(sf.OrgAdminAccountLocator, hostname, creds.Username, key, "GLOBALORGADMIN", sf.DisableOCSPChecks)
	db, err := p.dial(dialConfig{snowflake: sfCfg, probeTimeout: sf.ConnectionProbeTimeout})
	if err != nil {
		return nil, fmt.Errorf("credentials for org-admin %s were read successfully, but failed to connect to %s: %w (tip: run `DESC USER %s` in Snowflake and compare RSA_PUBLIC_KEY_FP against %s; DisableOCSPChecks=%t)",
			creds.Username, hostname, err, creds.Username, publicKeyFingerprint(key), sf.DisableOCSPChecks)
	}
	applyPoolSettings(db, p.cfg)

	p.orgAdminDB = db
	p.maybeRotateLocked(ctx, db, path)
	return db, nil
}

// TenantAccount returns the per-tenant *sql.DB, authenticated as that
// account's platform service user with the ACCOUNTADMIN role (design.md
// 3.6, 3.11, Appendix B X1). Keyed by (org, namespace, accountName) — the
// same tuple as the tenant secret path (003) — plus the account's current
// locator and region: a mismatch against a cached entry's locator or region
// closes it and dials again (see Key Concept: Self-Healing). Also rotates
// the credential inline once it is older than cfg.Secrets.RotationInterval
// (see specs/004-connection-pooling.md, Key Concept: Inline Rotation); a
// rotation failure never fails this call.
//
// Parameters:
//   - namespace: metadata.namespace at the call site, never a spec field
//     (design.md 3.11.1)
//   - accountName: the CRD's metadata.name, never the resolved, hash-suffixed
//     Snowflake account name (design.md 3.12) — matches the tenant secret path
//   - locator: the Snowflake account locator captured from CREATE ACCOUNT
//     (design.md 3.6); this package never runs CREATE ACCOUNT itself, so the
//     caller supplies it
//   - region: the account's cloud-region string (e.g. "aws-eu-central-1",
//     design.md 3.1)
//
// Returns:
//   - User error from host.Hostname if region does not match the expected
//     cloud-region format
//   - System error if the tenant credential cannot be read, does not parse
//     as a valid private key, or the connection cannot be established
func (p *Pool) TenantAccount(ctx context.Context, namespace, accountName, locator, region string) (*sql.DB, error) {
	key := tenantKey{namespace: namespace, accountName: accountName}
	sf := &p.cfg.Snowflake

	path, err := secrets.NewTenantPath(sf.Org, namespace, accountName)
	if err != nil {
		return nil, err
	}

	lock := p.keyLock(key)
	lock.Lock()
	defer lock.Unlock()

	if db, ok := p.cachedTenant(key, locator, region); ok {
		p.maybeRotateLocked(ctx, db, path)
		return db, nil
	}

	hostname, err := host.Hostname(locator, region, sf.UsePrivateLink)
	if err != nil {
		return nil, err
	}

	raw, rotatedAt, err := p.backend.Get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("failed to read tenant credentials for %s/%s: %w", namespace, accountName, err)
	}
	creds, err := secrets.UnmarshalCredentials(raw, rotatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to read tenant credentials for %s/%s: %w", namespace, accountName, err)
	}
	privKey, err := parsePrivateKey(creds.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key for %s/%s: %w", namespace, accountName, err)
	}

	sfCfg := buildSnowflakeConfig(locator, hostname, creds.Username, privKey, "ACCOUNTADMIN", sf.DisableOCSPChecks)
	db, err := p.dial(dialConfig{snowflake: sfCfg, probeTimeout: sf.ConnectionProbeTimeout})
	if err != nil {
		return nil, fmt.Errorf("credentials for %s/%s were read successfully, but failed to connect to %s: %w (debugging tip: run `DESC USER %s` in Snowflake and compare RSA_PUBLIC_KEY_FP against %s; DisableOCSPChecks=%t)",
			namespace, accountName, hostname, err, creds.Username, publicKeyFingerprint(privKey), sf.DisableOCSPChecks)
	}
	applyPoolSettings(db, p.cfg)

	p.entriesMu.Lock()
	stale, hadStale := p.entries[key]
	p.entries[key] = tenantEntry{db: db, locator: locator, region: region}
	p.entriesMu.Unlock()

	if hadStale {
		_ = stale.db.Close()
	}

	p.maybeRotateLocked(ctx, db, path)
	return db, nil
}

// cachedTenant returns the cached *sql.DB for key if one exists and its
// locator/region match. It never dials and never validates region — a cached
// entry was already validated when it was dialed.
func (p *Pool) cachedTenant(key tenantKey, locator, region string) (*sql.DB, bool) {
	p.entriesMu.RLock()
	defer p.entriesMu.RUnlock()
	entry, ok := p.entries[key]
	if !ok || entry.locator != locator || entry.region != region {
		return nil, false
	}
	return entry.db, true
}

// EvictTenant closes and removes the cached *sql.DB for (namespace,
// accountName), if one exists. Called once an account is dropped (017, not
// yet written) so a deleted tenant's connection does not linger for the rest
// of the process's life. A key never dialed is a no-op, not an error.
func (p *Pool) EvictTenant(namespace, accountName string) {
	key := tenantKey{namespace: namespace, accountName: accountName}

	lock := p.keyLock(key)
	lock.Lock()
	defer lock.Unlock()

	p.entriesMu.Lock()
	entry, ok := p.entries[key]
	delete(p.entries, key)
	p.entriesMu.Unlock()

	if ok {
		_ = entry.db.Close()
	}
}

// Close closes every cached *sql.DB — the org-admin connection, if opened,
// and every tenant connection. Called exactly once, from
// cmd/provider/main.go on shutdown.
//
// Returns:
//   - System error joining any individual Close failure; every entry is
//     still attempted even if an earlier one fails
func (p *Pool) Close() error {
	var errs []error

	p.orgAdminMu.Lock()
	if p.orgAdminDB != nil {
		if err := p.orgAdminDB.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close org-admin connection: %w", err))
		}
		p.orgAdminDB = nil
	}
	p.orgAdminMu.Unlock()

	p.entriesMu.Lock()
	for key, entry := range p.entries {
		if err := entry.db.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close connection for %s/%s: %w", key.namespace, key.accountName, err))
		}
	}
	p.entries = make(map[tenantKey]tenantEntry)
	p.entriesMu.Unlock()

	return stderrors.Join(errs...)
}

// applyPoolSettings applies BaseConfig.Snowflake's connection-pool tuning to
// db — the underlying *sql.DB's own limits, idle timeout, and maximum
// lifetime, applied identically to every *sql.DB this package dials.
func applyPoolSettings(db *sql.DB, cfg *base.BaseConfig) {
	sf := &cfg.Snowflake
	db.SetMaxOpenConns(sf.MaxConnectionPoolSize)
	db.SetMaxIdleConns(sf.MaxIdleConnections)
	db.SetConnMaxLifetime(sf.ConnectionMaxLifetime)
	db.SetConnMaxIdleTime(sf.ConnectionMaxIdleTime)
}
