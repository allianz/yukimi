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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/snowflakedb/gosnowflake"

	"github.com/allianz/yukimi/internal/secrets"
)

// SC-015, SC-016, SC-022: buildSnowflakeConfig maps every field exactly, for
// both scopes, and never sets Region — a set Region would make the driver
// rewrite Config.Host from its own notion of a raw Snowflake region code
// before dialing, silently overriding the host this package built.
func TestBuildSnowflakeConfig(t *testing.T) {
	_, pemStr, err := secrets.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	key, err := parsePrivateKey(pemStr)
	if err != nil {
		t.Fatalf("parsePrivateKey: %v", err)
	}

	cases := []struct {
		name              string
		account           string
		host              string
		user              string
		role              string
		disableOCSPChecks bool
	}{
		{"org-admin", "xc00000", "xc00000.eu-central-1.snowflakecomputing.com", "platform", "ORGADMIN", false},
		{"tenant", "xc19114", "xc19114.eu-central-1.privatelink.snowflakecomputing.com", "platform", "ACCOUNTADMIN", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := buildSnowflakeConfig(c.account, c.host, c.user, key, c.role, c.disableOCSPChecks)
			if cfg.Account != c.account {
				t.Errorf("Account = %q, want %q", cfg.Account, c.account)
			}
			if cfg.Host != c.host {
				t.Errorf("Host = %q, want %q", cfg.Host, c.host)
			}
			if cfg.User != c.user {
				t.Errorf("User = %q, want %q", cfg.User, c.user)
			}
			if cfg.Role != c.role {
				t.Errorf("Role = %q, want %q", cfg.Role, c.role)
			}
			if cfg.PrivateKey != key {
				t.Errorf("PrivateKey not set to the parsed key")
			}
			if cfg.Authenticator != gosnowflake.AuthTypeJwt {
				t.Errorf("Authenticator = %v, want AuthTypeJwt", cfg.Authenticator)
			}
			if cfg.DisableOCSPChecks != c.disableOCSPChecks {
				t.Errorf("DisableOCSPChecks = %v, want %v", cfg.DisableOCSPChecks, c.disableOCSPChecks)
			}
			if cfg.Region != "" {
				t.Errorf("Region = %q, want empty — a set Region lets the driver rewrite Host", cfg.Region)
			}
		})
	}
}

// parsePrivateKey round-trips a real keypair from secrets.GenerateKeyPair,
// and rejects every malformed shape it might otherwise be handed.
func TestParsePrivateKey(t *testing.T) {
	t.Run("valid PKCS#8 RSA key round-trips", func(t *testing.T) {
		_, pemStr, err := secrets.GenerateKeyPair()
		if err != nil {
			t.Fatalf("GenerateKeyPair: %v", err)
		}
		key, err := parsePrivateKey(pemStr)
		if err != nil {
			t.Fatalf("parsePrivateKey: %v", err)
		}
		if key == nil {
			t.Fatal("expected a non-nil key")
		}
	})

	t.Run("not PEM", func(t *testing.T) {
		if _, err := parsePrivateKey("not a pem block"); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("PEM but not PKCS#8", func(t *testing.T) {
		block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not valid PKCS#8 DER")})
		if _, err := parsePrivateKey(string(block)); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("PKCS#8 but not RSA", func(t *testing.T) {
		ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("ecdsa.GenerateKey: %v", err)
		}
		der, err := x509.MarshalPKCS8PrivateKey(ecKey)
		if err != nil {
			t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
		}
		block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		if _, err := parsePrivateKey(string(block)); err == nil {
			t.Fatal("expected an error for a non-RSA key")
		}
	})
}

// defaultDial's failure path: a non-routable host with a short probe timeout
// returns an error promptly. The success path needs a real Snowflake
// account, so it is exercised only by integration_test.go.
func TestDefaultDial_ProbeFailure(t *testing.T) {
	dc := dialConfig{
		snowflake: gosnowflake.Config{
			Account: "nonexistent",
			Host:    "invalid.invalid",
			User:    "nobody",
		},
		probeTimeout: 200 * time.Millisecond,
	}

	done := make(chan struct{})
	var err error
	go func() {
		_, err = defaultDial(dc)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("defaultDial did not return promptly on an unreachable host")
	}
	if err == nil {
		t.Fatal("expected an error for an unreachable host")
	}
}
