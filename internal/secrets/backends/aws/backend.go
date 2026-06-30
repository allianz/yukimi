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

// Package aws provides an AWS Secrets Manager backend for the secrets manager.
package aws

import (
	"context"
	"fmt"

	"github.com/allianz/yukimi/internal/secrets"
)

// AWSCredentials configures the AWS Secrets Manager backend.
type AWSCredentials struct {
	Source    string // "Secret" or "InjectedIdentity"
	Region    string // AWS region (e.g., "eu-central-1")
	// For "Secret" source — references a Kubernetes Secret containing AccessKeyID + SecretAccessKey.
	// For "InjectedIdentity" — leave nil, uses AWS IRSA automatically.
	CredentialsSecretRef *secrets.SecretReference
}

type backend struct {
	creds *AWSCredentials
}

// NewBackend creates an AWS Secrets Manager backend.
func NewBackend(creds *AWSCredentials) (secrets.SecretBackend, error) {
	if creds == nil {
		return nil, fmt.Errorf("aws credentials must not be nil")
	}
	if creds.Region == "" {
		return nil, fmt.Errorf("aws region must not be empty")
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

func (b *backend) IsSecretPendingDeletion(ctx context.Context, path string) (bool, error) {
	panic("not implemented")
}

func (b *backend) HealthCheck(ctx context.Context) error {
	panic("not implemented")
}
