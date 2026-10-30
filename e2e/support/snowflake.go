//go:build e2e

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

package support

import (
	"context"
	"database/sql"
	"fmt"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/secrets"
	secretsaws "github.com/allianz/yukimi/internal/secrets/aws"
	"github.com/allianz/yukimi/internal/snowflake/pool"
	"github.com/allianz/yukimi/internal/snowflake/statement"
)

// The Snowflake-side checks use the same connection pool as the operator and the
// existing integration tests: the org-admin credential is read from AWS Secrets
// Manager and the connection is made over the configured route (PrivateLink).
// The pool is opened on the first check, so runs that never look into
// Snowflake do not connect at all.

// orgAdminDB returns a connection as the organization administrator.
func (e *Env) orgAdminDB(ctx context.Context) (*sql.DB, error) {
	e.poolOnce.Do(func() {
		base := e.Cfg.Base
		store, err := secretsaws.New(base.AWS.Region, base.AWS.KmsKeyId, base.Deletion.GracePeriodDays)
		if err != nil {
			e.poolErr = fmt.Errorf("creating the AWS key store: %w", err)
			return
		}
		e.pool = pool.New(secrets.NewKeyManager(store, base.Secrets.CacheTTL), base)
	})
	if e.poolErr != nil {
		return nil, e.poolErr
	}
	return e.pool.OrgAdminDB(ctx)
}

// AccountRow looks an account up by its resolved Snowflake name with
// SHOW ACCOUNTS. The result is not found (Row.Found is false) when the
// organization does not list the account, which is also the case for an
// account that has been dropped.
func (e *Env) AccountRow(ctx context.Context, resolvedName string) (statement.Row, error) {
	db, err := e.orgAdminDB(ctx)
	if err != nil {
		return nil, fmt.Errorf("connecting to Snowflake as the org admin: %w", err)
	}
	// LIKE treats '_' as a wildcard, so the result is matched exactly afterwards.
	res, err := statement.New(db).Query(ctx, "e2e: look up the account", "SHOW ACCOUNTS LIKE ?", resolvedName)
	if err != nil {
		return nil, err
	}
	return res.FindRow("account_name", resolvedName), nil
}

// Close releases the Snowflake connections, if any were opened.
func (e *Env) Close() {
	if e.pool != nil {
		_ = e.pool.Close()
	}
}

// PlatformEmail returns the email of the platform user inside the account, read
// over the same tenant connection the operator uses. The platform user is the
// one the operator keeps in step with spec.contact.
func (e *Env) PlatformEmail(ctx context.Context, account *v1alpha1.SnowflakeAccount) (string, error) {
	if _, err := e.orgAdminDB(ctx); err != nil { // opens the pool
		return "", err
	}
	db, err := e.pool.TenantDB(ctx, account.Namespace, account.Name, account.Status.AccountLocator, account.Spec.Region)
	if err != nil {
		return "", fmt.Errorf("connecting to the account: %w", err)
	}
	res, err := statement.New(db).Query(ctx, "e2e: look up the platform user", "SHOW USERS LIKE ?", "platform")
	if err != nil {
		return "", err
	}
	email, _ := res.FindRow("name", "platform").StringValue("email")
	return email, nil
}
