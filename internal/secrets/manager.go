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

// KeyManager wraps a KeyStore with an in-memory, TTL-based, lazily-evicted
// cache. Every consumer outside this package holds a *KeyManager, never a
// concrete KeyStore.
type KeyManager struct {
	store KeyStore
	ttl   time.Duration

	mu      sync.Mutex
	entries map[Identifier]cacheEntry
}

// NewKeyManager wraps store. Every concrete KeyStore should be wrapped exactly
// once, at construction time in cmd/provider/main.go.
func NewKeyManager(store KeyStore, ttl time.Duration) *KeyManager {
	return &KeyManager{store: store, ttl: ttl, entries: make(map[Identifier]cacheEntry)}
}

// get returns the raw value stored at id, along with the time the backend
// last wrote it. Only GetCredentials calls this — a caller never reads a raw
// value directly.
func (c *KeyManager) get(ctx context.Context, id Identifier) (string, time.Time, error) {
	c.mu.Lock()
	entry, ok := c.entries[id]
	c.mu.Unlock()
	if ok && time.Now().Before(entry.expires) {
		return entry.value, entry.modifiedAt, nil
	}

	value, modifiedAt, err := c.store.Get(ctx, id)
	if err != nil {
		return "", time.Time{}, err // never cache a failure, not even a missing identifier
	}

	c.mu.Lock()
	c.entries[id] = cacheEntry{value: value, modifiedAt: modifiedAt, expires: time.Now().Add(c.ttl)}
	c.mu.Unlock()
	return value, modifiedAt, nil
}

// create stores value at id create-only (fails if id is already occupied).
// Only CreateCredentials calls this — a caller never stores a raw value
// directly.
func (c *KeyManager) create(ctx context.Context, id Identifier, value string) error {
	if err := c.store.Create(ctx, id, value); err != nil {
		return err
	}
	c.Invalidate(id)
	return nil
}

// update stores value at id update-only (fails if id is absent). Only
// UpdateCredentials calls this — a caller never stores a raw value directly.
func (c *KeyManager) update(ctx context.Context, id Identifier, value string) error {
	if err := c.store.Update(ctx, id, value); err != nil {
		return err
	}
	c.Invalidate(id)
	return nil
}

// CreateCredentials generates a fresh keypair for username, stores it at id,
// and returns the generated credentials.
func (c *KeyManager) CreateCredentials(ctx context.Context, id Identifier, username string) (*Credentials, error) {
	creds, err := NewCredentials(username)
	if err != nil {
		return nil, err
	}
	value, err := marshalCredentials(creds)
	if err != nil {
		return nil, err
	}
	if err := c.create(ctx, id, value); err != nil {
		return nil, err
	}
	return creds, nil
}

// UpdateCredentials marshals creds and stores it at id.
func (c *KeyManager) UpdateCredentials(ctx context.Context, id Identifier, creds *Credentials) error {
	value, err := marshalCredentials(creds)
	if err != nil {
		return err
	}
	return c.update(ctx, id, value)
}

// GetCredentials reads the credential at id and unmarshals it.
func (c *KeyManager) GetCredentials(ctx context.Context, id Identifier) (*Credentials, error) {
	value, rotatedAt, err := c.get(ctx, id)
	if err != nil {
		return nil, err
	}
	return unmarshalCredentials(value, rotatedAt)
}

// DeleteCredentials removes (or, store-dependent, schedules the removal of)
// the credential at id.
func (c *KeyManager) DeleteCredentials(ctx context.Context, id Identifier) error {
	if err := c.store.Delete(ctx, id); err != nil {
		return err
	}
	c.Invalidate(id)
	return nil
}

// Invalidate clears id's cache entry without touching the underlying
// KeyStore. Exposed for a caller that needs an identifier forced cold without
// going through CreateCredentials/UpdateCredentials/DeleteCredentials.
func (c *KeyManager) Invalidate(id Identifier) {
	c.mu.Lock()
	delete(c.entries, id)
	c.mu.Unlock()
}
