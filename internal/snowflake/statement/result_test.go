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

import "testing"

func TestResultFindRow(t *testing.T) {
	result := Result{
		Columns: []string{"NAME", "COMMENT"},
		Rows: []Row{
			{"NAME": "tenantXa", "COMMENT": "near match"},
			{"NAME": "TENANT_A", "COMMENT": "first exact"},
			{"NAME": "tenant_a", "COMMENT": "second exact"},
			{"NAME": nil, "COMMENT": "null name"},
		},
	}

	tests := []struct {
		name        string
		result      Result
		column      string
		value       string
		wantComment string // "" with wantFound false means no row expected
		wantFound   bool
	}{
		{"exact value, case-insensitive", result, "NAME", "tenant_a", "first exact", true},
		{"column name case-insensitive", result, "name", "TENANT_A", "first exact", true},
		{"near match is not a match", Result{Rows: []Row{{"NAME": "tenantXa"}}}, "name", "tenant_a", "", false},
		{"no matching row", result, "NAME", "missing", "", false},
		{"missing column", result, "NOPE", "tenant_a", "", false},
		{"empty result", Result{}, "NAME", "tenant_a", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := tc.result.FindRow(tc.column, tc.value)
			if row.Found() != tc.wantFound {
				t.Fatalf("Found() = %v, want %v", row.Found(), tc.wantFound)
			}
			comment, ok := row.StringValue("comment")
			if ok != tc.wantFound || comment != tc.wantComment {
				t.Errorf("StringValue(comment) = (%q, %v), want (%q, %v)", comment, ok, tc.wantComment, tc.wantFound)
			}
		})
	}
}

func TestRowStringValue(t *testing.T) {
	row := Row{"Name": "x", "EMPTY": "", "NULLED": nil, "COUNT": int64(3)}

	tests := []struct {
		name   string
		row    Row
		column string
		want   string
		wantOK bool
	}{
		{"exact column", row, "Name", "x", true},
		{"column case-insensitive", row, "NAME", "x", true},
		{"empty string is present", row, "empty", "", true},
		{"NULL is not a string", row, "NULLED", "", false},
		{"non-string value", row, "COUNT", "", false},
		{"missing column", row, "nope", "", false},
		{"nil row", nil, "Name", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.row.StringValue(tc.column)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("StringValue(%q) = (%q, %v), want (%q, %v)", tc.column, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestRowValue(t *testing.T) {
	row := Row{"COUNT": int64(3), "NULLED": nil}

	if got, ok := row.Value("count"); !ok || got != int64(3) {
		t.Errorf("Value(count) = (%v, %v), want (3, true)", got, ok)
	}
	if got, ok := row.Value("NULLED"); !ok || got != nil {
		t.Errorf("Value(NULLED) = (%v, %v), want (nil, true)", got, ok)
	}
	if got, ok := row.Value("nope"); ok || got != nil {
		t.Errorf("Value(nope) = (%v, %v), want (nil, false)", got, ok)
	}
	var nilRow Row
	if got, ok := nilRow.Value("COUNT"); ok || got != nil {
		t.Errorf("nil Row Value(COUNT) = (%v, %v), want (nil, false)", got, ok)
	}
}

func TestFindRowChainOnNoMatchDoesNotPanic(t *testing.T) {
	value, ok := Result{}.FindRow("name", "x").StringValue("email")
	if value != "" || ok {
		t.Errorf("chain on empty Result = (%q, %v), want (\"\", false)", value, ok)
	}
}
