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

package cloudprovider_test

import (
	"context"
	"testing"
	"time"

	"github.com/awslabs/operatorpkg/object"
	opstatus "github.com/awslabs/operatorpkg/status"
	"github.com/linode/linodego"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	clock "k8s.io/utils/clock/testing"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	corecloudprovider "sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/controllers/provisioning"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/events"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
	coretest "sigs.k8s.io/karpenter/pkg/test"
	testv1alpha1 "sigs.k8s.io/karpenter/pkg/test/v1alpha1"

	"github.com/linode/karpenter-provider-linode/pkg/fake"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
	"github.com/linode/karpenter-provider-linode/pkg/cloudprovider"
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
var prov *provisioning.Provisioner
var cloudProvider *cloudprovider.CloudProvider
var cluster *state.Cluster
var fakeClock *clock.FakeClock
var recorder events.Recorder

func TestLinode(t *testing.T) {
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "cloudProvider/Linode")
}

var _ = BeforeSuite(func() {
	env = coretest.NewEnvironment(
		coretest.WithCRDs(apis.CRDs...),
		coretest.WithCRDs(testv1alpha1.CRDs...),
		coretest.WithFieldIndexers(coretest.NodePoolNodeClassRefFieldIndexer(ctx)),
	)

	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: lo.ToPtr(true)}}))
	ctx = options.ToContext(ctx, test.Options())
	ctx, stop = context.WithCancel(ctx)
	linodeEnv = test.NewEnvironment(ctx)
	fakeClock = clock.NewFakeClock(time.Now())
	recorder = events.NewRecorder(&record.FakeRecorder{})
	cloudProvider = cloudprovider.New(linodeEnv.InstanceTypesProvider, linodeEnv.InstanceProvider, recorder, env.Client)
	cluster = state.NewCluster(fakeClock, env.Client, cloudProvider)
	prov = provisioning.NewProvisioner(env.Client, recorder, cloudProvider, cluster, fakeClock)
})

var _ = AfterSuite(func() {
	stop()
	Expect(env.Stop()).To(Succeed(), "Failed to stop environment")
})

var _ = BeforeEach(func() {
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: lo.ToPtr(true)}}))
	ctx = options.ToContext(ctx, test.Options())

	cluster.Reset()
	linodeEnv.Reset()
})

var _ = AfterEach(func() {
	ExpectCleanedUp(ctx, env.Client)
})

