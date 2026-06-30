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

// Package azure provides an Azure Key Vault backend for the secrets manager.
package azure

import (
	"context"
	"fmt"

	"github.com/allianz/yukimi/internal/secrets"
)

// AzureCredentials configures the Azure Key Vault backend.
type AzureCredentials struct {
	VaultURL string // e.g., "https://my-vault.vault.azure.net"
	// References a Kubernetes Secret containing clientId + clientSecret.
	// For managed identity (AKS pod identity) — leave nil.
	CredentialsSecretRef *secrets.SecretReference
}

type backend struct {
	creds *AzureCredentials
}

// NewBackend creates an Azure Key Vault backend.
func NewBackend(creds *AzureCredentials) (secrets.SecretBackend, error) {
	if creds == nil {
		return nil, fmt.Errorf("azure credentials must not be nil")
	}
	if creds.VaultURL == "" {
		return nil, fmt.Errorf("azure vault URL must not be empty")
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

// IsSecretPendingDeletion always returns false — Azure Key Vault does not use a
// pending deletion model equivalent to AWS Secrets Manager.
func (b *backend) IsSecretPendingDeletion(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (b *backend) HealthCheck(ctx context.Context) error {
	panic("not implemented")
}
