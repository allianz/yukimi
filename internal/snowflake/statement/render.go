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
// doubling any embedded double quote. Binding the name via IDENTIFIER(?) must
// be tried first; use this only once an integration test shows that bound
// attempt failing against live Snowflake (see specs/005, Verified Bind
// Positions). No such position is confirmed yet.
func QuoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// QuoteLiteral single-quotes s for use as a rendered SQL string literal,
// doubling any embedded single quote and any embedded backslash. The
// backslash doubling matters because Snowflake, unlike standard SQL, treats
// backslash as an escape character inside single-quoted literals: an
// un-doubled trailing backslash would let its own closing quote be consumed
// as an escaped literal quote rather than the literal's terminator, letting
// s run on into whatever SQL text follows (confirmed live — see
// notes-snowflake-sql-mechanics.md §7). Binding the value with ? must be
// tried first; use this only once an integration test shows that bound
// attempt failing against live Snowflake (see specs/005, Verified Bind
// Positions). The expected callers are the ALTER ACCOUNT SET value and the
// CREATE SECURITY INTEGRATION family.
func QuoteLiteral(s string) string {
	escaped := strings.ReplaceAll(s, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, "'", "''")
	return "'" + escaped + "'"
}

// BareIdentifier validates name as a bare, unquoted SQL token and returns
// it unchanged, or a user error if it does not match the expected charset.
// Its confirmed callers are the parameter name in ALTER ACCOUNT SET <param> =
// <value> and the key slot name in ALTER USER ... SET <slot> = <key>:
// integration tests show that both ? and IDENTIFIER(?) fail there with a
// syntax error. Those positions are keyword-like rather than true object
// names, so this check is the only defense against an operator-supplied name
// reaching SQL text unescaped. It must not be used at any position without
// such a failing test.
func BareIdentifier(name string) (string, error) {
	if !bareIdentifierPattern.MatchString(name) {
		return "", errors.NewUserError(fmt.Sprintf(
			"Parameter name '%s' is not a valid bare identifier (expected: letters, digits, underscore, starting with a letter)",
			name))
	}
	return name, nil
}
