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
)

// errStoreFault is the error every test injects through FakeBackend's hooks. It
// stands for any store-level fault; the fake propagates a hook's error
// unchanged, so a test asserts on this value rather than on anything the
// package exports.
var errStoreFault = stderrors.New("store fault")

func testPath(t *testing.T) Path {
	t.Helper()
	p, err := NewTenantPath("my_org", "finance", "analytics-team-eu")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return p
}

// SC-015: A failing OnGet hook short-circuits before any state mutation.
func TestFakeBackend_HookShortCircuitsBeforeMutation_Get(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)
	b.OnGet = func(Path) error { return errStoreFault }

	if _, _, err := b.Get(ctx, path); !stderrors.Is(err, errStoreFault) {
		t.Fatalf("got %v, want errStoreFault", err)
	}
}

// SC-015: A failing OnCreate hook short-circuits before any state mutation —
// nothing gets stored, so a subsequent unhooked Get still misses.
func TestFakeBackend_HookShortCircuitsBeforeMutation_Create(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)
	b.OnCreate = func(Path) error { return errStoreFault }

	if err := b.Create(ctx, path, "value"); !stderrors.Is(err, errStoreFault) {
		t.Fatalf("got %v, want errStoreFault", err)
	}

	b.OnCreate = nil
	if _, _, err := b.Get(ctx, path); err == nil {
		t.Fatal("expected nothing stored after hook short-circuit, got a value")
	}
}

