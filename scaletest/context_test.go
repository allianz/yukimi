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
	"strings"
	"testing"
)

func TestCheckContext(t *testing.T) {
	tests := []struct {
		name             string
		current, allowed string
		wantErr          string
	}{
		{name: "the cluster of make dev", current: DevContext},
		{name: "the cluster of make dev, another one allowed", current: DevContext, allowed: "other"},
		{name: "another cluster, allowed on purpose", current: "other", allowed: "other"},
		{name: "another cluster", current: "prod-cluster", wantErr: EnvAllowContext + "=prod-cluster"},
		{name: "another cluster, a different one allowed", current: "prod-cluster", allowed: "other", wantErr: DevContext},
		{name: "no context at all", current: "", wantErr: EnvAllowContext},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckContext(tt.current, tt.allowed)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}
