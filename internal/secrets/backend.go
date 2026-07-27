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

import "context"

// SecretBackend abstracts secret storage operations.
// Implementations live in internal/secrets/backends/ (one sub-package per backend, e.g. aws/).
// All methods operate on raw JSON bytes — no credential parsing.
type SecretBackend interface {
	// GetSecret retrieves raw secret bytes at the given path.
	// Returns system error if path not found, permissions denied, or backend unavailable.
	GetSecret(ctx context.Context, path string) ([]byte, error)

	// PutSecret stores raw secret bytes at the given path.
	// Creates or overwrites the secret.
	PutSecret(ctx context.Context, path string, value []byte) error

	// DeleteSecret removes the secret at the given path.
	// Soft delete where supported (e.g., AWS 30-day window).
	DeleteSecret(ctx context.Context, path string) error

	// IsSecretPendingDeletion checks if a secret exists but is pending deletion.
	// Used by GenerateAndStore() to detect and surface the conflict as a system error.
	// Returns false for backends that do not support soft-delete.
	IsSecretPendingDeletion(ctx context.Context, path string) (bool, error)

	// HealthCheck verifies backend connectivity and credentials.
	// Returns system error if credentials invalid, permissions denied, or backend unavailable.
	HealthCheck(ctx context.Context) error
}
