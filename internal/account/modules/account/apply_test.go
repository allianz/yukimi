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
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/snowflakedb/gosnowflake"

	coreaccount "github.com/allianz/yukimi/internal/account"
	internalerrors "github.com/allianz/yukimi/internal/errors"
	"github.com/allianz/yukimi/internal/secrets"
	"github.com/allianz/yukimi/internal/tenant"
)

// SC-004: Apply returns Done() without touching the credential store or
// issuing CREATE ACCOUNT when a locator is already known and the platform
// connection succeeds.
func TestApply_KnownLocator_ConnectionSucceeds(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "AB12345", []string{"a@b.com"}, "")
	backend := secrets.NewFakeBackend()
	backend.OnCreate = func(secrets.Path) error {
		t.Fatal("Backend.Create must not be called")
		return nil
	}
	fake := &fakeDBPool{t: t}
	mc := coreaccount.NewModuleContext(cr, "ns", nil, nil, nil, fake)

	m := &module{backend: backend, org: "myorg"}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != coreaccount.StateDone {
		t.Errorf("outcome.State = %v, want StateDone", outcome.State)
	}
	if fake.tenantCalls != 1 {
		t.Errorf("TenantAccount called %d times, want 1", fake.tenantCalls)
	}
	if fake.orgAdminCalls != 0 {
		t.Errorf("OrgAdmin called %d times, want 0", fake.orgAdminCalls)
	}
}

// SC-005: Apply aborts with a system error, and issues no SQL, when a
// locator is already known but the platform connection fails.
func TestApply_KnownLocator_ConnectionFails(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "AB12345", []string{"a@b.com"}, "")
	wantErr := errors.New("dial failed")
	backend := secrets.NewFakeBackend()
	backend.OnCreate = func(secrets.Path) error {
		t.Fatal("Backend.Create must not be called")
		return nil
	}
	fake := &fakeDBPool{t: t, tenantErr: wantErr}
	mc := coreaccount.NewModuleContext(cr, "ns", nil, nil, nil, fake)

	m := &module{backend: backend, org: "myorg"}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != coreaccount.StateFailed {
		t.Errorf("outcome.State = %v, want StateFailed", outcome.State)
	}
	if !outcome.Abort {
		t.Error("Abort = false, want true") // SC-016
	}
	if !errors.Is(outcome.Err, wantErr) {
		t.Errorf("outcome.Err = %v, want it to wrap %v", outcome.Err, wantErr)
	}
}

// SC-011: A fresh create aborts with a user error, generating no keypair,
// when spec.contacts is empty.
func TestApply_FreshCreate_EmptyContacts(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "", nil, "")
	backend := secrets.NewFakeBackend()
	backend.OnCreate = func(secrets.Path) error {
		t.Fatal("Backend.Create must not be called")
		return nil
	}
	fake := &fakeDBPool{t: t, forbidCalls: true}
	mc := coreaccount.NewModuleContext(cr, "ns", nil, nil, nil, fake)

	m := &module{backend: backend, org: "myorg"}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != coreaccount.StateRejected {
		t.Errorf("outcome.State = %v, want StateRejected", outcome.State)
	}
	if !outcome.Abort {
		t.Error("Abort = false, want true")
	}
	if !internalerrors.IsUserError(outcome.Err) {
		t.Errorf("expected a user error, got: %v", outcome.Err)
	}
}

// SC-007: A fresh create aborts with a system error, and issues no SQL, when
// the resolved secret path is already occupied.
func TestApply_FreshCreate_SecretPathOccupied(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
	backend := secrets.NewFakeBackend()
	backend.OnCreate = func(secrets.Path) error { return fmt.Errorf("secrets: a secret already exists") }

	fake := &fakeDBPool{t: t, forbidCalls: true} // OrgAdmin must never be reached: no SQL is possible without it.
	mc := coreaccount.NewModuleContext(cr, "ns", nil, nil, nil, fake)

	m := &module{backend: backend, org: "myorg"}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != coreaccount.StateFailed {
		t.Errorf("outcome.State = %v, want StateFailed", outcome.State)
	}
	if !outcome.Abort {
		t.Error("Abort = false, want true")
	}
	if internalerrors.IsUserError(outcome.Err) {
		t.Errorf("expected a system error, got a user error: %v", outcome.Err)
	}
}

