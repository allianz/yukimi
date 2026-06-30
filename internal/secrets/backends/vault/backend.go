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

// Package vault provides a HashiCorp Vault backend for the secrets manager.
package vault

import (
	"context"
	"fmt"

	"github.com/allianz/yukimi/internal/secrets"
)

// VaultCredentials configures the HashiCorp Vault backend.
type VaultCredentials struct {
	Address   string // e.g., "https://vault.example.com"
	MountPath string // KV mount path (e.g., "secret")
	// References a Kubernetes Secret containing the Vault token.
	// For Kubernetes auth method — leave nil, uses pod service account JWT automatically.
	CredentialsSecretRef *secrets.SecretReference
}

type backend struct {
	creds *VaultCredentials
}

// NewBackend creates a HashiCorp Vault backend.
func NewBackend(creds *VaultCredentials) (secrets.SecretBackend, error) {
	if creds == nil {
		return nil, fmt.Errorf("vault credentials must not be nil")
	}
	if creds.Address == "" {
		return nil, fmt.Errorf("vault address must not be empty")
	}
	if creds.MountPath == "" {
		creds.MountPath = "secret"
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

// IsSecretPendingDeletion always returns false — HashiCorp Vault does not have
// a soft-delete pending deletion model.
func (b *backend) IsSecretPendingDeletion(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (b *backend) HealthCheck(ctx context.Context) error {
	panic("not implemented")
}
