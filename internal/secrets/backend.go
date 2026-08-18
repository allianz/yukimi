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
	stderrors "errors"
)

// Backend is a string-valued keystore. It never parses a credential, never
// caches, and never logs — every method reports failure using the sentinel
// errors below, wrapped with %w so callers match them with errors.Is. How the
// value string is persisted is each implementation's own choice.
type Backend interface {
	// Get returns the value stored at path.
	//
	// Returns:
	//   - ErrNotFound if nothing is stored at path
	//   - ErrDenied, ErrUnavailable, or an unclassified store fault otherwise
	Get(ctx context.Context, path Path) (string, error)

	// Create stores value at path. Fails if path is already occupied — this
	// is the atomicity 010 depends on to never silently overwrite a live
	// account's credential on a retried request.
	//
	// Returns:
	//   - ErrAlreadyExists if something is already stored at path
	//   - ErrDenied, ErrUnavailable, or an unclassified store fault otherwise
	Create(ctx context.Context, path Path, value string) error

	// Update overwrites the value already stored at path. Fails if nothing
	// is there — Update never creates.
	//
	// Returns:
	//   - ErrNotFound if nothing is stored at path
	//   - ErrDenied, ErrUnavailable, or an unclassified store fault otherwise
	Update(ctx context.Context, path Path, value string) error

	// Delete removes path. Whether the value is gone immediately or sits in a
	// recovery window first is the implementation's business; nothing in this
	// package reads a deleted path afterwards.
	//
	// Returns:
	//   - ErrDenied, ErrUnavailable, or an unclassified store fault
	Delete(ctx context.Context, path Path) error
}

// Sentinel errors every Backend reports through. Backends wrap the concrete
// vendor error with %w; callers match with errors.Is.
var (
	ErrNotFound      = stderrors.New("secrets: not found")
	ErrAlreadyExists = stderrors.New("secrets: already exists")
	ErrDenied        = stderrors.New("secrets: access denied")
	ErrUnavailable   = stderrors.New("secrets: unavailable")
)
