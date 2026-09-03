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

// SnowflakeAccountSpec defines the desired state of a SnowflakeAccount. Every
// field is a direct sibling under spec, matching design.md 3.1's example
// exactly — there is no forProvider wrapper (Key Concept: Minimal
// Managed-Resource Surface).
type SnowflakeAccountSpec struct {
	// +optional
	Description string `json:"description,omitempty"`

	// +optional
	Contacts []string `json:"contacts,omitempty"`

	// Immutable after creation (design.md 3.11.3).
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="region is immutable"
	Region string `json:"region"`

	// Immutable after creation (design.md 3.11.3); selects the Guardrails
	// baseline (008).
	// +kubebuilder:validation:Enum=dev;prod
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="environment is immutable"
	Environment string `json:"environment"`

	// This account's share of the namespace's monthly credit allowance
	// (design.md 3.10). Ceiling enforcement is Guardrails'/Quota's job (008/011).
	// +optional
	CreditQuota int32 `json:"creditQuota,omitempty"`

	IdentityIntegration IdentityIntegration `json:"identityIntegration"`

	// +optional
	CustomNetworkRules *CustomNetworkRules `json:"customNetworkRules,omitempty"`

	// +optional
	CustomAuthRules *CustomAuthRules `json:"customAuthRules,omitempty"`

	// The only crossplane-runtime managed-resource field this type carries.
	// No ProviderConfigReference, no WriteConnectionSecretToReference.
	// +optional
	// +kubebuilder:default={"*"}
	ManagementPolicies common.ManagementPolicies `json:"managementPolicies,omitempty"`
}

// IdentityIntegration is design.md 3.1/3.7's identityIntegration block.
type IdentityIntegration struct {
	// Keyed by integration (e.g. "giam"); the key is free-form, not schema.
	// +optional
	Groups map[string][]string `json:"groups,omitempty"`

	// Must contain an ACCOUNTADMIN entry (design.md 3.7).
	// +kubebuilder:validation:XValidation:rule="'ACCOUNTADMIN' in self",message="roleBindings must bind ACCOUNTADMIN"
	RoleBindings map[string]string `json:"roleBindings"`
}

// CustomNetworkRules is design.md 3.1/3.8's customNetworkRules block.
type CustomNetworkRules struct {
	// +optional
	ServiceUsers map[string][]NetworkRule `json:"serviceUsers,omitempty"`

	// +optional
	AccountWide []NetworkRule `json:"accountWide,omitempty"`
}

// NetworkRule is one entry under customNetworkRules (design.md 3.8).
type NetworkRule struct {
	// An inventory connection name from the region's Backplane Config (007);
	// resolved, not validated, here.
	Connection string `json:"connection"`

	// +optional
	AllowedIPs []string `json:"allowedIPs,omitempty"`
}

// CustomAuthRules is design.md 3.1/3.9's customAuthRules block.
type CustomAuthRules struct {
	// +optional
	Exceptions []AuthException `json:"exceptions,omitempty"`
}

// AuthException is one entry under customAuthRules.exceptions (design.md 3.9).
// +kubebuilder:validation:XValidation:rule="self.rsaKeyAllowed || self.patAllowed",message="exception must permit at least one of rsaKeyAllowed or patAllowed"
type AuthException struct {
	User string `json:"user"`

	// +optional
	RSAKeyAllowed bool `json:"rsaKeyAllowed,omitempty"`

	// +optional
	PATAllowed bool `json:"patAllowed,omitempty"`

	// Audit only; never carried into Snowflake.
	Reason string `json:"reason"`
}

// SnowflakeAccountStatus defines the observed state of a SnowflakeAccount.
type SnowflakeAccountStatus struct {
	xpv1.ResourceStatus `json:",inline"`

	// The resolved Snowflake account name (design.md 3.12) — not
	// metadata.name.
	// +optional
	AccountName string `json:"accountName,omitempty"`

	// Captured from CREATE ACCOUNT's result (012).
	// +optional
	AccountLocator string `json:"accountLocator,omitempty"`

	// Built via internal/account/tenant.AccountURL (design.md 7.2).
	// +optional
	AccountURL string `json:"accountUrl,omitempty"`
}

// +kubebuilder:object:root=true

// A SnowflakeAccount is the resource a team commits to Git to describe the
// Snowflake account they want (design.md 3.1).
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
type SnowflakeAccount struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SnowflakeAccountSpec   `json:"spec"`
	Status SnowflakeAccountStatus `json:"status,omitempty"`
}

// The four methods below satisfy resource.Managed's Manageable and
// Conditioned interfaces. angryjet's generator does not recognize this type
// as a managed resource — its matcher requires an embedded
// xpv2.ManagedResourceSpec, which this type deliberately omits (Key Concept:
// Minimal Managed-Resource Surface) — so these are hand-written rather than
// generated into a zz_generated.managed.go. Go only promotes methods through
// anonymous (embedded) fields, and Spec/Status are named fields, so these
// must forward explicitly even though ManagementPolicies and
// ConditionedStatus are reachable one level down.

func (a *SnowflakeAccount) GetManagementPolicies() common.ManagementPolicies {
	return a.Spec.ManagementPolicies
}

func (a *SnowflakeAccount) SetManagementPolicies(p common.ManagementPolicies) {
	a.Spec.ManagementPolicies = p
}

func (a *SnowflakeAccount) GetCondition(ct xpv1.ConditionType) xpv1.Condition {
	return a.Status.GetCondition(ct)
}

func (a *SnowflakeAccount) SetConditions(c ...xpv1.Condition) {
	a.Status.SetConditions(c...)
}

var _ resource.Managed = &SnowflakeAccount{}

// +kubebuilder:object:root=true

// SnowflakeAccountList contains a list of SnowflakeAccount
type SnowflakeAccountList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SnowflakeAccount `json:"items"`
}

// SnowflakeAccount type metadata.
var (
	SnowflakeAccountKind             = reflect.TypeOf(SnowflakeAccount{}).Name()
	SnowflakeAccountGroupKind        = schema.GroupKind{Group: Group, Kind: SnowflakeAccountKind}.String()
	SnowflakeAccountKindAPIVersion   = SnowflakeAccountKind + "." + SchemeGroupVersion.String()
	SnowflakeAccountGroupVersionKind = SchemeGroupVersion.WithKind(SnowflakeAccountKind)
)

func init() {
	SchemeBuilder.Register(&SnowflakeAccount{}, &SnowflakeAccountList{})
}
