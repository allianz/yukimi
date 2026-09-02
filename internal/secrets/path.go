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

package secrets

import (
	"fmt"
	"regexp"

	"github.com/allianz/yukimi/internal/errors"
)

// segmentPattern is the allowed shape for every path segment. It rejects an
// empty segment (the + quantifier requires at least one character) and any
// segment containing '/', '.', or '..' as a side effect of excluding every
// character outside this class — a dot is never in the allowed set, so '..'
// needs no separate check.
var segmentPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// validateSegment rejects an empty segment, or one containing '/', '.', '..',
// or a byte outside [A-Za-z0-9_-]. name identifies the segment's role in the
// resulting error message.
func validateSegment(name, value string) error {
	if !segmentPattern.MatchString(value) {
		return errors.NewUserError(fmt.Sprintf(
			"invalid secrets path segment %q for %s: must be non-empty and contain only letters, digits, '_', or '-'",
			value, name))
	}
	return nil
}

// Path is an opaque, pre-validated secret path. The zero value is not valid;
// only NewTenantPath and NewOrgAdminPath produce one.
type Path struct {
	value string
}

// NewTenantPath builds the tenant platform-credential path (design.md 3.11.1):
// snowflake/tenant/<org>/<namespace>/<accountName>/platform-credentials.
//
// Parameters:
//   - org: Snowflake organization name (Config.Snowflake.Org, 002)
//   - namespace: Kubernetes namespace — MUST come from metadata.namespace at
//     the call site, never a spec field (design.md 3.11.1)
//   - accountName: the CRD's metadata.name — MUST NOT be the resolved,
//     hash-suffixed Snowflake account name from design.md 3.12
//
// Returns:
//   - User error if any segment is empty or contains '/', '.', '..', or a
//     character outside [A-Za-z0-9_-]
func NewTenantPath(org, namespace, accountName string) (Path, error) {
	for _, seg := range []struct{ name, value string }{
		{"org", org},
		{"namespace", namespace},
		{"accountName", accountName},
	} {
		if err := validateSegment(seg.name, seg.value); err != nil {
			return Path{}, err
		}
	}
	return Path{value: fmt.Sprintf("snowflake/tenant/%s/%s/%s/platform-credentials", org, namespace, accountName)}, nil
}

// NewOrgAdminPath builds the org-admin credential path:
// snowflake/org/<org>/<orgAdminAccount>/org-admin-credentials.
//
// Parameters:
//   - org: Config.Snowflake.Org (002)
//   - orgAdminAccount: Config.Snowflake.OrgAdminAccount (002)
//
// Returns:
//   - User error under the same validation rule as NewTenantPath
func NewOrgAdminPath(org, orgAdminAccount string) (Path, error) {
	for _, seg := range []struct{ name, value string }{
		{"org", org},
		{"orgAdminAccount", orgAdminAccount},
	} {
		if err := validateSegment(seg.name, seg.value); err != nil {
			return Path{}, err
		}
	}
	return Path{value: fmt.Sprintf("snowflake/org/%s/%s/org-admin-credentials", org, orgAdminAccount)}, nil
}

// String returns the path for logging. It never contains secret material —
// only the identifiers that make up the path itself.
func (p Path) String() string {
	return p.value
}
