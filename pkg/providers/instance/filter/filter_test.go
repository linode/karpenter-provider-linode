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

package filter_test

import (
	"context"
	"math/rand/v2"
	"testing"

	"github.com/awslabs/operatorpkg/option"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	"github.com/linode/karpenter-provider-linode/pkg/providers/instance/filter"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/karpenter/pkg/utils/testing"
)

var ctx context.Context

func TestLinode(t *testing.T) {
	t.Parallel()
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "FilterTest")
}

var _ = Describe("InstanceFiltersTest", func() {
	Context("CompatibleAvailableFilter", func() {
		It("should filter compatible instances (by requirements)", func() {
			f := filter.CompatibleAvailableFilter(scheduling.NewRequirements(scheduling.NewRequirement(
				corev1.LabelTopologyRegion,
				corev1.NodeSelectorOpIn,
				"us-east",
			)), corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1000m"),
			})
			kept, rejected := f.FilterReject([]*cloudprovider.InstanceType{
				makeInstanceType(
					"compatible-instance",
					withRequirements(scheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, "us-east")),
					withResource(resource.MustParse("2000m")),
					withOfferings(makeOffering(true, withZone("us-east"))),
				),
				makeInstanceType(
					"incompatible-instance",
					withRequirements(scheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, "us-west")),
					withResource(resource.MustParse("2000m")),
					withOfferings(makeOffering(true, withZone("us-west"))),
				),
			})
			expectInstanceTypes(kept, "compatible-instance")
			expectInstanceTypes(rejected, "incompatible-instance")
		})
		It("should filter compatible instances (by requests)", func() {
			f := filter.CompatibleAvailableFilter(scheduling.NewRequirements(scheduling.NewRequirement(
				corev1.LabelTopologyRegion,
				corev1.NodeSelectorOpIn,
				"us-east",
			)), corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1000m"),
			})
			kept, rejected := f.FilterReject([]*cloudprovider.InstanceType{
				makeInstanceType(
					"compatible-instance",
					withRequirements(scheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, "us-east")),
					withResource(resource.MustParse("2000m")),
					withOfferings(makeOffering(true, withZone("us-east"))),
				),
				makeInstanceType(
					"incompatible-instance",
					withRequirements(scheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, "us-east")),
					withResource(resource.MustParse("500m")),
					withOfferings(makeOffering(true, withZone("us-east"))),
				),
			})
			expectInstanceTypes(kept, "compatible-instance")
			expectInstanceTypes(rejected, "incompatible-instance")
		})
		It("should filter available instances", func() {
			f := filter.CompatibleAvailableFilter(scheduling.NewRequirements(scheduling.NewRequirement(
				corev1.LabelTopologyRegion,
				corev1.NodeSelectorOpIn,
				"us-east",
			)), corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1000m"),
			})
			kept, rejected := f.FilterReject([]*cloudprovider.InstanceType{
				makeInstanceType(
					"available-instance",
					withRequirements(scheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, "us-east")),
					withResource(resource.MustParse("2000m")),
					withOfferings(
						makeOffering(true, withZone("us-east")),
					),
				),
				makeInstanceType(
					"unavailable-instance",
					withRequirements(scheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, "us-east")),
					withResource(resource.MustParse("2000m")),
					withOfferings(
						makeOffering(false, withZone("us-east")),
					),
				),
			})
			expectInstanceTypes(kept, "available-instance")
			expectInstanceTypes(rejected, "unavailable-instance")
		})
	})
})

func expectInstanceTypes(instanceTypes []*cloudprovider.InstanceType, names ...string) {
	GinkgoHelper()
	resolvedNames := lo.Map(instanceTypes, func(it *cloudprovider.InstanceType, _ int) string {
		return it.Name
	})
	Expect(resolvedNames).To(ConsistOf(lo.Map(names, func(n string, _ int) any { return n })...))
}

type mockInstanceTypeOptions = option.Function[cloudprovider.InstanceType]

func withRequirements(reqs ...*scheduling.Requirement) mockInstanceTypeOptions {
	return func(it *cloudprovider.InstanceType) {
		if it.Requirements == nil {
			it.Requirements = scheduling.NewRequirements()
		}
		it.Requirements.Add(reqs...)
	}
}

func withResource(quantity resource.Quantity) mockInstanceTypeOptions {
	return func(it *cloudprovider.InstanceType) {
		if it.Capacity == nil {
			it.Capacity = corev1.ResourceList{}
		}
		it.Capacity[corev1.ResourceCPU] = quantity
	}
}

func withOfferings(offerings ...*cloudprovider.Offering) mockInstanceTypeOptions {
	return func(it *cloudprovider.InstanceType) {
		it.Offerings = offerings
	}
}

func makeInstanceType(name string, opts ...mockInstanceTypeOptions) *cloudprovider.InstanceType {
	instanceType := option.Resolve(opts...)
	rand.Shuffle(len(instanceType.Offerings), func(i, j int) {
		instanceType.Offerings[i], instanceType.Offerings[j] = instanceType.Offerings[j], instanceType.Offerings[i]
	})
	instanceType.Name = name
	instanceType.Overhead = &cloudprovider.InstanceTypeOverhead{
		KubeReserved:      corev1.ResourceList{},
		SystemReserved:    corev1.ResourceList{},
		EvictionThreshold: corev1.ResourceList{},
	}
	return instanceType
}

type mockOfferingOptions = option.Function[cloudprovider.Offering]

func withZone(zone string) mockOfferingOptions {
	return func(o *cloudprovider.Offering) {
		if o.Requirements == nil {
			o.Requirements = scheduling.NewRequirements()
		}
		o.Requirements.Add(scheduling.NewRequirement(
			corev1.LabelTopologyRegion,
			corev1.NodeSelectorOpIn,
			zone,
		))
	}
}

func makeOffering(available bool, opts ...mockOfferingOptions) *cloudprovider.Offering {
	offering := option.Resolve(opts...)
	if offering.Requirements == nil {
		offering.Requirements = scheduling.NewRequirements()
	}
	offering.Available = available
	return offering
}
