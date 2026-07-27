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

package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/allianz/yukimi/internal/errors"
)

// SecretManager provides credential retrieval, generation, and cache management.
type SecretManager interface {
	// GetOrgAdminCredentials retrieves organization-level admin credentials.
	// Path: snowflake/org/{org}/{account}/org-admin-credentials
	//
	// Returns:
	//   - System error if secret not found, backend permissions denied, backend unavailable, or credential parsing fails
	GetOrgAdminCredentials(ctx context.Context, orgName, account string) (*OrgAdminCredentials, error)

	// GetTenantCredentials retrieves namespace-specific tenant credentials.
	// Path: snowflake/tenant/{org}/{namespace}/{account}/platform-credentials
	//
	// Returns:
	//   - System error if secret not found, empty parameters, backend permissions denied, backend unavailable, or credential parsing fails
	GetTenantCredentials(ctx context.Context, orgName, namespace, account string) (*PlatformCredentials, error)

	// GenerateAndStore creates a new RSA key pair and stores it via backend.
	// If the secret path is pending deletion, returns a system error — never cancels the deletion silently.
	// The operator must cancel the pending deletion in the backend before retrying.
	//
	// Returns:
	//   - User error if spec.org or spec.account is empty
	//   - System error if secret is pending deletion, key generation fails, backend permissions denied, or backend unavailable
	GenerateAndStore(ctx context.Context, orgName, namespace, account string) (*PlatformCredentials, error)

	// DeleteTenantSecret removes tenant credentials via backend and invalidates cache.
	DeleteTenantSecret(ctx context.Context, orgName, namespace, account string) error

	// RotateTenantCredentials generates a new RSA key pair and overwrites the
	// existing tenant secret in the backend, then invalidates the cache entry.
	// The caller is responsible for pushing the new public key to Snowflake
	// (ALTER USER ... SET RSA_PUBLIC_KEY) — this method only replaces the stored
	// secret. Any live connection still using the old private key will fail once
	// the Snowflake-side key is updated; the caller must coordinate that update
	// with reconnection.
	//
	// Returns:
	//   - User error if spec.org or spec.account is empty
	//   - System error if secret is pending deletion, key generation fails, backend permissions denied, or backend unavailable
	RotateTenantCredentials(ctx context.Context, orgName, namespace, account string) (*PlatformCredentials, error)

	// RotateOrgAdminCredentials generates a new RSA key pair and overwrites the
	// existing org admin secret in the backend, then invalidates the cache entry.
	// Same caller responsibility as RotateTenantCredentials: pushing the new
	// public key to Snowflake is not handled by this package.
	//
	// Returns:
	//   - System error if secret is pending deletion, key generation fails, backend permissions denied, or backend unavailable
	RotateOrgAdminCredentials(ctx context.Context, orgName, account string) (*OrgAdminCredentials, error)

	// InvalidateTenantCache forces cache refresh on next GetTenantCredentials call.
	InvalidateTenantCache(orgName, namespace, account string)

	// InvalidateOrgAdminCache forces cache refresh on next GetOrgAdminCredentials call.
	InvalidateOrgAdminCache(orgName, account string)

	// ClearCache removes all cached credentials.
	ClearCache()

	// HealthCheck verifies backend connectivity.
	HealthCheck(ctx context.Context) error
}

var (
	instance     *secretManager
	instanceOnce sync.Once
	instanceMu   sync.RWMutex
)

type secretManager struct {
	backend     SecretBackend
	tenantCache *credentialCache[*PlatformCredentials]
	orgCache    *credentialCache[*OrgAdminCredentials]
}

// Initialize sets up the secrets manager singleton with a pre-built backend.
// Called once by the ProviderConfig controller during startup.
// Thread-safe using sync.Once — subsequent calls are silently ignored.
//
// The secrets package is business logic — it never logs. It only returns
// errors; the calling controller creates its own scoped Logger and calls
// Handle() on whatever error propagates up.
func Initialize(backend SecretBackend, cacheTTL time.Duration) error {
	instanceOnce.Do(func() {
		instanceMu.Lock()
		instance = &secretManager{
			backend:     backend,
			tenantCache: newCredentialCache[*PlatformCredentials](cacheTTL),
			orgCache:    newCredentialCache[*OrgAdminCredentials](cacheTTL),
		}
		instanceMu.Unlock()
	})
	return nil
}

