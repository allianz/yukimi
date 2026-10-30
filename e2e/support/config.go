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

// Package support holds what the e2e tests share: the operator configuration
// the tests must know, clients, start-up checks, the run namespace and its
// cleanup, and the Given/When/Then helpers.
package support

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/allianz/yukimi/internal/account/tenant"
	"github.com/allianz/yukimi/internal/config/backplane"
	"github.com/allianz/yukimi/internal/config/base"
)

const (
	// EnvConfigDir names the directory with the operator's base.yaml and
	// backplane.yaml. Locally that is _output/config, which `make dev`
	// generates and passes to the operator.
	EnvConfigDir = "E2E_CONFIG_DIR"

	// EnvMaxAccounts caps how many real accounts may exist at the same time
	// without being deleted.
	EnvMaxAccounts = "E2E_MAX_ACCOUNTS"

	// DefaultMaxAccounts is the default of EnvMaxAccounts; HardMaxAccounts is
	// the largest value that is accepted.
	DefaultMaxAccounts = 10
	HardMaxAccounts    = 10

	// snowflakeLabelLimit is Snowflake's DNS label limit on "<org>-<account>",
	// the same constant the account module checks names against.
	snowflakeLabelLimit = 63
)

// Config is what the tests must know about the environment under test. It is
// read with the operator's own loaders, so defaults match the running operator.
type Config struct {
	Dir         string
	Base        *base.Config
	Backplane   *backplane.Config
	MaxAccounts int
}

// LoadConfig reads the operator configuration directory and the e2e settings.
func LoadConfig() (*Config, error) {
	dir := os.Getenv(EnvConfigDir)
	if dir == "" {
		root, err := repoRoot()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(root, "_output", "config")
	}

	b, err := base.Load(dir)
	if err != nil {
		return nil, fmt.Errorf("reading base.yaml from %s (run `make dev` once to generate it, or set %s): %w", dir, EnvConfigDir, err)
	}
	bp, err := backplane.Load(dir)
	if err != nil {
		return nil, fmt.Errorf("reading backplane.yaml from %s: %w", dir, err)
	}

	maxAccounts := DefaultMaxAccounts
	if v := os.Getenv(EnvMaxAccounts); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > HardMaxAccounts {
			return nil, fmt.Errorf("%s must be an integer from 1 to %d, got %q", EnvMaxAccounts, HardMaxAccounts, v)
		}
		maxAccounts = n
	}

	return &Config{Dir: dir, Base: b, Backplane: bp, MaxAccounts: maxAccounts}, nil
}

// Org is the Snowflake organization the operator works in.
func (c *Config) Org() string { return c.Base.Snowflake.Org }

// OrgAdminAccount is the account the operator uses for org-level operations.
func (c *Config) OrgAdminAccount() string { return c.Base.Snowflake.OrgAdminAccount }

// AvailableRegion returns a region that is configured as available, so valid
// accounts can be created in it. The choice is deterministic.
func (c *Config) AvailableRegion() (string, error) {
	var names []string
	for name, r := range c.Backplane.Regions {
		if r.Available {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no region is marked available in backplane.yaml in %s", c.Dir)
	}
	sort.Strings(names)
	return names[0], nil
}

// UnknownRegion returns a region name with a valid format that is not in the
// configuration, so the CRD accepts it and only the controller rejects it.
func (c *Config) UnknownRegion() string {
	name := "aws-e2e-unknown-1"
	for i := 2; ; i++ {
		if _, ok := c.Backplane.Regions[name]; !ok {
			return name
		}
		name = fmt.Sprintf("aws-e2e-unknown-%d", i)
	}
}

// MaxAccountNameLength is the longest account name the operator accepts in the
// given namespace: Snowflake limits "<org>-<resolved name>" to 63 characters and
// the resolved name is the name plus a suffix derived from the namespace. The
// CRD itself allows more (55), so names between the two pass the API server and
// are rejected by the controller.
func (c *Config) MaxAccountNameLength(namespace string) int {
	suffixLen := len(tenant.ResolveName("a", namespace)) - 1
	return snowflakeLabelLimit - 1 - len(c.Org()) - suffixLen
}

// repoRoot finds the repository root by walking up to go.mod.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above the working directory; set %s", EnvConfigDir)
		}
		dir = parent
	}
}
