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
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
)

type NodeClass interface {
	client.Object
	LinodeImages() []v1.LinodeImage
}

type Provider interface {
	Get(context.Context, NodeClass, string) (*cloudprovider.InstanceType, error)
	List(context.Context, NodeClass) ([]*cloudprovider.InstanceType, error)
}

type DefaultProvider struct {
	client                *linodego.Client
	instanceTypesResolver Resolver

	muInstanceTypesInfo sync.RWMutex
	instanceTypesInfo   map[string]linodego.LinodeType

	muInstanceTypesOfferings sync.RWMutex
	instanceTypesOfferings   map[string]sets.Set[string]
	// allZones                 sets.Set[string]

	instanceTypesCache      *cache.Cache
	discoveredCapacityCache *cache.Cache
}

func NewDefaultProvider(
	client *linodego.Client,
	instanceTypesResolver Resolver,

	instanceTypesCache *cache.Cache,
	discoveredCapacityCache *cache.Cache,
	// Note: I don't think we actually need a pricing provider like AWS for Linode it's right in the instance type info
	// offeringCache *cache.Cache,

	// unavailableOfferingsCache *linodecache.UnavailableOfferings,
) *DefaultProvider {
	return &DefaultProvider{
		client:                  client,
		instanceTypesInfo:       map[string]linodego.LinodeType{},
		instanceTypesOfferings:  map[string]sets.Set[string]{},
		instanceTypesResolver:   instanceTypesResolver,
		instanceTypesCache:      instanceTypesCache,
		discoveredCapacityCache: discoveredCapacityCache,
		/* offeringProvider: offering.NewDefaultProvider(
			unavailableOfferingsCache,
			offeringCache,
		), */
	}
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
	// The ID is something like "g6-standard-2", the label would be something like "Linode 4GB"
	it := p.instanceTypesResolver.Resolve(ctx, info, p.instanceTypesOfferings[info.ID].UnsortedList(), nodeClass)
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
	return p.instanceTypesResolver.CacheKey(nodeClass)
}

func (p *DefaultProvider) List(ctx context.Context, class NodeClass) ([]*cloudprovider.InstanceType, error) {
	//TODO implement me
	panic("implement me")
}

func (p *DefaultProvider) Reset() {
	p.instanceTypesInfo = map[string]linodego.LinodeType{}
	p.instanceTypesOfferings = map[string]sets.Set[string]{}
	p.instanceTypesCache.Flush()
	p.discoveredCapacityCache.Flush()
}

func (p *DefaultProvider) UpdateInstanceTypes(ctx context.Context) error {
	return nil
}

func (p *DefaultProvider) UpdateInstanceTypeOfferings(ctx context.Context) error {
	return nil
}

func discoveredCapacityCacheKey(instanceType string, nodeClass NodeClass) string {
	amiHash, _ := hashstructure.Hash(nodeClass.LinodeImages(), hashstructure.FormatV2, &hashstructure.HashOptions{SlicesAsSets: true})
	return fmt.Sprintf("%s-%016x", instanceType, amiHash)
}
