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

package cache_test

import (
	"context"
	"testing"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/linode/karpenter-provider-linode/pkg/cache"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/karpenter/pkg/utils/testing"
)

var ctx context.Context

func TestLinode(t *testing.T) {
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "Cache")
}

var _ = Describe("Cache", func() {
	var unavailableOfferingCache *cache.UnavailableOfferings

	BeforeEach(func() {
		unavailableOfferingCache = cache.NewUnavailableOfferings()
	})
	Context("Unavailable Offering Cache", func() {
		It("should mark offerings as unavailable when calling MarkUnavailable", func() {
			// offerings should initially not be marked as unavailable
			Expect(unavailableOfferingCache.IsUnavailable("g6-standard-2", "test-zone-1a", karpv1.CapacityTypeOnDemand)).To(BeFalse())
			Expect(unavailableOfferingCache.IsUnavailable("g6-standard-4", "test-zone-1b", karpv1.CapacityTypeOnDemand)).To(BeFalse())

			// g6-standard-2 should return that it's unavailable when we mark it
			unavailableOfferingCache.MarkUnavailable(ctx, "test", "g6-standard-2", "test-zone-1a", karpv1.CapacityTypeOnDemand)
			Expect(unavailableOfferingCache.IsUnavailable("g6-standard-2", "test-zone-1a", karpv1.CapacityTypeOnDemand)).To(BeTrue())
			Expect(unavailableOfferingCache.IsUnavailable("g6-standard-4", "test-zone-1b", karpv1.CapacityTypeOnDemand)).To(BeFalse())

			// g6-standard-4 shouldn't return that it's unavailable when marking an unrelated instance type
			unavailableOfferingCache.MarkUnavailable(ctx, "test", "g6-standard-2", "test-zone-1b", karpv1.CapacityTypeOnDemand)
			Expect(unavailableOfferingCache.IsUnavailable("g6-standard-2", "test-zone-1a", karpv1.CapacityTypeOnDemand)).To(BeTrue())
			Expect(unavailableOfferingCache.IsUnavailable("g6-standard-4", "test-zone-1b", karpv1.CapacityTypeOnDemand)).To(BeFalse())

			// g6-standard-4 should return that it's unavailable when we mark it
			unavailableOfferingCache.MarkUnavailable(ctx, "test", "g6-standard-4", "test-zone-1b", karpv1.CapacityTypeOnDemand)
			Expect(unavailableOfferingCache.IsUnavailable("g6-standard-2", "test-zone-1a", karpv1.CapacityTypeOnDemand)).To(BeTrue())
			Expect(unavailableOfferingCache.IsUnavailable("g6-standard-4", "test-zone-1b", karpv1.CapacityTypeOnDemand)).To(BeTrue())
		})
		It("should mark offerings as unavailable when calling MarkRegionUnavailable", func() {
			// offerings should initially not be marked as unavailable
			Expect(unavailableOfferingCache.IsUnavailable("g6-standard-2", "test-zone-1a", karpv1.CapacityTypeOnDemand)).To(BeFalse())
			Expect(unavailableOfferingCache.IsUnavailable("g6-standard-4", "test-zone-1a", karpv1.CapacityTypeOnDemand)).To(BeFalse())
			Expect(unavailableOfferingCache.IsUnavailable("g6-standard-4", "test-zone-1a", karpv1.CapacityTypeOnDemand)).To(BeFalse())
			Expect(unavailableOfferingCache.IsUnavailable("g6-standard-4", "test-zone-1b", karpv1.CapacityTypeOnDemand)).To(BeFalse())

			// mark all test-zone-1a offerings as unavailable
			unavailableOfferingCache.MarkRegionUnavailable("test-zone-1a")
			Expect(unavailableOfferingCache.IsUnavailable("g6-standard-2", "test-zone-1a", karpv1.CapacityTypeOnDemand)).To(BeTrue())
			Expect(unavailableOfferingCache.IsUnavailable("g6-standard-4", "test-zone-1a", karpv1.CapacityTypeOnDemand)).To(BeTrue())
			Expect(unavailableOfferingCache.IsUnavailable("g6-standard-4", "test-zone-1a", karpv1.CapacityTypeOnDemand)).To(BeTrue())
			Expect(unavailableOfferingCache.IsUnavailable("g6-standard-4", "test-zone-1b", karpv1.CapacityTypeOnDemand)).To(BeFalse())
		})
		It("should increase sequence number when unavailability changes", func() {
			// sequence numbers should initially be 0
			Expect(unavailableOfferingCache.SeqNum("g6-standard-2")).To(BeNumerically("==", 0))
			Expect(unavailableOfferingCache.SeqNum("g6-standard-4")).To(BeNumerically("==", 0))

			// marking g6-standard-2 as unavailable should increase the sequence number for that instance type but not others
			unavailableOfferingCache.MarkUnavailable(ctx, "test", "g6-standard-2", "test-zone-1a", karpv1.CapacityTypeOnDemand)
			Expect(unavailableOfferingCache.SeqNum("g6-standard-2")).To(BeNumerically("==", 1))
			Expect(unavailableOfferingCache.SeqNum("g6-standard-4")).To(BeNumerically("==", 0))

			// marking g6-standard-4 as unavailable should increase the sequence number for that instance type but not others
			unavailableOfferingCache.MarkUnavailable(ctx, "test", "g6-standard-4", "test-zone-1a", karpv1.CapacityTypeOnDemand)
			Expect(unavailableOfferingCache.SeqNum("g6-standard-2")).To(BeNumerically("==", 1))
			Expect(unavailableOfferingCache.SeqNum("g6-standard-4")).To(BeNumerically("==", 1))

			// marking test-zone-1a as unavailable should increase the sequence number for all instance types
			unavailableOfferingCache.MarkRegionUnavailable("test-zone-1a")
			Expect(unavailableOfferingCache.SeqNum("g6-standard-2")).To(BeNumerically("==", 2))
			Expect(unavailableOfferingCache.SeqNum("g6-standard-4")).To(BeNumerically("==", 2))

			// marking test-zone-1b as unavailable should increase the sequence number for all instance types
			unavailableOfferingCache.MarkRegionUnavailable("test-zone-1b")
			Expect(unavailableOfferingCache.SeqNum("g6-standard-2")).To(BeNumerically("==", 3))
			Expect(unavailableOfferingCache.SeqNum("g6-standard-4")).To(BeNumerically("==", 3))

			// deleting g6-standard-4 from the cache should increase the sequence number for that instance type but not others
			unavailableOfferingCache.Delete("g6-standard-4", "test-zone-1a", karpv1.CapacityTypeOnDemand)
			Expect(unavailableOfferingCache.SeqNum("g6-standard-2")).To(BeNumerically("==", 3))
			Expect(unavailableOfferingCache.SeqNum("g6-standard-4")).To(BeNumerically("==", 4))
		})
	})
})
