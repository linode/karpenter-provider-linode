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

	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/awslabs/operatorpkg/object"
	opstatus "github.com/awslabs/operatorpkg/status"
	"github.com/linode/linodego"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	clock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	corecloudprovider "sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/controllers/provisioning"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/events"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
	"sigs.k8s.io/karpenter/pkg/scheduling"
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

var _ = Describe("CloudProvider", func() {
	var nodeClass *v1.LinodeNodeClass
	var nodePool *karpv1.NodePool
	var nodeClaim *karpv1.NodeClaim
	var _ = BeforeEach(func() {
		// Override context with instance mode for direct Linode instance tests
		ctx = options.ToContext(ctx, test.Options(test.OptionsFields{Mode: lo.ToPtr("instance")}))
		// Create CloudProvider with instance provider for this test suite
		cloudProvider = cloudprovider.New(linodeEnv.InstanceTypesProvider, linodeEnv.InstanceProvider, recorder, env.Client)
		cluster = state.NewCluster(fakeClock, env.Client, cloudProvider)
		prov = provisioning.NewProvisioner(env.Client, recorder, cloudProvider, cluster, fakeClock)
		// Use LinodeNodeClassWithoutLKE for direct Linode instance tests
		nodeClass = test.LinodeNodeClassWithoutLKE(
			v1.LinodeNodeClass{
				Spec: v1.LinodeNodeClassSpec{
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
	It("should return with NotFound if NodeClass is terminating", func() {
		nodeClass.DeletionTimestamp = &metav1.Time{Time: time.Now()}
		ExpectApplied(ctx, env.Client, nodePool, nodeClass, nodeClaim)
		_, err := cloudProvider.Create(ctx, nodeClaim)
		Expect(err).To(HaveOccurred())
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})
	It("should return InsufficientCapacityError error on creation if NodeClass does not exist", func() {
		ExpectApplied(ctx, env.Client, nodePool, nodeClaim)
		_, err := cloudProvider.Create(ctx, nodeClaim)
		Expect(err).To(HaveOccurred())
		Expect(corecloudprovider.IsInsufficientCapacityError(err)).To(BeTrue())
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
										Key:      v1.LabelInstanceClass,
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{"standard"},
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
						corev1.ResourceCPU: resource.MustParse("3.9")},
					},
				})
			pod2 := coretest.UnschedulablePod(
				coretest.PodOptions{
					ResourceRequirements: corev1.ResourceRequirements{Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("3.9")},
					},
				})
			ExpectProvisioned(ctx, env.Client, cluster, cloudProvider, prov, pod1, pod2)

			// Under normal circumstances 1 node would have been created that fits both the pods but
			// here minValue enforces to include both the instances. And since one of the instances can
			// only fit 1 pod, only 1 pod is scheduled to run in the node to be launched by CreateInstances.
			node1 := ExpectScheduled(ctx, env.Client, pod1)
			node2 := ExpectScheduled(ctx, env.Client, pod2)

			// This ensures that the pods are scheduled in 2 different nodes.
			Expect(node1.Name).ToNot(Equal(node2.Name))
			Expect(linodeEnv.LinodeAPI.CreateInstanceBehavior.Calls() == 2).To(BeTrue())
		})
	})
	Context("NodeClaim Drift", func() {
		var selectedInstanceType *corecloudprovider.InstanceType
		var instance linodego.Instance
		BeforeEach(func() {
			ExpectApplied(ctx, env.Client, nodePool, nodeClass)
			instanceTypes, err := cloudProvider.GetInstanceTypes(ctx, nodePool)
			Expect(err).ToNot(HaveOccurred())
			var ok bool
			selectedInstanceType, ok = lo.Find(instanceTypes, func(i *corecloudprovider.InstanceType) bool {
				return i.Requirements.Compatible(scheduling.NewLabelRequirements(map[string]string{
					corev1.LabelArchStable: karpv1.ArchitectureAmd64,
				})) == nil
			})
			Expect(ok).To(BeTrue())

			// Create the instance we want returned from the Linode API
			instance = linodego.Instance{
				Type:   selectedInstanceType.Name,
				Status: linodego.InstanceRunning,
				ID:     fake.InstanceID(),
				Region: fake.DefaultRegion,
			}
			linodeEnv.LinodeAPI.GetInstanceBehavior.Output.Set(ptr.To(ptr.To(instance)))
			nodeClass.Annotations = lo.Assign(nodeClass.Annotations, map[string]string{
				v1.AnnotationLinodeNodeClassHash:        nodeClass.Hash(),
				v1.AnnotationLinodeNodeClassHashVersion: v1.LinodeNodeClassHashVersion,
			})
			nodeClaim.Status.ProviderID = fake.ProviderID(instance.ID)
			nodeClaim.Annotations = lo.Assign(nodeClaim.Annotations, map[string]string{
				v1.AnnotationLinodeNodeClassHash:        nodeClass.Hash(),
				v1.AnnotationLinodeNodeClassHashVersion: v1.LinodeNodeClassHashVersion,
			})
			nodeClaim.Labels = lo.Assign(nodeClaim.Labels, map[string]string{corev1.LabelInstanceTypeStable: selectedInstanceType.Name})
		})
		It("should not fail if NodeClass does not exist", func() {
			ExpectDeleted(ctx, env.Client, nodeClass)
			drifted, err := cloudProvider.IsDrifted(ctx, nodeClaim)
			Expect(err).ToNot(HaveOccurred())
			Expect(drifted).To(BeEmpty())
		})
		It("should not fail if NodePool does not exist", func() {
			ExpectDeleted(ctx, env.Client, nodePool)
			drifted, err := cloudProvider.IsDrifted(ctx, nodeClaim)
			Expect(err).ToNot(HaveOccurred())
			Expect(drifted).To(BeEmpty())
		})
		It("should return drifted if there are multiple drift reasons", func() {
			// Instance is a reference to what we return in the GetInstances call
			instance.ID = fake.InstanceID()
			linodeEnv.LinodeAPI.GetInstanceBehavior.Output.Set(ptr.To(ptr.To(instance)))

			// Assign a fake hash
			nodeClass.Annotations = lo.Assign(nodeClass.Annotations, map[string]string{
				v1.AnnotationLinodeNodeClassHash: "abcdefghijkl",
			})
			ExpectApplied(ctx, env.Client, nodeClass)
			isDrifted, err := cloudProvider.IsDrifted(ctx, nodeClaim)
			Expect(err).ToNot(HaveOccurred())
			Expect(isDrifted).To(Equal(cloudprovider.NodeClassDrift))
		})
		It("should not return drifted if the NodeClaim is valid", func() {
			isDrifted, err := cloudProvider.IsDrifted(ctx, nodeClaim)
			Expect(err).ToNot(HaveOccurred())
			Expect(isDrifted).To(BeEmpty())
		})
		Context("Static Drift Detection", func() {
			BeforeEach(func() {
				nodeClass = &v1.LinodeNodeClass{
					ObjectMeta: nodeClass.ObjectMeta,
					Spec: v1.LinodeNodeClassSpec{
						Tags: []string{
							"fakeKey:fakeValue",
						},
					},
					Status: v1.LinodeNodeClassStatus{},
				}
				nodeClass.Annotations = lo.Assign(nodeClass.Annotations, map[string]string{v1.AnnotationLinodeNodeClassHash: nodeClass.Hash()})
				nodeClaim.Annotations = lo.Assign(nodeClaim.Annotations, map[string]string{v1.AnnotationLinodeNodeClassHash: nodeClass.Hash()})
			})
			It("should not return drifted if karpenter.k8s.linode/LinodeNodeclass-hash annotation is not present on the NodeClaim", func() {
				nodeClaim.Annotations = map[string]string{
					v1.AnnotationLinodeNodeClassHashVersion: v1.LinodeNodeClassHashVersion,
				}
				nodeClass.Spec.Tags = []string{
					"Test Key:Test Value",
				}
				ExpectApplied(ctx, env.Client, nodePool, nodeClass)
				isDrifted, err := cloudProvider.IsDrifted(ctx, nodeClaim)
				Expect(err).NotTo(HaveOccurred())
				Expect(isDrifted).To(BeEmpty())
			})
			It("should not return drifted if the NodeClaim's karpenter.k8s.linode/LinodeNodeclass-hash-version annotation does not match the LinodeNodeClass's", func() {
				nodeClass.Annotations = map[string]string{
					v1.AnnotationLinodeNodeClassHash:        "test-hash-111111",
					v1.AnnotationLinodeNodeClassHashVersion: "test-hash-version-1",
				}
				nodeClaim.Annotations = map[string]string{
					v1.AnnotationLinodeNodeClassHash:        "test-hash-222222",
					v1.AnnotationLinodeNodeClassHashVersion: "test-hash-version-2",
				}
				ExpectApplied(ctx, env.Client, nodePool, nodeClass)
				isDrifted, err := cloudProvider.IsDrifted(ctx, nodeClaim)
				Expect(err).NotTo(HaveOccurred())
				Expect(isDrifted).To(BeEmpty())
			})
			It("should not return drifted if karpenter.k8s.linode/LinodeNodeclass-hash-version annotation is not present on the NodeClass", func() {
				nodeClass.Annotations = map[string]string{
					v1.AnnotationLinodeNodeClassHash: "test-hash-111111",
				}
				nodeClaim.Annotations = map[string]string{
					v1.AnnotationLinodeNodeClassHash:        "test-hash-222222",
					v1.AnnotationLinodeNodeClassHashVersion: "test-hash-version-2",
				}
				// should trigger drift
				nodeClass.Spec.Tags = []string{
					"Test Key:Test Value",
				}
				ExpectApplied(ctx, env.Client, nodePool, nodeClass)
				isDrifted, err := cloudProvider.IsDrifted(ctx, nodeClaim)
				Expect(err).NotTo(HaveOccurred())
				Expect(isDrifted).To(BeEmpty())
			})
			It("should not return drifted if karpenter.k8s.linode/LinodeNodeclass-hash-version annotation is not present on the NodeClaim", func() {
				nodeClass.Annotations = map[string]string{
					v1.AnnotationLinodeNodeClassHash:        "test-hash-111111",
					v1.AnnotationLinodeNodeClassHashVersion: "test-hash-version-1",
				}
				nodeClaim.Annotations = map[string]string{
					v1.AnnotationLinodeNodeClassHash: "test-hash-222222",
				}
				// should trigger drift
				nodeClass.Spec.Tags = []string{
					"Test Key:Test Value",
				}
				ExpectApplied(ctx, env.Client, nodePool, nodeClass)
				isDrifted, err := cloudProvider.IsDrifted(ctx, nodeClaim)
				Expect(err).NotTo(HaveOccurred())
				Expect(isDrifted).To(BeEmpty())
			})
		})
	})
})

