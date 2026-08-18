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
)

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

	mu      sync.Mutex
	entries map[Path]string
}

var _ Backend = (*FakeBackend)(nil)

// NewFakeBackend returns an empty FakeBackend. Delete removes the entry
// outright and is idempotent, so a Create on a deleted path succeeds and a Get
// on one fails exactly as it would on a path nothing was ever stored at.
func NewFakeBackend() *FakeBackend {
	return &FakeBackend{entries: make(map[Path]string)}
}

func (f *FakeBackend) Get(_ context.Context, path Path) (string, error) {
	if f.OnGet != nil {
		if err := f.OnGet(path); err != nil {
			return "", err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	value, ok := f.entries[path]
	if !ok {
		return "", fmt.Errorf("secrets: no secret stored at %s", path)
	}
	return value, nil
}

func (f *FakeBackend) Create(_ context.Context, path Path, value string) error {
	if f.OnCreate != nil {
		if err := f.OnCreate(path); err != nil {
			return err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.entries[path]; ok {
		return fmt.Errorf("secrets: a secret already exists at %s", path)
	}
	f.entries[path] = value
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

	if _, ok := f.entries[path]; !ok {
		return fmt.Errorf("secrets: no secret stored at %s", path)
	}
	f.entries[path] = value
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

	delete(f.entries, path)
	return nil
}
