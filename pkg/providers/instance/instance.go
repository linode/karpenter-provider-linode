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

package instance

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/awslabs/operatorpkg/option"
	"github.com/google/uuid"
	"github.com/linode/linodego"
	"github.com/patrickmn/go-cache"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
	linodecache "github.com/linode/karpenter-provider-linode/pkg/cache"
	sdk "github.com/linode/karpenter-provider-linode/pkg/linode"
	"github.com/linode/karpenter-provider-linode/pkg/utils"
)

var SkipCache = func(opts *options) {
	opts.SkipCache = true
}
var defaultImage = "linode/ubuntu22.04"

type Provider interface {
	Create(context.Context, *v1.LinodeNodeClass, *karpv1.NodeClaim, map[string]string, []*cloudprovider.InstanceType) (*Instance, error)
	Get(context.Context, string, ...Options) (*Instance, error)
	List(context.Context) ([]*Instance, error)
	Delete(context.Context, string) error
	CreateTags(context.Context, string, map[string]string) error
}

type options struct {
	SkipCache bool
}

type Options = option.Function[options]

type DefaultProvider struct {
	region               string
	recorder             events.Recorder
	client               sdk.LinodeAPI
	unavailableOfferings *linodecache.UnavailableOfferings
	instanceCache        *cache.Cache
}

func NewDefaultProvider(
	region string,
	recorder events.Recorder,
	client sdk.LinodeAPI,
	unavailableOfferings *linodecache.UnavailableOfferings,
	instanceCache *cache.Cache,
) *DefaultProvider {

	return &DefaultProvider{
		region:               region,
		recorder:             recorder,
		client:               client,
		unavailableOfferings: unavailableOfferings,
		instanceCache:        instanceCache,
	}
}

func (p *DefaultProvider) Create(ctx context.Context, nodeClass *v1.LinodeNodeClass, nodeClaim *karpv1.NodeClaim, tags map[string]string, instanceTypes []*cloudprovider.InstanceType) (*Instance, error) {
	instanceTypes, err := utils.FilterInstanceTypes(ctx, instanceTypes, nodeClaim)
	if err != nil {
		return nil, err
	}
	cheapestType, err := utils.CheapestInstanceType(instanceTypes)
	if err != nil {
		return nil, err
	}
	capacityType := getCapacityType(nodeClaim, instanceTypes)
	// Merge tags from NodeClaim and LinodeNodeClass
	tagList := nodeClass.Spec.Tags
	for k, v := range tags {
		tagList = append(tagList, fmt.Sprintf("%s:%s", k, v))
	}

	if nodeClass.Spec.Image == "" {
		nodeClass.Spec.Image = defaultImage
	}

	createOpts := linodego.InstanceCreateOptions{
		Label:           fmt.Sprintf("%s-%s", nodeClaim.Name, uuid.NewString()[0:8]),
		Region:          p.region,
		Type:            cheapestType.Name,
		RootPass:        uuid.NewString(),
		AuthorizedKeys:  nodeClass.Spec.AuthorizedKeys,
		AuthorizedUsers: nodeClass.Spec.AuthorizedUsers,
		Image:           nodeClass.Spec.Image,
		BackupsEnabled:  nodeClass.Spec.BackupsEnabled,
		Tags:            utils.DedupeTags(tagList),
		// NOTE: Disk encryption may not currently be available to all users.
		DiskEncryption: nodeClass.Spec.DiskEncryption,
		SwapSize:       nodeClass.Spec.SwapSize,
		// TODO: maybe add LinodeInterface options here in the future for custom networking setups
	}

	if nodeClass.Spec.FirewallID != nil {
		createOpts.FirewallID = *nodeClass.Spec.FirewallID
	}

	if nodeClass.Spec.PlacementGroup != nil {
		createOpts.PlacementGroup = &linodego.InstanceCreatePlacementGroupOptions{
			ID:            nodeClass.Spec.PlacementGroup.ID,
			CompliantOnly: nodeClass.Spec.PlacementGroup.CompliantOnly,
		}
	}

	instance, err := p.client.CreateInstance(ctx, createOpts)
	// Update the offerings cache based on the error returned from the CreateInstance call.
	utils.UpdateUnavailableOfferingsCache(
		ctx,
		err,
		capacityType,
		p.region,
		cheapestType,
		p.unavailableOfferings,
	)
	if err != nil {
		return nil, cloudprovider.NewCreateError(err, "InstanceCreationFailed", "Failed to create Linode instance")
	}

	return NewInstance(ctx, *instance), nil
}

