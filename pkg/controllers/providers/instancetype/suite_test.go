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

package instancetype_test

import (
	"context"
	"testing"

	"github.com/linode/linodego"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
	coretest "sigs.k8s.io/karpenter/pkg/test"
	"sigs.k8s.io/karpenter/pkg/test/v1alpha1"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	controllersinstancetype "github.com/linode/karpenter-provider-linode/pkg/controllers/providers/instancetype"
	"github.com/linode/karpenter-provider-linode/pkg/fake"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/test"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
	. "sigs.k8s.io/karpenter/pkg/utils/testing"
)

var ctx context.Context
var stop context.CancelFunc
var env *coretest.Environment
var linodeEnv *test.Environment
var controller *controllersinstancetype.Controller

func TestLinode(t *testing.T) {
	t.Parallel()
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "InstanceType")
}

var _ = BeforeSuite(func() {
	env = coretest.NewEnvironment(coretest.WithCRDs(apis.CRDs...), coretest.WithCRDs(v1alpha1.CRDs...))
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: lo.ToPtr(true)}}))
	ctx = options.ToContext(ctx, test.Options())
	ctx, stop = context.WithCancel(ctx)
	linodeEnv = test.NewEnvironment(ctx)
	controller = controllersinstancetype.NewController(linodeEnv.InstanceTypesProvider)
})

var _ = AfterSuite(func() {
	stop()
	Expect(env.Stop()).To(Succeed(), "Failed to stop environment")
})

var _ = BeforeEach(func() {
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: lo.ToPtr(true)}}))
	ctx = options.ToContext(ctx, test.Options())

	linodeEnv.Reset()
})

var _ = AfterEach(func() {
	ExpectCleanedUp(ctx, env.Client)
})

var _ = Describe("InstanceType", func() {
	It("should update instance type date with response from the ListTypes API", func() {
		linodeInstanceTypeInfo := fake.MakeInstances()
		linodeOfferings := fake.MakeInstanceOfferings(linodeInstanceTypeInfo)
		linodeEnv.LinodeAPI.ListTypesOutput.Set(&linodeInstanceTypeInfo)
		linodeEnv.LinodeAPI.ListRegionsAvailabilityOutput.Set(&linodeOfferings)

		ExpectSingletonReconciled(ctx, controller)
		instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, &v1.LinodeNodeClass{})
		Expect(err).To(BeNil())

		Expect(instanceTypes).To(HaveLen(len(linodeInstanceTypeInfo)))
		for _, it := range instanceTypes {
			Expect(lo.ContainsBy(linodeInstanceTypeInfo, func(i linodego.LinodeType) bool { return i.ID == it.Name })).To(BeTrue())
		}
	})
	It("should update instance type offering date with response from the ListRegionsAvailability API", func() {
		linodeInstanceTypeInfo := fake.MakeInstances()
		linodeOfferings := fake.MakeInstanceOfferings(linodeInstanceTypeInfo)
		linodeEnv.LinodeAPI.ListTypesOutput.Set(&linodeInstanceTypeInfo)
		linodeEnv.LinodeAPI.ListRegionsAvailabilityOutput.Set(&linodeOfferings)

		ExpectSingletonReconciled(ctx, controller)
		instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, &v1.LinodeNodeClass{})
		Expect(err).To(BeNil())

		Expect(len(instanceTypes)).To(BeNumerically("==", len(linodeInstanceTypeInfo)))
		for x := range instanceTypes {
			offering, found := lo.Find(linodeOfferings, func(off linodego.RegionAvailability) bool {
				return instanceTypes[x].Name == off.Plan
			})
			Expect(found).To(BeTrue())
			for y := range instanceTypes[x].Offerings {
				Expect(instanceTypes[x].Offerings[y].Requirements.Get(corev1.LabelTopologyRegion).Any()).To(Equal(offering.Region))
			}
		}
	})
	It("should not update instance type date with response from the ListTypes API is empty", func() {
		linodeEnv.LinodeAPI.ListTypesOutput.Set(nil)
		linodeEnv.LinodeAPI.ListRegionsAvailabilityOutput.Set(nil)
		ExpectSingletonReconciled(ctx, controller)
		_, err := linodeEnv.InstanceTypesProvider.List(ctx, &v1.LinodeNodeClass{})
		Expect(err).ToNot(BeNil())
	})
	It("should not update instance type offering date with response from the ListRegionsAvailability API", func() {
		linodeEnv.LinodeAPI.ListTypesOutput.Set(nil)
		linodeEnv.LinodeAPI.ListRegionsAvailabilityOutput.Set(nil)
		ExpectSingletonReconciled(ctx, controller)
		_, err := linodeEnv.InstanceTypesProvider.List(ctx, &v1.LinodeNodeClass{})
		Expect(err).ToNot(BeNil())
	})
})
