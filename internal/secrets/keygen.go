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
	"encoding/pem"
	"fmt"
)

const minRSAKeyBits = 2048

type rsaKeyPair struct {
	// PublicKey is single-line base64 without PEM delimiters,
	// used directly in CREATE ACCOUNT and ALTER USER SQL commands.
	PublicKey string
	// PrivateKey is PKCS#8 format with PEM delimiters,
	// used directly by the Snowflake Go driver for JWT authentication.
	PrivateKey string
}

// generateRSAKeyPair generates a new RSA key pair with minimum 2048-bit key size.
// Uses crypto/rand for cryptographically secure random numbers.
func generateRSAKeyPair() (*rsaKeyPair, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, minRSAKeyBits)
	if err != nil {
		return nil, fmt.Errorf("failed to generate RSA key: %w", err)
	}

	privateKeyBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal private key to PKCS#8: %w", err)
	}

	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: privateKeyBytes,
	})

	publicKeyBytes, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal public key to PKIX: %w", err)
	}

	// Single-line base64 without PEM delimiters as required by Snowflake SQL commands.
	publicKeyBase64 := base64.StdEncoding.EncodeToString(publicKeyBytes)

	return &rsaKeyPair{
		PublicKey:  publicKeyBase64,
		PrivateKey: string(privateKeyPEM),
	}, nil
}
