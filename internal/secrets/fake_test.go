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
	"time"

	internalerrors "github.com/allianz/yukimi/internal/errors"
)

// errStoreFault is the error every test injects through FakeKeyStore's hooks. It
// stands for any store-level fault; the fake propagates a hook's error
// unchanged, so a test asserts on this value rather than on anything the
// package exports.
var errStoreFault = stderrors.New("store fault")

func testIdentifier(t *testing.T) Identifier {
	t.Helper()
	id, err := NewTenantIdentifier("my_org", "finance", "analytics-team-eu")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return id
}

// SC-015: A failing OnGet hook short-circuits before any state mutation.
func TestFakeKeyStore_HookShortCircuitsBeforeMutation_Get(t *testing.T) {
	ctx := t.Context()
	b := NewFakeKeyStore()
	id := testIdentifier(t)
	b.OnGet = func(Identifier) error { return errStoreFault }

	if _, _, err := b.Get(ctx, id); !stderrors.Is(err, errStoreFault) {
		t.Fatalf("got %v, want errStoreFault", err)
	}
}

// SC-015: A failing OnCreate hook short-circuits before any state mutation —
// nothing gets stored, so a subsequent unhooked Get still misses.
func TestFakeKeyStore_HookShortCircuitsBeforeMutation_Create(t *testing.T) {
	ctx := t.Context()
	b := NewFakeKeyStore()
	id := testIdentifier(t)
	b.OnCreate = func(Identifier) error { return errStoreFault }

	if err := b.Create(ctx, id, "value"); !stderrors.Is(err, errStoreFault) {
		t.Fatalf("got %v, want errStoreFault", err)
	}

	b.OnCreate = nil
	if _, _, err := b.Get(ctx, id); err == nil {
		t.Fatal("expected nothing stored after hook short-circuit, got a value")
	}
}

