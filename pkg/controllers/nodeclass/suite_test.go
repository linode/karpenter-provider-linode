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

package nodeclass_test

import (
	"context"
	"testing"
	"time"

	"github.com/awslabs/operatorpkg/object"
	"github.com/samber/lo"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/events"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
	coretest "sigs.k8s.io/karpenter/pkg/test"
	"sigs.k8s.io/karpenter/pkg/test/v1alpha1"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
	"github.com/linode/karpenter-provider-linode/pkg/cloudprovider"
	"github.com/linode/karpenter-provider-linode/pkg/controllers/nodeclass"
	"github.com/linode/karpenter-provider-linode/pkg/fake"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/test"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
	. "sigs.k8s.io/karpenter/pkg/utils/testing"
)

var ctx context.Context
var env *coretest.Environment
var linodeEnv *test.Environment
var nodeClass *v1.LinodeNodeClass
var controller *nodeclass.Controller
var cloudProvider *cloudprovider.CloudProvider

func TestAPIs(t *testing.T) {
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "LinodeNodeClass")
}

var _ = BeforeSuite(func() {
	env = coretest.NewEnvironment(
		coretest.WithCRDs(apis.CRDs...),
		coretest.WithCRDs(v1alpha1.CRDs...),
		coretest.WithFieldIndexers(coretest.NodeClaimNodeClassRefFieldIndexer(ctx)),
		coretest.WithFieldIndexers(coretest.NodePoolNodeClassRefFieldIndexer(ctx)),
	)
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: lo.ToPtr(true)}}))
	ctx = options.ToContext(ctx, test.Options())
	linodeEnv = test.NewEnvironment(ctx)
	cloudProvider = cloudprovider.New(linodeEnv.InstanceTypesProvider, linodeEnv.InstanceProvider, events.NewRecorder(&record.FakeRecorder{}), env.Client)
})

var _ = AfterSuite(func() {
	Expect(env.Stop()).To(Succeed(), "Failed to stop environment")
})

var _ = BeforeEach(func() {
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: lo.ToPtr(true)}}))
	ctx = options.ToContext(ctx, test.Options())
	nodeClass = test.LinodeNodeClass()
	linodeEnv.Reset()
	Expect(linodeEnv.InstanceTypesProvider.UpdateInstanceTypes(ctx)).To(Succeed())
	Expect(linodeEnv.InstanceTypesProvider.UpdateInstanceTypeOfferings(ctx)).To(Succeed())
	controller = nodeclass.NewController(
		env.Client,
		events.NewRecorder(&record.FakeRecorder{}),
		fake.DefaultRegion,
	)
})

var _ = AfterEach(func() {
	ExpectCleanedUp(ctx, env.Client)
})

var _ = Describe("NodeClass Termination", func() {
	BeforeEach(func() {
		nodeClass = test.LinodeNodeClass(v1.LinodeNodeClass{
			Spec: v1.LinodeNodeClassSpec{
				Type: "g6-standard-2",
			},
		})
	})
	It("should not delete the LinodeNodeClass until all associated NodeClaims are terminated", func() {
		var nodeClaims []*karpv1.NodeClaim
		for i := 0; i < 2; i++ {
			nc := coretest.NodeClaim(karpv1.NodeClaim{
				Spec: karpv1.NodeClaimSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: object.GVK(nodeClass).Group,
						Kind:  object.GVK(nodeClass).Kind,
						Name:  nodeClass.Name,
					},
				},
			})
			ExpectApplied(ctx, env.Client, nc)
			nodeClaims = append(nodeClaims, nc)
		}

		controllerutil.AddFinalizer(nodeClass, v1.TerminationFinalizer)
		ExpectApplied(ctx, env.Client, nodeClass)
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)

		nodeClass = ExpectExists(ctx, env.Client, nodeClass)

		Expect(env.Client.Delete(ctx, nodeClass)).To(Succeed())
		res := ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)
		Expect(res.RequeueAfter).To(Equal(time.Minute * 10))
		ExpectExists(ctx, env.Client, nodeClass)

		// Delete one of the NodeClaims
		// The NodeClass should still not delete
		ExpectDeleted(ctx, env.Client, nodeClaims[0])
		res = ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)
		Expect(res.RequeueAfter).To(Equal(time.Minute * 10))
		ExpectExists(ctx, env.Client, nodeClass)

		// Delete the last NodeClaim
		// The NodeClass should now delete
		ExpectDeleted(ctx, env.Client, nodeClaims[1])
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)
		ExpectNotFound(ctx, env.Client, nodeClass)
	})
})
