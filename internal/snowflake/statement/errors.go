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
	stderrors "errors"
	"fmt"

	"github.com/snowflakedb/gosnowflake"
)

// Error decorates a statement failure with structured context, never with
// the bound args — bind-first keeps sensitive values (an RSA public key
// today, more later) out of statement text, so putting them back into the
// error for "debuggability" is the wrong turn this type exists to close off.
//
// Number, SQLState and QueryID are populated only when the underlying error
// is a *gosnowflake.SnowflakeError (checked via errors.As); any other
// failure — a context deadline, a network error — leaves them zero/empty
// and this decorates with Label and Statement alone.
type Error struct {
	Label     string // caller-supplied, e.g. "create account"
	Statement string // the statement text; safe to log, args are bound separately
	QueryID   string // gosnowflake.SnowflakeError.QueryID; empty if Err isn't one
	Number    int    // gosnowflake.SnowflakeError.Number; 0 if Err isn't one
	SQLState  string // gosnowflake.SnowflakeError.SQLState; empty if Err isn't one
	Err       error  // the error returned by Executor or by row scanning
}

// newError decorates err with the caller-supplied label and statement text,
// plus the driver's structured fields when err is a *gosnowflake.SnowflakeError.
func newError(label, statement string, err error) *Error {
	e := &Error{Label: label, Statement: statement, Err: err}

	var sfErr *gosnowflake.SnowflakeError
	if stderrors.As(err, &sfErr) {
		e.Number = sfErr.Number
		e.SQLState = sfErr.SQLState
		e.QueryID = sfErr.QueryID
	}

	return e
}

// Error renders a one-line summary — label, driver code when present, and
// the underlying message — and never the bound args, e.g.:
//
//	create account: failed (Number=2003 SQLState=42710): SQL compilation error: ...
//	create account: failed: context deadline exceeded
func (e *Error) Error() string {
	var sfErr *gosnowflake.SnowflakeError
	if stderrors.As(e.Err, &sfErr) {
		return fmt.Sprintf("%s: failed (Number=%d SQLState=%s): %s", e.Label, e.Number, e.SQLState, e.Err)
	}
	return fmt.Sprintf("%s: failed: %s", e.Label, e.Err)
}

// Unwrap returns Err, so errors.Is and errors.As reach the original
// Executor/scan error through this wrapper.
func (e *Error) Unwrap() error {
	return e.Err
}
