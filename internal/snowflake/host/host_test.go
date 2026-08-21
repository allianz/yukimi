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
	"testing"

	"github.com/allianz/yukimi/internal/errors"
)

// SC-006: aws-eu-central-1 is the one named exception (no trailing suffix);
// every other region repeats the cloud as a trailing segment.
func TestRegionSegment(t *testing.T) {
	cases := []struct {
		region string
		want   string
	}{
		{"aws-eu-central-1", "eu-central-1"},
		{"aws-eu-west-3", "eu-west-3.aws"},
		{"aws-us-east-1", "us-east-1.aws"},
		{"azure-westeurope", "westeurope.azure"},
		{"gcp-us-east1", "us-east1.gcp"},
	}
	for _, c := range cases {
		got, err := regionSegment(c.region)
		if err != nil {
			t.Errorf("regionSegment(%q): unexpected error: %v", c.region, err)
			continue
		}
		if got != c.want {
			t.Errorf("regionSegment(%q) = %q, want %q", c.region, got, c.want)
		}
	}
}

// SC-008a: a region missing its cloud prefix, or otherwise malformed, is
// rejected by regionSegment (and therefore by Hostname and URL) without an
// allowlist of specific cloud names — the leading segment must simply be at
// least 3 characters, which "eu-central-1"'s 2-character "eu" fails.
func TestRegionSegment_RejectsMalformed(t *testing.T) {
	invalid := []string{
		"eu-central-1", // missing cloud prefix
		"Frankfurt!",   // garbage
		"",
		"AWS-eu-central-1", // uppercase cloud
		"aws-",
		"aws",
		"aws--1",
		"aws-eu-",
	}
	for _, region := range invalid {
		got, err := regionSegment(region)
		if err == nil {
			t.Errorf("regionSegment(%q): expected error, got %q", region, got)
			continue
		}
		if !errors.IsUserError(err) {
			t.Errorf("regionSegment(%q): expected a user error, got %v", region, err)
		}
		if got != "" {
			t.Errorf("regionSegment(%q): expected empty string on error, got %q", region, got)
		}
	}
}

// SC-007: Hostname selects the PrivateLink suffix based on usePrivateLink,
// with the locator leading in both cases.
func TestHostname_SelectsSuffix(t *testing.T) {
	cases := []struct {
		usePrivateLink bool
		want           string
	}{
		{true, "xc19114.eu-central-1.privatelink.snowflakecomputing.com"},
		{false, "xc19114.eu-central-1.snowflakecomputing.com"},
	}
	for _, c := range cases {
		got, err := Hostname("xc19114", "aws-eu-central-1", c.usePrivateLink)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != c.want {
			t.Errorf("Hostname(usePrivateLink=%v) = %q, want %q", c.usePrivateLink, got, c.want)
		}
	}
}

// SC-007a: design.md 7.2's example, verbatim.
func TestURL_MatchesDesignExample(t *testing.T) {
	got, err := URL("xc19114", "aws-eu-central-1", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://xc19114.eu-central-1.privatelink.snowflakecomputing.com"
	if got != want {
		t.Errorf("URL() = %q, want %q", got, want)
	}
}

// URL carries no path beyond scheme+host (design.md 7.2).
func TestURL_HasNoTrailingPath(t *testing.T) {
	got, err := URL("xc19114", "aws-eu-west-3", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://xc19114.eu-west-3.aws.snowflakecomputing.com"
	if got != want {
		t.Errorf("URL() = %q, want %q", got, want)
	}
}

// SC-008a: Hostname and URL both return an empty string and a user error for
// a malformed region.
func TestHostnameAndURL_RejectMalformedRegion(t *testing.T) {
	for _, region := range []string{"eu-central-1", "Frankfurt!"} {
		if got, err := Hostname("xc19114", region, true); err == nil || !errors.IsUserError(err) || got != "" {
			t.Errorf("Hostname(%q): got (%q, %v), want (\"\", user error)", region, got, err)
		}
		if got, err := URL("xc19114", region, true); err == nil || !errors.IsUserError(err) || got != "" {
			t.Errorf("URL(%q): got (%q, %v), want (\"\", user error)", region, got, err)
		}
	}
}
