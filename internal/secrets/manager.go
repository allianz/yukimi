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
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
)

// SecretManager provides credential retrieval, generation, and cache management.
type SecretManager interface {
	// GetOrgAdminCredentials retrieves organization-level admin credentials.
	// Path: snowflake/org/{org}/org-admin-credentials
	//
	// Returns:
	//   - User error if secret not found or backend permissions denied
	//   - System error if backend unavailable or credential parsing fails
	GetOrgAdminCredentials(ctx context.Context, orgName string) (*OrgAdminCredentials, error)

	// GetTenantCredentials retrieves namespace-specific tenant credentials.
	// Path: snowflake/tenant/{namespace}/{org}/{account}/platform-credentials
	//
	// Returns:
	//   - User error if secret not found or backend permissions denied
	//   - System error if empty parameters, backend unavailable, or credential parsing fails
	GetTenantCredentials(ctx context.Context, namespace, orgName, account string) (*PlatformCredentials, error)

	// GenerateAndStore creates a new RSA key pair and stores it via backend.
	// If the secret path is pending deletion, returns a user error — never restores silently.
	//
	// Returns:
	//   - User error if secret is pending deletion, or backend permissions denied
	//   - User error if spec.org or spec.account is empty
	//   - System error if key generation fails or backend unavailable
	GenerateAndStore(ctx context.Context, namespace, orgName, account string) (*PlatformCredentials, error)

	// DeleteTenantSecret removes tenant credentials via backend and invalidates cache.
	DeleteTenantSecret(ctx context.Context, namespace, orgName, account string) error

	// InvalidateTenantCache forces cache refresh on next GetTenantCredentials call.
	InvalidateTenantCache(namespace, orgName, account string)

	// InvalidateOrgAdminCache forces cache refresh on next GetOrgAdminCredentials call.
	InvalidateOrgAdminCache(orgName string)

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
	backend    SecretBackend
	tenantCache  *credentialCache[*PlatformCredentials]
	orgCache     *credentialCache[*OrgAdminCredentials]
	logger     logging.Logger
}

// Initialize sets up the secrets manager singleton with a pre-built backend.
// Called once by the ProviderConfig controller during startup.
// Thread-safe using sync.Once — subsequent calls are silently ignored.
func Initialize(backend SecretBackend, cacheTTL time.Duration, logger logging.Logger) error {
	instanceOnce.Do(func() {
		instanceMu.Lock()
		instance = &secretManager{
			backend:     backend,
			tenantCache: newCredentialCache[*PlatformCredentials](cacheTTL),
			orgCache:    newCredentialCache[*OrgAdminCredentials](cacheTTL),
			logger:      logger,
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

// InitializeForIntegrationTesting initializes with a real backend selected via SECRET_BACKEND env var.
// Defaults to "aws". Cache TTL is hardcoded to 30 seconds. Panics if config loading fails.
func InitializeForIntegrationTesting() {
	panic("InitializeForIntegrationTesting: not yet implemented — add backend selection logic here")
}

// InitializeForMockTesting initializes with the provided mock backend.
// For unit tests that don't need real backend dependencies.
func InitializeForMockTesting(backend SecretBackend, cacheTTL time.Duration) {
	instanceMu.Lock()
	instance = &secretManager{
		backend:     backend,
		tenantCache: newCredentialCache[*PlatformCredentials](cacheTTL),
		orgCache:    newCredentialCache[*OrgAdminCredentials](cacheTTL),
		logger:      logging.NewNopLogger(),
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

func (m *secretManager) GetOrgAdminCredentials(ctx context.Context, orgName string) (*OrgAdminCredentials, error) {
	key := orgAdminCacheKey(orgName)
	if creds, ok := m.orgCache.get(key); ok {
		return creds, nil
	}

	path, err := orgAdminSecretPath(orgName)
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

func (m *secretManager) GetTenantCredentials(ctx context.Context, namespace, orgName, account string) (*PlatformCredentials, error) {
	key := tenantCacheKey(namespace, orgName, account)
	if creds, ok := m.tenantCache.get(key); ok {
		return creds, nil
	}

	path, err := tenantSecretPath(namespace, orgName, account)
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

func (m *secretManager) GenerateAndStore(ctx context.Context, namespace, orgName, account string) (*PlatformCredentials, error) {
	if orgName == "" {
		return nil, errors.NewUserError("spec.org must not be empty")
	}
	if account == "" {
		return nil, errors.NewUserError("spec.account must not be empty")
	}

	path, err := tenantSecretPath(namespace, orgName, account)
	if err != nil {
		return nil, fmt.Errorf("invalid tenant secret path: %w", err)
	}

	pending, err := m.backend.IsSecretPendingDeletion(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("failed to check secret deletion status: %w", err)
	}
	if pending {
		return nil, errors.NewUserError(fmt.Sprintf(
			"Secret for account '%s' in namespace '%s' is pending deletion. "+
				"If accidental, restore it manually in the backend and retry. "+
				"If intentional, wait for deletion to complete before recreating.",
			sanitizeField(account), sanitizeField(namespace),
		))
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

	key := tenantCacheKey(namespace, orgName, account)
	m.tenantCache.set(key, creds)

	return creds, nil
}

func (m *secretManager) DeleteTenantSecret(ctx context.Context, namespace, orgName, account string) error {
	path, err := tenantSecretPath(namespace, orgName, account)
	if err != nil {
		return fmt.Errorf("invalid tenant secret path: %w", err)
	}

	if err := m.backend.DeleteSecret(ctx, path); err != nil {
		return fmt.Errorf("failed to delete tenant secret: %w", err)
	}

	m.tenantCache.delete(tenantCacheKey(namespace, orgName, account))
	return nil
}

func (m *secretManager) InvalidateTenantCache(namespace, orgName, account string) {
	m.tenantCache.delete(tenantCacheKey(namespace, orgName, account))
}

func (m *secretManager) InvalidateOrgAdminCache(orgName string) {
	m.orgCache.delete(orgAdminCacheKey(orgName))
}

func (m *secretManager) ClearCache() {
	m.tenantCache.clear()
	m.orgCache.clear()
}

func (m *secretManager) HealthCheck(ctx context.Context) error {
	return m.backend.HealthCheck(ctx)
}
