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
	"time"

	"github.com/awslabs/operatorpkg/status"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	"github.com/linode/karpenter-provider-linode/pkg/controllers/nodeclass"
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
})
