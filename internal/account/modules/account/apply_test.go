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
	"database/sql"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/snowflakedb/gosnowflake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/allianz/yukimi/internal/account/pipeline"
	internalerrors "github.com/allianz/yukimi/internal/errors"
	"github.com/allianz/yukimi/internal/secrets"
)

// newOrgAdminMock returns a *sql.DB backed by sqlmock, closed automatically
// at test cleanup.
func newOrgAdminMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// SC-006/SC-015: a fresh create issues CREATE ACCOUNT, captures the locator
// and creation time directly on the CRD's status, and defers verification —
// it returns Pending(...).Aborting(), not Done(), so the pipeline stops
// before any later module tries to connect to a not-yet-reachable account.
func TestApply_FreshCreate_Success(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
	orgAdminDB, mock := newOrgAdminMock(t)
	fake := &fakeDBPool{orgAdminDB: orgAdminDB}
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, fake)

	mock.ExpectExec("CREATE ACCOUNT").WillReturnResult(sqlmock.NewResult(0, 0))
	rows := sqlmock.NewRows([]string{"account_name", "account_locator"}).
		AddRow(mc.ResolvedAccountName(), "AB12345")
	mock.ExpectQuery("SHOW ACCOUNTS").WillReturnRows(rows)

	m := &module{backend: secrets.NewFakeBackend(), org: "myorg", gracePeriod: 5 * time.Minute}
	before := time.Now()
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != pipeline.StatePending {
		t.Errorf("outcome.State = %v, want StatePending", outcome.State)
	}
	if !outcome.Abort {
		t.Error("outcome.Abort = false, want true")
	}
	if cr.Status.AccountLocator != "AB12345" {
		t.Errorf("cr.Status.AccountLocator = %q, want %q", cr.Status.AccountLocator, "AB12345")
	}
	if cr.Status.AccountCreatedAt == nil {
		t.Fatal("cr.Status.AccountCreatedAt = nil, want set")
	}
	if cr.Status.AccountCreatedAt.Time.Before(before) {
		t.Errorf("AccountCreatedAt = %v, want at or after %v", cr.Status.AccountCreatedAt.Time, before)
	}
	if fake.tenantCalls != 0 {
		t.Errorf("TenantAccount called %d times, want 0 — a fresh create must not verify reachability itself", fake.tenantCalls)
	}
}

// SC-004 (backward compat): a known locator with no recorded AccountCreatedAt
// (an account that predates this field) is treated as past the grace period —
// Apply attempts a connection as usual.
func TestApply_KnownLocator_NilCreatedAt_ConnectionSucceeds(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "AB12345", []string{"a@b.com"}, "")
	fake := &fakeDBPool{}
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, fake)

	m := &module{gracePeriod: 5 * time.Minute}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != pipeline.StateDone {
		t.Errorf("outcome.State = %v, want StateDone", outcome.State)
	}
	if fake.tenantCalls != 1 {
		t.Errorf("TenantAccount called %d times, want 1", fake.tenantCalls)
	}
}

// SC-005: Apply aborts with a system error, issuing no SQL of its own, when
// a known locator's platform connection fails once past the grace period.
func TestApply_KnownLocator_PastGracePeriod_ConnectionFails(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "AB12345", []string{"a@b.com"}, "")
	cr.Status.AccountCreatedAt = &metav1.Time{Time: time.Now().Add(-10 * time.Minute)}
	wantErr := errors.New("dial failed")
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, &fakeDBPool{tenantErr: wantErr})

	m := &module{gracePeriod: 5 * time.Minute}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != pipeline.StateFailed {
		t.Errorf("outcome.State = %v, want StateFailed", outcome.State)
	}
	if !outcome.Abort {
		t.Error("outcome.Abort = false, want true")
	}
	if internalerrors.IsUserError(outcome.Err) {
		t.Errorf("expected a system error, got a user error: %v", outcome.Err)
	}
	if !errors.Is(outcome.Err, wantErr) {
		t.Errorf("outcome.Err = %v, want it to wrap %v", outcome.Err, wantErr)
	}
}

