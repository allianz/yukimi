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

package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/account/tenant"
	internalerrors "github.com/allianz/yukimi/internal/errors"
)

// tenantArgs captures one call to fakeDBPool.TenantAccount.
type tenantArgs struct {
	namespace, accountName, locator, region string
}

// fakeDBPool implements DBPool for tests, recording call counts/args and
// optionally failing the test if either method is invoked at all.
type fakeDBPool struct {
	t           *testing.T
	forbidCalls bool

	orgAdminDB    *sql.DB
	orgAdminErr   error
	orgAdminCalls int

	tenantDB    *sql.DB
	tenantErr   error
	tenantCalls int
	tenantArgs  []tenantArgs
}

func (f *fakeDBPool) OrgAdmin(ctx context.Context) (*sql.DB, error) {
	if f.forbidCalls {
		f.t.Fatal("OrgAdmin must not be called")
	}
	f.orgAdminCalls++
	return f.orgAdminDB, f.orgAdminErr
}

func (f *fakeDBPool) TenantAccount(ctx context.Context, namespace, accountName, locator, region string) (*sql.DB, error) {
	if f.forbidCalls {
		f.t.Fatal("TenantAccount must not be called")
	}
	f.tenantCalls++
	f.tenantArgs = append(f.tenantArgs, tenantArgs{namespace, accountName, locator, region})
	return f.tenantDB, f.tenantErr
}

func newTestCR(name, namespace, region, locator string) *v1alpha1.SnowflakeAccount {
	return &v1alpha1.SnowflakeAccount{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       v1alpha1.SnowflakeAccountSpec{Region: region},
		Status:     v1alpha1.SnowflakeAccountStatus{AccountLocator: locator},
	}
}

// SC-009: ModuleContext.TenantDB returns a system error when Locator() is
// empty, and never calls the pool when it is.
func TestTenantDB_EmptyLocator_NeverCallsPool(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "")
	fake := &fakeDBPool{t: t, forbidCalls: true}
	mc := NewModuleContext(cr, "ns", nil, nil, nil, fake)

	_, err := mc.TenantDB(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if internalerrors.IsUserError(err) {
		t.Errorf("expected a system error, got a user error: %v", err)
	}
}

// SC-010: ModuleContext.TenantDB resolves the connection once and returns
// the same *sql.DB on every subsequent call within the same context.
func TestTenantDB_MemoizesSameDB(t *testing.T) {
	db1, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error: %v", err)
	}
	t.Cleanup(func() { _ = db1.Close() })

	cr := newTestCR("acct", "ns", "aws-eu-central-1", "AB12345")
	fake := &fakeDBPool{tenantDB: db1}
	mc := NewModuleContext(cr, "ns", nil, nil, nil, fake)

	got1, err := mc.TenantDB(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got2, err := mc.TenantDB(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got1 != db1 || got2 != db1 {
		t.Error("TenantDB did not return the pool's *sql.DB")
	}
	if fake.tenantCalls != 1 {
		t.Errorf("TenantAccount called %d times, want 1", fake.tenantCalls)
	}
}

// TenantDB must pass cr.Name (the CRD's bare metadata.name), not
// ResolvedAccountName(), and cr.Spec.Region as the region string.
func TestTenantDB_PassesRawCRNameNotResolvedName(t *testing.T) {
	db1, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error: %v", err)
	}
	t.Cleanup(func() { _ = db1.Close() })

	cr := newTestCR("analytics-team", "finance", "aws-eu-central-1", "AB12345")
	fake := &fakeDBPool{tenantDB: db1}
	mc := NewModuleContext(cr, "finance", nil, nil, nil, fake)

	if _, err := mc.TenantDB(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fake.tenantArgs) != 1 {
		t.Fatalf("len(tenantArgs) = %d, want 1", len(fake.tenantArgs))
	}
	got := fake.tenantArgs[0]
	if got.accountName != cr.Name {
		t.Errorf("accountName = %q, want cr.Name %q", got.accountName, cr.Name)
	}
	if got.accountName == mc.ResolvedAccountName() {
		t.Errorf("accountName must not equal ResolvedAccountName(): both were %q", got.accountName)
	}
	if got.namespace != "finance" {
		t.Errorf("namespace = %q, want %q", got.namespace, "finance")
	}
	if got.region != cr.Spec.Region {
		t.Errorf("region = %q, want cr.Spec.Region %q", got.region, cr.Spec.Region)
	}
	if got.locator != "AB12345" {
		t.Errorf("locator = %q, want %q", got.locator, "AB12345")
	}
}

