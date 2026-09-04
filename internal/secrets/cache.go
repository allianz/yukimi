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
	"sync"
	"time"
)

// cacheEntry holds a cached value, the time the backend last wrote it, and
// the time at which the entry stops being served.
type cacheEntry struct {
	value      string
	modifiedAt time.Time
	expires    time.Time
}

// CachedBackend wraps a Backend with an in-memory, TTL-based, lazily-evicted
// cache. It implements Backend itself, so callers depend on the interface,
// never on this concrete type.
type CachedBackend struct {
	backend Backend
	ttl     time.Duration

	mu      sync.Mutex
	entries map[Path]cacheEntry
}

var _ Backend = (*CachedBackend)(nil)

// NewCachedBackend wraps b. Every concrete Backend should be wrapped exactly
// once, at construction time in cmd/provider/main.go.
func NewCachedBackend(b Backend, ttl time.Duration) *CachedBackend {
	return &CachedBackend{backend: b, ttl: ttl, entries: make(map[Path]cacheEntry)}
}

func (c *CachedBackend) Get(ctx context.Context, path Path) (string, time.Time, error) {
	c.mu.Lock()
	entry, ok := c.entries[path]
	c.mu.Unlock()
	if ok && time.Now().Before(entry.expires) {
		return entry.value, entry.modifiedAt, nil
	}

	value, modifiedAt, err := c.backend.Get(ctx, path)
	if err != nil {
		return "", time.Time{}, err // never cache a failure, not even a missing path
	}

	c.mu.Lock()
	c.entries[path] = cacheEntry{value: value, modifiedAt: modifiedAt, expires: time.Now().Add(c.ttl)}
	c.mu.Unlock()
	return value, modifiedAt, nil
}

func (c *CachedBackend) Create(ctx context.Context, path Path, value string) error {
	if err := c.backend.Create(ctx, path, value); err != nil {
		return err
	}
	c.Invalidate(path)
	return nil
}

func (c *CachedBackend) Update(ctx context.Context, path Path, value string) error {
	if err := c.backend.Update(ctx, path, value); err != nil {
		return err
	}
	c.Invalidate(path)
	return nil
}

func (c *CachedBackend) Delete(ctx context.Context, path Path) (time.Time, error) {
	restorableUntil, err := c.backend.Delete(ctx, path)
	if err != nil {
		return time.Time{}, err
	}
	c.Invalidate(path)
	return restorableUntil, nil
}

// Invalidate clears path's cache entry without touching the underlying
// Backend. Exposed for a caller that needs a path forced cold without going
// through Create/Update/Delete.
func (c *CachedBackend) Invalidate(path Path) {
	c.mu.Lock()
	delete(c.entries, path)
	c.mu.Unlock()
}
