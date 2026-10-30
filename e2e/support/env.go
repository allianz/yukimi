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

package support

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/secrets"
	"github.com/allianz/yukimi/internal/snowflake/pool"
)

// Env is everything a test needs: the operator configuration, the clients for
// Kubernetes and AWS, and the namespace of this run.
type Env struct {
	Cfg       *Config
	Kube      client.Client
	AWS       aws.Config
	Ctx       context.Context // cancelled by the first Ctrl-C; the clean-up does not use it
	Namespace string
	Region    string // a region that is available in the operator configuration

	// The fixtures of this run (see fixtures.go).
	fixtureMu sync.Mutex
	fixtures  map[string]*Fixture
	started   int // real accounts started by fixtures

	// The Snowflake pool is opened on first use (see snowflake.go).
	poolOnce sync.Once
	pool     *pool.Pool
	poolErr  error
}

// Scheme returns a scheme with the core Kubernetes types and the Yukimi API.
func Scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		panic(err)
	}
	return s
}

// Connect builds the clients the way a developer's tools already do: the
// Kubernetes client from the default kubeconfig and its current context, the AWS
// client from the default credential chain. It only builds them; Check proves
// they work.
func Connect(ctx context.Context, cfg *Config) (*Env, error) {
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("no usable Kubernetes configuration (is the default kubeconfig set?): %w", err)
	}
	kube, err := client.New(restCfg, client.Options{Scheme: Scheme()})
	if err != nil {
		return nil, fmt.Errorf("creating the Kubernetes client: %w", err)
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Base.AWS.Region))
	if err != nil {
		return nil, fmt.Errorf("loading the AWS configuration: %w", err)
	}

	region, err := cfg.AvailableRegion()
	if err != nil {
		return nil, err
	}
	return &Env{Cfg: cfg, Kube: kube, AWS: awsCfg, Region: region}, nil
}

// KubeContext returns the name of the current kubeconfig context, for the
// start-up banner.
func KubeContext() string {
	raw, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).RawConfig()
	if err != nil || raw.CurrentContext == "" {
		return "(unknown)"
	}
	return raw.CurrentContext
}

// Check runs the start-up checks and stops at the first failure with a message
// that says what to do. Nothing is created before all of them pass, except the
// probe object of the operator check, which is removed with the namespace.
func (e *Env) Check(ctx context.Context) (awsIdentity string, err error) {
	if err := e.checkKubernetes(ctx); err != nil {
		return "", err
	}
	awsIdentity, err = e.checkAWS(ctx)
	if err != nil {
		return "", err
	}
	return awsIdentity, nil
}

func (e *Env) checkKubernetes(ctx context.Context) error {
	var accounts v1alpha1.SnowflakeAccountList
	if err := e.Kube.List(ctx, &accounts, client.InNamespace(metav1.NamespaceDefault), client.Limit(1)); err != nil {
		return fmt.Errorf("Kubernetes check failed: cannot list SnowflakeAccounts (cluster unreachable, or the Yukimi CRDs are not installed; `make dev` installs them): %w", err)
	}
	var permits v1alpha1.DeletionPermitList
	if err := e.Kube.List(ctx, &permits, client.InNamespace(metav1.NamespaceDefault), client.Limit(1)); err != nil {
		return fmt.Errorf("Kubernetes check failed: cannot list DeletionPermits: %w", err)
	}
	return nil
}

func (e *Env) checkAWS(ctx context.Context) (string, error) {
	out, err := sts.NewFromConfig(e.AWS).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("AWS check failed: the default AWS credentials do not work (log in with the AWS CLI and select the profile with the AWS_PROFILE environment variable; `--profile` on the CLI does not apply to this process): %w", err)
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

// CheckOperatorAlive applies an account with a region the operator does not
// know and waits for the controller to react to it. The controller rejects such
// an account before it touches any external system, so the probe is harmless.
func (e *Env) CheckOperatorAlive(ctx context.Context, timeout time.Duration) error {
	probe := NewAccount("operator-probe", e.Namespace, e.Cfg.UnknownRegion())
	if err := e.Kube.Create(ctx, probe); err != nil {
		return fmt.Errorf("operator check failed: cannot create the probe account: %w", err)
	}
	err := Poll(ctx, timeout, 2*time.Second, "the controller to report a condition on the probe account", func(ctx context.Context) (bool, error) {
		var got v1alpha1.SnowflakeAccount
		if err := e.Kube.Get(ctx, types.NamespacedName{Namespace: e.Namespace, Name: probe.Name}, &got); err != nil {
			return false, err
		}
		return len(got.Status.Conditions) > 0, nil
	})
	if err != nil {
		return fmt.Errorf("operator check failed: the operator does not seem to run against this cluster (start it with `make dev`): %w", err)
	}
	return nil
}

// SecretInfo is what is known about the credential secret of an account.
type SecretInfo struct {
	Exists               bool // the secret is present in AWS Secrets Manager
	ScheduledForDeletion bool // it has a deletion date, so it is in its recovery window
}

// SecretInfo looks the credential secret of an account up in AWS Secrets
// Manager. It uses DescribeSecret, so only metadata is read, never the value.
func (e *Env) SecretInfo(ctx context.Context, accountName string) (SecretInfo, error) {
	return e.SecretInfoIn(ctx, e.Namespace, accountName)
}

// SecretInfoIn is SecretInfo for an account in another namespace of a run.
func (e *Env) SecretInfoIn(ctx context.Context, namespace, accountName string) (SecretInfo, error) {
	id, err := secrets.NewTenantIdentifier(e.Cfg.Org(), namespace, accountName)
	if err != nil {
		return SecretInfo{}, err
	}
	out, err := secretsmanager.NewFromConfig(e.AWS).DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{SecretId: aws.String(id.String())})
	if err == nil {
		return SecretInfo{Exists: true, ScheduledForDeletion: out.DeletedDate != nil}, nil
	}
	var notFound *smtypes.ResourceNotFoundException
	if errors.As(err, &notFound) {
		return SecretInfo{}, nil
	}
	return SecretInfo{}, err
}

// SecretExists reports whether the credential secret of an account exists.
func (e *Env) SecretExists(ctx context.Context, accountName string) (bool, error) {
	info, err := e.SecretInfo(ctx, accountName)
	return info.Exists, err
}

// NewAccount returns a SnowflakeAccount that passes CRD validation. Tests start
// from it and change what the scenario is about.
func NewAccount(name, namespace, region string) *v1alpha1.SnowflakeAccount {
	return &v1alpha1.SnowflakeAccount{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: v1alpha1.SnowflakeAccountSpec{
			Contact:     "e2e@example.com",
			Region:      region,
			Environment: "dev",
			CreditQuota: 1,
			IdentityIntegration: v1alpha1.IdentityIntegration{
				Groups:       map[string][]string{"giam": {"E2E_GROUP"}},
				RoleBindings: map[string]string{"ACCOUNTADMIN": "E2E_GROUP"},
			},
		},
	}
}