// Apply never attempts a connection while the account is within its
// post-create grace period, and aborts the pipeline for this pass instead.
func TestApply_KnownLocator_WithinGracePeriod_NoConnectionAttempt(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "AB12345", []string{"a@b.com"}, "")
	cr.Status.AccountCreatedAt = &metav1.Time{Time: time.Now()}
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, &fakeDBPool{t: t, forbidCalls: true})

	m := &module{gracePeriod: 5 * time.Minute}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != pipeline.StatePending {
		t.Errorf("outcome.State = %v, want StatePending", outcome.State)
	}
	if !outcome.Abort {
		t.Error("outcome.Abort = false, want true")
	}
	if outcome.Err != nil {
		t.Errorf("outcome.Err = %v, want nil — waiting out the grace period is not a failure", outcome.Err)
	}
}

// Apply attempts a connection as usual once the grace period has elapsed.
func TestApply_KnownLocator_PastGracePeriod_ConnectionSucceeds(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "AB12345", []string{"a@b.com"}, "")
	cr.Status.AccountCreatedAt = &metav1.Time{Time: time.Now().Add(-10 * time.Minute)}
	fake := &fakeDBPool{}
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, fake)

	m := &module{gracePeriod: 5 * time.Minute}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != pipeline.StateDone {
		t.Errorf("outcome.State = %v, want StateDone", outcome.State)
	}
	if fake.tenantCalls != 1 {
		t.Errorf("TenantAccount called %d times, want 1", fake.tenantCalls)
	}
}

// SC-012: a CREATE ACCOUNT failure due to an org-wide name collision is
// classified as a user error.
func TestApply_FreshCreate_DuplicateAccountName_Rejected(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
	orgAdminDB, mock := newOrgAdminMock(t)
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, &fakeDBPool{orgAdminDB: orgAdminDB})

	mock.ExpectExec("CREATE ACCOUNT").WillReturnError(&gosnowflake.SnowflakeError{
		Number:   2003,
		SQLState: duplicateAccountSQLState,
		Message:  "SQL compilation error: object already exists",
	})

	m := &module{backend: secrets.NewFakeBackend(), org: "myorg", gracePeriod: 5 * time.Minute}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != pipeline.StateRejected {
		t.Errorf("outcome.State = %v, want StateRejected", outcome.State)
	}
	if !internalerrors.IsUserError(outcome.Err) {
		t.Errorf("expected a user error, got: %v", outcome.Err)
	}
	if cr.Status.AccountLocator != "" {
		t.Errorf("cr.Status.AccountLocator = %q, want empty", cr.Status.AccountLocator)
	}
}

// SC-012: every other CREATE ACCOUNT failure is classified as a system error.
func TestApply_FreshCreate_CreateAccountFails_SystemError(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
	orgAdminDB, mock := newOrgAdminMock(t)
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, &fakeDBPool{orgAdminDB: orgAdminDB})

	mock.ExpectExec("CREATE ACCOUNT").WillReturnError(errors.New("connection reset"))

	m := &module{backend: secrets.NewFakeBackend(), org: "myorg", gracePeriod: 5 * time.Minute}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != pipeline.StateFailed {
		t.Errorf("outcome.State = %v, want StateFailed", outcome.State)
	}
	if internalerrors.IsUserError(outcome.Err) {
		t.Errorf("expected a system error, got a user error: %v", outcome.Err)
	}
}

// The org-admin connection cannot be opened — a system error, no SQL issued.
func TestApply_FreshCreate_OrgAdminConnectionFails(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
	wantErr := errors.New("dial failed")
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, &fakeDBPool{orgAdminErr: wantErr})

	m := &module{backend: secrets.NewFakeBackend(), org: "myorg", gracePeriod: 5 * time.Minute}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != pipeline.StateFailed {
		t.Errorf("outcome.State = %v, want StateFailed", outcome.State)
	}
	if !outcome.Abort {
		t.Error("outcome.Abort = false, want true")
	}
}

// A malformed spec.region — one that fails the bare-identifier charset check
// even after the CREATE ACCOUNT region transform — is rejected as a
// defense-in-depth backstop (specs/012-account-module.md, Security
// Considerations), issuing no SQL.
func TestApply_FreshCreate_MalformedRegion_Rejected(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-1!", "", []string{"a@b.com"}, "")
	orgAdminDB, _ := newOrgAdminMock(t)
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, &fakeDBPool{orgAdminDB: orgAdminDB})

	m := &module{backend: secrets.NewFakeBackend(), org: "myorg", gracePeriod: 5 * time.Minute}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != pipeline.StateRejected {
		t.Errorf("outcome.State = %v, want StateRejected", outcome.State)
	}
	if !internalerrors.IsUserError(outcome.Err) {
		t.Errorf("expected a user error, got: %v", outcome.Err)
	}
}

