/*
Copyright 2026.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RunnerSetSpec defines the desired state of RunnerSet
type RunnerSetSpec struct {
	// AutoscaleSetRef is a reference to the AutoscaleSet
	// +kubebuilder:validation:Required
	AutoscaleSetRef AutoscaleSetRef `json:"autoscaleSetRef"`

	// RunnerGroup is the runner group this set belongs to
	// +kubebuilder:validation:Required
	RunnerGroup string `json:"runnerGroup"`

	// RunnerTemplate is the pod template for creating runners
	// This will be merged with the AutoscaleSet template if both are specified,
	// with RunnerSet values taking precedence. If not specified, the AutoscaleSet
	// template will be used.
	RunnerTemplate *RunnerPodTemplate `json:"runnerTemplate,omitempty"`
}

// AutoscaleSetRef references an AutoscaleSet
type AutoscaleSetRef struct {
	// Name is the name of the AutoscaleSet
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Namespace is the namespace of the AutoscaleSet (defaults to RunnerSet namespace)
	Namespace string `json:"namespace,omitempty"`
}

// RunnerSetStatus defines the observed state of RunnerSet
type RunnerSetStatus struct {
	// RunnerCount is the current number of runners
	RunnerCount int32 `json:"runnerCount,omitempty"`

	// IdleRunners is the number of idle runners
	IdleRunners int32 `json:"idleRunners,omitempty"`

	// BusyRunners is the number of busy runners
	BusyRunners int32 `json:"busyRunners,omitempty"`

	// Conditions represent the latest available observations
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastMessageID is the last processed message ID
	LastMessageID int `json:"lastMessageID,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:categories=github;scaleset,shortName=rns
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="RunnerGroup",type="string",JSONPath=".spec.runnerGroup",description="Runner group name"
// +kubebuilder:printcolumn:name="Total",type="integer",JSONPath=".status.runnerCount",description="Total runner pods"
// +kubebuilder:printcolumn:name="Idle",type="integer",JSONPath=".status.idleRunners",description="Idle runners"
// +kubebuilder:printcolumn:name="Busy",type="integer",JSONPath=".status.busyRunners",description="Busy runners"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:selectablefield:JSONPath=".spec.runnerGroup"

// RunnerSet is the Schema for the runnersets API
type RunnerSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RunnerSetSpec   `json:"spec,omitempty"`
	Status            RunnerSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RunnerSetList contains a list of RunnerSet
type RunnerSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RunnerSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RunnerSet{}, &RunnerSetList{})
}
