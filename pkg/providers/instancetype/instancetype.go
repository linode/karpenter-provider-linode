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

package instancetype

import (
	"context"
	"fmt"
	"sync"

	"github.com/linode/linodego"
	"github.com/mitchellh/hashstructure/v2"
	"github.com/patrickmn/go-cache"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/utils/pretty"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
	linodecache "github.com/linode/karpenter-provider-linode/pkg/cache"
	sdk "github.com/linode/karpenter-provider-linode/pkg/linode"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instancetype/offering"
	"github.com/linode/karpenter-provider-linode/pkg/utils"
)

type NodeClass interface {
	client.Object
	KubeletConfiguration() *v1.KubeletConfiguration
	LinodeImages() []v1.LinodeImage
}

type Provider interface {
	Get(context.Context, NodeClass, string) (*cloudprovider.InstanceType, error)
	List(context.Context, NodeClass) ([]*cloudprovider.InstanceType, error)
}

type DefaultProvider struct {
	client                sdk.LinodeAPI
	instanceTypesResolver Resolver

	muInstanceTypesInfo sync.RWMutex
	instanceTypesInfo   map[string]linodego.LinodeType

	muInstanceTypesOfferings sync.RWMutex
	instanceTypesOfferings   map[string]sets.Set[string]
	allRegions               sets.Set[string]

	instanceTypesCache      *cache.Cache
	discoveredCapacityCache *cache.Cache
	cm                      *pretty.ChangeMonitor

	offeringProvider *offering.DefaultProvider
}

func NewDefaultProvider(
	client sdk.LinodeAPI,
	instanceTypesResolver Resolver,
	instanceTypesCache *cache.Cache,
	offeringCache *cache.Cache,
	discoveredCapacityCache *cache.Cache,
	unavailableOfferingsCache *linodecache.UnavailableOfferings,
	// TODO: add pricing provider here
) *DefaultProvider {
	return &DefaultProvider{
		client:                  client,
		instanceTypesInfo:       map[string]linodego.LinodeType{},
		instanceTypesOfferings:  map[string]sets.Set[string]{},
		instanceTypesResolver:   instanceTypesResolver,
		instanceTypesCache:      instanceTypesCache,
		discoveredCapacityCache: discoveredCapacityCache,
		cm:                      pretty.NewChangeMonitor(),
		offeringProvider: offering.NewDefaultProvider(
			client,
			unavailableOfferingsCache,
			offeringCache,
		),
	}
}

func (p *DefaultProvider) List(ctx context.Context, nodeClass NodeClass) ([]*cloudprovider.InstanceType, error) {
	p.muInstanceTypesInfo.RLock()
	p.muInstanceTypesOfferings.RLock()
	defer p.muInstanceTypesInfo.RUnlock()
	defer p.muInstanceTypesOfferings.RUnlock()

	if len(p.instanceTypesInfo) == 0 {
		return nil, fmt.Errorf("no instance types found")
	}
	if len(p.instanceTypesOfferings) == 0 {
		return nil, fmt.Errorf("no instance types offerings found")
	}

	key := p.cacheKey(nodeClass)
	var instanceTypes []*cloudprovider.InstanceType
	if item, ok := p.instanceTypesCache.Get(key); ok {
		// Ensure what's returned from this function is a shallow-copy of the slice (not a deep-copy of the data itself)
		// so that modifications to the ordering of the data don't affect the original
		instanceTypes = item.([]*cloudprovider.InstanceType)
	} else {
		instanceTypes = lo.FilterMapToSlice(p.instanceTypesInfo, func(name string, info linodego.LinodeType) (*cloudprovider.InstanceType, bool) {
			it, err := p.get(ctx, nodeClass, name)
			if err != nil {
				return nil, false
			}
			return it, true
		})
		p.instanceTypesCache.SetDefault(key, instanceTypes)
	}
	// Offerings aren't cached along with the rest of the instance type info because reserved offerings need to have up to
	// date capacity information. Rather than incurring a cache miss each time an instance is launched into a reserved
	// offering (or terminated), offerings are injected to the cached instance types on each call.
	return p.offeringProvider.InjectOfferings(
		ctx,
		instanceTypes,
		nodeClass,
		p.allRegions,
	), nil
}

