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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	yukimierrors "github.com/allianz/yukimi/internal/errors"
	"github.com/allianz/yukimi/internal/secrets"
)

// fakeClient implements this package's client interface with no AWS account
// and no network. Each *Err field, when non-nil, is returned by the
// corresponding method instead of a success response.
type fakeClient struct {
	getErr    error
	createErr error
	putErr    error
	deleteErr error

	getCalled    bool
	createCalled bool
	putCalled    bool
	deleteCalled bool

	getInput    *secretsmanager.GetSecretValueInput
	createInput *secretsmanager.CreateSecretInput
	putInput    *secretsmanager.PutSecretValueInput
	deleteInput *secretsmanager.DeleteSecretInput
}

func (f *fakeClient) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	f.getCalled = true
	f.getInput = in
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &secretsmanager.GetSecretValueOutput{SecretString: aws.String("stored-value"), CreatedDate: aws.Time(fakeCreatedDate)}, nil
}

// fakeCreatedDate is what fakeClient.GetSecretValue reports as the secret's
// CreatedDate, so TestGet can assert Get reads it through unchanged.
var fakeCreatedDate = time.Date(2024, 3, 15, 12, 0, 0, 0, time.UTC)

func (f *fakeClient) CreateSecret(_ context.Context, in *secretsmanager.CreateSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
	f.createCalled = true
	f.createInput = in
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &secretsmanager.CreateSecretOutput{}, nil
}

func (f *fakeClient) PutSecretValue(_ context.Context, in *secretsmanager.PutSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
	f.putCalled = true
	f.putInput = in
	if f.putErr != nil {
		return nil, f.putErr
	}
	return &secretsmanager.PutSecretValueOutput{}, nil
}

func (f *fakeClient) DeleteSecret(_ context.Context, in *secretsmanager.DeleteSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error) {
	f.deleteCalled = true
	f.deleteInput = in
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return &secretsmanager.DeleteSecretOutput{DeletionDate: aws.Time(fakeDeletionDate)}, nil
}

// fakeDeletionDate is what fakeClient.DeleteSecret reports as the moment the secret stops
// being restorable, so TestDelete can assert Delete reads it through unchanged instead of
// recomputing it from the window.
var fakeDeletionDate = time.Date(2026, 10, 4, 9, 30, 0, 0, time.UTC)

// newTestBackend builds a Backend whose recovery window is computed from gracePeriodDays
// exactly as New computes it, without the SDK config load New performs.
func newTestBackend(fake *fakeClient, gracePeriodDays int) *Backend {
	recoveryWindowDays := gracePeriodDays
	if recoveryWindowDays > maxRecoveryWindowDays {
		recoveryWindowDays = maxRecoveryWindowDays
	}
	return &Backend{client: fake, recoveryWindowDays: recoveryWindowDays}
}

func testPath(t *testing.T) secrets.Path {
	t.Helper()
	path, err := secrets.NewTenantPath("my_org", "finance", "analytics-team-eu")
	if err != nil {
		t.Fatalf("NewTenantPath: %v", err)
	}
	return path
}

