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

package lke_test

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/awslabs/operatorpkg/object"
	"github.com/linode/linodego"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	corecloudprovider "sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
	coretest "sigs.k8s.io/karpenter/pkg/test"
	testv1alpha1 "sigs.k8s.io/karpenter/pkg/test/v1alpha1"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
	"github.com/linode/karpenter-provider-linode/pkg/fake"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instance"
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
var recorder events.Recorder

func TestNodepool(t *testing.T) {
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "NodePool Provider Suite")
}

var _ = BeforeSuite(func() {
	env = coretest.NewEnvironment(coretest.WithCRDs(apis.CRDs...), coretest.WithCRDs(testv1alpha1.CRDs...))
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: lo.ToPtr(true)}}))
	ctx, stop = context.WithCancel(ctx)
	linodeEnv = test.NewEnvironment(ctx)
	recorder = events.NewRecorder(&record.FakeRecorder{})
})

var _ = AfterSuite(func() {
	stop()
	Expect(env.Stop()).To(Succeed(), "Failed to stop environment")
})

var _ = BeforeEach(func() {
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{}))
	ctx = options.ToContext(ctx, test.Options())
	linodeEnv.Reset()
})

var _ = Describe("LKENodeProvider", func() {
	var nodeClass *v1.LinodeNodeClass
	var nodePoolObj *karpv1.NodePool
	var nodeClaim *karpv1.NodeClaim

	BeforeEach(func() {
		nodeClass = test.LinodeNodeClassWithLKE()
		nodePoolObj = coretest.NodePool(karpv1.NodePool{
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
					karpv1.NodePoolLabelKey: nodePoolObj.Name,
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
		ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
		nodeClass = ExpectExists(ctx, env.Client, nodeClass)

		linodeEnv.LinodeAPI.InsufficientCapacityPools.Set([]fake.CapacityPool{
			{CapacityType: karpv1.CapacityTypeOnDemand, InstanceType: dedicated8GB, Region: fake.DefaultRegion},
		})

		instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
		Expect(err).ToNot(HaveOccurred())
		instanceTypes = lo.Filter(instanceTypes, func(i *corecloudprovider.InstanceType, _ int) bool { return i.Name == dedicated8GB })

		tags := map[string]string{}
		poolInstance, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, tags, instanceTypes)
		Expect(err).To(HaveOccurred())
		Expect(poolInstance).To(BeNil())

		Expect(linodeEnv.UnavailableOfferingsCache.IsUnavailable(dedicated8GB, fake.DefaultRegion, karpv1.CapacityTypeOnDemand)).To(BeTrue())
		Expect(linodeEnv.UnavailableOfferingsCache.IsUnavailable(standard8GB, fake.DefaultRegion, karpv1.CapacityTypeOnDemand)).To(BeFalse())
	})

	It("should create a dedicated nodepool instance", func() {
		nodeClaim.Spec.Requirements = []karpv1.NodeSelectorRequirementWithMinValues{
			{
				NodeSelectorRequirement: corev1.NodeSelectorRequirement{
					Key:      karpv1.CapacityTypeLabelKey,
					Operator: corev1.NodeSelectorOpIn,
					Values: []string{
						karpv1.CapacityTypeOnDemand,
					},
				},
			},
		}
		ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
		nodeClass = ExpectExists(ctx, env.Client, nodeClass)

		instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
		Expect(err).ToNot(HaveOccurred())

		tags := map[string]string{}
		poolInstance, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, tags, instanceTypes)
		Expect(err).To(BeNil())
		Expect(poolInstance).ToNot(BeNil())
		Expect(poolInstance.Type).ToNot(BeEmpty())
		Expect(poolInstance.ID).ToNot(BeZero())
	})

	It("should merge NodeClass tags with provided tags", func() {
		nodeClass.Spec.Tags = []string{"env:production", "team:platform"}
		ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
		nodeClass = ExpectExists(ctx, env.Client, nodeClass)

		instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
		Expect(err).ToNot(HaveOccurred())

		tags := map[string]string{
			"karpenter.sh/nodepool": "test-pool",
		}
		poolInstance, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, tags, instanceTypes)
		Expect(err).ToNot(HaveOccurred())
		Expect(poolInstance).ToNot(BeNil())
		Expect(poolInstance.Tags).To(ContainElement("env:production"))
		Expect(poolInstance.Tags).To(ContainElement("team:platform"))
		Expect(poolInstance.Tags).To(ContainElement("karpenter.sh/nodepool:test-pool"))
	})

	It("should convert NodeClaim taints to LKE taints", func() {
		nodeClaim.Spec.Taints = []corev1.Taint{
			{
				Key:    "dedicated",
				Value:  "gpu",
				Effect: corev1.TaintEffectNoSchedule,
			},
		}
		ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
		nodeClass = ExpectExists(ctx, env.Client, nodeClass)

		instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
		Expect(err).ToNot(HaveOccurred())

		tags := map[string]string{}
		poolInstance, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, tags, instanceTypes)
		Expect(err).ToNot(HaveOccurred())
		Expect(poolInstance).ToNot(BeNil())
		Expect(poolInstance.Taints).To(ContainElement(linodego.LKENodePoolTaint{
			Key:    "dedicated",
			Value:  "gpu",
			Effect: linodego.LKENodePoolTaintEffect(corev1.TaintEffectNoSchedule),
		}))
	})

	Context("Get", func() {
		It("should return node when available", func() {
			ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
			nodeClass = ExpectExists(ctx, env.Client, nodeClass)

			instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
			Expect(err).ToNot(HaveOccurred())

			// Create a node first
			tags := map[string]string{}
			createdNode, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, tags, instanceTypes)
			Expect(err).ToNot(HaveOccurred())
			Expect(createdNode).ToNot(BeNil())

			// Get the node - should return from cache
			id := fmt.Sprintf("%d", createdNode.ID)
			retrievedNode, err := linodeEnv.LKENodeProvider.Get(ctx, id)
			Expect(err).ToNot(HaveOccurred())
			Expect(retrievedNode).ToNot(BeNil())
			Expect(retrievedNode.ID).To(Equal(createdNode.ID))
			Expect(retrievedNode.PoolID).To(Equal(createdNode.PoolID))
		})

		It("should return error for invalid instance ID format", func() {
			_, err := linodeEnv.LKENodeProvider.Get(ctx, "invalid-id")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("parsing instance ID"))
		})

		It("should return error when instance not found in any pool", func() {
			_, err := linodeEnv.LKENodeProvider.Get(ctx, "999999")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})
	})

	Context("List", func() {
		It("should return all nodes from all pools", func() {
			ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
			nodeClass = ExpectExists(ctx, env.Client, nodeClass)

			instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
			Expect(err).ToNot(HaveOccurred())

			// Create first node
			tags := map[string]string{v1.LabelLKEManaged: "true", karpv1.NodePoolLabelKey: "pool1"}
			node1, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, tags, instanceTypes)
			Expect(err).ToNot(HaveOccurred())

			// Create second node with different nodeclaim
			nodeClaim2 := coretest.NodeClaim(karpv1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						karpv1.NodePoolLabelKey: nodePoolObj.Name,
					},
				},
				Spec: karpv1.NodeClaimSpec{
					NodeClassRef: nodeClaim.Spec.NodeClassRef,
				},
			})
			ExpectApplied(ctx, env.Client, nodeClaim2)
			node2, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim2, tags, instanceTypes)
			Expect(err).ToNot(HaveOccurred())

			// List should return both
			nodes, err := linodeEnv.LKENodeProvider.List(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(nodes)).To(BeNumerically(">=", 2))

			instanceIDs := lo.Map(nodes, func(n *instance.Instance, _ int) int { return n.ID })
			Expect(instanceIDs).To(ContainElement(node1.ID))
			Expect(instanceIDs).To(ContainElement(node2.ID))
		})

		It("should return empty list when no pools exist", func() {
			nodes, err := linodeEnv.LKENodeProvider.List(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(nodes).To(BeEmpty())
		})
	})

	Context("Delete", func() {
		It("should delete pool by instance ID", func() {
			ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
			nodeClass = ExpectExists(ctx, env.Client, nodeClass)

			instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
			Expect(err).ToNot(HaveOccurred())

			// Create a node
			tags := map[string]string{v1.LabelLKEManaged: "true", karpv1.NodePoolLabelKey: "pool1"}
			createdNode, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, tags, instanceTypes)
			Expect(err).ToNot(HaveOccurred())

			// Delete by instance ID
			id := fmt.Sprintf("%d", createdNode.ID)
			err = linodeEnv.LKENodeProvider.Delete(ctx, id)
			Expect(err).ToNot(HaveOccurred())

			// Verify node is no longer retrievable
			_, err = linodeEnv.LKENodeProvider.Get(ctx, id)
			Expect(err).To(HaveOccurred())
		})

		It("should return error for invalid instance ID", func() {
			err := linodeEnv.LKENodeProvider.Delete(ctx, "invalid-id")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("parsing instance ID"))
		})

		It("should return error when instance not found", func() {
			err := linodeEnv.LKENodeProvider.Delete(ctx, "999999")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})
	})

	Context("UpdateTags", func() {
		It("should merge new tags with existing pool tags", func() {
			ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
			nodeClass = ExpectExists(ctx, env.Client, nodeClass)

			instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
			Expect(err).ToNot(HaveOccurred())

			// Create a node with initial tags
			initialTags := map[string]string{
				v1.LabelLKEManaged:      "true",
				karpv1.NodePoolLabelKey: "pool1",
			}
			createdNode, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, initialTags, instanceTypes)
			Expect(err).ToNot(HaveOccurred())

			// Update with new tags
			newTags := map[string]string{
				v1.NameTagKey:      "test-node",
				v1.NodeClaimTagKey: "test-claim",
			}
			err = linodeEnv.LKENodeProvider.CreateTags(ctx, strconv.Itoa(createdNode.ID), newTags)
			Expect(err).ToNot(HaveOccurred())

			// Verify tags were merged by getting the node fresh
			id := fmt.Sprintf("%d", createdNode.ID)
			updatedNode, err := linodeEnv.LKENodeProvider.Get(ctx, id, instance.SkipCache)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedNode.Tags).To(ContainElement(ContainSubstring(v1.NameTagKey)))
			Expect(updatedNode.Tags).To(ContainElement(ContainSubstring(v1.NodeClaimTagKey)))
		})

		It("should return error when pool not found", func() {
			err := linodeEnv.LKENodeProvider.CreateTags(ctx, "999999", map[string]string{"test": "value"})
			Expect(err).To(HaveOccurred())
		})
	})
})
