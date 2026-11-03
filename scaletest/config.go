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

// Package scaletest creates SnowflakeAccounts in growing steps, up to 10,000,
// to find where the system reaches its limits. A run never removes anything; only
// the explicit clean-up at the end does. It runs against the local `make dev` setup.
package scaletest

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/allianz/yukimi/internal/config/backplane"
	"github.com/allianz/yukimi/internal/config/base"
)

const (
	// Namespace holds all accounts of the scale test. Nothing else may clean it up.
	Namespace = "scaletest"

	// EnvConfigDir names the directory with the operator's base.yaml and
	// backplane.yaml. Locally that is _output/config, which `make dev` generates.
	EnvConfigDir = "SCALETEST_CONFIG_DIR"
	// EnvAccounts is the total number of accounts that should exist after the run.
	EnvAccounts = "SCALETEST_ACCOUNTS"
	// EnvConfirm must repeat EnvAccounts, so a stray value cannot start a run.
	EnvConfirm = "SCALETEST_CONFIRM"
	// EnvBatch is how many accounts are applied at once.
	EnvBatch = "SCALETEST_BATCH"
	// EnvBatchInterval is the pause between two batches.
	EnvBatchInterval = "SCALETEST_BATCH_INTERVAL"
	// EnvReadyTimeout is how long an account may take to become ready.
	EnvReadyTimeout = "SCALETEST_READY_TIMEOUT"
	// EnvErrorGrace is how long an account may report an error before the run
	// stops. The operator retries on its own, so a short error is not a failure.
	EnvErrorGrace = "SCALETEST_ERROR_GRACE"
	// EnvDegradeGrace is how long an account that was ready may stay not ready
	// before the run stops. It is longer than the error grace: with thousands of
	// accounts the operator needs minutes to come back to one of them.
	EnvDegradeGrace = "SCALETEST_DEGRADE_GRACE"
	// EnvStartTimeout is how long an account may stay without any condition, which
	// means the operator does not react to it.
	EnvStartTimeout = "SCALETEST_START_TIMEOUT"
	// EnvAllowContext names a kube context other than the one of `make dev` that the
	// run may use. Without it the run refuses to touch any other cluster.
	EnvAllowContext = "SCALETEST_ALLOW_CONTEXT"
	// EnvConfirmDelete must hold the number of accounts in the namespace for
	// `make scaletest-delete` to delete them. The refusal names the number.
	EnvConfirmDelete = "SCALETEST_CONFIRM_DELETE"
	// EnvDeleteTimeout is how long an account may take to be deleted.
	EnvDeleteTimeout = "SCALETEST_DELETE_TIMEOUT"
	// EnvAction tells the tests what the run is for: "create" or "delete". The make
	// targets set it, so no test that creates or deletes accounts runs by accident.
	EnvAction = "SCALETEST_ACTION"

	// MaxAccounts is the most accounts the scale test creates.
	MaxAccounts          = 10000
	defaultBatch         = 10
	defaultReadyTimeout  = 30 * time.Minute
	defaultErrorGrace    = 5 * time.Minute
	defaultStartTimeout  = 5 * time.Minute
	defaultDegradeGrace  = 15 * time.Minute
	defaultDeleteTimeout = 30 * time.Minute
)

// Settings are the parameters of a run.
type Settings struct {
	Accounts      int
	Batch         int
	BatchInterval time.Duration
	ReadyTimeout  time.Duration
	ErrorGrace    time.Duration
	StartTimeout  time.Duration
	DegradeGrace  time.Duration
	DeleteTimeout time.Duration

	// AllowContext is a kube context besides DevContext that the run may use.
	AllowContext string

	// ConfirmDelete is the raw value of EnvConfirmDelete; the number it must
	// match is known only once the accounts are listed.
	ConfirmDelete string
}

// Config is what a run needs to know: its settings and the configuration of the
// operator under test, read with the operator's own loaders so defaults match.
type Config struct {
	Settings
	Dir       string
	Base      *base.Config
	Backplane *backplane.Config
}

// Org is the Snowflake organization the operator works in.
func (c *Config) Org() string { return c.Base.Snowflake.Org }

