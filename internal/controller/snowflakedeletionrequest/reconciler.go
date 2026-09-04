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

package snowflakedeletionrequest

import (
	"context"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"

	"github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	"github.com/allianz/yukimi/apis/base/v1alpha1"
)

const (
	stateActive   = "Active"
	stateExpired  = "Expired"
	stateConsumed = "Consumed"
)

// SetupGated adds a controller that reconciles SnowflakeDeletionRequest
// objects with safe-start support.
func SetupGated(mgr ctrl.Manager, o controller.Options) error {
	o.Gate.Register(func() {
		if err := Setup(mgr, o); err != nil {
			panic(errors.Wrap(err, "cannot setup SnowflakeDeletionRequest controller"))
		}
	}, v1alpha1.SnowflakeDeletionRequestGroupVersionKind)
	return nil
}

func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.SnowflakeDeletionRequestGroupKind)

	opts := []managed.ReconcilerOption{
		managed.WithTypedExternalConnector[*v1alpha1.SnowflakeDeletionRequest](&connector{}),
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

	// No MRStateMetrics wiring here: that recorder requires the List type to
	// implement resource.ManagedList (GetItems), which needs a generated
	// zz_generated.managedlist.go — unavailable for this deliberately
	// minimal-surface managed resource, the same reason it hand-implements
	// resource.Managed instead of relying on angryjet (see the type's own
	// doc comment).

	r := managed.NewReconciler(mgr, resource.ManagedKind(v1alpha1.SnowflakeDeletionRequestGroupVersionKind), opts...)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.SnowflakeDeletionRequest{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// A connector produces an ExternalClient. Unlike every other controller in
// this codebase, Connect here needs no secrets or pooled Snowflake
// connection — Observe/Create/Update/Delete compute only from fields
// already present on the object handed to them.
type connector struct{}

func (c *connector) Connect(_ context.Context, _ *v1alpha1.SnowflakeDeletionRequest) (managed.TypedExternalClient[*v1alpha1.SnowflakeDeletionRequest], error) {
	return &external{}, nil
}

// external implements the Validation-Only Controller pattern (CLAUDE.md):
// there is no external resource to manage, so Create/Update/Delete are
// no-ops and all logic lives in Observe.
type external struct{}

// Observe recomputes status.state on every call, regardless of whether
// Generation changed, because the Active -> Expired transition is
// time-driven rather than spec-driven. Once state reaches a terminal value
// (Expired or Consumed) it stops recomputing validUntil/state from
// spec.duration, freezing them at their terminal values.
func (c *external) Observe(_ context.Context, cr *v1alpha1.SnowflakeDeletionRequest) (managed.ExternalObservation, error) {
	if cr.GetDeletionTimestamp() != nil {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	if cr.Status.State != stateExpired && cr.Status.State != stateConsumed {
		validUntil := cr.CreationTimestamp.Add(cr.Spec.Duration.Duration)

		state := stateActive
		if time.Now().After(validUntil) {
			state = stateExpired
		}

		vu := metav1.NewTime(validUntil)
		cr.Status.ValidUntil = &vu
		cr.Status.State = state
	}

	cr.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true,
	}, nil
}

func (c *external) Create(_ context.Context, _ *v1alpha1.SnowflakeDeletionRequest) (managed.ExternalCreation, error) {
	return managed.ExternalCreation{}, nil
}

func (c *external) Update(_ context.Context, _ *v1alpha1.SnowflakeDeletionRequest) (managed.ExternalUpdate, error) {
	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(_ context.Context, _ *v1alpha1.SnowflakeDeletionRequest) (managed.ExternalDelete, error) {
	return managed.ExternalDelete{}, nil
}

func (c *external) Disconnect(_ context.Context) error {
	return nil
}
