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
	m.data[path] = value
	return nil
}

func (m *mockBackend) DeleteSecret(_ context.Context, path string) error {
	delete(m.data, path)
	return nil
}

func (m *mockBackend) IsSecretPendingDeletion(_ context.Context, path string) (bool, error) {
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

	if err := Initialize(mock1, time.Minute, nil); err != nil {
		t.Fatalf("first Initialize() failed: %v", err)
	}
	if err := Initialize(mock2, time.Minute, nil); err != nil {
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

	mock.putCredentials("snowflake/tenant/team-a/myorg/myaccount/platform-credentials", &PlatformCredentials{
		Account:    "myaccount",
		Username:   "PLATFORM",
		PublicKey:  "pubkey",
		PrivateKey: "privkey",
	})

	mgr, _ := GetInstance()
	creds, err := mgr.GetTenantCredentials(context.Background(), "team-a", "myorg", "myaccount")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.Username != "PLATFORM" {
		t.Errorf("expected PLATFORM, got %s", creds.Username)
	}

	// Second call should come from cache — delete from backend to verify
	delete(mock.data, "snowflake/tenant/team-a/myorg/myaccount/platform-credentials")
	creds2, err := mgr.GetTenantCredentials(context.Background(), "team-a", "myorg", "myaccount")
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

	path := "snowflake/tenant/ns/org/acc/platform-credentials"
	mock.putCredentials(path, &PlatformCredentials{
		Account: "acc", Username: "PLATFORM", PublicKey: "pub", PrivateKey: "priv",
	})

	mgr, _ := GetInstance()
	mgr.GetTenantCredentials(context.Background(), "ns", "org", "acc") //nolint:errcheck

	mgr.InvalidateTenantCache("ns", "org", "acc")
	delete(mock.data, path)

	_, err := mgr.GetTenantCredentials(context.Background(), "ns", "org", "acc")
	if err == nil {
		t.Error("expected error after invalidation with empty backend")
	}
}

// SC-019: GenerateAndStore returns user error when secret is pending deletion.
func TestGenerateAndStore_PendingDeletion(t *testing.T) {
	ResetForTesting()
	mock := newMockBackend()
	InitializeForMockTesting(mock, time.Minute)
	defer ResetForTesting()

	mock.pendingDeletion["snowflake/tenant/ns/org/acc/platform-credentials"] = true

	mgr, _ := GetInstance()
	_, err := mgr.GenerateAndStore(context.Background(), "ns", "org", "acc")
	if err == nil {
		t.Fatal("expected error for pending deletion secret")
	}
	if !isUserErr(err) {
		t.Errorf("expected user error, got: %v", err)
	}
}

// GenerateAndStore returns user error for empty org.
func TestGenerateAndStore_EmptyOrg(t *testing.T) {
	ResetForTesting()
	InitializeForMockTesting(newMockBackend(), time.Minute)
	defer ResetForTesting()

	mgr, _ := GetInstance()
	_, err := mgr.GenerateAndStore(context.Background(), "ns", "", "acc")
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
	_, err := mgr.GenerateAndStore(context.Background(), "ns", "org", "")
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
	creds, err := mgr.GenerateAndStore(context.Background(), "ns", "org", "acc")
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
	if _, ok := mock.data["snowflake/tenant/ns/org/acc/platform-credentials"]; !ok {
		t.Error("expected credentials stored in backend")
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

// isUserErr is a test helper to check if an error is a user error.
func isUserErr(err error) bool {
	return errors.IsUserError(err)
}
