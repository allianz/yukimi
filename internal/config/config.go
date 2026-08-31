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

// Package config loads the controller's base configuration from a mounted
// directory at startup into an immutable BaseConfig struct.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/allianz/yukimi/internal/errors"
	"gopkg.in/yaml.v3"
)

const (
	// defaultMaxConnectionPoolSize is snowflake.maxConnectionPoolSize's default (004).
	defaultMaxConnectionPoolSize = 10
	// defaultMaxIdleConnections is snowflake.maxIdleConnections's default (004).
	defaultMaxIdleConnections = 2
	// defaultConnectionMaxLifetime is snowflake.connectionMaxLifetime's default (004).
	defaultConnectionMaxLifetime = 30 * time.Minute
	// defaultConnectionMaxIdleTime is snowflake.connectionMaxIdleTime's default (004).
	defaultConnectionMaxIdleTime = 5 * time.Minute
	// defaultConnectionProbeTimeout is snowflake.connectionProbeTimeout's default (004).
	defaultConnectionProbeTimeout = 10 * time.Second
	// defaultSecretsCacheTTL is secrets.cacheTtl's default (003).
	defaultSecretsCacheTTL = 5 * time.Minute
	// defaultRotationInterval is secrets.rotationInterval's default (004).
	defaultRotationInterval = 4320 * time.Hour
)

var (
	// identifierPattern matches the Snowflake identifier form used by
	// snowflake.org and snowflake.orgAdminAccount (design.md 3.12).
	identifierPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

	// awsRegionPattern matches AWS region names such as "eu-central-1".
	awsRegionPattern = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)

	// kmsKeyIDPattern matches the AWS KMS key identifier forms accepted for
	// aws.kmsKeyId: a bare key ID (UUID), a key alias, a key ARN, or an alias ARN.
	kmsKeyIDPattern = regexp.MustCompile(`^(arn:aws:kms:[a-z0-9-]+:\d{12}:(key|alias)/[A-Za-z0-9/_-]+|alias/[A-Za-z0-9/_-]+|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`)

	// accountLocatorPattern matches the loose shape of a Snowflake account locator
	// (e.g. "xc19114", design.md 3.6). Snowflake publishes no strict grammar, so this
	// only rejects whitespace/punctuation, never judges realness.
	accountLocatorPattern = regexp.MustCompile(`^[A-Za-z0-9]+$`)

	// orgAdminRegionPattern matches the cloud-region form used by the Backplane Config's
	// region keys and the SnowflakeAccount CRD's region field (e.g. "aws-eu-central-1",
	// "azure-westeurope"; design.md 3.1, 3.5). The recognized prefixes must stay in sync
	// with cloudSectionKeys below — a Snowflake org's account may live on any of the same
	// three clouds, independent of the controller's own AWS-hosted Secrets Manager backend.
	orgAdminRegionPattern = regexp.MustCompile(`^(aws|azure|gcp)-[a-z][a-z0-9-]*$`)

	cloudSectionKeys = map[string]bool{"aws": true, "azure": true, "gcp": true}
)

// BaseConfig is the immutable, validated provider-wide configuration loaded at startup.
type BaseConfig struct {
	Snowflake SnowflakeSettings // organization identity plus connection-affecting settings
	AWS       AWSSettings       // consumed by 003.a; checked here for shape only
	Secrets   SecretsSettings   // consumed by whoever wraps a Backend in secrets.NewCachedBackend (003)

	cloudProvider string // resolved by Load from the cloud section present; read via CloudProvider()
}

// CloudProvider returns the name of the cloud section the file carries — "aws", "azure", or
// "gcp" — found by scanning the top-level keys in document order. There is no cloudProvider
// key: an "aws:" section is itself the selection, so the two can never disagree. Resolved once
// by Load, which requires exactly one cloud section, so the result is never empty.
func (c *BaseConfig) CloudProvider() string {
	return c.cloudProvider
}

