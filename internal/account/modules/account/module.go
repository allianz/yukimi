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

// Package account creates a tenant's Snowflake account and bootstraps the
// platform service user, the pipeline's first module (design.md 3.6). The
// package name deliberately collides with internal/account's own — callers
// alias one side. See specs/010-account-module.md.
package account

import (
	coreaccount "github.com/allianz/yukimi/internal/account"
	"github.com/allianz/yukimi/internal/secrets"
)

// module is the account module. It is the only module in the pipeline that
// ever opens an org-admin-scoped connection, and only on the fresh-create
// path in Apply.
type module struct {
	backend secrets.Backend
	org     string
}

// New constructs the account module.
//
// Parameters:
//   - backend: the secrets.Backend (003) the platform keypair is stored
//     through, via Backend.Create only — this module never calls Update.
//   - org: BaseConfig.Snowflake.Org (002), used to build the tenant secret
//     path (003) exactly as internal/snowflake/pool does.
//
// Returns:
//   - coreaccount.Module: never nil.
func New(backend secrets.Backend, org string) coreaccount.Module {
	return &module{backend: backend, org: org}
}

func (m *module) Name() string { return "account" }
