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

// Package backplane loads the platform-owned catalog of pre-provisioned regional networking
// infrastructure and Snowflake account parameters from a mounted directory at startup into an
// immutable Config.
package backplane

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"

	"github.com/allianz/yukimi/internal/errors"
	"gopkg.in/yaml.v3"
)

// Config is the immutable, validated Backplane Config loaded at startup.
type Config struct {
	GlobalParameters map[string]string // org-wide Snowflake account parameters applied to every account (011)
	Regions          map[string]Region // keyed by region name, e.g. "aws-eu-central-1"
}

// Region is one region's backplane entry.
type Region struct {
	Available          bool              // controller-side gate, no Snowflake counterpart; false when omitted
	Inventory          []Connection      // catalog of physical ingress paths for this region
	RegionalParameters map[string]string // region-specific Snowflake account parameters (011)
	RegionalAllowlist  []AllowlistEntry  // baseline network access applied to every account in this region
}

// Connection is one inventory entry: a physical ingress path and the widest range it may ever carry.
type Connection struct {
	Name     string   // e.g. "agn", "dbt-cloud", "public"
	Type     string   // e.g. "AWSVPCEID", "IPV4"; free-form, not interpreted by this package
	VpceID   string   // set for VPCE-typed connections; empty otherwise
	MaxCidrs []string // widest CIDR(s) this connection may ever carry; empty for VPCE-only connections
}

// AllowlistEntry is one entry under a region's regionalAllowlist.
type AllowlistEntry struct {
	Connection string   // must name a Connection present in the region's Inventory
	AllowedIPs []string // narrows Connection's MaxCidrs; empty means inherit the full range
}

// rawConfig is the direct YAML decode target.
type rawConfig struct {
	GlobalParameters map[string]string    `yaml:"globalParameters"`
	Regions          map[string]rawRegion `yaml:"regions"`
}

// rawRegion mirrors Region for YAML decoding. Available is a *bool so an omitted key can be
// distinguished from an explicit "available: false".
type rawRegion struct {
	Available          *bool               `yaml:"available"`
	Inventory          []rawConnection     `yaml:"inventory"`
	RegionalParameters map[string]string   `yaml:"regionalParameters"`
	RegionalAllowlist  []rawAllowlistEntry `yaml:"regionalAllowlist"`
}

type rawConnection struct {
	Connection string   `yaml:"connection"`
	Type       string   `yaml:"type"`
	VpceID     string   `yaml:"vpceId"`
	MaxCidrs   []string `yaml:"maxCidrs"`
}

type rawAllowlistEntry struct {
	Connection string   `yaml:"connection"`
	AllowedIPs []string `yaml:"allowedIPs"`
}

// Load reads, parses, and validates "<configDir>/backplane.yaml".
//
// Parameters:
//   - configDir: directory containing backplane.yaml (a sibling of base.yaml, 002)
//
// Returns:
//   - *Config: the validated configuration; never nil on a nil error
//   - User error if the file is missing, unreadable, not valid YAML, an inventory entry is
//     missing its connection name or type, a connection name repeats within one region's
//     inventory, a regionalAllowlist entry names a connection absent from that region's
//     inventory, any maxCidrs/allowedIPs entry is not a valid CIDR, an allowedIPs entry falls
//     outside its connection's maxCidrs, or allowedIPs is set for a connection with no maxCidrs
func Load(configDir string) (*Config, error) {
	path := filepath.Join(configDir, "backplane.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.NewUserError(fmt.Sprintf("backplane.yaml not found in %s", configDir))
		}
		return nil, fmt.Errorf("reading backplane.yaml: %w", err)
	}

	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, errors.NewUserError(fmt.Sprintf("failed to parse backplane.yaml: %v", err))
	}

	regionNames := make([]string, 0, len(raw.Regions))
	for name := range raw.Regions {
		regionNames = append(regionNames, name)
	}
	sort.Strings(regionNames)

	regions := make(map[string]Region, len(raw.Regions))
	for _, regionName := range regionNames {
		region, err := loadRegion(regionName, raw.Regions[regionName])
		if err != nil {
			return nil, err
		}
		regions[regionName] = *region
	}

	globalParameters := raw.GlobalParameters
	if globalParameters == nil {
		globalParameters = map[string]string{}
	}

	return &Config{
		GlobalParameters: globalParameters,
		Regions:          regions,
	}, nil
}

