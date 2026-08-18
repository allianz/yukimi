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

// Package secrets defines a backend-agnostic interface for storing and
// retrieving the RSA-keypair credentials the platform uses to authenticate to
// Snowflake, plus the path grammar, key generation, and TTL-cache helpers
// layered on top of it. See
// specs/003-secrets-handling.md for the full specification.
package secrets
