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
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/awslabs/operatorpkg/object"
	"github.com/linode/linodego"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	corecloudprovider "sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
	coretest "sigs.k8s.io/karpenter/pkg/test"
	testv1alpha1 "sigs.k8s.io/karpenter/pkg/test/v1alpha1"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	"github.com/linode/karpenter-provider-linode/pkg/fake"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instance"
	"github.com/linode/karpenter-provider-linode/pkg/providers/lke"
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
	ctx = options.ToContext(ctx, test.Options(test.OptionsFields{
		ClusterRegion: ptr.To(fake.DefaultRegion),
	}))
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
		nodeClass = test.LinodeNodeClass()
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

	Context("Create", func() {
		It("should surface instance lookup errors", func() {
			linodeEnv.LinodeAPI.ListInstancesBehavior.Error.Set(&linodego.Error{Code: http.StatusServiceUnavailable, Message: "retry"})

			ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
			nodeClass = ExpectExists(ctx, env.Client, nodeClass)
			instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
			Expect(err).ToNot(HaveOccurred())

			poolInstance, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, map[string]string{}, instanceTypes)
			Expect(err).To(HaveOccurred())
			Expect(poolInstance).To(BeNil())
			Expect(err.Error()).To(ContainSubstring("creating nodeclaim"))
			Expect(err.Error()).To(ContainSubstring("retry"))
			Expect(linodeEnv.LinodeAPI.CreateLKENodePoolBehavior.Calls()).To(Equal(0))
		})

		It("should return an ICE error when all attempted instance types return an ICE error", func() {
			dedicated8GB := "g6-dedicated-4"
			standard8GB := "g6-standard-4"
			ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
			nodeClass = ExpectExists(ctx, env.Client, nodeClass)

			linodeEnv.LinodeAPI.InsufficientCapacityPools.Set([]fake.CapacityPool{
				{InstanceType: dedicated8GB, Region: fake.DefaultRegion},
			})

			instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
			Expect(err).ToNot(HaveOccurred())
			instanceTypes = lo.Filter(instanceTypes, func(i *corecloudprovider.InstanceType, _ int) bool { return i.Name == dedicated8GB })

			tags := map[string]string{}
			poolInstance, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, tags, instanceTypes)
			Expect(err).To(HaveOccurred())
			Expect(poolInstance).To(BeNil())

			Expect(linodeEnv.UnavailableOfferingsCache.IsUnavailable(dedicated8GB, fake.DefaultRegion)).To(BeTrue())
			Expect(linodeEnv.UnavailableOfferingsCache.IsUnavailable(standard8GB, fake.DefaultRegion)).To(BeFalse())
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

		It("should include NodeClass tags in pool tags", func() {
			nodeClass.Spec.Tags = []string{"env:production", "team:platform"}
			ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
			nodeClass = ExpectExists(ctx, env.Client, nodeClass)

			instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
			Expect(err).ToNot(HaveOccurred())

			tags := map[string]string{}
			poolInstance, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, tags, instanceTypes)
			Expect(err).ToNot(HaveOccurred())
			Expect(poolInstance).ToNot(BeNil())
			Expect(poolInstance.Tags).To(ContainElement("env:production"))
			Expect(poolInstance.Tags).To(ContainElement("team:platform"))
		})

		It("should include caller-provided tags in pool tags", func() {
			ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
			nodeClass = ExpectExists(ctx, env.Client, nodeClass)

			instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
			Expect(err).ToNot(HaveOccurred())

			tags := map[string]string{"integration": "true"}
			poolInstance, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, tags, instanceTypes)
			Expect(err).ToNot(HaveOccurred())
			Expect(poolInstance).ToNot(BeNil())
			Expect(poolInstance.Tags).To(ContainElement("integration:true"))
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

		Context("Idempotency", func() {
			It("should reuse an existing instance by nodeclaim tag on the instance", func() {
				poolID := 201
				instanceID := 3001
				nodeClaimTag := fmt.Sprintf("%s:%s", v1.NodeClaimTagKey, nodeClaim.Name)
				poolTags := []string{
					fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
					fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
				}
				pool := &linodego.LKENodePool{ID: poolID, Type: "g6-standard-2", Tags: poolTags, Linodes: []linodego.LKENodePoolLinode{{InstanceID: instanceID, ID: "node-3001"}}}
				linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)
				now := time.Now()
				inst := linodego.Instance{ID: instanceID, Tags: append(poolTags, nodeClaimTag), Created: &now}
				linodeEnv.LinodeAPI.Instances.Store(instanceID, inst)

				ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
				nodeClass = ExpectExists(ctx, env.Client, nodeClass)
				instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
				Expect(err).ToNot(HaveOccurred())

				node, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, map[string]string{}, instanceTypes)
				Expect(err).ToNot(HaveOccurred())
				Expect(node).ToNot(BeNil())
				Expect(node.PoolID).To(Equal(poolID))
				Expect(node.ID).To(Equal(instanceID))
				Expect(linodeEnv.LinodeAPI.CreateLKENodePoolBehavior.Calls()).To(Equal(0))

				updatedInst, _ := linodeEnv.LinodeAPI.Instances.Load(instanceID)
				Expect(updatedInst.(linodego.Instance).Tags).To(ContainElement(fmt.Sprintf("%s:%s", v1.NodeClaimTagKey, nodeClaim.Name)))
				Expect(updatedInst.(linodego.Instance).Tags).To(ContainElement(fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name)))
				Expect(updatedInst.(linodego.Instance).Tags).To(ContainElement(fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true")))
			})

			It("should claim an unclaimed instance from an existing pool", func() {
				poolID := 202
				instanceID := 3002
				poolTags := []string{
					fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
					fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
				}
				pool := &linodego.LKENodePool{ID: poolID, Type: "g6-standard-2", Tags: poolTags, Linodes: []linodego.LKENodePoolLinode{{InstanceID: instanceID, ID: "node-3002"}}}
				linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

				now := time.Now()
				inst := linodego.Instance{ID: instanceID, Tags: []string{}, Created: &now}
				linodeEnv.LinodeAPI.Instances.Store(instanceID, inst)

				ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
				nodeClass = ExpectExists(ctx, env.Client, nodeClass)
				instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
				Expect(err).ToNot(HaveOccurred())

				node, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, map[string]string{}, instanceTypes)
				Expect(err).ToNot(HaveOccurred())
				Expect(node).ToNot(BeNil())
				Expect(node.PoolID).To(Equal(poolID))
				Expect(node.ID).To(Equal(instanceID))
				Expect(linodeEnv.LinodeAPI.CreateLKENodePoolBehavior.Calls()).To(Equal(0))

				updatedInst, _ := linodeEnv.LinodeAPI.Instances.Load(instanceID)
				Expect(updatedInst.(linodego.Instance).Tags).To(ContainElement(fmt.Sprintf("%s:%s", v1.NodeClaimTagKey, nodeClaim.Name)))
				Expect(updatedInst.(linodego.Instance).Tags).ToNot(ContainElement(fmt.Sprintf("%s=%d", v1.LKENodePoolTagKey, poolID)))
			})

			It("should preserve existing instance tags when claiming", func() {
				poolID := 203
				instanceID := 3003
				poolTags := []string{
					fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
					fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
				}
				pool := &linodego.LKENodePool{ID: poolID, Type: "g6-standard-2", Tags: poolTags, Linodes: []linodego.LKENodePoolLinode{{InstanceID: instanceID, ID: "node-3003"}}}
				linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

				now := time.Now()
				existingTags := []string{"custom-tag:value", "another-tag:data"}
				inst := linodego.Instance{ID: instanceID, Tags: existingTags, Created: &now}
				linodeEnv.LinodeAPI.Instances.Store(instanceID, inst)

				ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
				nodeClass = ExpectExists(ctx, env.Client, nodeClass)
				instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
				Expect(err).ToNot(HaveOccurred())

				node, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, map[string]string{}, instanceTypes)
				Expect(err).ToNot(HaveOccurred())
				Expect(node).ToNot(BeNil())

				updatedInst, _ := linodeEnv.LinodeAPI.Instances.Load(instanceID)
				tags := updatedInst.(linodego.Instance).Tags
				Expect(tags).To(ContainElement("custom-tag:value"))
				Expect(tags).To(ContainElement("another-tag:data"))
				Expect(tags).To(ContainElement(fmt.Sprintf("%s:%s", v1.NodeClaimTagKey, nodeClaim.Name)))
				Expect(tags).To(ContainElement(fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name)))
				Expect(tags).To(ContainElement(fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true")))
				Expect(tags).ToNot(ContainElement(fmt.Sprintf("%s=%d", v1.LKENodePoolTagKey, poolID)))
			})

			It("should handle instances with no existing tags", func() {
				poolID := 204
				instanceID := 3004
				poolTags := []string{
					fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
					fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
				}
				pool := &linodego.LKENodePool{ID: poolID, Type: "g6-standard-2", Tags: poolTags, Linodes: []linodego.LKENodePoolLinode{{InstanceID: instanceID, ID: "node-3004"}}}
				linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

				now := time.Now()
				inst := linodego.Instance{ID: instanceID, Tags: nil, Created: &now}
				linodeEnv.LinodeAPI.Instances.Store(instanceID, inst)

				ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
				nodeClass = ExpectExists(ctx, env.Client, nodeClass)
				instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
				Expect(err).ToNot(HaveOccurred())

				node, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, map[string]string{}, instanceTypes)
				Expect(err).ToNot(HaveOccurred())
				Expect(node).ToNot(BeNil())

				updatedInst, _ := linodeEnv.LinodeAPI.Instances.Load(instanceID)
				tags := updatedInst.(linodego.Instance).Tags
				Expect(tags).To(ContainElement(fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name)))
				Expect(tags).To(ContainElement(fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true")))
				Expect(tags).To(ContainElement(fmt.Sprintf("%s:%s", v1.NodeClaimTagKey, nodeClaim.Name)))
				Expect(tags).ToNot(ContainElement(fmt.Sprintf("%s=%d", v1.LKENodePoolTagKey, poolID)))
			})

			It("should claim first available unclaimed instance when multiple exist", func() {
				poolID := 205
				instanceID1 := 3010
				instanceID2 := 3005
				instanceID3 := 3015
				poolTags := []string{
					fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
					fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
				}
				pool := &linodego.LKENodePool{
					ID:   poolID,
					Type: "g6-standard-2",
					Tags: poolTags,
					Linodes: []linodego.LKENodePoolLinode{
						{InstanceID: instanceID1, ID: "node-3010"},
						{InstanceID: instanceID2, ID: "node-3005"},
						{InstanceID: instanceID3, ID: "node-3015"},
					},
				}
				linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

				now := time.Now()
				linodeEnv.LinodeAPI.Instances.Store(instanceID1, linodego.Instance{ID: instanceID1, Tags: []string{}, Created: &now})
				linodeEnv.LinodeAPI.Instances.Store(instanceID2, linodego.Instance{ID: instanceID2, Tags: []string{}, Created: &now})
				linodeEnv.LinodeAPI.Instances.Store(instanceID3, linodego.Instance{ID: instanceID3, Tags: []string{}, Created: &now})

				ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
				nodeClass = ExpectExists(ctx, env.Client, nodeClass)
				instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
				Expect(err).ToNot(HaveOccurred())

				node, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, map[string]string{}, instanceTypes)
				Expect(err).ToNot(HaveOccurred())
				Expect(node).ToNot(BeNil())
				Expect([]int{instanceID1, instanceID2, instanceID3}).To(ContainElement(node.ID))
			})
		})

		Context("Multi-node pool scaling", func() {
			It("should scale pool when no claimable instances exist", func() {
				poolID := 206
				instanceID := 3006
				nodeClaimTag := fmt.Sprintf("%s:%s", v1.NodeClaimTagKey, "other-nodeclaim")
				poolTags := []string{
					fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
					fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
				}
				pool := &linodego.LKENodePool{
					ID:      poolID,
					Type:    "g6-standard-2",
					Count:   1,
					Tags:    poolTags,
					Linodes: []linodego.LKENodePoolLinode{{InstanceID: instanceID, ID: "node-3006"}},
				}
				linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

				now := time.Now()
				alreadyClaimedInst := linodego.Instance{ID: instanceID, Tags: []string{nodeClaimTag}, Created: &now}
				linodeEnv.LinodeAPI.Instances.Store(instanceID, alreadyClaimedInst)

				ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
				nodeClass = ExpectExists(ctx, env.Client, nodeClass)
				instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
				Expect(err).ToNot(HaveOccurred())

				node, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, map[string]string{}, instanceTypes)
				Expect(err).ToNot(HaveOccurred())
				Expect(node).ToNot(BeNil())
				Expect(node.ID).ToNot(Equal(instanceID))

				Expect(linodeEnv.LinodeAPI.UpdateLKENodePoolBehavior.Calls()).To(BeNumerically(">=", 1))
			})

			It("should reuse existing pool for same (nodepool, instanceType)", func() {
				poolID := 207
				instanceID := 3008
				poolTags := []string{
					fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
					fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
				}
				pool := &linodego.LKENodePool{ID: poolID, Type: "g6-standard-2", Tags: poolTags, Linodes: []linodego.LKENodePoolLinode{{InstanceID: instanceID, ID: "node-3008"}}}
				linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

				now := time.Now()
				inst := linodego.Instance{ID: instanceID, Tags: []string{}, Created: &now}
				linodeEnv.LinodeAPI.Instances.Store(instanceID, inst)

				ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
				nodeClass = ExpectExists(ctx, env.Client, nodeClass)
				instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
				Expect(err).ToNot(HaveOccurred())

				node, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, map[string]string{}, instanceTypes)
				Expect(err).ToNot(HaveOccurred())
				Expect(node).ToNot(BeNil())
				Expect(node.PoolID).To(Equal(poolID))

				Expect(linodeEnv.LinodeAPI.CreateLKENodePoolBehavior.Calls()).To(Equal(0))
			})
		})

		Context("Tag verification", func() {
			It("should verify tags were applied before returning", func() {
				poolID := 208
				instanceID := 3009
				poolTags := []string{
					fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
					fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
				}
				pool := &linodego.LKENodePool{ID: poolID, Type: "g6-standard-2", Tags: poolTags, Linodes: []linodego.LKENodePoolLinode{{InstanceID: instanceID, ID: "node-3009"}}}
				linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

				now := time.Now()
				inst := linodego.Instance{ID: instanceID, Tags: []string{}, Created: &now}
				linodeEnv.LinodeAPI.Instances.Store(instanceID, inst)

				ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
				nodeClass = ExpectExists(ctx, env.Client, nodeClass)
				instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
				Expect(err).ToNot(HaveOccurred())

				node, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, map[string]string{}, instanceTypes)
				Expect(err).ToNot(HaveOccurred())
				Expect(node).ToNot(BeNil())

				Expect(linodeEnv.LinodeAPI.GetInstanceBehavior.Calls()).To(BeNumerically(">=", 2))
			})
		})

		Context("Standard tier", func() {
			It("should wrap non-retryable get errors when hydrating pools", func() {
				linodeEnv.LinodeAPI.GetLKENodePoolBehavior.Error.Set(&linodego.Error{Code: http.StatusNotFound, Message: "missing"})

				ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
				nodeClass = ExpectExists(ctx, env.Client, nodeClass)
				instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
				Expect(err).ToNot(HaveOccurred())

				poolInstance, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, map[string]string{}, instanceTypes)
				Expect(err).To(HaveOccurred())
				Expect(poolInstance).To(BeNil())
				Expect(err.Error()).To(ContainSubstring("getting node pool"))
			})

			It("should surface instance get errors when scanning for claimable instances", func() {
				poolID := 999
				instanceID := 9991
				poolTags := []string{
					fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
					fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
				}
				pool := &linodego.LKENodePool{ID: poolID, Type: "g6-standard-2", Tags: poolTags, Linodes: []linodego.LKENodePoolLinode{{InstanceID: instanceID, ID: "node-9991"}}}
				linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

				linodeEnv.LinodeAPI.GetInstanceBehavior.Error.Set(&linodego.Error{Code: http.StatusServiceUnavailable, Message: "retry"})

				ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
				nodeClass = ExpectExists(ctx, env.Client, nodeClass)
				instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
				Expect(err).ToNot(HaveOccurred())

				poolInstance, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, map[string]string{}, instanceTypes)
				Expect(err).To(HaveOccurred())
				Expect(poolInstance).To(BeNil())
				Expect(err.Error()).To(ContainSubstring("getting instance"))
			})
		})

		Context("Enterprise tier", func() {
			var enterpriseProvider *lke.DefaultProvider

			BeforeEach(func() {
				enterpriseProvider = lke.NewDefaultProvider(
					fake.DefaultClusterID,
					linodego.LKEVersionEnterprise,
					fake.DefaultClusterName,
					fake.DefaultRegion,
					recorder,
					linodeEnv.LinodeAPI,
					linodeEnv.UnavailableOfferingsCache,
					linodeEnv.NodePoolCache,
				)
			})

			It("should find existing instance by tag in enterprise tier", func() {
				poolID := 203
				instanceID := 7001
				nodeClaimTag := fmt.Sprintf("%s:%s", v1.NodeClaimTagKey, nodeClaim.Name)
				poolTags := []string{
					fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
					fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
				}
				pool := &linodego.LKENodePool{ID: poolID, Type: "g6-standard-2", Tags: poolTags, Linodes: []linodego.LKENodePoolLinode{{InstanceID: instanceID, ID: "node-7001"}}}
				linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

				now := time.Now()
				inst := linodego.Instance{ID: instanceID, Tags: []string{nodeClaimTag}, Created: &now}
				linodeEnv.LinodeAPI.Instances.Store(instanceID, inst)

				ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
				nodeClass = ExpectExists(ctx, env.Client, nodeClass)
				instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
				Expect(err).ToNot(HaveOccurred())

				node, err := enterpriseProvider.Create(ctx, nodeClass, nodeClaim, map[string]string{}, instanceTypes)
				Expect(err).ToNot(HaveOccurred())
				Expect(node).ToNot(BeNil())
				Expect(node.ID).To(Equal(instanceID))
			})

			It("should resolve pool by nodepool tag in enterprise tier", func() {
				poolID := 204
				instanceID := 7002
				poolTags := []string{
					fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
					fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
				}
				pool := &linodego.LKENodePool{ID: poolID, Type: "g6-standard-2", Tags: poolTags, Linodes: []linodego.LKENodePoolLinode{{InstanceID: instanceID, ID: "node-7002"}}}
				linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

				now := time.Now()
				inst := linodego.Instance{ID: instanceID, Tags: append(poolTags, fmt.Sprintf("%s=%d", v1.LKENodePoolTagKey, poolID)), Created: &now}
				linodeEnv.LinodeAPI.Instances.Store(instanceID, inst)

				retrieved, err := enterpriseProvider.Get(ctx, strconv.Itoa(instanceID))
				Expect(err).ToNot(HaveOccurred())
				Expect(retrieved).ToNot(BeNil())
				Expect(retrieved.PoolID).To(Equal(poolID))
			})
		})

		Context("Error paths", func() {
			It("should surface retryable create errors", func() {
				linodeEnv.LinodeAPI.CreateLKENodePoolBehavior.Error.Set(&linodego.Error{Code: http.StatusServiceUnavailable, Message: "retry"})

				ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
				nodeClass = ExpectExists(ctx, env.Client, nodeClass)
				instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
				Expect(err).ToNot(HaveOccurred())

				poolInstance, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, map[string]string{}, instanceTypes)
				Expect(err).To(HaveOccurred())
				Expect(poolInstance).To(BeNil())
			})
		})

		Context("Create options", func() {
			It("should propagate optional NodeClass fields", func() {
				firewallID := 123
				version := "1.29"
				strategy := linodego.LKENodePoolUpdateStrategy("rolling_update")
				nodeClass.Spec.FirewallID = lo.ToPtr(firewallID)
				nodeClass.Spec.LKEK8sVersion = lo.ToPtr(version)
				nodeClass.Spec.LKEUpdateStrategy = &strategy

				ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
				nodeClass = ExpectExists(ctx, env.Client, nodeClass)
				instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
				Expect(err).ToNot(HaveOccurred())

				_, _ = linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, map[string]string{}, instanceTypes)

				input := linodeEnv.LinodeAPI.CreateLKENodePoolBehavior.CalledWithInput.At(0)
				Expect(input.Opts.FirewallID).ToNot(BeNil())
				Expect(*input.Opts.FirewallID).To(Equal(firewallID))
				Expect(input.Opts.K8sVersion).ToNot(BeNil())
				Expect(*input.Opts.K8sVersion).To(Equal(version))
				Expect(input.Opts.UpdateStrategy).ToNot(BeNil())
				Expect(*input.Opts.UpdateStrategy).To(Equal(strategy))
			})
		})
	})

	Context("Get", func() {
		It("should return node when available", func() {
			ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
			nodeClass = ExpectExists(ctx, env.Client, nodeClass)

			instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
			Expect(err).ToNot(HaveOccurred())

			tags := map[string]string{}
			createdNode, err := linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, tags, instanceTypes)
			Expect(err).ToNot(HaveOccurred())
			Expect(createdNode).ToNot(BeNil())

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

		It("should surface instance lookup errors", func() {
			linodeEnv.LinodeAPI.GetInstanceBehavior.Error.Set(fmt.Errorf("get fail"))

			_, err := linodeEnv.LKENodeProvider.Get(ctx, "1234")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("get fail"))
		})

		It("should surface pool retrieval errors", func() {
			pool := &linodego.LKENodePool{ID: 1101, Tags: []string{fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name), fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true")}, Linodes: []linodego.LKENodePoolLinode{{InstanceID: 2201}}}
			linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, pool.ID), pool)
			linodeEnv.LinodeAPI.ListLKENodePoolsBehavior.Error.Set(fmt.Errorf("get fail"))

			now := time.Now()
			inst := linodego.Instance{ID: 2201, Tags: append(pool.Tags, fmt.Sprintf("%s:%s", v1.NodeClaimTagKey, nodeClaim.Name)), Created: &now}
			linodeEnv.LinodeAPI.Instances.Store(inst.ID, inst)

			_, err := linodeEnv.LKENodeProvider.Get(ctx, "2201")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("get fail"))
		})

		It("should surface node lookup errors within pool", func() {
			pool := &linodego.LKENodePool{ID: 1201, Tags: []string{fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name), fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true")}, Linodes: []linodego.LKENodePoolLinode{{InstanceID: 2301}}}
			linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, pool.ID), pool)

			now := time.Now()
			inst := linodego.Instance{ID: 2302, Tags: append(pool.Tags, fmt.Sprintf("%s:%s", v1.NodeClaimTagKey, nodeClaim.Name)), Created: &now}
			linodeEnv.LinodeAPI.Instances.Store(inst.ID, inst)

			_, err := linodeEnv.LKENodeProvider.Get(ctx, "2302")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found in any Karpenter-managed pool"))
		})
	})

	Context("List", func() {
		It("should return all nodes from all pools", func() {
			poolID := 301
			instanceID1 := 4001
			instanceID2 := 4002
			poolTags := []string{
				fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
				fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
			}
			pool := &linodego.LKENodePool{
				ID:   poolID,
				Type: "g6-standard-4",
				Tags: poolTags,
				Linodes: []linodego.LKENodePoolLinode{
					{InstanceID: instanceID1, ID: "node-4001"},
					{InstanceID: instanceID2, ID: "node-4002"},
				},
			}
			linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

			now := time.Now()
			inst1 := linodego.Instance{ID: instanceID1, Tags: append(poolTags, fmt.Sprintf("%s:%s", v1.NodeClaimTagKey, "nodeclaim-1")), Created: &now, LKEClusterID: fake.DefaultClusterID}
			inst2 := linodego.Instance{ID: instanceID2, Tags: append(poolTags, fmt.Sprintf("%s:%s", v1.NodeClaimTagKey, "nodeclaim-2")), Created: &now, LKEClusterID: fake.DefaultClusterID}
			linodeEnv.LinodeAPI.Instances.Store(instanceID1, inst1)
			linodeEnv.LinodeAPI.Instances.Store(instanceID2, inst2)

			nodes, err := linodeEnv.LKENodeProvider.List(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(nodes)).To(Equal(2))

			instanceIDs := lo.Map(nodes, func(n *instance.Instance, _ int) int { return n.ID })
			Expect(instanceIDs).To(ContainElement(instanceID1))
			Expect(instanceIDs).To(ContainElement(instanceID2))
		})

		It("should return empty list when no pools exist", func() {
			nodes, err := linodeEnv.LKENodeProvider.List(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(nodes).To(BeEmpty())
		})

		It("should surface list errors", func() {
			linodeEnv.LinodeAPI.ListInstancesBehavior.Error.Set(fmt.Errorf("list fail"))

			nodes, err := linodeEnv.LKENodeProvider.List(ctx)
			Expect(err).To(HaveOccurred())
			Expect(nodes).To(BeNil())
			Expect(err.Error()).To(ContainSubstring("list fail"))
		})

		It("should surface pool lookup errors during list", func() {
			poolID := 302
			instanceID := 4003
			poolTags := []string{
				fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
				fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
			}
			pool := &linodego.LKENodePool{
				ID:   poolID,
				Type: "g6-standard-4",
				Tags: poolTags,
				Linodes: []linodego.LKENodePoolLinode{
					{InstanceID: instanceID, ID: "node-4003"},
				},
			}
			linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)
			linodeEnv.LinodeAPI.ListLKENodePoolsBehavior.Error.Set(&linodego.Error{Code: http.StatusServiceUnavailable, Message: "retry"})

			now := time.Now()
			inst := linodego.Instance{ID: instanceID, Tags: append(poolTags, fmt.Sprintf("%s:%s", v1.NodeClaimTagKey, nodeClaim.Name)), Created: &now, LKEClusterID: fake.DefaultClusterID}
			linodeEnv.LinodeAPI.Instances.Store(instanceID, inst)

			nodes, err := linodeEnv.LKENodeProvider.List(ctx)
			Expect(err).To(HaveOccurred())
			Expect(nodes).To(BeNil())
			Expect(err.Error()).To(ContainSubstring("retry"))
		})
	})

	Context("Pool filtering", func() {
		It("should return not found when instance is missing pool membership", func() {
			instanceID := 9001
			now := time.Now()
			inst := linodego.Instance{ID: instanceID, Tags: []string{fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name)}, Created: &now}
			linodeEnv.LinodeAPI.Instances.Store(instanceID, inst)

			node, err := linodeEnv.LKENodeProvider.Get(ctx, strconv.Itoa(instanceID))
			Expect(err).To(HaveOccurred())
			Expect(node).To(BeNil())
			Expect(err.Error()).To(ContainSubstring("not found in any Karpenter-managed pool"))
		})
	})

	Context("Delete", func() {
		It("should delete individual node when pool has multiple nodes", func() {
			poolID := 401
			instanceID1 := 5001
			instanceID2 := 5002
			poolTags := []string{
				fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
				fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
			}
			pool := &linodego.LKENodePool{
				ID:   poolID,
				Type: "g6-standard-4",
				Tags: poolTags,
				Linodes: []linodego.LKENodePoolLinode{
					{InstanceID: instanceID1, ID: "node-5001"},
					{InstanceID: instanceID2, ID: "node-5002"},
				},
			}
			linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)
			now := time.Now()
			inst := linodego.Instance{ID: instanceID1, Tags: append(poolTags, fmt.Sprintf("%s:%s", v1.NodeClaimTagKey, nodeClaim.Name)), Created: &now}
			linodeEnv.LinodeAPI.Instances.Store(instanceID1, inst)
			inst2 := linodego.Instance{ID: instanceID2, Tags: append(poolTags, fmt.Sprintf("%s:%s", v1.NodeClaimTagKey, "other")), Created: &now}
			linodeEnv.LinodeAPI.Instances.Store(instanceID2, inst2)

			err := linodeEnv.LKENodeProvider.Delete(ctx, strconv.Itoa(instanceID1))
			Expect(err).ToNot(HaveOccurred())

			Expect(linodeEnv.LinodeAPI.DeleteLKENodePoolNodeBehavior.Calls()).To(Equal(1))
			Expect(linodeEnv.LinodeAPI.DeleteLKENodePoolBehavior.Calls()).To(Equal(0))
		})

		It("should delete pool when it's the last node", func() {
			poolID := 402
			instanceID := 5003
			poolTags := []string{
				fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
				fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
			}
			pool := &linodego.LKENodePool{
				ID:   poolID,
				Type: "g6-standard-4",
				Tags: poolTags,
				Linodes: []linodego.LKENodePoolLinode{
					{InstanceID: instanceID, ID: "node-5003"},
				},
			}
			linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)
			now := time.Now()
			inst := linodego.Instance{ID: instanceID, Tags: append(poolTags, fmt.Sprintf("%s:%s", v1.NodeClaimTagKey, nodeClaim.Name)), Created: &now}
			linodeEnv.LinodeAPI.Instances.Store(instanceID, inst)

			err := linodeEnv.LKENodeProvider.Delete(ctx, strconv.Itoa(instanceID))
			Expect(err).ToNot(HaveOccurred())

			Expect(linodeEnv.LinodeAPI.DeleteLKENodePoolBehavior.Calls()).To(Equal(1))
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

		It("should surface delete errors", func() {
			pool := &linodego.LKENodePool{ID: 777, Tags: []string{fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name), fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true")}, Linodes: []linodego.LKENodePoolLinode{{InstanceID: 888, ID: "node-888"}}}
			linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, pool.ID), pool)
			linodeEnv.LinodeAPI.DeleteLKENodePoolBehavior.Error.Set(fmt.Errorf("delete fail"))

			now := time.Now()
			inst := linodego.Instance{ID: 888, Tags: append(pool.Tags, fmt.Sprintf("%s:%s", v1.NodeClaimTagKey, nodeClaim.Name)), Created: &now}
			linodeEnv.LinodeAPI.Instances.Store(inst.ID, inst)

			err := linodeEnv.LKENodeProvider.Delete(ctx, "888")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("delete fail"))
		})
	})

	Context("CreateTags", func() {
		It("should update instance tags", func() {
			poolID := 501
			instanceID := 6001
			poolTags := []string{
				fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
				fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
			}
			pool := &linodego.LKENodePool{
				ID:      poolID,
				Type:    "g6-standard-4",
				Tags:    poolTags,
				Linodes: []linodego.LKENodePoolLinode{{InstanceID: instanceID, ID: "node-6001"}},
			}
			linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

			now := time.Now()
			inst := linodego.Instance{ID: instanceID, Tags: []string{"existing-tag"}, Created: &now}
			linodeEnv.LinodeAPI.Instances.Store(instanceID, inst)

			newTags := map[string]string{
				v1.NameTagKey:      "test-node",
				v1.NodeClaimTagKey: "test-claim",
			}
			err := linodeEnv.LKENodeProvider.CreateTags(ctx, strconv.Itoa(instanceID), newTags)
			Expect(err).ToNot(HaveOccurred())

			Expect(linodeEnv.LinodeAPI.UpdateInstanceBehavior.Calls()).To(Equal(1))

			updatedInst, _ := linodeEnv.LinodeAPI.Instances.Load(instanceID)
			Expect(updatedInst.(linodego.Instance).Tags).To(ContainElement("existing-tag"))
			Expect(updatedInst.(linodego.Instance).Tags).To(ContainElement(fmt.Sprintf("%s:test-node", v1.NameTagKey)))
			Expect(updatedInst.(linodego.Instance).Tags).To(ContainElement(fmt.Sprintf("%s:test-claim", v1.NodeClaimTagKey)))
		})

		It("should return error when instance not found", func() {
			err := linodeEnv.LKENodeProvider.CreateTags(ctx, "999999", map[string]string{"test": "value"})
			Expect(err).To(HaveOccurred())
		})

		It("should return error for invalid instance ID", func() {
			err := linodeEnv.LKENodeProvider.CreateTags(ctx, "bad-id", map[string]string{"foo": "bar"})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("parsing instance ID"))
		})

		It("should surface errors when fetching instance", func() {
			linodeEnv.LinodeAPI.GetInstanceBehavior.Error.Set(fmt.Errorf("get fail"))

			err := linodeEnv.LKENodeProvider.CreateTags(ctx, "2001", map[string]string{"foo": "bar"})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("get fail"))
		})

		It("should surface update failures", func() {
			instanceID := 9101
			now := time.Now()
			inst := linodego.Instance{ID: instanceID, Tags: []string{}, Created: &now}
			linodeEnv.LinodeAPI.Instances.Store(instanceID, inst)
			linodeEnv.LinodeAPI.UpdateInstanceBehavior.Error.Set(fmt.Errorf("boom"))

			err := linodeEnv.LKENodeProvider.CreateTags(ctx, strconv.Itoa(instanceID), map[string]string{"foo": "bar"})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("boom"))
		})
	})

	Context("Create options", func() {
		It("should propagate optional NodeClass fields", func() {
			firewallID := 123
			version := "1.29"
			strategy := linodego.LKENodePoolUpdateStrategy("rolling_update")
			nodeClass.Spec.FirewallID = lo.ToPtr(firewallID)
			nodeClass.Spec.LKEK8sVersion = lo.ToPtr(version)
			nodeClass.Spec.LKEUpdateStrategy = &strategy

			ExpectApplied(ctx, env.Client, nodeClaim, nodePoolObj, nodeClass)
			nodeClass = ExpectExists(ctx, env.Client, nodeClass)
			instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
			Expect(err).ToNot(HaveOccurred())

			_, _ = linodeEnv.LKENodeProvider.Create(ctx, nodeClass, nodeClaim, map[string]string{}, instanceTypes)

			input := linodeEnv.LinodeAPI.CreateLKENodePoolBehavior.CalledWithInput.At(0)
			Expect(input.Opts.FirewallID).ToNot(BeNil())
			Expect(*input.Opts.FirewallID).To(Equal(firewallID))
			Expect(input.Opts.K8sVersion).ToNot(BeNil())
			Expect(*input.Opts.K8sVersion).To(Equal(version))
			Expect(input.Opts.UpdateStrategy).ToNot(BeNil())
			Expect(*input.Opts.UpdateStrategy).To(Equal(strategy))
		})
	})
})
