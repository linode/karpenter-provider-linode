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

package capacity_test

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/awslabs/operatorpkg/object"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpcloudprovider "sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
	coretest "sigs.k8s.io/karpenter/pkg/test"
	"sigs.k8s.io/karpenter/pkg/test/v1alpha1"
	"sigs.k8s.io/karpenter/pkg/utils/resources"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
	"github.com/linode/karpenter-provider-linode/pkg/cloudprovider"
	controllersinstancetypecapacity "github.com/linode/karpenter-provider-linode/pkg/controllers/providers/instancetype/capacity"
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
var controller *controllersinstancetypecapacity.Controller

var nodeClass *v1.LinodeNodeClass
var nodeClaim *karpv1.NodeClaim
var node *corev1.Node

var standardImage v1.LinodeImage
var nvidiaImage v1.LinodeImage

func TestLinode(t *testing.T) {
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "CapacityCache")
}

var _ = BeforeSuite(func() {
	env = coretest.NewEnvironment(coretest.WithCRDs(apis.CRDs...), coretest.WithCRDs(v1alpha1.CRDs...), coretest.WithFieldIndexers(coretest.NodeClaimProviderIDFieldIndexer(ctx)))
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: lo.ToPtr(true)}}))
	ctx = options.ToContext(ctx, test.Options(test.OptionsFields{
		VMMemoryOverheadPercent: lo.ToPtr[float64](0.075),
	}))
	ctx, stop = context.WithCancel(ctx)
	linodeEnv = test.NewEnvironment(ctx)
	nodeClass = test.LinodeNodeClass()
	nodeClaim = coretest.NodeClaim()
	node = coretest.Node()
	cloudProvider := cloudprovider.New(linodeEnv.InstanceTypesProvider, linodeEnv.InstanceProvider, events.NewRecorder(&record.FakeRecorder{}),
		env.Client)
	controller = controllersinstancetypecapacity.NewController(env.Client, cloudProvider, linodeEnv.InstanceTypesProvider)
})

var _ = AfterSuite(func() {
	stop()
	Expect(env.Stop()).To(Succeed(), "Failed to stop environment")
})

var _ = BeforeEach(func() {
	linodeEnv.Reset()
	linodeInstanceTypeInfo := fake.MakeInstances()
	linodeOfferings := fake.MakeInstanceOfferings(linodeInstanceTypeInfo)
	linodeEnv.LinodeAPI.ListTypesOutput.Set(&linodeInstanceTypeInfo)
	linodeEnv.LinodeAPI.ListRegionsAvailabilityOutput.Set(&linodeOfferings)
	Expect(linodeEnv.InstanceTypesProvider.UpdateInstanceTypes(ctx)).To(Succeed())
	Expect(linodeEnv.InstanceTypesProvider.UpdateInstanceTypeOfferings(ctx)).To(Succeed())
	standardImage = v1.LinodeImage{
		ID:    "image-standard-test",
		Label: "Standard Image",
		Requirements: []corev1.NodeSelectorRequirement{
			{
				Key:      corev1.LabelArchStable,
				Operator: corev1.NodeSelectorOpIn,
				Values:   []string{karpv1.ArchitectureAmd64},
			},
			{
				Key:      v1.LabelInstanceGPUCount,
				Operator: corev1.NodeSelectorOpDoesNotExist,
			},
		},
	}
	nvidiaImage = v1.LinodeImage{
		ID:    "image-nvidia-test",
		Label: "NVIDIA Image",
		Requirements: []corev1.NodeSelectorRequirement{
			{
				Key:      corev1.LabelArchStable,
				Operator: corev1.NodeSelectorOpIn,
				Values:   []string{karpv1.ArchitectureAmd64},
			},
			{
				Key:      v1.LabelInstanceGPUCount,
				Operator: corev1.NodeSelectorOpExists,
			},
		},
	}
	nodeClass.Status.LinodeImages = []v1.LinodeImage{standardImage, nvidiaImage}
})

var _ = AfterEach(func() {
	ExpectCleanedUp(ctx, env.Client)
})

