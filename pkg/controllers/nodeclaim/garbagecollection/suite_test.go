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

package garbagecollection_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linode/linodego"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/karpenter/pkg/test/v1alpha1"

	"github.com/awslabs/operatorpkg/object"
	"github.com/samber/lo"
	"k8s.io/client-go/tools/record"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpcloudprovider "sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
	coretest "sigs.k8s.io/karpenter/pkg/test"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	"github.com/linode/karpenter-provider-linode/pkg/cloudprovider"
	"github.com/linode/karpenter-provider-linode/pkg/controllers/nodeclaim/garbagecollection"
	"github.com/linode/karpenter-provider-linode/pkg/fake"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/providers/lke"
	"github.com/linode/karpenter-provider-linode/pkg/test"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
	. "sigs.k8s.io/karpenter/pkg/utils/testing"
)

var ctx context.Context
var linodeEnv *test.Environment
var env *coretest.Environment
var garbageCollectionController *garbagecollection.Controller
var cloudProvider *cloudprovider.CloudProvider

func TestAPIs(t *testing.T) {
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "GarbageCollection")
}

var _ = BeforeSuite(func() {
	ctx = options.ToContext(ctx, test.Options())
	env = coretest.NewEnvironment(coretest.WithCRDs(apis.CRDs...), coretest.WithCRDs(v1alpha1.CRDs...))
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: lo.ToPtr(true)}}))
	linodeEnv = test.NewEnvironment(ctx)
	cloudProvider = cloudprovider.New(linodeEnv.InstanceTypesProvider, linodeEnv.InstanceProvider, events.NewRecorder(&record.FakeRecorder{}),
		env.Client)
	garbageCollectionController = garbagecollection.NewController(env.Client, cloudProvider)
})

var _ = AfterSuite(func() {
	Expect(env.Stop()).To(Succeed(), "Failed to stop environment")
})

var _ = BeforeEach(func() {
	linodeEnv.Reset()
})

