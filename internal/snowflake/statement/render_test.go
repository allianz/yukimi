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
