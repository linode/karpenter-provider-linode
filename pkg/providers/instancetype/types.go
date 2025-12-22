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

package instancetype

import (
	"fmt"
	"strconv"

	"github.com/linode/linodego"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/utils/resources"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
)

type Resolver interface {
	// CacheKey tells the InstanceType cache if something changes about the InstanceTypes or Offerings based on the NodeClass.
	CacheKey(NodeClass) string
	// Resolve generates an InstanceType based on raw LinodeType and NodeClass setting data
	Resolve(linodego.LinodeType) *cloudprovider.InstanceType
}

type DefaultResolver struct {
	region string
}

func (d DefaultResolver) CacheKey(nodeClass NodeClass) string {
	return nodeClass.GetName()
}

func (d DefaultResolver) Resolve(info linodego.LinodeType) *cloudprovider.InstanceType {
	// !!! Important !!!
	// Any changes to the values passed into the NewInstanceType method will require making updates to the cache key
	// so that Karpenter is able to cache the set of InstanceTypes based on values that alter the set of instance types
	// !!! Important !!!
	return NewInstanceType(
		info,
		d.region,
	)
}

func NewDefaultResolver(region string) *DefaultResolver {
	return &DefaultResolver{
		region: region,
	}
}

func NewInstanceType(
	info linodego.LinodeType,
	region string,
) *cloudprovider.InstanceType {
	it := &cloudprovider.InstanceType{
		Name:         info.ID,
		Requirements: computeRequirements(info, region),
		Capacity:     computeCapacity(info),
	}
	return it
}

//nolint:gocyclo
func computeRequirements(
	info linodego.LinodeType,
	region string,
) scheduling.Requirements {
	requirements := scheduling.NewRequirements(
		// Well Known Upstream
		scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, info.ID),
		scheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, region),
		// Well Known to Linode
		scheduling.NewRequirement(v1.LabelInstanceCPU, corev1.NodeSelectorOpIn, fmt.Sprint(info.VCPUs)),
		scheduling.NewRequirement(v1.LabelInstanceGPUCount, corev1.NodeSelectorOpDoesNotExist),
		scheduling.NewRequirement(v1.LabelInstanceMemory, corev1.NodeSelectorOpIn, fmt.Sprint(info.Memory)),
		scheduling.NewRequirement(v1.LabelInstanceNetworkBandwidth, corev1.NodeSelectorOpDoesNotExist),
	)
	// Network bandwidth, etc
	// TODO: automatically set this value based on Linode type, need to generate a mapping of Linode types to specs

	return requirements
}

func computeCapacity(info linodego.LinodeType) corev1.ResourceList {
	resourceList := corev1.ResourceList{
		corev1.ResourceCPU:    *cpu(info),
		corev1.ResourceMemory: *memory(info),
	}
	return resourceList
}

func cpu(info linodego.LinodeType) *resource.Quantity {
	return resources.Quantity(strconv.Itoa(info.VCPUs))
}

func memory(info linodego.LinodeType) *resource.Quantity {
	return resources.Quantity(fmt.Sprintf("%dMi", info.Memory))
}
