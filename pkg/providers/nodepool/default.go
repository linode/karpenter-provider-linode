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

package nodepool

import (
	"context"
	"fmt"
	"strconv"

	"github.com/awslabs/operatorpkg/option"
	"github.com/patrickmn/go-cache"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"

	"github.com/linode/linodego"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
	linodecache "github.com/linode/karpenter-provider-linode/pkg/cache"
	sdk "github.com/linode/karpenter-provider-linode/pkg/linode"
	"github.com/linode/karpenter-provider-linode/pkg/utils"
)

// DefaultProvider implements Provider against LKE NodePool APIs.
type DefaultProvider struct {
	clusterID            int
	region               string
	recorder             events.Recorder
	client               sdk.LinodeAPI
	unavailableOfferings *linodecache.UnavailableOfferings
	nodeCache            *cache.Cache // instanceID -> *NodePoolInstance
}

func NewDefaultProvider(
	clusterID int,
	region string,
	recorder events.Recorder,
	client sdk.LinodeAPI,
	unavailableOfferings *linodecache.UnavailableOfferings,
	nodePoolCache *cache.Cache,
) *DefaultProvider {
	return &DefaultProvider{
		clusterID:            clusterID,
		region:               region,
		recorder:             recorder,
		client:               client,
		unavailableOfferings: unavailableOfferings,
		nodeCache:            nodePoolCache,
	}
}

// Create provisions a new LKE node pool with a single node and returns the first node as NodePoolInstance.
func (p *DefaultProvider) Create(ctx context.Context, nodeClass *v1.LinodeNodeClass, nodeClaim *karpv1.NodeClaim, tags map[string]string, instanceTypes []*cloudprovider.InstanceType) (*LKENodePool, error) {
	instanceTypes, err := utils.FilterInstanceTypes(ctx, instanceTypes, nodeClaim)
	if err != nil {
		return nil, err
	}
	cheapestType, err := utils.CheapestInstanceType(instanceTypes)
	if err != nil {
		return nil, err
	}
	nodeType := cheapestType.Name
	capacityType := karpv1.CapacityTypeOnDemand // we only support on-demand for now

	// Merge tags from NodeClass + NodeClaim tags map into the Linode tag format key:value.
	tagList := append([]string{}, nodeClass.Spec.Tags...)
	tagList = append(tagList, utils.MapToTagList(tags)...)
	tagList = utils.DedupeTags(tagList)

	taints := convertToLkeTaints(nodeClaim.Spec.Taints)

	createOpts := linodego.LKENodePoolCreateOptions{
		Count:  1, // we only create one node per nodepool
		Type:   nodeType,
		Tags:   tagList,
		Labels: nodeClass.Spec.Labels,
		Taints: taints,
	}
	if nodeClass.Spec.FirewallID != 0 {
		createOpts.FirewallID = ptr.To(nodeClass.Spec.FirewallID)
	}

	pool, err := p.client.CreateLKENodePool(ctx, p.clusterID, createOpts)
	utils.UpdateUnavailableOfferingsCache(
		ctx,
		err,
		capacityType,
		p.region,
		cheapestType,
		p.unavailableOfferings,
	)
	if err != nil {
		return nil, cloudprovider.NewCreateError(err, "NodePoolCreationFailed", "Failed to create LKE node pool")
	}

	// Use the first node for the NodeClaim
	if len(pool.Linodes) == 0 {
		return nil, fmt.Errorf("created node pool %d but no linodes returned", pool.ID)
	}
	node := pool.Linodes[0]
	instance := NewLKENodePool(pool, node, p.region)
	p.cacheNode(instance)
	return instance, nil
}

func (p *DefaultProvider) Get(ctx context.Context, providerID string, opts ...Options) (*LKENodePool, error) {
	options := option.Resolve(opts...)
	if !options.SkipCache {
		if cached, found := p.nodeCache.Get(providerID); found {
			return cached.(*LKENodePool), nil
		}
	}

	poolID, err := p.lookupPoolByInstance(ctx, providerID)
	if err != nil {
		return nil, err
	}
	pool, err := p.client.GetLKENodePool(ctx, p.clusterID, poolID)
	if err != nil {
		return nil, err
	}
	node, err := findNodeInPool(pool, providerID)
	if err != nil {
		return nil, err
	}
	inst := NewLKENodePool(pool, *node, p.region)
	p.cacheNode(inst)
	return inst, nil
}