// SnowflakeSettings holds the Snowflake organization-level settings used across
// account identifiers, secret paths, and connection host construction.
type SnowflakeSettings struct {
	Org                    string // organization name; used in account identifiers, secret paths, and accountUrl
	OrgAdminAccount        string // account used for org-level operations
	OrgAdminAccountLocator string // Snowflake account locator for OrgAdminAccount (e.g. "xc19114"); static config because, unlike a tenant account, the controller never runs CREATE ACCOUNT for it (design.md 3.6)
	OrgAdminAccountRegion  string // Snowflake region OrgAdminAccount lives in, cloud-region form (e.g. "aws-eu-central-1" or "azure-westeurope"); paired with OrgAdminAccountLocator to build the org-admin connection host (004)
	UsePrivateLink         bool   // affects the connection host (004); defaults to true when omitted
	DisableOCSPChecks      bool   // disables OCSP certificate-revocation checking on Snowflake connections (004); testing/emergency use only. Defaults to false when omitted

	MaxConnectionPoolSize  int           // max open connections per pooled *sql.DB target (004); defaults to 10 when omitted
	MaxIdleConnections     int           // max idle connections kept per pooled *sql.DB target (004); defaults to 2 when omitted
	ConnectionMaxLifetime  time.Duration // max lifetime of a physical connection before it is recycled (004); defaults to 30m when omitted
	ConnectionMaxIdleTime  time.Duration // max time a physical connection may sit idle before being closed (004); defaults to 5m when omitted
	ConnectionProbeTimeout time.Duration // timeout for the health probe run on first dial (004); defaults to 10s when omitted
}

// SecretsSettings holds settings for the secrets cache decorator (003), consumed by whoever
// wraps a Backend in secrets.NewCachedBackend — today cmd/provider/main.go.
type SecretsSettings struct {
	CacheTTL         time.Duration // TTL for the in-memory secrets cache (003); defaults to 5m when omitted
	RotationInterval time.Duration // age past which OrgAdmin/TenantAccount rotate a stored credential inline (004); defaults to 4320h (~6 months) when omitted
}

// AWSSettings holds AWS-specific settings, consumed only by 003.a.
type AWSSettings struct {
	Region string // optional here, shape-checked if set; an empty region is a user error in 003.a, not here

	// KmsKeyId is an optional reference to a customer-managed KMS key for encrypting/decrypting
	// secrets in AWS Secrets Manager (003.a); shape-checked here only, not interpreted.
	KmsKeyId string
}

// rawConfig is the direct YAML decode target. UsePrivateLink is a *bool so an
// omitted key can be distinguished from an explicit "usePrivateLink: false".
type rawConfig struct {
	Snowflake rawSnowflake `yaml:"snowflake"`
	AWS       rawAWS       `yaml:"aws"`
	Secrets   rawSecrets   `yaml:"secrets"`
}

type rawSnowflake struct {
	Org                    string `yaml:"org"`
	OrgAdminAccount        string `yaml:"orgAdminAccount"`
	OrgAdminAccountLocator string `yaml:"orgAdminAccountLocator"`
	OrgAdminAccountRegion  string `yaml:"orgAdminAccountRegion"`
	UsePrivateLink         *bool  `yaml:"usePrivateLink"`
	DisableOCSPChecks      *bool  `yaml:"disableOcspChecks"`

	MaxConnectionPoolSize  *int   `yaml:"maxConnectionPoolSize"`
	MaxIdleConnections     *int   `yaml:"maxIdleConnections"`
	ConnectionMaxLifetime  string `yaml:"connectionMaxLifetime"`
	ConnectionMaxIdleTime  string `yaml:"connectionMaxIdleTime"`
	ConnectionProbeTimeout string `yaml:"connectionProbeTimeout"`
}

type rawAWS struct {
	Region   string `yaml:"region"`
	KmsKeyId string `yaml:"kmsKeyId"`
}

type rawSecrets struct {
	CacheTTL         string `yaml:"cacheTtl"`
	RotationInterval string `yaml:"rotationInterval"`
}

