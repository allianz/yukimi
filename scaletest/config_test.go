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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validBaseYAML = `snowflake:
  org: testorg
  orgAdminAccount: platform
  orgAdminAccountLocator: xy12345
  orgAdminAccountRegion: aws-eu-central-1
aws:
  region: eu-central-1
`

// configDir returns a directory with the given files, so LoadConfig can be tested
// without the configuration `make dev` generates.
func configDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runEnv sets the variables of a valid run for the duration of the test.
func runEnv(t *testing.T, dir string) {
	t.Helper()
	t.Setenv(EnvAccounts, "10")
	t.Setenv(EnvConfirm, "10")
	t.Setenv(EnvConfigDir, dir)
}

func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestParseSettings(t *testing.T) {
	tests := []struct {
		name    string
		vars    map[string]string
		want    Settings
		wantErr string
	}{
		{
			name: "defaults",
			vars: map[string]string{EnvAccounts: "10", EnvConfirm: "10"},
			want: Settings{Accounts: 10, Batch: defaultBatch, ReadyTimeout: defaultReadyTimeout, ErrorGrace: defaultErrorGrace, StartTimeout: defaultStartTimeout, DegradeGrace: defaultDegradeGrace},
		},
		{
			name: "all set",
			vars: map[string]string{EnvAccounts: "200", EnvConfirm: "200", EnvBatch: "25", EnvBatchInterval: "30s", EnvReadyTimeout: "1h", EnvErrorGrace: "90s", EnvStartTimeout: "3m", EnvDegradeGrace: "40m", EnvAllowContext: "other"},
			want: Settings{Accounts: 200, Batch: 25, BatchInterval: 30 * time.Second, ReadyTimeout: time.Hour, ErrorGrace: 90 * time.Second, StartTimeout: 3 * time.Minute, DegradeGrace: 40 * time.Minute, AllowContext: "other"},
		},
		{
			name: "the maximum",
			vars: map[string]string{EnvAccounts: "10000", EnvConfirm: "10000"},
			want: Settings{Accounts: 10000, Batch: defaultBatch, ReadyTimeout: defaultReadyTimeout, ErrorGrace: defaultErrorGrace, StartTimeout: defaultStartTimeout, DegradeGrace: defaultDegradeGrace},
		},
		{name: "accounts missing", vars: map[string]string{}, wantErr: EnvAccounts + " is required"},
		{name: "not confirmed", vars: map[string]string{EnvAccounts: "10"}, wantErr: EnvConfirm + " must repeat"},
		{name: "wrong confirmation", vars: map[string]string{EnvAccounts: "10", EnvConfirm: "100"}, wantErr: EnvConfirm + " must repeat"},
		{name: "above the end", vars: map[string]string{EnvAccounts: "10001", EnvConfirm: "10001"}, wantErr: "at most 10000"},
		{name: "not a number", vars: map[string]string{EnvAccounts: "ten", EnvConfirm: "ten"}, wantErr: "integer of at least 1"},
		{name: "zero accounts", vars: map[string]string{EnvAccounts: "0", EnvConfirm: "0"}, wantErr: "integer of at least 1"},
		{name: "zero batch", vars: map[string]string{EnvAccounts: "10", EnvConfirm: "10", EnvBatch: "0"}, wantErr: EnvBatch},
		{name: "bad interval", vars: map[string]string{EnvAccounts: "10", EnvConfirm: "10", EnvBatchInterval: "soon"}, wantErr: EnvBatchInterval},
		{name: "zero ready timeout", vars: map[string]string{EnvAccounts: "10", EnvConfirm: "10", EnvReadyTimeout: "0s"}, wantErr: EnvReadyTimeout},
		{name: "zero degrade grace", vars: map[string]string{EnvAccounts: "10", EnvConfirm: "10", EnvDegradeGrace: "0s"}, wantErr: EnvDegradeGrace},
		{name: "zero start timeout", vars: map[string]string{EnvAccounts: "10", EnvConfirm: "10", EnvStartTimeout: "0s"}, wantErr: EnvStartTimeout},
		{name: "bad error grace", vars: map[string]string{EnvAccounts: "10", EnvConfirm: "10", EnvErrorGrace: "later"}, wantErr: EnvErrorGrace},
		{name: "negative interval", vars: map[string]string{EnvAccounts: "10", EnvConfirm: "10", EnvBatchInterval: "-1s"}, wantErr: EnvBatchInterval},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSettings(env(tt.vars))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("settings = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	t.Run("reads the settings and the operator configuration", func(t *testing.T) {
		dir := configDir(t, map[string]string{"base.yaml": validBaseYAML, "backplane.yaml": "regions: {}\n"})
		runEnv(t, dir)

		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Dir != dir || cfg.Accounts != 10 || cfg.Batch != defaultBatch {
			t.Fatalf("config = dir %q, settings %+v", cfg.Dir, cfg.Settings)
		}
		if cfg.Org() != "testorg" || cfg.OrgAdminAccount() != "platform" {
			t.Fatalf("org = %q, org admin account = %q", cfg.Org(), cfg.OrgAdminAccount())
		}
	})

	t.Run("the delete run needs no target number", func(t *testing.T) {
		dir := configDir(t, map[string]string{"base.yaml": validBaseYAML, "backplane.yaml": "regions: {}\n"})
		t.Setenv(EnvConfigDir, dir)
		t.Setenv(EnvAccounts, "")
		t.Setenv(EnvConfirmDelete, "7")

		cfg, err := LoadDeleteConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.ConfirmDelete != "7" || cfg.Org() != "testorg" {
			t.Fatalf("config = %+v", cfg.Settings)
		}
	})

	t.Run("the delete run reports invalid settings", func(t *testing.T) {
		t.Setenv(EnvBatch, "0")
		if _, err := LoadDeleteConfig(); err == nil || !strings.Contains(err.Error(), EnvBatch) {
			t.Fatalf("error = %v, want one about %s", err, EnvBatch)
		}
	})

	t.Run("invalid settings", func(t *testing.T) {
		dir := configDir(t, map[string]string{"base.yaml": validBaseYAML, "backplane.yaml": "regions: {}\n"})
		runEnv(t, dir)
		t.Setenv(EnvConfirm, "")

		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), EnvConfirm) {
			t.Fatalf("error = %v, want one about %s", err, EnvConfirm)
		}
	})

	t.Run("missing base.yaml", func(t *testing.T) {
		dir := configDir(t, map[string]string{"backplane.yaml": "regions: {}\n"})
		runEnv(t, dir)

		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "reading base.yaml") {
			t.Fatalf("error = %v, want one about reading base.yaml", err)
		}
	})

	t.Run("missing backplane.yaml", func(t *testing.T) {
		dir := configDir(t, map[string]string{"base.yaml": validBaseYAML})
		runEnv(t, dir)

		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "reading backplane.yaml") {
			t.Fatalf("error = %v, want one about reading backplane.yaml", err)
		}
	})

	t.Run("picks the first available region", func(t *testing.T) {
		backplane := "regions:\n  aws-eu-west-1:\n    available: false\n  aws-eu-central-1:\n    available: true\n"
		dir := configDir(t, map[string]string{"base.yaml": validBaseYAML, "backplane.yaml": backplane})
		runEnv(t, dir)
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if got, err := cfg.AvailableRegion(); err != nil || got != "aws-eu-central-1" {
			t.Fatalf("region = %q, %v", got, err)
		}
	})

	t.Run("no available region", func(t *testing.T) {
		dir := configDir(t, map[string]string{"base.yaml": validBaseYAML, "backplane.yaml": "regions: {}\n"})
		runEnv(t, dir)
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cfg.AvailableRegion(); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("the default directory is _output/config of the repository", func(t *testing.T) {
		runEnv(t, "")
		_, err := LoadConfig()
		// The directory exists only after `make dev`; either way the message names it.
		if err != nil && !strings.Contains(err.Error(), filepath.Join("_output", "config")) {
			t.Fatalf("error = %v, want one naming _output/config", err)
		}
	})
}

func TestRepoRoot(t *testing.T) {
	t.Run("finds the directory with go.mod", func(t *testing.T) {
		root, err := repoRoot()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
			t.Fatalf("no go.mod in %s: %v", root, err)
		}
	})

	t.Run("fails outside a repository", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if _, err := repoRoot(); err == nil || !strings.Contains(err.Error(), EnvConfigDir) {
			t.Fatalf("error = %v, want one that names %s", err, EnvConfigDir)
		}
	})
}

