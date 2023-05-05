/*
Copyright 2021.

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

package v1beta1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ProvisionApplicationSpec defines the desired state of ProvisionApplication
type ProvisionApplicationSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	Selector metav1.LabelSelector `json:"selector,omitempty"`

	Consumers []Consumer `json:"consumers,omitempty"`
	Producers []Producer `json:"producers,omitempty"`
}

type Consumer struct {
	Competing bool              `json:"competing,omitempty"`
	Backlog   resource.Quantity `json:"backlog,omitempty"`
	Addresses []string          `json:"addresses,omitempty"`
	Roles     []string          `json:"roles,omitempty"`
}

type Producer struct {
	Addresses []string `json:"addresses,omitempty"`
	Roles     []string `json:"roles,omitempty"`
}

// ProvisionApplicationStatus defines the observed state of ProvisionApplication
type ProvisionApplicationStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// ProvisionApplication is the Schema for the provisionapplications API
type ProvisionApplication struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ProvisionApplicationSpec   `json:"spec,omitempty"`
	Status ProvisionApplicationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ProvisionApplicationList contains a list of ProvisionApplication
type ProvisionApplicationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProvisionApplication `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ProvisionApplication{}, &ProvisionApplicationList{})
}
