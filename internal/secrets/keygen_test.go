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
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
)

// SC-016: RSA key generation produces at least 2048-bit keys.
func TestGenerateRSAKeyPair_KeySize(t *testing.T) {
	kp, err := generateRSAKeyPair()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pubBytes, err := base64.StdEncoding.DecodeString(kp.PublicKey)
	if err != nil {
		t.Fatalf("failed to decode public key base64: %v", err)
	}
	pub, err := x509.ParsePKIXPublicKey(pubBytes)
	if err != nil {
		t.Fatalf("failed to parse PKIX public key: %v", err)
	}

	type keySizer interface{ Size() int }
	sizer, ok := pub.(keySizer)
	if !ok {
		t.Fatal("public key does not implement Size()")
	}
	if sizer.Size()*8 < minRSAKeyBits {
		t.Errorf("key size %d bits is below minimum %d bits", sizer.Size()*8, minRSAKeyBits)
	}
}

// SC-017: Public key is single-line base64 without PEM delimiters.
func TestGenerateRSAKeyPair_PublicKeyFormat(t *testing.T) {
	kp, err := generateRSAKeyPair()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(kp.PublicKey, "-----") {
		t.Error("public key must not contain PEM delimiters")
	}
	if strings.Contains(kp.PublicKey, "\n") {
		t.Error("public key must be single-line (no newlines)")
	}
	if _, err := base64.StdEncoding.DecodeString(kp.PublicKey); err != nil {
		t.Errorf("public key is not valid base64: %v", err)
	}
}

// SC-018: Private key is PKCS#8 format with PEM delimiters.
func TestGenerateRSAKeyPair_PrivateKeyFormat(t *testing.T) {
	kp, err := generateRSAKeyPair()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	block, _ := pem.Decode([]byte(kp.PrivateKey))
	if block == nil {
		t.Fatal("private key is not valid PEM")
	}
	if block.Type != "PRIVATE KEY" {
		t.Errorf("expected PEM type 'PRIVATE KEY' (PKCS#8), got %q", block.Type)
	}
	if _, err := x509.ParsePKCS8PrivateKey(block.Bytes); err != nil {
		t.Errorf("private key is not valid PKCS#8: %v", err)
	}
}
