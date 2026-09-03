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

package tenant

import "github.com/allianz/yukimi/internal/snowflake/host"

const loginPath = "/console/login"

// AccountURL returns the SnowflakeAccount's status.accountUrl (design.md
// 7.2): the account's login URL, built from the locator Snowflake assigned
// at CREATE ACCOUNT (012) and the CRD's region. It never derives from the
// resolved account name, which has no relationship to the locator. Wraps
// internal/snowflake/host.URL (004); adds no validation beyond that call.
//
// Parameters:
//   - locator: the account locator returned by CREATE ACCOUNT (e.g. "xc19114").
//   - region: the CRD's spec.region (e.g. "aws-eu-central-1").
//   - usePrivateLink: from the controller's base config (002), supplied by
//     the caller (020) — not read from the Backplane Config, which carries
//     no such field.
//
// Returns: User error if region does not match the expected
// "<cloud>-<region...>" shape (bubbled from internal/snowflake/host.URL).
func AccountURL(locator, region string, usePrivateLink bool) (string, error) {
	url, err := host.URL(locator, region, usePrivateLink)
	if err != nil {
		return "", err
	}
	return url + loginPath, nil
}
