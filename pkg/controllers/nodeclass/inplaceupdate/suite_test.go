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

package inplaceupdate_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/awslabs/operatorpkg/object"
	"github.com/linode/linodego"
	"github.com/samber/lo"
	"k8s.io/client-go/tools/record"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/events"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
	coretest "sigs.k8s.io/karpenter/pkg/test"
	karptestv1alpha1 "sigs.k8s.io/karpenter/pkg/test/v1alpha1"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	"github.com/linode/karpenter-provider-linode/pkg/cloudprovider"
	"github.com/linode/karpenter-provider-linode/pkg/controllers/nodeclass/inplaceupdate"
	"github.com/linode/karpenter-provider-linode/pkg/fake"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/test"
	"github.com/linode/karpenter-provider-linode/pkg/utils"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
	. "sigs.k8s.io/karpenter/pkg/utils/testing"
)

var ctx context.Context
var linodeEnv *test.Environment
var env *coretest.Environment
var controller *inplaceupdate.Controller

func TestAPIs(t *testing.T) {
	t.Parallel()
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "NodeClassInPlaceUpdateController")
}

var _ = BeforeSuite(func() {
	env = coretest.NewEnvironment(
		coretest.WithCRDs(apis.CRDs...),
		coretest.WithCRDs(karptestv1alpha1.CRDs...),
		coretest.WithFieldIndexers(coretest.NodeClaimNodeClassRefFieldIndexer(ctx)),
	)
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: lo.ToPtr(true)}}))
	ctx = options.ToContext(ctx, test.Options())
	linodeEnv = test.NewEnvironment(ctx)
	cloudProvider := cloudprovider.New(
		linodeEnv.InstanceTypesProvider,
		linodeEnv.NodeProvider(ctx),
		events.NewRecorder(&record.FakeRecorder{}),
		env.Client,
	)
	controller = inplaceupdate.NewController(env.Client, cloudProvider, linodeEnv.NodeProvider(ctx))
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

