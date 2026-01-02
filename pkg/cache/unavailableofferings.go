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

package cache

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/patrickmn/go-cache"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// UnavailableOfferings stores any offerings that return ICE (insufficient capacity errors) when
// attempting to launch the capacity. These offerings are ignored as long as they are in the cache on
// GetInstanceTypes responses
type UnavailableOfferings struct {
	// key: <instanceType>:<region>, value: struct{}{}
	offeringCache         *cache.Cache
	offeringCacheSeqNumMu sync.RWMutex
	offeringCacheSeqNum   map[string]uint64

	capacityTypeCache       *cache.Cache
	capacityTypeCacheSeqNum atomic.Uint64

	regionCache       *cache.Cache
	regionCacheSeqNum atomic.Uint64
}

func NewUnavailableOfferings() *UnavailableOfferings {
	uo := &UnavailableOfferings{
		offeringCache:         cache.New(UnavailableOfferingsTTL, UnavailableOfferingsCleanupInterval),
		offeringCacheSeqNumMu: sync.RWMutex{},
		offeringCacheSeqNum:   map[string]uint64{},

		capacityTypeCache: cache.New(UnavailableOfferingsTTL, UnavailableOfferingsCleanupInterval),
		regionCache:       cache.New(UnavailableOfferingsTTL, UnavailableOfferingsCleanupInterval),
	}
	uo.offeringCache.OnEvicted(func(k string, _ interface{}) {
		elems := strings.Split(k, ":")
		if len(elems) != 3 {
			panic("unavailable offerings cache key is not of expected format <capacity-type>:<instance-type>:<region>")
		}
		uo.offeringCacheSeqNumMu.Lock()
		uo.offeringCacheSeqNum[elems[1]]++
		uo.offeringCacheSeqNumMu.Unlock()
	})
	uo.capacityTypeCache.OnEvicted(func(k string, _ any) {
		uo.capacityTypeCacheSeqNum.Add(1)
	})
	uo.regionCache.OnEvicted(func(k string, _ interface{}) {
		uo.regionCacheSeqNum.Add(1)
	})
	return uo
}

// SeqNum returns a sequence number for an instance type to capture whether the offering cache has changed for the intance type
func (u *UnavailableOfferings) SeqNum(instanceType string) uint64 {
	u.offeringCacheSeqNumMu.RLock()
	defer u.offeringCacheSeqNumMu.RUnlock()

	v := u.offeringCacheSeqNum[instanceType]
	return v + u.regionCacheSeqNum.Load()
}

// IsUnavailable returns true if the offering appears in the cache
func (u *UnavailableOfferings) IsUnavailable(instanceType, region, capacityType string) bool {
	_, offeringFound := u.offeringCache.Get(u.key(instanceType, region, capacityType))
	_, capacityTypeFound := u.capacityTypeCache.Get(capacityType)
	_, regionFound := u.regionCache.Get(region)
	return offeringFound || capacityTypeFound || regionFound
}

// MarkUnavailable communicates recently observed temporary capacity shortages in the provided offerings
func (u *UnavailableOfferings) MarkUnavailable(ctx context.Context, unavailableReason string, instanceType string, region, capacityType string) {
	// even if the key is already in the cache, we still need to call Set to extend the cached entry's TTL
	log.FromContext(ctx).WithValues(
		"reason", unavailableReason,
		"instance-type", instanceType,
		"region", region,
		"capacity-type", capacityType,
		"ttl", UnavailableOfferingsTTL).V(1).Info("removing offering from offerings")
	u.offeringCache.SetDefault(u.key(instanceType, region, capacityType), struct{}{})
	u.offeringCacheSeqNumMu.Lock()
	u.offeringCacheSeqNum[instanceType]++
	u.offeringCacheSeqNumMu.Unlock()
}

func (u *UnavailableOfferings) MarkCapacityTypeUnavailable(capacityType string) {
	u.capacityTypeCache.SetDefault(capacityType, struct{}{})
	u.capacityTypeCacheSeqNum.Add(1)
}

func (u *UnavailableOfferings) MarkRegionUnavailable(region string) {
	u.regionCache.SetDefault(region, struct{}{})
	u.regionCacheSeqNum.Add(1)
}

func (u *UnavailableOfferings) Delete(instanceType string, region, capacityType string) {
	u.offeringCache.Delete(u.key(instanceType, region, capacityType))
}

func (u *UnavailableOfferings) Flush() {
	u.offeringCache.Flush()
	u.capacityTypeCache.Flush()
	u.regionCache.Flush()
}

// key returns the cache key for all offerings in the cache
func (u *UnavailableOfferings) key(instanceType string, region, capacityType string) string {
	return fmt.Sprintf("%s:%s:%s", capacityType, instanceType, region)
}
