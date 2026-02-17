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

package filter

import (
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/utils/resources"
)

type Filter interface {
	FilterReject(instanceTypes []*cloudprovider.InstanceType) (kept, rejected []*cloudprovider.InstanceType)
	Name() string
}

// CompatibleAvailableFilter removes instance types which do not have any compatible, available offerings. Other filters
// should not be used without first using this filter.
func CompatibleAvailableFilter(requirements scheduling.Requirements, requests corev1.ResourceList) Filter {
	return compatibleAvailableFilter{
		requirements: requirements,
		requests:     requests,
	}
}

type compatibleAvailableFilter struct {
	requirements scheduling.Requirements
	requests     corev1.ResourceList
}

func (f compatibleAvailableFilter) FilterReject(instanceTypes []*cloudprovider.InstanceType) (kept, rejected []*cloudprovider.InstanceType) {
	return lo.FilterReject(instanceTypes, func(i *cloudprovider.InstanceType, _ int) bool {
		if !f.requirements.IsCompatible(i.Requirements, scheduling.AllowUndefinedWellKnownLabels) {
			return false
		}
		if !resources.Fits(f.requests, i.Allocatable()) {
			return false
		}
		if len(i.Offerings.Compatible(f.requirements).Available()) == 0 {
			return false
		}
		return true
	})
}

func (compatibleAvailableFilter) Name() string {
	return "compatible-available-filter"
}