func (p *DefaultProvider) List(ctx context.Context) ([]*LKENodePool, error) {
	pools, err := p.listLKEPools(ctx)
	if err != nil {
		return nil, err
	}
	var instances []*LKENodePool
	for i := range pools {
		pool := pools[i]
		for _, node := range pool.Linodes {
			inst := NewLKENodePool(&pool, node, p.region)
			instances = append(instances, inst)
			p.cacheNode(inst)
		}
	}
	return instances, nil
}

func (p *DefaultProvider) Delete(ctx context.Context, providerID string) error {
	poolID, err := p.lookupPoolByInstance(ctx, providerID)
	if err != nil {
		return err
	}
	if err := p.client.DeleteLKENodePool(ctx, p.clusterID, poolID); err != nil {
		return err
	}
	p.nodeCache.Delete(providerID)
	return nil
}

func (p *DefaultProvider) UpdateTags(ctx context.Context, poolID int, tags map[string]string) error {
	pool, err := p.client.GetLKENodePool(ctx, p.clusterID, poolID)
	if err != nil {
		return err
	}
	merged := utils.DedupeTags(append([]string{}, append(pool.Tags, utils.MapToTagList(tags)...)...))
	_, err = p.client.UpdateLKENodePool(ctx, p.clusterID, poolID, linodego.LKENodePoolUpdateOptions{Tags: &merged})
	return err
}

func convertToLkeTaints(taints []corev1.Taint) []linodego.LKENodePoolTaint {
	res := make([]linodego.LKENodePoolTaint, 0, len(taints))
	for _, t := range taints {
		res = append(res, linodego.LKENodePoolTaint{
			Effect: linodego.LKENodePoolTaintEffect(t.Effect),
			Key:    t.Key,
			Value:  t.Value,
		})
	}
	return res
}

func (p *DefaultProvider) cacheNode(n *LKENodePool) {
	providerID := fmt.Sprintf("linode://%d", n.InstanceID)
	p.nodeCache.SetDefault(providerID, n)
}

func (p *DefaultProvider) lookupPoolByInstance(ctx context.Context, providerID string) (int, error) {
	if cached, found := p.nodeCache.Get(providerID); found {
		return cached.(*LKENodePool).PoolID, nil
	}
	instanceIDStr, err := utils.ParseInstanceID(providerID)
	if err != nil {
		return 0, fmt.Errorf("getting instance ID, %w", err)
	}
	instanceID, err := strconv.Atoi(instanceIDStr)
	if err != nil {
		return 0, fmt.Errorf("parsing instance ID, %w", err)
	}
	pools, err := p.listLKEPools(ctx)
	if err != nil {
		return 0, err
	}
	for _, pool := range pools {
		for _, node := range pool.Linodes {
			if node.InstanceID == instanceID {
				return pool.ID, nil
			}
		}
	}
	return 0, fmt.Errorf("instance %d not found in any pool", instanceID)
}

func findNodeInPool(pool *linodego.LKENodePool, providerID string) (*linodego.LKENodePoolLinode, error) {
	instanceID, err := utils.ParseInstanceID(providerID)
	if err != nil {
		return nil, err
	}
	intID, err := strconv.Atoi(instanceID)
	if err != nil {
		return nil, err
	}
	for _, node := range pool.Linodes {
		if node.InstanceID == intID {
			return &node, nil
		}
	}
	return nil, fmt.Errorf("instance %d not found in pool %d", intID, pool.ID)
}

func (p *DefaultProvider) listLKEPools(ctx context.Context) ([]linodego.LKENodePool, error) {
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
	return p.client.ListLKENodePools(ctx, p.clusterID, linodego.NewListOptions(1, filter))
}
