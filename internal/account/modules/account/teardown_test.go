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

package account

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/allianz/yukimi/internal/account/pipeline"
	"github.com/allianz/yukimi/internal/logger"
	"github.com/allianz/yukimi/internal/secrets"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
)

// spyLogger records every Info call for inspection, mirroring
// internal/logger's own test fake.
type spyLogger struct {
	mu    sync.Mutex
	infos []string
}

func (s *spyLogger) Info(msg string, _ ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.infos = append(s.infos, msg)
}
func (s *spyLogger) Debug(msg string, _ ...any)         {}
func (s *spyLogger) WithValues(_ ...any) logging.Logger { return s }

// SC-023: Teardown issues no SQL and evicts nothing when
// cr.Status.AccountLocator is empty, and still deletes the credential.
func TestTeardown_NoLocator_SkipsAccountSteps_DeletesCredential(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
	fake := &fakeDBPool{t: t, forbidCalls: true}
	backend := secrets.NewFakeBackend()
	path, err := secrets.NewTenantPath("myorg", cr.Namespace, cr.Name)
	if err != nil {
		t.Fatalf("secrets.NewTenantPath: %v", err)
	}
	if err := backend.Create(context.Background(), path, "creds"); err != nil {
		t.Fatalf("backend.Create: %v", err)
	}

	m := &module{backend: backend, org: "myorg"}
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, fake)

	if err := m.Teardown(context.Background(), mc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.evictCalls != 0 {
		t.Errorf("EvictTenant called %d times, want 0", fake.evictCalls)
	}
	if _, _, err := backend.Get(context.Background(), path); err == nil {
		t.Error("credential still present after Teardown")
	}
}

// SC-022, SC-025, SC-026: a known locator drops the account (bare identifier,
// unclamped GRACE_PERIOD_IN_DAYS), evicts the pooled connection, then deletes
// the credential — in that order.
func TestTeardown_KnownLocator_DropsEvictsDeletes_InOrder(t *testing.T) {
	for _, days := range []int{3, 90} {
		t.Run(fmt.Sprintf("gracePeriodDays=%d", days), func(t *testing.T) {
			cr := newTestCR("acct", "ns", "aws-eu-central-1", "AB12345", []string{"a@b.com"}, "")
			orgAdminDB, mock := newOrgAdminMock(t)
			fake := &fakeDBPool{orgAdminDB: orgAdminDB}
			backend := secrets.NewFakeBackend()
			mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, fake)

			path, err := secrets.NewTenantPath("myorg", cr.Namespace, cr.Name)
			if err != nil {
				t.Fatalf("secrets.NewTenantPath: %v", err)
			}
			if err := backend.Create(context.Background(), path, "creds"); err != nil {
				t.Fatalf("backend.Create: %v", err)
			}

			wantSQL := fmt.Sprintf("DROP ACCOUNT IF EXISTS %s GRACE_PERIOD_IN_DAYS = %d", mc.ResolvedAccountName(), days)
			mock.ExpectExec(regexp.QuoteMeta(wantSQL)).WillReturnResult(sqlmock.NewResult(0, 0))

			m := &module{backend: backend, org: "myorg", deletionGracePeriodDays: days}
			if err := m.Teardown(context.Background(), mc); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("sqlmock expectations not met: %v", err)
			}
			if fake.evictCalls != 1 {
				t.Errorf("EvictTenant called %d times, want 1", fake.evictCalls)
			}
			if fake.evictNamespace != "ns" || fake.evictAccountName != cr.Name {
				t.Errorf("EvictTenant(%q, %q), want (%q, %q)", fake.evictNamespace, fake.evictAccountName, "ns", cr.Name)
			}
			if _, _, err := backend.Get(context.Background(), path); err == nil {
				t.Error("credential still present after Teardown")
			}
		})
	}
}

// SC-025: a DROP ACCOUNT failure stops the run before EvictTenant or the
// credential deletion runs.
func TestTeardown_DropAccountFails_StopsBeforeEvictOrDelete(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "AB12345", []string{"a@b.com"}, "")
	orgAdminDB, mock := newOrgAdminMock(t)
	fake := &fakeDBPool{orgAdminDB: orgAdminDB}
	backend := secrets.NewFakeBackend()
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, fake)

	path, err := secrets.NewTenantPath("myorg", cr.Namespace, cr.Name)
	if err != nil {
		t.Fatalf("secrets.NewTenantPath: %v", err)
	}
	if err := backend.Create(context.Background(), path, "creds"); err != nil {
		t.Fatalf("backend.Create: %v", err)
	}

	mock.ExpectExec("DROP ACCOUNT").WillReturnError(errors.New("connection reset"))

	m := &module{backend: backend, org: "myorg", deletionGracePeriodDays: 30}
	if err := m.Teardown(context.Background(), mc); err == nil {
		t.Fatal("expected an error, got nil")
	}

	if fake.evictCalls != 0 {
		t.Errorf("EvictTenant called %d times, want 0", fake.evictCalls)
	}
	if _, _, err := backend.Get(context.Background(), path); err != nil {
		t.Error("credential deleted despite the account drop failing")
	}
}

