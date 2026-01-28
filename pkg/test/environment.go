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

package test

import (
	"context"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/samber/lo"
	clock "k8s.io/utils/clock/testing"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/karpenter/pkg/controllers/nodeoverlay"
	coretest "sigs.k8s.io/karpenter/pkg/test"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	linodecache "github.com/linode/karpenter-provider-linode/pkg/cache"
	"github.com/linode/karpenter-provider-linode/pkg/fake"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instance"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instancetype"
	"github.com/linode/karpenter-provider-linode/pkg/providers/lke"
)

func init() {
	coretest.SetDefaultNodeClassType(&v1.LinodeNodeClass{})
}

type Environment struct {
	// Mock
	Clock             *clock.FakeClock
	EventRecorder     *coretest.EventRecorder
	InstanceTypeStore *nodeoverlay.InstanceTypeStore

	// API
	LinodeAPI *fake.LinodeClient

	// Cache
	LinodeCache               *cache.Cache
	InstanceTypeCache         *cache.Cache
	InstanceCache             *cache.Cache
	OfferingCache             *cache.Cache
	NodePoolCache             *cache.Cache
	UnavailableOfferingsCache *linodecache.UnavailableOfferings
	ValidationCache           *cache.Cache

	// Providers
	InstanceTypesProvider *instancetype.DefaultProvider
	InstanceProvider      *instance.DefaultProvider
	InstanceTypesResolver *instancetype.DefaultResolver
	LKENodeProvider       *lke.DefaultProvider
}

// NodeProvider returns the appropriate provider based on the mode in context.
// For tests that need a specific provider, use InstanceProvider or LKENodeProvider directly.
func (env *Environment) NodeProvider(ctx context.Context) instance.Provider {
	if options.FromContext(ctx).Mode == "lke" {
		return env.LKENodeProvider
	}
	return env.InstanceProvider
}

func NewEnvironment(ctx context.Context) *Environment {
	// Mock
	clock := &clock.FakeClock{}
	store := nodeoverlay.NewInstanceTypeStore()

	linodeClient := fake.NewLinodeClient()

	// cache
	linodeCache := cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval)
	instanceTypeCache := cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval)
	instanceCache := cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval)
	nodePoolCache := cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval)
	discoveredCapacityCache := cache.New(linodecache.DiscoveredCapacityCacheTTL, linodecache.DefaultCleanupInterval)
	offeringCache := cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval)
	unavailableOfferingsCache := linodecache.NewUnavailableOfferings()
	validationCache := cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval)
	eventRecorder := coretest.NewEventRecorder()

	// Providers
	instanceTypesProvider := instancetype.NewDefaultProvider(
		linodeClient,
		instancetype.NewDefaultResolver(fake.DefaultRegion),
		instanceTypeCache,
		offeringCache,
		discoveredCapacityCache,
		unavailableOfferingsCache,
	)
	// Ensure we're able to hydrate instance types before starting any reliant controllers.
	// Instance type updates are hydrated asynchronously after this by controllers.
	lo.Must0(instanceTypesProvider.UpdateInstanceTypes(ctx))
	lo.Must0(instanceTypesProvider.UpdateInstanceTypeOfferings(ctx))

	instanceProvider := instance.NewDefaultProvider(
		fake.DefaultRegion,
		eventRecorder,
		linodeClient,
		unavailableOfferingsCache,
		instanceCache,
	)

	lkeNodeProvider := lke.NewDefaultProvider(
		fake.DefaultClusterID,
		fake.DefaultClusterTier,
		fake.DefaultRegion,
		eventRecorder,
		linodeClient,
		unavailableOfferingsCache,
		nodePoolCache,
	)

	return &Environment{
		Clock:             clock,
		EventRecorder:     eventRecorder,
		InstanceTypeStore: store,

		LinodeAPI: linodeClient,

		LinodeCache:               linodeCache,
		InstanceTypeCache:         instanceTypeCache,
		InstanceCache:             instanceCache,
		NodePoolCache:             nodePoolCache,
		OfferingCache:             offeringCache,
		UnavailableOfferingsCache: unavailableOfferingsCache,
		ValidationCache:           validationCache,

		InstanceTypesProvider: instanceTypesProvider,
		InstanceProvider:      instanceProvider,
		LKENodeProvider:       lkeNodeProvider,
	}
}

func (env *Environment) Reset() {
	env.Clock.SetTime(time.Time{})

	env.LinodeAPI.Reset()

	env.LinodeCache.Flush()
	env.InstanceCache.Flush()
	env.NodePoolCache.Flush()
	env.UnavailableOfferingsCache.Flush()
	env.OfferingCache.Flush()
	env.ValidationCache.Flush()

	env.InstanceTypesProvider.Reset()

	mfs, err := crmetrics.Registry.Gather()
	if err != nil {
		for _, mf := range mfs {
			for _, metric := range mf.GetMetric() {
				if metric != nil {
					metric.Reset()
				}
			}
		}
	}
}
