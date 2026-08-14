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
	"testing"

	"github.com/allianz/yukimi/internal/errors"
)

// SC-009: CreateOrRecover returns (newCreds, false, nil) on a clean create.
func TestCreateOrRecover_CleanCreate(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)
	newCreds, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stored, existed, err := CreateOrRecover(ctx, b, path, newCreds)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if existed {
		t.Error("expected existed=false")
	}
	if stored != newCreds {
		t.Error("expected stored to be newCreds")
	}
}

// SC-010: On ErrPendingDeletion, CreateOrRecover purges then creates fresh,
// never returning the value that was purged.
func TestCreateOrRecover_PendingDeletion_PurgesAndCreatesFresh(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	original, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, _, err := CreateOrRecover(ctx, b, path, original); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := b.Delete(ctx, path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fresh, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stored, existed, err := CreateOrRecover(ctx, b, path, fresh)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if existed {
		t.Error("expected existed=false")
	}
	if stored.PrivateKey != fresh.PrivateKey {
		t.Error("expected the fresh keypair to be returned")
	}
	if stored.PrivateKey == original.PrivateKey {
		t.Error("must never reuse the purged, deleted keypair")
	}
}

// SC-011: On ErrAlreadyExists, CreateOrRecover Gets and returns the existing
// value — never the caller's newCreds — and never calls Update. This also
// covers the concurrent-replica-race edge case: whichever replica's Create
// loses sees ErrAlreadyExists and recovers the winner's credentials.
func TestCreateOrRecover_AlreadyExists_ReturnsExistingNeverUpdates(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	first, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stored1, existed1, err := CreateOrRecover(ctx, b, path, first)
	if err != nil || existed1 {
		t.Fatalf("first call: got existed=%v err=%v, want existed=false err=nil", existed1, err)
	}

	b.OnUpdate = func(Path) error {
		t.Error("CreateOrRecover must never call Update on an already-existing secret")
		return nil
	}

	second, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stored2, existed2, err := CreateOrRecover(ctx, b, path, second)
	if err != nil || !existed2 {
		t.Fatalf("second call: got existed=%v err=%v, want existed=true err=nil", existed2, err)
	}
	if stored2.PrivateKey != stored1.PrivateKey {
		t.Error("expected the first attempt's keypair to be recovered, not overwritten")
	}
	if stored2.PrivateKey == second.PrivateKey {
		t.Error("must never return the caller's newCreds when existed=true")
	}
}

// SC-012: Rotate fails with a system error when nothing exists yet at path.
func TestRotate_FailsIfNothingStored(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	if _, err := Rotate(ctx, b, path, "platform"); err == nil {
		t.Fatal("expected error")
	} else if errors.IsUserError(err) {
		t.Error("expected a system error, not a user error")
	}
}

// SC-012: Rotate overwrites the stored value with a freshly generated Credentials.
func TestRotate_OverwritesExisting(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	original, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, _, err := CreateOrRecover(ctx, b, path, original); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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

	raw, err := b.Get(ctx, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stored, err := UnmarshalCredentials(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stored.PrivateKey != rotated.PrivateKey {
		t.Error("expected the store to hold the rotated keypair")
	}
}

// SC-010 / Edge case: a Purge racing against another CreateOrRecover call
// that already purged and recreated a moment earlier is never an error by
// itself; the following Create decides the outcome.
func TestCreateOrRecover_PurgeRaceRecoversWinner(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	original, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, _, err := CreateOrRecover(ctx, b, path, original); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := b.Delete(ctx, path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	winner, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Simulate another caller's CreateOrRecover purging and recreating the
	// path between this call's own Purge and its own follow-up Create: the
	// hook fires on this call's second Create attempt (the one after Purge),
	// injects the racing caller's write directly, and reports ErrAlreadyExists
	// to mirror what this call's own Create would observe.
	createCalls := 0
	b.OnCreate = func(p Path) error {
		createCalls++
		if createCalls != 2 {
			return nil
		}
		b.OnCreate = nil
		data, merr := MarshalCredentials(winner)
		if merr != nil {
			t.Fatalf("unexpected error: %v", merr)
		}
		if err := b.Create(ctx, p, data); err != nil {
			t.Fatalf("unexpected error simulating race: %v", err)
		}
		return ErrAlreadyExists
	}

	fresh, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stored, existed, err := CreateOrRecover(ctx, b, path, fresh)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !existed {
		t.Error("expected existed=true — this call's own Create should have lost the race")
	}
	if stored.PrivateKey != winner.PrivateKey {
		t.Error("expected to recover the racing caller's winning keypair")
	}
}

// Edge case: a second ErrPendingDeletion after Purge (an extremely unlikely
// double-race) escalates as a system error rather than looping.
func TestCreateOrRecover_DoublePendingDeletionIsSystemError(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	original, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, _, err := CreateOrRecover(ctx, b, path, original); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := b.Delete(ctx, path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b.OnCreate = func(Path) error { return ErrPendingDeletion }

	fresh, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, _, err = CreateOrRecover(ctx, b, path, fresh)
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.IsUserError(err) {
		t.Error("expected a system error, not a user error")
	}
	if !stderrors.Is(err, ErrPendingDeletion) {
		t.Errorf("expected the wrap to preserve errors.Is(err, ErrPendingDeletion), got %v", err)
	}
}

// Recovering an already-existing secret that fails to unmarshal (malformed
// stored JSON) is a system error.
func TestCreateOrRecover_UnmarshalFailureAfterAlreadyExistsIsError(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	if err := b.Create(ctx, path, []byte("not json")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fresh, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, _, err := CreateOrRecover(ctx, b, path, fresh); err == nil {
		t.Fatal("expected error")
	}
}

// System-error wrapping: a Purge failure during pending-deletion recovery is
// a system error, not a raw sentinel.
func TestCreateOrRecover_PurgeFailureIsSystemError(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	original, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, _, err := CreateOrRecover(ctx, b, path, original); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := b.Delete(ctx, path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b.OnPurge = func(Path) error { return ErrUnavailable }
	fresh, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, _, err = CreateOrRecover(ctx, b, path, fresh)
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.IsUserError(err) {
		t.Error("expected a system error, not a user error")
	}
	if !stderrors.Is(err, ErrUnavailable) {
		t.Errorf("expected the wrap to preserve errors.Is(err, ErrUnavailable), got %v", err)
	}
}

// System-error wrapping: a second Create failure after a successful Purge is
// a system error, not a raw sentinel.
func TestCreateOrRecover_CreateAfterPurgeFailureIsSystemError(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	original, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, _, err := CreateOrRecover(ctx, b, path, original); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := b.Delete(ctx, path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	createCalls := 0
	b.OnCreate = func(Path) error {
		createCalls++
		if createCalls == 2 {
			return ErrUnavailable
		}
		return nil
	}

	fresh, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, _, err = CreateOrRecover(ctx, b, path, fresh)
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.IsUserError(err) {
		t.Error("expected a system error, not a user error")
	}
	if !stderrors.Is(err, ErrUnavailable) {
		t.Errorf("expected the wrap to preserve errors.Is(err, ErrUnavailable), got %v", err)
	}
}

// System-error wrapping: a Get failure while recovering an already-existing
// secret is a system error, not a raw sentinel.
func TestCreateOrRecover_GetFailureAfterAlreadyExistsIsSystemError(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	first, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, _, err := CreateOrRecover(ctx, b, path, first); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b.OnGet = func(Path) error { return ErrUnavailable }
	second, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, _, err = CreateOrRecover(ctx, b, path, second)
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.IsUserError(err) {
		t.Error("expected a system error, not a user error")
	}
	if !stderrors.Is(err, ErrUnavailable) {
		t.Errorf("expected the wrap to preserve errors.Is(err, ErrUnavailable), got %v", err)
	}
}
