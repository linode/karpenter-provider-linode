/*
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

package v1

import (
	"fmt"

	"github.com/awslabs/operatorpkg/status"
	"github.com/linode/linodego"
	"github.com/mitchellh/hashstructure/v2"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LKENodeClassSpec defines the desired state for LKE-managed node pools.
// This is a simplified spec compared to LinodeNodeClass since LKE manages
// most instance configuration (image, network interfaces, placement, etc.).
type LKENodeClassSpec struct {
	// tags is a list of tags to apply to the LKE node pool.
	// +optional
	// +listType=set
	Tags []string `json:"tags,omitempty"`

	// labels is a map of Kubernetes node labels to apply to the Node.
	// +optional
	Labels linodego.LKENodePoolLabels `json:"labels,omitempty"`

	// firewallID is the ID of the cloud firewall to apply to the node pool.
	// +optional
	FirewallID *int `json:"firewallID,omitempty"`

	// diskEncryption determines if the disks of the instance should be encrypted.
	// This is only available for Enterprise LKE clusters.
	// +optional
	// +kubebuilder:validation:Enum=enabled;disabled
	DiskEncryption linodego.InstanceDiskEncryption `json:"diskEncryption,omitempty"`

	// k8sVersion is the Kubernetes version for the node pool.
	// This is only available for Enterprise LKE clusters.
	// +optional
	K8sVersion *string `json:"k8sVersion,omitempty"`

	// updateStrategy defines how nodes are updated when the node pool is modified.
	// Valid values are "rolling_update" and "on_recycle".
	// +optional
	// +kubebuilder:validation:Enum=rolling_update;on_recycle
	UpdateStrategy *linodego.LKENodePoolUpdateStrategy `json:"updateStrategy,omitempty"`
}

// LKENodeClass is the Schema for the LKENodeClass API
// +kubebuilder:object:root=true
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status",description=""
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description=""
// +kubebuilder:resource:path=lkenodeclasses,scope=Cluster,categories=karpenter,shortName={lkenc,lkencs}
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
type LKENodeClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LKENodeClassSpec   `json:"spec,omitempty"`
	Status LKENodeClassStatus `json:"status,omitempty"`
}

// LKENodeClassHashVersion should be bumped when:
// 1. A field changes its default value for an existing field that is already hashed
// 2. A field is added to the hash calculation with an already-set value
// 3. A field is removed from the hash calculations
const LKENodeClassHashVersion = "v1"

func (in *LKENodeClass) Hash() string {
	return fmt.Sprint(lo.Must(hashstructure.Hash([]interface{}{
		in.Spec,
	}, hashstructure.FormatV2, &hashstructure.HashOptions{
		SlicesAsSets:    true,
		IgnoreZeroValue: true,
		ZeroNil:         true,
	})))
}

func (in *LKENodeClass) GetConditions() []status.Condition {
	return in.Status.Conditions
}

func (in *LKENodeClass) SetConditions(conditions []status.Condition) {
	in.Status.Conditions = conditions
}

func (in *LKENodeClass) StatusConditions() status.ConditionSet {
	return status.NewReadyConditions().For(in)
}

// +kubebuilder:object:root=true

// LKENodeClassList contains a list of LKENodeClass
type LKENodeClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LKENodeClass `json:"items"`
}
