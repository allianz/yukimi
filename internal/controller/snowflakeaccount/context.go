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

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1"
	"github.com/allianz/yukimi/internal/account"
	"github.com/allianz/yukimi/internal/errors"
	"github.com/allianz/yukimi/internal/logger"
)

// buildModuleContext runs this package's entire validation phase — the
// region lookup and its available gate (007) — then fetches the tenant
// namespace's labels and builds the one ModuleContext (009) shared by every
// pipeline call this reconcile makes. No guardrail or quota-admission check
// runs here: 008 and 016 don't exist yet (see specs/018, Key Concept: A
// Pipeline of One).
//
// Returns:
//   - User error if cr.Spec.Region is unknown (007's own error, bubbled
//     unchanged) or known but not available (this package's own error).
//   - System error if the tenant namespace cannot be fetched.
func (e *external) buildModuleContext(ctx context.Context, cr *v1alpha1.SnowflakeAccount, log *logger.Logger) (*account.ModuleContext, error) {
	region, err := e.deps.Backplane.Region(cr.Spec.Region)
	if err != nil {
		return nil, err
	}
	if !region.Available {
		return nil, errors.NewUserError(fmt.Sprintf("region '%s' is not yet available", cr.Spec.Region))
	}

	ns := &corev1.Namespace{}
	if err := e.kube.Get(ctx, client.ObjectKey{Name: cr.Namespace}, ns); err != nil {
		return nil, fmt.Errorf("failed to fetch namespace %s: %w", cr.Namespace, err)
	}

	return account.NewModuleContext(cr, cr.Namespace, region, ns.Labels, log, e.deps.Pool), nil
}
