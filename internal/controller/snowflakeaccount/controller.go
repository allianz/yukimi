/*
Copyright 2025 The Crossplane Authors.
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

// Package snowflakeaccount reconciles SnowflakeAccount managed resources: it
// validates the CRD's region against the Backplane Config, drives the
// account-provisioning pipeline (009), and renders the result onto status
// and conditions. See specs/018-snowflakeaccount-controller.md.
package snowflakeaccount

import (
	"context"

	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/statemetrics"
	ctrl "sigs.k8s.io/controller-runtime"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/account"
	"github.com/allianz/yukimi/internal/backplane"
	"github.com/allianz/yukimi/internal/config"
)

// Dependencies are the runtime collaborators cmd/provider/main.go constructs
// once at startup and injects into this controller. Each is already fully
// built by the time Setup is called — this package never constructs, loads,
// or owns any of them.
type Dependencies struct {
	Config    *config.BaseConfig // 002 — read for Snowflake.UsePrivateLink (tenant.AccountURL)
	Backplane *backplane.Config  // 007 — region lookup and its available gate
	Pipeline  *account.Pipeline  // 009 — constructed from account.New(accountmodule.New(...)) today;
	// grows one argument per module as 011–013/015/016 land

	// Pool is the only Snowflake connection scope this package opens itself
	// (Delete's org-admin connection), and the DBPool every ModuleContext
	// this package builds is handed. Typed as account.DBPool, not the
	// concrete *pool.Pool, so tests can inject a fake; *pool.Pool satisfies
	// it implicitly.
	Pool account.DBPool
}

// SetupGated adds a controller that reconciles SnowflakeAccount managed
// resources with safe-start support, matching every other resource type's
// registration in internal/controller/yukimi.go.
func SetupGated(mgr ctrl.Manager, o controller.Options, deps Dependencies) error {
	o.Gate.Register(func() {
		if err := Setup(mgr, o, deps); err != nil {
			panic(errors.Wrap(err, "cannot setup SnowflakeAccount controller"))
		}
	}, v1alpha1.SnowflakeAccountGroupVersionKind)
	return nil
}

// Setup adds a controller that reconciles SnowflakeAccount managed resources.
func Setup(mgr ctrl.Manager, o controller.Options, deps Dependencies) error {
	name := managed.ControllerName(v1alpha1.SnowflakeAccountGroupKind)

	opts := []managed.ReconcilerOption{
		managed.WithTypedExternalConnector[*v1alpha1.SnowflakeAccount](&connector{
			kube: mgr.GetClient(),
			deps: deps,
			log:  o.Logger,
		}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
	}

	if o.Features.Enabled(feature.EnableBetaManagementPolicies) {
		opts = append(opts, managed.WithManagementPolicies())
	}

	if o.Features.Enabled(feature.EnableAlphaChangeLogs) {
		opts = append(opts, managed.WithChangeLogger(o.ChangeLogOptions.ChangeLogger))
	}

	if o.MetricOptions != nil {
		opts = append(opts, managed.WithMetricRecorder(o.MetricOptions.MRMetrics))
	}

	if o.MetricOptions != nil && o.MetricOptions.MRStateMetrics != nil {
		stateMetricsRecorder := statemetrics.NewMRStateRecorder(
			mgr.GetClient(), o.Logger, o.MetricOptions.MRStateMetrics, &v1alpha1.SnowflakeAccountList{}, o.MetricOptions.PollStateMetricInterval,
		)
		if err := mgr.Add(stateMetricsRecorder); err != nil {
			return errors.Wrap(err, "cannot register MR state metrics recorder for kind v1alpha1.SnowflakeAccountList")
		}
	}

	r := managed.NewReconciler(mgr, resource.ManagedKind(v1alpha1.SnowflakeAccountGroupVersionKind), opts...)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.SnowflakeAccount{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// connector produces an external client for one reconcile.
type connector struct {
	kube client.Client
	deps Dependencies
	log  logging.Logger
}

// Connect returns the external client. There is no per-tenant credential to
// resolve here — every Snowflake connection this package uses is resolved
// lazily, inside ModuleContext or Delete, from deps.Pool.
func (c *connector) Connect(_ context.Context, _ *v1alpha1.SnowflakeAccount) (managed.TypedExternalClient[*v1alpha1.SnowflakeAccount], error) {
	return &external{kube: c.kube, deps: c.deps, log: c.log}, nil
}

// external observes, then creates, updates, or deletes the Snowflake account
// backing one SnowflakeAccount resource.
type external struct {
	kube client.Client
	deps Dependencies
	log  logging.Logger
}

// Disconnect is a no-op: external holds no per-reconcile resource that
// outlives the call (every *sql.DB it reaches is owned and closed by
// deps.Pool, not by this struct).
func (e *external) Disconnect(_ context.Context) error {
	return nil
}
