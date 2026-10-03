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
	"strings"

	"github.com/allianz/yukimi/internal/errors"
)

// maxIdentifierLen is Azure Key Vault's secret-name length limit — the
// tightest of the supported secrets-manager backends' constraints, so it is
// the shared budget every Identifier must fit within.
const maxIdentifierLen = 127

// segmentPattern is the allowed shape for every identifier segment. It
// rejects an empty segment, a segment starting with '-' or '_', and any
// segment containing '/' or '.' as a side effect of excluding every
// character outside this class.
var segmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// separatorRunPattern matches a run of 2 or more '-'/'_' characters. Both map
// to '-' in the final identifier (toSecretSafe), and the identifier joins
// segments with "--" — so a segment containing such a run would be
// indistinguishable from a segment boundary, breaking the guarantee that two
// different (org, namespace, accountName) or (org, orgAdminAccount) tuples
// never produce the same identifier.
var separatorRunPattern = regexp.MustCompile(`[-_]{2,}`)

// validateSegment rejects an empty segment, one starting with '-' or '_', one
// containing '/', '.', a run of 2+ '-'/'_' characters, or a byte outside
// [A-Za-z0-9_-]. name identifies the segment's role in the resulting error
// message.
func validateSegment(name, value string) error {
	if !segmentPattern.MatchString(value) || separatorRunPattern.MatchString(value) {
		return errors.NewUserError(fmt.Sprintf(
			"invalid secret identifier segment %q for %s: must start with a letter or digit and contain only "+
				"letters, digits, single '_', or single '-' (no repeated '-'/'_')",
			value, name))
	}
	return nil
}

// toSecretSafe maps a validated segment onto the charset shared by every
// supported secrets-manager backend (letters, digits, '-'): Azure Key Vault
// secret names disallow '_', which is otherwise the only character
// validateSegment permits beyond that charset.
func toSecretSafe(s string) string {
	return strings.ReplaceAll(s, "_", "-")
}

// checkLen rejects an identifier that would exceed maxIdentifierLen. This is
// a system error, not a user error: every segment length this package can
// see is already bounded tightly enough elsewhere (the CRD's metadata.name
// cap, Kubernetes' namespace cap, and 012's org+accountName check) that this
// should never actually trigger — if it does, that bound has drifted out of
// sync with this package, which is an operator's problem to reconcile, not
// something fixable by editing a CRD or base.yaml.
func checkLen(value string) error {
	if len(value) > maxIdentifierLen {
		return fmt.Errorf(
			"secret identifier %q is %d characters, which exceeds the %d-character limit shared by supported "+
				"secrets-manager backends",
			value, len(value), maxIdentifierLen)
	}
	return nil
}

// Identifier is an opaque, pre-validated secret identifier. The zero value is
// not valid; only NewTenantIdentifier and NewOrgAdminIdentifier produce one.
type Identifier struct {
	value string
}

// NewTenantIdentifier builds the tenant platform-credential identifier
// (design.md 3.11.1): yk-<org>--<namespace>--<accountName>.
//
// Parameters:
//   - org: Snowflake organization name (Config.Snowflake.Org, 002)
//   - namespace: Kubernetes namespace — MUST come from metadata.namespace at
//     the call site, never a spec field (design.md 3.11.1)
//   - accountName: the CRD's metadata.name — MUST NOT be the resolved,
//     hash-suffixed Snowflake account name from design.md 3.12
//
// Returns:
//   - User error if any segment is empty, starts with '-'/'_', contains '/',
//     '.', a repeated '-'/'_', or a character outside [A-Za-z0-9_-]
//   - System error if the resulting identifier exceeds maxIdentifierLen —
//     every caller-visible length is already bounded tightly enough
//     elsewhere that this should never actually happen (see checkLen)
func NewTenantIdentifier(org, namespace, accountName string) (Identifier, error) {
	for _, seg := range []struct{ name, value string }{
		{"org", org},
		{"namespace", namespace},
		{"accountName", accountName},
	} {
		if err := validateSegment(seg.name, seg.value); err != nil {
			return Identifier{}, err
		}
	}
	value := fmt.Sprintf("yk-%s--%s--%s", toSecretSafe(org), toSecretSafe(namespace), toSecretSafe(accountName))
	if err := checkLen(value); err != nil {
		return Identifier{}, err
	}
	return Identifier{value: value}, nil
}

// NewOrgAdminIdentifier builds the org-admin credential identifier:
// yk-orgadmin--<org>--<orgAdminAccount>.
//
// Parameters:
//   - org: Config.Snowflake.Org (002)
//   - orgAdminAccount: Config.Snowflake.OrgAdminAccount (002)
//
// Returns:
//   - User error under the same validation rule as NewTenantIdentifier
func NewOrgAdminIdentifier(org, orgAdminAccount string) (Identifier, error) {
	for _, seg := range []struct{ name, value string }{
		{"org", org},
		{"orgAdminAccount", orgAdminAccount},
	} {
		if err := validateSegment(seg.name, seg.value); err != nil {
			return Identifier{}, err
		}
	}
	value := fmt.Sprintf("yk-orgadmin--%s--%s", toSecretSafe(org), toSecretSafe(orgAdminAccount))
	if err := checkLen(value); err != nil {
		return Identifier{}, err
	}
	return Identifier{value: value}, nil
}

// String returns the identifier for logging. It never contains secret
// material — only the identifiers that make it up.
func (i Identifier) String() string {
	return i.value
}
