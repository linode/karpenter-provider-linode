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

/* import (
	"github.com/awslabs/operatorpkg/status"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/karpenter/pkg/events"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
	"github.com/linode/karpenter-provider-linode/pkg/controllers/nodeclass"
	"github.com/linode/karpenter-provider-linode/pkg/fake"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/test"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
)

var _ = Describe("NodeClass Validation Status Controller", func() {
	Context("Preconditions", func() {
		var reconciler *nodeclass.Validation
		BeforeEach(func() {
			reconciler = nodeclass.NewValidationReconciler(
				env.Client,
				cloudProvider,
				linodeEnv.LinodeAPI,
				linodeEnv.InstanceTypesProvider,
				linodeEnv.ValidationCache,
				options.FromContext(ctx).DisableDryRun,
			)
		})
		DescribeTable(
			"should set validated status condition to false when any required condition is false",
			func(cond string) {
				nodeClass.StatusConditions().SetFalse(cond, "test", "test")
				_, err := reconciler.Reconcile(ctx, nodeClass)
				Expect(err).ToNot(HaveOccurred())
				Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).IsFalse()).To(BeTrue())
				Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).Reason).To(Equal(nodeclass.ConditionReasonDependenciesNotReady))
			},
		)
		DescribeTable(
			"should set validated status condition to unknown when no required condition is false and any are unknown",
			func(cond string) {
				nodeClass.StatusConditions().SetUnknown(cond)
				_, err := reconciler.Reconcile(ctx, nodeClass)
				Expect(err).ToNot(HaveOccurred())
				Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).IsUnknown()).To(BeTrue())
				Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).Reason).To(Equal(nodeclass.ConditionReasonDependenciesNotReady))
			},
		)
	})
	Context("Tag Validation", func() {
		BeforeEach(func() {
			nodeClass = test.LinodeNodeClass(v1.LinodeNodeClass{
				Spec: v1.LinodeNodeClassSpec{
					Tags: []string{"kubernetes.io/cluster/anothercluster:owned"},
				},
			})
		})
		DescribeTable("should update status condition on nodeClass as NotReady when tag validation fails", func(illegalTag []string) {
			nodeClass.Spec.Tags = illegalTag
			ExpectApplied(ctx, env.Client, nodeClass)
			err := ExpectObjectReconcileFailed(ctx, env.Client, controller, nodeClass)
			Expect(err).To(HaveOccurred())
			nodeClass = ExpectExists(ctx, env.Client, nodeClass)
			Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).IsFalse()).To(BeTrue())
			Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).Reason).To(Equal("TagValidationFailed"))
			Expect(nodeClass.StatusConditions().Get(status.ConditionReady).IsFalse()).To(BeTrue())
			Expect(nodeClass.StatusConditions().Get(status.ConditionReady).Message).To(Equal("ValidationSucceeded=False"))
		},
			Entry("kubernetes.io/cluster*", map[string]string{"kubernetes.io/cluster/acluster": "owned"}),
			Entry(v1.NodePoolTagKey, map[string]string{v1.NodePoolTagKey: "testnodepool"}),
			Entry(v1.LKEClusterNameTagKey, map[string]string{v1.LKEClusterNameTagKey: "acluster"}),
			Entry(v1.NodeClassTagKey, map[string]string{v1.NodeClassTagKey: "testnodeclass"}),
			Entry(v1.NodeClaimTagKey, map[string]string{v1.NodeClaimTagKey: "testnodeclaim"}),
		)
		It("should update status condition as Ready when tags are valid", func() {
			nodeClass.Spec.Tags = []string{}
			ExpectApplied(ctx, env.Client, nodeClass)
			ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)
			nodeClass = ExpectExists(ctx, env.Client, nodeClass)

			Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).IsTrue()).To(BeTrue())
			Expect(nodeClass.StatusConditions().Get(status.ConditionReady).IsTrue()).To(BeTrue())
		})
	})
	It("should not skip validation when new annotation is added", func() {
		ExpectApplied(ctx, env.Client, nodeClass)
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)
		nodeClass = ExpectExists(ctx, env.Client, nodeClass)
		Expect(linodeEnv.ValidationCache.Items()).To(HaveLen(1))
		nodeClass.SetAnnotations(map[string]string{"testing": "validation"})
		ExpectApplied(ctx, env.Client, nodeClass)

		linodeEnv.LinodeAPI.Reset()
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)
		nodeClass = ExpectExists(ctx, env.Client, nodeClass)

		Expect(linodeEnv.LinodeAPI.CreateInstanceBehavior.Calls()).To(Equal(1))
	})
	It("should clear the validation cache when the nodeclass is deleted", func() {
		controllerutil.AddFinalizer(nodeClass, v1.TerminationFinalizer)
		nodeClass.Spec.Tags = []string{}
		ExpectApplied(ctx, env.Client, nodeClass)
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)
		nodeClass = ExpectExists(ctx, env.Client, nodeClass)
		Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).IsTrue()).To(BeTrue())
		Expect(nodeClass.StatusConditions().Get(status.ConditionReady).IsTrue()).To(BeTrue())
		Expect(linodeEnv.ValidationCache.Items()).To(HaveLen(1))

		Expect(env.Client.Delete(ctx, nodeClass)).To(Succeed())
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)
		ExpectNotFound(ctx, env.Client, nodeClass)
		Expect(linodeEnv.ValidationCache.Items()).To(HaveLen(0))
	})
	It("should pass validation when the validation controller is disabled", func() {
		controller = nodeclass.NewController(
			env.Client,
			cloudProvider,
			events.NewRecorder(&record.FakeRecorder{}),
			fake.DefaultRegion,
			linodeEnv.InstanceTypesProvider,
			linodeEnv.LinodeAPI,
			linodeEnv.ValidationCache,
			true,
		)
		ExpectApplied(ctx, env.Client, nodeClass)
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)
		nodeClass = ExpectExists(ctx, env.Client, nodeClass)
		Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).IsTrue()).To(BeTrue())
		Expect(nodeClass.StatusConditions().Get(status.ConditionReady).IsTrue()).To(BeTrue())
		// The cache still has an entry so we don't revalidate tags
		Expect(linodeEnv.ValidationCache.Items()).To(HaveLen(1))
		// We shouldn't make any new calls when validation is disabled
		Expect(linodeEnv.LinodeAPI.CreateInstanceBehavior.Calls()).To(Equal(0))
	})
}) */
