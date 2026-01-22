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
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/linode/linodego"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/utils/resources"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
)

const (
	MemoryAvailable = "memory.available"
)

type Resolver interface {
	// CacheKey tells the InstanceType cache if something changes about the InstanceTypes or Offerings based on the NodeClass.
	CacheKey(NodeClass) string
	// Resolve generates an InstanceType based on raw LinodeType and NodeClass setting data
	Resolve(ctx context.Context, info linodego.LinodeType, nodeClass NodeClass) *cloudprovider.InstanceType
}

type DefaultResolver struct {
	region string
}

func (d DefaultResolver) CacheKey(nodeClass NodeClass) string {
	return nodeClass.GetName()
}

func (d DefaultResolver) Resolve(ctx context.Context, info linodego.LinodeType, nodeClass NodeClass) *cloudprovider.InstanceType {
	// !!! Important !!!
	// Any changes to the values passed into the NewInstanceType method will require making updates to the cache key
	// so that Karpenter is able to cache the set of InstanceTypes based on values that alter the set of instance types
	// !!! Important !!!
	kc := &v1.KubeletConfiguration{}
	if resolved := nodeClass.KubeletConfiguration(); resolved != nil {
		kc = resolved
	}
	return NewInstanceType(
		ctx,
		info,
		d.region,
		kc.MaxPods,
		kc.PodsPerCore,
		kc.KubeReserved,
		kc.SystemReserved,
		kc.EvictionHard,
		kc.EvictionSoft,
	)
}

func NewDefaultResolver(region string) *DefaultResolver {
	return &DefaultResolver{
		region: region,
	}
}

func NewInstanceType(
	ctx context.Context,
	info linodego.LinodeType,
	region string,
	maxPods *int32,
	podsPerCore *int32,
	kubeReserved map[string]string,
	systemReserved map[string]string,
	evictionHard map[string]string,
	evictionSoft map[string]string,
) *cloudprovider.InstanceType {
	it := &cloudprovider.InstanceType{
		Name:         info.ID,
		Requirements: computeRequirements(info, region),
		Capacity:     computeCapacity(ctx, info, maxPods, podsPerCore),
		Overhead: &cloudprovider.InstanceTypeOverhead{
			KubeReserved:      kubeReservedResources(cpu(info), pods(info, maxPods, podsPerCore), kubeReserved),
			SystemReserved:    systemReservedResources(systemReserved),
			EvictionThreshold: evictionThreshold(memory(ctx, info), evictionHard, evictionSoft),
		},
	}
	return it
}

func systemReservedResources(systemReserved map[string]string) corev1.ResourceList {
	return lo.MapEntries(systemReserved, func(k string, v string) (corev1.ResourceName, resource.Quantity) {
		return corev1.ResourceName(k), resource.MustParse(v)
	})
}

func kubeReservedResources(cpus, pods *resource.Quantity, kubeReserved map[string]string) corev1.ResourceList {
	resources := corev1.ResourceList{
		corev1.ResourceMemory:           resource.MustParse(fmt.Sprintf("%dMi", (11*pods.Value())+255)),
		corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"), // default kube-reserved ephemeral-storage
	}
	// kube-reserved Computed from
	// https://github.com/bottlerocket-os/bottlerocket/pull/1388/files#diff-bba9e4e3e46203be2b12f22e0d654ebd270f0b478dd34f40c31d7aa695620f2fR611
	for _, cpuRange := range []struct {
		start      int64
		end        int64
		percentage float64
	}{
		{start: 0, end: 1000, percentage: 0.06},
		{start: 1000, end: 2000, percentage: 0.01},
		{start: 2000, end: 4000, percentage: 0.005},
		{start: 4000, end: 1 << 31, percentage: 0.0025},
	} {
		if cpu := cpus.MilliValue(); cpu >= cpuRange.start {
			r := float64(cpuRange.end - cpuRange.start)
			if cpu < cpuRange.end {
				r = float64(cpu - cpuRange.start)
			}
			cpuOverhead := resources.Cpu()
			cpuOverhead.Add(*resource.NewMilliQuantity(int64(r*cpuRange.percentage), resource.DecimalSI))
			resources[corev1.ResourceCPU] = *cpuOverhead
		}
	}
	return lo.Assign(resources, lo.MapEntries(kubeReserved, func(k string, v string) (corev1.ResourceName, resource.Quantity) {
		return corev1.ResourceName(k), resource.MustParse(v)
	}))
}

func evictionThreshold(memory *resource.Quantity, evictionHard map[string]string, evictionSoft map[string]string) corev1.ResourceList {
	overhead := corev1.ResourceList{
		corev1.ResourceMemory: resource.MustParse("100Mi"),
	}

	override := corev1.ResourceList{}
	var evictionSignals []map[string]string
	if evictionHard != nil {
		evictionSignals = append(evictionSignals, evictionHard)
	}
	if evictionSoft != nil {
		evictionSignals = append(evictionSignals, evictionSoft)
	}

	for _, m := range evictionSignals {
		temp := corev1.ResourceList{}
		if v, ok := m[MemoryAvailable]; ok {
			temp[corev1.ResourceMemory] = computeEvictionSignal(*memory, v)
		}
		override = resources.MaxResources(override, temp)
	}
	// Assign merges maps from left to right so overrides will always be taken last
	return lo.Assign(overhead, override)
}

