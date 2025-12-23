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

package offering

import (
	"context"
	"fmt"
	"sync"

	"github.com/linode/linodego"
	"github.com/mitchellh/hashstructure/v2"
	"github.com/patrickmn/go-cache"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
	linodecache "github.com/linode/karpenter-provider-linode/pkg/cache"
	sdk "github.com/linode/karpenter-provider-linode/pkg/linode"
)

type Provider interface {
	InjectOfferings(context.Context, []*cloudprovider.InstanceType, *v1.LinodeNodeClass, []string) []*cloudprovider.InstanceType
}

type NodeClass interface {
}

type DefaultProvider struct {
	client                         sdk.LinodeAPI
	unavailableOfferings           *linodecache.UnavailableOfferings
	lastUnavailableOfferingsSeqNum sync.Map // instance type -> seqNum
	cache                          *cache.Cache
}

func NewDefaultProvider(
	client sdk.LinodeAPI,
	unavailableOfferingsCache *linodecache.UnavailableOfferings,
	offeringCache *cache.Cache,
) *DefaultProvider {
	return &DefaultProvider{
		client:               client,
		unavailableOfferings: unavailableOfferingsCache,
		cache:                offeringCache,
	}
}

func (p *DefaultProvider) InjectOfferings(
	ctx context.Context,
	instanceTypes []*cloudprovider.InstanceType,
	nodeClass NodeClass,
	allRegions sets.Set[string],
) []*cloudprovider.InstanceType {
	var its []*cloudprovider.InstanceType
	for _, it := range instanceTypes {
		offerings := p.createOfferings(
			ctx,
			it,
			allRegions,
		)
		// NOTE: By making this copy one level deep, we can modify the offerings without mutating the results from previous
		// GetInstanceTypes calls. This should still be done with caution - it is currently done here in the provider, and
		// once in the instance provider (filterReservedInstanceTypes)
		its = append(its, &cloudprovider.InstanceType{
			Name:         it.Name,
			Requirements: it.Requirements,
			Offerings:    offerings,
			Capacity:     it.Capacity,
			Overhead:     it.Overhead,
		})
	}
	return its
}

//nolint:gocyclo
func (p *DefaultProvider) createOfferings(
	ctx context.Context,
	it *cloudprovider.InstanceType,
	allRegions sets.Set[string],
) cloudprovider.Offerings {
	var offerings []*cloudprovider.Offering
	itRegions := sets.New(it.Requirements.Get(corev1.LabelTopologyRegion).Values()...)

	// If the sequence number has changed for the unavailable offerings, we know that we can't use the previously cached value
	lastSeqNum, ok := p.lastUnavailableOfferingsSeqNum.Load(it.Name)
	if !ok {
		lastSeqNum = 0
	}
	seqNum := p.unavailableOfferings.SeqNum(it.Name)
	if ofs, ok := p.cache.Get(p.cacheKeyFromInstanceType(it)); ok && lastSeqNum == seqNum {
		offerings = append(offerings, ofs.([]*cloudprovider.Offering)...)
	} else {
		var cachedOfferings []*cloudprovider.Offering
		for region := range allRegions {
			isUnavailable := p.unavailableOfferings.IsUnavailable(it.Name, region)
			linodeType, err := p.getLinodeType(ctx, it.Name)
			if err != nil {
				continue
			}
			var price float64
			if linodeType != nil && linodeType.Price != nil {
				// I'm not sure why we have both hourly and monthly prices, but stick to hourly for now
				price = float64(linodeType.Price.Hourly)
			}
			offering := &cloudprovider.Offering{
				Requirements: scheduling.NewRequirements(
					scheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, region),
				),
				Price:     price,
				Available: !isUnavailable && itRegions.Has(region),
			}
			cachedOfferings = append(cachedOfferings, offering)
		}
		p.cache.SetDefault(p.cacheKeyFromInstanceType(it), cachedOfferings)
		p.lastUnavailableOfferingsSeqNum.Store(it.Name, seqNum)
		offerings = append(offerings, cachedOfferings...)
	}

	return offerings
}

func (p *DefaultProvider) getLinodeType(ctx context.Context, instanceTypeID string) (*linodego.LinodeType, error) {
	linodeType, err := p.client.GetType(ctx, instanceTypeID)
	if err != nil {
		return nil, fmt.Errorf("getting Linode type %s: %w", instanceTypeID, err)
	}
	return linodeType, nil
}

func (p *DefaultProvider) cacheKeyFromInstanceType(it *cloudprovider.InstanceType) string {
	regionsHash, _ := hashstructure.Hash(
		it.Requirements.Get(corev1.LabelTopologyRegion).Values(),
		hashstructure.FormatV2,
		&hashstructure.HashOptions{SlicesAsSets: true},
	)
	capacityTypesHash, _ := hashstructure.Hash(
		it.Requirements.Get(karpv1.CapacityTypeLabelKey).Values(),
		hashstructure.FormatV2,
		&hashstructure.HashOptions{SlicesAsSets: true},
	)
	return fmt.Sprintf(
		"%s-%016x-%016x",
		it.Name,
		regionsHash,
		capacityTypesHash,
	)
}