// SC-015: A failing OnUpdate hook short-circuits before any state mutation —
// the previously stored value survives untouched.
func TestFakeKeyStore_HookShortCircuitsBeforeMutation_Update(t *testing.T) {
	ctx := t.Context()
	b := NewFakeKeyStore()
	id := testIdentifier(t)
	if err := b.Create(ctx, id, "original"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b.OnUpdate = func(Identifier) error { return errStoreFault }
	if err := b.Update(ctx, id, "new"); !stderrors.Is(err, errStoreFault) {
		t.Fatalf("got %v, want errStoreFault", err)
	}

	b.OnUpdate = nil
	got, _, err := b.Get(ctx, id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "original" {
		t.Errorf("got %q, want %q (update should not have applied)", got, "original")
	}
}

// SC-015: A failing OnDelete hook short-circuits before any state mutation.
func TestFakeKeyStore_HookShortCircuitsBeforeMutation_Delete(t *testing.T) {
	ctx := t.Context()
	b := NewFakeKeyStore()
	id := testIdentifier(t)
	if err := b.Create(ctx, id, "value"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b.OnDelete = func(Identifier) error { return errStoreFault }
	if err := b.Delete(ctx, id); !stderrors.Is(err, errStoreFault) {
		t.Fatalf("got %v, want errStoreFault", err)
	}

	b.OnDelete = nil
	if _, _, err := b.Get(ctx, id); err != nil {
		t.Fatalf("expected entry to remain after hook short-circuit, got %v", err)
	}
}

// SC-016a: Get returns the timestamp Create/Update most recently recorded,
// taken from Clock — which defaults to something close to time.Now and is
// overridable for a deterministic RotatedAt.
func TestFakeKeyStore_Clock(t *testing.T) {
	ctx := t.Context()
	b := NewFakeKeyStore()
	id := testIdentifier(t)

	before := time.Now()
	if err := b.Create(ctx, id, "original"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, modifiedAt, err := b.Get(ctx, id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if modifiedAt.Before(before) || modifiedAt.After(time.Now()) {
		t.Errorf("default Clock modifiedAt = %v, want between %v and now", modifiedAt, before)
	}

	fixed := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	b.Clock = func() time.Time { return fixed }
	if err := b.Update(ctx, id, "updated"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, modifiedAt, err = b.Get(ctx, id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !modifiedAt.Equal(fixed) {
		t.Errorf("got %v, want %v", modifiedAt, fixed)
	}
}

// SC-016: Delete removes the entry outright, so a following Get fails as it
// would on an identifier nothing was ever stored at and a following Create on
// the same identifier succeeds.
func TestFakeKeyStore_DeleteRemovesOutright(t *testing.T) {
	ctx := t.Context()
	b := NewFakeKeyStore()
	id := testIdentifier(t)

	if err := b.Create(ctx, id, "original"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := b.Delete(ctx, id); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, _, err := b.Get(ctx, id); err == nil || !strings.Contains(err.Error(), "no secret stored") {
		t.Errorf("Get after Delete: got %v, want an error naming the identifier as not stored", err)
	}
	if err := b.Create(ctx, id, "new"); err != nil {
		t.Errorf("Create after Delete: got %v, want nil", err)
	}
}

// SC-021: with SchedulesDeletion set, Delete schedules the removal instead of performing it,
// and the identifier stays occupied — unreadable, un-updatable, and not reusable by Create. This
// is the blockade the invariant bounds.
func TestFakeKeyStore_DeleteSchedulesRemoval(t *testing.T) {
	ctx := t.Context()
	b := NewFakeKeyStore()
	b.SchedulesDeletion = true
	id := testIdentifier(t)

	if err := b.Create(ctx, id, "original"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := b.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, _, err := b.Get(ctx, id); err == nil || !strings.Contains(err.Error(), "scheduled for deletion") {
		t.Errorf("Get on a pending identifier: got %v, want an error naming it as scheduled for deletion", err)
	}
	if err := b.Update(ctx, id, "new"); err == nil || !strings.Contains(err.Error(), "scheduled for deletion") {
		t.Errorf("Update on a pending identifier: got %v, want an error naming it as scheduled for deletion", err)
	}
	createErr := b.Create(ctx, id, "new")
	if createErr == nil || !strings.Contains(createErr.Error(), "cannot be reused") {
		t.Errorf("Create on a pending identifier: got %v, want an error naming the identifier as unreusable", createErr)
	}
	if !stderrors.Is(createErr, ErrPendingDeletion) {
		t.Errorf("Create on a pending identifier: got %v, want it to wrap ErrPendingDeletion", createErr)
	}
	if internalerrors.IsUserError(createErr) {
		t.Errorf("Create on a pending identifier: got a user error, want an ordinary system error — "+
			"classification is the catching caller's job, not this package's: %v", createErr)
	}
}

// Create on an identifier occupied by a live secret never wraps ErrPendingDeletion — only the
// pending-deletion sub-case does.
func TestFakeKeyStore_CreateOnLiveIdentifierDoesNotWrapErrPendingDeletion(t *testing.T) {
	ctx := t.Context()
	b := NewFakeKeyStore()
	id := testIdentifier(t)

	if err := b.Create(ctx, id, "original"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err := b.Create(ctx, id, "new")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("got %v, want an error naming the identifier as already occupied", err)
	}
	if stderrors.Is(err, ErrPendingDeletion) {
		t.Errorf("got %v, want it to not wrap ErrPendingDeletion", err)
	}
}

// SC-021: a second Delete of an already-pending identifier neither fails nor releases the
// identifier — a retried teardown converges instead of tripping over its own first attempt.
func TestFakeKeyStore_DeleteOnPendingIdentifierIsIdempotent(t *testing.T) {
	ctx := t.Context()
	b := NewFakeKeyStore()
	b.SchedulesDeletion = true
	id := testIdentifier(t)

	if err := b.Create(ctx, id, "original"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := b.Delete(ctx, id); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	if err := b.Delete(ctx, id); err != nil {
		t.Fatalf("second Delete: %v", err)
	}

	// Still blockaded, and still recoverable: the second Delete changed nothing.
	if err := b.Create(ctx, id, "new"); err == nil || !strings.Contains(err.Error(), "cannot be reused") {
		t.Errorf("Create after a second Delete: got %v, want an error naming the identifier as unreusable", err)
	}
	if err := b.Restore(id); err != nil {
		t.Errorf("Restore after a second Delete: got %v, want nil", err)
	}
}

// SC-021: Delete on an absent identifier stays a no-op success in pending mode too, and
// schedules nothing that a later Create would trip over.
func TestFakeKeyStore_DeleteOnAbsentIdentifierIsNoopInPendingMode(t *testing.T) {
	ctx := t.Context()
	b := NewFakeKeyStore()
	b.SchedulesDeletion = true
	id := testIdentifier(t)

	if err := b.Delete(ctx, id); err != nil {
		t.Fatalf("got %v, want nil", err)
	}
	if err := b.Create(ctx, id, "value"); err != nil {
		t.Errorf("Create after a no-op Delete: got %v, want nil", err)
	}
}

// SC-024: Restore cancels a pending deletion, returning the identifier to exactly the state it
// was in before — the store-side half of the manual repair 012 documents.
func TestFakeKeyStore_RestoreCancelsPendingDeletion(t *testing.T) {
	ctx := t.Context()
	b := NewFakeKeyStore()
	b.SchedulesDeletion = true
	id := testIdentifier(t)

	if err := b.Create(ctx, id, "original"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := b.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := b.Restore(id); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	got, _, err := b.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after Restore: %v", err)
	}
	if got != "original" {
		t.Errorf("got %q, want %q — Restore must bring back the stored value", got, "original")
	}
	if err := b.Update(ctx, id, "new"); err != nil {
		t.Errorf("Update after Restore: got %v, want nil", err)
	}
}

// SC-024: Restore fails when nothing at the identifier is scheduled for deletion, whether the
// identifier is empty or holds a live value.
func TestFakeKeyStore_RestoreWithoutPendingDeletionFails(t *testing.T) {
	ctx := t.Context()
	b := NewFakeKeyStore()
	b.SchedulesDeletion = true
	id := testIdentifier(t)

	if err := b.Restore(id); err == nil || !strings.Contains(err.Error(), "no secret scheduled for deletion") {
		t.Errorf("Restore on an absent identifier: got %v, want an error naming nothing as scheduled", err)
	}

	if err := b.Create(ctx, id, "original"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := b.Restore(id); err == nil || !strings.Contains(err.Error(), "no secret scheduled for deletion") {
		t.Errorf("Restore on a live identifier: got %v, want an error naming nothing as scheduled", err)
	}
}

// SC-016: Delete on an identifier nothing was ever stored at is a no-op success.
func TestFakeKeyStore_DeleteOnAbsentIdentifierIsNoop(t *testing.T) {
	ctx := t.Context()
	b := NewFakeKeyStore()
	id := testIdentifier(t)

	if err := b.Delete(ctx, id); err != nil {
		t.Errorf("got %v, want nil", err)
	}
}

// Update on a deleted identifier is treated as not-there.
func TestFakeKeyStore_UpdateOnDeletedIdentifierFails(t *testing.T) {
	ctx := t.Context()
	b := NewFakeKeyStore()
	id := testIdentifier(t)

	if err := b.Create(ctx, id, "original"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := b.Delete(ctx, id); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := b.Update(ctx, id, "new"); err == nil || !strings.Contains(err.Error(), "no secret stored") {
		t.Errorf("got %v, want an error naming the identifier as not stored", err)
	}
}

// Update on an identifier nothing was ever stored at fails; Update never creates.
func TestFakeKeyStore_UpdateOnAbsentIdentifierFails(t *testing.T) {
	ctx := t.Context()
	b := NewFakeKeyStore()
	id := testIdentifier(t)

	if err := b.Update(ctx, id, "new"); err == nil || !strings.Contains(err.Error(), "no secret stored") {
		t.Errorf("got %v, want an error naming the identifier as not stored", err)
	}
}
