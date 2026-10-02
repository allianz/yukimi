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
// cache. It implements KeyStore itself, so callers depend on the interface,
// never on this concrete type.
type KeyManager struct {
	store KeyStore
	ttl   time.Duration

	mu      sync.Mutex
	entries map[Identifier]cacheEntry
}

var _ KeyStore = (*KeyManager)(nil)

// NewKeyManager wraps store. Every concrete KeyStore should be wrapped exactly
// once, at construction time in cmd/provider/main.go.
func NewKeyManager(store KeyStore, ttl time.Duration) *KeyManager {
	return &KeyManager{store: store, ttl: ttl, entries: make(map[Identifier]cacheEntry)}
}

func (c *KeyManager) Get(ctx context.Context, id Identifier) (string, time.Time, error) {
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

func (c *KeyManager) Create(ctx context.Context, id Identifier, value string) error {
	if err := c.store.Create(ctx, id, value); err != nil {
		return err
	}
	c.Invalidate(id)
	return nil
}

func (c *KeyManager) Update(ctx context.Context, id Identifier, value string) error {
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
	if err := c.Create(ctx, id, value); err != nil {
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
	return c.Update(ctx, id, value)
}

// GetCredentials reads the credential at id and unmarshals it.
func (c *KeyManager) GetCredentials(ctx context.Context, id Identifier) (*Credentials, error) {
	value, rotatedAt, err := c.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return unmarshalCredentials(value, rotatedAt)
}

func (c *KeyManager) Delete(ctx context.Context, id Identifier) error {
	if err := c.store.Delete(ctx, id); err != nil {
		return err
	}
	c.Invalidate(id)
	return nil
}

// Invalidate clears id's cache entry without touching the underlying
// KeyStore. Exposed for a caller that needs an identifier forced cold without
// going through Create/Update/Delete.
func (c *KeyManager) Invalidate(id Identifier) {
	c.mu.Lock()
	delete(c.entries, id)
	c.mu.Unlock()
}