var _ = Describe("GarbageCollection", func() {
	var instance *linodego.Instance
	var nodeClass *v1.LinodeNodeClass
	var providerID string

	BeforeEach(func() {
		instanceID := fake.InstanceID()
		providerID = fake.ProviderID(instanceID)
		nodeClass = test.LinodeNodeClass()
		nodePool := coretest.NodePool(karpv1.NodePool{
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
		instance = &linodego.Instance{
			Status: linodego.InstanceRunning,
			Tags: []string{
				fmt.Sprintf("kubernetes.io/cluster/%s:owned", options.FromContext(ctx).ClusterName),
				karpv1.NodePoolLabelKey + ":" + nodePool.Name,
				v1.LabelNodeClass + ":" + nodeClass.Name,
				v1.LKEClusterNameTagKey + ":" + options.FromContext(ctx).ClusterName,
			},
			Region: fake.DefaultRegion,
			ID:     instanceID,
			Type:   "g6-standard-4",
		}
	})
	AfterEach(func() {
		ExpectCleanedUp(ctx, env.Client)
	})

	It("should delete an instance if there is no NodeClaim owner", func() {
		// Launch time was 1m ago
		instance.Created = ptr.To(time.Now().Add(-time.Minute))
		linodeEnv.LinodeAPI.Instances.Store(instance.ID, *instance)

		ExpectSingletonReconciled(ctx, garbageCollectionController)
		linodeEnv.InstanceCache.Flush()
		_, err := cloudProvider.Get(ctx, providerID)
		Expect(err).To(HaveOccurred())
		Expect(karpcloudprovider.IsNodeClaimNotFoundError(err)).To(BeTrue())
	})
	It("should delete an instance along with the node if there is no NodeClaim owner (to quicken scheduling)", func() {
		// Launch time was 1m ago
		instance.Created = ptr.To(time.Now().Add(-time.Minute))
		linodeEnv.LinodeAPI.Instances.Store(instance.ID, *instance)

		node := coretest.Node(coretest.NodeOptions{
			ProviderID: providerID,
		})
		ExpectApplied(ctx, env.Client, node)

		ExpectSingletonReconciled(ctx, garbageCollectionController)
		linodeEnv.InstanceCache.Flush()
		_, err := cloudProvider.Get(ctx, providerID)
		Expect(err).To(HaveOccurred())
		Expect(karpcloudprovider.IsNodeClaimNotFoundError(err)).To(BeTrue())

		ExpectNotFound(ctx, env.Client, node)
	})
	It("should delete many instances if they all don't have NodeClaim owners", func() {
		// Generate 100 instances that have different instanceIDs
		var ids []int
		for i := 0; i < 100; i++ {
			instanceID := fake.InstanceID()
			linodeEnv.LinodeAPI.Instances.Store(
				instanceID,
				linodego.Instance{
					Status: linodego.InstanceRunning,
					Tags: []string{
						fmt.Sprintf("kubernetes.io/cluster/%s:owned", options.FromContext(ctx).ClusterName),
						karpv1.NodePoolLabelKey + ":default",
						v1.LabelNodeClass + ":default",
						v1.LKEClusterNameTagKey + ":" + options.FromContext(ctx).ClusterName,
					},
					Region: fake.DefaultRegion,
					// Launch time was 1m ago
					Created: ptr.To(time.Now().Add(-time.Minute)),
					ID:      instanceID,
					Type:    "g6-standard-4",
				},
			)
			ids = append(ids, instanceID)
		}
		ExpectSingletonReconciled(ctx, garbageCollectionController)

		wg := sync.WaitGroup{}
		for _, id := range ids {
			wg.Go(func() {
				defer GinkgoRecover()
				linodeEnv.InstanceCache.Flush()
				_, err := cloudProvider.Get(ctx, fake.ProviderID(id))
				Expect(err).To(HaveOccurred())
				Expect(karpcloudprovider.IsNodeClaimNotFoundError(err)).To(BeTrue())
			})
		}
		wg.Wait()
	})
	It("should not delete all instances if they all have NodeClaim owners", func() {
		// Generate 100 instances that have different instanceIDs
		var ids []int
		var nodeClaims []*karpv1.NodeClaim
		for i := 0; i < 100; i++ {
			instanceID := fake.InstanceID()
			linodeEnv.LinodeAPI.Instances.Store(
				instanceID,
				linodego.Instance{
					Status: linodego.InstanceRunning,
					Tags: []string{
						fmt.Sprintf("kubernetes.io/cluster/%s:owned", options.FromContext(ctx).ClusterName),
					},
					Region: fake.DefaultRegion,
					// Launch time was 1m ago
					Created: ptr.To(time.Now().Add(-time.Minute)),
					ID:      instanceID,
					Type:    "g6-standard-4",
				},
			)
			nodeClaim := coretest.NodeClaim(karpv1.NodeClaim{
				Spec: karpv1.NodeClaimSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: object.GVK(nodeClass).Group,
						Kind:  object.GVK(nodeClass).Kind,
						Name:  nodeClass.Name,
					},
				},
				Status: karpv1.NodeClaimStatus{
					ProviderID: fake.ProviderID(instanceID),
				},
			})
			ExpectApplied(ctx, env.Client, nodeClaim)
			nodeClaims = append(nodeClaims, nodeClaim)
			ids = append(ids, instanceID)
		}
		ExpectSingletonReconciled(ctx, garbageCollectionController)

		wg := sync.WaitGroup{}
		for _, id := range ids {
			wg.Go(func() {
				defer GinkgoRecover()
				_, err := cloudProvider.Get(ctx, fake.ProviderID(id))
				Expect(err).ToNot(HaveOccurred())
			})
		}
		wg.Wait()

		for _, nodeClaim := range nodeClaims {
			ExpectExists(ctx, env.Client, nodeClaim)
		}
	})
	It("should not delete an instance if it is within the NodeClaim resolution window (1m)", func() {
		// Launch time just happened
		instance.Created = ptr.To(time.Now())
		linodeEnv.LinodeAPI.Instances.Store(instance.ID, *instance)

		ExpectSingletonReconciled(ctx, garbageCollectionController)
		_, err := cloudProvider.Get(ctx, providerID)
		Expect(err).NotTo(HaveOccurred())
	})
	It("should not delete an instance if it was not launched by a NodeClaim", func() {
		// Remove the "karpenter.sh/nodepool" tag (this isn't launched by a machine)
		instance.Tags = lo.Reject(instance.Tags, func(t string, _ int) bool {
			parts := strings.Split(t, ":")
			return parts[0] == karpv1.NodePoolLabelKey
		})

		// Launch time was 1m ago
		instance.Created = ptr.To(time.Now().Add(-time.Minute))
		linodeEnv.LinodeAPI.Instances.Store(instance.ID, *instance)

		ExpectSingletonReconciled(ctx, garbageCollectionController)
		_, err := cloudProvider.Get(ctx, providerID)
		Expect(err).NotTo(HaveOccurred())
	})
	It("should not delete the instance or node if it already has a NodeClaim that matches it", func() {
		// Launch time was 1m ago
		instance.Created = ptr.To(time.Now().Add(-time.Minute))
		linodeEnv.LinodeAPI.Instances.Store(instance.ID, *instance)

		nodeClaim := coretest.NodeClaim(karpv1.NodeClaim{
			Spec: karpv1.NodeClaimSpec{
				NodeClassRef: &karpv1.NodeClassReference{
					Group: object.GVK(nodeClass).Group,
					Kind:  object.GVK(nodeClass).Kind,
					Name:  nodeClass.Name,
				},
			},
			Status: karpv1.NodeClaimStatus{
				ProviderID: providerID,
			},
		})
		node := coretest.Node(coretest.NodeOptions{
			ProviderID: providerID,
		})
		ExpectApplied(ctx, env.Client, nodeClaim, node)

		ExpectSingletonReconciled(ctx, garbageCollectionController)
		_, err := cloudProvider.Get(ctx, providerID)
		Expect(err).ToNot(HaveOccurred())
		ExpectExists(ctx, env.Client, node)
	})
	It("should not delete many instances or nodes if they already have NodeClaim owners that match it", func() {
		// Generate 100 instances that have different instanceIDs that have NodeClaims
		var ids []int
		var nodes []*corev1.Node
		for i := 0; i < 100; i++ {
			instanceID := fake.InstanceID()
			linodeEnv.LinodeAPI.Instances.Store(
				instanceID,
				linodego.Instance{
					Status: linodego.InstanceRunning,
					Tags: []string{
						fmt.Sprintf("kubernetes.io/cluster/%s:owned", options.FromContext(ctx).ClusterName),
						karpv1.NodePoolLabelKey + ":default",
						v1.LKEClusterNameTagKey + ":" + options.FromContext(ctx).ClusterName,
					},
					Region: fake.DefaultRegion,
					// Launch time was 1m ago
					Created: ptr.To(time.Now().Add(-time.Minute)),
					ID:      instanceID,
					Type:    "g6-standard-4",
				},
			)
			nodeClaim := coretest.NodeClaim(karpv1.NodeClaim{
				Spec: karpv1.NodeClaimSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: object.GVK(nodeClass).Group,
						Kind:  object.GVK(nodeClass).Kind,
						Name:  nodeClass.Name,
					},
				},
				Status: karpv1.NodeClaimStatus{
					ProviderID: fake.ProviderID(instanceID),
				},
			})
			node := coretest.Node(coretest.NodeOptions{
				ProviderID: fake.ProviderID(instanceID),
			})
			ExpectApplied(ctx, env.Client, nodeClaim, node)
			ids = append(ids, instanceID)
			nodes = append(nodes, node)
		}
		ExpectSingletonReconciled(ctx, garbageCollectionController)

		wg := sync.WaitGroup{}
		for i := range ids {
			wg.Go(func() {
				defer GinkgoRecover()

				_, err := cloudProvider.Get(ctx, fake.ProviderID(ids[i]))
				Expect(err).ToNot(HaveOccurred())
				ExpectExists(ctx, env.Client, nodes[i])
			})
		}
		wg.Wait()
	})
})