// SC-014: a fresh create aborts with a system error when the post-create
// locator lookup finds no matching row.
func TestApply_FreshCreate_LocateAccount_NoMatch(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
	orgAdminDB, mock := newOrgAdminMock(t)
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, &fakeDBPool{orgAdminDB: orgAdminDB})

	mock.ExpectExec("CREATE ACCOUNT").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SHOW ACCOUNTS").WillReturnRows(sqlmock.NewRows([]string{"account_name", "account_locator"}))

	m := &module{backend: secrets.NewFakeBackend(), org: "myorg", gracePeriod: 5 * time.Minute}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != pipeline.StateFailed {
		t.Errorf("outcome.State = %v, want StateFailed", outcome.State)
	}
	if cr.Status.AccountLocator != "" {
		t.Errorf("cr.Status.AccountLocator = %q, want empty", cr.Status.AccountLocator)
	}
}

// SC-013: the post-create lookup discards a row whose account name is not an
// exact, case-insensitive match, even though the LIKE pattern matched it.
func TestApply_FreshCreate_LocateAccount_DiscardsNonExactMatch(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
	orgAdminDB, mock := newOrgAdminMock(t)
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, &fakeDBPool{orgAdminDB: orgAdminDB})

	mock.ExpectExec("CREATE ACCOUNT").WillReturnResult(sqlmock.NewResult(0, 0))
	rows := sqlmock.NewRows([]string{"account_name", "account_locator"}).
		AddRow("some_other_account", "ZZ00000").
		AddRow(mc.ResolvedAccountName(), "AB12345")
	mock.ExpectQuery("SHOW ACCOUNTS").WillReturnRows(rows)

	m := &module{backend: secrets.NewFakeBackend(), org: "myorg", gracePeriod: 5 * time.Minute}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != pipeline.StatePending {
		t.Fatalf("outcome.State = %v, want StatePending", outcome.State)
	}
	if cr.Status.AccountLocator != "AB12345" {
		t.Errorf("cr.Status.AccountLocator = %q, want %q", cr.Status.AccountLocator, "AB12345")
	}
}

// SC-007: a fresh create aborts with a system error, generating no keypair
// and issuing no SQL, when the resolved secret path is already occupied.
func TestApply_FreshCreate_SecretPathOccupied(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
	backend := secrets.NewFakeBackend()
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, &fakeDBPool{t: t, forbidCalls: true})

	path, err := secrets.NewTenantPath("myorg", cr.Namespace, cr.Name)
	if err != nil {
		t.Fatalf("secrets.NewTenantPath: %v", err)
	}
	if err := backend.Create(context.Background(), path, "existing-secret"); err != nil {
		t.Fatalf("seeding existing secret: %v", err)
	}

	m := &module{backend: backend, org: "myorg", gracePeriod: 5 * time.Minute}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != pipeline.StateFailed {
		t.Errorf("outcome.State = %v, want StateFailed", outcome.State)
	}
	if !outcome.Abort {
		t.Error("outcome.Abort = false, want true")
	}
}

// SC-011: a fresh create aborts with a user error, generating no keypair and
// issuing no SQL, when spec.contacts is empty.
func TestApply_FreshCreate_NoContacts_Rejected(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "", nil, "")
	mc := pipeline.NewModuleContext(cr, "ns", nil, nil, nil, &fakeDBPool{t: t, forbidCalls: true})

	m := &module{backend: secrets.NewFakeBackend(), org: "myorg", gracePeriod: 5 * time.Minute}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != pipeline.StateRejected {
		t.Errorf("outcome.State = %v, want StateRejected", outcome.State)
	}
	if !internalerrors.IsUserError(outcome.Err) {
		t.Errorf("expected a user error, got: %v", outcome.Err)
	}
	if cr.Status.AccountLocator != "" {
		t.Errorf("cr.Status.AccountLocator = %q, want empty", cr.Status.AccountLocator)
	}
}
