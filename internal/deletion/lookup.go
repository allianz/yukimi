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

const stateActive = "Active"

// FindActivePermit returns the Active DeletionPermit in
// namespace whose spec.targetRef matches targetKind/targetName, or nil if
// none exists. Trusts status.state as authoritative — performs no
// independent validUntil check. When more than one Active candidate
// matches, returns the one with the earliest creationTimestamp.
//
// Returns: system error if the list call against the Kubernetes API fails.
// Never a user error — there is nothing about the caller's input a tenant
// could fix here.
func FindActivePermit(ctx context.Context, c client.Client, namespace, targetKind, targetName string) (*v1alpha1.DeletionPermit, error) {
	var list v1alpha1.DeletionPermitList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list DeletionPermit objects: %w", err)
	}

	var earliest *v1alpha1.DeletionPermit
	for i := range list.Items {
		permit := &list.Items[i]
		if permit.Status.State != stateActive {
			continue
		}
		if permit.Spec.TargetRef.Kind != targetKind || permit.Spec.TargetRef.Name != targetName {
			continue
		}
		if earliest == nil || permit.CreationTimestamp.Before(&earliest.CreationTimestamp) {
			earliest = permit
		}
	}

	return earliest, nil
}
