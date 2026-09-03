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

package backplane

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/allianz/yukimi/internal/errors"
)

// newBackplaneDir writes content as backplane.yaml into a fresh temp directory
// and returns the directory path.
func newBackplaneDir(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "backplane.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return dir
}

const wellFormedFixture = `
globalParameters:
  PREVENT_UNLOAD_TO_INLINE_URL: "true"

regions:
  aws-eu-central-1:
    available: true
    inventory:
      - connection: agn
        type: AWSVPCEID
        vpceId: vpce-00006900000000001
        maxCidrs: ["172.16.0.0/12"]
      - connection: dbt-cloud
        type: AWSVPCEID
        vpceId: vpce-00006900000000004
      - connection: public
        type: IPV4
        maxCidrs: ["0.0.0.0/0"]
    regionalParameters:
      ENABLE_INTERNAL_STAGES_PRIVATELINK: "true"
    regionalAllowlist:
      - connection: agn
      - connection: public
        allowedIPs: ["203.0.113.0/24"]
`

// SC-001: Load returns a populated *Config for a well-formed backplane.yaml.
func TestLoad_WellFormed(t *testing.T) {
	cfg, err := Load(newBackplaneDir(t, wellFormedFixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil Config")
	}
	if got := cfg.GlobalParameters["PREVENT_UNLOAD_TO_INLINE_URL"]; got != "true" {
		t.Errorf("GlobalParameters[PREVENT_UNLOAD_TO_INLINE_URL] = %q, want %q", got, "true")
	}

	region, ok := cfg.Regions["aws-eu-central-1"]
	if !ok {
		t.Fatal("expected region aws-eu-central-1 to be present")
	}
	if !region.Available {
		t.Error("Available = false, want true")
	}
	if len(region.Inventory) != 3 {
		t.Fatalf("len(Inventory) = %d, want 3", len(region.Inventory))
	}
	if got := region.RegionalParameters["ENABLE_INTERNAL_STAGES_PRIVATELINK"]; got != "true" {
		t.Errorf("RegionalParameters[ENABLE_INTERNAL_STAGES_PRIVATELINK] = %q, want %q", got, "true")
	}
	if len(region.RegionalAllowlist) != 2 {
		t.Fatalf("len(RegionalAllowlist) = %d, want 2", len(region.RegionalAllowlist))
	}

	agn, ok := region.Connection("agn")
	if !ok {
		t.Fatal("expected connection agn to be present")
	}
	if agn.Type != "AWSVPCEID" || agn.VpceID != "vpce-00006900000000001" {
		t.Errorf("agn = %+v, want Type=AWSVPCEID VpceID=vpce-00006900000000001", agn)
	}
	if len(agn.MaxCidrs) != 1 || agn.MaxCidrs[0] != "172.16.0.0/12" {
		t.Errorf("agn.MaxCidrs = %v, want [172.16.0.0/12]", agn.MaxCidrs)
	}
}

// SC-002: Load returns a user error when <configDir>/backplane.yaml does not exist.
func TestLoad_FileNotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got: %v", err)
	}
	want := "backplane.yaml not found in " + dir
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// System error classification: an unreadable (but existing) backplane.yaml surfaces as a raw
// wrapped error, not a user error.
func TestLoad_UnreadableFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "backplane.yaml"), 0o755); err != nil {
		t.Fatalf("setting up fixture: %v", err)
	}

	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.IsUserError(err) {
		t.Errorf("expected non-user error, got user error: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "reading backplane.yaml:") {
		t.Errorf("error = %q, want prefix %q", err.Error(), "reading backplane.yaml:")
	}
}

