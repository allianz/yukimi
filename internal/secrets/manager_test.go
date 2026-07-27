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
	"testing"
	"time"

	"github.com/allianz/yukimi/internal/errors"
)

// mockBackend is a simple in-memory SecretBackend for unit tests.
type mockBackend struct {
	data            map[string][]byte
	pendingDeletion map[string]bool
	healthErr       error
	putErr          error
	deleteErr       error
	pendingCheckErr error
}

func newMockBackend() *mockBackend {
	return &mockBackend{
		data:            make(map[string][]byte),
		pendingDeletion: make(map[string]bool),
	}
}

func (m *mockBackend) GetSecret(_ context.Context, path string) ([]byte, error) {
	v, ok := m.data[path]
	if !ok {
		return nil, fmt.Errorf("secret not found: %s", path)
	}
	return v, nil
}

func (m *mockBackend) PutSecret(_ context.Context, path string, value []byte) error {
	if m.putErr != nil {
		return m.putErr
	}
	m.data[path] = value
	return nil
}

func (m *mockBackend) DeleteSecret(_ context.Context, path string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.data, path)
	return nil
}

func (m *mockBackend) IsSecretPendingDeletion(_ context.Context, path string) (bool, error) {
	if m.pendingCheckErr != nil {
		return false, m.pendingCheckErr
	}
	return m.pendingDeletion[path], nil
}

func (m *mockBackend) HealthCheck(_ context.Context) error {
	return m.healthErr
}

func (m *mockBackend) putCredentials(path string, creds *PlatformCredentials) {
	raw, _ := json.Marshal(creds)
	m.data[path] = raw
}

// SC-009: GetInstance() returns error before Initialize() called.
func TestGetInstance_BeforeInitialize(t *testing.T) {
	ResetForTesting()
	_, err := GetInstance()
	if err == nil {
		t.Fatal("expected error before Initialize()")
	}
}

// SC-008: Initialize() uses sync.Once — second call is ignored.
func TestInitialize_OnlyOnce(t *testing.T) {
	ResetForTesting()
	mock1 := newMockBackend()
	mock2 := newMockBackend()

	if err := Initialize(mock1, time.Minute); err != nil {
		t.Fatalf("first Initialize() failed: %v", err)
	}
	if err := Initialize(mock2, time.Minute); err != nil {
		t.Fatalf("second Initialize() failed: %v", err)
	}

	mgr, _ := GetInstance()
	sm := mgr.(*secretManager)
	if sm.backend != mock1 {
		t.Error("expected first backend to be retained after second Initialize()")
	}
}

// GetTenantCredentials returns credentials from backend and caches them.
func TestGetTenantCredentials_HitAndCache(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	mock.putCredentials("snowflake/tenant/myorg/team-a/myaccount/platform-credentials", &PlatformCredentials{
		Account:    "myaccount",
		Username:   "PLATFORM",
		PublicKey:  "pubkey",
		PrivateKey: "privkey",
	})

	mgr, _ := GetInstance()
	creds, err := mgr.GetTenantCredentials(context.Background(), "myorg", "team-a", "myaccount")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.Username != "PLATFORM" {
		t.Errorf("expected PLATFORM, got %s", creds.Username)
	}

	// Second call should come from cache — delete from backend to verify
	delete(mock.data, "snowflake/tenant/myorg/team-a/myaccount/platform-credentials")
	creds2, err := mgr.GetTenantCredentials(context.Background(), "myorg", "team-a", "myaccount")
	if err != nil {
		t.Fatalf("expected cache hit, got error: %v", err)
	}
	if creds2.Username != "PLATFORM" {
		t.Errorf("expected PLATFORM from cache, got %s", creds2.Username)
	}
}

// InvalidateTenantCache forces a backend fetch on next call.
func TestInvalidateTenantCache(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	path := "snowflake/tenant/org/ns/acc/platform-credentials"
	mock.putCredentials(path, &PlatformCredentials{
		Account: "acc", Username: "PLATFORM", PublicKey: "pub", PrivateKey: "priv",
	})

	mgr, _ := GetInstance()
	mgr.GetTenantCredentials(context.Background(), "org", "ns", "acc") //nolint:errcheck

	mgr.InvalidateTenantCache("org", "ns", "acc")
	delete(mock.data, path)

	_, err := mgr.GetTenantCredentials(context.Background(), "org", "ns", "acc")
	if err == nil {
		t.Error("expected error after invalidation with empty backend")
	}
}

