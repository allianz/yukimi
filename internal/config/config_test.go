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
	"time"

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
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: aws-eu-central-1
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
	if cfg.Snowflake.OrgAdminAccountLocator != "xc19114" {
		t.Errorf("Snowflake.OrgAdminAccountLocator = %q, want %q", cfg.Snowflake.OrgAdminAccountLocator, "xc19114")
	}
	if cfg.Snowflake.OrgAdminAccountRegion != "aws-eu-central-1" {
		t.Errorf("Snowflake.OrgAdminAccountRegion = %q, want %q", cfg.Snowflake.OrgAdminAccountRegion, "aws-eu-central-1")
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

// The pool-tuning and secrets-cache fields all default when omitted.
func TestLoad_PoolAndCacheDefaults(t *testing.T) {
	cfg, err := Load(newConfigDir(t, wellFormedFixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Snowflake.MaxConnectionPoolSize != defaultMaxConnectionPoolSize {
		t.Errorf("MaxConnectionPoolSize = %d, want default %d", cfg.Snowflake.MaxConnectionPoolSize, defaultMaxConnectionPoolSize)
	}
	if cfg.Snowflake.MaxIdleConnections != defaultMaxIdleConnections {
		t.Errorf("MaxIdleConnections = %d, want default %d", cfg.Snowflake.MaxIdleConnections, defaultMaxIdleConnections)
	}
	if cfg.Snowflake.ConnectionMaxLifetime != defaultConnectionMaxLifetime {
		t.Errorf("ConnectionMaxLifetime = %v, want default %v", cfg.Snowflake.ConnectionMaxLifetime, defaultConnectionMaxLifetime)
	}
	if cfg.Snowflake.ConnectionMaxIdleTime != defaultConnectionMaxIdleTime {
		t.Errorf("ConnectionMaxIdleTime = %v, want default %v", cfg.Snowflake.ConnectionMaxIdleTime, defaultConnectionMaxIdleTime)
	}
	if cfg.Snowflake.ConnectionProbeTimeout != defaultConnectionProbeTimeout {
		t.Errorf("ConnectionProbeTimeout = %v, want default %v", cfg.Snowflake.ConnectionProbeTimeout, defaultConnectionProbeTimeout)
	}
	if cfg.Secrets.CacheTTL != defaultSecretsCacheTTL {
		t.Errorf("Secrets.CacheTTL = %v, want default %v", cfg.Secrets.CacheTTL, defaultSecretsCacheTTL)
	}
}

// An explicit value for each pool-tuning/secrets-cache field overrides its default.
func TestLoad_PoolAndCacheExplicitValues(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: aws-eu-central-1
  maxConnectionPoolSize: 25
  maxIdleConnections: 0
  connectionMaxLifetime: 1h
  connectionMaxIdleTime: 15m
  connectionProbeTimeout: 30s
aws:
  region: eu-central-1
secrets:
  cacheTtl: 10m
`
	cfg, err := Load(newConfigDir(t, fixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Snowflake.MaxConnectionPoolSize != 25 {
		t.Errorf("MaxConnectionPoolSize = %d, want 25", cfg.Snowflake.MaxConnectionPoolSize)
	}
	if cfg.Snowflake.MaxIdleConnections != 0 {
		t.Errorf("MaxIdleConnections = %d, want 0", cfg.Snowflake.MaxIdleConnections)
	}
	if cfg.Snowflake.ConnectionMaxLifetime != time.Hour {
		t.Errorf("ConnectionMaxLifetime = %v, want 1h", cfg.Snowflake.ConnectionMaxLifetime)
	}
	if cfg.Snowflake.ConnectionMaxIdleTime != 15*time.Minute {
		t.Errorf("ConnectionMaxIdleTime = %v, want 15m", cfg.Snowflake.ConnectionMaxIdleTime)
	}
	if cfg.Snowflake.ConnectionProbeTimeout != 30*time.Second {
		t.Errorf("ConnectionProbeTimeout = %v, want 30s", cfg.Snowflake.ConnectionProbeTimeout)
	}
	if cfg.Secrets.CacheTTL != 10*time.Minute {
		t.Errorf("Secrets.CacheTTL = %v, want 10m", cfg.Secrets.CacheTTL)
	}
}

// snowflake.maxConnectionPoolSize must be a positive integer.
func TestLoad_MaxConnectionPoolSize_NotPositive(t *testing.T) {
	fixture := wellFormedFixtureWith("  maxConnectionPoolSize: 0\n")
	_, err := Load(newConfigDir(t, fixture))
	want := "snowflake.maxConnectionPoolSize '0' must be a positive integer"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
}

// snowflake.maxIdleConnections may not be negative.
func TestLoad_MaxIdleConnections_Negative(t *testing.T) {
	fixture := wellFormedFixtureWith("  maxIdleConnections: -1\n")
	_, err := Load(newConfigDir(t, fixture))
	want := "snowflake.maxIdleConnections '-1' must not be negative"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
}

// snowflake.connectionMaxLifetime must parse as a Go duration string.
func TestLoad_ConnectionMaxLifetime_Unparseable(t *testing.T) {
	fixture := wellFormedFixtureWith("  connectionMaxLifetime: not-a-duration\n")
	_, err := Load(newConfigDir(t, fixture))
	want := "snowflake.connectionMaxLifetime 'not-a-duration' does not match the expected format (expected: a Go duration string, e.g. 30m)"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
}

// snowflake.connectionMaxLifetime must be positive.
func TestLoad_ConnectionMaxLifetime_NotPositive(t *testing.T) {
	fixture := wellFormedFixtureWith("  connectionMaxLifetime: 0s\n")
	_, err := Load(newConfigDir(t, fixture))
	want := "snowflake.connectionMaxLifetime '0s' must be a positive duration"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
}

// snowflake.connectionMaxIdleTime must parse as a Go duration string.
func TestLoad_ConnectionMaxIdleTime_Unparseable(t *testing.T) {
	fixture := wellFormedFixtureWith("  connectionMaxIdleTime: not-a-duration\n")
	_, err := Load(newConfigDir(t, fixture))
	want := "snowflake.connectionMaxIdleTime 'not-a-duration' does not match the expected format (expected: a Go duration string, e.g. 30m)"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
}

// snowflake.connectionProbeTimeout must be positive.
func TestLoad_ConnectionProbeTimeout_NotPositive(t *testing.T) {
	fixture := wellFormedFixtureWith("  connectionProbeTimeout: -5s\n")
	_, err := Load(newConfigDir(t, fixture))
	want := "snowflake.connectionProbeTimeout '-5s' must be a positive duration"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
}

// secrets.cacheTtl must parse as a Go duration string.
func TestLoad_SecretsCacheTTL_Unparseable(t *testing.T) {
	fixture := wellFormedFixture + "secrets:\n  cacheTtl: not-a-duration\n"
	_, err := Load(newConfigDir(t, fixture))
	want := "secrets.cacheTtl 'not-a-duration' does not match the expected format (expected: a Go duration string, e.g. 30m)"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
}

// secrets.cacheTtl must be positive.
func TestLoad_SecretsCacheTTL_NotPositive(t *testing.T) {
	fixture := wellFormedFixture + "secrets:\n  cacheTtl: 0m\n"
	_, err := Load(newConfigDir(t, fixture))
	want := "secrets.cacheTtl '0m' must be a positive duration"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
}

// wellFormedFixtureWith appends extraSnowflakeLines under wellFormedFixture's snowflake:
// block, for tests that only need to override one pool-tuning field.
func wellFormedFixtureWith(extraSnowflakeLines string) string {
	return `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: aws-eu-central-1
  usePrivateLink: true
` + extraSnowflakeLines + `
aws:
  region: eu-central-1
`
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

// SC-015: Load returns a user error when snowflake.orgAdminAccountLocator is absent.
func TestLoad_MissingOrgAdminAccountLocator_Absent(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
  orgAdminAccountRegion: aws-eu-central-1
aws:
  region: eu-central-1
`
	_, err := Load(newConfigDir(t, fixture))
	want := "snowflake.orgAdminAccountLocator is required in baseConfig.yaml"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
}

// SC-015: Load returns a user error when snowflake.orgAdminAccountLocator is empty.
func TestLoad_MissingOrgAdminAccountLocator_Empty(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
  orgAdminAccountLocator: ""
  orgAdminAccountRegion: aws-eu-central-1
aws:
  region: eu-central-1
`
	_, err := Load(newConfigDir(t, fixture))
	want := "snowflake.orgAdminAccountLocator is required in baseConfig.yaml"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

// SC-016: Load returns a user error when snowflake.orgAdminAccountRegion is absent.
func TestLoad_MissingOrgAdminAccountRegion_Absent(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
  orgAdminAccountLocator: xc19114
aws:
  region: eu-central-1
`
	_, err := Load(newConfigDir(t, fixture))
	want := "snowflake.orgAdminAccountRegion is required in baseConfig.yaml"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
}

// SC-016: Load returns a user error when snowflake.orgAdminAccountRegion is empty.
func TestLoad_MissingOrgAdminAccountRegion_Empty(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: ""
aws:
  region: eu-central-1
`
	_, err := Load(newConfigDir(t, fixture))
	want := "snowflake.orgAdminAccountRegion is required in baseConfig.yaml"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

// SC-017: Load returns a user error when snowflake.orgAdminAccountLocator contains
// characters outside its documented shape.
func TestLoad_MalformedOrgAdminAccountLocator(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
  orgAdminAccountLocator: "xc-19114!"
  orgAdminAccountRegion: aws-eu-central-1
aws:
  region: eu-central-1
`
	_, err := Load(newConfigDir(t, fixture))
	want := "snowflake.orgAdminAccountLocator 'xc-19114!' does not match the expected format (expected: xc19114)"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
}

// SC-017: Load returns a user error when snowflake.orgAdminAccountRegion contains
// characters outside its documented shape.
func TestLoad_MalformedOrgAdminAccountRegion(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: "Frankfurt!"
aws:
  region: eu-central-1
`
	_, err := Load(newConfigDir(t, fixture))
	want := "snowflake.orgAdminAccountRegion 'Frankfurt!' does not match the expected format (expected: aws-eu-central-1 or azure-westeurope)"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
	}
}

// SC-017: a well-formed region under the azure- prefix is accepted, proving the regex
// recognizes every documented cloud prefix, not just aws-.
func TestLoad_OrgAdminAccountRegion_AzureStyleAccepted(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: azure-westeurope
aws:
  region: eu-central-1
`
	cfg, err := Load(newConfigDir(t, fixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Snowflake.OrgAdminAccountRegion != "azure-westeurope" {
		t.Errorf("Snowflake.OrgAdminAccountRegion = %q, want %q", cfg.Snowflake.OrgAdminAccountRegion, "azure-westeurope")
	}
}

// SC-017: a region missing its cloud prefix is now malformed, since orgAdminAccountRegion
// must use the same cloud-region form as the Backplane Config's region keys (design.md 3.5).
func TestLoad_OrgAdminAccountRegion_MissingCloudPrefix(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: eu-central-1
aws:
  region: eu-central-1
`
	_, err := Load(newConfigDir(t, fixture))
	want := "snowflake.orgAdminAccountRegion 'eu-central-1' does not match the expected format (expected: aws-eu-central-1 or azure-westeurope)"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got %v", err)
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
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: aws-eu-central-1
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
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: aws-eu-central-1
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

// SC-022: Snowflake.DisableOCSPChecks defaults to false when omitted.
func TestLoad_DisableOCSPChecks_Omitted(t *testing.T) {
	cfg, err := Load(newConfigDir(t, wellFormedFixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Snowflake.DisableOCSPChecks {
		t.Error("DisableOCSPChecks = true, want false (default)")
	}
}

// SC-022: an explicit "disableOcspChecks: false" is honored.
func TestLoad_DisableOCSPChecks_ExplicitFalse(t *testing.T) {
	fixture := wellFormedFixtureWith("  disableOcspChecks: false\n")
	cfg, err := Load(newConfigDir(t, fixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Snowflake.DisableOCSPChecks {
		t.Error("DisableOCSPChecks = true, want false (explicit)")
	}
}

// SC-022: an explicit "disableOcspChecks: true" is honored, proving the decoder
// distinguishes omitted from explicitly-false/true.
func TestLoad_DisableOCSPChecks_ExplicitTrue(t *testing.T) {
	fixture := wellFormedFixtureWith("  disableOcspChecks: true\n")
	cfg, err := Load(newConfigDir(t, fixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Snowflake.DisableOCSPChecks {
		t.Error("DisableOCSPChecks = false, want true (explicit)")
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
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: aws-eu-central-1
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
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: aws-eu-central-1
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
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: aws-eu-central-1
`,
		},
		{
			name: "aws_last",
			fixture: `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: aws-eu-central-1
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
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: aws-eu-central-1
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

// SC-010: a well-formed but non-existent region is accepted (existence is 003.a's concern).
func TestLoad_AWSRegionWellFormedNonexistent(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: aws-eu-central-1
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
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: aws-eu-central-1
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

// SC-015: an absent aws.kmsKeyId is accepted.
func TestLoad_AWSKmsKeyIdAbsent(t *testing.T) {
	cfg, err := Load(newConfigDir(t, wellFormedFixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AWS.KmsKeyId != "" {
		t.Errorf("AWS.KmsKeyId = %q, want empty", cfg.AWS.KmsKeyId)
	}
}

// SC-015: every documented KMS key identifier form is accepted.
func TestLoad_AWSKmsKeyIdWellFormed(t *testing.T) {
	cases := []struct {
		name     string
		kmsKeyId string
	}{
		{name: "bare_key_id", kmsKeyId: "1234abcd-12ab-34cd-56ef-1234567890ab"},
		{name: "alias", kmsKeyId: "alias/yukimi-secrets"},
		{name: "key_arn", kmsKeyId: "arn:aws:kms:eu-central-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab"},
		{name: "alias_arn", kmsKeyId: "arn:aws:kms:eu-central-1:111122223333:alias/yukimi-secrets"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: aws-eu-central-1
aws:
  region: eu-central-1
  kmsKeyId: ` + tc.kmsKeyId + `
`
			cfg, err := Load(newConfigDir(t, fixture))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.AWS.KmsKeyId != tc.kmsKeyId {
				t.Errorf("AWS.KmsKeyId = %q, want %q", cfg.AWS.KmsKeyId, tc.kmsKeyId)
			}
		})
	}
}

// SC-015: a malformed aws.kmsKeyId is a user error.
func TestLoad_AWSKmsKeyIdMalformed(t *testing.T) {
	fixture := `
snowflake:
  org: my_org_name
  orgAdminAccount: my_org_admin_account_name
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: aws-eu-central-1
aws:
  region: eu-central-1
  kmsKeyId: "not a key!"
`
	_, err := Load(newConfigDir(t, fixture))
	want := "aws.kmsKeyId 'not a key!' does not match the expected format (expected: a KMS key ID, alias, or ARN, e.g. alias/my-key)"
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
  orgAdminAccountLocator: xc19114
  orgAdminAccountRegion: aws-eu-central-1
  someFutureSetting: 10
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
			_ = cfg.Snowflake.OrgAdminAccountLocator
			_ = cfg.Snowflake.OrgAdminAccountRegion
			_ = cfg.Snowflake.UsePrivateLink
			_ = cfg.Snowflake.DisableOCSPChecks
			_ = cfg.Snowflake.MaxConnectionPoolSize
			_ = cfg.Snowflake.MaxIdleConnections
			_ = cfg.Snowflake.ConnectionMaxLifetime
			_ = cfg.Snowflake.ConnectionMaxIdleTime
			_ = cfg.Snowflake.ConnectionProbeTimeout
			_ = cfg.AWS.Region
			_ = cfg.AWS.KmsKeyId
			_ = cfg.Secrets.CacheTTL
		}()
	}
	wg.Wait()
}
