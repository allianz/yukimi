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

// Command clean removes what earlier e2e runs left behind, without asking the
// operator: the Snowflake accounts are dropped, their AWS secrets are deleted at
// once, and the Kubernetes objects and run namespaces are removed even when a
// finalizer is stuck. It skips the deletion permit gate, so run it only against a
// test organization.
//
//	go run -tags e2e ./e2e/clean [-dry-run] [-older-than 1h]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/allianz/yukimi/e2e/support"
)

func main() { os.Exit(run()) }

func run() int {
	dryRun := flag.Bool("dry-run", false, "only print what would be done")
	olderThan := flag.Duration("older-than", 0, "spare run namespaces younger than this, for example the one of a run that is still going")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if _, err := support.LoadDotEnv(); err != nil {
		return fail("configuration", err)
	}
	cfg, err := support.LoadConfig()
	if err != nil {
		return fail("configuration", err)
	}
	e, err := support.Connect(ctx, cfg)
	if err != nil {
		return fail("connection", err)
	}
	defer e.Close()
	if _, err := e.Check(ctx); err != nil {
		return fail("start-up check", err)
	}

	mode := "force clean"
	if *dryRun {
		mode = "dry run"
	}
	fmt.Printf("e2e clean (%s): kube context %q, organization %q\n", mode, support.KubeContext(), cfg.Org())

	lines, err := e.ForceClean(ctx, support.ForceCleanOptions{DryRun: *dryRun, OlderThan: *olderThan})
	for _, l := range lines {
		fmt.Println(l)
	}
	if err != nil {
		return fail("clean", err)
	}
	return 0
}

func fail(what string, err error) int {
	fmt.Fprintf(os.Stderr, "e2e clean: %s: %v\n", what, err)
	return 1
}
