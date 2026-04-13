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

package hash_test

import (
	"context"
	"testing"

	"github.com/awslabs/operatorpkg/object"
	"github.com/awslabs/operatorpkg/status"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
	coretest "sigs.k8s.io/karpenter/pkg/test"
	karpv1alpha1 "sigs.k8s.io/karpenter/pkg/test/v1alpha1"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
	v1alpha1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	"github.com/linode/karpenter-provider-linode/pkg/controllers/nodeclass/hash"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/test"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
	. "sigs.k8s.io/karpenter/pkg/utils/testing"
)

var ctx context.Context
var env *coretest.Environment
var controller *hash.Controller
var nodeClass *v1alpha1.LinodeNodeClass

func TestAPIs(t *testing.T) {
	t.Parallel()
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "LinodeNodeClassHashController")
}

var _ = BeforeSuite(func() {
	env = coretest.NewEnvironment(
		coretest.WithCRDs(apis.CRDs...),
		coretest.WithCRDs(karpv1alpha1.CRDs...),
		coretest.WithFieldIndexers(coretest.NodeClaimNodeClassRefFieldIndexer(ctx)),
	)
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: lo.ToPtr(true)}}))
	ctx = options.ToContext(ctx, test.Options())
})

var _ = AfterSuite(func() {
	Expect(env.Stop()).To(Succeed(), "Failed to stop environment")
})

var _ = BeforeEach(func() {
	nodeClass = test.LinodeNodeClass()
	controller = hash.NewController(env.Client)
})

var _ = AfterEach(func() {
	ExpectCleanedUp(ctx, env.Client)
})

var _ = Describe("Hash Controller", func() {
	It("should write LinodeNodeClass hash annotations", func() {
		ExpectApplied(ctx, env.Client, nodeClass)
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)

		nodeClass = ExpectExists(ctx, env.Client, nodeClass)
		Expect(nodeClass.Annotations[v1alpha1.AnnotationLinodeNodeClassHash]).To(Equal(nodeClass.Hash()))
		Expect(nodeClass.Annotations[v1alpha1.AnnotationLinodeNodeClassHashVersion]).To(Equal(v1alpha1.LinodeNodeClassHashVersion))
	})

	It("should backfill referenced NodeClaims when the NodeClass hash version annotation is missing", func() {
		nodeClaim := coretest.NodeClaim(karpv1.NodeClaim{
			Spec: karpv1.NodeClaimSpec{
				NodeClassRef: &karpv1.NodeClassReference{
					Group: object.GVK(nodeClass).Group,
					Kind:  object.GVK(nodeClass).Kind,
					Name:  nodeClass.Name,
				},
			},
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					v1alpha1.AnnotationLinodeNodeClassHash: "old-hash",
				},
			},
		})

		ExpectApplied(ctx, env.Client, nodeClass, nodeClaim)
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)

		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		Expect(nodeClaim.Annotations[v1alpha1.AnnotationLinodeNodeClassHashVersion]).To(Equal(v1alpha1.LinodeNodeClassHashVersion))
		Expect(nodeClaim.Annotations[v1alpha1.AnnotationLinodeNodeClassHash]).To(Equal(nodeClass.Hash()))
	})

	It("should update referenced NodeClaims on a hash version bump", func() {
		nodeClass.Annotations = map[string]string{
			v1alpha1.AnnotationLinodeNodeClassHash:        "old-hash",
			v1alpha1.AnnotationLinodeNodeClassHashVersion: "old-version",
		}
		nodeClaim := coretest.NodeClaim(karpv1.NodeClaim{
			Spec: karpv1.NodeClaimSpec{
				NodeClassRef: &karpv1.NodeClassReference{
					Group: object.GVK(nodeClass).Group,
					Kind:  object.GVK(nodeClass).Kind,
					Name:  nodeClass.Name,
				},
			},
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					v1alpha1.AnnotationLinodeNodeClassHash:        "old-hash",
					v1alpha1.AnnotationLinodeNodeClassHashVersion: "old-version",
				},
			},
		})

		ExpectApplied(ctx, env.Client, nodeClass, nodeClaim)
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)

		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		Expect(nodeClaim.Annotations[v1alpha1.AnnotationLinodeNodeClassHashVersion]).To(Equal(v1alpha1.LinodeNodeClassHashVersion))
		Expect(nodeClaim.Annotations[v1alpha1.AnnotationLinodeNodeClassHash]).To(Equal(nodeClass.Hash()))
	})

	It("should not rewrite the hash for already drifted NodeClaims on version bump", func() {
		nodeClass.Annotations = map[string]string{
			v1alpha1.AnnotationLinodeNodeClassHash:        "old-hash",
			v1alpha1.AnnotationLinodeNodeClassHashVersion: "old-version",
		}
		nodeClaim := coretest.NodeClaim(karpv1.NodeClaim{
			Spec: karpv1.NodeClaimSpec{
				NodeClassRef: &karpv1.NodeClassReference{
					Group: object.GVK(nodeClass).Group,
					Kind:  object.GVK(nodeClass).Kind,
					Name:  nodeClass.Name,
				},
			},
			Status: karpv1.NodeClaimStatus{
				Conditions: []status.Condition{{
					Type:               string(karpv1.ConditionTypeDrifted),
					Status:             "True",
					LastTransitionTime: metav1.Now(),
				}},
			},
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					v1alpha1.AnnotationLinodeNodeClassHash:        "old-hash",
					v1alpha1.AnnotationLinodeNodeClassHashVersion: "old-version",
				},
			},
		})

		ExpectApplied(ctx, env.Client, nodeClass, nodeClaim)
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)

		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		Expect(nodeClaim.Annotations[v1alpha1.AnnotationLinodeNodeClassHashVersion]).To(Equal(v1alpha1.LinodeNodeClassHashVersion))
		Expect(nodeClaim.Annotations[v1alpha1.AnnotationLinodeNodeClassHash]).To(Equal("old-hash"))
	})
})
