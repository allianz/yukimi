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
	"time"
)

// Backend is a string-valued keystore. It never parses a credential, never
// caches, and never logs — every method reports failure as an ordinary error
// whose message names the path it failed on, and no caller branches on an
// error's identity. How the value string is persisted is each implementation's
// own choice.
type Backend interface {
	// Get returns the value stored at path, along with the time the backend
	// last wrote that value — creation time if never overwritten,
	// modification time otherwise. It fails if nothing is stored there, and
	// it fails if the store cannot be read; the returned time is the zero
	// value on error.
	Get(ctx context.Context, path Path) (string, time.Time, error)

	// Create stores value at path. It fails if path is already occupied, and
	// leaves the occupying value untouched when it does — this is the
	// atomicity 010 depends on to never silently overwrite a live account's
	// credential on a retried request.
	Create(ctx context.Context, path Path, value string) error

	// Update overwrites the value already stored at path. It fails if nothing
	// is stored there — Update never creates.
	Update(ctx context.Context, path Path, value string) error

	// Delete removes path. Whether the value is gone immediately or sits in a
	// recovery window first is the implementation's business; nothing in this
	// package reads a deleted path afterwards.
	Delete(ctx context.Context, path Path) error
}
