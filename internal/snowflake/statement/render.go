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
	"fmt"
	"regexp"
	"strings"

	"github.com/allianz/yukimi/internal/errors"
)

var bareIdentifierPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// QuoteIdentifier double-quotes name for use as a rendered SQL identifier,
// doubling any embedded double quote. Use only where IDENTIFIER(?) binding
// has been confirmed, at the calling module's spec-writing time, not to
// work for that statement position (e.g. CREATE ACCOUNT's account name, if
// found unsupported there — notes-snowflake-sql-mechanics.md §7).
func QuoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// QuoteLiteral single-quotes s for use as a rendered SQL string literal,
// doubling any embedded single quote. Its primary caller is
// SHOW ... LIKE '<pattern>', since whether SHOW accepts a bind for its
// pattern at all is unverified (notes-snowflake-sql-mechanics.md §7) —
// assume rendered.
func QuoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// BareIdentifier validates name as a bare, unquoted SQL token and returns
// it unchanged, or a user error if it does not match the expected charset.
// Its one known caller is the parameter name in ALTER ACCOUNT SET <param> =
// <value>: that position is keyword-like rather than a true object name, so
// neither IDENTIFIER(?) nor quoting is believed to apply
// (notes-snowflake-sql-mechanics.md §7) — this check is the only defense
// against an operator-supplied parameter name reaching SQL text unescaped,
// and is the load-bearing rendering case in this package.
func BareIdentifier(name string) (string, error) {
	if !bareIdentifierPattern.MatchString(name) {
		return "", errors.NewUserError(fmt.Sprintf(
			"Parameter name '%s' is not a valid bare identifier (expected: letters, digits, underscore, starting with a letter)",
			name))
	}
	return name, nil
}
