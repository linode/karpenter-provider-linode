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

package metrics_test

import (
	"context"
	"testing"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
	"github.com/linode/karpenter-provider-linode/pkg/cloudprovider"
	"github.com/linode/karpenter-provider-linode/pkg/controllers/metrics"
	"github.com/linode/karpenter-provider-linode/pkg/fake"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/test"

	"github.com/awslabs/operatorpkg/object"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/karpenter/pkg/test/v1alpha1"

	"github.com/awslabs/operatorpkg/status"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/events"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
	coretest "sigs.k8s.io/karpenter/pkg/test"

	. "sigs.k8s.io/karpenter/pkg/test/expectations"
	. "sigs.k8s.io/karpenter/pkg/utils/testing"
)

var ctx context.Context
var env *coretest.Environment
var linodeEnv *test.Environment
var cloudProvider *cloudprovider.CloudProvider
var controller *metrics.Controller
var nodeClass *v1.LinodeNodeClass
var nodePool *karpv1.NodePool

func TestLinode(t *testing.T) {
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "MetricsController")
}

var _ = BeforeSuite(func() {
	env = coretest.NewEnvironment(coretest.WithCRDs(apis.CRDs...), coretest.WithCRDs(v1alpha1.CRDs...))
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: lo.ToPtr(true)}}))
	ctx = options.ToContext(ctx, test.Options())
	linodeEnv = test.NewEnvironment(ctx)
	cloudProvider = cloudprovider.New(linodeEnv.InstanceTypesProvider, linodeEnv.InstanceProvider, events.NewRecorder(&record.FakeRecorder{}), env.Client)
	controller = metrics.NewController(env.Client, cloudProvider)
})

var _ = AfterSuite(func() {
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: lo.ToPtr(true)}}))
	Expect(env.Stop()).To(Succeed(), "Failed to stop environment")
})

var _ = BeforeEach(func() {
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: lo.ToPtr(true)}}))
	ctx = options.ToContext(ctx, test.Options())

	nodePool = coretest.NodePool()
	nodeClass = test.LinodeNodeClass(
		v1.LinodeNodeClass{
			Status: v1.LinodeNodeClassStatus{},
		},
	)
	nodePool.Spec.Template.Spec.NodeClassRef = &karpv1.NodeClassReference{
		Group: object.GVK(nodeClass).Group,
		Kind:  object.GVK(nodeClass).Kind,
		Name:  nodeClass.Name,
	}
	nodeClass.StatusConditions().SetTrue(status.ConditionReady)
	Expect(linodeEnv.InstanceTypesProvider.UpdateInstanceTypes(ctx)).To(Succeed())
	Expect(linodeEnv.InstanceTypesProvider.UpdateInstanceTypeOfferings(ctx)).To(Succeed())
})

var _ = AfterEach(func() {
	ExpectCleanedUp(ctx, env.Client)
	linodeEnv.Reset()
})

var _ = Describe("MetricsController", func() {
	Context("Availability", func() {
		It("should expose availability metrics for instance types", func() {
			ExpectApplied(ctx, env.Client, nodePool, nodeClass)
			ExpectSingletonReconciled(ctx, controller)
			instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
			Expect(err).To(BeNil())
			Expect(len(instanceTypes)).To(BeNumerically(">", 0))
			for _, it := range instanceTypes {
				for _, of := range it.Offerings {
					metric, ok := FindMetricWithLabelValues("karpenter_cloudprovider_instance_type_offering_available", map[string]string{
						"instance_type": it.Name,
						"region":        of.Requirements.Get(corev1.LabelTopologyRegion).Any(),
					})
					Expect(ok).To(BeTrue())
					Expect(metric).To(Not(BeNil()))
					value := metric.GetGauge().Value
					Expect(*value).To(BeNumerically("==", lo.Ternary(of.Available, 1, 0)))
				}
			}
		})
		It("should mark offerings as available as long as one NodePool marks it as available", func() {
			nodeClass2 := test.LinodeNodeClass(v1.LinodeNodeClass{})
			nodeClass3 := test.LinodeNodeClass(v1.LinodeNodeClass{})
			nodePool2 := coretest.NodePool(karpv1.NodePool{
				Spec: karpv1.NodePoolSpec{
					Template: karpv1.NodeClaimTemplate{
						Spec: karpv1.NodeClaimTemplateSpec{
							NodeClassRef: &karpv1.NodeClassReference{
								Group: object.GVK(nodeClass).Group,
								Kind:  object.GVK(nodeClass).Kind,
								Name:  nodeClass2.Name,
							},
						},
					},
				},
			})
			nodePool3 := coretest.NodePool(karpv1.NodePool{
				Spec: karpv1.NodePoolSpec{
					Template: karpv1.NodeClaimTemplate{
						Spec: karpv1.NodeClaimTemplateSpec{
							NodeClassRef: &karpv1.NodeClassReference{
								Group: object.GVK(nodeClass).Group,
								Kind:  object.GVK(nodeClass).Kind,
								Name:  nodeClass3.Name,
							},
						},
					},
				},
			})
			ExpectApplied(ctx, env.Client, nodePool, nodePool2, nodePool3, nodeClass, nodeClass2, nodeClass3)
			ExpectSingletonReconciled(ctx, controller)

			instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
			Expect(err).To(BeNil())
			Expect(len(instanceTypes)).To(BeNumerically(">", 0))

			availableRegions := sets.New[string]()
			for _, it := range instanceTypes {
				for _, of := range it.Offerings {
					metric, ok := FindMetricWithLabelValues("karpenter_cloudprovider_instance_type_offering_available", map[string]string{
						"instance_type": it.Name,
						"region":        of.Requirements.Get(corev1.LabelTopologyRegion).Any(),
					})
					Expect(ok).To(BeTrue())
					Expect(metric).To(Not(BeNil()))
					value := metric.GetGauge().Value
					if of.Requirements.Get(corev1.LabelTopologyRegion).Any() == fake.DefaultRegion {
						Expect(*value).To(BeNumerically("==", lo.Ternary(of.Available, 1, 0)))
					}
					if value != nil {
						availableRegions.Insert(of.Requirements.Get(corev1.LabelTopologyRegion).Any())
					}
				}
			}
			Expect(availableRegions).To(HaveLen(1))
			Expect(availableRegions.UnsortedList()).To(ContainElements(fake.DefaultRegion))
		})
	})
	Context("Pricing", func() {
		It("should expose pricing metrics for instance types", func() {
			ExpectApplied(ctx, env.Client, nodePool, nodeClass)
			ExpectSingletonReconciled(ctx, controller)
			instanceTypes, err := linodeEnv.InstanceTypesProvider.List(ctx, nodeClass)
			Expect(err).To(BeNil())
			Expect(len(instanceTypes)).To(BeNumerically(">", 0))
			for _, it := range instanceTypes {
				for _, of := range it.Offerings {
					metric, ok := FindMetricWithLabelValues("karpenter_cloudprovider_instance_type_offering_price_estimate", map[string]string{
						"instance_type": it.Name,
						"region":        of.Requirements.Get(corev1.LabelTopologyRegion).Any(),
					})
					Expect(ok).To(BeTrue())
					Expect(metric).To(Not(BeNil()))
					value := metric.GetGauge().Value
					Expect(*value).To(BeNumerically("==", of.Price))
				}
			}
		})
	})
})
