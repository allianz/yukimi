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

package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/allianz/yukimi/internal/errors"
)

// newConfigDir writes content as baseConfig.yaml into a fresh temp directory
// and returns the directory path.
func newConfigDir(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "baseConfig.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return dir
}

const wellFormedFixture = `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
  usePrivateLink: true

aws:
  region: eu-central-1
`

// SC-001: Load returns a populated *BaseConfig for a well-formed baseConfig.yaml.
func TestLoad_WellFormed(t *testing.T) {
	cfg, err := Load(newConfigDir(t, wellFormedFixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil BaseConfig")
	}
	if cfg.Snowflake.Org != "my_org_name" {
		t.Errorf("Snowflake.Org = %q, want %q", cfg.Snowflake.Org, "my_org_name")
	}
	if cfg.Snowflake.OrgAdminAccount != "my_org_admin_account_name" {
		t.Errorf("Snowflake.OrgAdminAccount = %q, want %q", cfg.Snowflake.OrgAdminAccount, "my_org_admin_account_name")
	}
	if !cfg.Snowflake.UsePrivateLink {
		t.Error("Snowflake.UsePrivateLink = false, want true")
	}
	if cfg.AWS.Region != "eu-central-1" {
		t.Errorf("AWS.Region = %q, want %q", cfg.AWS.Region, "eu-central-1")
	}
	if got := cfg.CloudProvider(); got != "aws" {
		t.Errorf("CloudProvider() = %q, want %q", got, "aws")
	}
}

// SC-002: Load returns a user error when <configDir>/baseConfig.yaml does not exist.
func TestLoad_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
	want := "baseConfig.yaml not found in " + dir
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// Error Classification (system error): an OS error other than "not exist" is not a user error.
func TestLoad_UnreadableFile(t *testing.T) {
	dir := t.TempDir()
	// Make baseConfig.yaml a directory so os.ReadFile fails deterministically (EISDIR),
	// avoiding a flaky/permission-dependent chmod-based fixture.
	if err := os.Mkdir(filepath.Join(dir, "baseConfig.yaml"), 0o755); err != nil {
		t.Fatalf("setting up fixture: %v", err)
	}

	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.IsUserError(err) {
		t.Errorf("expected non-user error, got user error: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "reading baseConfig.yaml:") {
		t.Errorf("error = %q, want prefix %q", err.Error(), "reading baseConfig.yaml:")
	}
}

// SC-003: Load returns a user error when the file is not valid YAML (syntax error).
func TestLoad_MalformedYAML_Syntax(t *testing.T) {
	_, err := Load(newConfigDir(t, "aws: [region: eu-central-1"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
	if !strings.HasPrefix(err.Error(), "failed to parse baseConfig.yaml:") {
		t.Errorf("error = %q, want prefix %q", err.Error(), "failed to parse baseConfig.yaml:")
	}
}

// SC-003: Load returns a user error when the document parses but isn't a top-level mapping.
func TestLoad_MalformedYAML_WrongShape(t *testing.T) {
	_, err := Load(newConfigDir(t, "- foo\n- bar\n"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
	if !strings.HasPrefix(err.Error(), "failed to parse baseConfig.yaml:") {
		t.Errorf("error = %q, want prefix %q", err.Error(), "failed to parse baseConfig.yaml:")
	}
}

// SC-004: Load returns a user error when snowflake.org is absent.
func TestLoad_MissingOrg_Absent(t *testing.T) {
	fixture := `
snowflake:
  orgAdminAccount: my_org_admin_account_name
aws:
  region: eu-central-1
`
	_, err := Load(newConfigDir(t, fixture))
	want := "snowflake.org is required in baseConfig.yaml"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
}

// SC-004: Load returns a user error when snowflake.org is empty.
func TestLoad_MissingOrg_Empty(t *testing.T) {
	fixture := `
snowflake:
  org: ""
  orgAdminAccount: my_org_admin_account_name
aws:
  region: eu-central-1
`
	_, err := Load(newConfigDir(t, fixture))
	want := "snowflake.org is required in baseConfig.yaml"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

// SC-005: Load returns a user error when snowflake.orgAdminAccount is absent.
func TestLoad_MissingOrgAdminAccount_Absent(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
aws:
  region: eu-central-1
`
	_, err := Load(newConfigDir(t, fixture))
	want := "snowflake.orgAdminAccount is required in baseConfig.yaml"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
}

// SC-005: Load returns a user error when snowflake.orgAdminAccount is empty.
func TestLoad_MissingOrgAdminAccount_Empty(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: ""
aws:
  region: eu-central-1
`
	_, err := Load(newConfigDir(t, fixture))
	want := "snowflake.orgAdminAccount is required in baseConfig.yaml"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

// SC-010a: Load returns a user error when snowflake.org contains characters outside
// the Snowflake identifier form.
func TestLoad_MalformedOrg(t *testing.T) {
	fixture := `
snowflake:
  org: my-org
  orgAdminAccount: my_org_admin_account_name
aws:
  region: eu-central-1
`
	_, err := Load(newConfigDir(t, fixture))
	want := "snowflake.org 'my-org' does not match the expected format (expected: my_org_name)"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
}

// SC-010a: Load returns a user error when snowflake.orgAdminAccount contains characters
// outside the Snowflake identifier form.
func TestLoad_MalformedOrgAdminAccount(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my-org-admin
aws:
  region: eu-central-1
`
	_, err := Load(newConfigDir(t, fixture))
	want := "snowflake.orgAdminAccount 'my-org-admin' does not match the expected format (expected: my_org_admin_account_name)"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

// SC-006: Load returns a user error when the file carries no cloud section.
func TestLoad_NoCloudSection(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
`
	_, err := Load(newConfigDir(t, fixture))
	want := "baseConfig.yaml must contain one cloud section (one of: aws, azure, gcp)"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
}

// SC-006 / SC-009: several cloud sections is a user error, and the error message lists
// them in file order — aws first here.
func TestLoad_TwoCloudSections_AWSFirst(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
aws:
  region: eu-central-1
azure:
  foo: bar
`
	_, err := Load(newConfigDir(t, fixture))
	want := "baseConfig.yaml contains several cloud sections (aws, azure); exactly one is allowed"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

// SC-006 / SC-009: same as above but with azure appearing first in the file, proving the
// listed order follows file position, not alphabetical or struct-field order.
func TestLoad_TwoCloudSections_AzureFirst(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
azure:
  foo: bar
aws:
  region: eu-central-1
`
	_, err := Load(newConfigDir(t, fixture))
	want := "baseConfig.yaml contains several cloud sections (azure, aws); exactly one is allowed"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

// SC-006: three cloud sections present.
func TestLoad_ThreeCloudSections(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
aws:
  region: eu-central-1
azure:
  foo: bar
gcp:
  foo: bar
`
	_, err := Load(newConfigDir(t, fixture))
	want := "baseConfig.yaml contains several cloud sections (aws, azure, gcp); exactly one is allowed"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

// SC-007: Snowflake.UsePrivateLink defaults to true when omitted.
func TestLoad_UsePrivateLink_Omitted(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
aws:
  region: eu-central-1
`
	cfg, err := Load(newConfigDir(t, fixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Snowflake.UsePrivateLink {
		t.Error("UsePrivateLink = false, want true (default)")
	}
}

// SC-007: an explicit "usePrivateLink: false" is honored, proving the decoder
// distinguishes omitted from explicitly-false.
func TestLoad_UsePrivateLink_ExplicitFalse(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
  usePrivateLink: false
aws:
  region: eu-central-1
`
	cfg, err := Load(newConfigDir(t, fixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Snowflake.UsePrivateLink {
		t.Error("UsePrivateLink = true, want false (explicit)")
	}
}

// SC-007: an explicit "usePrivateLink: true" is honored.
func TestLoad_UsePrivateLink_ExplicitTrue(t *testing.T) {
	cfg, err := Load(newConfigDir(t, wellFormedFixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Snowflake.UsePrivateLink {
		t.Error("UsePrivateLink = false, want true (explicit)")
	}
}

// SC-008: CloudProvider() returns "aws" for a file whose only cloud section is aws:.
func TestLoad_CloudProvider_AWS(t *testing.T) {
	cfg, err := Load(newConfigDir(t, wellFormedFixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.CloudProvider(); got != "aws" {
		t.Errorf("CloudProvider() = %q, want %q", got, "aws")
	}
}

// SC-008: a section with no compiled-in Go struct backing it (azure) is still recognized.
func TestLoad_CloudProvider_Azure(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
azure:
  subscriptionId: some-id
`
	cfg, err := Load(newConfigDir(t, fixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.CloudProvider(); got != "azure" {
		t.Errorf("CloudProvider() = %q, want %q", got, "azure")
	}
}

// SC-008: same as above for gcp.
func TestLoad_CloudProvider_GCP(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
gcp:
  project: some-project
`
	cfg, err := Load(newConfigDir(t, fixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.CloudProvider(); got != "gcp" {
		t.Errorf("CloudProvider() = %q, want %q", got, "gcp")
	}
}

// SC-009: CloudProvider() returns the same value regardless of where the cloud section
// sits among the file's top-level keys.
func TestLoad_CloudProviderOrderIndependence(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
	}{
		{
			name: "aws_first",
			fixture: `
aws:
  region: eu-central-1
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
`,
		},
		{
			name: "aws_last",
			fixture: `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
aws:
  region: eu-central-1
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(newConfigDir(t, tc.fixture))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := cfg.CloudProvider(); got != "aws" {
				t.Errorf("CloudProvider() = %q, want %q", got, "aws")
			}
		})
	}
}

// SC-010: an absent aws.region is accepted.
func TestLoad_AWSRegionAbsent(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
aws: {}
`
	cfg, err := Load(newConfigDir(t, fixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AWS.Region != "" {
		t.Errorf("AWS.Region = %q, want empty", cfg.AWS.Region)
	}
}

// SC-010: a well-formed but non-existent region is accepted (existence is 003-a's concern).
func TestLoad_AWSRegionWellFormedNonexistent(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
aws:
  region: xx-nowhere-9
`
	cfg, err := Load(newConfigDir(t, fixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AWS.Region != "xx-nowhere-9" {
		t.Errorf("AWS.Region = %q, want %q", cfg.AWS.Region, "xx-nowhere-9")
	}
}

// SC-010: a malformed region is a user error.
func TestLoad_AWSRegionMalformed(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
aws:
  region: Frankfurt!
`
	_, err := Load(newConfigDir(t, fixture))
	want := "aws.region 'Frankfurt!' does not match the expected format (expected: eu-central-1)"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
}

// SC-011: an unrecognized top-level YAML key does not cause Load to fail.
func TestLoad_UnrecognizedTopLevelKey(t *testing.T) {
	fixture := wellFormedFixture + "\ntimeout: 30s\n"
	if _, err := Load(newConfigDir(t, fixture)); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// Edge case: an unrecognized key nested under a known section is ignored too, since the
// schema is expected to grow over time.
func TestLoad_UnrecognizedNestedKey(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
  maxConnectionPoolSize: 10
aws:
  region: eu-central-1
`
	if _, err := Load(newConfigDir(t, fixture)); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// Edge case: an empty file has no cloud section, surfacing that specific error.
func TestLoad_EmptyFile(t *testing.T) {
	_, err := Load(newConfigDir(t, ""))
	want := "baseConfig.yaml must contain one cloud section (one of: aws, azure, gcp)"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
}

// Edge case: a document whose root is YAML null (not a mapping) decodes into a
// zero-value rawConfig without error, so it must still surface as "no cloud section"
// rather than a parse error.
func TestLoad_NullDocument(t *testing.T) {
	_, err := Load(newConfigDir(t, "~\n"))
	want := "baseConfig.yaml must contain one cloud section (one of: aws, azure, gcp)"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

// SC-012: BaseConfig contains only value fields (no pointers/maps/slices), so a loaded
// *BaseConfig is safe for concurrent read-only use. This test documents that guarantee and
// guards against a future regression; run with -race.
func TestBaseConfig_ConcurrentReadOnlyUse(t *testing.T) {
	cfg, err := Load(newConfigDir(t, wellFormedFixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = cfg.CloudProvider()
			_ = cfg.Snowflake.Org
			_ = cfg.Snowflake.OrgAdminAccount
			_ = cfg.Snowflake.UsePrivateLink
			_ = cfg.AWS.Region
		}()
	}
	wg.Wait()
}
