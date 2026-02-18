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

package operator_test

import (
	"context"
	"os"
	"testing"
	"time"

	coretest "sigs.k8s.io/karpenter/pkg/test"
	"sigs.k8s.io/karpenter/pkg/test/v1alpha1"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
	"github.com/linode/karpenter-provider-linode/pkg/operator"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
	. "sigs.k8s.io/karpenter/pkg/utils/testing"
)

var ctx context.Context
var stop context.CancelFunc
var env *coretest.Environment

func TestLinode(t *testing.T) {
	t.Parallel()
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "CloudProvider/Linode")
}

var _ = BeforeSuite(func() {
	env = coretest.NewEnvironment(coretest.WithCRDs(apis.CRDs...), coretest.WithCRDs(v1alpha1.CRDs...))
	ctx, stop = context.WithCancel(ctx)
})

var _ = AfterSuite(func() {
	stop()
	Expect(env.Stop()).To(Succeed(), "Failed to stop environment")
})

var _ = BeforeEach(func() {
})

var _ = AfterEach(func() {
	ExpectCleanedUp(ctx, env.Client)
	os.Clearenv()
})

var _ = Describe("Linode Operator", func() {
	It("should create linode client from valid environment", func() {
		Expect(os.Setenv("LINODE_TOKEN", "fake-token")).To(Succeed())
		Expect(os.Setenv("LINODE_CLIENT_TIMEOUT", "10")).To(Succeed())
		cfg, err := operator.CreateLinodeClientConfig(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).ToNot(BeNil())
		Expect(cfg.Token).To(Equal("fake-token"))
		Expect(cfg.Timeout).To(Equal(10 * time.Second))
	})
	It("should not set timout if it's invalid", func() {
		Expect(os.Setenv("LINODE_TOKEN", "fake-token")).To(Succeed())
		Expect(os.Setenv("LINODE_CLIENT_TIMEOUT", "foo")).To(Succeed())
		cfg, err := operator.CreateLinodeClientConfig(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).ToNot(BeNil())
		Expect(cfg.Token).To(Equal("fake-token"))
		Expect(cfg.Timeout).To(Equal(0 * time.Second))
	})
	It("should fail to create linode client if token is not set", func() {
		_, err := operator.CreateLinodeClientConfig(ctx)
		Expect(err).To(HaveOccurred())
	})
})
