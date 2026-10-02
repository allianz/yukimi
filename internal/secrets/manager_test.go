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
	"sync"
	"testing"
	"time"
)

// SC-012: Get serves a cached value within ttl without invoking the
// underlying KeyStore.
func TestKeyManager_Get_ServesWithinTTL(t *testing.T) {
	ctx := t.Context()
	fake := NewFakeKeyStore()
	id := testIdentifier(t)
	if err := fake.Create(ctx, id, "value"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c := NewKeyManager(fake, time.Hour)
	if _, _, err := c.get(ctx, id); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fake.OnGet = func(Identifier) error { return errStoreFault }
	got, _, err := c.get(ctx, id)
	if err != nil {
		t.Fatalf("expected cached Get to succeed without touching the store, got %v", err)
	}
	if got != "value" {
		t.Errorf("got %q, want %q", got, "value")
	}
}

// SC-012: a cache hit replays the exact (value, modifiedAt) pair first
// fetched from the store; a miss re-fetches a fresh pair.
func TestKeyManager_Get_ReplaysModifiedAtOnHit(t *testing.T) {
	ctx := t.Context()
	fake := NewFakeKeyStore()
	id := testIdentifier(t)
	fixed := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	fake.Clock = func() time.Time { return fixed }
	if err := fake.Create(ctx, id, "value"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c := NewKeyManager(fake, time.Hour)
	_, modifiedAt, err := c.get(ctx, id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !modifiedAt.Equal(fixed) {
		t.Fatalf("got %v, want %v", modifiedAt, fixed)
	}

	fake.Clock = func() time.Time { return fixed.Add(time.Hour) } // must not affect a cache hit
	_, cachedModifiedAt, err := c.get(ctx, id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cachedModifiedAt.Equal(fixed) {
		t.Errorf("cache hit modifiedAt = %v, want unchanged %v", cachedModifiedAt, fixed)
	}
}

// SC-013: KeyManager never caches a failed Get — two consecutive Gets on an
// identifier nothing is stored at both reach the underlying KeyStore.
func TestKeyManager_Get_NeverCachesAFailedGet(t *testing.T) {
	ctx := t.Context()
	fake := NewFakeKeyStore()
	id := testIdentifier(t)

	calls := 0
	fake.OnGet = func(Identifier) error { calls++; return nil }

	c := NewKeyManager(fake, time.Hour)
	if _, _, err := c.get(ctx, id); err == nil {
		t.Fatal("expected the first Get to fail on an identifier nothing is stored at")
	}
	if _, _, err := c.get(ctx, id); err == nil {
		t.Fatal("expected the second Get to fail on an identifier nothing is stored at")
	}
	if calls != 2 {
		t.Errorf("expected 2 store calls, got %d", calls)
	}
}

// SC-014: create/update/Delete invalidate an identifier's cache entry on
// success, so the next Get re-fetches rather than serving a stale value.
func TestKeyManager_InvalidatesOnWrite(t *testing.T) {
	newCache := func(t *testing.T) (*KeyManager, *FakeKeyStore, Identifier) {
		t.Helper()
		fake := NewFakeKeyStore()
		id := testIdentifier(t)
		return NewKeyManager(fake, time.Hour), fake, id
	}

	t.Run("Create", func(t *testing.T) {
		ctx := t.Context()
		c, _, id := newCache(t)
		if _, _, err := c.get(ctx, id); err == nil {
			t.Fatal("expected a Get on an identifier nothing is stored at to fail")
		}
		if err := c.create(ctx, id, "value"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got, _, err := c.get(ctx, id)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "value" {
			t.Errorf("got %q, want %q (a stale failed lookup must not have been served)", got, "value")
		}
	})

	t.Run("Update", func(t *testing.T) {
		ctx := t.Context()
		c, _, id := newCache(t)
		if err := c.create(ctx, id, "original"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, _, err := c.get(ctx, id); err != nil { // warm the cache
			t.Fatalf("unexpected error: %v", err)
		}
		if err := c.update(ctx, id, "updated"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got, _, err := c.get(ctx, id)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "updated" {
			t.Errorf("got %q, want %q (stale cached value must not have been served)", got, "updated")
		}
	})

	t.Run("Delete", func(t *testing.T) {
		ctx := t.Context()
		c, _, id := newCache(t)
		if err := c.create(ctx, id, "value"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, _, err := c.get(ctx, id); err != nil { // warm the cache
			t.Fatalf("unexpected error: %v", err)
		}
		if err := c.DeleteCredentials(ctx, id); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, _, err := c.get(ctx, id); err == nil {
			t.Error("expected a Get after Delete to fail (stale cached value must not have been served)")
		}
	})
}

// SC-014: Invalidate clears an identifier's cache entry directly, without
// touching the underlying KeyStore itself — only the next Get does.
func TestInvalidate_ClearsEntryDirectly(t *testing.T) {
	ctx := t.Context()
	fake := NewFakeKeyStore()
	id := testIdentifier(t)
	if err := fake.Create(ctx, id, "original"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c := NewKeyManager(fake, time.Hour)
	if _, _, err := c.get(ctx, id); err != nil { // warm the cache
		t.Fatalf("unexpected error: %v", err)
	}

	getCallsDuringInvalidate := 0
	fake.OnGet = func(Identifier) error { getCallsDuringInvalidate++; return nil }
	c.Invalidate(id)
	if getCallsDuringInvalidate != 0 {
		t.Errorf("Invalidate itself must not touch the store, got %d Get calls", getCallsDuringInvalidate)
	}

	if err := fake.Update(ctx, id, "updated"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _, err := c.get(ctx, id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "updated" {
		t.Errorf("got %q, want %q (Get after Invalidate must re-fetch)", got, "updated")
	}
}

// Edge case: an expired cache entry is a plain miss on the next Get — lazy
// eviction, no background goroutine.
func TestKeyManager_Get_ExpiredEntryRefetches(t *testing.T) {
	ctx := t.Context()
	fake := NewFakeKeyStore()
	id := testIdentifier(t)
	if err := fake.Create(ctx, id, "value"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := 0
	fake.OnGet = func(Identifier) error { calls++; return nil }

	c := NewKeyManager(fake, 5*time.Millisecond)
	if _, _, err := c.get(ctx, id); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, _, err := c.get(ctx, id); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 store calls after expiry, got %d", calls)
	}
}

// Edge case: while the underlying store is unavailable but a cached entry is
// still within its TTL, Get serves the cached value without calling the
// underlying KeyStore — an accepted trade-off, not a defect.
func TestKeyManager_Get_ServesStaleDuringOutage(t *testing.T) {
	ctx := t.Context()
	fake := NewFakeKeyStore()
	id := testIdentifier(t)
	if err := fake.Create(ctx, id, "value"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c := NewKeyManager(fake, time.Hour)
	if _, _, err := c.get(ctx, id); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fake.OnGet = func(Identifier) error { return errStoreFault }
	got, _, err := c.get(ctx, id)
	if err != nil {
		t.Fatalf("expected cached value to be served during outage, got %v", err)
	}
	if got != "value" {
		t.Errorf("got %q, want %q", got, "value")
	}
}

// KeyManager's create/update/Delete propagate the underlying KeyStore's error
// without invalidating anything.
func TestKeyManager_WriteMethods_PropagateKeyStoreError(t *testing.T) {
	ctx := t.Context()
	fake := NewFakeKeyStore()
	id := testIdentifier(t)
	c := NewKeyManager(fake, time.Hour)

	fake.OnCreate = func(Identifier) error { return errStoreFault }
	if err := c.create(ctx, id, "v"); !stderrors.Is(err, errStoreFault) {
		t.Errorf("Create: got %v, want errStoreFault", err)
	}
	fake.OnCreate = nil

	fake.OnUpdate = func(Identifier) error { return errStoreFault }
	if err := c.update(ctx, id, "v"); !stderrors.Is(err, errStoreFault) {
		t.Errorf("Update: got %v, want errStoreFault", err)
	}
	fake.OnUpdate = nil

	fake.OnDelete = func(Identifier) error { return errStoreFault }
	if err := c.DeleteCredentials(ctx, id); !stderrors.Is(err, errStoreFault) {
		t.Errorf("Delete: got %v, want errStoreFault", err)
	}
}

// CreateCredentials generates a fresh keypair, stores it, and returns it; a
// subsequent GetCredentials reads back the same values.
func TestKeyManager_CreateCredentials_StoresAndReturnsGeneratedCredentials(t *testing.T) {
	ctx := t.Context()
	c := NewKeyManager(NewFakeKeyStore(), time.Hour)
	id := testIdentifier(t)

	created, err := c.CreateCredentials(ctx, id, "platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created.Username != "platform" {
		t.Errorf("created.Username = %q, want %q", created.Username, "platform")
	}
	if created.PublicKey == "" || created.PrivateKey == "" {
		t.Error("expected a generated public and private key")
	}

	got, err := c.GetCredentials(ctx, id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Username != created.Username || got.PublicKey != created.PublicKey || got.PrivateKey != created.PrivateKey {
		t.Errorf("GetCredentials after CreateCredentials = %+v, want %+v", got, created)
	}
}

// CreateCredentials propagates ErrPendingDeletion from the underlying Create
// call, so a caller can still match it via errors.Is.
func TestKeyManager_CreateCredentials_PropagatesErrPendingDeletion(t *testing.T) {
	ctx := t.Context()
	fake := NewFakeKeyStore()
	fake.SchedulesDeletion = true
	id := testIdentifier(t)
	if err := fake.Create(ctx, id, "value"); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if err := fake.Delete(ctx, id); err != nil {
		t.Fatalf("scheduling deletion: %v", err)
	}

	c := NewKeyManager(fake, time.Hour)
	if _, err := c.CreateCredentials(ctx, id, "platform"); !stderrors.Is(err, ErrPendingDeletion) {
		t.Errorf("got %v, want ErrPendingDeletion", err)
	}
}

// UpdateCredentials marshals and stores creds, invalidating the cache so a
// following GetCredentials observes the new value rather than a stale one.
func TestKeyManager_UpdateCredentials_StoresAndInvalidatesCache(t *testing.T) {
	ctx := t.Context()
	c := NewKeyManager(NewFakeKeyStore(), time.Hour)
	id := testIdentifier(t)

	original, err := c.CreateCredentials(ctx, id, "platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := c.GetCredentials(ctx, id); err != nil { // warm the cache
		t.Fatalf("unexpected error: %v", err)
	}

	fresh, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fresh.PrivateKey == original.PrivateKey {
		t.Fatal("expected a freshly generated keypair distinct from the original")
	}
	if err := c.UpdateCredentials(ctx, id, fresh); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := c.GetCredentials(ctx, id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.PrivateKey != fresh.PrivateKey {
		t.Errorf("GetCredentials after UpdateCredentials returned the stale value, not the updated one")
	}
}

// GetCredentials propagates the underlying Get's error rather than hiding it
// behind an unmarshal failure.
func TestKeyManager_GetCredentials_PropagatesGetError(t *testing.T) {
	ctx := t.Context()
	c := NewKeyManager(NewFakeKeyStore(), time.Hour)
	id := testIdentifier(t)

	if _, err := c.GetCredentials(ctx, id); err == nil {
		t.Fatal("expected an error for an identifier nothing is stored at")
	}
}

// GetCredentials propagates a malformed stored value's unmarshal failure.
func TestKeyManager_GetCredentials_PropagatesUnmarshalError(t *testing.T) {
	ctx := t.Context()
	fake := NewFakeKeyStore()
	id := testIdentifier(t)
	if err := fake.Create(ctx, id, "not valid json"); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	c := NewKeyManager(fake, time.Hour)
	if _, err := c.GetCredentials(ctx, id); err == nil {
		t.Fatal("expected an error for a credential that does not unmarshal")
	}
}

// Concurrency smoke test: Get/Create/Invalidate from multiple goroutines on a
// handful of identifiers must not race. Run with -race to be meaningful.
func TestKeyManager_ConcurrentAccess_NoRace(t *testing.T) {
	ctx := t.Context()
	fake := NewFakeKeyStore()
	c := NewKeyManager(fake, 10*time.Millisecond)

	ids := make([]Identifier, 4)
	for i := range ids {
		id, err := NewTenantIdentifier("my_org", "finance", "analytics-team-eu")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		ids[i] = id
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := ids[i%len(ids)]
			_ = c.create(ctx, id, "value")
			_, _, _ = c.get(ctx, id)
			c.Invalidate(id)
			_, _, _ = c.get(ctx, id)
		}(i)
	}
	wg.Wait()
}
