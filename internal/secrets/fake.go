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
	"fmt"
	"sync"
	"time"
)

// fakeEntry holds a stored value and the time it was last recorded. A
// pendingDeletion entry is one Delete scheduled rather than removed: the value
// is unreadable but the identifier is still occupied.
type fakeEntry struct {
	value           string
	modifiedAt      time.Time
	pendingDeletion bool
}

// FakeKeyStore is an in-memory KeyStore for tests, exported (not a _test.go
// file) so 004, 012, and every other consumer can depend on it without a real
// store. Each hook, if set and returning a non-nil error, short-circuits the
// call before any state mutation — this lets a test flip behavior mid-run
// (e.g. "OnCreate fails once, then is cleared") in a way a construction-time
// option cannot.
type FakeKeyStore struct {
	OnGet    func(id Identifier) error
	OnCreate func(id Identifier) error
	OnUpdate func(id Identifier) error
	OnDelete func(id Identifier) error

	// Clock returns the time recorded against an identifier on Create and
	// Update, and returned by Get. Defaults to time.Now; tests override it
	// for a deterministic RotatedAt.
	Clock func() time.Time

	// SchedulesDeletion makes Delete schedule the removal instead of performing
	// it: the entry becomes unreadable but keeps its identifier occupied until
	// Restore cancels the removal. False — the default — deletes outright, so a
	// consumer that does not care about the pending state sees the simplest
	// possible behavior.
	SchedulesDeletion bool

	mu      sync.Mutex
	entries map[Identifier]fakeEntry
}

var _ KeyStore = (*FakeKeyStore)(nil)

// NewFakeKeyStore returns an empty FakeKeyStore that deletes outright. Delete
// removes the entry and is idempotent, so a Create on a deleted identifier
// succeeds and a Get on one fails exactly as it would on an identifier
// nothing was ever stored at. Set SchedulesDeletion to exercise the
// pending-deletion state instead.
func NewFakeKeyStore() *FakeKeyStore {
	return &FakeKeyStore{entries: make(map[Identifier]fakeEntry), Clock: time.Now}
}

func (f *FakeKeyStore) Get(_ context.Context, id Identifier) (string, time.Time, error) {
	if f.OnGet != nil {
		if err := f.OnGet(id); err != nil {
			return "", time.Time{}, err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	entry, ok := f.entries[id]
	if !ok {
		return "", time.Time{}, fmt.Errorf("secrets: no secret stored at %s", id)
	}
	if entry.pendingDeletion {
		return "", time.Time{}, fmt.Errorf("secrets: the secret at %s is scheduled for deletion", id)
	}
	return entry.value, entry.modifiedAt, nil
}

func (f *FakeKeyStore) Create(_ context.Context, id Identifier, value string) error {
	if f.OnCreate != nil {
		if err := f.OnCreate(id); err != nil {
			return err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if existing, ok := f.entries[id]; ok {
		if existing.pendingDeletion {
			return fmt.Errorf(
				"secrets: the secret at %s is scheduled for deletion and its identifier cannot be reused: %w",
				id, ErrPendingDeletion)
		}
		return fmt.Errorf("secrets: a secret already exists at %s", id)
	}
	f.entries[id] = fakeEntry{value: value, modifiedAt: f.Clock()}
	return nil
}

func (f *FakeKeyStore) Update(_ context.Context, id Identifier, value string) error {
	if f.OnUpdate != nil {
		if err := f.OnUpdate(id); err != nil {
			return err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	existing, ok := f.entries[id]
	if !ok {
		return fmt.Errorf("secrets: no secret stored at %s", id)
	}
	if existing.pendingDeletion {
		return fmt.Errorf("secrets: the secret at %s is scheduled for deletion", id)
	}
	f.entries[id] = fakeEntry{value: value, modifiedAt: f.Clock()}
	return nil
}

func (f *FakeKeyStore) Delete(_ context.Context, id Identifier) error {
	if f.OnDelete != nil {
		if err := f.OnDelete(id); err != nil {
			return err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.SchedulesDeletion {
		delete(f.entries, id)
		return nil
	}

	entry, ok := f.entries[id]
	if !ok {
		// Nothing to schedule; Delete stays idempotent on an absent identifier.
		return nil
	}
	entry.pendingDeletion = true
	f.entries[id] = entry
	return nil
}

// Restore cancels a pending deletion, making the value readable and the
// identifier writable again — the store-side half of the manual repair 012
// documents.
//
// Returns:
//   - Error if nothing at id is scheduled for deletion, whether because the
//     identifier is empty or because the entry is live
func (f *FakeKeyStore) Restore(id Identifier) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	entry, ok := f.entries[id]
	if !ok || !entry.pendingDeletion {
		return fmt.Errorf("secrets: no secret scheduled for deletion at %s", id)
	}
	entry.pendingDeletion = false
	f.entries[id] = entry
	return nil
}
