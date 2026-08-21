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

// Package pool keeps one already-authenticated *sql.DB open per Snowflake
// connection target for the controller process's whole life, opening each
// lazily on first use and never closing it except on explicit eviction or
// process shutdown. It exposes exactly two connection scopes reflecting the
// organization/tenant privilege step-down of design.md 3.11: a single
// org-admin connection used only for CREATE ACCOUNT/DROP ACCOUNT, and one
// connection per tenant account, authenticated as that account's platform
// service user. See specs/004-connection-pooling.md for the full
// specification.
package pool