// Load reads, parses, and validates "<configDir>/baseConfig.yaml".
//
// Parameters:
//   - configDir: directory containing baseConfig.yaml (and, in a full deployment,
//     its sibling config files for 007/008 — this package reads only its own file)
//
// Returns:
//   - *BaseConfig: the validated configuration; never nil on a nil error
//   - User error if the file is missing, unreadable, not valid YAML, a required field
//     (Snowflake.Org, Snowflake.OrgAdminAccount, Snowflake.OrgAdminAccountLocator,
//     Snowflake.OrgAdminAccountRegion) is empty, the file does not carry exactly
//     one cloud section, a field's value does not match its documented format, a pool-tuning
//     integer (MaxConnectionPoolSize, MaxIdleConnections) is out of range, or a duration field
//     (ConnectionMaxLifetime, ConnectionMaxIdleTime, ConnectionProbeTimeout, Secrets.CacheTTL,
//     Secrets.RotationInterval) does not parse as a positive Go duration
//
// Load walks the parsed YAML's top-level keys to find the cloud sections, so a section with
// no Go struct yet (azure:, gcp:) is still recognized rather than silently dropped.
func Load(configDir string) (*BaseConfig, error) {
	path := filepath.Join(configDir, "baseConfig.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.NewUserError(fmt.Sprintf("baseConfig.yaml not found in %s", configDir))
		}
		return nil, fmt.Errorf("reading baseConfig.yaml: %w", err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, errors.NewUserError(fmt.Sprintf("failed to parse baseConfig.yaml: %v", err))
	}

	var raw rawConfig
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		if err := root.Decode(&raw); err != nil {
			return nil, errors.NewUserError(fmt.Sprintf("failed to parse baseConfig.yaml: %v", err))
		}
	}

	cloudSections := detectCloudSections(&root)
	switch len(cloudSections) {
	case 0:
		return nil, errors.NewUserError("baseConfig.yaml must contain one cloud section (one of: aws, azure, gcp)")
	case 1:
		// exactly one, continue
	default:
		return nil, errors.NewUserError(fmt.Sprintf(
			"baseConfig.yaml contains several cloud sections (%s); exactly one is allowed",
			strings.Join(cloudSections, ", ")))
	}

	if raw.Snowflake.Org == "" {
		return nil, errors.NewUserError("snowflake.org is required in baseConfig.yaml")
	}
	if !identifierPattern.MatchString(raw.Snowflake.Org) {
		return nil, errors.NewUserError(fmt.Sprintf(
			"snowflake.org '%s' does not match the expected format (expected: my_org_name)", raw.Snowflake.Org))
	}

	if raw.Snowflake.OrgAdminAccount == "" {
		return nil, errors.NewUserError("snowflake.orgAdminAccount is required in baseConfig.yaml")
	}
	if !identifierPattern.MatchString(raw.Snowflake.OrgAdminAccount) {
		return nil, errors.NewUserError(fmt.Sprintf(
			"snowflake.orgAdminAccount '%s' does not match the expected format (expected: my_org_admin_account_name)",
			raw.Snowflake.OrgAdminAccount))
	}

	if raw.Snowflake.OrgAdminAccountLocator == "" {
		return nil, errors.NewUserError("snowflake.orgAdminAccountLocator is required in baseConfig.yaml")
	}
	if !accountLocatorPattern.MatchString(raw.Snowflake.OrgAdminAccountLocator) {
		return nil, errors.NewUserError(fmt.Sprintf(
			"snowflake.orgAdminAccountLocator '%s' does not match the expected format (expected: xc19114)",
			raw.Snowflake.OrgAdminAccountLocator))
	}

	if raw.Snowflake.OrgAdminAccountRegion == "" {
		return nil, errors.NewUserError("snowflake.orgAdminAccountRegion is required in baseConfig.yaml")
	}
	if !orgAdminRegionPattern.MatchString(raw.Snowflake.OrgAdminAccountRegion) {
		return nil, errors.NewUserError(fmt.Sprintf(
			"snowflake.orgAdminAccountRegion '%s' does not match the expected format (expected: aws-eu-central-1 or azure-westeurope)",
			raw.Snowflake.OrgAdminAccountRegion))
	}

	if raw.AWS.Region != "" && !awsRegionPattern.MatchString(raw.AWS.Region) {
		return nil, errors.NewUserError(fmt.Sprintf(
			"aws.region '%s' does not match the expected format (expected: eu-central-1)", raw.AWS.Region))
	}

	if raw.AWS.KmsKeyId != "" && !kmsKeyIDPattern.MatchString(raw.AWS.KmsKeyId) {
		return nil, errors.NewUserError(fmt.Sprintf(
			"aws.kmsKeyId '%s' does not match the expected format (expected: a KMS key ID, alias, or ARN, e.g. alias/my-key)",
			raw.AWS.KmsKeyId))
	}

	usePrivateLink := true
	if raw.Snowflake.UsePrivateLink != nil {
		usePrivateLink = *raw.Snowflake.UsePrivateLink
	}

	disableOCSPChecks := false
	if raw.Snowflake.DisableOCSPChecks != nil {
		disableOCSPChecks = *raw.Snowflake.DisableOCSPChecks
	}

	maxConnectionPoolSize, err := resolvePositiveInt(
		"snowflake.maxConnectionPoolSize", raw.Snowflake.MaxConnectionPoolSize, defaultMaxConnectionPoolSize)
	if err != nil {
		return nil, err
	}

	maxIdleConnections, err := resolveNonNegativeInt(
		"snowflake.maxIdleConnections", raw.Snowflake.MaxIdleConnections, defaultMaxIdleConnections)
	if err != nil {
		return nil, err
	}

	connectionMaxLifetime, err := resolvePositiveDuration(
		"snowflake.connectionMaxLifetime", raw.Snowflake.ConnectionMaxLifetime, defaultConnectionMaxLifetime)
	if err != nil {
		return nil, err
	}

	connectionMaxIdleTime, err := resolvePositiveDuration(
		"snowflake.connectionMaxIdleTime", raw.Snowflake.ConnectionMaxIdleTime, defaultConnectionMaxIdleTime)
	if err != nil {
		return nil, err
	}

	connectionProbeTimeout, err := resolvePositiveDuration(
		"snowflake.connectionProbeTimeout", raw.Snowflake.ConnectionProbeTimeout, defaultConnectionProbeTimeout)
	if err != nil {
		return nil, err
	}

	cacheTTL, err := resolvePositiveDuration("secrets.cacheTtl", raw.Secrets.CacheTTL, defaultSecretsCacheTTL)
	if err != nil {
		return nil, err
	}

	rotationInterval, err := resolvePositiveDuration(
		"secrets.rotationInterval", raw.Secrets.RotationInterval, defaultRotationInterval)
	if err != nil {
		return nil, err
	}

	return &BaseConfig{
		Snowflake: SnowflakeSettings{
			Org:                    raw.Snowflake.Org,
			OrgAdminAccount:        raw.Snowflake.OrgAdminAccount,
			OrgAdminAccountLocator: raw.Snowflake.OrgAdminAccountLocator,
			OrgAdminAccountRegion:  raw.Snowflake.OrgAdminAccountRegion,
			UsePrivateLink:         usePrivateLink,
			DisableOCSPChecks:      disableOCSPChecks,
			MaxConnectionPoolSize:  maxConnectionPoolSize,
			MaxIdleConnections:     maxIdleConnections,
			ConnectionMaxLifetime:  connectionMaxLifetime,
			ConnectionMaxIdleTime:  connectionMaxIdleTime,
			ConnectionProbeTimeout: connectionProbeTimeout,
		},
		AWS: AWSSettings{
			Region:   raw.AWS.Region,
			KmsKeyId: raw.AWS.KmsKeyId,
		},
		Secrets: SecretsSettings{
			CacheTTL:         cacheTTL,
			RotationInterval: rotationInterval,
		},
		cloudProvider: cloudSections[0],
	}, nil
}

