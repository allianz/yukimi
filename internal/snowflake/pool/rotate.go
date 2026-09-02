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

package pool

import (
	"context"
	"crypto/rsa"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/allianz/yukimi/internal/secrets"
)

const (
	rsaPublicKeySlot  = "RSA_PUBLIC_KEY"
	rsaPublicKey2Slot = "RSA_PUBLIC_KEY_2"
)

// credentialDue reports whether a credential last written at rotatedAt is
// older than interval (base.BaseConfig's Secrets.RotationInterval, 002).
func credentialDue(rotatedAt time.Time, interval time.Duration) bool {
	return rotatedAt.Before(time.Now().Add(-interval))
}

// maybeRotateLocked checks the stored credential's age at path and, if it is
// due, rotates it over db. The caller must already hold the lock guarding
// path's target (keyLock for a tenant, orgAdminMu for org-admin).
//
// Any failure — reading the credential, parsing it, finding a rotation
// slot, pushing the new key into Snowflake, or writing the new credential
// back to the store — is swallowed: db is already a working connection the
// caller is about to return regardless of whether rotation succeeds, and
// the same check simply runs again on the next call.
func (p *Pool) maybeRotateLocked(ctx context.Context, db *sql.DB, path secrets.Path) {
	raw, rotatedAt, err := p.backend.Get(ctx, path)
	if err != nil || !credentialDue(rotatedAt, p.cfg.Secrets.RotationInterval) {
		return
	}
	creds, err := secrets.UnmarshalCredentials(raw, rotatedAt)
	if err != nil {
		return
	}
	key, err := parsePrivateKey(creds.PrivateKey)
	if err != nil {
		return
	}
	_ = p.rotateCredential(ctx, db, path, creds.Username, key)
}

// rotateCredential generates a fresh keypair, pushes its public half into
// whichever of Snowflake's two key slots does not match currentKey's
// fingerprint, over db, and only once that succeeds writes the new keypair
// to the secret store at path. The slot currently in use is never touched
// until that write succeeds, so a failure at any step leaves the
// credential db is already authenticated with exactly as valid as before.
func (p *Pool) rotateCredential(ctx context.Context, db *sql.DB, path secrets.Path, username string, currentKey *rsa.PrivateKey) error {
	slot, err := targetSlot(ctx, db, username, publicKeyFingerprint(currentKey))
	if err != nil {
		return fmt.Errorf("failed to determine rotation slot for %s: %w", username, err)
	}

	fresh, err := secrets.NewCredentials(username)
	if err != nil {
		return fmt.Errorf("failed to generate rotated credentials for %s: %w", username, err)
	}

	stmt := fmt.Sprintf("ALTER USER %s SET %s = '%s'", quoteIdentifier(username), slot, fresh.PublicKey)
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("failed to push rotated public key into %s for %s: %w", slot, username, err)
	}

	value, err := secrets.MarshalCredentials(fresh)
	if err != nil {
		return fmt.Errorf("failed to marshal rotated credentials for %s: %w", username, err)
	}
	if err := p.backend.Update(ctx, path, value); err != nil {
		return fmt.Errorf("failed to store rotated credentials for %s: %w", username, err)
	}
	return nil
}

// targetSlot queries DESC USER over db and returns whichever of
// RSA_PUBLIC_KEY/RSA_PUBLIC_KEY_2 does not match currentFingerprint — the
// slot safe to overwrite. Whether that slot is empty or holds an older,
// already-superseded key makes no difference.
func targetSlot(ctx context.Context, db *sql.DB, username, currentFingerprint string) (string, error) {
	rows, err := db.QueryContext(ctx, "DESC USER "+quoteIdentifier(username))
	if err != nil {
		return "", err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}

	var slot1FP, slot2FP string
	for rows.Next() {
		raw := make([]sql.NullString, len(cols))
		dest := make([]any, len(cols))
		for i := range raw {
			dest[i] = &raw[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return "", err
		}
		if len(raw) < 2 {
			continue
		}
		switch raw[0].String {
		case "RSA_PUBLIC_KEY_FP":
			slot1FP = raw[1].String
		case "RSA_PUBLIC_KEY_2_FP":
			slot2FP = raw[1].String
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	switch {
	case slot1FP == currentFingerprint:
		return rsaPublicKey2Slot, nil
	case slot2FP == currentFingerprint:
		return rsaPublicKeySlot, nil
	default:
		return "", fmt.Errorf("neither key slot's fingerprint on %s matches the stored credential", username)
	}
}

// quoteIdentifier double-quotes a Snowflake identifier, doubling any
// embedded quote. Only ever called with usernames this package itself
// generated or read from the secret store — never with tenant-controlled
// input. The stored username (e.g. "platform") is the value CREATE
// ACCOUNT's unquoted ADMIN_NAME parameter produced (design.md 3.6), and
// Snowflake folds an unquoted identifier to uppercase when it creates the
// object; quoting the stored value verbatim would instead address it
// case-sensitively and never match, so it is uppercased first.
func quoteIdentifier(s string) string {
	return `"` + strings.ReplaceAll(strings.ToUpper(s), `"`, `""`) + `"`
}