var _ = Describe("CapacityCache", func() {
	BeforeEach(func() {
		ExpectApplied(ctx, env.Client, nodeClass)

		node = coretest.Node(coretest.NodeOptions{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-node",
				Labels: map[string]string{
					corev1.LabelInstanceTypeStable: "g6-standard-2",
					karpv1.NodeRegisteredLabelKey:  "true",
				},
			},
			Capacity: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", 3788)),
			},
			ProviderID: fake.ProviderID(fake.InstanceID()),
		})
		ExpectApplied(ctx, env.Client, node)

		nodeClaim = &karpv1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-nodeclaim",
			},
			Spec: karpv1.NodeClaimSpec{
				NodeClassRef: &karpv1.NodeClassReference{
					Group: object.GVK(nodeClass).Group,
					Kind:  object.GVK(nodeClass).Kind,
					Name:  nodeClass.Name,
				},
				Requirements: make([]karpv1.NodeSelectorRequirementWithMinValues, 0),
			},
			Status: karpv1.NodeClaimStatus{
				NodeName:   node.Name,
				ImageID:    nodeClass.Status.LinodeImages[0].ID,
				ProviderID: node.Spec.ProviderID,
			},
		}
		ExpectApplied(ctx, env.Client, nodeClaim)
	})
	It("should update discovered capacity based on existing nodes", func() {
		ExpectObjectReconciled(ctx, env.Client, controller, node)
		instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
		Expect(err).To(BeNil())
		i, ok := lo.Find(instanceTypes, func(i *karpcloudprovider.InstanceType) bool {
			return i.Name == "g6-standard-2"
		})
		Expect(ok).To(BeTrue())
		Expect(i.Capacity.Memory().Value()).To(Equal(node.Status.Capacity.Memory().Value()), "Expected capacity to match discovered node capacity")
	})

	It("should use VM_MEMORY_OVERHEAD_PERCENT calculation after Image update", func() {
		ExpectObjectReconciled(ctx, env.Client, controller, node)

		// Update NodeClass Image and list instance-types. Cached values from prior Images should no longer be used.
		nodeClass.Status.LinodeImages[0].ID = "image-new-test-id"
		ExpectApplied(ctx, env.Client, nodeClaim)
		ExpectObjectReconciled(ctx, env.Client, controller, node)
		instanceTypesNoCache, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
		Expect(err).To(BeNil())
		i, ok := lo.Find(instanceTypesNoCache, func(i *karpcloudprovider.InstanceType) bool {
			return i.Name == "g6-standard-4"
		})
		Expect(ok).To(BeTrue())

		// Calculate memory capacity based on VM_MEMORY_OVERHEAD_PERCENT and output from ListTypes
		mem := resources.Quantity(fmt.Sprintf("%dMi", 8192)) // Reported memory from fake.MakeInstances()
		mem.Sub(resource.MustParse(fmt.Sprintf("%dMi", int64(math.Ceil(float64(mem.Value())*options.FromContext(ctx).VMMemoryOverheadPercent/1024/1024)))))
		Expect(i.Capacity.Memory().Value()).To(Equal(mem.Value()), "Expected capacity to match VMMemoryOverheadPercent calculation")
	})

	It("should properly update discovered capacity when matching Image is not the first in the list", func() {
		// Update nodeClass AMIs for this test
		nodeClass.Status.LinodeImages = []v1.LinodeImage{standardImage}
		ExpectApplied(ctx, env.Client, nodeClass)

		// Get available instance types from the test environment
		availableInstanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
		Expect(err).To(BeNil())
		Expect(availableInstanceTypes).ToNot(BeEmpty(), "No instance types available in test environment")

		// Choose the first instance type for testing
		testInstanceType := availableInstanceTypes[0]
		instanceTypeName := testInstanceType.Name

		// Create a test node with the discovered instance type
		memoryCapacity := resource.MustParse("4Gi")

		testNodeClassNvidiaFirst := test.LinodeNodeClass()
		testNodeClassNvidiaFirst.Status.LinodeImages = []v1.LinodeImage{nvidiaImage, standardImage}
		ExpectApplied(ctx, env.Client, testNodeClassNvidiaFirst)

		testNodeClaim := coretest.NodeClaim()
		testNodeClaim.Spec.NodeClassRef = &karpv1.NodeClassReference{
			Group: object.GVK(testNodeClassNvidiaFirst).Group,
			Kind:  object.GVK(testNodeClassNvidiaFirst).Kind,
			Name:  testNodeClassNvidiaFirst.Name,
		}
		testNodeClaim.Status.Capacity = corev1.ResourceList{
			corev1.ResourceMemory: memoryCapacity,
		}
		testNodeClaim.Labels[corev1.LabelInstanceTypeStable] = instanceTypeName
		testNodeClaim.Labels[corev1.LabelArchStable] = karpv1.ArchitectureAmd64
		testNodeClaim.Status.ImageID = "image-standard-test"
		ExpectApplied(ctx, env.Client, testNodeClaim)

		testNode := coretest.NodeClaimLinkedNode(testNodeClaim)
		ExpectApplied(ctx, env.Client, testNode)

		ExpectObjectReconciled(ctx, env.Client, controller, testNode)

		// Verify that the cache was updated by getting the instance types and checking the memory capacity
		instanceTypesAfterUpdateReversed, err := linodeEnv.InstanceTypesProvider.List(ctx, testNodeClassNvidiaFirst)
		Expect(err).To(BeNil())

		// Find our instance type and verify its memory capacity was updated
		found := false
		for _, it := range instanceTypesAfterUpdateReversed {
			if it.Name == instanceTypeName {
				found = true
				// Memory capacity should now match what we set on the node
				memValue := it.Capacity.Memory().Value()
				Expect(memValue).To(Equal(memoryCapacity.Value()))
				break
			}
		}
		Expect(found).To(BeTrue())
	})
})
