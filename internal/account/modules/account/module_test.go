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
	"context"
	"testing"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/allianz/yukimi/internal/account/pipeline"
	"github.com/allianz/yukimi/internal/config/backplane"
	"github.com/allianz/yukimi/internal/logger"
	"github.com/allianz/yukimi/internal/secrets"
)

func TestNew(t *testing.T) {
	m := New(secrets.NewKeyManager(secrets.NewFakeKeyStore(), time.Hour), "myorg", 5*time.Minute, 30, false, &backplane.Config{})
	if m == nil {
		t.Fatal("New() = nil, want non-nil")
	}
	if got, want := m.Name(), "account"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
}

// syncStatus sets status.accountName always, and status.accountUrl once a
// locator is known, honoring usePrivateLink.
func TestSyncStatus(t *testing.T) {
	log := logger.New(logging.NewNopLogger(), "ns", "SnowflakeAccount", "acct", logger.OpObserve)

	t.Run("no locator: name only", func(t *testing.T) {
		cr := newTestCR("acct", "ns", "aws-eu-central-1", "", "a@b.com", "")
		mc := pipeline.NewModuleContext(cr, nil, log, nil)
		(&module{}).syncStatus(mc)
		if cr.Status.AccountName != mc.ResolvedAccountName() || cr.Status.AccountName == "" {
			t.Errorf("AccountName = %q, want %q", cr.Status.AccountName, mc.ResolvedAccountName())
		}
		if cr.Status.AccountURL != "" {
			t.Errorf("AccountURL = %q, want empty without a locator", cr.Status.AccountURL)
		}
	})

	t.Run("locator: URL set, private link changes it", func(t *testing.T) {
		urls := map[bool]string{}
		for _, privateLink := range []bool{false, true} {
			cr := newTestCR("acct", "ns", "aws-eu-central-1", "xy12345", "a@b.com", "")
			(&module{usePrivateLink: privateLink}).syncStatus(pipeline.NewModuleContext(cr, nil, log, nil))
			if cr.Status.AccountURL == "" {
				t.Fatalf("AccountURL empty with usePrivateLink=%v", privateLink)
			}
			urls[privateLink] = cr.Status.AccountURL
		}
		if urls[true] == urls[false] {
			t.Errorf("expected usePrivateLink to change the URL, both %q", urls[true])
		}
	})

	t.Run("URL error is logged and swallowed", func(t *testing.T) {
		cr := newTestCR("acct", "ns", "not a region", "xy12345", "a@b.com", "")
		(&module{}).syncStatus(pipeline.NewModuleContext(cr, nil, log, nil))
		if cr.Status.AccountName == "" {
			t.Error("AccountName should still be set")
		}
		if cr.Status.AccountURL != "" {
			t.Errorf("AccountURL = %q, want unset on a region-format error", cr.Status.AccountURL)
		}
	})
}

// Observe (with a known locator) and Apply both leave the status fields set,
// including when they report Pending.
func TestObserveAndApply_SetStatus(t *testing.T) {
	log := logger.New(logging.NewNopLogger(), "ns", "SnowflakeAccount", "acct", logger.OpObserve)
	m := &module{gracePeriod: 5 * time.Minute}

	for name, run := range map[string]func(*pipeline.ModuleContext) pipeline.Outcome{
		"Observe": func(mc *pipeline.ModuleContext) pipeline.Outcome { return m.Observe(context.Background(), mc) },
		"Apply":   func(mc *pipeline.ModuleContext) pipeline.Outcome { return m.Apply(context.Background(), mc) },
	} {
		t.Run(name, func(t *testing.T) {
			cr := newTestCR("acct", "ns", "aws-eu-central-1", "xy12345", "a@b.com", "")
			cr.Status.AccountCreatedAt = &metav1.Time{Time: time.Now()} // within the grace period: Pending, no connection
			out := run(pipeline.NewModuleContext(cr, nil, log, &fakeDBPool{t: t, forbidCalls: true}))
			if out.State != pipeline.StatePending {
				t.Fatalf("State = %v, want StatePending", out.State)
			}
			if cr.Status.AccountName == "" || cr.Status.AccountURL == "" {
				t.Errorf("expected accountName/accountUrl set, got %+v", cr.Status)
			}
		})
	}
}
