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

package helpers

import (
	"context"
	"fmt"

	"github.com/samber/lo"
	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	instancefilter "github.com/linode/karpenter-provider-linode/pkg/providers/instance/filter"
	"github.com/linode/karpenter-provider-linode/pkg/utils"
)

// FilterInstanceTypes applies common filters to available instance types.
func FilterInstanceTypes(ctx context.Context, instanceTypes []*cloudprovider.InstanceType, nodeClaim *karpv1.NodeClaim) ([]*cloudprovider.InstanceType, error) {
	rejectedInstanceTypes := map[string][]*cloudprovider.InstanceType{}
	reqs := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...)
	for _, filter := range []instancefilter.Filter{
		instancefilter.CompatibleAvailableFilter(reqs, nodeClaim.Spec.Resources.Requests),
	} {
		remaining, rejected := filter.FilterReject(instanceTypes)
		if len(remaining) == 0 {
			return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("all requested instance types were unavailable during launch"))
		}
		if len(rejected) != 0 && filter.Name() != "compatible-available-filter" {
			rejectedInstanceTypes[filter.Name()] = rejected
		}
		instanceTypes = remaining
	}
	for filterName, its := range rejectedInstanceTypes {
		log.FromContext(ctx).WithValues("filter", filterName, "instance-types", utils.PrettySlice(lo.Map(its, func(i *cloudprovider.InstanceType, _ int) string { return i.Name }), 5)).V(1).Info("filtered out instance types from launch")
	}
	return instanceTypes, nil
}

// CheapestInstanceType returns the lowest-price instance type from the set.
func CheapestInstanceType(instanceTypes []*cloudprovider.InstanceType) (*cloudprovider.InstanceType, error) {
	if len(instanceTypes) == 0 {
		return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("no available instance types after filtering"))
	}
	cheapest := instanceTypes[0]
	for _, it := range instanceTypes {
		if it.Offerings.Cheapest().Price < cheapest.Offerings.Cheapest().Price {
			cheapest = it
		}
	}
	return cheapest, nil
}
