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

	"github.com/allianz/yukimi/internal/errors"
)

const (
	departmentLabel  = "department"
	costCenterLabel  = "cost-center"
	creditQuotaLabel = "credit-quota"
)

func readLabel(labels map[string]string, key string) (string, error) {
	value, ok := labels[key]
	if !ok || value == "" {
		return "", errors.NewUserError(fmt.Sprintf(
			"namespace missing required label '%s'; contact platform ops", key))
	}
	return value, nil
}

// Department returns the ops-set "department" namespace label (design.md
// chapter 2), consumed by Guardrails target matching (008).
//
// Returns: User error if the label is missing or empty — the tenant can't
// fix this by editing their CRD, but a readable message ("namespace missing
// required label 'department'; contact platform ops") surfaced directly on
// the resource is more useful to them than a system error's incident ID
// (see Error Classification).
func Department(labels map[string]string) (string, error) {
	return readLabel(labels, departmentLabel)
}

// CostCenter returns the ops-set "cost-center" namespace label (design.md
// chapter 2). No spec currently consumes the returned value; this reader
// exists so that whichever spec adds the first consumer doesn't also need to
// touch this package.
//
// Returns: User error if the label is missing or empty, for the same
// readability reason as Department.
func CostCenter(labels map[string]string) (string, error) {
	return readLabel(labels, costCenterLabel)
}

// CreditQuota returns the ops-set "credit-quota" namespace label (design.md
// chapter 2 and 3.10), parsed to an int.
//
// Returns: User error if the label is missing, empty, or not a valid
// non-negative integer — same readability reasoning as Department.
func CreditQuota(labels map[string]string) (int, error) {
	value, err := readLabel(labels, creditQuotaLabel)
	if err != nil {
		return 0, err
	}
	quota, err := strconv.Atoi(value)
	if err != nil || quota < 0 {
		return 0, errors.NewUserError(fmt.Sprintf(
			"namespace label '%s' must be a non-negative integer, got %q; contact platform ops",
			creditQuotaLabel, value))
	}
	return quota, nil
}
