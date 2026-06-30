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
	"strings"
)

// PlatformCredentials represents tenant-level credentials.
type PlatformCredentials struct {
	Account    string `json:"account"`     // Snowflake account name
	Username   string `json:"username"`    // Fixed value: "PLATFORM"
	PublicKey  string `json:"public_key"`  // Single-line base64, no PEM delimiters
	PrivateKey string `json:"private_key"` // PKCS#8 format with PEM delimiters
}

// OrgAdminCredentials represents organization-level admin credentials.
type OrgAdminCredentials struct {
	Account    string `json:"account"`     // Organization account name
	Username   string `json:"username"`    // Org admin username
	PublicKey  string `json:"public_key"`  // Single-line base64, no PEM delimiters
	PrivateKey string `json:"private_key"` // PKCS#8 format with PEM delimiters
}

func (c *PlatformCredentials) validate() error {
	if c.Account == "" {
		return fmt.Errorf("credential field 'account' is empty")
	}
	if c.Username == "" {
		return fmt.Errorf("credential field 'username' is empty")
	}
	if c.PublicKey == "" {
		return fmt.Errorf("credential field 'public_key' is empty")
	}
	if c.PrivateKey == "" {
		return fmt.Errorf("credential field 'private_key' is empty")
	}
	return nil
}

func (c *OrgAdminCredentials) validate() error {
	if c.Account == "" {
		return fmt.Errorf("credential field 'account' is empty")
	}
	if c.Username == "" {
		return fmt.Errorf("credential field 'username' is empty")
	}
	if c.PublicKey == "" {
		return fmt.Errorf("credential field 'public_key' is empty")
	}
	if c.PrivateKey == "" {
		return fmt.Errorf("credential field 'private_key' is empty")
	}
	return nil
}

// sanitizeField strips control characters from a credential field before
// using it in log messages to prevent log injection.
func sanitizeField(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
