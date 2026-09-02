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

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/logger"
)

// Observe reports whether the account exists and is reachable — nothing
// more. It does not special-case a resource with a deletion timestamp set:
// the managed reconciler itself decides, from this existence signal plus the
// deletion timestamp, whether to call Create/Update or Delete next (see
// specs/018, Key Concept: Existence Decides Everything, Even Deletion).
//
// Ready is always recomputed from a fresh pipeline.Observe call; it never
// trusts a previous Create/Update call's aggregation (specs/018, D-005).
func (e *external) Observe(ctx context.Context, cr *v1alpha1.SnowflakeAccount) (managed.ExternalObservation, error) {
	log := logger.New(e.log, cr.Namespace, "SnowflakeAccount", cr.Name, logger.OpObserve)

	mc, err := e.buildModuleContext(ctx, cr, log)
	if err != nil {
		retryErr := log.Handle(err)
		cr.SetConditions(xpv1.Unavailable().WithMessage(retryErr.Error()))
		return managed.ExternalObservation{}, nil
	}

	obs, _ := e.deps.Pipeline.Observe(ctx, mc) // err always nil today (009)
	if !obs.Exists {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	if obs.InSync {
		cr.SetConditions(xpv1.Available())
	} else {
		cr.SetConditions(xpv1.Unavailable())
	}

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: cr.Status.GetObservedGeneration() == cr.Generation && obs.InSync,
	}, nil
}
