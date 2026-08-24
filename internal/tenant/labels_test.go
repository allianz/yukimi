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

package tenant

import (
	"testing"

	"github.com/allianz/yukimi/internal/errors"
)

func TestDepartment_Present(t *testing.T) {
	got, err := Department(map[string]string{"department": "Allianz_DE"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Allianz_DE" {
		t.Errorf("Department() = %q, want %q", got, "Allianz_DE")
	}
}

// SC-011: missing label returns a user error.
func TestDepartment_Missing(t *testing.T) {
	_, err := Department(map[string]string{})
	if err == nil || !errors.IsUserError(err) {
		t.Fatalf("expected user error, got %v", err)
	}
}

// SC-011: empty label returns a user error.
func TestDepartment_Empty(t *testing.T) {
	_, err := Department(map[string]string{"department": ""})
	if err == nil || !errors.IsUserError(err) {
		t.Fatalf("expected user error, got %v", err)
	}
}

func TestCostCenter_Present(t *testing.T) {
	got, err := CostCenter(map[string]string{"cost-center": "CC-123"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "CC-123" {
		t.Errorf("CostCenter() = %q, want %q", got, "CC-123")
	}
}

// SC-011: missing label returns a user error.
func TestCostCenter_Missing(t *testing.T) {
	_, err := CostCenter(map[string]string{})
	if err == nil || !errors.IsUserError(err) {
		t.Fatalf("expected user error, got %v", err)
	}
}

// SC-011: empty label returns a user error.
func TestCostCenter_Empty(t *testing.T) {
	_, err := CostCenter(map[string]string{"cost-center": ""})
	if err == nil || !errors.IsUserError(err) {
		t.Fatalf("expected user error, got %v", err)
	}
}

func TestCreditQuota_Present(t *testing.T) {
	got, err := CreditQuota(map[string]string{"credit-quota": "500"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 500 {
		t.Errorf("CreditQuota() = %d, want %d", got, 500)
	}
}

// SC-011: missing label returns a user error.
func TestCreditQuota_Missing(t *testing.T) {
	_, err := CreditQuota(map[string]string{})
	if err == nil || !errors.IsUserError(err) {
		t.Fatalf("expected user error, got %v", err)
	}
}

// SC-011: empty label returns a user error.
func TestCreditQuota_Empty(t *testing.T) {
	_, err := CreditQuota(map[string]string{"credit-quota": ""})
	if err == nil || !errors.IsUserError(err) {
		t.Fatalf("expected user error, got %v", err)
	}
}

// SC-012: non-integer label value returns a user error.
func TestCreditQuota_NonInteger(t *testing.T) {
	_, err := CreditQuota(map[string]string{"credit-quota": "not-a-number"})
	if err == nil || !errors.IsUserError(err) {
		t.Fatalf("expected user error, got %v", err)
	}
}

// SC-012: negative label value returns a user error.
func TestCreditQuota_Negative(t *testing.T) {
	_, err := CreditQuota(map[string]string{"credit-quota": "-5"})
	if err == nil || !errors.IsUserError(err) {
		t.Fatalf("expected user error, got %v", err)
	}
}

// SC-012: zero is a valid non-negative integer.
func TestCreditQuota_Zero(t *testing.T) {
	got, err := CreditQuota(map[string]string{"credit-quota": "0"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("CreditQuota() = %d, want %d", got, 0)
	}
}