func (p *DefaultProvider) Get(ctx context.Context, nodeClass NodeClass, name string) (*cloudprovider.InstanceType, error) {
	p.muInstanceTypesInfo.RLock()
	p.muInstanceTypesOfferings.RLock()
	defer p.muInstanceTypesInfo.RUnlock()
	defer p.muInstanceTypesOfferings.RUnlock()

	if len(p.instanceTypesInfo) == 0 {
		return nil, fmt.Errorf("no instance types found")
	}
	if len(p.instanceTypesOfferings) == 0 {
		return nil, fmt.Errorf("no instance types offerings found")
	}

	var instanceType *cloudprovider.InstanceType
	if item, ok := p.instanceTypesCache.Get(p.cacheKey(nodeClass)); ok {
		instanceType, _ = lo.Find(item.([]*cloudprovider.InstanceType), func(i *cloudprovider.InstanceType) bool {
			return i.Name == name
		})
	}
	if instanceType == nil {
		var err error
		instanceType, err = p.get(ctx, nodeClass, name)
		if err != nil {
			return nil, err
		}
	}
	return instanceType, nil
}

func (p *DefaultProvider) get(ctx context.Context, nodeClass NodeClass, name string) (*cloudprovider.InstanceType, error) {
	info, ok := p.instanceTypesInfo[name]
	if !ok {
		return nil, fmt.Errorf("instance type %s not found in cache", name)
	}
	it := p.instanceTypesResolver.Resolve(ctx, info, nodeClass)
	if it == nil {
		return nil, fmt.Errorf("failed to generate instance type %s", name)
	}
	if cached, ok := p.discoveredCapacityCache.Get(discoveredCapacityCacheKey(it.Name, nodeClass)); ok {
		it.Capacity[corev1.ResourceMemory] = cached.(resource.Quantity)
	}
	InstanceTypeVCPU.Set(float64(info.VCPUs), map[string]string{
		instanceTypeLabel: info.ID,
	})
	InstanceTypeMemory.Set(float64(info.Memory*1024*1024), map[string]string{
		instanceTypeLabel: info.ID,
	})
	return it, nil
}

func (p *DefaultProvider) cacheKey(nodeClass NodeClass) string {
	// Compute hash key against node class Images (used to force cache rebuild when Images change)
	imageHash, _ := hashstructure.Hash(nodeClass.LinodeImages(), hashstructure.FormatV2, &hashstructure.HashOptions{SlicesAsSets: true})
	return fmt.Sprintf("%016x-%s",
		imageHash,
		p.instanceTypesResolver.CacheKey(nodeClass),
	)
}

func (p *DefaultProvider) UpdateInstanceTypes(ctx context.Context) error {
	// DO NOT REMOVE THIS LOCK ----------------------------------------------------------------------------
	// We lock here so that multiple callers to getInstanceTypeOfferings do not result in cache misses and multiple
	// calls to Linode API when we could have just made one call.
	p.muInstanceTypesInfo.Lock()
	defer p.muInstanceTypesInfo.Unlock()

	instanceTypes, err := p.client.ListTypes(ctx, &linodego.ListOptions{}) // TODO: pagination / filter by region (do we expect to support multiple regions?)
	if err != nil {
		return fmt.Errorf("listing linode instance types, %w", err)
	}

	if p.cm.HasChanged("instance-types", instanceTypes) {
		// Only update instanceTypesSeqNun with the instance types have been changed
		// This is to not create new keys with duplicate instance types option
		p.instanceTypesCache.Flush() // None of the cached instance type info is valid when the instance type info changes
		log.FromContext(ctx).WithValues("count", len(instanceTypes)).V(1).Info("discovered instance types")
	}
	p.instanceTypesInfo = lo.SliceToMap(instanceTypes, func(i linodego.LinodeType) (string, linodego.LinodeType) {
		return i.ID, i
	})

	return nil
}

