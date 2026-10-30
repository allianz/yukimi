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

// Command report turns the JSON output of `go test -json` into a short summary
// of the cases: one line per case with its result and, for a failure, the step that failed and
// what it said. The full test output is written to a file for debugging.
//
//	go test -tags e2e -json ./e2e/... | go run -tags e2e ./e2e/report -raw run.log
//
// It exits with status 1 if anything failed, so it can end a pipeline.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// event is one line of the `go test -json` stream.
type event struct {
	Action  string
	Package string
	// ImportPath is set instead of Package on the build events.
	ImportPath string
	Test       string
	Output     string
	Elapsed    float64
}

// failure is a failed step (or a failed test body) and what it printed.
type failure struct {
	step  string
	lines []string
}

// result is the outcome of one top-level test.
type result struct {
	name     string
	id       string // case number from the test name, for example "B3"; empty if the name has none
	status   string // pass, fail or skip
	elapsed  float64
	cases    int // subtests that are cases of a table, not steps
	failures []failure
}

// report collects everything the summary needs.
type report struct {
	results  map[string]*result
	pkgLines map[string][]string // output not attributed to a test, per package
	pkgFail  map[string]bool
	stray    []string // lines that are not JSON, for example compiler errors
	build    []string // compiler output reported by go test
	outputs  map[string][]string
}

var (
	testNamePattern = regexp.MustCompile(`^Test([A-Z][0-9]+)_(.+)$`)
)

func main() {
	rawPath := flag.String("raw", "", "write the full test output to this file")
	verbose := flag.Bool("v", false, "also print the full test output")
	flag.Parse()

	// Ctrl-C reaches the whole pipeline. The tests clean up after it; the report
	// must stay alive to print what they say.
	signal.Ignore(os.Interrupt)

	var raw io.Writer = io.Discard
	if *rawPath != "" {
		if err := os.MkdirAll(filepath.Dir(*rawPath), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "report:", err)
			os.Exit(2)
		}
		f, err := os.Create(*rawPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "report:", err)
			os.Exit(2)
		}
		defer f.Close()
		raw = f
	}
	var echo io.Writer = io.Discard
	if *verbose {
		echo = os.Stdout
	}

	rep, err := process(os.Stdin, raw, echo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "report:", err)
		os.Exit(2)
	}
	// The summary goes to the terminal and to the end of the full log, so the log
	// is complete on its own.
	fmt.Fprintln(raw, "\n=== e2e summary ===")
	failed := render(io.MultiWriter(os.Stdout, raw), rep, *rawPath)
	if failed {
		os.Exit(1)
	}
}

// process reads the JSON stream. Every output line is also written to raw, in
// the form `go test -v` prints it, and to echo.
func process(in io.Reader, raw, echo io.Writer) (*report, error) {
	rep := &report{
		results:  map[string]*result{},
		pkgLines: map[string][]string{},
		pkgFail:  map[string]bool{},
		outputs:  map[string][]string{},
	}

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		var ev event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			// Not an event: build output of the go tool, for example.
			fmt.Fprintln(raw, line)
			fmt.Fprintln(echo, line)
			rep.stray = append(rep.stray, line)
			continue
		}
		if ev.Output != "" {
			_, _ = io.WriteString(raw, ev.Output)
			_, _ = io.WriteString(echo, ev.Output)
		}
		rep.add(ev)
	}
	return rep, scanner.Err()
}

