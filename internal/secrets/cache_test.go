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
// underlying Backend.
func TestCachedBackend_Get_ServesWithinTTL(t *testing.T) {
	ctx := t.Context()
	fake := NewFakeBackend()
	path := testPath(t)
	if err := fake.Create(ctx, path, "value"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c := NewCachedBackend(fake, time.Hour)
	if _, err := c.Get(ctx, path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fake.OnGet = func(Path) error { return ErrUnavailable }
	got, err := c.Get(ctx, path)
	if err != nil {
		t.Fatalf("expected cached Get to succeed without touching the backend, got %v", err)
	}
	if got != "value" {
		t.Errorf("got %q, want %q", got, "value")
	}
}

// SC-013: CachedBackend never caches an ErrNotFound result — two misses both
// reach the backend.
func TestCachedBackend_Get_NeverCachesNotFound(t *testing.T) {
	ctx := t.Context()
	fake := NewFakeBackend()
	path := testPath(t)

	calls := 0
	fake.OnGet = func(Path) error { calls++; return nil }

	c := NewCachedBackend(fake, time.Hour)
	if _, err := c.Get(ctx, path); !stderrors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
	if _, err := c.Get(ctx, path); !stderrors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 backend calls, got %d", calls)
	}
}

// SC-014: Create/Update/Delete invalidate a path's cache entry on success, so
// the next Get re-fetches rather than serving a stale value.
func TestCachedBackend_InvalidatesOnWrite(t *testing.T) {
	newCache := func(t *testing.T) (*CachedBackend, *FakeBackend, Path) {
		t.Helper()
		fake := NewFakeBackend()
		path := testPath(t)
		return NewCachedBackend(fake, time.Hour), fake, path
	}

	t.Run("Create", func(t *testing.T) {
		ctx := t.Context()
		c, _, path := newCache(t)
		if _, err := c.Get(ctx, path); !stderrors.Is(err, ErrNotFound) {
			t.Fatalf("got %v, want ErrNotFound", err)
		}
		if err := c.Create(ctx, path, "value"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got, err := c.Get(ctx, path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "value" {
			t.Errorf("got %q, want %q (stale ErrNotFound must not have been served)", got, "value")
		}
	})

	t.Run("Update", func(t *testing.T) {
		ctx := t.Context()
		c, _, path := newCache(t)
		if err := c.Create(ctx, path, "original"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := c.Get(ctx, path); err != nil { // warm the cache
			t.Fatalf("unexpected error: %v", err)
		}
		if err := c.Update(ctx, path, "updated"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		got, err := c.Get(ctx, path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "updated" {
			t.Errorf("got %q, want %q (stale cached value must not have been served)", got, "updated")
		}
	})

	t.Run("Delete", func(t *testing.T) {
		ctx := t.Context()
		c, _, path := newCache(t)
		if err := c.Create(ctx, path, "value"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := c.Get(ctx, path); err != nil { // warm the cache
			t.Fatalf("unexpected error: %v", err)
		}
		if err := c.Delete(ctx, path); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := c.Get(ctx, path); !stderrors.Is(err, ErrNotFound) {
			t.Errorf("got %v, want ErrNotFound (stale cached value must not have been served)", err)
		}
	})
}

// SC-014: Invalidate clears a path's cache entry directly, without touching
// the underlying Backend itself — only the next Get does.
func TestInvalidate_ClearsEntryDirectly(t *testing.T) {
	ctx := t.Context()
	fake := NewFakeBackend()
	path := testPath(t)
	if err := fake.Create(ctx, path, "original"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c := NewCachedBackend(fake, time.Hour)
	if _, err := c.Get(ctx, path); err != nil { // warm the cache
		t.Fatalf("unexpected error: %v", err)
	}

	getCallsDuringInvalidate := 0
	fake.OnGet = func(Path) error { getCallsDuringInvalidate++; return nil }
	c.Invalidate(path)
	if getCallsDuringInvalidate != 0 {
		t.Errorf("Invalidate itself must not touch the backend, got %d Get calls", getCallsDuringInvalidate)
	}

	if err := fake.Update(ctx, path, "updated"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := c.Get(ctx, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "updated" {
		t.Errorf("got %q, want %q (Get after Invalidate must re-fetch)", got, "updated")
	}
}

// Edge case: an expired cache entry is a plain miss on the next Get — lazy
// eviction, no background goroutine.
func TestCachedBackend_Get_ExpiredEntryRefetches(t *testing.T) {
	ctx := t.Context()
	fake := NewFakeBackend()
	path := testPath(t)
	if err := fake.Create(ctx, path, "value"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := 0
	fake.OnGet = func(Path) error { calls++; return nil }

	c := NewCachedBackend(fake, 5*time.Millisecond)
	if _, err := c.Get(ctx, path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := c.Get(ctx, path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 backend calls after expiry, got %d", calls)
	}
}

// Edge case: while the underlying store is unavailable but a cached entry is
// still within its TTL, Get serves the cached value without calling the
// underlying Backend — an accepted trade-off, not a defect.
func TestCachedBackend_Get_ServesStaleDuringOutage(t *testing.T) {
	ctx := t.Context()
	fake := NewFakeBackend()
	path := testPath(t)
	if err := fake.Create(ctx, path, "value"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c := NewCachedBackend(fake, time.Hour)
	if _, err := c.Get(ctx, path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fake.OnGet = func(Path) error { return ErrUnavailable }
	got, err := c.Get(ctx, path)
	if err != nil {
		t.Fatalf("expected cached value to be served during outage, got %v", err)
	}
	if got != "value" {
		t.Errorf("got %q, want %q", got, "value")
	}
}

// CachedBackend.Create/Update/Delete propagate the underlying Backend's error
// without invalidating anything.
func TestCachedBackend_WriteMethods_PropagateBackendError(t *testing.T) {
	ctx := t.Context()
	fake := NewFakeBackend()
	path := testPath(t)
	c := NewCachedBackend(fake, time.Hour)

	fake.OnCreate = func(Path) error { return ErrUnavailable }
	if err := c.Create(ctx, path, "v"); !stderrors.Is(err, ErrUnavailable) {
		t.Errorf("Create: got %v, want ErrUnavailable", err)
	}
	fake.OnCreate = nil

	fake.OnUpdate = func(Path) error { return ErrUnavailable }
	if err := c.Update(ctx, path, "v"); !stderrors.Is(err, ErrUnavailable) {
		t.Errorf("Update: got %v, want ErrUnavailable", err)
	}
	fake.OnUpdate = nil

	fake.OnDelete = func(Path) error { return ErrUnavailable }
	if err := c.Delete(ctx, path); !stderrors.Is(err, ErrUnavailable) {
		t.Errorf("Delete: got %v, want ErrUnavailable", err)
	}
}

// Concurrency smoke test: Get/Create/Invalidate from multiple goroutines on a
// handful of paths must not race. Run with -race to be meaningful.
func TestCachedBackend_ConcurrentAccess_NoRace(t *testing.T) {
	ctx := t.Context()
	fake := NewFakeBackend()
	c := NewCachedBackend(fake, 10*time.Millisecond)

	paths := make([]Path, 4)
	for i := range paths {
		p, err := NewTenantPath("my_org", "finance", "analytics-team-eu")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		paths[i] = p
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := paths[i%len(paths)]
			_ = c.Create(ctx, p, "value")
			_, _ = c.Get(ctx, p)
			c.Invalidate(p)
			_, _ = c.Get(ctx, p)
		}(i)
	}
	wg.Wait()
}
