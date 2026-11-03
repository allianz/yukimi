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
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/secrets"
)

// DevContext is the kube context of the cluster `make dev` creates
// (kind-$(PROJECT_NAME)-dev in the Makefile).
const DevContext = "kind-yukimi-dev"

// CheckContext refuses any kube context but the one of `make dev`, unless the run
// was told explicitly to use it. A run creates real accounts on the current
// cluster, and they stay.
func CheckContext(current, allowed string) error {
	if current == DevContext || (allowed != "" && current == allowed) {
		return nil
	}
	return fmt.Errorf("the current kube context is %q, not %q, the cluster of `make dev`; a run creates real accounts on the current cluster. "+
		"To use this context on purpose, set %s=%s", current, DevContext, EnvAllowContext, current)
}

// Env is what a run works with: its configuration and the Kubernetes and AWS
// clients.
type Env struct {
	Cfg     *Config
	Kube    client.Client
	AWS     aws.Config
	Context string // the kube context the client is pinned to

	// PollInterval is how often the accounts are listed; zero means the default.
	PollInterval time.Duration
}

func (e *Env) pollInterval() time.Duration {
	if e.PollInterval > 0 {
		return e.PollInterval
	}
	return defaultPollInterval
}

// newScheme (as in e2e/support) returns a scheme with the core Kubernetes types and the Yukimi API.
func newScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	return scheme, nil
}

// Connect builds the clients: the Kubernetes client from the default kubeconfig,
// the AWS client from the default credential chain. It first checks the current
// kube context (see CheckContext) and pins the client to the context it checked,
// so the guard and the client cannot disagree, and a context switched in another
// terminal later changes nothing. It only builds the clients; Check proves they work.
func Connect(ctx context.Context, cfg *Config) (*Env, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	raw, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).RawConfig()
	if err != nil {
		return nil, fmt.Errorf("no usable Kubernetes configuration (is the default kubeconfig set?): %w", err)
	}
	current := raw.CurrentContext
	if current == "" {
		return nil, fmt.Errorf("the kubeconfig has no current kube context; select the cluster of `make dev` with `kubectl config use-context %s`", DevContext)
	}
	if err := CheckContext(current, cfg.AllowContext); err != nil {
		return nil, err
	}
	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: current}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("no usable Kubernetes configuration for the context %q: %w", current, err)
	}

	scheme, err := newScheme()
	if err != nil {
		return nil, err
	}
	// controller-runtime prints a stack dump into the log when no logger is set; the
	// run has its own log.
	ctrllog.SetLogger(zap.New(zap.WriteTo(io.Discard)))
	// The default request rate of client-go (5 per second) would throttle a run
	// that creates thousands of objects.
	restCfg.QPS, restCfg.Burst = 50, 100
	kube, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("creating the Kubernetes client: %w", err)
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Base.AWS.Region))
	if err != nil {
		return nil, fmt.Errorf("loading the AWS configuration: %w", err)
	}
	return &Env{Cfg: cfg, Kube: kube, AWS: awsCfg, Context: current}, nil
}

// Check runs the start-up checks and stops at the first failure with a message
// that says what to do. It creates nothing and reads no secret value.
func (e *Env) Check(ctx context.Context) (awsAccount string, err error) {
	var accounts v1alpha1.SnowflakeAccountList
	if err := e.Kube.List(ctx, &accounts, client.InNamespace(Namespace), client.Limit(1)); err != nil {
		return "", fmt.Errorf("Kubernetes check failed: cannot list SnowflakeAccounts (cluster unreachable, or the Yukimi CRDs are not installed; `make dev` installs them): %w", err)
	}
	var permits v1alpha1.DeletionPermitList
	if err := e.Kube.List(ctx, &permits, client.InNamespace(Namespace), client.Limit(1)); err != nil {
		return "", fmt.Errorf("Kubernetes check failed: cannot list DeletionPermits: %w", err)
	}

	out, err := sts.NewFromConfig(e.AWS).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("AWS check failed: the default AWS credentials do not work (log in with the AWS CLI and select the profile with the AWS_PROFILE environment variable): %w", err)
	}
	id, err := secrets.NewOrgAdminIdentifier(e.Cfg.Org(), e.Cfg.OrgAdminAccount())
	if err != nil {
		return "", fmt.Errorf("AWS check failed: building the org-admin secret identifier: %w", err)
	}
	// DescribeSecret returns metadata only; the secret value is never read.
	if _, err := secretsmanager.NewFromConfig(e.AWS).DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{SecretId: aws.String(id.String())}); err != nil {
		return "", fmt.Errorf("AWS check failed: the org-admin secret %q is not reachable with these credentials: %w", id.String(), err)
	}
	return aws.ToString(out.Account), nil
}

// ExistingAccounts counts the accounts already in the scale test namespace. A
// namespace that does not exist yet lists as empty.
func (e *Env) ExistingAccounts(ctx context.Context) (int, error) {
	accounts, err := e.listAccounts(ctx)
	return len(accounts), err
}
