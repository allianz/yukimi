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

package account

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	coreaccount "github.com/allianz/yukimi/internal/account"
	internalerrors "github.com/allianz/yukimi/internal/errors"
)

// fakeDBPool implements coreaccount.DBPool for this package's own tests,
// letting Observe/Apply be exercised through a real *coreaccount.ModuleContext
// without a live Snowflake connection.
type fakeDBPool struct {
	t           *testing.T
	forbidCalls bool

	orgAdminDB    *sql.DB
	orgAdminErr   error
	orgAdminCalls int
	onOrgAdmin    func() // optional hook run before OrgAdmin returns, e.g. to check call ordering

	tenantDB    *sql.DB
	tenantErr   error
	tenantCalls int
}

func (f *fakeDBPool) OrgAdmin(ctx context.Context) (*sql.DB, error) {
	if f.forbidCalls {
		f.t.Fatal("OrgAdmin must not be called")
	}
	if f.onOrgAdmin != nil {
		f.onOrgAdmin()
	}
	f.orgAdminCalls++
	return f.orgAdminDB, f.orgAdminErr
}

func (f *fakeDBPool) TenantAccount(ctx context.Context, namespace, accountName, locator, region string) (*sql.DB, error) {
	if f.forbidCalls {
		f.t.Fatal("TenantAccount must not be called")
	}
	f.tenantCalls++
	return f.tenantDB, f.tenantErr
}

func newTestCR(name, namespace, region, locator string, contacts []string, description string) *v1alpha1.SnowflakeAccount {
	return &v1alpha1.SnowflakeAccount{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: v1alpha1.SnowflakeAccountSpec{
			Region:      region,
			Contacts:    contacts,
			Description: description,
		},
		Status: v1alpha1.SnowflakeAccountStatus{AccountLocator: locator},
	}
}

// SC-001: Observe returns not-in-sync with no connection attempt when no
// locator is known.
func TestObserve_NoLocator_NoConnectionAttempt(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "", []string{"a@b.com"}, "")
	mc := coreaccount.NewModuleContext(cr, "ns", nil, nil, nil, &fakeDBPool{t: t, forbidCalls: true})

	m := &module{}
	inSync, outcome := m.Observe(context.Background(), mc)

	if inSync {
		t.Error("inSync = true, want false")
	}
	if outcome != (coreaccount.Outcome{}) {
		t.Errorf("outcome = %+v, want the zero-value Outcome", outcome)
	}
}

// SC-002: Observe returns in-sync once a known locator's platform connection
// succeeds.
func TestObserve_KnownLocator_ConnectionSucceeds(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "AB12345", []string{"a@b.com"}, "")
	fake := &fakeDBPool{}
	mc := coreaccount.NewModuleContext(cr, "ns", nil, nil, nil, fake)

	m := &module{}
	inSync, outcome := m.Observe(context.Background(), mc)

	if !inSync {
		t.Error("inSync = false, want true")
	}
	if outcome.State != coreaccount.StateDone {
		t.Errorf("outcome.State = %v, want StateDone", outcome.State)
	}
	if fake.tenantCalls != 1 {
		t.Errorf("TenantAccount called %d times, want 1", fake.tenantCalls)
	}
}

// SC-003: Observe returns not-in-sync, with a system error, when a known
// locator's platform connection fails.
func TestObserve_KnownLocator_ConnectionFails(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "AB12345", []string{"a@b.com"}, "")
	wantErr := errors.New("dial failed")
	mc := coreaccount.NewModuleContext(cr, "ns", nil, nil, nil, &fakeDBPool{tenantErr: wantErr})

	m := &module{}
	inSync, outcome := m.Observe(context.Background(), mc)

	if inSync {
		t.Error("inSync = true, want false")
	}
	if outcome.State != coreaccount.StateFailed {
		t.Errorf("outcome.State = %v, want StateFailed", outcome.State)
	}
	if internalerrors.IsUserError(outcome.Err) {
		t.Errorf("expected a system error, got a user error: %v", outcome.Err)
	}
	if !errors.Is(outcome.Err, wantErr) {
		t.Errorf("outcome.Err = %v, want it to wrap %v", outcome.Err, wantErr)
	}
}
