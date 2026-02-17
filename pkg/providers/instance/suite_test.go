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

package instance_test

import (
	"context"
	"testing"

	"github.com/awslabs/operatorpkg/object"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	corecloudprovider "sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
	coretest "sigs.k8s.io/karpenter/pkg/test"
	testv1alpha1 "sigs.k8s.io/karpenter/pkg/test/v1alpha1"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	"github.com/linode/karpenter-provider-linode/pkg/cloudprovider"
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
var cloudProvider *cloudprovider.CloudProvider

func TestLinode(t *testing.T) {
	t.Parallel()
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "InstanceProvider")
}

var _ = BeforeSuite(func() {
	env = coretest.NewEnvironment(coretest.WithCRDs(apis.CRDs...), coretest.WithCRDs(testv1alpha1.CRDs...))
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: lo.ToPtr(true)}}))
	ctx = options.ToContext(ctx, test.Options())
	ctx, stop = context.WithCancel(ctx)
	linodeEnv = test.NewEnvironment(ctx)
	cloudProvider = cloudprovider.New(
		linodeEnv.InstanceTypesProvider,
		linodeEnv.NodeProvider(ctx),
		events.NewRecorder(&record.FakeRecorder{}),
		env.Client,
	)
})

var _ = AfterSuite(func() {
	stop()
	Expect(env.Stop()).To(Succeed(), "Failed to stop environment")
})

var _ = BeforeEach(func() {
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{}))
	ctx = options.ToContext(ctx, test.Options())
	linodeEnv.Reset()
	linodeEnv.SetDefaults()
})

var _ = Describe("InstanceProvider", func() {
	var nodeClass *v1.LinodeNodeClass
	var nodePool *karpv1.NodePool
	var nodeClaim *karpv1.NodeClaim
	BeforeEach(func() {
		nodeClass = test.LinodeNodeClass()
		nodePool = coretest.NodePool(karpv1.NodePool{
			Spec: karpv1.NodePoolSpec{
				Template: karpv1.NodeClaimTemplate{
					Spec: karpv1.NodeClaimTemplateSpec{
						NodeClassRef: &karpv1.NodeClassReference{
							Group: object.GVK(nodeClass).Group,
							Kind:  object.GVK(nodeClass).Kind,
							Name:  nodeClass.Name,
						},
					},
				},
			},
		})
		nodeClaim = coretest.NodeClaim(karpv1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					karpv1.NodePoolLabelKey: nodePool.Name,
				},
			},
			Spec: karpv1.NodeClaimSpec{
				NodeClassRef: &karpv1.NodeClassReference{
					Group: object.GVK(nodeClass).Group,
					Kind:  object.GVK(nodeClass).Kind,
					Name:  nodeClass.Name,
				},
			},
		})
		Expect(linodeEnv.InstanceTypesProvider.UpdateInstanceTypes(ctx)).To(Succeed())
		Expect(linodeEnv.InstanceTypesProvider.UpdateInstanceTypeOfferings(ctx)).To(Succeed())
	})
	It("should return an ICE error when all attempted instance types return an ICE error", func() {
		dedicated8GB := "g6-dedicated-4"
		standard8GB := "g6-standard-4"
		ExpectApplied(ctx, env.Client, nodeClaim, nodePool, nodeClass)
		nodeClass = ExpectExists(ctx, env.Client, nodeClass)
		// Mark dedicated8GB as insufficient capacity
		linodeEnv.LinodeAPI.InsufficientCapacityPools.Set([]fake.CapacityPool{
			{InstanceType: dedicated8GB, Region: fake.DefaultRegion},
		})
		instanceTypes, err := cloudProvider.GetInstanceTypes(ctx, nodePool)
		Expect(err).ToNot(HaveOccurred())

		// Filter down to a single instance type
		instanceTypes = lo.Filter(instanceTypes, func(i *corecloudprovider.InstanceType, _ int) bool { return i.Name == dedicated8GB })

		// Since all the capacity pool for dedicated is ICEd, this should return an ICE error
		instance, err := linodeEnv.InstanceProvider.Create(ctx, nodeClass, nodeClaim, nil, instanceTypes)
		Expect(err).To(HaveOccurred())
		Expect(instance).To(BeNil())

		Expect(linodeEnv.UnavailableOfferingsCache.IsUnavailable(dedicated8GB, fake.DefaultRegion)).To(BeTrue())
		Expect(linodeEnv.UnavailableOfferingsCache.IsUnavailable(standard8GB, fake.DefaultRegion)).To(BeFalse())
	})
	It("should create an instance", func() {
		ExpectApplied(ctx, env.Client, nodeClaim, nodePool, nodeClass)
		nodeClass = ExpectExists(ctx, env.Client, nodeClass)
		instanceTypes, err := cloudProvider.GetInstanceTypes(ctx, nodePool)
		Expect(err).ToNot(HaveOccurred())

		_, err = linodeEnv.InstanceProvider.Create(ctx, nodeClass, nodeClaim, nil, instanceTypes)
		Expect(err).To(BeNil())
	})
})