// A fresh create aborts with a system error, and never reaches Backend at
// all, when the tenant secret path cannot be built (secrets.NewTenantPath
// validation failure).
func TestApply_FreshCreate_InvalidSecretPath(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
	backend := secrets.NewFakeBackend()
	backend.OnCreate = func(secrets.Path) error {
		t.Fatal("Backend.Create must not be called")
		return nil
	}
	fake := &fakeDBPool{t: t, forbidCalls: true}
	mc := coreaccount.NewModuleContext(cr, "ns", nil, nil, nil, fake)

	m := &module{backend: backend, org: ""} // empty org segment fails NewTenantPath's validation
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != coreaccount.StateFailed {
		t.Errorf("outcome.State = %v, want StateFailed", outcome.State)
	}
	if !outcome.Abort {
		t.Error("Abort = false, want true")
	}
}

// A fresh create aborts with a system error, and issues no SQL, when the
// org-admin connection cannot be opened.
func TestApply_FreshCreate_OrgAdminConnectionFails(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
	wantErr := errors.New("dial failed")
	backend := secrets.NewFakeBackend()
	fake := &fakeDBPool{t: t, orgAdminErr: wantErr}
	mc := coreaccount.NewModuleContext(cr, "ns", nil, nil, nil, fake)

	m := &module{backend: backend, org: "myorg"}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != coreaccount.StateFailed {
		t.Errorf("outcome.State = %v, want StateFailed", outcome.State)
	}
	if !outcome.Abort {
		t.Error("Abort = false, want true")
	}
	if !errors.Is(outcome.Err, wantErr) {
		t.Errorf("outcome.Err = %v, want it to wrap %v", outcome.Err, wantErr)
	}
}

