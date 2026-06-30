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

// Package gcp provides a GCP Secret Manager backend for the secrets manager.
package gcp

import (
	"context"
	"fmt"

	"github.com/allianz/yukimi/internal/secrets"
)

// GCPCredentials configures the GCP Secret Manager backend.
type GCPCredentials struct {
	ProjectID string // GCP project ID (e.g., "my-project-123")
	// References a Kubernetes Secret containing service account JSON.
	// For Workload Identity (GKE pod identity) — leave nil.
	CredentialsSecretRef *secrets.SecretReference
}

type backend struct {
	creds *GCPCredentials
}

// NewBackend creates a GCP Secret Manager backend.
func NewBackend(creds *GCPCredentials) (secrets.SecretBackend, error) {
	if creds == nil {
		return nil, fmt.Errorf("gcp credentials must not be nil")
	}
	if creds.ProjectID == "" {
		return nil, fmt.Errorf("gcp project ID must not be empty")
	}
	return &backend{creds: creds}, nil
}

func (b *backend) GetSecret(ctx context.Context, path string) ([]byte, error) {
	panic("not implemented")
}

func (b *backend) PutSecret(ctx context.Context, path string, value []byte) error {
	panic("not implemented")
}

func (b *backend) DeleteSecret(ctx context.Context, path string) error {
	panic("not implemented")
}

// IsSecretPendingDeletion always returns false — GCP Secret Manager does not have
// a soft-delete pending deletion model.
func (b *backend) IsSecretPendingDeletion(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (b *backend) HealthCheck(ctx context.Context) error {
	panic("not implemented")
}
