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
	"sync"
	"time"
)

type cachedSecret[T any] struct {
	value     T
	expiresAt time.Time
}

func (c *cachedSecret[T]) isExpired() bool {
	return time.Now().After(c.expiresAt)
}

// credentialCache is a generic TTL cache with lazy eviction and RWMutex for thread safety.
// Zero allocations on cache hit path.
type credentialCache[T any] struct {
	mu      sync.RWMutex
	entries map[string]*cachedSecret[T]
	ttl     time.Duration
}

func newCredentialCache[T any](ttl time.Duration) *credentialCache[T] {
	return &credentialCache[T]{
		entries: make(map[string]*cachedSecret[T]),
		ttl:     ttl,
	}
}

// get returns the cached value for the key if present and not expired.
// Expired entries are evicted lazily on access.
func (c *credentialCache[T]) get(key string) (T, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok {
		var zero T
		return zero, false
	}
	if entry.isExpired() {
		c.delete(key)
		var zero T
		return zero, false
	}
	return entry.value, true
}

// set stores value under key with a new TTL expiry.
func (c *credentialCache[T]) set(key string, value T) {
	c.mu.Lock()
	c.entries[key] = &cachedSecret[T]{
		value:     value,
		expiresAt: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()
}

// delete removes the entry for key. Idempotent.
func (c *credentialCache[T]) delete(key string) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

// clear removes all entries.
func (c *credentialCache[T]) clear() {
	c.mu.Lock()
	c.entries = make(map[string]*cachedSecret[T])
	c.mu.Unlock()
}
