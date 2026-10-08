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
	"fmt"
	"strings"
	"time"

	"github.com/awslabs/operatorpkg/status"
	"github.com/linode/linodego/v2"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	"github.com/linode/karpenter-provider-linode/pkg/controllers/nodeclass"
	"github.com/linode/karpenter-provider-linode/pkg/fake"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/test"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
)

var _ = Describe("NodeClass Validation", func() {
	BeforeEach(func() {
		nodeClass = test.LinodeNodeClass()
	})

	DescribeTable("should fail validation for restricted tags", func(tag string) {
		nodeClass.Spec.Tags = []string{tag}

		ExpectApplied(ctx, env.Client, nodeClass)
		err := ExpectObjectReconcileFailed(ctx, env.Client, controller, nodeClass)
		Expect(err).To(HaveOccurred())

		nodeClass = ExpectExists(ctx, env.Client, nodeClass)
		Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).IsFalse()).To(BeTrue())
		Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).Reason).To(Equal(nodeclass.ConditionReasonTagValidationFailed))
		Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).Message).To(ContainSubstring(tag))
		Expect(nodeClass.StatusConditions().Get(status.ConditionReady).IsFalse()).To(BeTrue())
		Expect(linodeEnv.EventRecorder.Events()).To(BeEmpty())
	},
		Entry("nodepool key", "karpenter.sh/nodepool=test"),
		Entry("nodeclass key", "karpenter.k8s.linode/linodenodeclass=test"),
		Entry("lke managed key", "karpenter.k8s.linode/lke-managed=true"),
		Entry("nodeclaim key", "karpenter.sh/nodeclaim=test"),
		Entry("cluster name key", "lke-cluster-name=test"),
		Entry("Name key", "Name=test"),
	)

	It("should allow non-restricted tags", func() {
		nodeClass.Spec.Tags = []string{"owner=platform", "opaque-user-tag"}

		ExpectApplied(ctx, env.Client, nodeClass)
		res := ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)
		nodeClass = ExpectExists(ctx, env.Client, nodeClass)

		Expect(res.RequeueAfter).To(Equal(10 * time.Minute))
		Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).IsTrue()).To(BeTrue())
		Expect(nodeClass.StatusConditions().Get(status.ConditionReady).IsTrue()).To(BeTrue())
	})

	It("should reject overlong user tags before LKE API validation", func() {
		tag := "env=" + strings.Repeat("a", 47)
		nodeClass.Spec.Tags = []string{tag}
		nodeClass.Spec.LKEK8sVersion = new("v1.31.9+lke7")

		ExpectApplied(ctx, env.Client, nodeClass)
		err := ExpectObjectReconcileFailed(ctx, env.Client, controller, nodeClass)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(tag))
		Expect(linodeEnv.LinodeAPI.GetLKEClusterBehavior.Calls()).To(Equal(0))
	})

	It("should reject an overlong generated cluster tag before LKE API validation", func() {
		clusterName := strings.Repeat("c", 23)
		ctx = options.ToContext(ctx, test.Options(test.OptionsFields{ClusterName: &clusterName}))
		nodeClass.Spec.LKEK8sVersion = new("v1.31.9+lke7")

		ExpectApplied(ctx, env.Client, nodeClass)
		err := ExpectObjectReconcileFailed(ctx, env.Client, controller, nodeClass)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("kubernetes.io/cluster/" + clusterName + "=owned"))

		nodeClass = ExpectExists(ctx, env.Client, nodeClass)
		Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).IsFalse()).To(BeTrue())
		Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).Reason).To(Equal(nodeclass.ConditionReasonTagValidationFailed))
		Expect(linodeEnv.LinodeAPI.GetLKEClusterBehavior.Calls()).To(Equal(0))
	})

	It("should report all tag validation failures", func() {
		clusterName := strings.Repeat("c", 23)
		ctx = options.ToContext(ctx, test.Options(test.OptionsFields{ClusterName: &clusterName}))
		restrictedTag := "karpenter.sh/nodepool=test"
		overlongUserTag := "env=" + strings.Repeat("a", 47)
		overlongClusterTag := "kubernetes.io/cluster/" + clusterName + "=owned"
		nodeClass.Spec.Tags = []string{restrictedTag, overlongUserTag}
		nodeClass.Spec.LKEK8sVersion = new("v1.31.9+lke7")

		ExpectApplied(ctx, env.Client, nodeClass)
		err := ExpectObjectReconcileFailed(ctx, env.Client, controller, nodeClass)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(restrictedTag))
		Expect(err.Error()).To(ContainSubstring(overlongUserTag))
		Expect(err.Error()).To(ContainSubstring(overlongClusterTag))

		nodeClass = ExpectExists(ctx, env.Client, nodeClass)
		condition := nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded)
		Expect(condition.IsFalse()).To(BeTrue())
		Expect(condition.Message).To(ContainSubstring(restrictedTag))
		Expect(condition.Message).To(ContainSubstring(overlongUserTag))
		Expect(condition.Message).To(ContainSubstring(overlongClusterTag))
		Expect(linodeEnv.LinodeAPI.GetLKEClusterBehavior.Calls()).To(Equal(0))
	})

	It("should set ValidationSucceeded false for lkeK8sVersion on standard tier", func() {
		nodeClass.Spec.LKEK8sVersion = new("v1.31.9+lke7")
		linodeEnv.LinodeAPI.ClusterTier = linodego.LKEVersionStandard
		linodeEnv.LinodeAPI.ClusterVersion = fake.DefaultClusterVersion

		ExpectApplied(ctx, env.Client, nodeClass)
		res := ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)
		nodeClass = ExpectExists(ctx, env.Client, nodeClass)

		Expect(res.RequeueAfter).To(Equal(time.Minute))
		Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).IsFalse()).To(BeTrue())
		Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).Reason).To(Equal(nodeclass.ConditionReasonLKEK8sVersionUnsupported))
		Expect(nodeClass.StatusConditions().Get(status.ConditionReady).IsFalse()).To(BeTrue())
		Expect(linodeEnv.EventRecorder.Calls(nodeclass.ConditionReasonLKEK8sVersionUnsupported)).To(Equal(1))
	})

	It("should set ValidationSucceeded false when lkeK8sVersion does not match the enterprise control plane", func() {
		nodeClass.Spec.LKEK8sVersion = new("v1.32.8+lke13")
		linodeEnv.LinodeAPI.ClusterTier = linodego.LKEVersionEnterprise
		linodeEnv.LinodeAPI.ClusterVersion = "v1.31.9+lke7"

		ExpectApplied(ctx, env.Client, nodeClass)
		res := ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)
		nodeClass = ExpectExists(ctx, env.Client, nodeClass)

		Expect(res.RequeueAfter).To(Equal(time.Minute))
		Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).IsFalse()).To(BeTrue())
		Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).Reason).To(Equal(nodeclass.ConditionReasonLKEK8sVersionControlPlaneMismatch))
		Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).Message).To(ContainSubstring(`current cluster version is "v1.31.9+lke7"`))
		Expect(nodeClass.StatusConditions().Get(status.ConditionReady).IsFalse()).To(BeTrue())
		Expect(linodeEnv.EventRecorder.Calls(nodeclass.ConditionReasonLKEK8sVersionControlPlaneMismatch)).To(Equal(1))
	})

	It("should set ValidationSucceeded true when lkeK8sVersion matches the enterprise control plane", func() {
		nodeClass.Spec.LKEK8sVersion = new("v1.31.9+lke7")
		linodeEnv.LinodeAPI.ClusterTier = linodego.LKEVersionEnterprise
		linodeEnv.LinodeAPI.ClusterVersion = "v1.31.9+lke7"

		ExpectApplied(ctx, env.Client, nodeClass)
		res := ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)
		nodeClass = ExpectExists(ctx, env.Client, nodeClass)

		Expect(res.RequeueAfter).To(Equal(10 * time.Minute))
		Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).IsTrue()).To(BeTrue())
		Expect(nodeClass.StatusConditions().Get(status.ConditionReady).IsTrue()).To(BeTrue())
		Expect(linodeEnv.EventRecorder.Events()).To(BeEmpty())
	})

	It("should return an error on transient GetLKECluster failures without setting a false condition", func() {
		nodeClass.Spec.LKEK8sVersion = new("v1.31.9+lke7")
		linodeEnv.LinodeAPI.ClusterTier = linodego.LKEVersionEnterprise
		linodeEnv.LinodeAPI.GetLKEClusterBehavior.Error.Set(fmt.Errorf("boom"))

		ExpectApplied(ctx, env.Client, nodeClass)
		err := ExpectObjectReconcileFailed(ctx, env.Client, controller, nodeClass)
		Expect(err).To(HaveOccurred())

		nodeClass = ExpectExists(ctx, env.Client, nodeClass)
		Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).IsFalse()).To(BeFalse())
		Expect(linodeEnv.EventRecorder.Events()).To(BeEmpty())
	})

	It("should revalidate lkeK8sVersion on spec updates", func() {
		nodeClass.Spec.LKEK8sVersion = new("v1.32.8+lke13")
		linodeEnv.LinodeAPI.ClusterTier = linodego.LKEVersionEnterprise
		linodeEnv.LinodeAPI.ClusterVersion = "v1.31.9+lke7"

		ExpectApplied(ctx, env.Client, nodeClass)
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)
		nodeClass = ExpectExists(ctx, env.Client, nodeClass)
		Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).IsFalse()).To(BeTrue())

		nodeClass.Spec.LKEK8sVersion = new("v1.31.9+lke7")
		ExpectApplied(ctx, env.Client, nodeClass)
		ExpectObjectReconciled(ctx, env.Client, controller, nodeClass)
		nodeClass = ExpectExists(ctx, env.Client, nodeClass)

		Expect(nodeClass.StatusConditions().Get(v1.ConditionTypeValidationSucceeded).IsTrue()).To(BeTrue())
		Expect(nodeClass.StatusConditions().Get(status.ConditionReady).IsTrue()).To(BeTrue())
	})
})