// OrgAdminAccount is the account the operator uses for org-level operations.
func (c *Config) OrgAdminAccount() string { return c.Base.Snowflake.OrgAdminAccount }

// AvailableRegion (as in e2e/support) returns a region that is configured as
// available, so the accounts can be created in it. The choice is deterministic.
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
	slices.Sort(names)
	return names[0], nil
}

// LoadConfig reads the settings of a run that creates accounts from the
// environment and the operator configuration directory.
func LoadConfig() (*Config, error) {
	s, err := ParseSettings(os.Getenv)
	if err != nil {
		return nil, err
	}
	return loadOperatorConfig(s)
}

// LoadDeleteConfig is LoadConfig for the run that deletes all accounts. It needs
// no target number.
func LoadDeleteConfig() (*Config, error) {
	s, err := ParseDeleteSettings(os.Getenv)
	if err != nil {
		return nil, err
	}
	return loadOperatorConfig(s)
}

func loadOperatorConfig(s Settings) (*Config, error) {
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
	return &Config{Settings: s, Dir: dir, Base: b, Backplane: bp}, nil
}

// ParseSettings reads and validates the run settings. The run refuses to start
// unless EnvConfirm repeats EnvAccounts.
func ParseSettings(getenv func(string) string) (Settings, error) {
	var s Settings
	accounts, err := positiveInt(getenv, EnvAccounts, 0)
	if err != nil {
		return s, err
	}
	if accounts == 0 {
		return s, fmt.Errorf("%s is required: the total number of accounts that should exist after the run", EnvAccounts)
	}
	if accounts > MaxAccounts {
		return s, fmt.Errorf("%s must be at most %d, got %d", EnvAccounts, MaxAccounts, accounts)
	}
	if got := getenv(EnvConfirm); got != strconv.Itoa(accounts) {
		return s, fmt.Errorf("%s must repeat %s (%d) to confirm that this many real accounts are created, got %q", EnvConfirm, EnvAccounts, accounts, got)
	}
	s.Accounts = accounts

	if s.ReadyTimeout, err = duration(getenv, EnvReadyTimeout, defaultReadyTimeout, false); err != nil {
		return s, err
	}
	if s.StartTimeout, err = duration(getenv, EnvStartTimeout, defaultStartTimeout, false); err != nil {
		return s, err
	}
	if s.DegradeGrace, err = duration(getenv, EnvDegradeGrace, defaultDegradeGrace, false); err != nil {
		return s, err
	}
	return withCommon(getenv, s)
}

// ParseDeleteSettings reads the settings of the run that deletes all accounts.
func ParseDeleteSettings(getenv func(string) string) (Settings, error) {
	s := Settings{ConfirmDelete: getenv(EnvConfirmDelete)}
	var err error
	if s.DeleteTimeout, err = duration(getenv, EnvDeleteTimeout, defaultDeleteTimeout, false); err != nil {
		return s, err
	}
	return withCommon(getenv, s)
}

// withCommon reads the settings that both kinds of run share: the batches, the error
// grace and the context.
func withCommon(getenv func(string) string, s Settings) (Settings, error) {
	s.AllowContext = getenv(EnvAllowContext)
	var err error
	if s.Batch, err = positiveInt(getenv, EnvBatch, defaultBatch); err != nil {
		return s, err
	}
	if s.BatchInterval, err = duration(getenv, EnvBatchInterval, 0, true); err != nil {
		return s, err
	}
	if s.ErrorGrace, err = duration(getenv, EnvErrorGrace, defaultErrorGrace, false); err != nil {
		return s, err
	}
	return s, nil
}

// duration reads a Go duration such as 30s, or returns def when it is unset. A
// zero is accepted only if allowZero is set.
func duration(getenv func(string) string, name string, def time.Duration, allowZero bool) (time.Duration, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 || (d == 0 && !allowZero) {
		kind := "positive"
		if allowZero {
			kind = "non-negative"
		}
		return 0, fmt.Errorf("%s must be a %s duration such as 30s, got %q", name, kind, v)
	}
	return d, nil
}

// positiveInt reads an integer of at least 1, or returns def when it is unset.
func positiveInt(getenv func(string) string, name string, def int) (int, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s must be an integer of at least 1, got %q", name, v)
	}
	return n, nil
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
