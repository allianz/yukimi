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
	"strings"

	"github.com/allianz/yukimi/internal/errors"
)

const (
	privateLinkSuffix = ".privatelink.snowflakecomputing.com"
	publicSuffix      = ".snowflakecomputing.com"
)

// validClouds mirrors internal/config/base.cloudSectionKeys (002) — the same
// three clouds a Snowflake org's account may live on. Duplicated rather than
// imported (Key Concept: Duplicated Loaders, 002/007): neither package
// should depend on the other just for a three-entry map. Onboarding a new
// cloud means updating this map, base.go's cloudSectionKeys, and the CRD's
// region Pattern (006) together — a deliberate, coordinated change.
var validClouds = map[string]bool{"aws": true, "azure": true, "gcp": true}

// regionSegment returns the hostname segment for region, e.g.
// "eu-central-1" for "aws-eu-central-1" or "eu-west-3.aws" for
// "aws-eu-west-3". Most regions repeat the cloud as a trailing segment after
// the region; "aws-eu-central-1" is the one known exception and needs no
// suffix. Only checks that region starts with a known cloud — the region
// suffix's shape is the SnowflakeAccount CRD's job (006), not re-validated
// here.
func regionSegment(region string) (string, error) {
	idx := strings.IndexByte(region, '-')
	if idx < 0 || !validClouds[region[:idx]] {
		return "", errors.NewUserError(fmt.Sprintf(
			"region '%s' does not match the expected cloud-region format (expected: aws-eu-central-1)", region))
	}
	switch region {
	case "aws-eu-central-1":
		return "eu-central-1", nil
	default:
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
//     Config.Snowflake.UsePrivateLink, 002), never this package
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