// SC-015: A failing OnUpdate hook short-circuits before any state mutation —
// the previously stored value survives untouched.
func TestFakeBackend_HookShortCircuitsBeforeMutation_Update(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)
	if err := b.Create(ctx, path, "original"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b.OnUpdate = func(Path) error { return errStoreFault }
	if err := b.Update(ctx, path, "new"); !stderrors.Is(err, errStoreFault) {
		t.Fatalf("got %v, want errStoreFault", err)
	}

	b.OnUpdate = nil
	got, _, err := b.Get(ctx, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "original" {
		t.Errorf("got %q, want %q (update should not have applied)", got, "original")
	}
}

// SC-015: A failing OnDelete hook short-circuits before any state mutation.
func TestFakeBackend_HookShortCircuitsBeforeMutation_Delete(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)
	if err := b.Create(ctx, path, "value"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b.OnDelete = func(Path) error { return errStoreFault }
	if _, err := b.Delete(ctx, path); !stderrors.Is(err, errStoreFault) {
		t.Fatalf("got %v, want errStoreFault", err)
	}

	b.OnDelete = nil
	if _, _, err := b.Get(ctx, path); err != nil {
		t.Fatalf("expected entry to remain after hook short-circuit, got %v", err)
	}
}

// SC-016a: Get returns the timestamp Create/Update most recently recorded,
// taken from Clock — which defaults to something close to time.Now and is
// overridable for a deterministic RotatedAt.
func TestFakeBackend_Clock(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	before := time.Now()
	if err := b.Create(ctx, path, "original"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, modifiedAt, err := b.Get(ctx, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if modifiedAt.Before(before) || modifiedAt.After(time.Now()) {
		t.Errorf("default Clock modifiedAt = %v, want between %v and now", modifiedAt, before)
	}

	fixed := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	b.Clock = func() time.Time { return fixed }
	if err := b.Update(ctx, path, "updated"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, modifiedAt, err = b.Get(ctx, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !modifiedAt.Equal(fixed) {
		t.Errorf("got %v, want %v", modifiedAt, fixed)
	}
}

// SC-016: Delete removes the entry outright, so a following Get fails as it
// would on a path nothing was ever stored at and a following Create on the same
// path succeeds.
func TestFakeBackend_DeleteRemovesOutright(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	if err := b.Create(ctx, path, "original"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	restorableUntil, err := b.Delete(ctx, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Nothing is restorable, so there is no deadline to report.
	if !restorableUntil.IsZero() {
		t.Errorf("restorableUntil = %v, want the zero time with no RecoveryWindow set", restorableUntil)
	}

	if _, _, err := b.Get(ctx, path); err == nil || !strings.Contains(err.Error(), "no secret stored") {
		t.Errorf("Get after Delete: got %v, want an error naming the path as not stored", err)
	}
	if err := b.Create(ctx, path, "new"); err != nil {
		t.Errorf("Create after Delete: got %v, want nil", err)
	}
}

// SC-023: with a RecoveryWindow set, Delete schedules the removal instead of performing it:
// it reports the deadline from Clock, and the path stays occupied until then — unreadable,
// un-updatable, and not reusable by Create. This is the blockade the invariant bounds.
func TestFakeBackend_DeleteSchedulesWithinRecoveryWindow(t *testing.T) {
	ctx := t.Context()
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	b := NewFakeBackend()
	b.Clock = func() time.Time { return now }
	b.RecoveryWindow = 30 * 24 * time.Hour
	path := testPath(t)

	if err := b.Create(ctx, path, "original"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	restorableUntil, err := b.Delete(ctx, path)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if want := now.Add(30 * 24 * time.Hour); !restorableUntil.Equal(want) {
		t.Errorf("restorableUntil = %v, want %v", restorableUntil, want)
	}

	if _, _, err := b.Get(ctx, path); err == nil || !strings.Contains(err.Error(), "scheduled for deletion") {
		t.Errorf("Get on a pending path: got %v, want an error naming it as scheduled for deletion", err)
	}
	if err := b.Update(ctx, path, "new"); err == nil || !strings.Contains(err.Error(), "scheduled for deletion") {
		t.Errorf("Update on a pending path: got %v, want an error naming it as scheduled for deletion", err)
	}
	if err := b.Create(ctx, path, "new"); err == nil || !strings.Contains(err.Error(), "cannot be reused") {
		t.Errorf("Create on a pending path: got %v, want an error naming the path as unreusable", err)
	}
}

// SC-023: a second Delete of an already-pending path neither fails nor extends the deadline —
// the store scheduled the removal once and does not restart its clock.
func TestFakeBackend_DeleteOnPendingPathKeepsDeadline(t *testing.T) {
	ctx := t.Context()
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	b := NewFakeBackend()
	b.Clock = func() time.Time { return now }
	b.RecoveryWindow = 7 * 24 * time.Hour
	path := testPath(t)

	if err := b.Create(ctx, path, "original"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	first, err := b.Delete(ctx, path)
	if err != nil {
		t.Fatalf("first Delete: %v", err)
	}

	b.Clock = func() time.Time { return now.Add(48 * time.Hour) }
	second, err := b.Delete(ctx, path)
	if err != nil {
		t.Fatalf("second Delete: %v", err)
	}
	if !second.Equal(first) {
		t.Errorf("second Delete moved the deadline from %v to %v", first, second)
	}
}

// SC-023: Delete on an absent path stays a no-op success in pending mode too, and schedules
// nothing that a later Create would trip over.
func TestFakeBackend_DeleteOnAbsentPathIsNoopInPendingMode(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	b.RecoveryWindow = 30 * 24 * time.Hour
	path := testPath(t)

	restorableUntil, err := b.Delete(ctx, path)
	if err != nil {
		t.Fatalf("got %v, want nil", err)
	}
	if !restorableUntil.IsZero() {
		t.Errorf("restorableUntil = %v, want the zero time — nothing was scheduled", restorableUntil)
	}
	if err := b.Create(ctx, path, "value"); err != nil {
		t.Errorf("Create after a no-op Delete: got %v, want nil", err)
	}
}

// SC-024: Restore cancels a pending deletion, returning the path to exactly the state it was
// in before — the store-side half of the manual repair 012 documents.
func TestFakeBackend_RestoreCancelsPendingDeletion(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	b.RecoveryWindow = 30 * 24 * time.Hour
	path := testPath(t)

	if err := b.Create(ctx, path, "original"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := b.Delete(ctx, path); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := b.Restore(path); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	got, _, err := b.Get(ctx, path)
	if err != nil {
		t.Fatalf("Get after Restore: %v", err)
	}
	if got != "original" {
		t.Errorf("got %q, want %q — Restore must bring back the stored value", got, "original")
	}
	if err := b.Update(ctx, path, "new"); err != nil {
		t.Errorf("Update after Restore: got %v, want nil", err)
	}
}

// SC-024: Restore fails when nothing at the path is scheduled for deletion, whether the path
// is empty or holds a live value.
func TestFakeBackend_RestoreWithoutPendingDeletionFails(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	b.RecoveryWindow = 30 * 24 * time.Hour
	path := testPath(t)

	if err := b.Restore(path); err == nil || !strings.Contains(err.Error(), "no secret scheduled for deletion") {
		t.Errorf("Restore on an absent path: got %v, want an error naming nothing as scheduled", err)
	}

	if err := b.Create(ctx, path, "original"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := b.Restore(path); err == nil || !strings.Contains(err.Error(), "no secret scheduled for deletion") {
		t.Errorf("Restore on a live path: got %v, want an error naming nothing as scheduled", err)
	}
}

// SC-016: Delete on a path nothing was ever stored at is a no-op success.
func TestFakeBackend_DeleteOnAbsentPathIsNoop(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	if _, err := b.Delete(ctx, path); err != nil {
		t.Errorf("got %v, want nil", err)
	}
}

// Update on a deleted path is treated as not-there.
func TestFakeBackend_UpdateOnDeletedPathFails(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	if err := b.Create(ctx, path, "original"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := b.Delete(ctx, path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := b.Update(ctx, path, "new"); err == nil || !strings.Contains(err.Error(), "no secret stored") {
		t.Errorf("got %v, want an error naming the path as not stored", err)
	}
}

// Update on a path nothing was ever stored at fails; Update never creates.
func TestFakeBackend_UpdateOnAbsentPathFails(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	if err := b.Update(ctx, path, "new"); err == nil || !strings.Contains(err.Error(), "no secret stored") {
		t.Errorf("got %v, want an error naming the path as not stored", err)
	}
}
