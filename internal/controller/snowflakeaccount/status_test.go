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
	"testing"

	coreaccount "github.com/allianz/yukimi/internal/account"
	"github.com/allianz/yukimi/internal/logger"
)

// SC-008: status.accountName comes from ModuleContext.ResolvedAccountName(),
// never from a module's Outcome; status.accountLocator stays blank until a
// locator is known.
func TestPersistStatus_NoLocatorYet(t *testing.T) {
	cr := newTestCR("team-a", "aws-eu-central-1")
	mc := coreaccount.NewModuleContext(cr, "team-a", nil, nil, newTestLogger(logger.OpCreate), &fakePool{})

	persistStatus(cr, mc, true, newTestLogger(logger.OpCreate))

	if cr.Status.AccountName == "" {
		t.Errorf("AccountName not set")
	}
	if cr.Status.AccountLocator != "" {
		t.Errorf("AccountLocator = %q, want empty", cr.Status.AccountLocator)
	}
	if cr.Status.AccountURL != "" {
		t.Errorf("AccountURL = %q, want empty", cr.Status.AccountURL)
	}
}

// Once a locator is known, status.accountUrl is computed via
// tenant.AccountURL from that same locator and the CRD's region.
func TestPersistStatus_WithLocator(t *testing.T) {
	cr := newTestCR("team-a", "aws-eu-central-1")
	mc := coreaccount.NewModuleContext(cr, "team-a", nil, nil, newTestLogger(logger.OpCreate), &fakePool{})
	mc.SetLocator("xc19114")

	persistStatus(cr, mc, false, newTestLogger(logger.OpCreate))

	if cr.Status.AccountLocator != "xc19114" {
		t.Errorf("AccountLocator = %q, want xc19114", cr.Status.AccountLocator)
	}
	if cr.Status.AccountURL == "" {
		t.Errorf("AccountURL not set")
	}
}

// SC-009: a tenant.AccountURL failure (malformed region: "eu-central-1" is
// missing its cloud prefix) leaves status.accountUrl blank without
// affecting accountName/accountLocator, and does not panic.
func TestPersistStatus_AccountURLFailureLeavesURLBlank(t *testing.T) {
	cr := newTestCR("team-a", "eu-central-1")
	mc := coreaccount.NewModuleContext(cr, "team-a", nil, nil, newTestLogger(logger.OpCreate), &fakePool{})
	mc.SetLocator("xc19114")

	persistStatus(cr, mc, false, newTestLogger(logger.OpCreate))

	if cr.Status.AccountLocator != "xc19114" {
		t.Errorf("AccountLocator = %q, want xc19114", cr.Status.AccountLocator)
	}
	if cr.Status.AccountURL != "" {
		t.Errorf("AccountURL = %q, want empty", cr.Status.AccountURL)
	}
}
