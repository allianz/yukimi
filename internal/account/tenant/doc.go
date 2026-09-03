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

// Package tenant resolves a SnowflakeAccount CRD's tenant identity: the
// Snowflake account name derived from metadata.name and metadata.namespace,
// the ops-owned onboarding labels set on that namespace, and the account's
// browser URL. See specs/006-snowflake-account-crd.md for the full
// specification.
package tenant
