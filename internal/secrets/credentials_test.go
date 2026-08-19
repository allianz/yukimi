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

package secrets

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	stderrors "errors"
	"io"
	"strings"
	"testing"

	"github.com/allianz/yukimi/internal/errors"
)

// errNoEntropy is what withoutEntropy's reader fails with, so a test can assert
// the failure it injected is the one that surfaced.
var errNoEntropy = stderrors.New("no entropy")

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errNoEntropy }

// withoutEntropy swaps crypto/rand's reader for one that always fails, so the
// key-generation failure path — otherwise unreachable — can be exercised. It is
// restored when the test ends, so no test here may call t.Parallel.
func withoutEntropy(t *testing.T) {
	t.Helper()
	original := rand.Reader
	t.Cleanup(func() { rand.Reader = original })
	rand.Reader = io.Reader(failingReader{})
}

// SC-007: Credentials marshals to JSON with exactly username/public_key/private_key.
func TestMarshalCredentials_FieldNames(t *testing.T) {
	c := &Credentials{Username: "platform", PublicKey: "pub", PrivateKey: "priv"}
	data, err := MarshalCredentials(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var m map[string]string
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 3 {
		t.Fatalf("got %d fields, want 3: %v", len(m), m)
	}
	if m["username"] != "platform" || m["public_key"] != "pub" || m["private_key"] != "priv" {
		t.Errorf("unexpected field values: %v", m)
	}
	if _, ok := m["account"]; ok {
		t.Error("expected no 'account' field")
	}
}

// SC-008: GenerateKeyPair produces a minimum 2048-bit RSA key: PKCS#8/PEM
// private key, PKIX single-line-base64 public key with no PEM delimiters.
func TestGenerateKeyPair_ProducesValidKey(t *testing.T) {
	pubB64, privPEM, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(pubB64, "\n") || strings.Contains(pubB64, "-----BEGIN") {
		t.Errorf("public key must be single-line base64 with no PEM delimiters, got %q", pubB64)
	}
	pubDER, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		t.Fatalf("public key is not valid base64: %v", err)
	}
	pub, err := x509.ParsePKIXPublicKey(pubDER)
	if err != nil {
		t.Fatalf("public key is not valid PKIX: %v", err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("public key is not RSA: %T", pub)
	}
	if rsaPub.N.BitLen() < minRSABits {
		t.Errorf("public key is %d bits, want >= %d", rsaPub.N.BitLen(), minRSABits)
	}

	block, _ := pem.Decode([]byte(privPEM))
	if block == nil {
		t.Fatal("private key is not valid PEM")
	}
	priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("private key is not valid PKCS#8: %v", err)
	}
	rsaPriv, ok := priv.(*rsa.PrivateKey)
	if !ok {
		t.Fatalf("private key is not RSA: %T", priv)
	}
	if rsaPriv.N.BitLen() < minRSABits {
		t.Errorf("private key is %d bits, want >= %d", rsaPriv.N.BitLen(), minRSABits)
	}
}

// SC-008 (via NewCredentials): username is carried through as given.
func TestNewCredentials_SetsUsername(t *testing.T) {
	c, err := NewCredentials("platform")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Username != "platform" {
		t.Errorf("got %q, want %q", c.Username, "platform")
	}
	if c.PublicKey == "" || c.PrivateKey == "" {
		t.Error("expected non-empty PublicKey and PrivateKey")
	}
}

// SC-008: a key-generation failure is a system error, wrapped so the
// underlying cause stays matchable.
func TestGenerateKeyPair_FailureIsSystemError(t *testing.T) {
	withoutEntropy(t)

	_, _, err := GenerateKeyPair()
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.IsUserError(err) {
		t.Error("expected a system error, not a user error")
	}
	if !stderrors.Is(err, errNoEntropy) {
		t.Errorf("expected the wrap to preserve the underlying cause, got %v", err)
	}
}

// NewCredentials propagates GenerateKeyPair's failure rather than returning a
// half-filled Credentials.
func TestNewCredentials_PropagatesKeyGenerationFailure(t *testing.T) {
	withoutEntropy(t)

	c, err := NewCredentials("platform")
	if err == nil {
		t.Fatal("expected error")
	}
	if c != nil {
		t.Error("expected no Credentials alongside the error")
	}
}

// SC-009: UnmarshalCredentials rejects a value with any of the three fields empty.
func TestUnmarshalCredentials_RejectsEmptyField(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{"empty username", `{"username":"","public_key":"pub","private_key":"priv"}`},
		{"empty public_key", `{"username":"platform","public_key":"","private_key":"priv"}`},
		{"empty private_key", `{"username":"platform","public_key":"pub","private_key":""}`},
	}
	for _, tt := range tests {
		if _, err := UnmarshalCredentials(tt.json); err == nil {
			t.Errorf("%s: expected error, got nil", tt.name)
		}
	}
}

// SC-009: UnmarshalCredentials rejects malformed JSON as a system error.
func TestUnmarshalCredentials_RejectsMalformedJSON(t *testing.T) {
	if _, err := UnmarshalCredentials("not json"); err == nil {
		t.Error("expected error for malformed JSON")
	}
}

// SC-009: A well-formed value with all three fields populated round-trips cleanly.
func TestUnmarshalCredentials_WellFormedRoundTrip(t *testing.T) {
	c := &Credentials{Username: "platform", PublicKey: "pub", PrivateKey: "priv"}
	data, err := MarshalCredentials(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := UnmarshalCredentials(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *got != *c {
		t.Errorf("got %+v, want %+v", got, c)
	}
}