// getCapacityType selects the capacity type based on the flexibility of the NodeClaim and the available offerings.
// Only on-demand is currently supported for Linode, so this will always return "on-demand".
func getCapacityType(_ *karpv1.NodeClaim, _ []*cloudprovider.InstanceType) string {
	return karpv1.CapacityTypeOnDemand
}

func (p *DefaultProvider) Get(ctx context.Context, id string, opts ...Options) (*Instance, error) {
	skipCache := option.Resolve(opts...).SkipCache
	if !skipCache {
		if i, ok := p.instanceCache.Get(id); ok {
			return i.(*Instance), nil
		}
	}

	intID, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("invalid instance id %s, %w", id, err)
	}

	instance, err := p.client.GetInstance(ctx, intID)
	if linodego.IsNotFound(err) {
		p.instanceCache.Delete(id)
		return nil, cloudprovider.NewNodeClaimNotFoundError(err)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get linode instance, %w", err)
	}
	p.instanceCache.SetDefault(id, instance)

	return NewInstance(ctx, *instance), nil
}

func (p *DefaultProvider) List(ctx context.Context) ([]*Instance, error) {
	listFilter := utils.Filter{
		Tags: []string{
			karpv1.NodePoolLabelKey,
			apis.Group + "/linodenodeclass",
		},
	}
	filter, err := listFilter.String()
	if err != nil {
		return nil, err
	}
	instances, err := p.client.ListInstances(ctx, linodego.NewListOptions(1, filter))
	if err != nil {
		return nil, fmt.Errorf("failed to list linode instances, %w", err)
	}

	res := make([]*Instance, 0, len(instances))
	for _, it := range instances {
		p.instanceCache.SetDefault(strconv.Itoa(it.ID), it)
		res = append(res, NewInstance(ctx, it))
	}

	return res, cloudprovider.IgnoreNodeClaimNotFoundError(err)
}

func (p *DefaultProvider) Delete(ctx context.Context, id string) error {
	out, err := p.Get(ctx, id, SkipCache)
	if err != nil {
		return err
	}
	// Check if the instance is already shutting-down to reduce the number of
	// terminate-instance calls we make thereby reducing our overall QPS.
	if out.Status != linodego.InstanceDeleting {
		intID, err := strconv.Atoi(id)
		if err != nil {
			return fmt.Errorf("invalid instance id %s, %w", id, err)
		}

		if err := p.client.DeleteInstance(ctx, intID); err != nil {
			return err
		}
	}
	return nil
}

// NOTE: Linode's API only supports creating tags one at a time. This might be a problem if we want to add multiple tags at once.
func (p *DefaultProvider) CreateTags(ctx context.Context, id string, tags map[string]string) error {
	intId, err := strconv.Atoi(id)
	if err != nil {
		return fmt.Errorf("invalid instance id %s, %w", id, err)
	}
	for k, v := range tags {
		// Ensures that no more than 1 CreateTag call is made per second.
		time.Sleep(time.Second)
		if _, err := p.client.CreateTag(ctx, linodego.TagCreateOptions{
			Linodes: []int{intId},
			Label:   fmt.Sprintf("%s:%s", k, v),
		}); err != nil {
			if linodego.IsNotFound(err) {
				return cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("tagging instance, %w", err))
			}
			return fmt.Errorf("tagging instance, %w", err)
		}
	}

	return nil
}