// The org-admin connection failing to open stops the run before any SQL is
// issued, before EvictTenant, and before the credential is deleted.
func TestTeardown_OrgAdminConnectionFails_ReturnsError(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "AB12345", []string{"a@b.com"}, "")
	wantErr := errors.New("dial failed")
	fake := &fakeDBPool{orgAdminErr: wantErr}
	backend := secrets.NewFakeBackend()
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, fake)

	path, err := secrets.NewTenantPath("myorg", cr.Namespace, cr.Name)
	if err != nil {
		t.Fatalf("secrets.NewTenantPath: %v", err)
	}
	if err := backend.Create(context.Background(), path, "creds"); err != nil {
		t.Fatalf("backend.Create: %v", err)
	}

	m := &module{backend: backend, org: "myorg", deletionGracePeriodDays: 30}
	if err := m.Teardown(context.Background(), mc); !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}

	if fake.evictCalls != 0 {
		t.Errorf("EvictTenant called %d times, want 0", fake.evictCalls)
	}
	if _, _, err := backend.Get(context.Background(), path); err != nil {
		t.Error("credential deleted despite the org-admin connection failing")
	}
}

// A tenant path that fails to build (here, via an empty org) is returned
// unchanged.
func TestTeardown_CredentialPathBuildFails_ReturnsError(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
	fake := &fakeDBPool{t: t, forbidCalls: true}
	backend := secrets.NewFakeBackend()

	m := &module{backend: backend, org: ""}
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, fake)

	if err := m.Teardown(context.Background(), mc); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

// SC-024: an already-absent credential path is treated as success, and
// Delete is never called.
func TestTeardown_CredentialAlreadyAbsent_DeleteNeverCalled(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
	fake := &fakeDBPool{t: t, forbidCalls: true}
	backend := secrets.NewFakeBackend()
	deleteCalls := 0
	backend.OnDelete = func(secrets.Path) error {
		deleteCalls++
		return nil
	}

	m := &module{backend: backend, org: "myorg"}
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, fake)

	if err := m.Teardown(context.Background(), mc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleteCalls != 0 {
		t.Errorf("Backend.Delete called %d times, want 0", deleteCalls)
	}
}

// A credential present at Get time but failing on Delete is a real system
// error.
func TestTeardown_CredentialGetSucceeds_DeleteFails_ReturnsError(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
	fake := &fakeDBPool{t: t, forbidCalls: true}
	backend := secrets.NewFakeBackend()
	path, err := secrets.NewTenantPath("myorg", cr.Namespace, cr.Name)
	if err != nil {
		t.Fatalf("secrets.NewTenantPath: %v", err)
	}
	if err := backend.Create(context.Background(), path, "creds"); err != nil {
		t.Fatalf("backend.Create: %v", err)
	}
	backend.OnDelete = func(secrets.Path) error { return errors.New("store unreachable") }

	m := &module{backend: backend, org: "myorg"}
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, fake)

	if err := m.Teardown(context.Background(), mc); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

// SC-027: Teardown logs the restorable-until time Backend.Delete returned,
// including the zero-time immediate-destroy case, and only when Delete
// actually runs.
func TestTeardown_LogsRestorableUntil(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
	fake := &fakeDBPool{t: t, forbidCalls: true}
	backend := secrets.NewFakeBackend() // zero RecoveryWindow: Delete destroys outright, returns the zero time
	path, err := secrets.NewTenantPath("myorg", cr.Namespace, cr.Name)
	if err != nil {
		t.Fatalf("secrets.NewTenantPath: %v", err)
	}
	if err := backend.Create(context.Background(), path, "creds"); err != nil {
		t.Fatalf("backend.Create: %v", err)
	}

	spy := &spyLogger{}
	log := logger.New(spy, cr.Namespace, "SnowflakeAccount", cr.Name, logger.OpDelete)
	m := &module{backend: backend, org: "myorg"}
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, log, fake)

	if err := m.Teardown(context.Background(), mc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.infos) != 1 {
		t.Fatalf("Info called %d times, want 1", len(spy.infos))
	}
	if !strings.Contains(spy.infos[0], "restorable until") {
		t.Errorf("log message %q does not mention the restorable-until time", spy.infos[0])
	}
}
