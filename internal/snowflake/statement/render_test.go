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
	"strings"
	"testing"

	"github.com/allianz/yukimi/internal/errors"
)

func TestQuoteIdentifier(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "MY_ACCOUNT", want: `"MY_ACCOUNT"`},
		{name: "embedded double quote is doubled", in: `we"ird`, want: `"we""ird"`},
		{name: "empty", in: "", want: `""`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := QuoteIdentifier(tt.in); got != tt.want {
				t.Errorf("QuoteIdentifier(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestQuoteLiteral(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "my-pattern", want: "'my-pattern'"},
		{name: "embedded single quote is doubled", in: "o'brien", want: "'o''brien'"},
		{name: "empty", in: "", want: "''"},
		{name: "embedded backslash is doubled", in: `back\slash`, want: `'back\\slash'`},
		// Snowflake treats backslash as an escape character inside single-quoted
		// literals: a trailing, un-doubled backslash would let it escape — rather
		// than terminate on — the literal's own closing quote, letting s run on
		// into whatever SQL text follows it (confirmed live against a real
		// account; see QuoteLiteral's doc comment).
		{name: "trailing backslash does not escape the closing quote", in: `x\`, want: `'x\\'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := QuoteLiteral(tt.in); got != tt.want {
				t.Errorf("QuoteLiteral(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestBareIdentifier(t *testing.T) {
	validTests := []string{
		"STATEMENT_TIMEOUT",
		"a",
		"A1",
		"snake_case_1",
	}
	for _, in := range validTests {
		t.Run("valid/"+in, func(t *testing.T) {
			got, err := BareIdentifier(in)
			if err != nil {
				t.Fatalf("BareIdentifier(%q) returned unexpected error: %v", in, err)
			}
			if got != in {
				t.Errorf("BareIdentifier(%q) = %q, want unchanged", in, got)
			}
		})
	}

	invalidTests := []struct {
		name string
		in   string
	}{
		{name: "starts with digit", in: "1PARAM"},
		{name: "contains whitespace", in: "STATEMENT TIMEOUT"},
		{name: "contains quote", in: `STATEMENT_TIMEOUT"`},
		{name: "sql injection attempt", in: "STATEMENT_TIMEOUT; DROP TABLE X"},
		{name: "empty", in: ""},
		{name: "contains dash", in: "STATEMENT-TIMEOUT"},
	}
	for _, tt := range invalidTests {
		t.Run("invalid/"+tt.name, func(t *testing.T) {
			_, err := BareIdentifier(tt.in)
			if err == nil {
				t.Fatalf("BareIdentifier(%q) = nil error, want a user error", tt.in)
			}
			if !errors.IsUserError(err) {
				t.Errorf("BareIdentifier(%q) error is not a user error: %v", tt.in, err)
			}
		})
	}
}

// unquoteDoubled decodes a value produced by QuoteIdentifier/QuoteLiteral per
// the SQL doubled-quote escaping convention: q wraps the value, and every
// embedded q is doubled. ok is false if s is not validly quoted this way -
// in particular if a lone, unescaped q appears before the closing quote,
// which would let the encoded value break out of its quoted token early.
func unquoteDoubled(s string, q byte) (decoded string, ok bool) {
	if len(s) < 2 || s[0] != q || s[len(s)-1] != q {
		return "", false
	}
	inner := s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(inner); i++ {
		if inner[i] != q {
			b.WriteByte(inner[i])
			continue
		}
		if i+1 < len(inner) && inner[i+1] == q {
			b.WriteByte(q)
			i++
			continue
		}
		return "", false
	}
	return b.String(), true
}

func FuzzQuoteIdentifier(f *testing.F) {
	f.Add(`MY_ACCOUNT`)
	f.Add(`we"ird`)
	f.Add(``)
	f.Add(`"; DROP TABLE X; --`)
	f.Add(`""""`)
	f.Fuzz(func(t *testing.T, name string) {
		quoted := QuoteIdentifier(name)
		decoded, ok := unquoteDoubled(quoted, '"')
		if !ok {
			t.Fatalf("QuoteIdentifier(%q) = %q does not decode as a validly quoted identifier", name, quoted)
		}
		if decoded != name {
			t.Fatalf("QuoteIdentifier(%q) = %q, round-trip decoded to %q", name, quoted, decoded)
		}
	})
}

// unquoteLiteral decodes a value produced by QuoteLiteral: single quotes
// wrap it, and both an embedded single quote and an embedded backslash are
// doubled (see QuoteLiteral's doc comment on why backslash needs its own
// escaping, distinct from unquoteDoubled's identifier-only convention). ok
// is false if s is not validly escaped this way — in particular if a lone,
// unescaped quote or backslash appears before the closing quote, which
// would let the encoded value break out of its literal early.
func unquoteLiteral(s string) (decoded string, ok bool) {
	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		return "", false
	}
	inner := s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(inner); i++ {
		switch inner[i] {
		case '\'', '\\':
			if i+1 < len(inner) && inner[i+1] == inner[i] {
				b.WriteByte(inner[i])
				i++
				continue
			}
			return "", false
		default:
			b.WriteByte(inner[i])
		}
	}
	return b.String(), true
}

func FuzzQuoteLiteral(f *testing.F) {
	f.Add(`my-pattern`)
	f.Add(`o'brien`)
	f.Add(``)
	f.Add(`'; DROP TABLE X; --`)
	f.Add(`''''`)
	f.Add(`x\`)
	f.Add(`back\slash`)
	f.Add(`\\\\`)
	f.Add(`\'; DROP TABLE X; --`)
	f.Fuzz(func(t *testing.T, s string) {
		quoted := QuoteLiteral(s)
		decoded, ok := unquoteLiteral(quoted)
		if !ok {
			t.Fatalf("QuoteLiteral(%q) = %q does not decode as a validly quoted literal", s, quoted)
		}
		if decoded != s {
			t.Fatalf("QuoteLiteral(%q) = %q, round-trip decoded to %q", s, quoted, decoded)
		}
	})
}

func FuzzBareIdentifier(f *testing.F) {
	f.Add("STATEMENT_TIMEOUT")
	f.Add("1PARAM")
	f.Add("")
	f.Add("STATEMENT_TIMEOUT; DROP TABLE X")
	f.Add(`STATEMENT_TIMEOUT"`)
	f.Fuzz(func(t *testing.T, name string) {
		got, err := BareIdentifier(name)
		if err != nil {
			if !errors.IsUserError(err) {
				t.Fatalf("BareIdentifier(%q) returned a non-user error: %v", name, err)
			}
			return
		}
		if got != name {
			t.Fatalf("BareIdentifier(%q) = %q, want unchanged", name, got)
		}
		for _, r := range got {
			if r > 127 {
				t.Fatalf("BareIdentifier(%q) accepted non-ASCII rune %q as a bare identifier", name, r)
			}
		}
	})
}
