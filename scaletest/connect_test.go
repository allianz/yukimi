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
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/config/base"
)

// isolateAWS keeps the AWS settings of the developer's machine out of the test.
func isolateAWS(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
}

// server is a fake API server that counts the requests it gets.
func server(t *testing.T) (url string, requests *atomic.Int32) {
	t.Helper()
	requests = new(atomic.Int32)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.NotFound(w, nil)
	}))
	t.Cleanup(s.Close)
	return s.URL, requests
}

// writeKubeconfig makes KUBECONFIG point at a file with one context per server,
// named like the keys of servers, and the given one current.
func writeKubeconfig(t *testing.T, current string, servers map[string]string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("apiVersion: v1\nkind: Config\ncurrent-context: " + current + "\nusers:\n- name: u\n  user: {token: x}\nclusters:\n")
	for name, url := range servers {
		b.WriteString("- name: " + name + "\n  cluster: {server: '" + url + "'}\n")
	}
	b.WriteString("contexts:\n")
	for name := range servers {
		b.WriteString("- name: " + name + "\n  context: {cluster: " + name + ", user: u}\n")
	}
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
}

// touch makes the client talk to its server.
func touch(env *Env) {
	var list v1alpha1.SnowflakeAccountList
	_ = env.Kube.List(context.Background(), &list, client.InNamespace(Namespace))
}

func TestConnectUsesTheCheckedContext(t *testing.T) {
	isolateAWS(t)
	dev, devRequests := server(t)
	prod, prodRequests := server(t)
	writeKubeconfig(t, DevContext, map[string]string{DevContext: dev, "prod": prod})

	env, err := Connect(context.Background(), &Config{Base: &base.Config{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	touch(env)
	if env.Context != DevContext || devRequests.Load() == 0 || prodRequests.Load() != 0 {
		t.Fatalf("context %q, requests to dev %d, to prod %d; want the client to talk to the cluster of make dev only",
			env.Context, devRequests.Load(), prodRequests.Load())
	}
}

func TestConnectRefusesAnotherContext(t *testing.T) {
	isolateAWS(t)
	dev, devRequests := server(t)
	prod, prodRequests := server(t)
	writeKubeconfig(t, "prod", map[string]string{DevContext: dev, "prod": prod})

	_, err := Connect(context.Background(), &Config{Base: &base.Config{}})
	if err == nil || !strings.Contains(err.Error(), EnvAllowContext+"=prod") {
		t.Fatalf("err = %v, want a refusal that names %s", err, EnvAllowContext)
	}
	if devRequests.Load() != 0 || prodRequests.Load() != 0 {
		t.Fatal("a refused run must not talk to any cluster")
	}

	// On purpose: allowed by name. Then it talks to that cluster, and only to it.
	env, err := Connect(context.Background(), &Config{Base: &base.Config{}, Settings: Settings{AllowContext: "prod"}})
	if err != nil {
		t.Fatal(err)
	}
	touch(env)
	if env.Context != "prod" || prodRequests.Load() == 0 || devRequests.Load() != 0 {
		t.Fatalf("context %q, requests to dev %d, to prod %d", env.Context, devRequests.Load(), prodRequests.Load())
	}
}

func TestConnectWithABrokenKubeconfig(t *testing.T) {
	isolateAWS(t)
	t.Run("no kubeconfig at all", func(t *testing.T) {
		t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
		t.Setenv("HOME", t.TempDir())
		_, err := Connect(context.Background(), &Config{Base: &base.Config{}})
		if err == nil || !strings.Contains(err.Error(), "use-context "+DevContext) {
			t.Fatalf("err = %v, want advice to select %s", err, DevContext)
		}
	})

	t.Run("the current context does not exist", func(t *testing.T) {
		dev, _ := server(t)
		writeKubeconfig(t, "ghost", map[string]string{DevContext: dev})
		_, err := Connect(context.Background(), &Config{Base: &base.Config{}, Settings: Settings{AllowContext: "ghost"}})
		if err == nil || !strings.Contains(err.Error(), "ghost") {
			t.Fatalf("err = %v, want an error that names the missing context", err)
		}
	})
}
