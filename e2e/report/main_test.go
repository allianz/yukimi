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

package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestHumanize(t *testing.T) {
	cases := map[string]string{
		"NameBoundariesAreRejected":                  "Name boundaries are rejected",
		"NonASCIINameIsRejected":                     "Non ASCII name is rejected",
		"NameOverTheCRDLimitIsRejected":              "Name over the CRD limit is rejected",
		"AValidAccountIsAccepted":                    "A valid account is accepted",
		"AnAccountThatNeverGotALocatorCanBeDeleted":  "An account that never got a locator can be deleted",
		"RoleBindingsWithoutAccountAdminAreRejected": "Role bindings without account admin are rejected",
	}
	for in, want := range cases {
		if got := humanize(in); got != want {
			t.Errorf("humanize(%q) = %q, want %q", in, got, want)
		}
	}
}

// A stream with one passing test that has table cases, one passing test with
// steps, and one failing test whose Then step failed.
const sampleStream = `{"Action":"output","Package":"p","Output":"e2e target: kube context \"kind-x\"\n"}
{"Action":"run","Package":"p","Test":"TestB3_NameBoundariesAreRejected"}
{"Action":"pass","Package":"p","Test":"TestB3_NameBoundariesAreRejected/1abc","Elapsed":0}
{"Action":"pass","Package":"p","Test":"TestB3_NameBoundariesAreRejected/Upper","Elapsed":0}
{"Action":"pass","Package":"p","Test":"TestB3_NameBoundariesAreRejected","Elapsed":0.01}
{"Action":"pass","Package":"p","Test":"TestB1_NonASCIINameIsRejected/Given_an_account","Elapsed":0}
{"Action":"pass","Package":"p","Test":"TestB1_NonASCIINameIsRejected","Elapsed":0}
{"Action":"output","Package":"p","Test":"TestB2_NameOverTheCRDLimitIsRejected/Then_the_API_server_rejects_it","Output":"    x_test.go:9: the API server accepted the account\n"}
{"Action":"fail","Package":"p","Test":"TestB2_NameOverTheCRDLimitIsRejected/Then_the_API_server_rejects_it","Elapsed":0}
{"Action":"fail","Package":"p","Test":"TestB2_NameOverTheCRDLimitIsRejected","Elapsed":0}
{"Action":"fail","Package":"p","Elapsed":1}
`

func TestSummary(t *testing.T) {
	rep, err := process(strings.NewReader(sampleStream), io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	failed := render(&out, rep, "run.log")
	text := out.String()

	if !failed {
		t.Error("a failing test must make the report fail")
	}
	for _, want := range []string{
		"e2e target:",
		"Name boundaries are rejected",
		"2 cases",
		"FAILED B2",
		"step: Then the API server rejects it",
		"the API server accepted the account",
		"3 cases ran: 2 passed, 1 failed, 0 skipped.",
		"Full output: run.log",
		"RESULT: FAILED",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the summary does not contain %q:\n%s", want, text)
		}
	}
	// Case order follows the case number, not the order of the stream.
	if strings.Index(text, "B1 ") > strings.Index(text, "B3 ") {
		t.Errorf("B1 should be listed before B3:\n%s", text)
	}
}

// renderStream runs a stream through the report and returns the summary and
// whether the run failed.
func renderStream(t *testing.T, stream string) (string, bool) {
	t.Helper()
	rep, err := process(strings.NewReader(stream), io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	failed := render(&out, rep, "")
	return out.String(), failed
}

func TestBuildFailureShowsTheCompilerOutput(t *testing.T) {
	out, failed := renderStream(t, `{"ImportPath":"p [p.test]","Action":"build-output","Output":"e2e/x_test.go:3:13: cannot use \"s\" as int value\n"}
{"ImportPath":"p [p.test]","Action":"build-fail"}
{"Action":"output","Package":"p","Output":"FAIL\tp [build failed]\n"}
{"Action":"fail","Package":"p","Elapsed":0}
`)
	if !failed || !strings.Contains(out, "cannot use") {
		t.Errorf("failed = %v, want true with the compiler message in:\n%s", failed, out)
	}
}

func TestARunWithoutAnyTestFails(t *testing.T) {
	out, failed := renderStream(t, "")
	if !failed || !strings.Contains(out, "no test ran") {
		t.Errorf("failed = %v, want true with \"no test ran\" in:\n%s", failed, out)
	}
}

func TestATestThatNeverFinishedFails(t *testing.T) {
	out, failed := renderStream(t, `{"Action":"run","Package":"p","Test":"TestA1_Slow"}
{"Action":"output","Package":"p","Test":"TestA1_Slow","Output":"=== RUN   TestA1_Slow\n"}
`)
	if !failed || !strings.Contains(out, "did not finish") {
		t.Errorf("failed = %v, want true with \"did not finish\" in:\n%s", failed, out)
	}
}