func TestNew(t *testing.T) {
	t.Run("empty region is a user error", func(t *testing.T) {
		backend, err := New("", "", 30)
		if err == nil {
			t.Fatal("expected an error for empty region")
		}
		if !yukimierrors.IsUserError(err) {
			t.Fatalf("expected a user error, got %v", err)
		}
		if backend != nil {
			t.Fatalf("expected a nil Backend on error, got %v", backend)
		}
	})

	t.Run("non-empty region succeeds and makes no AWS call", func(t *testing.T) {
		backend, err := New("eu-central-1", "", 30)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if backend == nil {
			t.Fatal("expected a non-nil Backend")
		}
		// SC-019: the window is computed once, here, from the account grace period.
		if got := backend.recoveryWindowDays; got != 30 {
			t.Fatalf("recoveryWindowDays = %d, want 30", got)
		}
	})

	t.Run("local SDK config load failure is a system error", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(configPath, []byte("[profile broken\nthis is not valid ini"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		t.Setenv("AWS_SDK_LOAD_CONFIG", "1")
		t.Setenv("AWS_CONFIG_FILE", configPath)
		t.Setenv("AWS_PROFILE", "broken")

		backend, err := New("eu-central-1", "", 30)
		if err == nil {
			t.Fatal("expected a config-load error")
		}
		if yukimierrors.IsUserError(err) {
			t.Fatalf("expected a system error, got a user error: %v", err)
		}
		if backend != nil {
			t.Fatalf("expected a nil Backend on error, got %v", backend)
		}
	})
}

func TestGet(t *testing.T) {
	path := testPath(t)

	t.Run("success reads SecretString via GetSecretValue only", func(t *testing.T) {
		fake := &fakeClient{}
		backend := &Backend{client: fake}

		got, gotAt, err := backend.Get(context.Background(), path)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got != "stored-value" {
			t.Fatalf("Get = %q, want %q", got, "stored-value")
		}
		if !gotAt.Equal(fakeCreatedDate) {
			t.Fatalf("Get returned time %v, want %v", gotAt, fakeCreatedDate)
		}
		if !fake.getCalled {
			t.Fatal("expected GetSecretValue to be called")
		}
		if aws.ToString(fake.getInput.SecretId) != path.String() {
			t.Fatalf("SecretId = %q, want %q", aws.ToString(fake.getInput.SecretId), path.String())
		}
	})

	t.Run("failure is wrapped with operation and path", func(t *testing.T) {
		underlying := errors.New("ResourceNotFoundException: secret not found")
		fake := &fakeClient{getErr: underlying}
		backend := &Backend{client: fake}

		_, _, err := backend.Get(context.Background(), path)
		if err == nil {
			t.Fatal("expected an error")
		}
		wantPrefix := "failed to get secret at " + path.String() + ": "
		if !strings.HasPrefix(err.Error(), wantPrefix) {
			t.Fatalf("error = %q, want prefix %q", err.Error(), wantPrefix)
		}
		if !errors.Is(err, underlying) {
			t.Fatalf("expected errors.Is to reach the underlying AWS error, got %v", err)
		}
	})
}

func TestCreate(t *testing.T) {
	path := testPath(t)

	t.Run("success calls CreateSecret with SecretString, never SecretBinary", func(t *testing.T) {
		fake := &fakeClient{}
		backend := &Backend{client: fake}

		if err := backend.Create(context.Background(), path, "some-opaque-value"); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if !fake.createCalled {
			t.Fatal("expected CreateSecret to be called")
		}
		if fake.putCalled {
			t.Fatal("Create must never call PutSecretValue")
		}
		if aws.ToString(fake.createInput.Name) != path.String() {
			t.Fatalf("Name = %q, want %q", aws.ToString(fake.createInput.Name), path.String())
		}
		if aws.ToString(fake.createInput.SecretString) != "some-opaque-value" {
			t.Fatalf("SecretString = %q, want %q", aws.ToString(fake.createInput.SecretString), "some-opaque-value")
		}
		if fake.createInput.SecretBinary != nil {
			t.Fatal("Create must never set SecretBinary")
		}
	})

	t.Run("KmsKeyId is set when configured", func(t *testing.T) {
		fake := &fakeClient{}
		backend := &Backend{client: fake, kmsKeyId: "alias/yukimi-secrets"}

		if err := backend.Create(context.Background(), path, "value"); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if got := aws.ToString(fake.createInput.KmsKeyId); got != "alias/yukimi-secrets" {
			t.Fatalf("KmsKeyId = %q, want %q", got, "alias/yukimi-secrets")
		}
	})

	t.Run("KmsKeyId is left unset when not configured", func(t *testing.T) {
		fake := &fakeClient{}
		backend := &Backend{client: fake}

		if err := backend.Create(context.Background(), path, "value"); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if fake.createInput.KmsKeyId != nil {
			t.Fatalf("KmsKeyId = %q, want unset", aws.ToString(fake.createInput.KmsKeyId))
		}
	})

	t.Run("failure on an occupied name is wrapped, never falls back to PutSecretValue", func(t *testing.T) {
		underlying := errors.New("ResourceExistsException: a secret with this name already exists")
		fake := &fakeClient{createErr: underlying}
		backend := &Backend{client: fake}

		err := backend.Create(context.Background(), path, "some-opaque-value")
		if err == nil {
			t.Fatal("expected Create to fail on an occupied name")
		}
		if fake.putCalled {
			t.Fatal("Create must never call PutSecretValue")
		}
		wantPrefix := "failed to create secret at " + path.String() + ": "
		if !strings.HasPrefix(err.Error(), wantPrefix) {
			t.Fatalf("error = %q, want prefix %q", err.Error(), wantPrefix)
		}
		if !errors.Is(err, underlying) {
			t.Fatalf("expected errors.Is to reach the underlying AWS error, got %v", err)
		}
	})
}

func TestUpdate(t *testing.T) {
	path := testPath(t)

	t.Run("success calls PutSecretValue, never CreateSecret", func(t *testing.T) {
		fake := &fakeClient{}
		backend := &Backend{client: fake}

		if err := backend.Update(context.Background(), path, "new-value"); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if !fake.putCalled {
			t.Fatal("expected PutSecretValue to be called")
		}
		if fake.createCalled {
			t.Fatal("Update must never call CreateSecret")
		}
		if aws.ToString(fake.putInput.SecretId) != path.String() {
			t.Fatalf("SecretId = %q, want %q", aws.ToString(fake.putInput.SecretId), path.String())
		}
		if aws.ToString(fake.putInput.SecretString) != "new-value" {
			t.Fatalf("SecretString = %q, want %q", aws.ToString(fake.putInput.SecretString), "new-value")
		}
	})

	t.Run("failure on a missing secret is wrapped, never falls back to CreateSecret", func(t *testing.T) {
		underlying := errors.New("ResourceNotFoundException: secret not found")
		fake := &fakeClient{putErr: underlying}
		backend := &Backend{client: fake}

		err := backend.Update(context.Background(), path, "new-value")
		if err == nil {
			t.Fatal("expected Update to fail on a missing secret")
		}
		if fake.createCalled {
			t.Fatal("Update must never call CreateSecret")
		}
		wantPrefix := "failed to update secret at " + path.String() + ": "
		if !strings.HasPrefix(err.Error(), wantPrefix) {
			t.Fatalf("error = %q, want prefix %q", err.Error(), wantPrefix)
		}
		if !errors.Is(err, underlying) {
			t.Fatalf("expected errors.Is to reach the underlying AWS error, got %v", err)
		}
	})
}

func TestDelete(t *testing.T) {
	path := testPath(t)

	// SC-020, SC-021: the computed window is what Delete sends, never AWS's own 30-day default,
	// and never ForceDeleteWithoutRecovery — 002's grace period floor (7) already matches
	// Secrets Manager's own minimum.
	t.Run("the window sent is computed from the grace period", func(t *testing.T) {
		for _, tc := range []struct {
			name            string
			gracePeriodDays int
			wantWindowDays  int64
		}{
			{"grace period above the ceiling is capped", 90, 30},
			{"grace period at the ceiling matches exactly", 30, 30},
			{"grace period inside the band is used verbatim", 14, 14},
			{"grace period at the floor is used verbatim", 7, 7},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fake := &fakeClient{}
				backend := newTestBackend(fake, tc.gracePeriodDays)

				restorableUntil, err := backend.Delete(context.Background(), path)
				if err != nil {
					t.Fatalf("Delete: %v", err)
				}
				if !fake.deleteCalled {
					t.Fatal("expected DeleteSecret to be called")
				}
				if aws.ToString(fake.deleteInput.SecretId) != path.String() {
					t.Fatalf("SecretId = %q, want %q", aws.ToString(fake.deleteInput.SecretId), path.String())
				}

				if got := aws.ToInt64(fake.deleteInput.RecoveryWindowInDays); got != tc.wantWindowDays {
					t.Errorf("RecoveryWindowInDays = %d, want %d", got, tc.wantWindowDays)
				}
				if fake.deleteInput.ForceDeleteWithoutRecovery != nil {
					t.Error("Delete must never set ForceDeleteWithoutRecovery")
				}
				// SC-022: the deadline is AWS's own DeletionDate, read through unchanged.
				if !restorableUntil.Equal(fakeDeletionDate) {
					t.Errorf("restorableUntil = %v, want %v", restorableUntil, fakeDeletionDate)
				}
			})
		}
	})

	t.Run("failure on an already-absent path is wrapped, not swallowed", func(t *testing.T) {
		underlying := errors.New("ResourceNotFoundException: secret not found")
		fake := &fakeClient{deleteErr: underlying}
		backend := newTestBackend(fake, 30)

		restorableUntil, err := backend.Delete(context.Background(), path)
		if !restorableUntil.IsZero() {
			t.Errorf("restorableUntil = %v, want the zero time on error", restorableUntil)
		}
		if err == nil {
			t.Fatal("expected Delete on an already-absent path to fail (not idempotent)")
		}
		wantPrefix := "failed to delete secret at " + path.String() + ": "
		if !strings.HasPrefix(err.Error(), wantPrefix) {
			t.Fatalf("error = %q, want prefix %q", err.Error(), wantPrefix)
		}
		if !errors.Is(err, underlying) {
			t.Fatalf("expected errors.Is to reach the underlying AWS error, got %v", err)
		}
	})
}