// SC-014: GenerateAndStore returns system error when secret is pending deletion.
func TestGenerateAndStore_PendingDeletion(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	mock.pendingDeletion["snowflake/tenant/org/ns/acc/platform-credentials"] = true

	mgr, _ := GetInstance()
	_, err := mgr.GenerateAndStore(context.Background(), "org", "ns", "acc")
	if err == nil {
		t.Fatal("expected error for pending deletion secret")
	}
	if isUserErr(err) {
		t.Errorf("expected system error, got user error: %v", err)
	}
}

// GenerateAndStore returns user error for empty org.
func TestGenerateAndStore_EmptyOrg(t *testing.T) {
	ResetForTesting()
	InitializeForMockTesting(newMockBackend(), time.Minute)
	defer ResetForTesting()

	mgr, _ := GetInstance()
	_, err := mgr.GenerateAndStore(context.Background(), "", "ns", "acc")
	if err == nil {
		t.Fatal("expected error for empty org")
	}
	if !isUserErr(err) {
		t.Errorf("expected user error for empty org, got: %v", err)
	}
}

// GenerateAndStore returns user error for empty account.
func TestGenerateAndStore_EmptyAccount(t *testing.T) {
	ResetForTesting()
	InitializeForMockTesting(newMockBackend(), time.Minute)
	defer ResetForTesting()

	mgr, _ := GetInstance()
	_, err := mgr.GenerateAndStore(context.Background(), "org", "ns", "")
	if err == nil {
		t.Fatal("expected error for empty account")
	}
	if !isUserErr(err) {
		t.Errorf("expected user error for empty account, got: %v", err)
	}
}

// GenerateAndStore stores credentials and returns them.
func TestGenerateAndStore_Success(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	mgr, _ := GetInstance()
	creds, err := mgr.GenerateAndStore(context.Background(), "org", "ns", "acc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.Username != "PLATFORM" {
		t.Errorf("expected PLATFORM, got %s", creds.Username)
	}
	if creds.PublicKey == "" {
		t.Error("expected non-empty public key")
	}
	if creds.PrivateKey == "" {
		t.Error("expected non-empty private key")
	}

	// Verify stored in backend
	if _, ok := mock.data["snowflake/tenant/org/ns/acc/platform-credentials"]; !ok {
		t.Error("expected credentials stored in backend")
	}
}

// SC-021: RotateTenantCredentials overwrites the stored secret and invalidates the cache.
func TestRotateTenantCredentials_Success(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	path := "snowflake/tenant/org/ns/acc/platform-credentials"
	mock.putCredentials(path, &PlatformCredentials{
		Account: "acc", Username: "PLATFORM", PublicKey: "old-pub", PrivateKey: "old-priv",
	})

	mgr, _ := GetInstance()
	// Prime the cache with the old credentials.
	if _, err := mgr.GetTenantCredentials(context.Background(), "org", "ns", "acc"); err != nil {
		t.Fatalf("unexpected error priming cache: %v", err)
	}

	newCreds, err := mgr.RotateTenantCredentials(context.Background(), "org", "ns", "acc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newCreds.PublicKey == "old-pub" {
		t.Error("expected a newly generated public key, got the old one")
	}

	// The cache must reflect the new credentials on the next read, without touching the backend.
	delete(mock.data, path)
	cached, err := mgr.GetTenantCredentials(context.Background(), "org", "ns", "acc")
	if err != nil {
		t.Fatalf("expected cache hit with rotated credentials, got error: %v", err)
	}
	if cached.PublicKey != newCreds.PublicKey {
		t.Errorf("expected cached credentials to match rotated credentials, got %q want %q", cached.PublicKey, newCreds.PublicKey)
	}
}

