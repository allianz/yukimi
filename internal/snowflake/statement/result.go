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

import "strings"

// Row is one materialized row, keyed by column name as the driver reported
// it. A nil Row means "no such row": every accessor below returns its zero
// value and false on it, so lookups chain without intermediate checks.
// Because Row is a map, callers may still index or range over it directly.
type Row map[string]any

// Result is a materialized row-returning query result.
type Result struct {
	Columns []string
	Rows    []Row
}

// FindRow returns the first row whose column holds a string equal to value,
// or nil if there is none. Both the column name and value are compared
// ignoring case (the way Snowflake treats unquoted identifiers). It is the
// exact-match step every SHOW ... LIKE check needs, since LIKE treats "_" as
// a wildcard. It never returns an error: if several rows match ignoring
// case, the first wins.
func (r Result) FindRow(column, value string) Row {
	for _, row := range r.Rows {
		if s, ok := row.StringValue(column); ok && strings.EqualFold(s, value) {
			return row
		}
	}
	return nil
}

// Found reports whether the row exists, i.e. whether FindRow matched.
func (r Row) Found() bool {
	return r != nil
}

// StringValue returns the column's value if it is a string. The column name
// is matched ignoring case. ok is false for a nil Row, a missing column, a
// NULL, or a non-string value; an empty string is returned as ("", true), so
// a caller that treats empty as absent adds its own `&& value != ""`.
func (r Row) StringValue(column string) (value string, ok bool) {
	raw, ok := r.Value(column)
	if !ok {
		return "", false
	}
	value, ok = raw.(string)
	return value, ok
}

// Value returns the column's raw value, for types StringValue does not
// cover (e.g. a timestamp). ok is false only for a nil Row or a missing
// column — a NULL is (nil, true). An exact column name is tried first, then
// a case-insensitive scan, since the driver's casing for SHOW output columns
// is not guaranteed.
func (r Row) Value(column string) (value any, ok bool) {
	if v, found := r[column]; found {
		return v, true
	}
	for key, v := range r {
		if strings.EqualFold(key, column) {
			return v, true
		}
	}
	return nil, false
}
