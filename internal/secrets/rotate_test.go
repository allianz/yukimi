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
	stderrors "errors"
	"strings"
	"testing"

	"github.com/allianz/yukimi/internal/errors"
)

// storeCredentials is the create-only store the account module (010) performs:
// generate, marshal, Create. It is the fixture every Rotate test builds on.
func storeCredentials(t *testing.T, b Backend, path Path) *Credentials {
	t.Helper()
	creds, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	value, err := MarshalCredentials(creds)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := b.Create(t.Context(), path, value); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return creds
}

// SC-010: Create on an occupied path returns an error and leaves the stored
// value byte-for-byte unchanged. This also covers the concurrent-replica race:
// the replica whose Create loses sees a failure, and nothing is reconciled on
// its behalf.
func TestCreate_OccupiedPathRejectedAndValuePreserved(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	first := storeCredentials(t, b, path)

	second, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	other, err := MarshalCredentials(second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := b.Create(ctx, path, other); err == nil {
		t.Fatal("expected the second create to fail on an occupied path")
	}

	value, rotatedAt, err := b.Get(ctx, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stored, err := UnmarshalCredentials(value, rotatedAt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stored.PrivateKey != first.PrivateKey {
		t.Error("a rejected Create must leave the stored credential untouched")
	}
}

// SC-011: Rotate fails with a system error when nothing exists yet at path.
func TestRotate_FailsIfNothingStored(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	_, err := Rotate(ctx, b, path, "platform")
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.IsUserError(err) {
		t.Error("expected a system error, not a user error")
	}
	if !strings.Contains(err.Error(), "failed to rotate secret at") ||
		!strings.Contains(err.Error(), "no secret stored") {
		t.Errorf("expected the wrap to name the rotation and the not-stored cause, got %v", err)
	}
}

// SC-011: Rotate overwrites the stored value with a freshly generated Credentials.
func TestRotate_OverwritesExisting(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	original := storeCredentials(t, b, path)

	rotated, err := Rotate(ctx, b, path, "platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rotated.Username != "platform" {
		t.Errorf("got username %q, want %q", rotated.Username, "platform")
	}
	if rotated.PrivateKey == original.PrivateKey {
		t.Error("expected a freshly generated keypair, not the original")
	}

	value, rotatedAt, err := b.Get(ctx, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stored, err := UnmarshalCredentials(value, rotatedAt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stored.PrivateKey != rotated.PrivateKey {
		t.Error("expected the store to hold the rotated keypair")
	}
}

// Rotate propagates a key-generation failure without touching the store, so a
// live credential is never replaced by a half-generated one.
func TestRotate_KeyGenerationFailureLeavesStoreUntouched(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	original := storeCredentials(t, b, path)
	b.OnUpdate = func(Path) error {
		t.Error("Rotate must not reach the store when key generation fails")
		return nil
	}
	withoutEntropy(t)

	if _, err := Rotate(ctx, b, path, "platform"); err == nil {
		t.Fatal("expected error")
	}

	b.OnUpdate = nil
	value, rotatedAt, err := b.Get(ctx, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stored, err := UnmarshalCredentials(value, rotatedAt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stored.PrivateKey != original.PrivateKey {
		t.Error("expected the stored credential to survive a failed rotation")
	}
}

// Rotate wraps any store failure as a system error, preserving errors.Is.
func TestRotate_StoreFailureIsSystemError(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	storeCredentials(t, b, path)
	b.OnUpdate = func(Path) error { return errStoreFault }

	_, err := Rotate(ctx, b, path, "platform")
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.IsUserError(err) {
		t.Error("expected a system error, not a user error")
	}
	if !stderrors.Is(err, errStoreFault) {
		t.Errorf("expected the wrap to preserve errors.Is(err, errStoreFault), got %v", err)
	}
}