// RotateTenantCredentials returns user error for empty org.
func TestRotateTenantCredentials_EmptyOrg(t *testing.T) {
	ResetForTesting()
	InitializeForMockTesting(newMockBackend(), time.Minute)
	defer ResetForTesting()

	mgr, _ := GetInstance()
	_, err := mgr.RotateTenantCredentials(context.Background(), "", "ns", "acc")
	if err == nil {
		t.Fatal("expected error for empty org")
	}
	if !isUserErr(err) {
		t.Errorf("expected user error for empty org, got: %v", err)
	}
}

// RotateTenantCredentials returns user error for empty account.
func TestRotateTenantCredentials_EmptyAccount(t *testing.T) {
	ResetForTesting()
	InitializeForMockTesting(newMockBackend(), time.Minute)
	defer ResetForTesting()

	mgr, _ := GetInstance()
	_, err := mgr.RotateTenantCredentials(context.Background(), "org", "ns", "")
	if err == nil {
		t.Fatal("expected error for empty account")
	}
	if !isUserErr(err) {
		t.Errorf("expected user error for empty account, got: %v", err)
	}
}

// RotateTenantCredentials returns system error when secret is pending deletion.
func TestRotateTenantCredentials_PendingDeletion(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	mock.pendingDeletion["snowflake/tenant/org/ns/acc/platform-credentials"] = true

	mgr, _ := GetInstance()
	_, err := mgr.RotateTenantCredentials(context.Background(), "org", "ns", "acc")
	if err == nil {
		t.Fatal("expected error for pending deletion secret")
	}
	if isUserErr(err) {
		t.Errorf("expected system error, got user error: %v", err)
	}
}

// SC-021: RotateOrgAdminCredentials overwrites the stored secret and invalidates the cache.
func TestRotateOrgAdminCredentials_Success(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	path := "snowflake/org/myorg/orgadmin/org-admin-credentials"
	mock.putCredentials(path, &PlatformCredentials{
		Account: "orgadmin", Username: "PLATFORM", PublicKey: "old-pub", PrivateKey: "old-priv",
	})

	mgr, _ := GetInstance()
	if _, err := mgr.GetOrgAdminCredentials(context.Background(), "myorg", "orgadmin"); err != nil {
		t.Fatalf("unexpected error priming cache: %v", err)
	}

	newCreds, err := mgr.RotateOrgAdminCredentials(context.Background(), "myorg", "orgadmin")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newCreds.PublicKey == "old-pub" {
		t.Error("expected a newly generated public key, got the old one")
	}

	cached, err := mgr.GetOrgAdminCredentials(context.Background(), "myorg", "orgadmin")
	if err != nil {
		t.Fatalf("expected cache hit with rotated credentials, got error: %v", err)
	}
	if cached.PublicKey != newCreds.PublicKey {
		t.Errorf("expected cached credentials to match rotated credentials, got %q want %q", cached.PublicKey, newCreds.PublicKey)
	}
}

// RotateOrgAdminCredentials returns system error when secret is pending deletion.
func TestRotateOrgAdminCredentials_PendingDeletion(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	mock.pendingDeletion["snowflake/org/myorg/orgadmin/org-admin-credentials"] = true

	mgr, _ := GetInstance()
	_, err := mgr.RotateOrgAdminCredentials(context.Background(), "myorg", "orgadmin")
	if err == nil {
		t.Fatal("expected error for pending deletion secret")
	}
	if isUserErr(err) {
		t.Errorf("expected system error, got user error: %v", err)
	}
}

// SC-022: HealthCheck delegates to backend.
func TestHealthCheck_Delegates(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	mock.healthErr = fmt.Errorf("backend down")
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	mgr, _ := GetInstance()
	err := mgr.HealthCheck(context.Background())
	if err == nil || err.Error() != "backend down" {
		t.Errorf("expected 'backend down', got: %v", err)
	}
}

