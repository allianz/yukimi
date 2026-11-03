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
	"maps"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// EnsureNamespace creates the namespace of the scale test with the labels the
// controller reads. If it exists, only missing or changed labels are set; nothing
// else about it is touched.
func (e *Env) EnsureNamespace(ctx context.Context) error {
	var ns corev1.Namespace
	err := e.Kube.Get(ctx, types.NamespacedName{Name: Namespace}, &ns)
	if apierrors.IsNotFound(err) {
		ns = corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace, Labels: maps.Clone(namespaceLabels)}}
		if err := e.Kube.Create(ctx, &ns); err != nil {
			return fmt.Errorf("creating namespace %q: %w", Namespace, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading namespace %q: %w", Namespace, err)
	}

	changed := false
	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	for k, v := range namespaceLabels {
		if ns.Labels[k] != v {
			ns.Labels[k] = v
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := e.Kube.Update(ctx, &ns); err != nil {
		return fmt.Errorf("labeling namespace %q: %w", Namespace, err)
	}
	return nil
}