var _ = Describe("CloudProvider LKE Mode", func() {
	var lkeNodeClass *v1.LinodeNodeClass
	var lkeNodePool *karpv1.NodePool
	var lkeNodeClaim *karpv1.NodeClaim
	var _ = BeforeEach(func() {
		// Override context with lke mode for LKE-managed mode tests
		ctx = options.ToContext(ctx, test.Options(test.OptionsFields{Mode: lo.ToPtr("lke")}))
		// Create CloudProvider with LKE provider for this test suite
		cloudProvider = cloudprovider.New(linodeEnv.InstanceTypesProvider, linodeEnv.LKENodeProvider, recorder, env.Client)
		cluster = state.NewCluster(fakeClock, env.Client, cloudProvider)
		prov = provisioning.NewProvisioner(env.Client, recorder, cloudProvider, cluster, fakeClock)
		// Use LinodeNodeClass for LKE-managed mode tests
		lkeNodeClass = test.LinodeNodeClass()
		lkeNodeClass.StatusConditions().SetTrue(opstatus.ConditionReady)
		lkeNodePool = coretest.NodePool(karpv1.NodePool{
			Spec: karpv1.NodePoolSpec{
				Template: karpv1.NodeClaimTemplate{
					Spec: karpv1.NodeClaimTemplateSpec{
						NodeClassRef: &karpv1.NodeClassReference{
							Group: object.GVK(lkeNodeClass).Group,
							Kind:  object.GVK(lkeNodeClass).Kind,
							Name:  lkeNodeClass.Name,
						},
					},
				},
			},
		})
		lkeNodeClaim = coretest.NodeClaim(karpv1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{karpv1.NodePoolLabelKey: lkeNodePool.Name},
			},
			Spec: karpv1.NodeClaimSpec{
				NodeClassRef: &karpv1.NodeClassReference{
					Group: object.GVK(lkeNodeClass).Group,
					Kind:  object.GVK(lkeNodeClass).Kind,
					Name:  lkeNodeClass.Name,
				},
			},
		})
		Expect(linodeEnv.InstanceTypesProvider.UpdateInstanceTypes(ctx)).To(Succeed())
		Expect(linodeEnv.InstanceTypesProvider.UpdateInstanceTypeOfferings(ctx)).To(Succeed())
	})

	Context("Create", func() {
		It("should create LKE node pool in LKE mode", func() {
			ExpectApplied(ctx, env.Client, lkeNodePool, lkeNodeClass, lkeNodeClaim)
			cloudProviderNodeClaim, err := cloudProvider.Create(ctx, lkeNodeClaim)
			Expect(err).ToNot(HaveOccurred())
			Expect(cloudProviderNodeClaim).ToNot(BeNil())
			Expect(cloudProviderNodeClaim.Status.ProviderID).To(ContainSubstring("linode://"))
		})

		It("should set correct annotations on LKE nodeclaim", func() {
			ExpectApplied(ctx, env.Client, lkeNodePool, lkeNodeClass, lkeNodeClaim)
			cloudProviderNodeClaim, err := cloudProvider.Create(ctx, lkeNodeClaim)
			Expect(err).ToNot(HaveOccurred())
			Expect(cloudProviderNodeClaim).ToNot(BeNil())
			Expect(lo.Keys(cloudProviderNodeClaim.Annotations)).To(ContainElements(
				v1.AnnotationLinodeNodeClassHash,
				v1.AnnotationLinodeNodeClassHashVersion,
			))
		})

		It("should return NodeClassNotReady error when LKENodeClass is not ready", func() {
			lkeNodeClass.StatusConditions().SetFalse(opstatus.ConditionReady, "NodeClassNotReady", "NodeClass not ready")
			ExpectApplied(ctx, env.Client, lkeNodePool, lkeNodeClass, lkeNodeClaim)
			_, err := cloudProvider.Create(ctx, lkeNodeClaim)
			Expect(err).To(HaveOccurred())
			Expect(corecloudprovider.IsNodeClassNotReadyError(err)).To(BeTrue())
		})
	})

	Context("Get", func() {
		It("should return LKE node when instance exists", func() {
			ExpectApplied(ctx, env.Client, lkeNodePool, lkeNodeClass, lkeNodeClaim)
			cloudProviderNodeClaim, err := cloudProvider.Create(ctx, lkeNodeClaim)
			Expect(err).ToNot(HaveOccurred())
			Expect(cloudProviderNodeClaim).ToNot(BeNil())

			// Get the node by providerID
			retrievedNodeClaim, err := cloudProvider.Get(ctx, cloudProviderNodeClaim.Status.ProviderID)
			Expect(err).ToNot(HaveOccurred())
			Expect(retrievedNodeClaim).ToNot(BeNil())
			Expect(retrievedNodeClaim.Status.ProviderID).To(Equal(cloudProviderNodeClaim.Status.ProviderID))
		})
	})

	Context("List", func() {
		It("should include LKE nodes in list results", func() {
			ExpectApplied(ctx, env.Client, lkeNodePool, lkeNodeClass, lkeNodeClaim)
			cloudProviderNodeClaim, err := cloudProvider.Create(ctx, lkeNodeClaim)
			Expect(err).ToNot(HaveOccurred())

			nodeClaims, err := cloudProvider.List(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(nodeClaims)).To(BeNumerically(">=", 1))

			providerIDs := lo.Map(nodeClaims, func(nc *karpv1.NodeClaim, _ int) string {
				return nc.Status.ProviderID
			})
			Expect(providerIDs).To(ContainElement(cloudProviderNodeClaim.Status.ProviderID))
		})

		It("should not return duplicates for LKE-managed instances", func() {
			ExpectApplied(ctx, env.Client, lkeNodePool, lkeNodeClass, lkeNodeClaim)
			cloudProviderNodeClaim, err := cloudProvider.Create(ctx, lkeNodeClaim)
			Expect(err).ToNot(HaveOccurred())

			nodeClaims, err := cloudProvider.List(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Count how many times this providerID appears
			count := lo.CountBy(nodeClaims, func(nc *karpv1.NodeClaim) bool {
				return nc.Status.ProviderID == cloudProviderNodeClaim.Status.ProviderID
			})
			Expect(count).To(Equal(1), "LKE node should appear exactly once in list")
		})
	})

	Context("Delete", func() {
		It("should delete LKE node pool via lkenodeProvider", func() {
			ExpectApplied(ctx, env.Client, lkeNodePool, lkeNodeClass, lkeNodeClaim)
			cloudProviderNodeClaim, err := cloudProvider.Create(ctx, lkeNodeClaim)
			Expect(err).ToNot(HaveOccurred())

			// Update nodeClaim with providerID for delete
			lkeNodeClaim.Status.ProviderID = cloudProviderNodeClaim.Status.ProviderID
			ExpectApplied(ctx, env.Client, lkeNodeClaim)

			err = cloudProvider.Delete(ctx, lkeNodeClaim)
			Expect(err).ToNot(HaveOccurred())

			// Verify node is deleted
			_, err = cloudProvider.Get(ctx, cloudProviderNodeClaim.Status.ProviderID)
			Expect(err).To(HaveOccurred())
		})
	})
})
