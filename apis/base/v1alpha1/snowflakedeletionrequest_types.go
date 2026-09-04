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

package v1alpha1

import (
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/crossplane/crossplane-runtime/v2/apis/common"
	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
)

// SnowflakeDeletionRequestSpec is the deletion request a tenant creates to
// authorize destroying one specific target (design.md 6.1). Nothing here is
// immutable after creation (see Security Considerations in
// specs/019-deletion-request.md).
type SnowflakeDeletionRequestSpec struct {
	TargetRef TargetRef `json:"targetRef"`

	// Maintenance window length, capped at 8h on every write.
	// +kubebuilder:validation:Format=duration
	// +kubebuilder:validation:XValidation:rule="self > duration('0s') && self <= duration('8h')",message="duration must be greater than 0 and at most 8h"
	Duration metav1.Duration `json:"duration"`

	// Audit trail: why this destruction is authorized (design.md 6.2).
	// +kubebuilder:validation:MinLength=1
	Reason string `json:"reason"`

	// The only crossplane-runtime managed-resource field this type carries.
	// No ProviderConfigReference, no WriteConnectionSecretToReference.
	// +optional
	// +kubebuilder:default={"*"}
	ManagementPolicies common.ManagementPolicies `json:"managementPolicies,omitempty"`
}

// TargetRef names the one resource this request authorizes destroying. Name
// is the CRD name, not the resolved Snowflake account name.
type TargetRef struct {
	// +kubebuilder:validation:Enum=SnowflakeAccount
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// SnowflakeDeletionRequestStatus reports this request's time-boxed
// lifecycle. Written only by internal/controller/snowflakedeletionrequest
// and internal/deletion.MarkConsumed.
type SnowflakeDeletionRequestStatus struct {
	xpv1.ResourceStatus `json:",inline"`

	// +optional
	ValidUntil *metav1.Time `json:"validUntil,omitempty"`

	// +kubebuilder:validation:Enum=Active;Expired;Consumed
	// +optional
	State string `json:"state,omitempty"`
}

// +kubebuilder:object:root=true

// A SnowflakeDeletionRequest is the one way to authorize destroying a
// SnowflakeAccount (design.md 6.1-6.3).
// +kubebuilder:printcolumn:name="STATE",type="string",JSONPath=".status.state"
// +kubebuilder:printcolumn:name="VALID-UNTIL",type="string",JSONPath=".status.validUntil"
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
type SnowflakeDeletionRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SnowflakeDeletionRequestSpec   `json:"spec"`
	Status SnowflakeDeletionRequestStatus `json:"status,omitempty"`
}

// The methods below satisfy resource.Managed's Manageable and Conditioned
// interfaces. angryjet's generator does not recognize this type as a
// managed resource — its matcher requires an embedded
// xpv2.ManagedResourceSpec, which this type deliberately omits, identically
// to SnowflakeAccount (006) — so these are hand-written rather than
// generated into a zz_generated.managed.go.

func (r *SnowflakeDeletionRequest) GetManagementPolicies() common.ManagementPolicies {
	return r.Spec.ManagementPolicies
}

func (r *SnowflakeDeletionRequest) SetManagementPolicies(p common.ManagementPolicies) {
	r.Spec.ManagementPolicies = p
}

// GetWriteConnectionSecretToReference/SetWriteConnectionSecretToReference
// satisfy resource.LocalConnectionSecretOwner with a permanent no-op: this
// type carries no such field (Key Concept: Minimal Managed-Resource
// Surface) and never wants a connection secret published. Without these,
// crossplane-runtime v2's reconciler falls through to its default case
// after every successful Observe and reports a permanent ReconcileError
// ("managed resource does not implement connection details"), which pins
// the Synced condition to False forever. Implementing the interface routes
// PublishConnection/UnpublishConnection into APILocalSecretPublisher's own
// nil-ref no-op instead.
func (r *SnowflakeDeletionRequest) GetWriteConnectionSecretToReference() *xpv1.LocalSecretReference {
	return nil
}

func (r *SnowflakeDeletionRequest) SetWriteConnectionSecretToReference(_ *xpv1.LocalSecretReference) {
}

func (r *SnowflakeDeletionRequest) GetCondition(ct xpv1.ConditionType) xpv1.Condition {
	return r.Status.GetCondition(ct)
}

func (r *SnowflakeDeletionRequest) SetConditions(c ...xpv1.Condition) {
	r.Status.SetConditions(c...)
}

var _ resource.Managed = &SnowflakeDeletionRequest{}

// +kubebuilder:object:root=true

// SnowflakeDeletionRequestList contains a list of SnowflakeDeletionRequest
type SnowflakeDeletionRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SnowflakeDeletionRequest `json:"items"`
}

// SnowflakeDeletionRequest type metadata.
var (
	SnowflakeDeletionRequestKind             = reflect.TypeOf(SnowflakeDeletionRequest{}).Name()
	SnowflakeDeletionRequestGroupKind        = schema.GroupKind{Group: Group, Kind: SnowflakeDeletionRequestKind}.String()
	SnowflakeDeletionRequestKindAPIVersion   = SnowflakeDeletionRequestKind + "." + SchemeGroupVersion.String()
	SnowflakeDeletionRequestGroupVersionKind = SchemeGroupVersion.WithKind(SnowflakeDeletionRequestKind)
)

func init() {
	SchemeBuilder.Register(&SnowflakeDeletionRequest{}, &SnowflakeDeletionRequestList{})
}
