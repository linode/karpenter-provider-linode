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

	"github.com/awslabs/operatorpkg/object"
	"github.com/awslabs/operatorpkg/status"
	"github.com/linode/linodego/v2"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/tools/record"
	clock "k8s.io/utils/clock/testing"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpcloudprovider "sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/controllers/provisioning"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/events"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
	coretest "sigs.k8s.io/karpenter/pkg/test"
	"sigs.k8s.io/karpenter/pkg/test/v1alpha1"
	"sigs.k8s.io/karpenter/pkg/utils/resources"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	"github.com/linode/karpenter-provider-linode/pkg/cloudprovider"
	"github.com/linode/karpenter-provider-linode/pkg/fake"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instancetype"
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
var fakeClock *clock.FakeClock
var prov *provisioning.Provisioner
var cluster *state.Cluster
var cloudProvider *cloudprovider.CloudProvider

func TestLinode(t *testing.T) {
	t.Parallel()
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "InstanceTypeProvider")
}

var _ = BeforeSuite(func() {
	env = coretest.NewEnvironment(coretest.WithCRDs(apis.CRDs...), coretest.WithCRDs(v1alpha1.CRDs...))
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: new(true)}}))
	ctx = options.ToContext(ctx, test.Options())
	ctx, stop = context.WithCancel(ctx)
	linodeEnv = test.NewEnvironment(ctx)
	fakeClock = &clock.FakeClock{}
	cloudProvider = cloudprovider.New(linodeEnv.InstanceTypesProvider, linodeEnv.NodeProvider(ctx), events.NewRecorder(&record.FakeRecorder{}),
		env.Client)
	cluster = state.NewCluster(fakeClock, env.Client, cloudProvider)
	prov = provisioning.NewProvisioner(env.Client, events.NewRecorder(&record.FakeRecorder{}), cloudProvider, cluster, fakeClock, nil, nil)
})

var _ = AfterSuite(func() {
	stop()
	Expect(env.Stop()).To(Succeed(), "Failed to stop environment")
})

var _ = BeforeEach(func() {
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{}))
	ctx = options.ToContext(ctx, test.Options())
	cluster.Reset()
	linodeEnv.Reset()
	linodeEnv.SetDefaults()
})

var _ = AfterEach(func() {
	ExpectCleanedUp(ctx, env.Client)
})

