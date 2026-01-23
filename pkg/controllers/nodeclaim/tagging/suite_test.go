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

package tagging_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/linode/linodego"
	"github.com/samber/lo"
	corev1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/events"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
	coretest "sigs.k8s.io/karpenter/pkg/test"
	"sigs.k8s.io/karpenter/pkg/test/v1alpha1"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	"github.com/linode/karpenter-provider-linode/pkg/cloudprovider"
	"github.com/linode/karpenter-provider-linode/pkg/controllers/nodeclaim/tagging"
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
var linodeEnv *test.Environment
var env *coretest.Environment
var taggingController *tagging.Controller

func TestAPIs(t *testing.T) {
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "TaggingController")
}

var _ = BeforeSuite(func() {
	env = coretest.NewEnvironment(coretest.WithCRDs(apis.CRDs...), coretest.WithCRDs(v1alpha1.CRDs...))
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: lo.ToPtr(true)}}))
	ctx = options.ToContext(ctx, test.Options())
	linodeEnv = test.NewEnvironment(ctx)
	cloudProvider := cloudprovider.New(linodeEnv.InstanceTypesProvider, linodeEnv.InstanceProvider, events.NewRecorder(&record.FakeRecorder{}),
		env.Client)
	taggingController = tagging.NewController(env.Client, cloudProvider, linodeEnv.InstanceProvider)
})
var _ = AfterSuite(func() {
	Expect(env.Stop()).To(Succeed(), "Failed to stop environment")
})

var _ = BeforeEach(func() {
	linodeEnv.Reset()
})

var _ = AfterEach(func() {
	ExpectCleanedUp(ctx, env.Client)
})