var _ = Describe("GarbageCollection LKE Mode", func() {
	var nodeClass *v1.LinodeNodeClass
	var nodePoolObj *karpv1.NodePool
	var lkeCloudProvider *cloudprovider.CloudProvider
	var lkeGCController *garbagecollection.Controller

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
		lkeCloudProvider = cloudprovider.New(linodeEnv.InstanceTypesProvider, linodeEnv.LKENodeProvider, events.NewRecorder(&record.FakeRecorder{}),
			env.Client)
		lkeGCController = garbagecollection.NewController(env.Client, lkeCloudProvider)
	})

	AfterEach(func() {
		ExpectCleanedUp(ctx, env.Client)
	})

	It("should delete an LKE node if there is no NodeClaim owner", func() {
		poolID := 501
		instanceID := 6001
		providerID := fake.ProviderID(instanceID)
		poolTags := []string{
			fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
			fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
		}
		pool := &linodego.LKENodePool{
			ID:      poolID,
			Type:    "g6-standard-2",
			Tags:    poolTags,
			Linodes: []linodego.LKENodePoolLinode{{InstanceID: instanceID, ID: "node-6001"}},
		}
		linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

		now := time.Now().Add(-time.Minute)
		instTags := poolTags
		inst := linodego.Instance{ID: instanceID, Type: "g6-standard-2", Region: fake.DefaultRegion, Created: &now, Tags: instTags, LKEClusterID: fake.DefaultClusterID}
		linodeEnv.LinodeAPI.Instances.Store(instanceID, inst)

		ExpectSingletonReconciled(ctx, lkeGCController)

		_, err := lkeCloudProvider.Get(ctx, providerID)
		Expect(err).To(HaveOccurred())
		Expect(karpcloudprovider.IsNodeClaimNotFoundError(err)).To(BeTrue())
		Expect(linodeEnv.LinodeAPI.DeleteLKENodePoolBehavior.Calls()).To(Equal(1))
	})

	It("should delete individual LKE node when pool has multiple nodes and no NodeClaim owner", func() {
		poolID := 502
		instanceID1 := 6002
		instanceID2 := 6003
		providerID1 := fake.ProviderID(instanceID1)
		poolTags := []string{
			fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
			fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
		}
		pool := &linodego.LKENodePool{
			ID:   poolID,
			Type: "g6-standard-2",
			Tags: poolTags,
			Linodes: []linodego.LKENodePoolLinode{
				{InstanceID: instanceID1, ID: "node-6002"},
				{InstanceID: instanceID2, ID: "node-6003"},
			},
		}
		linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

		now := time.Now().Add(-time.Minute)
		instTags := poolTags
		inst1 := linodego.Instance{ID: instanceID1, Type: "g6-standard-2", Region: fake.DefaultRegion, Created: &now, Tags: instTags, LKEClusterID: fake.DefaultClusterID}
		inst2 := linodego.Instance{ID: instanceID2, Type: "g6-standard-2", Region: fake.DefaultRegion, Created: &now, Tags: instTags, LKEClusterID: fake.DefaultClusterID}
		linodeEnv.LinodeAPI.Instances.Store(instanceID1, inst1)
		linodeEnv.LinodeAPI.Instances.Store(instanceID2, inst2)

		nodeClaim := coretest.NodeClaim(karpv1.NodeClaim{
			Spec: karpv1.NodeClaimSpec{
				NodeClassRef: &karpv1.NodeClassReference{
					Group: object.GVK(nodeClass).Group,
					Kind:  object.GVK(nodeClass).Kind,
					Name:  nodeClass.Name,
				},
			},
			Status: karpv1.NodeClaimStatus{
				ProviderID: fake.ProviderID(instanceID2),
			},
		})
		ExpectApplied(ctx, env.Client, nodeClaim)

		ExpectSingletonReconciled(ctx, lkeGCController)

		_, err := lkeCloudProvider.Get(ctx, providerID1)
		Expect(err).To(HaveOccurred())
		Expect(karpcloudprovider.IsNodeClaimNotFoundError(err)).To(BeTrue())

		Expect(linodeEnv.LinodeAPI.DeleteLKENodePoolNodeBehavior.Calls()).To(Equal(1))
		Expect(linodeEnv.LinodeAPI.DeleteLKENodePoolBehavior.Calls()).To(Equal(0))
	})

	It("should not delete LKE nodes if they have NodeClaim owners", func() {
		poolID := 503
		instanceID := 6004
		providerID := fake.ProviderID(instanceID)
		poolTags := []string{
			fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
			fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
		}
		pool := &linodego.LKENodePool{
			ID:      poolID,
			Type:    "g6-standard-2",
			Tags:    poolTags,
			Linodes: []linodego.LKENodePoolLinode{{InstanceID: instanceID, ID: "node-6004"}},
		}
		linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

		now := time.Now().Add(-time.Minute)
		instTags := poolTags
		inst := linodego.Instance{ID: instanceID, Type: "g6-standard-2", Region: fake.DefaultRegion, Created: &now, Tags: instTags, LKEClusterID: fake.DefaultClusterID}
		linodeEnv.LinodeAPI.Instances.Store(instanceID, inst)

		nodeClaim := coretest.NodeClaim(karpv1.NodeClaim{
			Spec: karpv1.NodeClaimSpec{
				NodeClassRef: &karpv1.NodeClassReference{
					Group: object.GVK(nodeClass).Group,
					Kind:  object.GVK(nodeClass).Kind,
					Name:  nodeClass.Name,
				},
			},
			Status: karpv1.NodeClaimStatus{
				ProviderID: providerID,
			},
		})
		ExpectApplied(ctx, env.Client, nodeClaim)

		ExpectSingletonReconciled(ctx, lkeGCController)

		_, err := lkeCloudProvider.Get(ctx, providerID)
		Expect(err).ToNot(HaveOccurred())
		Expect(linodeEnv.LinodeAPI.DeleteLKENodePoolBehavior.Calls()).To(Equal(0))
		Expect(linodeEnv.LinodeAPI.DeleteLKENodePoolNodeBehavior.Calls()).To(Equal(0))
	})

	It("should not delete LKE nodes within the resolution window", func() {
		poolID := 504
		instanceID := 6005
		providerID := fake.ProviderID(instanceID)
		poolTags := []string{
			fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
			fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
		}
		pool := &linodego.LKENodePool{
			ID:      poolID,
			Type:    "g6-standard-2",
			Tags:    poolTags,
			Linodes: []linodego.LKENodePoolLinode{{InstanceID: instanceID, ID: "node-6005"}},
		}
		linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

		now := time.Now()
		instTags := poolTags
		inst := linodego.Instance{ID: instanceID, Type: "g6-standard-2", Region: fake.DefaultRegion, Created: &now, Tags: instTags, LKEClusterID: fake.DefaultClusterID}
		linodeEnv.LinodeAPI.Instances.Store(instanceID, inst)

		ExpectSingletonReconciled(ctx, lkeGCController)

		_, err := lkeCloudProvider.Get(ctx, providerID)
		Expect(err).ToNot(HaveOccurred())
		Expect(linodeEnv.LinodeAPI.DeleteLKENodePoolBehavior.Calls()).To(Equal(0))
		Expect(linodeEnv.LinodeAPI.DeleteLKENodePoolNodeBehavior.Calls()).To(Equal(0))
	})

	Context("Enterprise tier", func() {
		var enterpriseProvider *lke.DefaultProvider
		var enterpriseCloudProvider *cloudprovider.CloudProvider
		var enterpriseGCController *garbagecollection.Controller

		BeforeEach(func() {
			recorder := events.NewRecorder(&record.FakeRecorder{})
			enterpriseProvider = lke.NewDefaultProvider(
				fake.DefaultClusterID,
				linodego.LKEVersionEnterprise,
				fake.DefaultClusterName,
				fake.DefaultRegion,
				recorder,
				linodeEnv.LinodeAPI,
				linodeEnv.UnavailableOfferingsCache,
				linodeEnv.NodePoolCache,
				lke.ProviderConfig{},
			)
			enterpriseCloudProvider = cloudprovider.New(linodeEnv.InstanceTypesProvider, enterpriseProvider, recorder, env.Client)
			enterpriseGCController = garbagecollection.NewController(env.Client, enterpriseCloudProvider)
		})

		It("should delete an LKE node if there is no NodeClaim owner", func() {
			poolID := 601
			instanceID := 7001
			providerID := fake.ProviderID(instanceID)
			poolTags := []string{
				fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
				fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
			}
			pool := &linodego.LKENodePool{
				ID:      poolID,
				Type:    "g6-standard-2",
				Tags:    poolTags,
				Linodes: []linodego.LKENodePoolLinode{{InstanceID: instanceID, ID: "node-7001"}},
			}
			linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

			now := time.Now().Add(-time.Minute)
			instTags := append(poolTags,
				fmt.Sprintf("%s=%d", v1.LKENodePoolTagKey, poolID),
				fmt.Sprintf("lke%d", fake.DefaultClusterID),
			)
			inst := linodego.Instance{ID: instanceID, Type: "g6-standard-2", Region: fake.DefaultRegion, Created: &now, Tags: instTags, LKEClusterID: fake.DefaultClusterID, Label: "node-7001"}
			linodeEnv.LinodeAPI.Instances.Store(instanceID, inst)

			ExpectSingletonReconciled(ctx, enterpriseGCController)

			_, err := enterpriseCloudProvider.Get(ctx, providerID)
			Expect(err).To(HaveOccurred())
			Expect(karpcloudprovider.IsNodeClaimNotFoundError(err)).To(BeTrue())
			Expect(linodeEnv.LinodeAPI.DeleteLKENodePoolBehavior.Calls()).To(Equal(1))
		})

		It("should delete individual LKE node when pool has multiple nodes and no NodeClaim owner", func() {
			poolID := 602
			instanceID1 := 7002
			instanceID2 := 7003
			providerID1 := fake.ProviderID(instanceID1)
			poolTags := []string{
				fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
				fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
			}
			pool := &linodego.LKENodePool{
				ID:   poolID,
				Type: "g6-standard-2",
				Tags: poolTags,
				Linodes: []linodego.LKENodePoolLinode{
					{InstanceID: instanceID1, ID: "node-7002"},
					{InstanceID: instanceID2, ID: "node-7003"},
				},
			}
			linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

			now := time.Now().Add(-time.Minute)
			instTags := append(poolTags,
				fmt.Sprintf("%s=%d", v1.LKENodePoolTagKey, poolID),
				fmt.Sprintf("lke%d", fake.DefaultClusterID),
			)
			inst1 := linodego.Instance{ID: instanceID1, Type: "g6-standard-2", Region: fake.DefaultRegion, Created: &now, Tags: instTags, LKEClusterID: fake.DefaultClusterID, Label: "node-7002"}
			inst2 := linodego.Instance{ID: instanceID2, Type: "g6-standard-2", Region: fake.DefaultRegion, Created: &now, Tags: instTags, LKEClusterID: fake.DefaultClusterID, Label: "node-7003"}
			linodeEnv.LinodeAPI.Instances.Store(instanceID1, inst1)
			linodeEnv.LinodeAPI.Instances.Store(instanceID2, inst2)

			nodeClaim := coretest.NodeClaim(karpv1.NodeClaim{
				Spec: karpv1.NodeClaimSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: object.GVK(nodeClass).Group,
						Kind:  object.GVK(nodeClass).Kind,
						Name:  nodeClass.Name,
					},
				},
				Status: karpv1.NodeClaimStatus{
					ProviderID: fake.ProviderID(instanceID2),
				},
			})
			ExpectApplied(ctx, env.Client, nodeClaim)

			ExpectSingletonReconciled(ctx, enterpriseGCController)

			_, err := enterpriseCloudProvider.Get(ctx, providerID1)
			Expect(err).To(HaveOccurred())
			Expect(karpcloudprovider.IsNodeClaimNotFoundError(err)).To(BeTrue())
			Expect(linodeEnv.LinodeAPI.DeleteLKENodePoolNodeBehavior.Calls()).To(Equal(1))
			Expect(linodeEnv.LinodeAPI.DeleteLKENodePoolBehavior.Calls()).To(Equal(0))
		})

		It("should not delete LKE nodes if they have NodeClaim owners", func() {
			poolID := 603
			instanceID := 7004
			providerID := fake.ProviderID(instanceID)
			poolTags := []string{
				fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
				fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
			}
			pool := &linodego.LKENodePool{
				ID:      poolID,
				Type:    "g6-standard-2",
				Tags:    poolTags,
				Linodes: []linodego.LKENodePoolLinode{{InstanceID: instanceID, ID: "node-7004"}},
			}
			linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

			now := time.Now().Add(-time.Minute)
			instTags := append(poolTags,
				fmt.Sprintf("%s=%d", v1.LKENodePoolTagKey, poolID),
				fmt.Sprintf("lke%d", fake.DefaultClusterID),
			)
			inst := linodego.Instance{ID: instanceID, Type: "g6-standard-2", Region: fake.DefaultRegion, Created: &now, Tags: instTags, LKEClusterID: fake.DefaultClusterID, Label: "node-7004"}
			linodeEnv.LinodeAPI.Instances.Store(instanceID, inst)

			nodeClaim := coretest.NodeClaim(karpv1.NodeClaim{
				Spec: karpv1.NodeClaimSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: object.GVK(nodeClass).Group,
						Kind:  object.GVK(nodeClass).Kind,
						Name:  nodeClass.Name,
					},
				},
				Status: karpv1.NodeClaimStatus{
					ProviderID: providerID,
				},
			})
			ExpectApplied(ctx, env.Client, nodeClaim)

			ExpectSingletonReconciled(ctx, enterpriseGCController)

			_, err := enterpriseCloudProvider.Get(ctx, providerID)
			Expect(err).ToNot(HaveOccurred())
			Expect(linodeEnv.LinodeAPI.DeleteLKENodePoolBehavior.Calls()).To(Equal(0))
			Expect(linodeEnv.LinodeAPI.DeleteLKENodePoolNodeBehavior.Calls()).To(Equal(0))
		})

		It("should not delete LKE nodes within the resolution window", func() {
			poolID := 604
			instanceID := 7005
			providerID := fake.ProviderID(instanceID)
			poolTags := []string{
				fmt.Sprintf("%s:%s", karpv1.NodePoolLabelKey, nodePoolObj.Name),
				fmt.Sprintf("%s:%s", v1.LabelLKEManaged, "true"),
			}
			pool := &linodego.LKENodePool{
				ID:      poolID,
				Type:    "g6-standard-2",
				Tags:    poolTags,
				Linodes: []linodego.LKENodePoolLinode{{InstanceID: instanceID, ID: "node-7005"}},
			}
			linodeEnv.LinodeAPI.NodePools.Store(fmt.Sprintf("%d-%d", fake.DefaultClusterID, poolID), pool)

			now := time.Now()
			instTags := append(poolTags,
				fmt.Sprintf("%s=%d", v1.LKENodePoolTagKey, poolID),
				fmt.Sprintf("lke%d", fake.DefaultClusterID),
			)
			inst := linodego.Instance{ID: instanceID, Type: "g6-standard-2", Region: fake.DefaultRegion, Created: &now, Tags: instTags, LKEClusterID: fake.DefaultClusterID, Label: "node-7005"}
			linodeEnv.LinodeAPI.Instances.Store(instanceID, inst)

			ExpectSingletonReconciled(ctx, enterpriseGCController)

			_, err := enterpriseCloudProvider.Get(ctx, providerID)
			Expect(err).ToNot(HaveOccurred())
			Expect(linodeEnv.LinodeAPI.DeleteLKENodePoolBehavior.Calls()).To(Equal(0))
			Expect(linodeEnv.LinodeAPI.DeleteLKENodePoolNodeBehavior.Calls()).To(Equal(0))
		})
	})
})
