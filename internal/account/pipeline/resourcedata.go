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
	"fmt"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/account/tenant"
	"github.com/allianz/yukimi/internal/logger"
	"github.com/allianz/yukimi/internal/snowflake/pool"
)

// DBPool is the subset of *pool.Pool's API ResourceData depends on, defined
// here (rather than imported from internal/snowflake/pool) so any package —
// including a module under internal/account/modules/ — can inject a fake
// without reaching into pool's own unexported dial-function test seam.
// *pool.Pool satisfies this implicitly.
type DBPool interface {
	OrgAdminDB(ctx context.Context) (*sql.DB, error)
	TenantDB(ctx context.Context, namespace, accountName, locator, region string) (*sql.DB, error)
	EvictTenant(namespace, accountName string)
}

var _ DBPool = (*pool.Pool)(nil)

// ResourceData is the resource a module works on: the SnowflakeAccount CR
// (whose status modules write directly), facts derived from it, the logger
// and access to the Snowflake connections. It is built once per pipeline call
// (Observe, Apply or Destroy) and handed to every module in it. Its identity
// fields are immutable for the call — the account locator lives on the CRD's
// own status, reached via CR(), not cached here.
type ResourceData struct {
	cr              *v1alpha1.SnowflakeAccount
	namespace       string
	resolvedName    string
	namespaceLabels map[string]string
	log             *logger.Logger

	pool DBPool

	tenantDB *sql.DB
}

// NewResourceData builds the shared resource data for one pipeline call.
//
// namespace is derived from cr.Namespace, the trust anchor the resolved
// account name is derived from — so ResolvedAccountName() is computed once,
// here, and no two callers can disagree about it. namespaceLabels are the
// raw namespace labels set at onboarding; Department/CostCenter/CreditQuota
// are read from them by each module itself, not by this constructor. The
// account locator lives on cr.Status.AccountLocator directly — every module
// reads and writes it through CR(), not through a ResourceData accessor. p
// is DBPool, not the concrete *pool.Pool, so a module package's own tests
// can pass a fake. Static, binary-lifetime dependencies (e.g. backplane
// config) are not carried here — ResourceData is rebuilt per pipeline call, so
// a module that needs one injects its own copy at construction instead.
func NewResourceData(
	cr *v1alpha1.SnowflakeAccount,
	namespaceLabels map[string]string,
	log *logger.Logger,
	p DBPool,
) *ResourceData {
	namespace := cr.Namespace
	return &ResourceData{
		cr:              cr,
		namespace:       namespace,
		resolvedName:    tenant.ResolveName(cr.Name, namespace),
		namespaceLabels: namespaceLabels,
		log:             log,
		pool:            p,
	}
}

func (c *ResourceData) CR() *v1alpha1.SnowflakeAccount { return c.cr }

// ResolvedAccountName returns tenant.ResolveName(cr.Name, namespace), resolved once.
func (c *ResourceData) ResolvedAccountName() string { return c.resolvedName }

func (c *ResourceData) NamespaceLabels() map[string]string { return c.namespaceLabels }

func (c *ResourceData) Logger() *logger.Logger { return c.log }

// OrgAdminDB returns an org-admin-scoped connection. Only the account module
// (012) needs this scope.
func (c *ResourceData) OrgAdminDB(ctx context.Context) (*sql.DB, error) {
	return c.pool.OrgAdminDB(ctx)
}

// TenantDB returns a connection scoped to this tenant's own account,
// resolved on first call and memoized for the rest of the run.
//
// Returns:
//   - System error if CR().Status.AccountLocator is still empty — every
//     module calling TenantDB needs a locator, and getting one is the whole
//     point of running the account module (012) before any such module.
func (c *ResourceData) TenantDB(ctx context.Context) (*sql.DB, error) {
	if c.tenantDB != nil {
		return c.tenantDB, nil
	}
	locator := c.cr.Status.AccountLocator
	if locator == "" {
		return nil, fmt.Errorf("cannot resolve tenant connection: account locator is not yet known for %s/%s", c.namespace, c.cr.Name)
	}

	db, err := c.pool.TenantDB(ctx, c.namespace, c.cr.Name, locator, c.cr.Spec.Region)
	if err != nil {
		return nil, err
	}
	c.tenantDB = db
	return db, nil
}

// EvictTenant closes and forgets the pooled connection to this tenant's own
// account, keyed exactly as TenantDB resolves it.
func (c *ResourceData) EvictTenant() {
	c.pool.EvictTenant(c.namespace, c.cr.Name)
}