// computeEvictionSignal computes the resource quantity value for an eviction signal value, computed off the
// base capacity value if the signal value is a percentage or as a resource quantity if the signal value isn't a percentage
func computeEvictionSignal(capacity resource.Quantity, signalValue string) resource.Quantity {
	if strings.HasSuffix(signalValue, "%") {
		p := mustParsePercentage(signalValue)

		// Calculation is node.capacity * signalValue if percentage
		// From https://kubernetes.io/docs/concepts/scheduling-eviction/node-pressure-eviction/#eviction-signals
		return resource.MustParse(fmt.Sprint(math.Ceil(capacity.AsApproximateFloat64() / 100 * p)))
	}
	return resource.MustParse(signalValue)
}

func mustParsePercentage(v string) float64 {
	p, err := strconv.ParseFloat(strings.Trim(v, "%"), 64)
	if err != nil {
		panic(fmt.Sprintf("expected percentage value to be a float but got %s, %v", v, err))
	}
	// Setting percentage value to 100% is considered disabling the threshold according to
	// https://kubernetes.io/docs/reference/config-api/kubelet-config.v1beta1/
	if p == 100 {
		p = 0
	}
	return p
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
		// Arch and OS are currently fixed for Linode instances
		scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "amd64"),
		scheduling.NewRequirement(corev1.LabelOSStable, corev1.NodeSelectorOpIn, "linux"),
		// Well Known to Karpenter
		// We only support on-demand capacity types for now
		scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
		// Well Known to Linode
		scheduling.NewRequirement(v1.LabelInstanceCPU, corev1.NodeSelectorOpIn, fmt.Sprint(info.VCPUs)),
		scheduling.NewRequirement(v1.LabelInstanceMemory, corev1.NodeSelectorOpIn, fmt.Sprint(info.Memory)),
		scheduling.NewRequirement(v1.LabelInstanceNetworkBandwidth, corev1.NodeSelectorOpIn, fmt.Sprint(info.Transfer)),
	)
	if info.GPUs > 0 {
		requirements.Add(
			scheduling.NewRequirement(v1.LabelInstanceGPUCount, corev1.NodeSelectorOpIn, fmt.Sprint(info.GPUs)),
		)
	}
	if info.AcceleratedDevices > 0 {
		requirements.Add(
			scheduling.NewRequirement(v1.LabelInstanceAcceleratedDevicesCount, corev1.NodeSelectorOpIn, fmt.Sprint(info.AcceleratedDevices)),
		)
	}
	// Linode instance types are generally formatted as <generation>-<class>-<memory>(-<cpu>), e.g. g6-standard-4, g8-dedicated-128-64
	// Exceptions to this naming convention are GPU and accelerated plan types (e.g. g1-accelerated-netint-vpu-t1u1-m, g1-gpu-rtx6000-1)
	// For generation, we can safely assume it's the first part of the instance type ID
	instanceTypeParts := strings.Split(info.ID, "-")
	if len(instanceTypeParts) >= 2 {
		requirements.Add(
			scheduling.NewRequirement(v1.LabelInstanceGeneration, corev1.NodeSelectorOpIn, instanceTypeParts[0]),
		)
	}

	return requirements
}

func computeCapacity(ctx context.Context, info linodego.LinodeType, maxPods, podsPerCore *int32) corev1.ResourceList {
	resourceList := corev1.ResourceList{
		corev1.ResourceCPU:    *cpu(info),
		corev1.ResourceMemory: *memory(ctx, info),
		corev1.ResourcePods:   *pods(info, maxPods, podsPerCore),
	}
	return resourceList
}

func cpu(info linodego.LinodeType) *resource.Quantity {
	return resources.Quantity(strconv.Itoa(info.VCPUs))
}

func memory(ctx context.Context, info linodego.LinodeType) *resource.Quantity {
	sizeInMib := info.Memory
	mem := resources.Quantity(fmt.Sprintf("%dMi", sizeInMib))
	// Account for VM overhead in calculation
	overheadInMiB := int64(math.Ceil(float64(mem.Value()) * options.FromContext(ctx).VMMemoryOverheadPercent / 1024 / 1024))
	mem.Sub(resource.MustParse(fmt.Sprintf("%dMi", overheadInMiB)))
	return mem
}

func pods(info linodego.LinodeType, maxPods, podsPerCore *int32) *resource.Quantity {
	var count int64
	switch {
	case maxPods != nil:
		count = int64(lo.FromPtr(maxPods))
	default:
		count = 110
	}
	if lo.FromPtr(podsPerCore) > 0 {
		count = lo.Min([]int64{int64(lo.FromPtr(podsPerCore)) * int64(info.VCPUs), count})
	}
	return resources.Quantity(fmt.Sprint(count))
}
