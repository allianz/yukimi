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

package statement

import (
	"context"
	stderrors "errors"
	"reflect"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/snowflakedb/gosnowflake"
)

func newMock(t *testing.T) (*Runner, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New() error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db), mock
}

func TestNew(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error: %v", err)
	}
	defer db.Close()

	r := New(db)
	if r == nil {
		t.Fatal("New(db) = nil, want non-nil *Runner")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("New made an unexpected call: %v", err)
	}
}

func TestRunnerExecSuccess(t *testing.T) {
	r, mock := newMock(t)
	ctx := context.Background()

	mock.ExpectExec(`CREATE ACCOUNT IDENTIFIER(?) ADMIN_RSA_PUBLIC_KEY = ?`).
		WithArgs("test_account", "pubkey").
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := r.Exec(ctx, "create account", `CREATE ACCOUNT IDENTIFIER(?) ADMIN_RSA_PUBLIC_KEY = ?`,
		"test_account", "pubkey"); err != nil {
		t.Fatalf("Exec() error = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestRunnerExecFailureGeneric(t *testing.T) {
	r, mock := newMock(t)
	ctx := context.Background()
	underlying := stderrors.New("connection refused")

	mock.ExpectExec(`ALTER ACCOUNT SET PREVENT_UNLOAD_TO_INLINE_URL = ?`).
		WithArgs("true").
		WillReturnError(underlying)

	err := r.Exec(ctx, "set global parameter", `ALTER ACCOUNT SET PREVENT_UNLOAD_TO_INLINE_URL = ?`, "true")
	if err == nil {
		t.Fatal("Exec() error = nil, want non-nil")
	}

	var stmtErr *Error
	if !stderrors.As(err, &stmtErr) {
		t.Fatalf("Exec() error is not a *statement.Error: %v", err)
	}
	if stmtErr.Label != "set global parameter" {
		t.Errorf("Label = %q, want %q", stmtErr.Label, "set global parameter")
	}
	if stmtErr.Number != 0 || stmtErr.SQLState != "" || stmtErr.QueryID != "" {
		t.Errorf("expected zero-value driver fields for a non-SnowflakeError, got %+v", stmtErr)
	}
	if !stderrors.Is(err, underlying) {
		t.Errorf("errors.Is did not reach the underlying error through Unwrap")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestRunnerExecFailureSnowflakeError(t *testing.T) {
	r, mock := newMock(t)
	ctx := context.Background()
	sfErr := &gosnowflake.SnowflakeError{
		Number:   2003,
		SQLState: "42710",
		QueryID:  "query-abc",
		Message:  "SQL compilation error: object already exists",
	}

	mock.ExpectExec(`CREATE ACCOUNT IDENTIFIER(?)`).WithArgs("dup_account").WillReturnError(sfErr)

	err := r.Exec(ctx, "create account", `CREATE ACCOUNT IDENTIFIER(?)`, "dup_account")

	var stmtErr *Error
	if !stderrors.As(err, &stmtErr) {
		t.Fatalf("Exec() error is not a *statement.Error: %v", err)
	}
	if stmtErr.Number != 2003 || stmtErr.SQLState != "42710" || stmtErr.QueryID != "query-abc" {
		t.Errorf("driver fields not populated from SnowflakeError, got %+v", stmtErr)
	}

	var got *gosnowflake.SnowflakeError
	if !stderrors.As(err, &got) || got != sfErr {
		t.Errorf("errors.As did not reach the original *gosnowflake.SnowflakeError")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestRunnerQuerySuccess(t *testing.T) {
	r, mock := newMock(t)
	ctx := context.Background()

	rows := sqlmock.NewRows([]string{"NAME", "COMMENT"}).
		AddRow("test_policy", "baseline")

	mock.ExpectQuery(`SHOW NETWORK POLICIES LIKE 'test_policy'`).WillReturnRows(rows)

	result, err := r.Query(ctx, "check network policy exists", `SHOW NETWORK POLICIES LIKE 'test_policy'`)
	if err != nil {
		t.Fatalf("Query() error = %v, want nil", err)
	}

	wantColumns := []string{"NAME", "COMMENT"}
	if !reflect.DeepEqual(result.Columns, wantColumns) {
		t.Errorf("Columns = %v, want %v", result.Columns, wantColumns)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("len(Rows) = %d, want 1", len(result.Rows))
	}
	if name, ok := result.Rows[0]["NAME"].(string); !ok || name != "test_policy" {
		t.Errorf("Rows[0][\"NAME\"] = %v, want %q", result.Rows[0]["NAME"], "test_policy")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestRunnerQueryNoRows(t *testing.T) {
	r, mock := newMock(t)
	ctx := context.Background()

	rows := sqlmock.NewRows([]string{"NAME", "COMMENT"})
	mock.ExpectQuery(`SHOW NETWORK POLICIES LIKE 'missing'`).WillReturnRows(rows)

	result, err := r.Query(ctx, "check network policy exists", `SHOW NETWORK POLICIES LIKE 'missing'`)
	if err != nil {
		t.Fatalf("Query() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(result, Result{}) {
		t.Errorf("Query() = %+v, want the zero-value Result{}", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestRunnerQueryFailureOnQueryContext(t *testing.T) {
	r, mock := newMock(t)
	ctx := context.Background()
	underlying := context.DeadlineExceeded

	mock.ExpectQuery(`SHOW NETWORK POLICIES LIKE 'test_policy'`).WillReturnError(underlying)

	_, err := r.Query(ctx, "check network policy exists", `SHOW NETWORK POLICIES LIKE 'test_policy'`)
	var stmtErr *Error
	if !stderrors.As(err, &stmtErr) {
		t.Fatalf("Query() error is not a *statement.Error: %v", err)
	}
	if !stderrors.Is(err, underlying) {
		t.Errorf("errors.Is did not reach the underlying error through Unwrap")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestRunnerQueryMidIterationRowsErrSurfacesAsError(t *testing.T) {
	r, mock := newMock(t)
	ctx := context.Background()
	rowFailure := stderrors.New("network reset mid-stream")

	rows := sqlmock.NewRows([]string{"NAME"}).
		AddRow("first_policy").
		AddRow("second_policy").
		RowError(1, rowFailure)

	mock.ExpectQuery(`SHOW NETWORK POLICIES LIKE '%'`).WillReturnRows(rows)

	result, err := r.Query(ctx, "check network policy exists", `SHOW NETWORK POLICIES LIKE '%'`)
	if err == nil {
		t.Fatalf("Query() error = nil, want non-nil after a mid-iteration rows.Err() failure")
	}
	if !reflect.DeepEqual(result, Result{}) {
		t.Errorf("Query() returned a non-empty partial Result on failure: %+v", result)
	}

	var stmtErr *Error
	if !stderrors.As(err, &stmtErr) {
		t.Fatalf("Query() error is not a *statement.Error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
