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
	"testing"
	"time"
)

// SC-010: Cache returns value before TTL expires.
func TestCache_HitBeforeExpiry(t *testing.T) {
	c := newCredentialCache[string](time.Minute)
	c.set("key", "value")

	got, ok := c.get("key")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got != "value" {
		t.Errorf("expected 'value', got %q", got)
	}
}

// SC-010: Cache evicts expired entry on access (lazy eviction).
func TestCache_LazyEvictionOnExpiry(t *testing.T) {
	c := newCredentialCache[string](-time.Second) // already expired TTL
	c.set("key", "value")

	_, ok := c.get("key")
	if ok {
		t.Fatal("expected cache miss for expired entry")
	}
}

// Cache miss for unknown key.
func TestCache_Miss(t *testing.T) {
	c := newCredentialCache[string](time.Minute)

	_, ok := c.get("nonexistent")
	if ok {
		t.Fatal("expected cache miss for unknown key")
	}
}

// delete removes entry. Idempotent — second delete does not panic.
func TestCache_DeleteIdempotent(t *testing.T) {
	c := newCredentialCache[string](time.Minute)
	c.set("key", "value")
	c.delete("key")
	c.delete("key") // second delete — must not panic

	_, ok := c.get("key")
	if ok {
		t.Fatal("expected cache miss after delete")
	}
}

// clear removes all entries.
func TestCache_Clear(t *testing.T) {
	c := newCredentialCache[string](time.Minute)
	c.set("a", "1")
	c.set("b", "2")
	c.clear()

	if _, ok := c.get("a"); ok {
		t.Error("expected cache miss after clear for 'a'")
	}
	if _, ok := c.get("b"); ok {
		t.Error("expected cache miss after clear for 'b'")
	}
}
