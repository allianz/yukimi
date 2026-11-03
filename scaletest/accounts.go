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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
)

// The labels of the namespace (as in e2e/support) use the key names the controller reads
// (internal/account/tenant/labels.go). Only alpha-tester is read today; the
// others are set so checks added later do not break the scale test. The credit
// quota covers MaxAccounts accounts that claim one credit each.
var namespaceLabels = map[string]string{
	"alpha-tester": "true",
	"department":   "scaletest",
	"cost-center":  "000000",
	"credit-quota": "10000",
}

// accountPrefix starts the name of every account of the scale test.
const accountPrefix = "scaletest"

// accountName is the fixed name of the n-th account of the scale test, counted
// from 1. The name stays the same in every run, so a run can continue where an
// earlier one stopped.
func accountName(n int) string {
	return fmt.Sprintf("%s-%05d", accountPrefix, n)
}

// rangeSet returns the names of the accounts 1..total.
func rangeSet(total int) map[string]bool {
	set := make(map[string]bool, total)
	for n := 1; n <= total; n++ {
		set[accountName(n)] = true
	}
	return set
}

// missingNames returns the names of the accounts among 1..total that are not in
// existing, in order. Accounts outside that range are not the scale test's business.
func missingNames(existing map[string]bool, total int) []string {
	var missing []string
	for n := 1; n <= total; n++ {
		if name := accountName(n); !existing[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

// batches splits names into groups of at most size.
func batches(names []string, size int) [][]string {
	var out [][]string
	for len(names) > 0 {
		n := min(size, len(names))
		out = append(out, names[:n])
		names = names[n:]
	}
	return out
}

// newAccount (as in e2e/support) returns a valid SnowflakeAccount of the scale test.
func newAccount(name, region string) *v1alpha1.SnowflakeAccount {
	return &v1alpha1.SnowflakeAccount{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: Namespace},
		Spec: v1alpha1.SnowflakeAccountSpec{
			Contact:     "scaletest@example.com",
			Region:      region,
			Environment: "dev",
			CreditQuota: 1,
			IdentityIntegration: v1alpha1.IdentityIntegration{
				Groups:       map[string][]string{"giam": {"SCALETEST_GROUP"}},
				RoleBindings: map[string]string{"ACCOUNTADMIN": "SCALETEST_GROUP"},
			},
		},
	}
}
