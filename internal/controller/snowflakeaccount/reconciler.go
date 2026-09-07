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

package snowflakeaccount

import (
	"context"
	"fmt"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/allianz/yukimi/apis/base/v1alpha1"
	accountmodule "github.com/allianz/yukimi/internal/account/modules/account"
	"github.com/allianz/yukimi/internal/account/pipeline"
	"github.com/allianz/yukimi/internal/account/tenant"
	"github.com/allianz/yukimi/internal/config/base"
	"github.com/allianz/yukimi/internal/deletion"
	internalerrors "github.com/allianz/yukimi/internal/errors"
	"github.com/allianz/yukimi/internal/logger"
	"github.com/allianz/yukimi/internal/secrets"
	"github.com/allianz/yukimi/internal/snowflake/pool"
)

// SetupGated adds a controller that reconciles SnowflakeAccount objects with
// safe-start support. Registers a pipeline (009) of exactly one module for
// this cut — the account module (012) — since guardrail-check, quota-check,
// parameters, network, auth, identity, and quota-monitor (010, 011, 013-015,
// 017, 018) are not written yet.
func SetupGated(mgr ctrl.Manager, o controller.Options, cfg *base.Config, p *pool.Pool, secretsBackend secrets.Backend) error {
	o.Gate.Register(func() {
		if err := Setup(mgr, o, cfg, p, secretsBackend); err != nil {
			panic(errors.Wrap(err, "cannot setup SnowflakeAccount controller"))
		}
	}, v1alpha1.SnowflakeAccountGroupVersionKind)
	return nil
}

