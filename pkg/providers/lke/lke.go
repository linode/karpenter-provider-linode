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

package lke

import (
	"context"
	"fmt"
	"strconv"

	"github.com/awslabs/operatorpkg/option"
	"github.com/patrickmn/go-cache"
	corev1 "k8s.io/api/core/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"

	"github.com/linode/linodego"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
	linodecache "github.com/linode/karpenter-provider-linode/pkg/cache"
	sdk "github.com/linode/karpenter-provider-linode/pkg/linode"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instance"
	"github.com/linode/karpenter-provider-linode/pkg/utils"
)

// DefaultProvider implements Provider against LKE NodePool APIs.
type DefaultProvider struct {
	clusterID            int
	clusterTier          linodego.LKEVersionTier
	region               string
	recorder             events.Recorder
	client               sdk.LinodeAPI
	unavailableOfferings *linodecache.UnavailableOfferings
	nodeCache            *cache.Cache // instanceID -> *NodePoolInstance
}

func NewDefaultProvider(
	clusterID int,
	clusterTier linodego.LKEVersionTier,
	region string,
	recorder events.Recorder,
	client sdk.LinodeAPI,
	unavailableOfferings *linodecache.UnavailableOfferings,
	nodePoolCache *cache.Cache,
) *DefaultProvider {
	return &DefaultProvider{
		clusterID:            clusterID,
		clusterTier:          clusterTier,
		region:               region,
		recorder:             recorder,
		client:               client,
		unavailableOfferings: unavailableOfferings,
		nodeCache:            nodePoolCache,
	}
}