func (p *DefaultProvider) UpdateInstanceTypeOfferings(ctx context.Context) error {
	// DO NOT REMOVE THIS LOCK ----------------------------------------------------------------------------
	// We lock here so that multiple callers to GetInstanceTypes do not result in cache misses and multiple
	// calls to Linode API when we could have just made one call. This lock is here because multiple callers to Linode API result
	// in A LOT of extra memory generated from the response for simultaneous callers.
	p.muInstanceTypesOfferings.Lock()
	defer p.muInstanceTypesOfferings.Unlock()

	// Get offerings from Linode API
	instanceTypeOfferings := map[string]sets.Set[string]{}

	listFilter := utils.Filter{
		AdditionalFilters: map[string]string{}, // TODO: filter by region (do we expect to support multiple regions?)
	}
	filter, err := listFilter.String()
	if err != nil {
		return err
	}

	regionAvail, err := p.client.ListRegionsAvailability(ctx, &linodego.ListOptions{
		Filter:   filter,
		PageSize: 1000, //TODO: pagination
	})
	if err != nil {
		return fmt.Errorf("listing region availability %w", err)
	}

	for _, offering := range regionAvail {
		if _, ok := instanceTypeOfferings[offering.Plan]; !ok {
			instanceTypeOfferings[offering.Plan] = sets.New[string]()
		}
		if offering.Available {
			instanceTypeOfferings[offering.Plan].Insert(offering.Region)
		}
	}

	if p.cm.HasChanged("instance-type-offering", instanceTypeOfferings) {
		// Only update instanceTypesSeqNun with the instance type offerings have been changed
		// This is to not create new keys with duplicate instance type offerings option
		p.instanceTypesCache.Flush() // None of the cached instance type info is valid when the instance type offerings info changes
		log.FromContext(ctx).WithValues("instance-type-count", len(instanceTypeOfferings)).V(1).Info("discovered offerings for instance types")
	}
	p.instanceTypesOfferings = instanceTypeOfferings

	allRegions := sets.New[string]()
	for _, offeringRegions := range instanceTypeOfferings {
		for region := range offeringRegions {
			allRegions.Insert(region)
		}
	}
	if p.cm.HasChanged("Regions", allRegions) {
		log.FromContext(ctx).WithValues("Regions", allRegions.UnsortedList()).V(1).Info("discovered Regions")
	}
	p.allRegions = allRegions
	return nil
}

func (p *DefaultProvider) UpdateInstanceTypeCapacityFromNode(ctx context.Context, node *corev1.Node, nodeClass NodeClass) error {
	instanceTypeName := node.Labels[corev1.LabelInstanceTypeStable]

	key := discoveredCapacityCacheKey(instanceTypeName, nodeClass)
	actualCapacity := node.Status.Capacity.Memory()
	if cachedCapacity, ok := p.discoveredCapacityCache.Get(key); !ok || actualCapacity.Cmp(cachedCapacity.(resource.Quantity)) < 1 {
		// Update the capacity in the cache if it is less than or equal to the current cached capacity. We update when it's equal to refresh the TTL.
		p.discoveredCapacityCache.SetDefault(key, *actualCapacity)
		// Only log if we haven't discovered the capacity for the instance type yet or the discovered capacity is **less** than the cached capacity
		if !ok || actualCapacity.Cmp(cachedCapacity.(resource.Quantity)) < 0 {
			log.FromContext(ctx).WithValues("memory-capacity", actualCapacity, "instance-type", instanceTypeName).V(1).Info("updating discovered capacity cache")
		}
	}
	return nil
}

func (p *DefaultProvider) Reset() {
	p.instanceTypesInfo = map[string]linodego.LinodeType{}
	p.instanceTypesOfferings = map[string]sets.Set[string]{}
	p.instanceTypesCache.Flush()
	p.discoveredCapacityCache.Flush()
}

func discoveredCapacityCacheKey(instanceType string, nodeClass NodeClass) string {
	imageHash, _ := hashstructure.Hash(nodeClass.LinodeImages(), hashstructure.FormatV2, &hashstructure.HashOptions{SlicesAsSets: true})
	return fmt.Sprintf("%s-%016x", instanceType, imageHash)
}
