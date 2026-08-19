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

	if _, err := b.Get(ctx, path); !stderrors.Is(err, errStoreFault) {
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
	if _, err := b.Get(ctx, path); err == nil {
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
	got, err := b.Get(ctx, path)
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
	if err := b.Delete(ctx, path); !stderrors.Is(err, errStoreFault) {
		t.Fatalf("got %v, want errStoreFault", err)
	}

	b.OnDelete = nil
	if _, err := b.Get(ctx, path); err != nil {
		t.Fatalf("expected entry to remain after hook short-circuit, got %v", err)
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
	if err := b.Delete(ctx, path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := b.Get(ctx, path); err == nil || !strings.Contains(err.Error(), "no secret stored") {
		t.Errorf("Get after Delete: got %v, want an error naming the path as not stored", err)
	}
	if err := b.Create(ctx, path, "new"); err != nil {
		t.Errorf("Create after Delete: got %v, want nil", err)
	}
}

// SC-016: Delete on a path nothing was ever stored at is a no-op success.
func TestFakeBackend_DeleteOnAbsentPathIsNoop(t *testing.T) {
	ctx := t.Context()
	b := NewFakeBackend()
	path := testPath(t)

	if err := b.Delete(ctx, path); err != nil {
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
	if err := b.Delete(ctx, path); err != nil {
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