// SC-003: Load returns a user error when the file is not valid YAML.
func TestLoad_MalformedYAML(t *testing.T) {
	_, err := Load(newBackplaneDir(t, "regions: [this is not a valid map"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "failed to parse backplane.yaml:") {
		t.Errorf("error = %q, want prefix %q", err.Error(), "failed to parse backplane.yaml:")
	}
}

// SC-004: Load accepts an empty or omitted regions map.
func TestLoad_EmptyRegions(t *testing.T) {
	cfg, err := Load(newBackplaneDir(t, "globalParameters:\n  FOO: \"bar\"\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Regions) != 0 {
		t.Errorf("len(Regions) = %d, want 0", len(cfg.Regions))
	}
	if cfg.Regions == nil {
		t.Error("Regions = nil, want non-nil empty map")
	}
}

// SC-004 (continued): Load defaults globalParameters to an empty map when omitted entirely.
func TestLoad_EmptyFile(t *testing.T) {
	cfg, err := Load(newBackplaneDir(t, ""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GlobalParameters == nil {
		t.Error("GlobalParameters = nil, want non-nil empty map")
	}
	if cfg.Regions == nil {
		t.Error("Regions = nil, want non-nil empty map")
	}
}

// SC-005: A region's Available defaults to false when omitted, and honors an explicit
// true or false.
func TestLoad_AvailableDefault(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want bool
	}{
		{"omitted", "regions:\n  aws-eu-central-1: {}\n", false},
		{"explicitTrue", "regions:\n  aws-eu-central-1:\n    available: true\n", true},
		{"explicitFalse", "regions:\n  aws-eu-central-1:\n    available: false\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(newBackplaneDir(t, tc.yaml))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			region, ok := cfg.Regions["aws-eu-central-1"]
			if !ok {
				t.Fatal("expected region aws-eu-central-1 to be present")
			}
			if region.Available != tc.want {
				t.Errorf("Available = %v, want %v", region.Available, tc.want)
			}
		})
	}
}

// SC-006: Region(name) returns the matching *Region for a known region, and a user error
// for an unknown one.
func TestRegion_Lookup(t *testing.T) {
	cfg, err := Load(newBackplaneDir(t, wellFormedFixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	region, err := cfg.Region("aws-eu-central-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !region.Available {
		t.Error("Available = false, want true")
	}

	_, err = cfg.Region("aws-ap-southeast-1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got: %v", err)
	}
	want := "region 'aws-ap-southeast-1' not found in backplane.yaml"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}

	_, err = cfg.Region("")
	if err == nil {
		t.Fatal("expected error for empty region name, got nil")
	}
}

// SC-007: Connection(name) returns the matching *Connection and ok == true for a known
// connection, and ok == false for an unknown one.
func TestConnection_Lookup(t *testing.T) {
	cfg, err := Load(newBackplaneDir(t, wellFormedFixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	region, err := cfg.Region("aws-eu-central-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	conn, ok := region.Connection("public")
	if !ok {
		t.Fatal("expected connection public to be present")
	}
	if conn.Type != "IPV4" {
		t.Errorf("Type = %q, want %q", conn.Type, "IPV4")
	}

	_, ok = region.Connection("does-not-exist")
	if ok {
		t.Error("expected ok == false for unknown connection")
	}

	_, ok = region.Connection("")
	if ok {
		t.Error("expected ok == false for empty connection name")
	}
}

// SC-008: Load returns a user error when the same connection name appears twice in one
// region's inventory.
func TestLoad_DuplicateConnection(t *testing.T) {
	yaml := `
regions:
  aws-eu-central-1:
    inventory:
      - connection: agn
        type: AWSVPCEID
      - connection: agn
        type: AWSVPCEID
`
	_, err := Load(newBackplaneDir(t, yaml))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got: %v", err)
	}
	want := "regions.aws-eu-central-1.inventory contains connection 'agn' more than once"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// SC-009: Load returns a user error when an inventory entry is missing connection or type.
func TestLoad_MissingInventoryFields(t *testing.T) {
	t.Run("missingConnection", func(t *testing.T) {
		yaml := "regions:\n  aws-eu-central-1:\n    inventory:\n      - type: AWSVPCEID\n"
		_, err := Load(newBackplaneDir(t, yaml))
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		want := "regions.aws-eu-central-1.inventory[0].connection is required"
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
		if !errors.IsUserError(err) {
			t.Errorf("expected user error, got: %v", err)
		}
	})

	t.Run("missingType", func(t *testing.T) {
		yaml := "regions:\n  aws-eu-central-1:\n    inventory:\n      - connection: agn\n"
		_, err := Load(newBackplaneDir(t, yaml))
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		want := "regions.aws-eu-central-1.inventory[0].type is required"
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
		if !errors.IsUserError(err) {
			t.Errorf("expected user error, got: %v", err)
		}
	})
}

// SC-010: Load returns a user error when a regionalAllowlist entry references a connection
// absent from that region's inventory.
func TestLoad_UnknownAllowlistConnection(t *testing.T) {
	yaml := `
regions:
  aws-eu-central-1:
    inventory:
      - connection: public
        type: IPV4
        maxCidrs: ["0.0.0.0/0"]
    regionalAllowlist:
      - connection: agn
`
	_, err := Load(newBackplaneDir(t, yaml))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got: %v", err)
	}
	want := "regions.aws-eu-central-1.regionalAllowlist references unknown connection 'agn'"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// SC-011: Load accepts a regionalAllowlist entry with no allowedIPs, treating it as
// inheriting the connection's full maxCidrs.
func TestLoad_AllowlistInheritsFullRangeWhenAllowedIPsOmitted(t *testing.T) {
	yaml := `
regions:
  aws-eu-central-1:
    inventory:
      - connection: agn
        type: AWSVPCEID
        maxCidrs: ["172.16.0.0/12"]
    regionalAllowlist:
      - connection: agn
`
	cfg, err := Load(newBackplaneDir(t, yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	region, _ := cfg.Region("aws-eu-central-1")
	if len(region.RegionalAllowlist) != 1 {
		t.Fatalf("len(RegionalAllowlist) = %d, want 1", len(region.RegionalAllowlist))
	}
	if len(region.RegionalAllowlist[0].AllowedIPs) != 0 {
		t.Errorf("AllowedIPs = %v, want empty", region.RegionalAllowlist[0].AllowedIPs)
	}
}

// SC-012: Load returns a user error when a regionalAllowlist entry's allowedIPs falls
// outside its connection's maxCidrs.
func TestLoad_AllowlistOutsideMaxCidrs(t *testing.T) {
	yaml := `
regions:
  aws-eu-central-1:
    inventory:
      - connection: agn
        type: AWSVPCEID
        maxCidrs: ["172.16.0.0/12"]
    regionalAllowlist:
      - connection: agn
        allowedIPs: ["172.32.0.0/16"]
`
	_, err := Load(newBackplaneDir(t, yaml))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got: %v", err)
	}
	want := "regions.aws-eu-central-1.regionalAllowlist connection 'agn' allowedIPs " +
		"'172.32.0.0/16' is not contained within maxCidrs [172.16.0.0/12]"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// SC-013: Load returns a user error when allowedIPs is set for a connection with no maxCidrs.
func TestLoad_AllowlistWithNoMaxCidrsToNarrow(t *testing.T) {
	yaml := `
regions:
  aws-eu-central-1:
    inventory:
      - connection: dbt-cloud
        type: AWSVPCEID
    regionalAllowlist:
      - connection: dbt-cloud
        allowedIPs: ["10.0.0.0/8"]
`
	_, err := Load(newBackplaneDir(t, yaml))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.IsUserError(err) {
		t.Errorf("expected user error, got: %v", err)
	}
	want := "regions.aws-eu-central-1.regionalAllowlist connection 'dbt-cloud' specifies " +
		"allowedIPs but this connection has no maxCidrs to narrow"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// SC-014: Load returns a user error when any maxCidrs or allowedIPs entry is not a valid CIDR.
func TestLoad_InvalidCIDR(t *testing.T) {
	t.Run("maxCidrs", func(t *testing.T) {
		yaml := `
regions:
  aws-eu-central-1:
    inventory:
      - connection: agn
        type: AWSVPCEID
        maxCidrs: ["172.16.0.0/99"]
`
		_, err := Load(newBackplaneDir(t, yaml))
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		want := "regions.aws-eu-central-1.inventory[0].maxCidrs '172.16.0.0/99' is not a valid CIDR"
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
		if !errors.IsUserError(err) {
			t.Errorf("expected user error, got: %v", err)
		}
	})

	t.Run("allowedIPs", func(t *testing.T) {
		yaml := `
regions:
  aws-eu-central-1:
    inventory:
      - connection: agn
        type: AWSVPCEID
        maxCidrs: ["172.16.0.0/12"]
    regionalAllowlist:
      - connection: agn
        allowedIPs: ["not-a-cidr"]
`
		_, err := Load(newBackplaneDir(t, yaml))
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		want := "regions.aws-eu-central-1.regionalAllowlist connection 'agn' allowedIPs " +
			"'not-a-cidr' is not a valid CIDR"
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
		if !errors.IsUserError(err) {
			t.Errorf("expected user error, got: %v", err)
		}
	})
}

// SC-015: ContainsCIDR returns true when candidate is fully inside one of ranges (including
// when it equals a range exactly), and false when it exceeds every range or matches none.
func TestContainsCIDR_Containment(t *testing.T) {
	cases := []struct {
		name      string
		ranges    []string
		candidate string
		want      bool
	}{
		{"containedNarrower", []string{"172.16.0.0/12"}, "172.16.5.0/24", true},
		{"exactMatch", []string{"172.16.0.0/12"}, "172.16.0.0/12", true},
		{"wider", []string{"172.16.0.0/12"}, "172.0.0.0/8", false},
		{"disjoint", []string{"172.16.0.0/12"}, "10.0.0.0/8", false},
		{"noRanges", []string{}, "10.0.0.0/8", false},
		{"secondRangeMatches", []string{"10.0.0.0/8", "172.16.0.0/12"}, "172.16.5.0/24", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ContainsCIDR(tc.ranges, tc.candidate)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("ContainsCIDR(%v, %q) = %v, want %v", tc.ranges, tc.candidate, got, tc.want)
			}
		})
	}
}

// SC-016: ContainsCIDR returns a user error when candidate or any entry of ranges is not a
// valid CIDR.
func TestContainsCIDR_InvalidCIDR(t *testing.T) {
	t.Run("invalidCandidate", func(t *testing.T) {
		_, err := ContainsCIDR([]string{"172.16.0.0/12"}, "not-a-cidr")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.IsUserError(err) {
			t.Errorf("expected user error, got: %v", err)
		}
		want := "'not-a-cidr' is not a valid CIDR"
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
	})

	t.Run("invalidRangeEntry", func(t *testing.T) {
		_, err := ContainsCIDR([]string{"not-a-cidr"}, "172.16.0.0/12")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.IsUserError(err) {
			t.Errorf("expected user error, got: %v", err)
		}
		want := "'not-a-cidr' is not a valid CIDR"
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
	})
}

// SC-018: The returned *Config is safe for concurrent read-only use by multiple goroutines
// after Load returns.
func TestConfig_ConcurrentReadOnlyUse(t *testing.T) {
	cfg, err := Load(newBackplaneDir(t, wellFormedFixture))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = cfg.GlobalParameters["PREVENT_UNLOAD_TO_INLINE_URL"]
			region, err := cfg.Region("aws-eu-central-1")
			if err != nil {
				return
			}
			_ = region.Available
			_ = region.RegionalParameters["ENABLE_INTERNAL_STAGES_PRIVATELINK"]
			for _, entry := range region.RegionalAllowlist {
				conn, ok := region.Connection(entry.Connection)
				if !ok {
					continue
				}
				_ = conn.Type
				_ = conn.VpceID
				_ = conn.MaxCidrs
				_ = entry.AllowedIPs
			}
		}()
	}
	wg.Wait()
}
