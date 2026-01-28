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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/awslabs/operatorpkg/status"
)

const (
	ConditionTypeValidationSucceeded = "ValidationSucceeded"
)

// LinodeImage contains resolved LinodeImage selector values utilized for node launch
type LinodeImage struct {
	// ID of the Image
	// +required
	ID string `json:"id"`
	// Deprecation status of the Image
	// +optional
	Deprecated bool `json:"deprecated,omitempty"`
	// Label of the Image
	// +optional
	Label string `json:"label,omitempty"`
	// Requirements used to select the Image
	// +optional
	Requirements []corev1.NodeSelectorRequirement `json:"requirements,omitempty"`

	// TODO: Add more fields as necessary
}

// LinodeNodeClassStatus contains the resolved state of the LinodeNodeClass
type LinodeNodeClassStatus struct {
	// LinodeImage contains the current LinodeImage values that are available to the
	// cluster under the LinodeImage selectors.
	// +optional
	LinodeImages []LinodeImage `json:"linodeImages,omitempty"`
	// Conditions contains signals for health and readiness
	// +optional
	Conditions []status.Condition `json:"conditions,omitempty"`
}

func (in *LinodeNodeClass) LinodeImages() []LinodeImage {
	return in.Status.LinodeImages
}