// A failed TenantDB call (empty locator) must not be cached — once the
// locator is set, a later call must still try the pool.
func TestTenantDB_FailureNotCached(t *testing.T) {
	db1, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error: %v", err)
	}
	t.Cleanup(func() { _ = db1.Close() })

	cr := newTestCR("acct", "ns", "aws-eu-central-1", "")
	fake := &fakeDBPool{tenantDB: db1}
	mc := NewModuleContext(cr, "ns", nil, nil, nil, fake)

	if _, err := mc.TenantDB(context.Background()); err == nil {
		t.Fatal("expected an error on first call with empty locator")
	}
	if fake.tenantCalls != 0 {
		t.Errorf("TenantAccount called %d times before locator was set, want 0", fake.tenantCalls)
	}

	mc.SetLocator("AB12345")
	got, err := mc.TenantDB(context.Background())
	if err != nil {
		t.Fatalf("unexpected error after SetLocator: %v", err)
	}
	if got != db1 {
		t.Error("TenantDB did not return the pool's *sql.DB after SetLocator")
	}
	if fake.tenantCalls != 1 {
		t.Errorf("TenantAccount called %d times, want 1", fake.tenantCalls)
	}
}

// TenantDB passes through a pool error without caching it.
func TestTenantDB_PoolError(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "AB12345")
	wantErr := errors.New("dial failed")
	fake := &fakeDBPool{tenantErr: wantErr}
	mc := NewModuleContext(cr, "ns", nil, nil, nil, fake)

	_, err := mc.TenantDB(context.Background())
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if fake.tenantCalls != 1 {
		t.Errorf("TenantAccount called %d times, want 1", fake.tenantCalls)
	}
}

// OrgAdminDB passes through to the pool, including error passthrough.
func TestOrgAdminDB_PassesThrough(t *testing.T) {
	db1, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error: %v", err)
	}
	t.Cleanup(func() { _ = db1.Close() })

	cr := newTestCR("acct", "ns", "aws-eu-central-1", "")
	fake := &fakeDBPool{orgAdminDB: db1}
	mc := NewModuleContext(cr, "ns", nil, nil, nil, fake)

	got, err := mc.OrgAdminDB(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != db1 {
		t.Error("OrgAdminDB did not return the pool's *sql.DB")
	}

	wantErr := errors.New("dial failed")
	fake2 := &fakeDBPool{orgAdminErr: wantErr}
	mc2 := NewModuleContext(cr, "ns", nil, nil, nil, fake2)
	if _, err := mc2.OrgAdminDB(context.Background()); err != wantErr {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}

// SC-011: ModuleContext.ResolvedAccountName returns the same value
// tenant.ResolveName would compute directly from the same CRD name and
// namespace.
func TestNewModuleContext_ResolvedAccountName(t *testing.T) {
	cr := newTestCR("analytics-team", "finance", "aws-eu-central-1", "")
	mc := NewModuleContext(cr, "finance", nil, nil, nil, nil)

	want := tenant.ResolveName(cr.Name, "finance")
	if got := mc.ResolvedAccountName(); got != want {
		t.Errorf("ResolvedAccountName() = %q, want %q", got, want)
	}
}

// NewModuleContext seeds Locator() from cr.Status.AccountLocator when set.
func TestNewModuleContext_SeedsLocatorFromStatus(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "AB12345")
	mc := NewModuleContext(cr, "ns", nil, nil, nil, &fakeDBPool{t: t, forbidCalls: true})

	if got := mc.Locator(); got != "AB12345" {
		t.Errorf("Locator() = %q, want %q", got, "AB12345")
	}
}

// NewModuleContext leaves Locator() empty when cr.Status.AccountLocator is
// empty.
func TestNewModuleContext_EmptyStatusLocator(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "")
	mc := NewModuleContext(cr, "ns", nil, nil, nil, &fakeDBPool{t: t, forbidCalls: true})

	if got := mc.Locator(); got != "" {
		t.Errorf("Locator() = %q, want empty", got)
	}
}

// SetLocator overrides whatever Locator() previously returned.
func TestSetLocator_Overrides(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "")
	mc := NewModuleContext(cr, "ns", nil, nil, nil, &fakeDBPool{t: t, forbidCalls: true})

	mc.SetLocator("XY999")
	if got := mc.Locator(); got != "XY999" {
		t.Errorf("Locator() = %q, want %q", got, "XY999")
	}
}

// Plain accessors return exactly what was passed to NewModuleContext.
func TestModuleContext_Accessors(t *testing.T) {
	cr := newTestCR("acct", "ns", "aws-eu-central-1", "")
	labels := map[string]string{"department": "analytics"}

	mc := NewModuleContext(cr, "ns", nil, labels, nil, &fakeDBPool{t: t, forbidCalls: true})

	if mc.CR() != cr {
		t.Error("CR() did not return the exact CRD pointer passed in")
	}
	if got := mc.NamespaceLabels(); got["department"] != "analytics" {
		t.Errorf("NamespaceLabels() = %v, want department=analytics", got)
	}
	if mc.Logger() != nil {
		t.Error("Logger() should be nil when nil was passed in")
	}
	if mc.BackplaneRegion() != nil {
		t.Error("BackplaneRegion() should be nil when nil was passed in")
	}
}
