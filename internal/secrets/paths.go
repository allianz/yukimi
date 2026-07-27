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
)

// pathComponentPattern allows alphanumeric, hyphens, and underscores only.
// Prevents path traversal via `/` or `..` in path components.
var pathComponentPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func validatePathComponent(name, value string) error {
	if value == "" {
		return fmt.Errorf("secret path component '%s' must not be empty", name)
	}
	if !pathComponentPattern.MatchString(value) {
		return fmt.Errorf("secret path component '%s' contains invalid characters (allowed: alphanumeric, hyphens, underscores)", name)
	}
	return nil
}

// tenantSecretPath constructs and validates the path for tenant credentials.
// Format: snowflake/tenant/{org}/{namespace}/{account}/platform-credentials
func tenantSecretPath(org, namespace, account string) (string, error) {
	if err := validatePathComponent("org", org); err != nil {
		return "", err
	}
	if err := validatePathComponent("namespace", namespace); err != nil {
		return "", err
	}
	if err := validatePathComponent("account", account); err != nil {
		return "", err
	}
	return fmt.Sprintf("snowflake/tenant/%s/%s/%s/platform-credentials", org, namespace, account), nil
}

// orgAdminSecretPath constructs and validates the path for org admin credentials.
// Format: snowflake/org/{org}/{account}/org-admin-credentials
func orgAdminSecretPath(org, account string) (string, error) {
	if err := validatePathComponent("org", org); err != nil {
		return "", err
	}
	if err := validatePathComponent("account", account); err != nil {
		return "", err
	}
	return fmt.Sprintf("snowflake/org/%s/%s/org-admin-credentials", org, account), nil
}

// tenantCacheKey returns the cache key for tenant credentials.
func tenantCacheKey(org, namespace, account string) string {
	return fmt.Sprintf("%s/%s/%s", org, namespace, account)
}

// orgAdminCacheKey returns the cache key for org admin credentials.
func orgAdminCacheKey(org, account string) string {
	return fmt.Sprintf("%s/%s", org, account)
}