var _ = Describe("CloudProvider", func() {
	var nodeClass *v1.LinodeNodeClass
	var nodePool *karpv1.NodePool
	var nodeClaim *karpv1.NodeClaim
	var _ = BeforeEach(func() {
		nodeClass = test.LinodeNodeClass(
			v1.LinodeNodeClass{
				Spec: v1.LinodeNodeClassSpec{
					Type:  "g6-standard-2",
					Image: "linode/ubuntu22.04",
				},
				Status: v1.LinodeNodeClassStatus{},
			},
		)
		nodeClass.StatusConditions().SetTrue(opstatus.ConditionReady)
		nodePool = coretest.NodePool(karpv1.NodePool{
			Spec: karpv1.NodePoolSpec{
				Template: karpv1.NodeClaimTemplate{
					Spec: karpv1.NodeClaimTemplateSpec{
						NodeClassRef: &karpv1.NodeClassReference{
							Group: object.GVK(nodeClass).Group,
							Kind:  object.GVK(nodeClass).Kind,
							Name:  nodeClass.Name,
						},
						Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
							{
								NodeSelectorRequirement: corev1.NodeSelectorRequirement{
									Key:      karpv1.CapacityTypeLabelKey,
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{karpv1.CapacityTypeOnDemand},
								}},
						},
					},
				},
			},
		})
		nodeClaim = coretest.NodeClaim(karpv1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{karpv1.NodePoolLabelKey: nodePool.Name},
			},
			Spec: karpv1.NodeClaimSpec{
				NodeClassRef: &karpv1.NodeClassReference{
					Group: object.GVK(nodeClass).Group,
					Kind:  object.GVK(nodeClass).Kind,
					Name:  nodeClass.Name,
				},
				Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
					{
						NodeSelectorRequirement: corev1.NodeSelectorRequirement{
							Key:      karpv1.CapacityTypeLabelKey,
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{karpv1.CapacityTypeOnDemand},
						},
					},
				},
			},
		})
		Expect(linodeEnv.InstanceTypesProvider.UpdateInstanceTypes(ctx)).To(Succeed())
		Expect(linodeEnv.InstanceTypesProvider.UpdateInstanceTypeOfferings(ctx)).To(Succeed())
	})
	It("should not proceed with instance creation if NodeClass is unknown", func() {
		nodeClass.StatusConditions().SetUnknown(opstatus.ConditionReady)
		ExpectApplied(ctx, env.Client, nodePool, nodeClass, nodeClaim)
		_, err := cloudProvider.Create(ctx, nodeClaim)
		Expect(err).To(HaveOccurred())
		Expect(corecloudprovider.IsNodeClassNotReadyError(err)).To(BeFalse())
	})
	It("should return NodeClassNotReady error on creation if NodeClass is not ready", func() {
		nodeClass.StatusConditions().SetFalse(opstatus.ConditionReady, "NodeClassNotReady", "NodeClass not ready")
		ExpectApplied(ctx, env.Client, nodePool, nodeClass, nodeClaim)
		_, err := cloudProvider.Create(ctx, nodeClaim)
		Expect(err).To(HaveOccurred())
		Expect(corecloudprovider.IsNodeClassNotReadyError(err)).To(BeTrue())
	})
	It("should return NodeClassNotReady error on creation if NodeClass tag validation fails", func() {
		ExpectApplied(ctx, env.Client, nodePool, nodeClass, nodeClaim)
		nodeClass.Spec.Tags = []string{"kubernetes.io/cluster/thewrongcluster:owned"}
		ExpectApplied(ctx, env.Client, nodeClass)
		_, err := cloudProvider.Create(ctx, nodeClaim)
		Expect(err).To(HaveOccurred())
		Expect(corecloudprovider.IsNodeClassNotReadyError(err)).To(BeTrue())
	})
	It("should return NodeClassNotReady error when observed generation doesn't match", func() {
		nodeClass.Generation = 2
		nodeClass.StatusConditions().SetTrue(opstatus.ConditionReady)
		nodeClass.StatusConditions().Get(opstatus.ConditionReady).ObservedGeneration = 1
		ExpectApplied(ctx, env.Client, nodePool, nodeClass, nodeClaim)
		_, err := cloudProvider.Create(ctx, nodeClaim)
		Expect(err).To(HaveOccurred())
		Expect(corecloudprovider.IsNodeClassNotReadyError(err)).To(BeTrue())
	})
	It("should return an ICE error when there are no instance types to launch", func() {
		// Specify no instance types and expect to receive a capacity error
		nodeClaim.Spec.Requirements = []karpv1.NodeSelectorRequirementWithMinValues{
			{
				NodeSelectorRequirement: corev1.NodeSelectorRequirement{
					Key:      corev1.LabelInstanceTypeStable,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{"test-instance-type"},
				},
			},
		}
		ExpectApplied(ctx, env.Client, nodePool, nodeClass, nodeClaim)
		cloudProviderNodeClaim, err := cloudProvider.Create(ctx, nodeClaim)
		Expect(corecloudprovider.IsInsufficientCapacityError(err)).To(BeTrue())
		Expect(cloudProviderNodeClaim).To(BeNil())
	})
	It("should set ImageID in the status field of the nodeClaim", func() {
		ExpectApplied(ctx, env.Client, nodePool, nodeClass, nodeClaim)
		cloudProviderNodeClaim, err := cloudProvider.Create(ctx, nodeClaim)
		Expect(err).To(BeNil())
		Expect(cloudProviderNodeClaim).ToNot(BeNil())
		Expect(cloudProviderNodeClaim.Status.ImageID).ToNot(BeEmpty())
	})
	It("should expect a strict set of annotation keys", func() {
		ExpectApplied(ctx, env.Client, nodePool, nodeClass, nodeClaim)
		cloudProviderNodeClaim, err := cloudProvider.Create(ctx, nodeClaim)
		Expect(err).To(BeNil())
		Expect(cloudProviderNodeClaim).ToNot(BeNil())
		Expect(len(lo.Keys(cloudProviderNodeClaim.Annotations))).To(BeNumerically("==", 2))
		Expect(lo.Keys(cloudProviderNodeClaim.Annotations)).To(ContainElements(v1.AnnotationLinodeNodeClassHash, v1.AnnotationLinodeNodeClassHashVersion))
	})
	It("should return NodeClass Hash on the nodeClaim", func() {
		ExpectApplied(ctx, env.Client, nodePool, nodeClass, nodeClaim)
		cloudProviderNodeClaim, err := cloudProvider.Create(ctx, nodeClaim)
		Expect(err).To(BeNil())
		Expect(cloudProviderNodeClaim).ToNot(BeNil())
		_, ok := cloudProviderNodeClaim.Annotations[v1.AnnotationLinodeNodeClassHash]
		Expect(ok).To(BeTrue())
	})
	It("should return NodeClass Hash Version on the nodeClaim", func() {
		ExpectApplied(ctx, env.Client, nodePool, nodeClass, nodeClaim)
		cloudProviderNodeClaim, err := cloudProvider.Create(ctx, nodeClaim)
		Expect(err).To(BeNil())
		Expect(cloudProviderNodeClaim).ToNot(BeNil())
		v, ok := cloudProviderNodeClaim.Annotations[v1.AnnotationLinodeNodeClassHashVersion]
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal(v1.LinodeNodeClassHashVersion))
	})
	Context("MinValues", func() {
		It("CreateInstances input should respect minValues for In operator requirement from NodePool", func() {
			// Create fake InstanceTypes where one instances can fit 2 pods and another one can fit only 1 pod.
			// This specific type of inputs will help us differentiate the scenario we are trying to test where ideally
			// 1 instance launch would have been sufficient to fit the pods and was cheaper but we would launch 2 separate
			// instances to meet the minimum requirement.
			linodeInstanceTypeInfo := fake.MakeInstances()
			linodeInstanceTypeInfo[0].VCPUs = 1
			linodeInstanceTypeInfo[1].VCPUs = 8
			linodeEnv.LinodeAPI.ListTypesOutput.Set(&linodeInstanceTypeInfo)
			linodeOfferings := fake.MakeInstanceOfferings(linodeInstanceTypeInfo)
			linodeEnv.LinodeAPI.ListRegionsAvailabilityOutput.Set(&linodeOfferings)
			Expect(linodeEnv.InstanceTypesProvider.UpdateInstanceTypes(ctx)).To(Succeed())
			Expect(linodeEnv.InstanceTypesProvider.UpdateInstanceTypeOfferings(ctx)).To(Succeed())
			instanceNames := lo.Map(linodeInstanceTypeInfo, func(info linodego.LinodeType, _ int) string { return info.ID })

			// Define NodePool that has minValues on instance-type requirement.
			nodePool = coretest.NodePool(karpv1.NodePool{
				Spec: karpv1.NodePoolSpec{
					Template: karpv1.NodeClaimTemplate{
						Spec: karpv1.NodeClaimTemplateSpec{
							NodeClassRef: &karpv1.NodeClassReference{
								Group: object.GVK(nodeClass).Group,
								Kind:  object.GVK(nodeClass).Kind,
								Name:  nodeClass.Name,
							},
							Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
								{
									NodeSelectorRequirement: corev1.NodeSelectorRequirement{
										Key:      karpv1.CapacityTypeLabelKey,
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{karpv1.CapacityTypeOnDemand},
									},
								},
								{
									NodeSelectorRequirement: corev1.NodeSelectorRequirement{
										Key:      corev1.LabelInstanceTypeStable,
										Operator: corev1.NodeSelectorOpIn,
										Values:   instanceNames,
									},
									MinValues: lo.ToPtr(2),
								},
							},
						},
					},
				},
			})

			ExpectApplied(ctx, env.Client, nodePool, nodeClass)

			// 2 pods are created with resources such that both fit together only in one of the 2 InstanceTypes created above.
			pod1 := coretest.UnschedulablePod(
				coretest.PodOptions{
					ResourceRequirements: corev1.ResourceRequirements{Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("0.9")},
					},
				})
			pod2 := coretest.UnschedulablePod(
				coretest.PodOptions{
					ResourceRequirements: corev1.ResourceRequirements{Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("0.9")},
					},
				})
			ExpectProvisioned(ctx, env.Client, cluster, cloudProvider, prov, pod1, pod2)

			// Under normal circumstances 1 node would have been created that fits both the pods but
			// here minValue enforces to include both the instances. And since one of the instances can
			// only fit 1 pod, only 1 pod is scheduled to run in the node to be launched by CreateInstances.

			// FIXME
			/* node1 := ExpectScheduled(ctx, env.Client, pod1)
			node2 := ExpectScheduled(ctx, env.Client, pod2)

			// This ensures that the pods are scheduled in 2 different nodes.
			Expect(node1.Name).ToNot(Equal(node2.Name))
			Expect(linodeEnv.LinodeAPI.CreateInstanceBehavior.Calls() == 2).To(BeTrue()) */
		})
	})
})
