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
	"fmt"
	"strconv"
)

const (
	departmentLabel  = "department"
	costCenterLabel  = "cost-center"
	creditQuotaLabel = "credit-quota"
	alphaTesterLabel = "alpha-tester"
)

func readLabel(labels map[string]string, key string) (string, error) {
	value, ok := labels[key]
	if !ok || value == "" {
		return "", fmt.Errorf(
			"namespace missing required label '%s'; contact platform ops", key)
	}
	return value, nil
}

// Department returns the ops-set "department" namespace label (design.md
// chapter 2), consumed by Guardrails target matching (008).
//
// Returns: System error if the label is missing or empty — only ops can fix
// it (see Error Classification).
func Department(labels map[string]string) (string, error) {
	return readLabel(labels, departmentLabel)
}

// CostCenter returns the ops-set "cost-center" namespace label (design.md
// chapter 2). No spec currently consumes the returned value; this reader
// exists so that whichever spec adds the first consumer doesn't also need to
// touch this package.
//
// Returns: System error if the label is missing or empty, as for Department.
func CostCenter(labels map[string]string) (string, error) {
	return readLabel(labels, costCenterLabel)
}

// CreditQuota returns the ops-set "credit-quota" namespace label (design.md
// chapter 2 and 3.10), parsed to an int.
//
// Returns: System error if the label is missing, empty, or not a valid
// non-negative integer.
func CreditQuota(labels map[string]string) (int, error) {
	value, err := readLabel(labels, creditQuotaLabel)
	if err != nil {
		return 0, err
	}
	quota, err := strconv.Atoi(value)
	if err != nil || quota < 0 {
		return 0, fmt.Errorf(
			"namespace label '%s' must be a non-negative integer, got %q; contact platform ops",
			creditQuotaLabel, value)
	}
	return quota, nil
}

// AlphaTester returns whether the namespace carries the ops-set "alpha-tester" label (design.md
// chapter 2), consumed by the account module (012) to bypass a region's Backplane Config (007)
// availability gate. Unlike Department/CostCenter/CreditQuota, this label is optional: most
// namespaces don't carry it, so a missing or empty value means "not an alpha tester" rather than an
// error.
//
// Returns: System error if the label is present but not a valid boolean.
func AlphaTester(labels map[string]string) (bool, error) {
	value, ok := labels[alphaTesterLabel]
	if !ok || value == "" {
		return false, nil
	}
	isAlphaTester, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf(
			"namespace label '%s' must be a boolean, got %q; contact platform ops",
			alphaTesterLabel, value)
	}
	return isAlphaTester, nil
}