func Setup(mgr ctrl.Manager, o controller.Options, cfg *base.Config, p *pool.Pool, secretsBackend secrets.Backend) error {
	name := managed.ControllerName(v1alpha1.SnowflakeAccountGroupKind)

	pl := pipeline.New(accountmodule.New(
		secretsBackend, cfg.Snowflake.Org, cfg.Snowflake.AccountCreationGracePeriod, cfg.Deletion.GracePeriodDays))
	rec := event.NewAPIRecorder(mgr.GetEventRecorderFor(name))
	opLogger := o.Logger.WithValues("controller", name)

	conn := &connector{
		kube:     mgr.GetClient(),
		pool:     p,
		pipeline: pl,
		cfg:      cfg,
		logger:   opLogger,
		record:   rec,
	}

	opts := []managed.ReconcilerOption{
		managed.WithTypedExternalConnector[*v1alpha1.SnowflakeAccount](conn),
		managed.WithLogger(opLogger),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(rec),
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

	r := managed.NewReconciler(mgr, resource.ManagedKind(v1alpha1.SnowflakeAccountGroupVersionKind), opts...)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.SnowflakeAccount{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// connector produces an external client sharing one pipeline and pool across
// every SnowflakeAccount this controller reconciles. Unlike
// snowflakedeletionrequest's connector, Connect here has real fields to carry
// forward — but still performs no I/O of its own, since the account module's
// connections are all lazy and pooled (004).
type connector struct {
	kube client.Client
	// pool is typed as the pipeline.DBPool interface, not the concrete
	// *pool.Pool SetupGated/Setup take, so unit tests can inject a fake
	// without a live Snowflake connection.
	pool     pipeline.DBPool
	pipeline *pipeline.Pipeline
	cfg      *base.Config
	logger   logging.Logger
	record   event.Recorder
}

func (c *connector) Connect(_ context.Context, _ *v1alpha1.SnowflakeAccount) (managed.TypedExternalClient[*v1alpha1.SnowflakeAccount], error) {
	return &external{
		kube:     c.kube,
		pool:     c.pool,
		pipeline: c.pipeline,
		cfg:      c.cfg,
		logger:   c.logger,
		record:   c.record,
	}, nil
}

// external implements the Standard Controller with External State pattern
// (CLAUDE.md): Observe/Create/Update/Delete each build one
// pipeline.ModuleContext, fresh from the CRD and namespace labels as they
// stand at that moment, and hand it unchanged to whichever pipeline entry
// point that method needs.
type external struct {
	kube     client.Client
	pool     pipeline.DBPool
	pipeline *pipeline.Pipeline
	cfg      *base.Config
	logger   logging.Logger
	record   event.Recorder
}

func (e *external) Disconnect(_ context.Context) error { return nil }

// namespaceLabels reads the target namespace's labels fresh from the
// Kubernetes API on every call. Nothing registered in this cut reads the
// result, but fetching it now means a future guardrail-check or quota-check
// module (010/011) needs no additional plumbing change here to start
// receiving real data.
func (e *external) namespaceLabels(ctx context.Context, namespace string) (map[string]string, error) {
	var ns corev1.Namespace
	if err := e.kube.Get(ctx, client.ObjectKey{Name: namespace}, &ns); err != nil {
		return nil, fmt.Errorf("failed to get namespace %s: %w", namespace, err)
	}
	return ns.Labels, nil
}

// renderOutcomes records every outcome's Event/Condition onto cr, in outcome
// order, exactly as Observe or apply received them from the pipeline.
func (e *external) renderOutcomes(cr *v1alpha1.SnowflakeAccount, outcomes []pipeline.ModuleOutcome) {
	for _, mo := range outcomes {
		if mo.Outcome.Event != nil {
			e.record.Event(cr, *mo.Outcome.Event)
		}
		if mo.Outcome.Condition != nil {
			cr.SetConditions(*mo.Outcome.Condition)
		}
	}
}

// updateAccountStatus computes status.accountName/status.accountUrl directly
// from mc and the CRD's own already-set status.accountLocator — never from a
// module's Outcome. A tenant.AccountURL error is logged and swallowed, never
// failing the caller: it can only fire despite CREATE ACCOUNT having already
// succeeded with that same region string (Edge Cases).
func (e *external) updateAccountStatus(cr *v1alpha1.SnowflakeAccount, log *logger.Logger, mc *pipeline.ModuleContext) {
	cr.Status.AccountName = mc.ResolvedAccountName()
	if cr.Status.AccountLocator == "" {
		return
	}
	url, err := tenant.AccountURL(cr.Status.AccountLocator, cr.Spec.Region, e.cfg.Snowflake.UsePrivateLink)
	if err != nil {
		log.Handle(err)
		return
	}
	cr.Status.AccountURL = url
}

func (e *external) Observe(ctx context.Context, cr *v1alpha1.SnowflakeAccount) (managed.ExternalObservation, error) {
	log := logger.New(e.logger, cr.Namespace, "SnowflakeAccount", cr.Name, logger.OpObserve)

	if cr.GetDeletionTimestamp() != nil {
		return managed.ExternalObservation{
			ResourceExists:   cr.Status.AccountLocator != "",
			ResourceUpToDate: true,
		}, nil
	}

	labels, err := e.namespaceLabels(ctx, cr.Namespace)
	if err != nil {
		retryErr := log.Handle(err)
		cr.SetConditions(xpv1.Unavailable().WithMessage(retryErr.Error()))
		return managed.ExternalObservation{}, retryErr
	}

	// No backplane lookup for this cut (D-005): nothing registered ever calls
	// ModuleContext.BackplaneRegion().
	mc := pipeline.NewModuleContext(cr, cr.Namespace, nil, labels, log, e.pool)
	obs := e.pipeline.Observe(ctx, mc)
	if !obs.Exists {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	e.renderOutcomes(cr, obs.Outcomes)
	e.updateAccountStatus(cr, log, mc)

	// Observe never flips Ready to True for the first time — only apply()'s
	// AllDone branch does that (Key Concept: The Ready Latch Lives on the
	// Persisted Condition).
	if cr.GetCondition(xpv1.TypeReady).Status != corev1.ConditionTrue {
		cr.SetConditions(xpv1.Unavailable().WithMessage(obs.PendingReason()))
	}

	upToDate := cr.Status.GetObservedGeneration() == cr.Generation && obs.InSync
	return managed.ExternalObservation{ResourceExists: true, ResourceUpToDate: upToDate}, nil
}

func (e *external) Create(ctx context.Context, cr *v1alpha1.SnowflakeAccount) (managed.ExternalCreation, error) {
	return managed.ExternalCreation{}, e.apply(ctx, cr)
}

func (e *external) Update(ctx context.Context, cr *v1alpha1.SnowflakeAccount) (managed.ExternalUpdate, error) {
	return managed.ExternalUpdate{}, e.apply(ctx, cr)
}

// apply is shared by Create and Update: Pipeline.Apply is idempotent by
// construction, so there is nothing for the two methods to do differently.
func (e *external) apply(ctx context.Context, cr *v1alpha1.SnowflakeAccount) error {
	log := logger.New(e.logger, cr.Namespace, "SnowflakeAccount", cr.Name, logger.OpUpdate)

	labels, err := e.namespaceLabels(ctx, cr.Namespace)
	if err != nil {
		return log.Handle(err)
	}

	mc := pipeline.NewModuleContext(cr, cr.Namespace, nil, labels, log, e.pool)
	result := e.pipeline.Apply(ctx, mc)

	e.renderOutcomes(cr, result.Outcomes)
	// firstErr becomes Synced's message below; log.Handle(nil) == nil.
	firstErr := log.Handle(result.FirstError())

	e.updateAccountStatus(cr, log, mc)

	if result.AllDone() {
		cr.Status.SetObservedGeneration(cr.Generation)
		cr.SetConditions(xpv1.Available())
	}
	if cr.GetCondition(xpv1.TypeReady).Status != corev1.ConditionTrue {
		cr.SetConditions(xpv1.Unavailable().WithMessage(result.PendingReason()))
	}

	// Persist status now, before returning: the managed reconciler's own
	// post-Create/Update status write happens after UpdateCriticalAnnotations,
	// whose plain (non-status-subresource) client.Update round-trips the
	// object through the API server and overwrites our in-memory status with
	// whatever is currently persisted. Without this call, a freshly captured
	// account locator never survives a Create(), and every later reconcile
	// re-attempts account creation against the same name forever.
	if err := e.kube.Status().Update(ctx, cr); err != nil && firstErr == nil {
		firstErr = log.Handle(err)
	}

	// Returning nil here would drop firstErr: the managed reconciler calls
	// status.MarkConditions(xpv1.ReconcileSuccess()) right after Create/Update
	// returns nil, overwriting Synced regardless of what was set above.
	return firstErr
}

// Delete implements design.md §6.3 Phases 2-3, the second of the two gates
// standing between a tenant deleting a SnowflakeAccount and its account
// actually being dropped — crossplane-runtime's managed reconciler (which
// only calls Delete when the immediately preceding Observe reported the
// account as existing) is the first.
func (e *external) Delete(ctx context.Context, cr *v1alpha1.SnowflakeAccount) (managed.ExternalDelete, error) {
	log := logger.New(e.logger, cr.Namespace, "SnowflakeAccount", cr.Name, logger.OpDelete)

	// Phase 2: no active request, no destruction.
	req, err := deletion.FindActiveRequest(ctx, e.kube, cr.Namespace, v1alpha1.SnowflakeAccountKind, cr.Name)
	if err != nil {
		return managed.ExternalDelete{}, log.Handle(err)
	}
	if req == nil {
		blockedErr := internalerrors.NewUserError("deletion blocked: no active SnowflakeDeletionRequest authorizes this account")
		e.record.Event(cr, event.Warning("DeletionBlocked", blockedErr))
		cr.SetConditions(xpv1.Unavailable().WithMessage(blockedErr.Error()))
		return managed.ExternalDelete{}, log.Handle(blockedErr)
	}

	labels, err := e.namespaceLabels(ctx, cr.Namespace)
	if err != nil {
		return managed.ExternalDelete{}, log.Handle(err)
	}
	mc := pipeline.NewModuleContext(cr, cr.Namespace, nil, labels, log, e.pool)

	// Phase 3: every module's Teardown, in reverse — today, just the account
	// module's DROP ACCOUNT and credential cleanup.
	if err := e.pipeline.Destroy(ctx, mc); err != nil {
		return managed.ExternalDelete{}, log.Handle(err)
	}

	return managed.ExternalDelete{}, deletion.MarkConsumed(ctx, e.kube, req)
}
