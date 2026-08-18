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
)

// fakeEntry is the state held for a single path in a FakeBackend. Absence of
// a path from FakeBackend.entries is itself the "nothing ever stored" state,
// so no separate enum value is needed for it.
type fakeEntry struct {
	value           []byte
	pendingDeletion bool
}

// FakeBackend is an in-memory Backend for tests, exported (not a _test.go
// file) so 004, 010, and every other consumer can depend on it without a real
// store. Each hook, if set and returning a non-nil error, short-circuits the
// call before any state mutation — this lets a test flip behavior mid-run
// (e.g. "OnCreate fails once, then is cleared") in a way a construction-time
// option cannot.
type FakeBackend struct {
	OnGet    func(path Path) error
	OnCreate func(path Path) error
	OnUpdate func(path Path) error
	OnDelete func(path Path) error
	OnPurge  func(path Path) error

	mu      sync.Mutex
	entries map[Path]fakeEntry
}

var _ Backend = (*FakeBackend)(nil)

// NewFakeBackend returns an empty FakeBackend. Delete marks an entry
// pending-deletion rather than removing it, so a subsequent Create or Get
// against that path returns ErrPendingDeletion until Purge — mirroring the
// state machine 003.a implements against a real backend's recovery window.
func NewFakeBackend() *FakeBackend {
	return &FakeBackend{entries: make(map[Path]fakeEntry)}
}

func (f *FakeBackend) Get(_ context.Context, path Path) ([]byte, error) {
	if f.OnGet != nil {
		if err := f.OnGet(path); err != nil {
			return nil, err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	entry, ok := f.entries[path]
	if !ok {
		return nil, ErrNotFound
	}
	if entry.pendingDeletion {
		return nil, ErrPendingDeletion
	}
	return append([]byte(nil), entry.value...), nil
}

func (f *FakeBackend) Create(_ context.Context, path Path, value []byte) error {
	if f.OnCreate != nil {
		if err := f.OnCreate(path); err != nil {
			return err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	entry, ok := f.entries[path]
	if !ok {
		f.entries[path] = fakeEntry{value: append([]byte(nil), value...)}
		return nil
	}
	if entry.pendingDeletion {
		return ErrPendingDeletion
	}
	return ErrAlreadyExists
}

func (f *FakeBackend) Update(_ context.Context, path Path, value []byte) error {
	if f.OnUpdate != nil {
		if err := f.OnUpdate(path); err != nil {
			return err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	entry, ok := f.entries[path]
	if !ok || entry.pendingDeletion {
		return ErrNotFound
	}
	f.entries[path] = fakeEntry{value: append([]byte(nil), value...)}
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

	entry, ok := f.entries[path]
	if !ok {
		return nil
	}
	entry.pendingDeletion = true
	f.entries[path] = entry
	return nil
}

func (f *FakeBackend) Purge(_ context.Context, path Path) error {
	if f.OnPurge != nil {
		if err := f.OnPurge(path); err != nil {
			return err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.entries, path)
	return nil
}
