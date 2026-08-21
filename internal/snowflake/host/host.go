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

package host

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/allianz/yukimi/internal/errors"
)

const (
	privateLinkSuffix = ".privatelink.snowflakecomputing.com"
	publicSuffix      = ".snowflakecomputing.com"
)

// regionPattern requires a leading cloud segment of at least 3 characters
// (every plausible cloud identifier — "aws", "azure", "gcp" — is at least
// that long) followed by one or more hyphen-separated region segments. This
// rejects a bare region string missing its cloud prefix (e.g.
// "eu-central-1": its leading segment "eu" is only 2 characters, an AWS
// geographic region code, not a cloud identifier) without maintaining an
// allowlist of specific cloud names.
var regionPattern = regexp.MustCompile(`^[a-z][a-z0-9]{2,}-[a-z0-9]+(-[a-z0-9]+)*$`)

// regionSegment returns the hostname segment for region, e.g.
// "eu-central-1" for "aws-eu-central-1" or "eu-west-3.aws" for
// "aws-eu-west-3". Most regions repeat the cloud as a trailing segment after
// the region; "aws-eu-central-1" is the one known exception and needs no
// suffix.
func regionSegment(region string) (string, error) {
	if !regionPattern.MatchString(region) {
		return "", errors.NewUserError(fmt.Sprintf(
			"region '%s' does not match the expected cloud-region format (expected: aws-eu-central-1)", region))
	}
	switch region {
	case "aws-eu-central-1":
		return "eu-central-1", nil
	default:
		idx := strings.IndexByte(region, '-')
		return region[idx+1:] + "." + region[:idx], nil
	}
}

// Hostname returns the Snowflake connection host for an account, e.g.
// "xc19114.eu-central-1.privatelink.snowflakecomputing.com".
//
// Parameters:
//   - locator: the Snowflake account locator (design.md 3.6), e.g. "xc19114";
//     opaque, and never validated here
//   - region: the account's cloud-region string (e.g. "aws-eu-central-1",
//     design.md 3.1)
//   - usePrivateLink: selects the .privatelink.snowflakecomputing.com suffix
//     over .snowflakecomputing.com; the caller decides (today from
//     BaseConfig.Snowflake.UsePrivateLink, 002), never this package
//
// Returns:
//   - the host, or an empty string and a user error if region does not match
//     the expected cloud-region format
func Hostname(locator, region string, usePrivateLink bool) (string, error) {
	segment, err := regionSegment(region)
	if err != nil {
		return "", err
	}
	suffix := publicSuffix
	if usePrivateLink {
		suffix = privateLinkSuffix
	}
	return locator + "." + segment + suffix, nil
}

// URL returns the account's browser URL — Hostname with "https://" prefixed,
// carrying no path (design.md 7.2). Consumed by 006 for status.accountUrl.
//
// Parameters: as Hostname.
//
// Returns:
//   - the URL, or an empty string and the same user error Hostname returns for
//     a malformed region
func URL(locator, region string, usePrivateLink bool) (string, error) {
	hostname, err := Hostname(locator, region, usePrivateLink)
	if err != nil {
		return "", err
	}
	return "https://" + hostname, nil
}