// The account-name-must-start-with-a-letter backstop: a fresh create is
// Rejected, before any SQL is issued, when the resolved account name (built
// from a CRD name that itself starts with a digit) fails the bare-identifier
// check.
func TestApply_FreshCreate_ResolvedNameNotBareIdentifier(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cr := newTestCR("123-team", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
	backend := secrets.NewFakeBackend()
	fake := &fakeDBPool{t: t, orgAdminDB: db}
	mc := coreaccount.NewModuleContext(cr, "ns", nil, nil, nil, fake)

	m := &module{backend: backend, org: "myorg"}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != coreaccount.StateRejected {
		t.Errorf("outcome.State = %v, want StateRejected", outcome.State)
	}
	if !outcome.Abort {
		t.Error("Abort = false, want true")
	}
	if !internalerrors.IsUserError(outcome.Err) {
		t.Errorf("expected a user error, got: %v", outcome.Err)
	}
}

// SC-008's own backstop: a malformed region (one that doesn't transform into
// a valid bare identifier) is Rejected before any SQL is issued.
func TestApply_FreshCreate_InvalidRegion(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cr := newTestCR("acct", "ns", "1-not-a-valid-region", "", []string{"a@b.com"}, "")
	backend := secrets.NewFakeBackend()
	fake := &fakeDBPool{t: t, orgAdminDB: db}
	mc := coreaccount.NewModuleContext(cr, "ns", nil, nil, nil, fake)

	m := &module{backend: backend, org: "myorg"}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != coreaccount.StateRejected {
		t.Errorf("outcome.State = %v, want StateRejected", outcome.State)
	}
	if !outcome.Abort {
		t.Error("Abort = false, want true")
	}
	if !internalerrors.IsUserError(outcome.Err) {
		t.Errorf("expected a user error, got: %v", outcome.Err)
	}
}

// The post-create locator lookup itself failing (a query/connection error,
// not "no match") is a system error.
func TestApply_FreshCreate_LocatorLookupQueryError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cr := newTestCR("acct", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
	backend := secrets.NewFakeBackend()
	fake := &fakeDBPool{t: t, orgAdminDB: db}
	mc := coreaccount.NewModuleContext(cr, "ns", nil, nil, nil, fake)

	mock.ExpectExec(`CREATE ACCOUNT`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SHOW ACCOUNTS LIKE`).WillReturnError(errors.New("connection reset"))

	m := &module{backend: backend, org: "myorg"}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != coreaccount.StateFailed {
		t.Errorf("outcome.State = %v, want StateFailed", outcome.State)
	}
	if !outcome.Abort {
		t.Error("Abort = false, want true")
	}
	if internalerrors.IsUserError(outcome.Err) {
		t.Errorf("expected a system error, got a user error: %v", outcome.Err)
	}
}

// SC-006, SC-010, SC-013, SC-015: a fresh create generates a keypair, stores
// it create-only, then issues CREATE ACCOUNT — in that order — renders EMAIL
// as contacts[0], and calls SetLocator with the locator from the exact,
// case-insensitive match among the SHOW ACCOUNTS rows before returning Done.
func TestApply_FreshCreate_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cr := newTestCR("analytics-team", "finance", "aws-eu-central-1", "", []string{"owner@example.com"}, "team account")
	resolvedName := tenant.ResolveName(cr.Name, cr.Namespace)

	var storeCalled, orgAdminCalledAfterStore bool
	backend := secrets.NewFakeBackend()
	backend.OnCreate = func(path secrets.Path) error {
		storeCalled = true
		stored, _, getErr := backend.Get(context.Background(), path)
		if getErr == nil {
			t.Errorf("Backend.Create stored, but the credential should not be readable via Get before Create returns: %v", stored)
		}
		return nil
	}

	fake := &fakeDBPool{t: t, orgAdminDB: db, onOrgAdmin: func() {
		if !storeCalled {
			t.Fatal("OrgAdminDB was fetched before the platform credential was stored") // SC-006 ordering
		}
		orgAdminCalledAfterStore = true
	}}
	mc := coreaccount.NewModuleContext(cr, "finance", nil, nil, nil, fake)

	createPattern := fmt.Sprintf(
		`^CREATE ACCOUNT %s ADMIN_NAME='platform' ADMIN_RSA_PUBLIC_KEY='.*' ADMIN_USER_TYPE=SERVICE EMAIL='owner@example\.com' EDITION=ENTERPRISE REGION=AWS_EU_CENTRAL_1 COMMENT='team account'$`,
		regexp.QuoteMeta(resolvedName))
	mock.ExpectExec(createPattern).WillReturnResult(sqlmock.NewResult(0, 0))

	showPattern := fmt.Sprintf(`^SHOW ACCOUNTS LIKE '%s'$`, regexp.QuoteMeta(resolvedName))
	mock.ExpectQuery(showPattern).WillReturnRows(
		sqlmock.NewRows([]string{"ACCOUNT_NAME", "ACCOUNT_LOCATOR", "IS_ORG_ADMIN"}).
			AddRow("SOME_OTHER_ACCT", "ZZ00000", true).              // a decoy row LIKE's own wildcard could otherwise match
			AddRow(strings.ToUpper(resolvedName), "AB99999", false), // the real, exact (case-insensitive) match; non-string column exercises the type-skip path
	)

	m := &module{backend: backend, org: "myorg"}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != coreaccount.StateDone {
		t.Fatalf("outcome = %+v, want Done()", outcome)
	}
	if !storeCalled {
		t.Error("Backend.Create was never called")
	}
	if !orgAdminCalledAfterStore {
		t.Error("OrgAdminDB was never fetched")
	}
	if got := mc.Locator(); got != "AB99999" {
		t.Errorf("Locator() = %q, want %q", got, "AB99999")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet SQL expectations: %v", err)
	}
}

// SC-009: CREATE ACCOUNT's COMMENT clause is omitted entirely when
// spec.description is empty.
func TestApply_FreshCreate_EmptyDescriptionOmitsComment(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cr := newTestCR("analytics-team", "finance", "aws-eu-central-1", "", []string{"owner@example.com"}, "")
	resolvedName := tenant.ResolveName(cr.Name, cr.Namespace)

	backend := secrets.NewFakeBackend()
	fake := &fakeDBPool{t: t, orgAdminDB: db}
	mc := coreaccount.NewModuleContext(cr, "finance", nil, nil, nil, fake)

	createPattern := fmt.Sprintf(
		`^CREATE ACCOUNT %s ADMIN_NAME='platform' ADMIN_RSA_PUBLIC_KEY='.*' ADMIN_USER_TYPE=SERVICE EMAIL='owner@example\.com' EDITION=ENTERPRISE REGION=AWS_EU_CENTRAL_1$`,
		regexp.QuoteMeta(resolvedName))
	mock.ExpectExec(createPattern).WillReturnResult(sqlmock.NewResult(0, 0))

	showPattern := fmt.Sprintf(`^SHOW ACCOUNTS LIKE '%s'$`, regexp.QuoteMeta(resolvedName))
	mock.ExpectQuery(showPattern).WillReturnRows(
		sqlmock.NewRows([]string{"account_name", "account_locator"}).
			AddRow(strings.ToUpper(resolvedName), "AB99999"),
	)

	m := &module{backend: backend, org: "myorg"}
	outcome := m.Apply(context.Background(), mc)

	if outcome.State != coreaccount.StateDone {
		t.Fatalf("outcome = %+v, want Done()", outcome)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet SQL expectations (COMMENT clause likely rendered when it shouldn't be): %v", err)
	}
}

// SC-012: a CREATE ACCOUNT failure due to an org-wide name collision is
// classified as a user error; every other CREATE ACCOUNT failure is a
// system error.
func TestApply_FreshCreate_CreateAccountFailureClassification(t *testing.T) {
	cases := []struct {
		name        string
		sfErr       *gosnowflake.SnowflakeError
		wantState   coreaccount.State
		wantUserErr bool
	}{
		{
			name:        "name collision",
			sfErr:       &gosnowflake.SnowflakeError{Number: 2002, SQLState: "42710", Message: "SQL compilation error: object already exists"},
			wantState:   coreaccount.StateRejected,
			wantUserErr: true,
		},
		{
			name:        "other failure",
			sfErr:       &gosnowflake.SnowflakeError{Number: 5, SQLState: "08006", Message: "connection lost"},
			wantState:   coreaccount.StateFailed,
			wantUserErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New() error: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			cr := newTestCR("acct", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
			backend := secrets.NewFakeBackend()
			fake := &fakeDBPool{t: t, orgAdminDB: db}
			mc := coreaccount.NewModuleContext(cr, "ns", nil, nil, nil, fake)

			mock.ExpectExec(`CREATE ACCOUNT`).WillReturnError(tc.sfErr)

			m := &module{backend: backend, org: "myorg"}
			outcome := m.Apply(context.Background(), mc)

			if outcome.State != tc.wantState {
				t.Errorf("outcome.State = %v, want %v", outcome.State, tc.wantState)
			}
			if !outcome.Abort {
				t.Error("Abort = false, want true")
			}
			if got := internalerrors.IsUserError(outcome.Err); got != tc.wantUserErr {
				t.Errorf("IsUserError(outcome.Err) = %v, want %v (err: %v)", got, tc.wantUserErr, outcome.Err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet SQL expectations: %v", err)
			}
		})
	}
}

// SC-014: a fresh create aborts with a system error when the post-create
// locator lookup finds no matching row.
func TestApply_FreshCreate_LocatorLookupNoMatch(t *testing.T) {
	cases := []struct {
		name string
		rows *sqlmock.Rows
	}{
		{"no rows at all", sqlmock.NewRows([]string{"account_name", "account_locator"})},
		{"only a non-matching row", sqlmock.NewRows([]string{"account_name", "account_locator"}).AddRow("SOME_OTHER_ACCT", "ZZ00000")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New() error: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			cr := newTestCR("acct", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
			backend := secrets.NewFakeBackend()
			fake := &fakeDBPool{t: t, orgAdminDB: db}
			mc := coreaccount.NewModuleContext(cr, "ns", nil, nil, nil, fake)

			mock.ExpectExec(`CREATE ACCOUNT`).WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery(`SHOW ACCOUNTS LIKE`).WillReturnRows(tc.rows)

			m := &module{backend: backend, org: "myorg"}
			outcome := m.Apply(context.Background(), mc)

			if outcome.State != coreaccount.StateFailed {
				t.Errorf("outcome.State = %v, want StateFailed", outcome.State)
			}
			if !outcome.Abort {
				t.Error("Abort = false, want true")
			}
			if internalerrors.IsUserError(outcome.Err) {
				t.Errorf("expected a system error, got a user error: %v", outcome.Err)
			}
			if mc.Locator() != "" {
				t.Errorf("Locator() = %q, want empty (SetLocator must not be called on failure)", mc.Locator())
			}
		})
	}
}