func TestParseDeleteSettings(t *testing.T) {
	got, err := ParseDeleteSettings(env(map[string]string{EnvConfirmDelete: "50", EnvBatch: "20"}))
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{Batch: 20, ErrorGrace: defaultErrorGrace, DeleteTimeout: defaultDeleteTimeout, ConfirmDelete: "50"}
	if got != want {
		t.Fatalf("settings = %+v, want %+v", got, want)
	}
	// No target number is needed, and an unconfirmed value is the delete's own business.
	if _, err := ParseDeleteSettings(env(nil)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := ParseDeleteSettings(env(map[string]string{EnvBatch: "0"})); err == nil {
		t.Fatal("expected an error for a zero batch")
	}
}

func TestParseDeleteSettingsReadsTheDeleteTimeout(t *testing.T) {
	got, err := ParseDeleteSettings(env(map[string]string{EnvDeleteTimeout: "2h"}))
	if err != nil || got.DeleteTimeout != 2*time.Hour {
		t.Fatalf("settings = %+v, err = %v", got, err)
	}
	if _, err := ParseDeleteSettings(env(map[string]string{EnvDeleteTimeout: "0s"})); err == nil {
		t.Fatal("expected an error for a zero delete timeout")
	}
	// The settings of a create run are not the business of the clean-up, and the other way round.
	if _, err := ParseDeleteSettings(env(map[string]string{EnvReadyTimeout: "soon"})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := ParseSettings(env(map[string]string{EnvAccounts: "1", EnvConfirm: "1", EnvDeleteTimeout: "soon"})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
