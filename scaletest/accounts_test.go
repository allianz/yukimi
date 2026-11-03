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
	"reflect"
	"testing"
)

func TestAccountName(t *testing.T) {
	for n, want := range map[int]string{1: "scaletest-00001", 42: "scaletest-00042", 10000: "scaletest-10000"} {
		if got := accountName(n); got != want {
			t.Errorf("accountName(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestMissingNames(t *testing.T) {
	existing := map[string]bool{"scaletest-00001": true, "scaletest-00003": true, "scaletest-00099": true}

	got := missingNames(existing, 4)
	want := []string{"scaletest-00002", "scaletest-00004"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("missing = %v, want %v", got, want)
	}
	if got := missingNames(existing, 1); len(got) != 0 {
		t.Fatalf("nothing is missing up to 1, got %v", got)
	}
}

func TestBatches(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e"}
	got := batches(names, 2)
	want := [][]string{{"a", "b"}, {"c", "d"}, {"e"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("batches = %v, want %v", got, want)
	}
	if got := batches(nil, 3); len(got) != 0 {
		t.Fatalf("no names, no batches, got %v", got)
	}
}

func TestNewAccountIsInTheScaletestNamespace(t *testing.T) {
	a := newAccount("scaletest-00001", "aws-eu-central-1")
	if a.Namespace != Namespace || a.Spec.Region != "aws-eu-central-1" {
		t.Fatalf("account = %s/%s in %s", a.Namespace, a.Name, a.Spec.Region)
	}
	if _, ok := a.Spec.IdentityIntegration.RoleBindings["ACCOUNTADMIN"]; !ok {
		t.Fatal("the CRD requires an ACCOUNTADMIN role binding")
	}
}
