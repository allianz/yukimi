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
	"crypto/sha256"
	"encoding/base32"
	"strings"
)

const namespaceSuffixLen = 5

// ResolveName derives the Snowflake account name for a SnowflakeAccount CRD:
// metadata.name with every '-' translated to '_', suffixed with '_' plus the
// first 5 characters of the base32-encoded SHA-256 of metadata.namespace
// (design.md 3.12). Requires no stored state — namespaces can't be renamed
// and name is immutable, so the result is stable and recomputable on every
// call.
//
// Parameters:
//   - name: metadata.name of the SnowflakeAccount CRD.
//   - namespace: metadata.namespace of the SnowflakeAccount CRD.
//
// Returns: the resolved Snowflake account name. Never errors — both inputs
// are Kubernetes identifiers already validated by the API server.
func ResolveName(name, namespace string) string {
	sum := sha256.Sum256([]byte(namespace))
	encoded := strings.ToLower(base32.StdEncoding.EncodeToString(sum[:]))
	translated := strings.ReplaceAll(name, "-", "_")
	return translated + "_" + encoded[:namespaceSuffixLen]
}
