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

package snowflakeaccount

import (
	"context"
	"database/sql"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	coreaccount "github.com/allianz/yukimi/internal/account"
	"github.com/allianz/yukimi/internal/backplane"
	"github.com/allianz/yukimi/internal/logger"
)

// fakeModule is a minimal coreaccount.Module double, mirroring
// internal/account/pipeline_test.go's own fakeModule. It never touches mc.
type fakeModule struct {
	name          string
	observeInSync bool
	observeOut    coreaccount.Outcome
	applyOut      coreaccount.Outcome
	applyCalled   int

	// setLocator, if non-empty, is set on the ModuleContext during Apply —
	// mirrors the account module (010) capturing CREATE ACCOUNT's locator.
	setLocator string
}

func (f *fakeModule) Name() string { return f.name }

func (f *fakeModule) Observe(_ context.Context, _ *coreaccount.ModuleContext) (bool, coreaccount.Outcome) {
	return f.observeInSync, f.observeOut
}

func (f *fakeModule) Apply(_ context.Context, mc *coreaccount.ModuleContext) coreaccount.Outcome {
	f.applyCalled++
	if f.setLocator != "" {
		mc.SetLocator(f.setLocator)
	}
	return f.applyOut
}

// fakePool is a minimal coreaccount.DBPool double for tests that never need
// a real Snowflake connection.
type fakePool struct {
	orgAdminDB  *sql.DB
	orgAdminErr error
}

func (p *fakePool) OrgAdmin(_ context.Context) (*sql.DB, error) { return p.orgAdminDB, p.orgAdminErr }

func (p *fakePool) TenantAccount(_ context.Context, _, _, _, _ string) (*sql.DB, error) {
	return nil, nil
}

var _ coreaccount.DBPool = (*fakePool)(nil)

// newTestBackplane builds a Backplane Config with one available and one
// not-yet-available region, for buildModuleContext's validation phase.
func newTestBackplane() *backplane.Config {
	return &backplane.Config{
		Regions: map[string]backplane.Region{
			"aws-eu-central-1":   {Available: true},
			"aws-eu-west-1-cold": {Available: false},
		},
	}
}

func newTestLogger(op logger.Operation) *logger.Logger {
	return logger.New(logging.NewNopLogger(), "team-a", "SnowflakeAccount", "acct", op)
}
