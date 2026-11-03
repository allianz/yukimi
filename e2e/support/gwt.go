//go:build e2e

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

package support

import "testing"

// Given, When and Then are thin wrappers around t.Run, so the test output reads
// as the scenario: "Given ...", "When ...", "Then ...". Each step shares state
// with the others through variables of the enclosing test. A failed step stops
// the scenario, because the steps after it would only report follow-up noise.

// Given sets up the starting point of a scenario.
func Given(t *testing.T, description string, step func(t *testing.T)) {
	t.Helper()
	run(t, "Given "+description, step)
}

// When performs the action under test.
func When(t *testing.T, description string, step func(t *testing.T)) {
	t.Helper()
	run(t, "When "+description, step)
}

// Then asserts the outcome.
func Then(t *testing.T, description string, step func(t *testing.T)) {
	t.Helper()
	run(t, "Then "+description, step)
}

func run(t *testing.T, name string, step func(t *testing.T)) {
	t.Helper()
	if !t.Run(name, step) {
		t.FailNow()
	}
}
