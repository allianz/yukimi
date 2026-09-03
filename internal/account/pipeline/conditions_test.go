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

package pipeline

import (
	"testing"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
)

func TestConditionTypes(t *testing.T) {
	if string(TypeQuotaAvailable) != "QuotaAvailable" {
		t.Errorf("TypeQuotaAvailable = %q, want %q", TypeQuotaAvailable, "QuotaAvailable")
	}
	if string(TypeIdentitySynced) != "IdentitySynced" {
		t.Errorf("TypeIdentitySynced = %q, want %q", TypeIdentitySynced, "IdentitySynced")
	}
}

func TestGatesReady(t *testing.T) {
	if !GatesReady[TypeIdentitySynced] {
		t.Error("GatesReady[TypeIdentitySynced] = false, want true")
	}
	if GatesReady[TypeQuotaAvailable] {
		t.Error("GatesReady[TypeQuotaAvailable] = true, want false")
	}
	if got := GatesReady[xpv1.ConditionType("SomethingElse")]; got {
		t.Error("GatesReady map-miss should default to false")
	}
}
