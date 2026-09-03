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

package account

import (
	"testing"
	"time"

	"github.com/allianz/yukimi/internal/secrets"
)

func TestNew(t *testing.T) {
	m := New(secrets.NewFakeBackend(), "myorg", 5*time.Minute)
	if m == nil {
		t.Fatal("New() = nil, want non-nil")
	}
	if got, want := m.Name(), "account"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
}
