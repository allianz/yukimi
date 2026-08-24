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
	"database/sql"
)

// Executor is the subset of *sql.DB this package needs. *sql.DB, *sql.Conn
// and *sql.Tx all satisfy it, as does DATA-DOG/go-sqlmock's driver in tests.
// This package never imports internal/snowflake/pool (004); 004 hands one
// of these in.
type Executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Runner executes statements against a fixed Executor. Safe for concurrent
// use exactly when the Executor is (true of *sql.DB; not of a shared *sql.Tx).
type Runner struct {
	exec Executor
}

// New wraps exec for statement execution. Makes no call of its own.
func New(exec Executor) *Runner {
	return &Runner{exec: exec}
}

// Exec runs one statement that returns no rows (DDL, and non-SELECT DML).
// args are bound positionally with ? or IDENTIFIER(?); use the renderers
// in render.go only where binding a given position is not possible.
func (r *Runner) Exec(ctx context.Context, label, sql string, args ...any) error {
	if _, err := r.exec.ExecContext(ctx, sql, args...); err != nil {
		return newError(label, sql, err)
	}
	return nil
}

// Result is a materialized row-returning query result. Deliberately thin:
// no accessor or coercion methods. Every caller already knows its own
// query's column shape and casts at the call site, comma-ok
// (e.g. name, ok := row["NAME"].(string)).
type Result struct {
	Columns []string
	Rows    []map[string]any
}

// Query runs one row-returning statement (SHOW ... LIKE existence checks,
// drift read-backs) and materializes every row before returning, so callers
// never drive rows.Next()/rows.Err() themselves.
//
// Returns Result{}, nil for a query that matches no rows — not an error.
func (r *Runner) Query(ctx context.Context, label, sql string, args ...any) (Result, error) {
	rows, err := r.exec.QueryContext(ctx, sql, args...)
	if err != nil {
		return Result{}, newError(label, sql, err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return Result{}, newError(label, sql, err)
	}

	var matched []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return Result{}, newError(label, sql, err)
		}

		row := make(map[string]any, len(columns))
		for i, col := range columns {
			row[col] = values[i]
		}
		matched = append(matched, row)
	}
	if err := rows.Err(); err != nil {
		return Result{}, newError(label, sql, err)
	}

	if len(matched) == 0 {
		return Result{}, nil
	}
	return Result{Columns: columns, Rows: matched}, nil
}