var _ = Describe("InPlaceUpdateController", func() {
	var linodeInstance linodego.Instance
	var nodeClass *v1.LinodeNodeClass

	BeforeEach(func() {
		linodeInstance = linodego.Instance{
			Status: linodego.InstanceRunning,
			Tags: []string{
				fmt.Sprintf("kubernetes.io/cluster/%s=owned", options.FromContext(ctx).ClusterName),
				karpv1.NodePoolLabelKey + "=default",
				v1.LKEClusterNameTagKey + "=" + options.FromContext(ctx).ClusterName,
				v1.NameTagKey + "=default",
				"external=keep",
				"lke12345",
			},
			Region: fake.DefaultRegion,
			ID:     fake.InstanceID(),
			Type:   "g6-standard-4",
		}
		linodeEnv.LinodeAPI.Instances.Store(linodeInstance.ID, linodeInstance)

		nodeClass = test.LinodeNodeClass(v1.LinodeNodeClass{
			Spec: v1.LinodeNodeClassSpec{
				Tags: []string{"owner=platform", "opaque-user-tag"},
			},
		})
	})

	newNodeClaim := func() *karpv1.NodeClaim {
		return coretest.NodeClaim(karpv1.NodeClaim{
			Spec: karpv1.NodeClaimSpec{
				NodeClassRef: &karpv1.NodeClassReference{
					Group: object.GVK(nodeClass).Group,
					Kind:  object.GVK(nodeClass).Kind,
					Name:  nodeClass.Name,
				},
			},
			Status: karpv1.NodeClaimStatus{
				ProviderID: fake.ProviderID(linodeInstance.ID),
				NodeName:   "default",
			},
		})
	}

	It("should skip nodeclaims without a provider ID", func() {
		nodeClaim := newNodeClaim()
		nodeClaim.Status.ProviderID = ""

		ExpectApplied(ctx, env.Client, nodeClass, nodeClaim)
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClaim)
		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		Expect(nodeClaim.Annotations).ToNot(HaveKey(v1.AnnotationLastAppliedUserTags))

		linodeInstance = lo.Must(linodeEnv.LinodeAPI.Instances.Load(linodeInstance.ID)).(linodego.Instance)
		Expect(utils.TagListToMap(linodeInstance.Tags)).ToNot(HaveKey("owner"))
	})

	It("should gracefully handle missing linodeInstance", func() {
		nodeClaim := newNodeClaim()

		ExpectApplied(ctx, env.Client, nodeClass, nodeClaim)
		linodeEnv.LinodeAPI.Instances.Delete(linodeInstance.ID)
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClaim)
		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		Expect(nodeClaim.Annotations).ToNot(HaveKey(v1.AnnotationLastAppliedUserTags))
	})

	It("should apply user tags and record them on first reconcile", func() {
		nodeClaim := newNodeClaim()

		ExpectApplied(ctx, env.Client, nodeClass, nodeClaim)
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClaim)

		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		Expect(nodeClaim.Annotations[v1.AnnotationLastAppliedUserTags]).ToNot(BeEmpty())

		linodeInstance = lo.Must(linodeEnv.LinodeAPI.Instances.Load(linodeInstance.ID)).(linodego.Instance)
		tags := utils.TagListToMap(linodeInstance.Tags)
		Expect(tags).To(HaveKeyWithValue("owner", "platform"))
		Expect(tags).To(HaveKeyWithValue(v1.NameTagKey, "default"))
		Expect(tags).To(HaveKeyWithValue(v1.LKEClusterNameTagKey, options.FromContext(ctx).ClusterName))
		Expect(tags).To(HaveKeyWithValue("external", "keep"))
		Expect(linodeInstance.Tags).To(ContainElement("opaque-user-tag"))
		Expect(linodeInstance.Tags).To(ContainElement("lke12345"))
	})

	It("should preserve unknown existing tags on the first reconcile", func() {
		linodeInstance.Tags = append(linodeInstance.Tags, "legacy=unknown")
		linodeEnv.LinodeAPI.Instances.Store(linodeInstance.ID, linodeInstance)
		nodeClaim := newNodeClaim()

		ExpectApplied(ctx, env.Client, nodeClass, nodeClaim)
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClaim)

		linodeInstance = lo.Must(linodeEnv.LinodeAPI.Instances.Load(linodeInstance.ID)).(linodego.Instance)
		tags := utils.TagListToMap(linodeInstance.Tags)
		Expect(tags).To(HaveKeyWithValue("legacy", "unknown"))
		Expect(tags).To(HaveKeyWithValue("owner", "platform"))
	})

	It("should update user tags in place and remove old user-managed keys", func() {
		nodeClaim := newNodeClaim()

		ExpectApplied(ctx, env.Client, nodeClass, nodeClaim)
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClaim)

		nodeClass = ExpectExists(ctx, env.Client, nodeClass)
		nodeClass.Spec.Tags = []string{"team=infra", "opaque-user-tag-v2"}
		ExpectApplied(ctx, env.Client, nodeClass)

		ExpectObjectReconciled(ctx, env.Client, controller, nodeClaim)

		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		Expect(nodeClaim.Annotations[v1.AnnotationLastAppliedUserTags]).ToNot(BeEmpty())
		linodeInstance = lo.Must(linodeEnv.LinodeAPI.Instances.Load(linodeInstance.ID)).(linodego.Instance)
		tags := utils.TagListToMap(linodeInstance.Tags)
		Expect(tags).ToNot(HaveKey("owner"))
		Expect(tags).To(HaveKeyWithValue("team", "infra"))
		Expect(tags).To(HaveKeyWithValue("external", "keep"))
		Expect(tags).To(HaveKeyWithValue(v1.NameTagKey, "default"))
		Expect(tags).To(HaveKeyWithValue(v1.LKEClusterNameTagKey, options.FromContext(ctx).ClusterName))
		Expect(linodeInstance.Tags).ToNot(ContainElement("opaque-user-tag"))
		Expect(linodeInstance.Tags).To(ContainElement("opaque-user-tag-v2"))
		Expect(linodeInstance.Tags).To(ContainElement("lke12345"))
	})

	It("should remove user tags when the nodeclass tags become empty", func() {
		nodeClaim := newNodeClaim()

		ExpectApplied(ctx, env.Client, nodeClass, nodeClaim)
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClaim)

		nodeClass = ExpectExists(ctx, env.Client, nodeClass)
		nodeClass.Spec.Tags = nil
		ExpectApplied(ctx, env.Client, nodeClass)
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClaim)

		linodeInstance = lo.Must(linodeEnv.LinodeAPI.Instances.Load(linodeInstance.ID)).(linodego.Instance)
		tags := utils.TagListToMap(linodeInstance.Tags)
		Expect(tags).ToNot(HaveKey("owner"))
		Expect(tags).To(HaveKeyWithValue("external", "keep"))
		Expect(tags).To(HaveKeyWithValue(v1.NameTagKey, "default"))
		Expect(tags).To(HaveKeyWithValue(v1.LKEClusterNameTagKey, options.FromContext(ctx).ClusterName))
		Expect(linodeInstance.Tags).ToNot(ContainElement("opaque-user-tag"))
		Expect(linodeInstance.Tags).To(ContainElement("lke12345"))
	})
})