// GetInstance returns the initialized secrets manager singleton.
// Returns error if Initialize() has not been called.
func GetInstance() (SecretManager, error) {
	instanceMu.RLock()
	mgr := instance
	instanceMu.RUnlock()

	if mgr == nil {
		return nil, fmt.Errorf("secrets manager not initialized - waiting for ProviderConfig 'default'")
	}
	return mgr, nil
}

// InitializeForMockTesting initializes with the provided mock backend.
// For unit tests that don't need real backend dependencies.
func InitializeForMockTesting(backend SecretBackend, cacheTTL time.Duration) {
	instanceMu.Lock()
	instance = &secretManager{
		backend:     backend,
		tenantCache: newCredentialCache[*PlatformCredentials](cacheTTL),
		orgCache:    newCredentialCache[*OrgAdminCredentials](cacheTTL),
	}
	instanceMu.Unlock()
}

// ResetForTesting resets singleton state between tests.
// Not thread-safe — only call from test setup/teardown.
func ResetForTesting() {
	instanceMu.Lock()
	instance = nil
	instanceOnce = sync.Once{}
	instanceMu.Unlock()
}

func (m *secretManager) GetOrgAdminCredentials(ctx context.Context, orgName, account string) (*OrgAdminCredentials, error) {
	key := orgAdminCacheKey(orgName, account)
	if creds, ok := m.orgCache.get(key); ok {
		return creds, nil
	}

	path, err := orgAdminSecretPath(orgName, account)
	if err != nil {
		return nil, fmt.Errorf("invalid org admin secret path: %w", err)
	}

	raw, err := m.backend.GetSecret(ctx, path)
	if err != nil {
		return nil, err
	}

	var creds OrgAdminCredentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, fmt.Errorf("failed to parse org admin credentials: %w", err)
	}
	if err := creds.validate(); err != nil {
		return nil, fmt.Errorf("invalid org admin credentials in backend: %w", err)
	}

	m.orgCache.set(key, &creds)
	return &creds, nil
}

func (m *secretManager) GetTenantCredentials(ctx context.Context, orgName, namespace, account string) (*PlatformCredentials, error) {
	key := tenantCacheKey(orgName, namespace, account)
	if creds, ok := m.tenantCache.get(key); ok {
		return creds, nil
	}

	path, err := tenantSecretPath(orgName, namespace, account)
	if err != nil {
		return nil, fmt.Errorf("invalid tenant secret path: %w", err)
	}

	raw, err := m.backend.GetSecret(ctx, path)
	if err != nil {
		return nil, err
	}

	var creds PlatformCredentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, fmt.Errorf("failed to parse tenant credentials: %w", err)
	}
	if err := creds.validate(); err != nil {
		return nil, fmt.Errorf("invalid tenant credentials in backend: %w", err)
	}

	m.tenantCache.set(key, &creds)
	return &creds, nil
}

func (m *secretManager) GenerateAndStore(ctx context.Context, orgName, namespace, account string) (*PlatformCredentials, error) {
	if orgName == "" {
		return nil, errors.NewUserError("spec.org must not be empty")
	}
	if account == "" {
		return nil, errors.NewUserError("spec.account must not be empty")
	}
	return m.generateAndPutTenantSecret(ctx, orgName, namespace, account)
}

func (m *secretManager) DeleteTenantSecret(ctx context.Context, orgName, namespace, account string) error {
	path, err := tenantSecretPath(orgName, namespace, account)
	if err != nil {
		return fmt.Errorf("invalid tenant secret path: %w", err)
	}

	if err := m.backend.DeleteSecret(ctx, path); err != nil {
		return fmt.Errorf("failed to delete tenant secret: %w", err)
	}

	m.tenantCache.delete(tenantCacheKey(orgName, namespace, account))
	return nil
}