// resolvePositiveInt returns def when raw is nil, and otherwise returns *raw if it is a
// positive integer or a user error naming fieldPath if it is not.
func resolvePositiveInt(fieldPath string, raw *int, def int) (int, error) {
	if raw == nil {
		return def, nil
	}
	if *raw <= 0 {
		return 0, errors.NewUserError(fmt.Sprintf("%s '%d' must be a positive integer", fieldPath, *raw))
	}
	return *raw, nil
}

// resolveNonNegativeInt returns def when raw is nil, and otherwise returns *raw if it is not
// negative or a user error naming fieldPath if it is.
func resolveNonNegativeInt(fieldPath string, raw *int, def int) (int, error) {
	if raw == nil {
		return def, nil
	}
	if *raw < 0 {
		return 0, errors.NewUserError(fmt.Sprintf("%s '%d' must not be negative", fieldPath, *raw))
	}
	return *raw, nil
}

// resolvePositiveDuration returns def when raw is empty, and otherwise parses raw as a Go
// duration string, returning a user error naming fieldPath if it does not parse or is not
// positive.
func resolvePositiveDuration(fieldPath, raw string, def time.Duration) (time.Duration, error) {
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, errors.NewUserError(fmt.Sprintf(
			"%s '%s' does not match the expected format (expected: a Go duration string, e.g. 30m)", fieldPath, raw))
	}
	if d <= 0 {
		return 0, errors.NewUserError(fmt.Sprintf("%s '%s' must be a positive duration", fieldPath, raw))
	}
	return d, nil
}

// detectCloudSections walks the parsed document's top-level mapping keys in document order and
// returns the subset that are cloud sections ("aws", "azure", "gcp"), in the order they appear
// in the file. It returns nil if the document is not a top-level mapping (e.g. an empty file).
func detectCloudSections(root *yaml.Node) []string {
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil
	}
	mapping := root.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return nil
	}

	var found []string
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if key := mapping.Content[i].Value; cloudSectionKeys[key] {
			found = append(found, key)
		}
	}
	return found
}