// DeleteTenantSecret removes the secret via backend and invalidates the cache.
func TestDeleteTenantSecret_Success(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	path := "snowflake/tenant/org/ns/acc/platform-credentials"
	mock.putCredentials(path, &PlatformCredentials{
		Account: "acc", Username: "PLATFORM", PublicKey: "pub", PrivateKey: "priv",
	})

	mgr, _ := GetInstance()
	if _, err := mgr.GetTenantCredentials(context.Background(), "org", "ns", "acc"); err != nil {
		t.Fatalf("unexpected error priming cache: %v", err)
	}

	if err := mgr.DeleteTenantSecret(context.Background(), "org", "ns", "acc"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := mock.data[path]; ok {
		t.Error("expected secret removed from backend")
	}

	// Cache entry must be invalidated too, so a stale value is never served.
	_, err := mgr.GetTenantCredentials(context.Background(), "org", "ns", "acc")
	if err == nil {
		t.Error("expected error after deletion with empty backend")
	}
}

// InvalidateOrgAdminCache forces a backend fetch on next call.
func TestInvalidateOrgAdminCache(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	path := "snowflake/org/myorg/orgadmin/org-admin-credentials"
	mock.putCredentials(path, &PlatformCredentials{
		Account: "orgadmin", Username: "PLATFORM", PublicKey: "pub", PrivateKey: "priv",
	})

	mgr, _ := GetInstance()
	if _, err := mgr.GetOrgAdminCredentials(context.Background(), "myorg", "orgadmin"); err != nil {
		t.Fatalf("unexpected error priming cache: %v", err)
	}

	mgr.InvalidateOrgAdminCache("myorg", "orgadmin")
	delete(mock.data, path)

	_, err := mgr.GetOrgAdminCredentials(context.Background(), "myorg", "orgadmin")
	if err == nil {
		t.Error("expected error after invalidation with empty backend")
	}
}

// ClearCache removes both tenant and org admin cache entries.
func TestClearCache(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	tenantPath := "snowflake/tenant/org/ns/acc/platform-credentials"
	mock.putCredentials(tenantPath, &PlatformCredentials{
		Account: "acc", Username: "PLATFORM", PublicKey: "pub", PrivateKey: "priv",
	})
	orgPath := "snowflake/org/myorg/orgadmin/org-admin-credentials"
	mock.putCredentials(orgPath, &PlatformCredentials{
		Account: "orgadmin", Username: "PLATFORM", PublicKey: "pub", PrivateKey: "priv",
	})

	mgr, _ := GetInstance()
	if _, err := mgr.GetTenantCredentials(context.Background(), "org", "ns", "acc"); err != nil {
		t.Fatalf("unexpected error priming tenant cache: %v", err)
	}
	if _, err := mgr.GetOrgAdminCredentials(context.Background(), "myorg", "orgadmin"); err != nil {
		t.Fatalf("unexpected error priming org cache: %v", err)
	}

	mgr.ClearCache()
	delete(mock.data, tenantPath)
	delete(mock.data, orgPath)

	if _, err := mgr.GetTenantCredentials(context.Background(), "org", "ns", "acc"); err == nil {
		t.Error("expected error for tenant credentials after ClearCache with empty backend")
	}
	if _, err := mgr.GetOrgAdminCredentials(context.Background(), "myorg", "orgadmin"); err == nil {
		t.Error("expected error for org admin credentials after ClearCache with empty backend")
	}
}

// GetOrgAdminCredentials returns error for an invalid path (empty component).
func TestGetOrgAdminCredentials_InvalidPath(t *testing.T) {
	ResetForTesting()
	InitializeForMockTesting(newMockBackend(), time.Minute)
	defer ResetForTesting()

	mgr, _ := GetInstance()
	_, err := mgr.GetOrgAdminCredentials(context.Background(), "", "orgadmin")
	if err == nil {
		t.Fatal("expected error for empty org in path construction")
	}
}

// GetTenantCredentials returns error for an invalid path (empty component).
func TestGetTenantCredentials_InvalidPath(t *testing.T) {
	ResetForTesting()
	InitializeForMockTesting(newMockBackend(), time.Minute)
	defer ResetForTesting()

	mgr, _ := GetInstance()
	_, err := mgr.GetTenantCredentials(context.Background(), "", "ns", "acc")
	if err == nil {
		t.Fatal("expected error for empty org in path construction")
	}
}

// GetOrgAdminCredentials returns error when backend fails to parse or lookup.
func TestGetOrgAdminCredentials_NotFound(t *testing.T) {
	ResetForTesting()
	InitializeForMockTesting(newMockBackend(), time.Minute)
	defer ResetForTesting()

	mgr, _ := GetInstance()
	_, err := mgr.GetOrgAdminCredentials(context.Background(), "myorg", "orgadmin")
	if err == nil {
		t.Fatal("expected error for missing org admin secret")
	}
}

// GetOrgAdminCredentials returns error on malformed JSON in backend.
func TestGetOrgAdminCredentials_MalformedJSON(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	mock.data["snowflake/org/myorg/orgadmin/org-admin-credentials"] = []byte("not json")

	mgr, _ := GetInstance()
	_, err := mgr.GetOrgAdminCredentials(context.Background(), "myorg", "orgadmin")
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

// GetOrgAdminCredentials returns error when a credential field is empty (tampered secret).
func TestGetOrgAdminCredentials_InvalidCredentials(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	mock.data["snowflake/org/myorg/orgadmin/org-admin-credentials"] = []byte(`{
		"account": "orgadmin",
		"username": "",
		"public_key": "pub",
		"private_key": "priv"
	}`)

	mgr, _ := GetInstance()
	_, err := mgr.GetOrgAdminCredentials(context.Background(), "myorg", "orgadmin")
	if err == nil {
		t.Fatal("expected error for credential with empty username")
	}
}

// GetTenantCredentials returns error on malformed JSON in backend.
func TestGetTenantCredentials_MalformedJSON(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	mock.data["snowflake/tenant/org/ns/acc/platform-credentials"] = []byte("not json")

	mgr, _ := GetInstance()
	_, err := mgr.GetTenantCredentials(context.Background(), "org", "ns", "acc")
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

// GetTenantCredentials returns error when a credential field is empty (tampered secret).
func TestGetTenantCredentials_InvalidCredentials(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	mock.data["snowflake/tenant/org/ns/acc/platform-credentials"] = []byte(`{
		"account": "acc",
		"username": "PLATFORM",
		"public_key": "",
		"private_key": "priv"
	}`)

	mgr, _ := GetInstance()
	_, err := mgr.GetTenantCredentials(context.Background(), "org", "ns", "acc")
	if err == nil {
		t.Fatal("expected error for credential with empty public_key")
	}
}

// RotateOrgAdminCredentials returns error when key generation cannot proceed
// because the secret path is invalid.
func TestRotateOrgAdminCredentials_InvalidPath(t *testing.T) {
	ResetForTesting()
	InitializeForMockTesting(newMockBackend(), time.Minute)
	defer ResetForTesting()

	mgr, _ := GetInstance()
	_, err := mgr.RotateOrgAdminCredentials(context.Background(), "", "orgadmin")
	if err == nil {
		t.Fatal("expected error for empty org in path construction")
	}
}

// GenerateAndStore returns error when the backend fails to store the secret.
func TestGenerateAndStore_BackendPutFails(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	mock.putErr = fmt.Errorf("put failed")
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	mgr, _ := GetInstance()
	_, err := mgr.GenerateAndStore(context.Background(), "org", "ns", "acc")
	if err == nil {
		t.Fatal("expected error when backend PutSecret fails")
	}
}

// GenerateAndStore returns error when checking pending deletion fails.
func TestGenerateAndStore_PendingCheckFails(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	mock.pendingCheckErr = fmt.Errorf("describe failed")
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	mgr, _ := GetInstance()
	_, err := mgr.GenerateAndStore(context.Background(), "org", "ns", "acc")
	if err == nil {
		t.Fatal("expected error when IsSecretPendingDeletion fails")
	}
}

// DeleteTenantSecret returns error when the backend fails to delete.
func TestDeleteTenantSecret_BackendFails(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	mock.deleteErr = fmt.Errorf("delete failed")
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	mgr, _ := GetInstance()
	err := mgr.DeleteTenantSecret(context.Background(), "org", "ns", "acc")
	if err == nil {
		t.Fatal("expected error when backend DeleteSecret fails")
	}
}

// DeleteTenantSecret returns error for an invalid path (empty component).
func TestDeleteTenantSecret_InvalidPath(t *testing.T) {
	ResetForTesting()
	InitializeForMockTesting(newMockBackend(), time.Minute)
	defer ResetForTesting()

	mgr, _ := GetInstance()
	err := mgr.DeleteTenantSecret(context.Background(), "", "ns", "acc")
	if err == nil {
		t.Fatal("expected error for empty org in path construction")
	}
}

// RotateTenantCredentials returns error when the backend fails to store the secret.
func TestRotateTenantCredentials_BackendPutFails(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	mock.putErr = fmt.Errorf("put failed")
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	mgr, _ := GetInstance()
	_, err := mgr.RotateTenantCredentials(context.Background(), "org", "ns", "acc")
	if err == nil {
		t.Fatal("expected error when backend PutSecret fails")
	}
}

// RotateOrgAdminCredentials returns error when checking pending deletion fails.
func TestRotateOrgAdminCredentials_PendingCheckFails(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	mock.pendingCheckErr = fmt.Errorf("describe failed")
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	mgr, _ := GetInstance()
	_, err := mgr.RotateOrgAdminCredentials(context.Background(), "myorg", "orgadmin")
	if err == nil {
		t.Fatal("expected error when IsSecretPendingDeletion fails")
	}
}

// RotateOrgAdminCredentials returns error when the backend fails to store the secret.
func TestRotateOrgAdminCredentials_BackendPutFails(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	mock.putErr = fmt.Errorf("put failed")
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	mgr, _ := GetInstance()
	_, err := mgr.RotateOrgAdminCredentials(context.Background(), "myorg", "orgadmin")
	if err == nil {
		t.Fatal("expected error when backend PutSecret fails")
	}
}

// PlatformCredentials.validate rejects each individually empty field.
func TestPlatformCredentials_Validate(t *testing.T) {
	base := PlatformCredentials{Account: "acc", Username: "PLATFORM", PublicKey: "pub", PrivateKey: "priv"}

	cases := []struct {
		name string
		mut  func(*PlatformCredentials)
	}{
		{"empty account", func(c *PlatformCredentials) { c.Account = "" }},
		{"empty username", func(c *PlatformCredentials) { c.Username = "" }},
		{"empty public key", func(c *PlatformCredentials) { c.PublicKey = "" }},
		{"empty private key", func(c *PlatformCredentials) { c.PrivateKey = "" }},
	}

	for _, c := range cases {
		creds := base
		c.mut(&creds)
		if err := creds.validate(); err == nil {
			t.Errorf("%s: expected validation error", c.name)
		}
	}

	if err := base.validate(); err != nil {
		t.Errorf("expected valid credentials to pass, got: %v", err)
	}
}

// OrgAdminCredentials.validate rejects each individually empty field.
func TestOrgAdminCredentials_Validate(t *testing.T) {
	base := OrgAdminCredentials{Account: "acc", Username: "PLATFORM", PublicKey: "pub", PrivateKey: "priv"}

	cases := []struct {
		name string
		mut  func(*OrgAdminCredentials)
	}{
		{"empty account", func(c *OrgAdminCredentials) { c.Account = "" }},
		{"empty username", func(c *OrgAdminCredentials) { c.Username = "" }},
		{"empty public key", func(c *OrgAdminCredentials) { c.PublicKey = "" }},
		{"empty private key", func(c *OrgAdminCredentials) { c.PrivateKey = "" }},
	}

	for _, c := range cases {
		creds := base
		c.mut(&creds)
		if err := creds.validate(); err == nil {
			t.Errorf("%s: expected validation error", c.name)
		}
	}

	if err := base.validate(); err != nil {
		t.Errorf("expected valid credentials to pass, got: %v", err)
	}
}

// sanitizeField strips control characters used in log injection attempts.
func TestSanitizeField(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"normal-value", "normal-value"},
		{"line1\nline2", "line1line2"},
		{"tab\there", "tabhere"},
		{"del\x7fchar", "delchar"},
		{"", ""},
	}
	for _, c := range cases {
		if got := sanitizeField(c.input); got != c.want {
			t.Errorf("sanitizeField(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// isUserErr is a test helper to check if an error is a user error.
func isUserErr(err error) bool {
	return errors.IsUserError(err)
}
