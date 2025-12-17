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

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
	linodecache "github.com/linode/karpenter-provider-linode/pkg/cache"
	"github.com/linode/karpenter-provider-linode/pkg/fake"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instance"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instancetype"
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
	LinodeAPI *fake.LinodeAPI

	// Cache
	LinodeCache       *cache.Cache
	InstanceTypeCache *cache.Cache
	InstanceCache     *cache.Cache

	// Providers
	InstanceTypesProvider *instancetype.DefaultProvider
	InstanceProvider      *instance.DefaultProvider
	InstanceTypesResolver *instancetype.DefaultResolver
}

func NewEnvironment(ctx context.Context, env *coretest.Environment) *Environment {
	// Mock
	clock := &clock.FakeClock{}
	store := nodeoverlay.NewInstanceTypeStore()

	// API
	linodeAPI := fake.NewLinodeAPI()

	// cache
	linodeCache := cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval)
	instanceTypeCache := cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval)
	instanceCache := cache.New(linodecache.DefaultTTL, linodecache.DefaultCleanupInterval)
	discoveredCapacityCache := cache.New(linodecache.DiscoveredCapacityCacheTTL, linodecache.DefaultCleanupInterval)
	eventRecorder := coretest.NewEventRecorder()

	// Providers
	instanceTypesProvider := instancetype.NewDefaultProvider(
		linodeAPI.Client,
		instancetype.NewDefaultResolver(fake.DefaultRegion),
		instanceTypeCache,
		discoveredCapacityCache,
	)
	// Ensure we're able to hydrate instance types before starting any reliant controllers.
	// Instance type updates are hydrated asynchronously after this by controllers.
	lo.Must0(instanceTypesProvider.UpdateInstanceTypes(ctx))
	lo.Must0(instanceTypesProvider.UpdateInstanceTypeOfferings(ctx))

	instanceProvider := instance.NewDefaultProvider(
		"",
		eventRecorder,
		linodeAPI.Client,
		instanceCache,
	)

	return &Environment{
		Clock:             clock,
		EventRecorder:     eventRecorder,
		InstanceTypeStore: store,

		LinodeAPI: linodeAPI,

		LinodeCache:       linodeCache,
		InstanceTypeCache: instanceTypeCache,
		InstanceCache:     instanceCache,

		InstanceTypesProvider: instanceTypesProvider,
		InstanceProvider:      instanceProvider,
	}
}

func (env *Environment) Reset() {
	env.Clock.SetTime(time.Time{})
	env.LinodeAPI.Reset()

	env.LinodeCache.Flush()
	env.InstanceCache.Flush()
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
