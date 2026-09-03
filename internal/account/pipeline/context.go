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
	"github.com/allianz/yukimi/internal/config/backplane"
	"github.com/allianz/yukimi/internal/logger"
	"github.com/allianz/yukimi/internal/snowflake/pool"
)

// DBPool is the subset of *pool.Pool's API ModuleContext depends on, defined
// here (rather than imported from internal/snowflake/pool) so any package —
// including a module under internal/account/modules/ — can inject a fake
// without reaching into pool's own unexported dial-function test seam.
// *pool.Pool satisfies this implicitly.
type DBPool interface {
	OrgAdmin(ctx context.Context) (*sql.DB, error)
	TenantAccount(ctx context.Context, namespace, accountName, locator, region string) (*sql.DB, error)
}

var _ DBPool = (*pool.Pool)(nil)

// ModuleContext is built once per reconcile and handed unchanged to every
// module. Everything on it is either immutable for the run or, in the case of
// the account locator, mutated by exactly one module (012).
type ModuleContext struct {
	cr              *v1alpha1.SnowflakeAccount
	namespace       string
	resolvedName    string
	backplaneRegion *backplane.Region
	namespaceLabels map[string]string
	log             *logger.Logger

	pool DBPool

	locator string

	tenantDB *sql.DB
}

// NewModuleContext builds the shared context for one reconcile.
//
// namespace is the trust anchor the resolved account name is derived from —
// callers pass the bare namespace, not a pre-resolved name, so
// ResolvedAccountName() is computed once, here, and no two callers can
// disagree about it. namespaceLabels are the raw namespace labels set at
// onboarding; Department/CostCenter/CreditQuota are read from them by each
// module itself, not by this constructor. If cr.Status.AccountLocator is
// already set, it seeds Locator() immediately — callers never seed it
// themselves; see SetLocator for the only other way it changes. p is
// DBPool, not the concrete *pool.Pool, so a module package's own tests can
// pass a fake.
func NewModuleContext(
	cr *v1alpha1.SnowflakeAccount,
	namespace string,
	backplaneRegion *backplane.Region,
	namespaceLabels map[string]string,
	log *logger.Logger,
	p DBPool,
) *ModuleContext {
	return &ModuleContext{
		cr:              cr,
		namespace:       namespace,
		resolvedName:    tenant.ResolveName(cr.Name, namespace),
		backplaneRegion: backplaneRegion,
		namespaceLabels: namespaceLabels,
		log:             log,
		pool:            p,
		locator:         cr.Status.AccountLocator,
	}
}

func (c *ModuleContext) CR() *v1alpha1.SnowflakeAccount { return c.cr }

// ResolvedAccountName returns tenant.ResolveName(cr.Name, namespace), resolved once.
func (c *ModuleContext) ResolvedAccountName() string { return c.resolvedName }

func (c *ModuleContext) BackplaneRegion() *backplane.Region { return c.backplaneRegion }

func (c *ModuleContext) NamespaceLabels() map[string]string { return c.namespaceLabels }

func (c *ModuleContext) Logger() *logger.Logger { return c.log }

// Locator returns the account locator, or "" if the account does not exist
// yet on this reconcile. Seeded by NewModuleContext from
// cr.Status.AccountLocator when already set; see SetLocator for the only way
// it changes afterward.
func (c *ModuleContext) Locator() string { return c.locator }

// SetLocator records the locator immediately after CREATE ACCOUNT returns it,
// for the one reconcile where the account did not exist before this call.
// Only the account module (012) calls this.
func (c *ModuleContext) SetLocator(locator string) { c.locator = locator }

// OrgAdminDB returns an org-admin-scoped connection. Only the account module
// (012) needs this scope.
func (c *ModuleContext) OrgAdminDB(ctx context.Context) (*sql.DB, error) {
	return c.pool.OrgAdmin(ctx)
}

// TenantDB returns a connection scoped to this tenant's own account,
// resolved on first call and memoized for the rest of the run.
//
// Returns:
//   - System error if Locator() is still empty — every module calling
//     TenantDB needs a locator, and getting one is the whole point of
//     running the account module (012) before any such module.
func (c *ModuleContext) TenantDB(ctx context.Context) (*sql.DB, error) {
	if c.tenantDB != nil {
		return c.tenantDB, nil
	}
	if c.locator == "" {
		return nil, fmt.Errorf("cannot resolve tenant connection: account locator is not yet known for %s/%s", c.namespace, c.cr.Name)
	}

	db, err := c.pool.TenantAccount(ctx, c.namespace, c.cr.Name, c.locator, c.cr.Spec.Region)
	if err != nil {
		return nil, err
	}
	c.tenantDB = db
	return db, nil
}