var _ = Describe("InstanceTypeProvider", func() {
	var nodeClass *v1.LinodeNodeClass
	var nodePool *karpv1.NodePool
	BeforeEach(func() {
		nodeClass = test.LinodeNodeClass(
			v1.LinodeNodeClass{
				Status: v1.LinodeNodeClassStatus{},
			},
		)
		nodeClass.StatusConditions().SetTrue(status.ConditionReady)
		nodePool = coretest.NodePool(karpv1.NodePool{
			Spec: karpv1.NodePoolSpec{
				Template: karpv1.NodeClaimTemplate{
					Spec: karpv1.NodeClaimTemplateSpec{
						Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
							{
								Key:      corev1.LabelTopologyRegion,
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{fake.DefaultRegion},
							},
							{
								Key:      corev1.LabelTopologyZone,
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{fake.DefaultRegion},
							},
						},
						NodeClassRef: &karpv1.NodeClassReference{
							Group: object.GVK(nodeClass).Group,
							Kind:  object.GVK(nodeClass).Kind,
							Name:  nodeClass.Name,
						},
					},
				},
			},
		})
		Expect(linodeEnv.InstanceTypesProvider.UpdateInstanceTypes(ctx)).To(Succeed())
		Expect(linodeEnv.InstanceTypesProvider.UpdateInstanceTypeOfferings(ctx)).To(Succeed())
	})

	It("should expose prices by the node topology zone", func() {
		instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
		Expect(err).ToNot(HaveOccurred())

		instanceType, ok := lo.Find(instanceTypes, func(instanceType *karpcloudprovider.InstanceType) bool {
			return instanceType.Name == "g6-standard-4"
		})
		Expect(ok).To(BeTrue())
		Expect(instanceType.Requirements.Get(corev1.LabelTopologyZone).Has(fake.DefaultRegion)).To(BeTrue())
		Expect(instanceType.Requirements.Get("topology.linode.com/region").Any()).To(Equal(fake.DefaultRegion))
		Expect(instanceType.Requirements.Get(karpv1.CapacityTypeLabelKey).Has(karpv1.CapacityTypeOnDemand)).To(BeTrue())

		price, ok := instanceType.OfferingPrice(fake.DefaultRegion, karpv1.CapacityTypeOnDemand)
		Expect(ok).To(BeTrue())
		Expect(price).To(BeNumerically(">", 0))
	})
	It("should refresh cached offering availability when region availability changes", func() {
		const targetPlan = "g6-standard-2"
		availability := linodeEnv.LinodeAPI.GetRegionAvailabilityOutput.Clone()
		Expect(availability).ToNot(BeNil())

		assertAvailability := func(want bool) {
			instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
			Expect(err).ToNot(HaveOccurred())
			instanceType, ok := lo.Find(instanceTypes, func(it *karpcloudprovider.InstanceType) bool {
				return it.Name == targetPlan
			})
			Expect(ok).To(BeTrue())
			Expect(instanceType.Offerings).To(HaveLen(1))
			Expect(instanceType.Offerings[0].Available).To(Equal(want))
		}
		refreshAvailability := func(available bool) {
			found := false
			for i := range *availability {
				if (*availability)[i].Plan == targetPlan {
					(*availability)[i].Available = available
					found = true
					break
				}
			}
			Expect(found).To(BeTrue())
			// Other plans remain available, so the overall region set does not change.
			linodeEnv.LinodeAPI.GetRegionAvailabilityOutput.Set(availability)
			Expect(linodeEnv.InstanceTypesProvider.UpdateInstanceTypeOfferings(ctx)).To(Succeed())
		}

		assertAvailability(true)
		refreshAvailability(false)
		assertAvailability(false)
		refreshAvailability(true)
		assertAvailability(true)
	})

	It("should support individual instance type labels", func() {
		ExpectApplied(ctx, env.Client, nodePool, nodeClass)

		nodeSelector := map[string]string{
			// Well known
			karpv1.NodePoolLabelKey:        nodePool.Name,
			corev1.LabelTopologyRegion:     fake.DefaultRegion,
			"topology.linode.com/region":   fake.DefaultRegion,
			corev1.LabelInstanceTypeStable: "g6-standard-2",
			corev1.LabelOSStable:           "linux",
			corev1.LabelArchStable:         "amd64",
			// Well Known to Linode
			v1.LabelInstanceCPU:                     "2",
			v1.LabelInstanceMemory:                  "4096",
			v1.LabelInstanceTransfer:                "2000",
			v1.LabelInstanceNetworkOut:              "1000",
			v1.LabelInstanceGPUCount:                "4",
			v1.LabelInstanceAcceleratedDevicesCount: "0",
			v1.LabelInstanceClass:                   "standard",
			v1.LabelInstanceGeneration:              "6",
			v1.LabelInstanceGPUName:                 "rtx6000",
			v1.LabelInstanceDisk:                    "307200",
		}

		// Ensure that we're exercising all well-known labels
		Expect(lo.Keys(nodeSelector)).To(ContainElements(append(v1.LinodeWellKnownLabels.UnsortedList(), corev1.LabelInstanceTypeStable)))

		var pods []*corev1.Pod
		for key, value := range nodeSelector {
			pods = append(pods, coretest.UnschedulablePod(coretest.PodOptions{NodeSelector: map[string]string{key: value}}))
		}
		ExpectProvisioned(ctx, env.Client, cluster, cloudProvider, prov, pods...)
		for _, pod := range pods {
			node := ExpectScheduled(ctx, env.Client, pod)
			if region, ok := pod.Spec.NodeSelector["topology.linode.com/region"]; ok {
				Expect(node.Labels["topology.linode.com/region"]).To(Equal(region))
			}
		}
	})
	Context("Overhead", func() {
		var info linodego.LinodeType
		BeforeEach(func() {
			ctx = options.ToContext(ctx, test.Options(test.OptionsFields{
				ClusterName: new("karpenter-cluster"),
			}))

			var ok bool
			instanceInfo, err := linodeEnv.LinodeAPI.ListTypes(ctx, &linodego.ListOptions{})
			Expect(err).To(BeNil())
			info, ok = lo.Find(instanceInfo, func(i linodego.LinodeType) bool {
				return i.ID == "g6-standard-4"
			})
			Expect(ok).To(BeTrue())
		})
		Context("Kube Reserved Resources", func() {
			DescribeTable("uses plan-based CPU and memory bands for each LKE tier",
				func(cpus, memoryMi, standardCPU, enterpriseCPU, standardMemoryMi, enterpriseMemoryMi int) {
					plan := linodego.LinodeType{ID: "test-plan", VCPUs: cpus, Memory: memoryMi}
					for _, tier := range []linodego.LKEVersionTier{linodego.LKEVersionStandard, linodego.LKEVersionEnterprise} {
						By(string(tier))
						it := instancetype.NewDefaultResolver(fake.DefaultRegion, tier).Resolve(ctx, &plan, nodeClass)
						wantCPU, wantMemoryMi := standardCPU, standardMemoryMi
						if tier == linodego.LKEVersionEnterprise {
							wantCPU, wantMemoryMi = enterpriseCPU, enterpriseMemoryMi
						}
						Expect(it.Overhead.KubeReserved.Cpu().MilliValue()).To(Equal(int64(wantCPU)))
						Expect(it.Overhead.KubeReserved.Memory().Value()).To(Equal(int64(wantMemoryMi) * 1024 * 1024))
					}
				},
				Entry("first CPU and memory bands", 1, 1024, 60, 60, 256, 256),
				Entry("second CPU and first memory boundary", 2, 4096, 70, 69, 1024, 1024),
				Entry("third CPU and fractional GiB", 3, 4608, 75, 75, 1229, 1126),
				Entry("fourth CPU and second memory boundary", 4, 8192, 80, 79, 1844, 1843),
				Entry("above four CPUs and above first memory boundary", 5, 5120, 83, 82, 1229, 1228),
				Entry("eight CPUs and above second memory boundary", 8, 9216, 92, 89, 1949, 1945),
				Entry("sixteen CPUs and third memory boundary", 16, 16384, 116, 109, 2684, 2662),
				Entry("above third memory boundary", 4, 17408, 80, 79, 2745, 2723),
				Entry("fourth memory boundary", 4, 131072, 80, 79, 9516, 9543),
				Entry("above fourth memory boundary", 4, 132096, 80, 79, 9536, 9564),
			)

			DescribeTable("fits workload and DaemonSet requests without tying LKE reservations to pod capacity",
				func(tier linodego.LKEVersionTier, maxPods int, cpu, memory string) {
					plan := linodego.LinodeType{ID: "g6-standard-2", VCPUs: 2, Memory: 4096, Disk: 81920}
					nodeClass.Spec.Kubelet = &v1.KubeletConfiguration{MaxPods: new(int32(maxPods))}
					it := instancetype.NewDefaultResolver(fake.DefaultRegion, tier).Resolve(ctx, &plan, nodeClass)
					requests := corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse(cpu),
						corev1.ResourceMemory: resource.MustParse(memory),
						corev1.ResourcePods:   resource.MustParse("4"),
					}
					Expect(resources.Fits(requests, it.Allocatable())).To(BeTrue())
					requests[corev1.ResourceMemory] = resource.MustParse("2664Mi")
					Expect(resources.Fits(requests, it.Allocatable())).To(BeTrue())
					requests[corev1.ResourceMemory] = resource.MustParse("2665Mi")
					Expect(resources.Fits(requests, it.Allocatable())).To(BeFalse())
				},
				Entry("Standard at 110 pods", linodego.LKEVersionStandard, 110, "1250m", "2500Mi"),
				Entry("Standard at 250 pods", linodego.LKEVersionStandard, 250, "1250m", "2500Mi"),
				Entry("Enterprise at 110 pods", linodego.LKEVersionEnterprise, 110, "1100m", "2510Mi"),
				Entry("Enterprise at 250 pods", linodego.LKEVersionEnterprise, 250, "1100m", "2510Mi"),
			)

			DescribeTable("applies explicit reservations without changing defaults for other resources",
				func(tier linodego.LKEVersionTier) {
					plan := linodego.LinodeType{ID: "g6-standard-2", VCPUs: 2, Memory: 4096, Disk: 81920}
					resolver := instancetype.NewDefaultResolver(fake.DefaultRegion, tier)
					defaults := resolver.Resolve(ctx, &plan, nodeClass)
					requests := corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1250m"),
						corev1.ResourceMemory: resource.MustParse("2500Mi"),
					}
					nodeClass.Spec.Kubelet = &v1.KubeletConfiguration{KubeReserved: map[string]string{"memory": "2Gi"}}
					overridden := resolver.Resolve(ctx, &plan, nodeClass)
					Expect(resources.Fits(requests, overridden.Allocatable())).To(BeFalse())
					Expect(overridden.Overhead.KubeReserved.Cpu().Cmp(*defaults.Overhead.KubeReserved.Cpu())).To(Equal(0))
					nodeClass.Spec.Kubelet.KubeReserved = map[string]string{"cpu": "500m"}
					overridden = resolver.Resolve(ctx, &plan, nodeClass)
					Expect(resources.Fits(requests, overridden.Allocatable())).To(BeTrue())
					requests[corev1.ResourceCPU] = resource.MustParse("1501m")
					Expect(resources.Fits(requests, overridden.Allocatable())).To(BeFalse())
				},
				Entry("Standard", linodego.LKEVersionStandard),
				Entry("Enterprise", linodego.LKEVersionEnterprise),
			)

			DescribeTable("preserves pod-based memory reservations for direct instances",
				func(maxPods, memoryMi int) {
					nodeClass.Spec.Kubelet = &v1.KubeletConfiguration{MaxPods: new(int32(maxPods))}
					it := instancetype.NewDefaultResolver(fake.DefaultRegion, "").Resolve(ctx, &info, nodeClass)
					Expect(it.Overhead.KubeReserved.Cpu().MilliValue()).To(Equal(int64(80)))
					Expect(it.Overhead.KubeReserved.Memory().Value()).To(Equal(int64(memoryMi) * 1024 * 1024))
				},
				Entry("110 pods", 110, 1465),
				Entry("40 pods", 40, 695),
			)
		})
		Context("System Reserved Resources", func() {
			It("should use defaults when no kubelet is specified", func() {
				nodeClass.Spec.Kubelet = &v1.KubeletConfiguration{}
				it := instancetype.NewInstanceType(ctx, &info, fake.DefaultRegion, fake.DefaultClusterTier, nodeClass.Spec.Kubelet)
				Expect(it.Overhead.SystemReserved.Cpu().String()).To(Equal("0"))
				Expect(it.Overhead.SystemReserved.Memory().String()).To(Equal("0"))
				Expect(it.Overhead.SystemReserved.StorageEphemeral().String()).To(Equal("0"))
			})
			It("should override system reserved cpus when specified", func() {
				nodeClass.Spec.Kubelet = &v1.KubeletConfiguration{
					SystemReserved: map[string]string{
						string(corev1.ResourceCPU):              "2",
						string(corev1.ResourceMemory):           "20Gi",
						string(corev1.ResourceEphemeralStorage): "10Gi",
					},
				}
				it := instancetype.NewInstanceType(ctx, &info, fake.DefaultRegion, fake.DefaultClusterTier, nodeClass.Spec.Kubelet)
				Expect(it.Overhead.SystemReserved.Cpu().String()).To(Equal("2"))
				Expect(it.Overhead.SystemReserved.Memory().String()).To(Equal("20Gi"))
				Expect(it.Overhead.SystemReserved.StorageEphemeral().String()).To(Equal("10Gi"))
			})
		})
		Context("Ephemeral Storage", func() {
			It("should predict the plan disk and reserve the default filesystem eviction threshold once", func() {
				it := instancetype.NewInstanceType(ctx, &info, fake.DefaultRegion, fake.DefaultClusterTier, nil)
				allocatable := it.Allocatable()

				Expect(it.Capacity.StorageEphemeral().String()).To(Equal("160Gi"))
				Expect(it.Overhead.KubeReserved.StorageEphemeral().String()).To(Equal("1Gi"))
				Expect(it.Overhead.EvictionThreshold.StorageEphemeral().Cmp(resource.MustParse("24Gi"))).To(Equal(0))
				Expect(allocatable.StorageEphemeral().Cmp(resource.MustParse("135Gi"))).To(Equal(0))
			})

			It("should subtract kubelet reservations and use the strictest shared-filesystem eviction signal", func() {
				it := instancetype.NewInstanceType(ctx, &info, fake.DefaultRegion, fake.DefaultClusterTier, &v1.KubeletConfiguration{
					KubeReserved:   map[string]string{string(corev1.ResourceEphemeralStorage): "3Gi"},
					SystemReserved: map[string]string{string(corev1.ResourceEphemeralStorage): "2Gi"},
					EvictionHard: map[string]string{
						"nodefs.available":  "10%",
						"imagefs.available": "15%",
					},
					EvictionSoft: map[string]string{"nodefs.available": "20%"},
				})
				allocatable := it.Allocatable()

				Expect(it.Overhead.KubeReserved.StorageEphemeral().String()).To(Equal("3Gi"))
				Expect(it.Overhead.SystemReserved.StorageEphemeral().String()).To(Equal("2Gi"))
				Expect(it.Overhead.EvictionThreshold.StorageEphemeral().Cmp(resource.MustParse("32Gi"))).To(Equal(0))
				Expect(allocatable.StorageEphemeral().Cmp(resource.MustParse("123Gi"))).To(Equal(0))
			})

			It("should fit combined pod and DaemonSet requests up to usable storage only", func() {
				it := instancetype.NewInstanceType(ctx, &info, fake.DefaultRegion, fake.DefaultClusterTier, nil)
				podRequest := resource.MustParse("120Gi")
				daemonSetRequest := resource.MustParse("15Gi")
				podRequest.Add(daemonSetRequest)
				requests := corev1.ResourceList{corev1.ResourceEphemeralStorage: podRequest}

				Expect(resources.Fits(requests, it.Allocatable())).To(BeTrue())

				overCapacity := podRequest.DeepCopy()
				overCapacity.Add(resource.MustParse("1Mi"))
				requests[corev1.ResourceEphemeralStorage] = overCapacity
				Expect(resources.Fits(requests, it.Allocatable())).To(BeFalse())
			})
		})

		It("should include kubelet settings in the instance type cache key", func() {
			resolver := instancetype.NewDefaultResolver(fake.DefaultRegion, fake.DefaultClusterTier)
			originalKey := resolver.CacheKey(nodeClass)
			nodeClass.Spec.Kubelet = &v1.KubeletConfiguration{
				KubeReserved: map[string]string{
					string(corev1.ResourceEphemeralStorage): "2Gi",
				},
			}

			Expect(resolver.CacheKey(nodeClass)).NotTo(Equal(originalKey))
		})
	})
})
