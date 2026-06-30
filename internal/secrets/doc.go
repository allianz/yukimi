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

// Package secrets provides a backend-agnostic secrets manager for the Snowflake
// provider. It abstracts secret storage behind the SecretBackend interface
// (AWS Secrets Manager, Azure Key Vault, HashiCorp Vault, GCP Secret Manager),
// manages RSA key pairs for JWT authentication, and caches credentials in memory
// with configurable TTL.
//
// The manager follows a singleton pattern: Initialize() is called once by the
// ProviderConfig controller; all resource controllers call GetInstance().
package secrets
