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

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/logger"
)

// apply is the shared body of Create and Update: it is idempotent by
// construction (account.Pipeline.Apply is), so both lifecycle methods may
// call it with identical behavior (SC-006).
func (e *external) apply(ctx context.Context, cr *v1alpha1.SnowflakeAccount, op logger.Operation) error {
	log := logger.New(e.log, cr.Namespace, "SnowflakeAccount", cr.Name, op)

	mc, err := e.buildModuleContext(ctx, cr, log)
	if err != nil {
		return log.Handle(err)
	}

	result, _ := e.deps.Pipeline.Apply(ctx, mc) // err always nil today (009)

	persistStatus(cr, mc, e.deps.Config.Snowflake.UsePrivateLink, log) // accountLocator first — shrinks the crash window (010)

	var failureMsg string
	for _, mo := range result.Outcomes {
		if mo.Outcome.Err != nil {
			failureMsg = log.Handle(mo.Outcome.Err).Error() // incident-tracked; retryErr is also this Ready condition's message
		}
	}
	renderReady(cr, result, failureMsg)

	if result.AllDone() {
		cr.Status.SetObservedGeneration(cr.Generation)
	}

	// crossplane-runtime's managed reconciler reverts any in-memory status
	// change made during Create() once it persists the critical
	// external-create-succeeded annotation right afterward (its own
	// UpdateCriticalAnnotations doc comment: "Any other changes made during
	// Create will be reverted when annotations are updated" — a plain
	// Update() on a status-subresource type decodes the API server's
	// still-stale status back over ours). Persisting here, synchronously,
	// before returning, means that later step reads back the real value
	// instead of wiping it — without this, a freshly captured
	// accountLocator never survives a Create() call, and every later
	// reconcile re-attempts CREATE ACCOUNT against the same name forever,
	// hitting a silent, invisible duplicate-name rejection every time
	// (confirmed against a live org while debugging 018).
	if err := e.kube.Status().Update(ctx, cr); err != nil {
		_ = log.Handle(err) // best-effort; the framework's own status update after this call still tries again
	}
	return nil
}

// Create and Update share apply()'s body — see its doc comment.

func (e *external) Create(ctx context.Context, cr *v1alpha1.SnowflakeAccount) (managed.ExternalCreation, error) {
	return managed.ExternalCreation{}, e.apply(ctx, cr, logger.OpCreate)
}

func (e *external) Update(ctx context.Context, cr *v1alpha1.SnowflakeAccount) (managed.ExternalUpdate, error) {
	return managed.ExternalUpdate{}, e.apply(ctx, cr, logger.OpUpdate)
}
