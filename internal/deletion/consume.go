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

package deletion

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/allianz/yukimi/apis/base/v1alpha1"
)

const stateConsumed = "Consumed"

// MarkConsumed transitions req's status.state to Consumed. Its
// status.validUntil is left untouched: once state is terminal, the
// SnowflakeDeletionRequest controller stops recomputing it from
// spec.duration, so it freezes at whatever value was already there. Called
// by 020 after a successful DROP ACCOUNT.
//
// Returns: system error if the status update against the Kubernetes API
// fails.
func MarkConsumed(ctx context.Context, c client.Client, req *v1alpha1.SnowflakeDeletionRequest) error {
	req.Status.State = stateConsumed
	if err := c.Status().Update(ctx, req); err != nil {
		return fmt.Errorf("failed to mark SnowflakeDeletionRequest %s/%s as consumed: %w", req.Namespace, req.Name, err)
	}
	return nil
}
