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
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/snowflakedb/gosnowflake"
)

// dialConfig carries everything defaultDial needs to open and probe one
// *sql.DB. It is a struct, not two separate parameters, so tests can swap
// Pool.dial for a function with this exact signature (see the package's own
// tests) without a real Snowflake account, network call, or driver.
type dialConfig struct {
	snowflake    gosnowflake.Config
	probeTimeout time.Duration
}

// dialFunc is the seam Pool.dial holds. defaultDial is the only production
// implementation; unit tests substitute a fake to exercise Pool's caching,
// eviction, self-healing, and concurrency behavior without touching the real
// driver.
type dialFunc func(dialConfig) (*sql.DB, error)

// defaultDial opens a *sql.DB via the real gosnowflake driver and runs a
// health probe (PingContext, which lazily authenticates on first use and then
// round-trips a query) so a bad credential or unreachable host fails
// immediately rather than on some later caller's first real query.
func defaultDial(dc dialConfig) (*sql.DB, error) {
	connector := gosnowflake.NewConnector(gosnowflake.SnowflakeDriver{}, dc.snowflake)
	db := sql.OpenDB(connector)

	ctx, cancel := context.WithTimeout(context.Background(), dc.probeTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// buildSnowflakeConfig builds the gosnowflake.Config shared by both
// connection scopes. It deliberately never sets Config.Region: if set, the
// driver rewrites Config.Host from its own notion of a raw Snowflake region
// code before dialing, which would silently override the host this package
// built specifically to get the PrivateLink suffix right.
func buildSnowflakeConfig(account, host, user string, key *rsa.PrivateKey, role string) gosnowflake.Config {
	return gosnowflake.Config{
		Account:       account,
		Host:          host,
		User:          user,
		PrivateKey:    key,
		Role:          role,
		Authenticator: gosnowflake.AuthTypeJwt,
	}
}

// parsePrivateKey parses a PEM-wrapped PKCS#8 private key, the form
// secrets.Credentials.PrivateKey is stored in, into an *rsa.PrivateKey.
func parsePrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse PKCS#8 private key: %w", err)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not an RSA key")
	}
	return rsaKey, nil
}
