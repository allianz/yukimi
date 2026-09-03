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

import xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"

// Custom condition types this package defines, plus the static table
// deciding which of them forces the resource's aggregate Ready to False. A
// module attaches its own condition to its Outcome; 020 collects and renders
// them, applying this table when aggregating Ready.
const (
	TypeQuotaAvailable xpv1.ConditionType = "QuotaAvailable" // design.md 3.10
	TypeIdentitySynced xpv1.ConditionType = "IdentitySynced" // design.md 4.3
)

// GatesReady maps a module-owned condition type to whether that condition
// being non-True forces the resource's aggregate Ready to False.
var GatesReady = map[xpv1.ConditionType]bool{
	TypeIdentitySynced: true,  // §4.3 — nobody can administer the account until ACCOUNTADMIN is imported
	TypeQuotaAvailable: false, // §3.10 — the account is intact; warehouses are merely suspended
}
