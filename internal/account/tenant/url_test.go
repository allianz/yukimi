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

import (
	"testing"

	"github.com/allianz/yukimi/internal/errors"
	"github.com/allianz/yukimi/internal/snowflake/host"
)

// Spec 006 Appendix Example 2, verbatim.
func TestAccountURL_MatchesDesignExample(t *testing.T) {
	got, err := AccountURL("xc19114", "aws-eu-central-1", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://xc19114.eu-central-1.privatelink.snowflakecomputing.com/console/login"
	if got != want {
		t.Errorf("AccountURL() = %q, want %q", got, want)
	}
}

// AccountURL appends the console login path on top of host.URL's bare host.
func TestAccountURL_AppendsLoginPath(t *testing.T) {
	got, err := AccountURL("xc19114", "aws-eu-west-3", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://xc19114.eu-west-3.aws.snowflakecomputing.com/console/login"
	if got != want {
		t.Errorf("AccountURL() = %q, want %q", got, want)
	}
}

// SC-013: AccountURL's error path matches host.URL's error path exactly —
// same malformed region, same error case, run against both functions.
func TestAccountURL_ErrorPathMatchesHost(t *testing.T) {
	const region = "eu-central-1" // missing cloud prefix

	tenantGot, tenantErr := AccountURL("xc19114", region, true)
	hostGot, hostErr := host.URL("xc19114", region, true)

	if tenantGot != hostGot {
		t.Errorf("AccountURL() = %q, host.URL() = %q, want equal", tenantGot, hostGot)
	}
	if (tenantErr == nil) != (hostErr == nil) {
		t.Fatalf("AccountURL() error = %v, host.URL() error = %v", tenantErr, hostErr)
	}
	if tenantErr.Error() != hostErr.Error() {
		t.Errorf("AccountURL() error = %q, host.URL() error = %q", tenantErr, hostErr)
	}
	if !errors.IsUserError(tenantErr) {
		t.Errorf("AccountURL() error is not a user error: %v", tenantErr)
	}
}
