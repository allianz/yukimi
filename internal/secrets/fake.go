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
// is unreadable but the path is still occupied.
type fakeEntry struct {
	value           string
	modifiedAt      time.Time
	pendingDeletion bool
}

// FakeBackend is an in-memory Backend for tests, exported (not a _test.go
// file) so 004, 012, and every other consumer can depend on it without a real
// store. Each hook, if set and returning a non-nil error, short-circuits the
// call before any state mutation — this lets a test flip behavior mid-run
// (e.g. "OnCreate fails once, then is cleared") in a way a construction-time
// option cannot.
type FakeBackend struct {
	OnGet    func(path Path) error
	OnCreate func(path Path) error
	OnUpdate func(path Path) error
	OnDelete func(path Path) error

	// Clock returns the time recorded against a path on Create and Update,
	// and returned by Get. Defaults to time.Now; tests override it for a
	// deterministic RotatedAt.
	Clock func() time.Time

	// SchedulesDeletion makes Delete schedule the removal instead of performing
	// it: the entry becomes unreadable but keeps its path occupied until
	// Restore cancels the removal. False — the default — deletes outright, so a
	// consumer that does not care about the pending state sees the simplest
	// possible behavior.
	SchedulesDeletion bool

	mu      sync.Mutex
	entries map[Path]fakeEntry
}

var _ Backend = (*FakeBackend)(nil)

// NewFakeBackend returns an empty FakeBackend that deletes outright. Delete
// removes the entry and is idempotent, so a Create on a deleted path succeeds
// and a Get on one fails exactly as it would on a path nothing was ever stored
// at. Set SchedulesDeletion to exercise the pending-deletion state instead.
func NewFakeBackend() *FakeBackend {
	return &FakeBackend{entries: make(map[Path]fakeEntry), Clock: time.Now}
}

func (f *FakeBackend) Get(_ context.Context, path Path) (string, time.Time, error) {
	if f.OnGet != nil {
		if err := f.OnGet(path); err != nil {
			return "", time.Time{}, err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	entry, ok := f.entries[path]
	if !ok {
		return "", time.Time{}, fmt.Errorf("secrets: no secret stored at %s", path)
	}
	if entry.pendingDeletion {
		return "", time.Time{}, fmt.Errorf("secrets: the secret at %s is scheduled for deletion", path)
	}
	return entry.value, entry.modifiedAt, nil
}

func (f *FakeBackend) Create(_ context.Context, path Path, value string) error {
	if f.OnCreate != nil {
		if err := f.OnCreate(path); err != nil {
			return err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if existing, ok := f.entries[path]; ok {
		if existing.pendingDeletion {
			return fmt.Errorf(
				"secrets: the secret at %s is scheduled for deletion and its path cannot be reused", path)
		}
		return fmt.Errorf("secrets: a secret already exists at %s", path)
	}
	f.entries[path] = fakeEntry{value: value, modifiedAt: f.Clock()}
	return nil
}

func (f *FakeBackend) Update(_ context.Context, path Path, value string) error {
	if f.OnUpdate != nil {
		if err := f.OnUpdate(path); err != nil {
			return err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	existing, ok := f.entries[path]
	if !ok {
		return fmt.Errorf("secrets: no secret stored at %s", path)
	}
	if existing.pendingDeletion {
		return fmt.Errorf("secrets: the secret at %s is scheduled for deletion", path)
	}
	f.entries[path] = fakeEntry{value: value, modifiedAt: f.Clock()}
	return nil
}

func (f *FakeBackend) Delete(_ context.Context, path Path) error {
	if f.OnDelete != nil {
		if err := f.OnDelete(path); err != nil {
			return err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.SchedulesDeletion {
		delete(f.entries, path)
		return nil
	}

	entry, ok := f.entries[path]
	if !ok {
		// Nothing to schedule; Delete stays idempotent on an absent path.
		return nil
	}
	entry.pendingDeletion = true
	f.entries[path] = entry
	return nil
}

// Restore cancels a pending deletion, making the value readable and the path
// writable again — the store-side half of the manual repair 012 documents.
//
// Returns:
//   - Error if nothing at path is scheduled for deletion, whether because the
//     path is empty or because the entry is live
func (f *FakeBackend) Restore(path Path) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	entry, ok := f.entries[path]
	if !ok || !entry.pendingDeletion {
		return fmt.Errorf("secrets: no secret scheduled for deletion at %s", path)
	}
	entry.pendingDeletion = false
	f.entries[path] = entry
	return nil
}
