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
	"fmt"
)

// minRSABits is the minimum RSA key size GenerateKeyPair produces.
const minRSABits = 2048

// Credentials is the JSON shape a credential is stored in: exactly three
// fields, deliberately no account field (the path already identifies it).
type Credentials struct {
	Username   string `json:"username"`
	PublicKey  string `json:"public_key"`  // PKIX, single-line base64, no PEM delimiters
	PrivateKey string `json:"private_key"` // PKCS#8, PEM-wrapped
}

// GenerateKeyPair generates a fresh RSA keypair: crypto/rand, minimum 2048-bit,
// PKCS#8-encoded private key wrapped in PEM, PKIX-encoded public key as
// single-line base64 with no PEM delimiters. One function, not two, so a
// caller can never end up with two independently generated, mismatched halves.
//
// Returns:
//   - System error if key generation fails (a cryptographic/OS-level fault)
func GenerateKeyPair() (publicKeyB64, privateKeyPEM string, err error) {
	key, err := rsa.GenerateKey(rand.Reader, minRSABits)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate RSA key pair: %w", err)
	}

	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate RSA key pair: %w", err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pubDER)

	privDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate RSA key pair: %w", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})

	return pubB64, string(privPEM), nil
}

// NewCredentials generates a fresh keypair via GenerateKeyPair and returns it
// as a Credentials value for username. username is caller-supplied domain
// knowledge (e.g. design.md 3.6's "platform") — this package owns no literal.
func NewCredentials(username string) (*Credentials, error) {
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	return &Credentials{Username: username, PublicKey: pub, PrivateKey: priv}, nil
}

// MarshalCredentials converts c to the JSON bytes a Backend stores.
func MarshalCredentials(c *Credentials) ([]byte, error) {
	return json.Marshal(c)
}

// UnmarshalCredentials converts the JSON bytes a Backend stores back into a
// Credentials value. It rejects a value with any of the three fields empty;
// it does not otherwise validate PublicKey or PrivateKey contents.
func UnmarshalCredentials(data []byte) (*Credentials, error) {
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("failed to unmarshal credentials: %w", err)
	}
	if c.Username == "" || c.PublicKey == "" || c.PrivateKey == "" {
		return nil, fmt.Errorf("failed to unmarshal credentials: username, public_key, and private_key must all be non-empty")
	}
	return &c, nil
}
