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
	clock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/karpenter/pkg/controllers/nodeoverlay"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
	awscache "github.com/linode/karpenter-provider-linode/pkg/cache"
	"github.com/linode/karpenter-provider-linode/pkg/fake"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instance"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instancetype"

	coretest "sigs.k8s.io/karpenter/pkg/test"

	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
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
	LinodeCache                   *cache.Cache
	InstanceTypeCache             *cache.Cache
	InstanceCache                 *cache.Cache
	LaunchTemplateCache           *cache.Cache
	AvailableIPAdressCache        *cache.Cache
	AssociatePublicIPAddressCache *cache.Cache
	RecreationCache               *cache.Cache
	ProtectedProfilesCache        *cache.Cache

	// Providers
	InstanceTypesProvider *instancetype.DefaultProvider
	InstanceProvider      *instance.DefaultProvider
}

func NewEnvironment(ctx context.Context, env *coretest.Environment) *Environment {
	// Mock
	clock := &clock.FakeClock{}
	store := nodeoverlay.NewInstanceTypeStore()

	// API
	linodeAPI := fake.NewLinodeAPI()

	// cache
	LinodeCache := cache.New(awscache.DefaultTTL, awscache.DefaultCleanupInterval)
	instanceTypeCache := cache.New(awscache.DefaultTTL, awscache.DefaultCleanupInterval)
	instanceCache := cache.New(awscache.DefaultTTL, awscache.DefaultCleanupInterval)
	launchTemplateCache := cache.New(awscache.DefaultTTL, awscache.DefaultCleanupInterval)
	availableIPAdressCache := cache.New(awscache.AvailableIPAddressTTL, awscache.DefaultCleanupInterval)
	associatePublicIPAddressCache := cache.New(awscache.AssociatePublicIPAddressTTL, awscache.DefaultCleanupInterval)
	protectedProfilesCache := cache.New(awscache.DefaultTTL, awscache.DefaultCleanupInterval)
	recreationCache := cache.New(awscache.DefaultTTL, awscache.DefaultCleanupInterval)
	eventRecorder := coretest.NewEventRecorder()

	// Providers
	instanceTypesProvider := instancetype.NewDefaultProvider(
		"",
		eventRecorder,
		linodeAPI.Client,
		instanceTypeCache,
	)
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

		LinodeCache:       LinodeCache,
		InstanceTypeCache: instanceTypeCache,
		InstanceCache:     instanceCache,

		LaunchTemplateCache:           launchTemplateCache,
		AvailableIPAdressCache:        availableIPAdressCache,
		AssociatePublicIPAddressCache: associatePublicIPAddressCache,
		RecreationCache:               recreationCache,
		ProtectedProfilesCache:        protectedProfilesCache,

		InstanceTypesProvider: instanceTypesProvider,
		InstanceProvider:      instanceProvider,
	}
}

func (env *Environment) Reset() {
	env.Clock.SetTime(time.Time{})
	env.LinodeAPI.Reset()

	env.LinodeCache.Flush()
	env.InstanceCache.Flush()
	env.LaunchTemplateCache.Flush()
	env.AssociatePublicIPAddressCache.Flush()
	env.AvailableIPAdressCache.Flush()
	env.RecreationCache.Flush()
	env.ProtectedProfilesCache.Flush()
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
