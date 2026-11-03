//go:build scaletest

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

package scaletest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"

	"github.com/allianz/yukimi/internal/secrets"
	secretsaws "github.com/allianz/yukimi/internal/secrets/aws"
	"github.com/allianz/yukimi/internal/snowflake/pool"
	"github.com/allianz/yukimi/internal/snowflake/statement"
)

// snowflakeAccounts returns the resolved names of the accounts of the scale test
// that Snowflake still lists, whichever namespace they belong to: every name
// starts with the account prefix. A dropped account is not listed.
func (e *Env) snowflakeAccounts(ctx context.Context) ([]string, error) {
	if e.listSnowflake != nil {
		return e.listSnowflake(ctx)
	}
	db, err := e.orgAdminDB(ctx)
	if err != nil {
		return nil, fmt.Errorf("connecting to Snowflake as the org admin: %w", err)
	}
	res, err := statement.New(db).Query(ctx, "scaletest: list the accounts", "SHOW ACCOUNTS LIKE ?", accountPrefix+"%")
	if err != nil {
		return nil, err
	}
	return scaletestAccounts(res.Rows), nil
}

// scaletestAccounts picks the accounts of the scale test out of the rows of SHOW
// ACCOUNTS. LIKE ignores case and treats '_' as a wildcard, so the prefix is
// checked again here.
func scaletestAccounts(rows []statement.Row) []string {
	var names []string
	for _, row := range rows {
		if name, ok := row.StringValue("account_name"); ok && strings.HasPrefix(strings.ToLower(name), accountPrefix+"_") {
			names = append(names, name)
		}
	}
	return names
}

// secretsAPI is the part of AWS Secrets Manager the verification uses.
type secretsAPI interface {
	secretsmanager.ListSecretsAPIClient
	DescribeSecret(ctx context.Context, in *secretsmanager.DescribeSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.DescribeSecretOutput, error)
}

// secretsNotScheduled returns the credential secrets of the scale test that exist
// and are not scheduled for deletion. It lists by the name prefix of the namespace,
// so it also finds those of accounts that earlier delete runs removed. It needs
// secretsmanager:ListSecrets and reads metadata only, never a value.
func (e *Env) secretsNotScheduled(ctx context.Context) ([]string, error) {
	if e.listSecrets != nil {
		return e.listSecrets(ctx)
	}
	id, err := secrets.NewTenantIdentifier(e.Cfg.Org(), Namespace, "x")
	if err != nil {
		return nil, err
	}
	return notScheduledSecrets(ctx, secretsmanager.NewFromConfig(e.AWS), strings.TrimSuffix(id.String(), "x"))
}

// notScheduledSecrets lists the secrets whose name starts with prefix. The listing
// may lag behind recent changes by up to five minutes (the SDK says so), so a secret
// that it shows as not scheduled is looked at again with DescribeSecret, which is
// current, before it is reported.
func notScheduledSecrets(ctx context.Context, api secretsAPI, prefix string) ([]string, error) {
	pages := secretsmanager.NewListSecretsPaginator(api, &secretsmanager.ListSecretsInput{
		Filters: []smtypes.Filter{{Key: smtypes.FilterNameStringTypeName, Values: []string{prefix}}},
	})
	var candidates []string
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing the secrets with the prefix %q (the credentials need secretsmanager:ListSecrets): %w", prefix, err)
		}
		candidates = append(candidates, listedNotScheduled(page.SecretList, prefix)...)
	}

	var left []string
	for _, name := range candidates {
		out, err := api.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{SecretId: aws.String(name)})
		var notFound *smtypes.ResourceNotFoundException
		switch {
		case errors.As(err, &notFound):
		case err != nil:
			return nil, fmt.Errorf("looking up the secret %s: %w", name, err)
		case out.DeletedDate == nil:
			left = append(left, name)
		}
	}
	return left, nil
}

// listedNotScheduled picks the names that start with prefix and have no deletion date.
func listedNotScheduled(entries []smtypes.SecretListEntry, prefix string) []string {
	var names []string
	for _, s := range entries {
		if name := aws.ToString(s.Name); strings.HasPrefix(name, prefix) && s.DeletedDate == nil {
			names = append(names, name)
		}
	}
	return names
}

// orgAdminDB opens the Snowflake pool on first use, so runs that never look into
// Snowflake do not connect at all, and returns the org-admin connection.
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

// Close releases the Snowflake connections, if any were opened.
func (e *Env) Close() {
	if e.pool != nil {
		_ = e.pool.Close()
	}
}