func (r *report) add(ev event) {
	if ev.Action == "build-output" {
		if text := strings.TrimRight(ev.Output, "\n"); text != "" {
			r.build = append(r.build, text)
		}
		return
	}
	if ev.Test == "" {
		if text := strings.TrimRight(ev.Output, "\n"); text != "" {
			r.pkgLines[ev.Package] = append(r.pkgLines[ev.Package], text)
		}
		if ev.Action == "fail" {
			r.pkgFail[ev.Package] = true
		}
		return
	}

	parts := strings.Split(ev.Test, "/")
	top := parts[0]
	res := r.results[top]
	if res == nil {
		res = &result{name: top}
		if m := testNamePattern.FindStringSubmatch(top); m != nil {
			res.id = m[1]
		}
		r.results[top] = res
	}

	if text := strings.TrimRight(ev.Output, "\n"); text != "" {
		r.outputs[ev.Test] = append(r.outputs[ev.Test], text)
	}

	switch ev.Action {
	case "pass", "fail", "skip":
	default:
		return
	}

	depth := len(parts) - 1
	last := parts[len(parts)-1]
	switch {
	case depth == 0:
		res.status = ev.Action
		res.elapsed = ev.Elapsed
		if ev.Action == "fail" && len(res.failures) == 0 {
			res.failures = append(res.failures, failure{step: "(test body)", lines: meaningful(r.outputs[ev.Test])})
		}
	case !isStep(last):
		// A subtest that is not a step is a case of a table.
		if depth == 1 {
			res.cases++
		}
	case ev.Action == "fail":
		res.failures = append(res.failures, failure{
			step:  strings.ReplaceAll(strings.Join(parts[1:], " / "), "_", " "),
			lines: meaningful(r.outputs[ev.Test]),
		})
	}
}

func isStep(name string) bool {
	return strings.HasPrefix(name, "Given_") || strings.HasPrefix(name, "When_") || strings.HasPrefix(name, "Then_")
}

// meaningful drops the lines `go test` adds around a test result, which say
// nothing the summary does not already say.
func meaningful(lines []string) []string {
	var out []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "=== ") || strings.Contains(t, "--- PASS") || strings.Contains(t, "--- FAIL") || strings.Contains(t, "--- SKIP") {
			continue
		}
		out = append(out, t)
	}
	return out
}

// humanize turns a test name such as "NameOverTheCRDLimitIsRejected" into
// "Name over the CRD limit is rejected".
func humanize(s string) string {
	runes := []rune(s)
	var words []string
	start := 0
	for i := 1; i < len(runes); i++ {
		prev, cur := runes[i-1], runes[i]
		var next rune
		if i+1 < len(runes) {
			next = runes[i+1]
		}
		boundary := unicode.IsUpper(cur) && (unicode.IsLower(prev) || unicode.IsDigit(prev) ||
			(unicode.IsUpper(prev) && next != 0 && unicode.IsLower(next)))
		if boundary {
			words = append(words, string(runes[start:i]))
			start = i
		}
	}
	words = append(words, string(runes[start:]))
	for i, w := range words {
		allCaps := len(w) > 1 && strings.ToUpper(w) == w
		if !allCaps && i > 0 {
			words[i] = strings.ToLower(w)
		}
	}
	return strings.Join(words, " ")
}

// idLess orders case numbers by letter, then by number: A1, A2, A10, B0.
func idLess(a, b string) bool {
	if a[0] != b[0] {
		return a[0] < b[0]
	}
	na, _ := strconv.Atoi(a[1:])
	nb, _ := strconv.Atoi(b[1:])
	return na < nb
}

