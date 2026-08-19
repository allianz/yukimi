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

package secretsaws

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/allianz/yukimi/internal/errors"
	"github.com/allianz/yukimi/internal/secrets"
)

// client is the subset of *secretsmanager.Client this package calls. It
// exists so backend_test.go can substitute a fake with no AWS account and no
// network; the real *secretsmanager.Client satisfies it unchanged.
type client interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
	CreateSecret(ctx context.Context, in *secretsmanager.CreateSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error)
	PutSecretValue(ctx context.Context, in *secretsmanager.PutSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error)
	DeleteSecret(ctx context.Context, in *secretsmanager.DeleteSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error)
}

// Backend implements secrets.Backend (003) against AWS Secrets Manager.
// Each method is exactly one AWS API call; the struct holds only the client
// and the KMS key id carried through to every Create.
type Backend struct {
	client   client
	kmsKeyId string
}

var _ secrets.Backend = (*Backend)(nil)

// New constructs a Backend for region. It loads credentials from the AWS
// SDK's default chain only and makes no AWS API call — a bad region or bad
// credentials surfaces on the first real Get/Create/Update/Delete, not here.
//
// Parameters:
//   - region: AWS region secrets are stored in (BaseConfig.AWS.Region, 002)
//   - kmsKeyId: optional customer-managed KMS key id, alias, or ARN
//     (BaseConfig.AWS.KmsKeyId, 002); passed to CreateSecret's KmsKeyId only
//     when non-empty — otherwise Secrets Manager's AWS-managed default key
//     encrypts the secret
//
// Returns:
//   - secrets.Backend: never nil on a nil error
//   - User error if region is empty
//   - System error if the AWS SDK's own local config/credential loading
//     fails (e.g. a malformed shared config file) — this is not a network
//     call and not specific to Secrets Manager
func New(region, kmsKeyId string) (secrets.Backend, error) {
	if region == "" {
		return nil, errors.NewUserError(
			"AWS region is required to construct the secrets backend (expected: aws.region in baseConfig.yaml)")
	}

	cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS SDK config: %w", err)
	}

	return &Backend{
		client:   secretsmanager.NewFromConfig(cfg),
		kmsKeyId: kmsKeyId,
	}, nil
}

// Get reads the value stored at path via GetSecretValue.
//
// Returns:
//   - System error if the secret does not exist or the call otherwise fails
func (b *Backend) Get(ctx context.Context, path secrets.Path) (string, error) {
	out, err := b.client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(path.String()),
	})
	if err != nil {
		return "", fmt.Errorf("failed to get secret at %s: %w", path, err)
	}
	return aws.ToString(out.SecretString), nil
}

// Create stores value at path via CreateSecret, setting KmsKeyId when the
// constructor was given one. Never calls PutSecretValue.
//
// Returns:
//   - System error if path is already occupied or the call otherwise fails
func (b *Backend) Create(ctx context.Context, path secrets.Path, value string) error {
	in := &secretsmanager.CreateSecretInput{
		Name:         aws.String(path.String()),
		SecretString: aws.String(value),
	}
	if b.kmsKeyId != "" {
		in.KmsKeyId = aws.String(b.kmsKeyId)
	}

	if _, err := b.client.CreateSecret(ctx, in); err != nil {
		return fmt.Errorf("failed to create secret at %s: %w", path, err)
	}
	return nil
}

// Update overwrites the value at path via PutSecretValue. Never calls
// CreateSecret.
//
// Returns:
//   - System error if nothing is stored at path or the call otherwise fails
func (b *Backend) Update(ctx context.Context, path secrets.Path, value string) error {
	_, err := b.client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId:     aws.String(path.String()),
		SecretString: aws.String(value),
	})
	if err != nil {
		return fmt.Errorf("failed to update secret at %s: %w", path, err)
	}
	return nil
}

// Delete removes path via DeleteSecret, leaving AWS's default recovery
// window in place. Never calls RestoreSecret or ForceDeleteWithoutRecovery.
//
// Returns:
//   - System error if the call fails, including on an already-absent path
//     (AWS's ResourceNotFoundException) — unlike 003's FakeBackend.Delete,
//     this is not idempotent (see Edge Cases)
func (b *Backend) Delete(ctx context.Context, path secrets.Path) error {
	_, err := b.client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId: aws.String(path.String()),
	})
	if err != nil {
		return fmt.Errorf("failed to delete secret at %s: %w", path, err)
	}
	return nil
}
