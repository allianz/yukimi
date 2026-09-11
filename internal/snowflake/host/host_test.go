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

// SC-008a: a region missing its cloud prefix, or naming a cloud outside
// validClouds, is rejected by regionSegment (and therefore by Hostname and
// URL). The region *suffix*'s shape is no longer checked here — that's the
// SnowflakeAccount CRD's job (006).
func TestRegionSegment_RejectsMalformed(t *testing.T) {
	invalid := []string{
		"eu-central-1", // missing cloud prefix
		"Frankfurt!",   // garbage
		"",
		"AWS-eu-central-1", // uppercase cloud
		"aws",
		"oracle-eu-1", // well-formed shape, unrecognized cloud
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

// FuzzHostnameAndURL: locator is documented as opaque and never validated
// (Hostname's doc comment), so it is not this fuzz target's job to reject
// any locator shape — only to confirm Hostname/URL never panic on one, and
// that their few real invariants hold regardless of what locator contains:
// on a malformed region, both still return ("", a user error); on a
// well-formed one, the result is always built from exactly Hostname's own
// pieces (locator, a dot, the region segment, the suffix usePrivateLink
// selects), and URL is always "https://" prefixed onto that same Hostname.
func FuzzHostnameAndURL(f *testing.F) {
	f.Add("xc19114", "aws-eu-central-1", false)
	f.Add("xc19114", "aws-eu-west-3", true)
	f.Add("", "aws-eu-central-1", false)
	f.Add(`loc"; DROP TABLE X; --`, "aws-eu-central-1", false)
	f.Add("xc19114/../etc", "aws-eu-central-1", false)
	f.Add("xc19114", "not-a-cloud", false)
	f.Add("xc19114", "", false)
	f.Add("xc19114", "AWS-eu-central-1", false)
	f.Fuzz(func(t *testing.T, locator, region string, usePrivateLink bool) {
		host, err := Hostname(locator, region, usePrivateLink)
		if err != nil {
			if !errors.IsUserError(err) {
				t.Fatalf("Hostname(%q, %q, %v) returned a non-user error: %v", locator, region, usePrivateLink, err)
			}
			if host != "" {
				t.Fatalf("Hostname(%q, %q, %v) = %q on error, want \"\"", locator, region, usePrivateLink, host)
			}
			if url, err := URL(locator, region, usePrivateLink); err == nil || !errors.IsUserError(err) || url != "" {
				t.Fatalf("URL(%q, %q, %v) = (%q, %v), want (\"\", the same user error) once Hostname itself errors",
					locator, region, usePrivateLink, url, err)
			}
			return
		}

		suffix := publicSuffix
		if usePrivateLink {
			suffix = privateLinkSuffix
		}
		segment, segErr := regionSegment(region)
		if segErr != nil {
			t.Fatalf("Hostname(%q, %q, %v) succeeded but regionSegment itself errors: %v", locator, region, usePrivateLink, segErr)
		}
		want := locator + "." + segment + suffix
		if host != want {
			t.Fatalf("Hostname(%q, %q, %v) = %q, want %q", locator, region, usePrivateLink, host, want)
		}

		url, err := URL(locator, region, usePrivateLink)
		if err != nil {
			t.Fatalf("URL(%q, %q, %v) errored after Hostname succeeded: %v", locator, region, usePrivateLink, err)
		}
		if url != "https://"+host {
			t.Fatalf("URL(%q, %q, %v) = %q, want %q", locator, region, usePrivateLink, url, "https://"+host)
		}
	})
}
