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

package options_test

import (
	"context"
	"flag"
	"os"
	"testing"

	"github.com/samber/lo"
	coreoperator "sigs.k8s.io/karpenter/pkg/operator"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
	coretest "sigs.k8s.io/karpenter/pkg/test"

	"github.com/linode/karpenter-provider-linode/pkg/operator"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instance"
	"github.com/linode/karpenter-provider-linode/pkg/providers/lke"
	"github.com/linode/karpenter-provider-linode/pkg/test"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/karpenter/pkg/utils/testing"
)

var ctx context.Context
var linodeEnv *test.Environment
var stop context.CancelFunc

func TestAPIs(t *testing.T) {
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "Options")
}

var _ = BeforeSuite(func() {
	ctx = coreoptions.ToContext(ctx, coretest.Options(coretest.OptionsFields{FeatureGates: coretest.FeatureGates{ReservedCapacity: lo.ToPtr(true)}}))
	ctx = options.ToContext(ctx, test.Options())
	ctx, stop = context.WithCancel(ctx)
	linodeEnv = test.NewEnvironment(ctx)
})

var _ = AfterSuite(func() {
	stop()
})

var _ = Describe("Options", func() {
	var fs *coreoptions.FlagSet
	var opts *options.Options

	BeforeEach(func() {
		fs = &coreoptions.FlagSet{
			FlagSet: flag.NewFlagSet("karpenter", flag.ContinueOnError),
		}
		opts = &options.Options{}
		linodeEnv.Reset()
	})
	AfterEach(func() {
		os.Clearenv()
	})

	It("should correctly override default vars when CLI flags are set", func() {
		opts.AddFlags(fs)
		err := opts.Parse(fs,
			"--cluster-name", "env-cluster",
			"--cluster-endpoint", "https://env-cluster",
			"--cluster-region", "us-west",
			"--vm-memory-overhead-percent", "0.1")
		Expect(err).ToNot(HaveOccurred())
		expectOptionsEqual(opts, test.Options(test.OptionsFields{
			ClusterName:             lo.ToPtr("env-cluster"),
			ClusterEndpoint:         lo.ToPtr("https://env-cluster"),
			ClusterRegion:           lo.ToPtr("us-west"),
			VMMemoryOverheadPercent: lo.ToPtr[float64](0.1),
		}))
	})
	It("should use lke provider if mode is not set", func() {
		Expect(os.Setenv("LINODE_TOKEN", "fake-token")).To(Succeed())
		Expect(os.Setenv("LINODE_CLIENT_TIMEOUT", "10")).To(Succeed())

		os.Args = os.Args[:1] // Clear any existing args for the test

		ctx := options.ToContext(context.Background(), test.Options(test.OptionsFields{
			ClusterName: lo.ToPtr("env-cluster"),
		}))
		_, op, err := operator.NewOperator(ctx, &coreoperator.Operator{}, linodeEnv.LinodeAPI)
		Expect(err).NotTo(HaveOccurred())
		Expect(op.NodeProvider).To(BeAssignableToTypeOf(&lke.DefaultProvider{}))
	})
	It("should use instance provider if in instance mode", func() {
		Expect(os.Setenv("LINODE_TOKEN", "fake-token")).To(Succeed())
		Expect(os.Setenv("LINODE_CLIENT_TIMEOUT", "10")).To(Succeed())

		os.Args = os.Args[:1] // Clear any existing args for the test

		ctx := options.ToContext(context.Background(), test.Options(test.OptionsFields{
			Mode:                    lo.ToPtr("instance"),
			ClusterName:             lo.ToPtr("env-cluster"),
			ClusterEndpoint:         lo.ToPtr("https://env-cluster"),
			ClusterRegion:           lo.ToPtr("us-west"),
			VMMemoryOverheadPercent: lo.ToPtr[float64](0.1),
		}))
		_, op, err := operator.NewOperator(ctx, &coreoperator.Operator{}, linodeEnv.LinodeAPI)
		Expect(err).NotTo(HaveOccurred())
		Expect(op.NodeProvider).To(BeAssignableToTypeOf(&instance.DefaultProvider{}))
	})
	It("should correctly fallback to env vars when CLI flags aren't set", func() {
		Expect(os.Setenv("CLUSTER_NAME", "env-cluster"))
		Expect(os.Setenv("CLUSTER_ID", "12345"))
		Expect(os.Setenv("CLUSTER_REGION", "us-west"))
		Expect(os.Setenv("CLUSTER_ENDPOINT", "https://env-cluster"))
		Expect(os.Setenv("VM_MEMORY_OVERHEAD_PERCENT", "0.1"))

		// Add flags after we set the environment variables so that the parsing logic correctly refers
		// to the new environment variable values
		opts.AddFlags(fs)
		err := opts.Parse(fs)
		Expect(err).ToNot(HaveOccurred())
		expectOptionsEqual(opts, test.Options(test.OptionsFields{
			ClusterName:             lo.ToPtr("env-cluster"),
			ClusterEndpoint:         lo.ToPtr("https://env-cluster"),
			ClusterRegion:           lo.ToPtr("us-west"),
			VMMemoryOverheadPercent: lo.ToPtr[float64](0.1),
		}))
	})

	Context("Validation", func() {
		BeforeEach(func() {
			opts.AddFlags(fs)
		})
		It("should fail when cluster id is not set", func() {
			err := opts.Parse(fs)
			Expect(err).To(HaveOccurred())
		})
		It("should fail when clusterEndpoint is invalid (not absolute)", func() {
			err := opts.Parse(fs, "--cluster-name", "test-cluster", "--cluster-endpoint", "00000000000000000000000.test.us-west.linode.com")
			Expect(err).To(HaveOccurred())
		})
		It("should fail when vmMemoryOverheadPercent is negative", func() {
			err := opts.Parse(fs, "--cluster-name", "test-cluster", "--vm-memory-overhead-percent", "-0.01")
			Expect(err).To(HaveOccurred())
		})
	})
})

func expectOptionsEqual(optsA *options.Options, optsB *options.Options) {
	GinkgoHelper()
	Expect(optsA.ClusterName).To(Equal(optsB.ClusterName))
	Expect(optsA.ClusterEndpoint).To(Equal(optsB.ClusterEndpoint))
	Expect(optsA.VMMemoryOverheadPercent).To(Equal(optsB.VMMemoryOverheadPercent))
	Expect(optsA.ClusterRegion).To(Equal(optsB.ClusterRegion))
}