var _ = Describe("TaggingController", func() {
	var linodeInstance linodego.Instance
	BeforeEach(func() {
		linodeInstance = linodego.Instance{
			Status: linodego.InstanceRunning,
			Tags: []string{
				fmt.Sprintf("kubernetes.io/cluster/%s:owned", options.FromContext(ctx).ClusterName),
				karpv1.NodePoolLabelKey + ":" + "default",
				v1.LKEClusterNameTagKey + ":" + options.FromContext(ctx).ClusterName,
			},
			Region: fake.DefaultRegion,
			ID:     fake.InstanceID(),
			Type:   "g6-standard-4",
		}

		linodeEnv.LinodeAPI.Instances.Store(linodeInstance.ID, linodeInstance)
	})

	It("shouldn't tag linodeInstances without a Node", func() {
		nodeClaim := coretest.NodeClaim(karpv1.NodeClaim{
			Status: karpv1.NodeClaimStatus{
				ProviderID: fake.ProviderID(linodeInstance.ID),
			},
		})

		ExpectApplied(ctx, env.Client, nodeClaim)
		ExpectObjectReconciled(ctx, env.Client, taggingController, nodeClaim)
		Expect(nodeClaim.Annotations).To(Not(HaveKey(v1.AnnotationInstanceTagged)))
		Expect(lo.ContainsBy(linodeInstance.Tags, func(tag string) bool {
			parts := strings.Split(tag, ":")
			return parts[0] == v1.NameTagKey
		})).To(BeFalse())
	})

	It("should gracefully handle missing NodeClaim", func() {
		nodeClaim := coretest.NodeClaim(karpv1.NodeClaim{
			Status: karpv1.NodeClaimStatus{
				ProviderID: fake.ProviderID(linodeInstance.ID),
				NodeName:   "default",
			},
		})

		ExpectApplied(ctx, env.Client, nodeClaim)
		ExpectDeleted(ctx, env.Client, nodeClaim)
		ExpectObjectReconciled(ctx, env.Client, taggingController, nodeClaim)
	})

	It("should gracefully handle missing linodeInstance", func() {
		nodeClaim := coretest.NodeClaim(karpv1.NodeClaim{
			Status: karpv1.NodeClaimStatus{
				ProviderID: fake.ProviderID(linodeInstance.ID),
				NodeName:   "default",
			},
		})

		ExpectApplied(ctx, env.Client, nodeClaim)
		linodeEnv.LinodeAPI.Instances.Delete(strconv.Itoa(linodeInstance.ID))
		ExpectObjectReconciled(ctx, env.Client, taggingController, nodeClaim)
		Expect(nodeClaim.Annotations).To(Not(HaveKey(v1.AnnotationInstanceTagged)))
	})

	It("shouldn't tag nodeclaim with deletion timestamp set", func() {
		nodeClaim := coretest.NodeClaim(karpv1.NodeClaim{
			Status: karpv1.NodeClaimStatus{
				ProviderID: fake.ProviderID(linodeInstance.ID),
				NodeName:   "default",
			},
			ObjectMeta: corev1.ObjectMeta{
				Finalizers: []string{"testing/finalizer"},
			},
		})

		ExpectApplied(ctx, env.Client, nodeClaim)
		Expect(env.Client.Delete(ctx, nodeClaim)).To(Succeed())
		ExpectObjectReconciled(ctx, env.Client, taggingController, nodeClaim)
		Expect(nodeClaim.Annotations).To(Not(HaveKey(v1.AnnotationInstanceTagged)))
		Expect(lo.ContainsBy(linodeInstance.Tags, func(tag string) bool {
			parts := strings.Split(tag, ":")
			return parts[0] == v1.NameTagKey
		})).To(BeFalse())
	})

	DescribeTable(
		"should tag taggable linodeInstances",
		func(customTags ...string) {
			nodeClaim := coretest.NodeClaim(karpv1.NodeClaim{
				Status: karpv1.NodeClaimStatus{
					ProviderID: fake.ProviderID(linodeInstance.ID),
					NodeName:   "default",
				},
			})

			for _, tag := range customTags {
				linodeInstance.Tags = append(linodeInstance.Tags, tag+":custom-tag")
			}
			linodeEnv.LinodeAPI.Instances.Store(linodeInstance.ID, linodeInstance)

			ExpectApplied(ctx, env.Client, nodeClaim)
			ExpectObjectReconciled(ctx, env.Client, taggingController, nodeClaim)
			nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
			Expect(nodeClaim.Annotations).To(HaveKey(v1.AnnotationInstanceTagged))

			expectedTags := []string{
				v1.NameTagKey + ":" + nodeClaim.Status.NodeName,
				v1.NodeClaimTagKey + ":" + nodeClaim.Name,
				v1.LKEClusterNameTagKey + ":" + options.FromContext(ctx).ClusterName,
			}
			linodeInstance := lo.Must(linodeEnv.LinodeAPI.Instances.Load(linodeInstance.ID)).(linodego.Instance)
			linodeInstanceTags := instance.NewInstance(ctx, linodeInstance).Tags

			for _, tag := range expectedTags {
				parts := strings.Split(tag, ":")
				if len(parts) == 2 && lo.Contains(customTags, parts[0]) {
					parts[1] = "custom-tag"
					tag = strings.Join(parts, ":")
				}
				Expect(linodeInstanceTags).To(ContainElement(tag))
			}
		},
		Entry("with the karpenter.sh/nodeclaim tag", v1.NameTagKey, v1.LKEClusterNameTagKey),
		Entry("with the lke:lke-cluster-name tag", v1.NameTagKey, v1.NodeClaimTagKey),
		Entry("with the Name tag", v1.NodeClaimTagKey, v1.LKEClusterNameTagKey),
		Entry("with the karpenter.sh/nodeclaim and lke:lke-cluster-name tags", v1.NameTagKey),
		Entry("with the Name and lke:lke-cluster-name tags", v1.NodeClaimTagKey),
		Entry("with the karpenter.sh/nodeclaim and Name tags", v1.LKEClusterNameTagKey),
		Entry("with nothing to tag", v1.NodeClaimTagKey, v1.LKEClusterNameTagKey, v1.NameTagKey),
	)
})