// Create provisions a new LKE node pool with a single node and returns the first node as Instance.
func (p *DefaultProvider) Create(ctx context.Context, nodeClass *v1.LinodeNodeClass, nodeClaim *karpv1.NodeClaim, tags map[string]string, instanceTypes []*cloudprovider.InstanceType) (*instance.Instance, error) {
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

	// Check if a node pool already exists for this nodeclaim
	if pool, err := p.findPoolByNodeClaimTag(ctx, nodeClaim.Name); err != nil {
		return nil, fmt.Errorf("finding existing node pool for nodeclaim %s, %w", nodeClaim.Name, err)
	} else if pool != nil {
		// Pool was found. We don't need to create a new one.
		// We can not try to get instance info and hydrate nodeclaim.
		// This adds idempotency to the Create operation so Karpenter nodeclaim controller
		// doesn't end up with multiple nodepools for the same nodeclaim.
		return p.instanceFromPool(ctx, pool, nodeClaim.Name)
	}

	// If no node pool was found, we need to create a new one.

	// Merge both regular taints and startup taints
	// TODO: we should introduce a new linode api field for startup taints
	allTaints := append(nodeClaim.Spec.Taints, nodeClaim.Spec.StartupTaints...)
	taints := convertToLkeTaints(allTaints)

	createOpts := linodego.LKENodePoolCreateOptions{
		Count:  1, // we only create one node per nodepool
		Type:   nodeType,
		Tags:   tagList,
		Labels: nodeClass.Spec.Labels,
		Taints: taints,
	}
	if nodeClass.Spec.FirewallID != nil {
		createOpts.FirewallID = nodeClass.Spec.FirewallID
	}
	if nodeClass.Spec.LKEK8sVersion != nil {
		createOpts.K8sVersion = nodeClass.Spec.LKEK8sVersion
	}
	if nodeClass.Spec.LKEUpdateStrategy != nil {
		createOpts.UpdateStrategy = nodeClass.Spec.LKEUpdateStrategy
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
	return p.instanceFromPool(ctx, pool, nodeClaim.Name)
}

func (p *DefaultProvider) instanceFromPool(ctx context.Context, pool *linodego.LKENodePool, nodeClaimName string) (*instance.Instance, error) {
	if p.clusterTier == linodego.LKEVersionEnterprise {
		return p.instanceFromPoolEnterprise(ctx, pool, nodeClaimName)
	}
	return p.instanceFromPoolStandard(ctx, pool)
}

func (p *DefaultProvider) instanceFromPoolStandard(ctx context.Context, pool *linodego.LKENodePool) (*instance.Instance, error) {
	// InstanceID is populated shortly after pool creation; return CreateError to requeue until ready.
	freshPool, err := p.client.GetLKENodePool(ctx, p.clusterID, pool.ID)
	if err != nil {
		if utils.IsRetryableError(err) {
			return nil, cloudprovider.NewCreateError(fmt.Errorf("waiting for instance ID on pool %d", pool.ID), "NodePoolProvisioning", "Waiting for LKE instance to be ready")
		}
		return nil, fmt.Errorf("getting node pool %d, %w", pool.ID, err)
	}
	if len(freshPool.Linodes) == 0 || freshPool.Linodes[0].InstanceID == 0 {
		return nil, cloudprovider.NewCreateError(fmt.Errorf("waiting for instance ID on pool %d", pool.ID), "NodePoolProvisioning", "Waiting for LKE instance to be ready")
	}
	inst := instance.NewLKEInstance(freshPool, freshPool.Linodes[0], p.region)
	p.cacheNode(inst)
	return inst, nil
}

func (p *DefaultProvider) instanceFromPoolEnterprise(ctx context.Context, pool *linodego.LKENodePool, nodeClaimName string) (*instance.Instance, error) {
	nodeClaimTag := fmt.Sprintf("%s:%s", v1.NodeClaimTagKey, nodeClaimName)
	linodeInstance, err := utils.LookupInstanceByTag(ctx, p.client, nodeClaimTag)
	if err != nil {
		return nil, err
	}
	if linodeInstance == nil {
		return nil, cloudprovider.NewCreateError(fmt.Errorf("waiting for instance tagged %q", nodeClaimTag), "NodePoolProvisioning", "Waiting for LKE instance to be tagged")
	}
	inst := instance.NewLKEInstance(pool, linodego.LKENodePoolLinode{InstanceID: linodeInstance.ID}, p.region)
	p.cacheNode(inst)
	return inst, nil
}

func (p *DefaultProvider) Get(ctx context.Context, id string, opts ...instance.Options) (*instance.Instance, error) {
	options := option.Resolve(opts...)
	if !options.SkipCache {
		if cached, found := p.nodeCache.Get(id); found {
			return cached.(*instance.Instance), nil
		}
	}

	poolID, err := p.lookupPoolByInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	pool, err := p.client.GetLKENodePool(ctx, p.clusterID, poolID)
	if err != nil {
		return nil, err
	}
	node, err := findNodeInPool(pool, id)
	if err != nil {
		return nil, err
	}
	inst := instance.NewLKEInstance(pool, *node, p.region)
	p.cacheNode(inst)
	return inst, nil
}

func (p *DefaultProvider) List(ctx context.Context) ([]*instance.Instance, error) {
	pools, err := p.client.ListLKENodePools(ctx, p.clusterID, nil)
	if err != nil {
		return nil, err
	}
	var instances []*instance.Instance
	for i := range pools {
		pool := pools[i]
		// checking if api filtering is not working but manually filtering here
		if !isKarpenterManagedPool(&pool) {
			continue
		}
		for _, node := range pool.Linodes {
			// Skip nodes that don't have an InstanceID yet (still provisioning)
			if node.InstanceID == 0 {
				continue
			}
			inst := instance.NewLKEInstance(&pool, node, p.region)
			instances = append(instances, inst)
			p.cacheNode(inst)
		}
	}
	return instances, nil
}

func isKarpenterManagedPool(pool *linodego.LKENodePool) bool {
	tags := utils.TagListToMap(pool.Tags)
	_, hasNodePoolLabel := tags[karpv1.NodePoolLabelKey]
	_, hasLKEManagedLabel := tags[v1.LabelLKEManaged]
	return hasNodePoolLabel && hasLKEManagedLabel
}

func (p *DefaultProvider) Delete(ctx context.Context, id string) error {
	poolID, err := p.lookupPoolByInstance(ctx, id)
	if err != nil {
		return cloudprovider.NewNodeClaimNotFoundError(err)
	}
	if err := p.client.DeleteLKENodePool(ctx, p.clusterID, poolID); err != nil {
		if linodego.IsNotFound(err) {
			p.nodeCache.Delete(id)
			return cloudprovider.NewNodeClaimNotFoundError(err)
		}
		return err
	}
	p.nodeCache.Delete(id)
	return nil
}

func (p *DefaultProvider) CreateTags(ctx context.Context, id string, tags map[string]string) error {
	poolID, err := p.lookupPoolByInstance(ctx, id)
	if err != nil {
		return err
	}
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

func (p *DefaultProvider) cacheNode(n *instance.Instance) {
	id := strconv.Itoa(n.ID)
	p.nodeCache.SetDefault(id, n)
}

func (p *DefaultProvider) lookupPoolByInstance(ctx context.Context, id string) (int, error) {
	if cached, found := p.nodeCache.Get(id); found {
		return cached.(*instance.Instance).PoolID, nil
	}
	instanceID, err := strconv.Atoi(id)
	if err != nil {
		return 0, fmt.Errorf("parsing instance ID, %w", err)
	}
	pools, err := p.client.ListLKENodePools(ctx, p.clusterID, nil)
	if err != nil {
		return 0, err
	}
	for _, pool := range pools {
		// Only look in Karpenter-managed pools (API filtering may not work)
		if !isKarpenterManagedPool(&pool) {
			continue
		}
		for _, node := range pool.Linodes {
			if node.InstanceID == instanceID {
				return pool.ID, nil
			}
		}
	}
	return 0, fmt.Errorf("instance %d not found in any Karpenter-managed pool", instanceID)
}

func findNodeInPool(pool *linodego.LKENodePool, id string) (*linodego.LKENodePoolLinode, error) {
	instanceID, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("parsing instance ID, %w", err)
	}
	for _, node := range pool.Linodes {
		if node.InstanceID == instanceID {
			return &node, nil
		}
	}
	return nil, fmt.Errorf("instance %d not found in pool %d", instanceID, pool.ID)
}

func (p *DefaultProvider) findPoolByNodeClaimTag(ctx context.Context, nodeClaimName string) (*linodego.LKENodePool, error) {
	pools, err := p.client.ListLKENodePools(ctx, p.clusterID, nil)
	if err != nil {
		return nil, err
	}
	filtered := make([]linodego.LKENodePool, 0, 1)
	for _, pool := range pools {
		// Need to manually filter pools by nodeclaim tag since API serverside filtering is not working :)
		if poolHasNodeClaimTag(&pool, nodeClaimName) {
			filtered = append(filtered, pool)
		}
	}
	// no node pools found, we can create a new nodepool for this nodeclaim
	if len(filtered) == 0 {
		return nil, nil
	}
	// multiple node pools found, we should fail. This should never happen.
	if len(filtered) > 1 {
		return nil, fmt.Errorf("found multiple node pools for nodeclaim %q. This should never happen.", nodeClaimName)
	}
	return &filtered[0], nil
}

// poolHasNodeClaimTag checks if the pool has the node claim tag.
// if a pool has the node claim tag, it means that the pool is managed by karpenter.
func poolHasNodeClaimTag(pool *linodego.LKENodePool, nodeClaimName string) bool {
	tags := utils.TagListToMap(pool.Tags)
	return tags[v1.NodeClaimTagKey] == nodeClaimName
}