// render prints the summary and reports whether anything failed.
func render(w io.Writer, rep *report, rawPath string) (failed bool) {
	// The banner of TestMain comes first; its error messages are shown with the
	// failure below, not twice.
	var pkgs []string
	for pkg := range rep.pkgLines {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	for _, pkg := range pkgs {
		for _, l := range rep.pkgLines[pkg] {
			if strings.HasPrefix(l, "e2e target:") || strings.HasPrefix(l, "e2e sweep:") {
				fmt.Fprintln(w, l)
			}
		}
	}
	fmt.Fprintln(w)

	var withID, other []*result
	for _, res := range rep.results {
		if res.id != "" {
			withID = append(withID, res)
		} else {
			other = append(other, res)
		}
	}
	sort.Slice(withID, func(i, j int) bool { return idLess(withID[i].id, withID[j].id) })
	sort.Slice(other, func(i, j int) bool { return other[i].name < other[j].name })

	title := func(res *result) string {
		if m := testNamePattern.FindStringSubmatch(res.name); m != nil {
			return humanize(m[2])
		}
		return humanize(strings.TrimPrefix(res.name, "Test"))
	}
	detail := func(res *result) string {
		d := fmt.Sprintf("%.2fs", res.elapsed)
		if res.cases > 0 {
			d = fmt.Sprintf("%d cases, %s", res.cases, d)
		}
		return d
	}

	width := 0
	for _, res := range append(append([]*result{}, withID...), other...) {
		if n := len(title(res)); n > width {
			width = n
		}
	}

	counts := map[string]int{}
	tested := map[string]bool{}
	fmt.Fprintf(w, "%-5s %-6s %-*s  %s\n", "Case", "Result", width, "Test", "Detail")
	for _, res := range withID {
		counts[res.status]++
		tested[res.id] = true
		fmt.Fprintf(w, "%-5s %-6s %-*s  %s\n", res.id, strings.ToUpper(res.status), width, title(res), detail(res))
	}
	for _, res := range other {
		counts[res.status]++
		fmt.Fprintf(w, "%-5s %-6s %-*s  %s\n", "-", strings.ToUpper(res.status), width, title(res), detail(res))
	}
	fmt.Fprintln(w)

	// Failures, with the step that failed and what it said.
	for _, res := range append(append([]*result{}, withID...), other...) {
		if res.status == "" {
			// The stream ended while the test was running: go test was killed.
			failed = true
			fmt.Fprintf(w, "FAILED %s %s: the test did not finish; go test was probably killed (for example by its timeout)\n", res.id, title(res))
			continue
		}
		if res.status != "fail" {
			continue
		}
		failed = true
		fmt.Fprintf(w, "FAILED %s %s\n", res.id, title(res))
		for _, f := range res.failures {
			fmt.Fprintf(w, "  step: %s\n", f.step)
			for _, l := range f.lines {
				fmt.Fprintf(w, "    %s\n", l)
			}
		}
	}

	// A package that failed without any failed test: a build error, or the
	// start-up checks of TestMain stopped the run.
	for _, pkg := range pkgs {
		if !rep.pkgFail[pkg] {
			continue
		}
		hasFailedTest := false
		for _, res := range rep.results {
			if res.status == "fail" {
				hasFailedTest = true
			}
		}
		if hasFailedTest {
			failed = true
			continue
		}
		failed = true
		fmt.Fprintf(w, "FAILED: %s stopped before or outside of any test:\n", pkg)
		for _, l := range rep.pkgLines[pkg] {
			if l == "FAIL" || strings.HasPrefix(l, "FAIL\t") || strings.HasPrefix(l, "ok ") {
				continue
			}
			fmt.Fprintf(w, "  %s\n", l)
		}
	}
	if len(rep.build) > 0 && failed {
		fmt.Fprintln(w, "FAILED to build:")
		for _, l := range rep.build {
			fmt.Fprintf(w, "  %s\n", l)
		}
	}
	if len(rep.results) == 0 && !failed {
		// A run that ran no test at all is not a success: the filter matched
		// nothing, or go test produced nothing.
		failed = true
		fmt.Fprintln(w, "FAILED: no test ran")
	}
	if len(rep.stray) > 0 && failed {
		fmt.Fprintln(w, "FAILED to build or start:")
		for _, l := range rep.stray {
			fmt.Fprintf(w, "  %s\n", l)
		}
	}

	fmt.Fprintf(w, "%d cases ran: %d passed, %d failed, %d skipped.\n",
		len(tested), countCases(withID, "pass"), countCases(withID, "fail"), countCases(withID, "skip"))
	if rawPath != "" {
		fmt.Fprintf(w, "Full output: %s\n", rawPath)
	}
	if failed {
		fmt.Fprintln(w, "RESULT: FAILED")
	} else {
		fmt.Fprintln(w, "RESULT: ok")
	}
	return failed
}

func countCases(results []*result, status string) int {
	n := 0
	for _, r := range results {
		if r.status == status {
			n++
		}
	}
	return n
}
