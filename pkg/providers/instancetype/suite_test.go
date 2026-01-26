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
	"github.com/linode/linodego"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	clock "k8s.io/utils/clock/testing"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/controllers/provisioning"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/events"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
	coretest "sigs.k8s.io/karpenter/pkg/test"
	"sigs.k8s.io/karpenter/pkg/test/v1alpha1"

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
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "InstanceTypeProvider")
}

var _ = BeforeSuite(func() {
	env = coretest.NewEnvironment(coretest.WithCRDs(apis.CRDs...), coretest.WithCRDs(v1alpha1.CRDs...))
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: lo.ToPtr(true)}}))
	ctx = options.ToContext(ctx, test.Options())
	ctx, stop = context.WithCancel(ctx)
	linodeEnv = test.NewEnvironment(ctx)
	fakeClock = &clock.FakeClock{}
	cloudProvider = cloudprovider.New(linodeEnv.InstanceTypesProvider, linodeEnv.NodeProvider(ctx), events.NewRecorder(&record.FakeRecorder{}),
		env.Client)
	cluster = state.NewCluster(fakeClock, env.Client, cloudProvider)
	prov = provisioning.NewProvisioner(env.Client, events.NewRecorder(&record.FakeRecorder{}), cloudProvider, cluster, fakeClock)
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
								NodeSelectorRequirement: corev1.NodeSelectorRequirement{
									Key:      karpv1.CapacityTypeLabelKey,
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{karpv1.CapacityTypeOnDemand},
								},
							},
							{
								NodeSelectorRequirement: corev1.NodeSelectorRequirement{
									Key:      corev1.LabelTopologyRegion,
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{fake.DefaultRegion},
								},
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

	It("should support individual instance type labels", func() {
		ExpectApplied(ctx, env.Client, nodePool, nodeClass)

		nodeSelector := map[string]string{
			// Well known
			karpv1.NodePoolLabelKey:        nodePool.Name,
			corev1.LabelTopologyRegion:     fake.DefaultRegion,
			corev1.LabelInstanceTypeStable: "g6-standard-2",
			corev1.LabelOSStable:           "linux",
			corev1.LabelArchStable:         "amd64",
			karpv1.CapacityTypeLabelKey:    karpv1.CapacityTypeOnDemand,
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
			ExpectScheduled(ctx, env.Client, pod)
		}
	})
	Context("Overhead", func() {
		var info linodego.LinodeType
		BeforeEach(func() {
			ctx = options.ToContext(ctx, test.Options(test.OptionsFields{
				ClusterName: lo.ToPtr("karpenter-cluster"),
			}))

			var ok bool
			instanceInfo, err := linodeEnv.LinodeAPI.ListTypes(ctx, &linodego.ListOptions{})
			Expect(err).To(BeNil())
			info, ok = lo.Find(instanceInfo, func(i linodego.LinodeType) bool {
				return i.ID == "g6-standard-4"
			})
			Expect(ok).To(BeTrue())
		})
		Context("System Reserved Resources", func() {
			It("should use defaults when no kubelet is specified", func() {
				nodeClass.Spec.Kubelet = &v1.KubeletConfiguration{}
				it := instancetype.NewInstanceType(ctx,
					info,
					fake.DefaultRegion,
					nodeClass.Spec.Kubelet.MaxPods,
					nodeClass.Spec.Kubelet.PodsPerCore,
					nodeClass.Spec.Kubelet.KubeReserved,
					nodeClass.Spec.Kubelet.SystemReserved,
					nodeClass.Spec.Kubelet.EvictionHard,
					nodeClass.Spec.Kubelet.EvictionSoft,
				)
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
				it := instancetype.NewInstanceType(ctx,
					info,
					fake.DefaultRegion,
					nodeClass.Spec.Kubelet.MaxPods,
					nodeClass.Spec.Kubelet.PodsPerCore,
					nodeClass.Spec.Kubelet.KubeReserved,
					nodeClass.Spec.Kubelet.SystemReserved,
					nodeClass.Spec.Kubelet.EvictionHard,
					nodeClass.Spec.Kubelet.EvictionSoft,
				)
				Expect(it.Overhead.SystemReserved.Cpu().String()).To(Equal("2"))
				Expect(it.Overhead.SystemReserved.Memory().String()).To(Equal("20Gi"))
				Expect(it.Overhead.SystemReserved.StorageEphemeral().String()).To(Equal("10Gi"))
			})
		})
	})
})