// loadRegion validates and converts one region's raw YAML entry, using regionName to build the
// field-path prefix ("regions.<regionName>....") in any error it returns.
func loadRegion(regionName string, rr rawRegion) (*Region, error) {
	available := false
	if rr.Available != nil {
		available = *rr.Available
	}

	inventory := make([]Connection, 0, len(rr.Inventory))
	seenConnections := make(map[string]bool, len(rr.Inventory))
	for idx, rc := range rr.Inventory {
		if rc.Connection == "" {
			return nil, errors.NewUserError(fmt.Sprintf(
				"regions.%s.inventory[%d].connection is required", regionName, idx))
		}
		if rc.Type == "" {
			return nil, errors.NewUserError(fmt.Sprintf(
				"regions.%s.inventory[%d].type is required", regionName, idx))
		}
		if seenConnections[rc.Connection] {
			return nil, errors.NewUserError(fmt.Sprintf(
				"regions.%s.inventory contains connection '%s' more than once", regionName, rc.Connection))
		}
		seenConnections[rc.Connection] = true

		for _, cidr := range rc.MaxCidrs {
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				return nil, errors.NewUserError(fmt.Sprintf(
					"regions.%s.inventory[%d].maxCidrs '%s' is not a valid CIDR", regionName, idx, cidr))
			}
		}

		inventory = append(inventory, Connection{
			Name:     rc.Connection,
			Type:     rc.Type,
			VpceID:   rc.VpceID,
			MaxCidrs: rc.MaxCidrs,
		})
	}

	connByName := make(map[string]*Connection, len(inventory))
	for i := range inventory {
		connByName[inventory[i].Name] = &inventory[i]
	}

	allowlist := make([]AllowlistEntry, 0, len(rr.RegionalAllowlist))
	for _, ra := range rr.RegionalAllowlist {
		conn, ok := connByName[ra.Connection]
		if !ok {
			return nil, errors.NewUserError(fmt.Sprintf(
				"regions.%s.regionalAllowlist references unknown connection '%s'", regionName, ra.Connection))
		}

		for _, ip := range ra.AllowedIPs {
			if _, _, err := net.ParseCIDR(ip); err != nil {
				return nil, errors.NewUserError(fmt.Sprintf(
					"regions.%s.regionalAllowlist connection '%s' allowedIPs '%s' is not a valid CIDR",
					regionName, ra.Connection, ip))
			}
		}

		if len(ra.AllowedIPs) > 0 && len(conn.MaxCidrs) == 0 {
			return nil, errors.NewUserError(fmt.Sprintf(
				"regions.%s.regionalAllowlist connection '%s' specifies allowedIPs but this connection has no maxCidrs to narrow",
				regionName, ra.Connection))
		}

		for _, ip := range ra.AllowedIPs {
			// conn.MaxCidrs and ip were already validated as well-formed CIDRs above,
			// so ContainsCIDR cannot itself error here.
			contained, _ := ContainsCIDR(conn.MaxCidrs, ip)
			if !contained {
				return nil, errors.NewUserError(fmt.Sprintf(
					"regions.%s.regionalAllowlist connection '%s' allowedIPs '%s' is not contained within maxCidrs %v",
					regionName, ra.Connection, ip, conn.MaxCidrs))
			}
		}

		allowlist = append(allowlist, AllowlistEntry{
			Connection: ra.Connection,
			AllowedIPs: ra.AllowedIPs,
		})
	}

	regionalParameters := rr.RegionalParameters
	if regionalParameters == nil {
		regionalParameters = map[string]string{}
	}

	return &Region{
		Available:          available,
		Inventory:          inventory,
		RegionalParameters: regionalParameters,
		RegionalAllowlist:  allowlist,
	}, nil
}

// Region looks up a region by name — typically a SnowflakeAccount's spec.region (design.md 3.1).
// It reports only what the file says; it does not consult Available, leaving what an unavailable
// or unknown region means for the caller's request to the caller (009, 018).
//
// Returns:
//   - User error if no region with this name exists in the loaded config
func (c *Config) Region(name string) (*Region, error) {
	region, ok := c.Regions[name]
	if !ok {
		return nil, errors.NewUserError(fmt.Sprintf("region '%s' not found in backplane.yaml", name))
	}
	return &region, nil
}

// Connection looks up a connection by name within this region's inventory.
//
// Returns:
//   - ok: false if no connection with this name exists in the region's inventory
func (r *Region) Connection(name string) (*Connection, bool) {
	for i := range r.Inventory {
		if r.Inventory[i].Name == name {
			return &r.Inventory[i], true
		}
	}
	return nil, false
}

// ContainsCIDR reports whether candidate is fully contained within at least one entry of ranges.
// Pure comparison, no I/O; reused by 012 to validate customNetworkRules allowedIPs entries
// against a connection's MaxCidrs, applying the identical containment rule Load itself uses for
// regionalAllowlist.
//
// Returns:
//   - User error if candidate or any entry of ranges is not a valid CIDR
func ContainsCIDR(ranges []string, candidate string) (bool, error) {
	candidateIP, candidateNet, err := net.ParseCIDR(candidate)
	if err != nil {
		return false, errors.NewUserError(fmt.Sprintf("'%s' is not a valid CIDR", candidate))
	}
	candidateOnes, _ := candidateNet.Mask.Size()

	for _, r := range ranges {
		_, rangeNet, err := net.ParseCIDR(r)
		if err != nil {
			return false, errors.NewUserError(fmt.Sprintf("'%s' is not a valid CIDR", r))
		}
		rangeOnes, _ := rangeNet.Mask.Size()

		if candidateOnes >= rangeOnes && rangeNet.Contains(candidateIP) {
			return true, nil
		}
	}
	return false, nil
}