// RotateTenantCredentials generates a new RSA key pair and overwrites the
// existing tenant secret in the backend. It never contacts Snowflake — the
// caller is responsible for pushing the new public key via ALTER USER.
func (m *secretManager) RotateTenantCredentials(ctx context.Context, orgName, namespace, account string) (*PlatformCredentials, error) {
	if orgName == "" {
		return nil, errors.NewUserError("spec.org must not be empty")
	}
	if account == "" {
		return nil, errors.NewUserError("spec.account must not be empty")
	}
	return m.generateAndPutTenantSecret(ctx, orgName, namespace, account)
}

// RotateOrgAdminCredentials generates a new RSA key pair and overwrites the
// existing org admin secret in the backend. It never contacts Snowflake — the
// caller is responsible for pushing the new public key via ALTER USER.
func (m *secretManager) RotateOrgAdminCredentials(ctx context.Context, orgName, account string) (*OrgAdminCredentials, error) {
	path, err := orgAdminSecretPath(orgName, account)
	if err != nil {
		return nil, fmt.Errorf("invalid org admin secret path: %w", err)
	}

	pending, err := m.backend.IsSecretPendingDeletion(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("failed to check secret deletion status: %w", err)
	}
	if pending {
		return nil, fmt.Errorf(
			"secret for org admin account '%s' in org '%s' is pending deletion — "+
				"the operator must cancel the pending deletion in the backend before this secret can be regenerated",
			sanitizeField(account), sanitizeField(orgName),
		)
	}

	keyPair, err := generateRSAKeyPair()
	if err != nil {
		return nil, fmt.Errorf("failed to generate RSA key pair: %w", err)
	}

	creds := &OrgAdminCredentials{
		Account:    account,
		Username:   "PLATFORM",
		PublicKey:  keyPair.PublicKey,
		PrivateKey: keyPair.PrivateKey,
	}

	raw, err := json.Marshal(creds)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal credentials: %w", err)
	}

	if err := m.backend.PutSecret(ctx, path, raw); err != nil {
		return nil, fmt.Errorf("failed to store credentials: %w", err)
	}

	m.orgCache.set(orgAdminCacheKey(orgName, account), creds)

	return creds, nil
}

// generateAndPutTenantSecret checks for a pending deletion, generates a new
// RSA key pair, stores it via the backend, and updates the cache. Shared by
// GenerateAndStore and RotateTenantCredentials, which differ only in caller
// intent (creating vs. replacing), not in behavior.
func (m *secretManager) generateAndPutTenantSecret(ctx context.Context, orgName, namespace, account string) (*PlatformCredentials, error) {
	path, err := tenantSecretPath(orgName, namespace, account)
	if err != nil {
		return nil, fmt.Errorf("invalid tenant secret path: %w", err)
	}

	pending, err := m.backend.IsSecretPendingDeletion(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("failed to check secret deletion status: %w", err)
	}
	if pending {
		return nil, fmt.Errorf(
			"secret for account '%s' in namespace '%s' is pending deletion — "+
				"the operator must cancel the pending deletion in the backend before this secret can be regenerated",
			sanitizeField(account), sanitizeField(namespace),
		)
	}

	keyPair, err := generateRSAKeyPair()
	if err != nil {
		return nil, fmt.Errorf("failed to generate RSA key pair: %w", err)
	}

	creds := &PlatformCredentials{
		Account:    account,
		Username:   "PLATFORM",
		PublicKey:  keyPair.PublicKey,
		PrivateKey: keyPair.PrivateKey,
	}

	raw, err := json.Marshal(creds)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal credentials: %w", err)
	}

	if err := m.backend.PutSecret(ctx, path, raw); err != nil {
		return nil, fmt.Errorf("failed to store credentials: %w", err)
	}

	m.tenantCache.set(tenantCacheKey(orgName, namespace, account), creds)

	return creds, nil
}

func (m *secretManager) InvalidateTenantCache(orgName, namespace, account string) {
	m.tenantCache.delete(tenantCacheKey(orgName, namespace, account))
}

func (m *secretManager) InvalidateOrgAdminCache(orgName, account string) {
	m.orgCache.delete(orgAdminCacheKey(orgName, account))
}

func (m *secretManager) ClearCache() {
	m.tenantCache.clear()
	m.orgCache.clear()
}

func (m *secretManager) HealthCheck(ctx context.Context) error {
	return m.backend.HealthCheck(ctx)
}
