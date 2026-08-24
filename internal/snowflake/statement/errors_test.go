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
	"strings"
	"testing"

	"github.com/snowflakedb/gosnowflake"
)

func TestNewErrorWithSnowflakeError(t *testing.T) {
	sfErr := &gosnowflake.SnowflakeError{
		Number:   2003,
		SQLState: "42710",
		QueryID:  "query-123",
		Message:  "SQL compilation error: object already exists",
	}

	err := newError("create account", `CREATE ACCOUNT IDENTIFIER(?)`, sfErr)

	if err.Label != "create account" {
		t.Errorf("Label = %q, want %q", err.Label, "create account")
	}
	if err.Statement != `CREATE ACCOUNT IDENTIFIER(?)` {
		t.Errorf("Statement = %q, want the statement text", err.Statement)
	}
	if err.Number != 2003 {
		t.Errorf("Number = %d, want 2003", err.Number)
	}
	if err.SQLState != "42710" {
		t.Errorf("SQLState = %q, want %q", err.SQLState, "42710")
	}
	if err.QueryID != "query-123" {
		t.Errorf("QueryID = %q, want %q", err.QueryID, "query-123")
	}

	msg := err.Error()
	if !strings.Contains(msg, "create account: failed (Number=2003 SQLState=42710):") {
		t.Errorf("Error() = %q, want it to contain the driver code prefix", msg)
	}
	if !strings.Contains(msg, "SQL compilation error") {
		t.Errorf("Error() = %q, want it to contain the underlying message", msg)
	}
}

func TestNewErrorWithGenericError(t *testing.T) {
	err := newError("set global parameter", `ALTER ACCOUNT SET ?`, context.DeadlineExceeded)

	if err.Number != 0 {
		t.Errorf("Number = %d, want 0 for a non-SnowflakeError", err.Number)
	}
	if err.SQLState != "" {
		t.Errorf("SQLState = %q, want empty for a non-SnowflakeError", err.SQLState)
	}
	if err.QueryID != "" {
		t.Errorf("QueryID = %q, want empty for a non-SnowflakeError", err.QueryID)
	}

	want := "set global parameter: failed: context deadline exceeded"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestErrorNeverIncludesBoundArgs(t *testing.T) {
	err := newError("create account", `CREATE ACCOUNT IDENTIFIER(?) ADMIN_RSA_PUBLIC_KEY = ?`,
		stderrors.New("SQL compilation error"))

	msg := err.Error()
	if strings.Contains(msg, "SECRET-RSA-KEY") {
		t.Fatalf("Error() leaked a bound arg value: %q", msg)
	}
}

func TestErrorUnwrap(t *testing.T) {
	sfErr := &gosnowflake.SnowflakeError{Number: 1, SQLState: "X", QueryID: "q"}
	err := newError("label", "sql", sfErr)

	var got *gosnowflake.SnowflakeError
	if !stderrors.As(err, &got) {
		t.Fatal("errors.As did not reach the wrapped *gosnowflake.SnowflakeError")
	}
	if got != sfErr {
		t.Errorf("errors.As resolved to a different error instance")
	}

	if stderrors.Unwrap(error(err)) != sfErr {
		t.Errorf("Unwrap() did not return the original error")
	}
}
