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
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/awslabs/operatorpkg/option"
	"github.com/patrickmn/go-cache"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"

	"github.com/linode/linodego"

	"github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	linodecache "github.com/linode/karpenter-provider-linode/pkg/cache"
	sdk "github.com/linode/karpenter-provider-linode/pkg/linode"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instance"
	"github.com/linode/karpenter-provider-linode/pkg/utils"
)

const (
	DefaultCreateDeadline         = 20 * time.Second
	DefaultTagVerificationTimeout = 5 * time.Second
)

var ErrNodesProvisioning = errors.New("nodes provisioning")

type DefaultProvider struct {
	clusterID            int
	clusterTier          linodego.LKEVersionTier
	clusterName          string
	region               string
	recorder             events.Recorder
	client               sdk.LinodeAPI
	unavailableOfferings *linodecache.UnavailableOfferings
	nodeCache            *cache.Cache
	poolMutex            *utils.KeyedMutex
}

func NewDefaultProvider(
	clusterID int,
	clusterTier linodego.LKEVersionTier,
	clusterName string,
	region string,
	recorder events.Recorder,
	client sdk.LinodeAPI,
	unavailableOfferings *linodecache.UnavailableOfferings,
	nodePoolCache *cache.Cache,
) *DefaultProvider {
	return &DefaultProvider{
		clusterID:            clusterID,
		clusterTier:          clusterTier,
		clusterName:          clusterName,
		region:               region,
		recorder:             recorder,
		client:               client,
		unavailableOfferings: unavailableOfferings,
		nodeCache:            nodePoolCache,
		poolMutex:            utils.NewKeyedMutex(),
	}
}

func (p *DefaultProvider) Create(ctx context.Context, nodeClass *v1alpha1.LinodeNodeClass, nodeClaim *karpv1.NodeClaim, tags map[string]string, instanceTypes []*cloudprovider.InstanceType) (*instance.Instance, error) {
	logger := log.FromContext(ctx)

	instanceTypes, err := utils.FilterInstanceTypes(ctx, instanceTypes, nodeClaim)
	if err != nil {
		return nil, err
	}
	cheapestType, err := utils.CheapestInstanceType(instanceTypes)
	if err != nil {
		return nil, err
	}
	instanceType := cheapestType.Name

	nodeClaimTag := fmt.Sprintf("%s:%s", v1alpha1.NodeClaimTagKey, nodeClaim.Name)
	existingInstance, err := utils.LookupInstanceByTag(ctx, p.client, nodeClaimTag)
	if err == nil && existingInstance != nil {
		logger.V(1).Info("found existing instance for nodeclaim", "instanceID", existingInstance.ID, "nodeclaim", nodeClaim.Name)
		return p.hydrateInstanceFromLinode(ctx, existingInstance)
	}
	if err != nil && !errors.Is(err, utils.ErrInstanceNotFound) {
		return nil, cloudprovider.NewCreateError(
			err,
			"InstanceLookupFailed",
			fmt.Sprintf("Failed to lookup existing instance for nodeclaim %s", nodeClaim.Name),
		)
	}

	poolKey := makePoolKey(nodeClaim.Labels[karpv1.NodePoolLabelKey], instanceType)
	scaledOnce := false
	createdPool := false
	deadline := time.Now().Add(DefaultCreateDeadline)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		p.poolMutex.Lock(poolKey)
		pool, err := p.findOrCreatePool(ctx, nodeClass, nodeClaim, tags, instanceType, &createdPool)
		if err != nil {
			p.poolMutex.Unlock(poolKey)
			utils.UpdateUnavailableOfferingsCache(ctx, err, p.region, cheapestType, p.unavailableOfferings)
			return nil, cloudprovider.NewCreateError(err, "NodePoolCreationFailed", fmt.Sprintf("Failed to find or create LKE node pool: %s", err.Error()))
		}

		// TODO: remove the nodeID returned field
		claimableInstance, _, err := p.findClaimableInstance(ctx, pool)
		if err != nil {
			p.poolMutex.Unlock(poolKey)
			if errors.Is(err, ErrNodesProvisioning) {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			return nil, err
		}

		if claimableInstance != nil {
			claimedInstance, err := p.claimInstance(ctx, claimableInstance, nodeClaim, pool)
			if err != nil {
				logger.Error(err, "failed to claim instance", "instanceID", claimableInstance.ID)
				p.poolMutex.Unlock(poolKey)
				continue
			}

			inst := &instance.Instance{
				ID:     claimedInstance.ID,
				Region: p.region,
				Type:   pool.Type,
				Tags:   claimedInstance.Tags,
			}
			if claimedInstance.Created != nil {
				inst.Created = claimedInstance.Created
			}
			p.cacheNode(inst)
			p.poolMutex.Unlock(poolKey)
			return inst, nil
		}

		if createdPool || scaledOnce {
			p.poolMutex.Unlock(poolKey)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		newCount := pool.Count + 1
		_, err = p.client.UpdateLKENodePool(ctx, p.clusterID, pool.ID, linodego.LKENodePoolUpdateOptions{Count: newCount})
		if err != nil {
			logger.Error(err, "failed to scale pool", "poolID", pool.ID)
			p.poolMutex.Unlock(poolKey)
			continue
		}
		scaledOnce = true
		p.poolMutex.Unlock(poolKey)
		time.Sleep(200 * time.Millisecond)
	}

	return nil, cloudprovider.NewCreateError(
		fmt.Errorf("timed out waiting for claimable instance for nodeclaim %s", nodeClaim.Name),
		"NodePoolProvisioning",
		"Timed out waiting for LKE instance to become available",
	)
}

func (p *DefaultProvider) findOrCreatePool(ctx context.Context, nodeClass *v1alpha1.LinodeNodeClass, nodeClaim *karpv1.NodeClaim, tags map[string]string, instanceType string, createdPool *bool) (*linodego.LKENodePool, error) {
	// Step 1: Find existing pool
	pools, err := p.client.ListLKENodePools(ctx, p.clusterID, nil)
	if err != nil {
		return nil, fmt.Errorf("listing node pools: %w", err)
	}
	karpenterNodePoolName := nodeClaim.Labels[karpv1.NodePoolLabelKey]
	for i := range pools {
		pool := &pools[i]
		if p.matchesPoolKey(pool, instanceType, karpenterNodePoolName) {
			return pool, nil
		}
	}

	// Step 2: Create new pool
	poolTags := utils.GetTagsForLKE(nodeClass, nodeClaim, p.clusterName)
	tagList := utils.MapToTagList(poolTags)
	tagList = append(tagList, utils.MapToTagList(tags)...)
	tagList = append(tagList, nodeClass.Spec.Tags...)
	tagList = utils.DedupeTags(tagList)

	// Eventually we need to differentiate between taints and startup taints on LKE API
	allTaints := append(nodeClaim.Spec.Taints, nodeClaim.Spec.StartupTaints...)
	taints := convertToLkeTaints(allTaints)

	createOpts := linodego.LKENodePoolCreateOptions{
		Count:  1,
		Type:   instanceType,
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
	if err != nil {
		return nil, fmt.Errorf("creating node pool: %w", err)
	}
	if createdPool != nil {
		*createdPool = true
	}
	return pool, nil
}

func (p *DefaultProvider) matchesPoolKey(pool *linodego.LKENodePool, instanceType string, nodePoolName string) bool {
	if pool.Type != instanceType {
		return false
	}
	if !isKarpenterManagedPool(pool) {
		return false
	}
	tags := utils.TagListToMap(pool.Tags)
	return tags[karpv1.NodePoolLabelKey] == nodePoolName
}

func (p *DefaultProvider) findClaimableInstance(ctx context.Context, pool *linodego.LKENodePool) (*linodego.Instance, string, error) {
	if p.clusterTier == linodego.LKEVersionEnterprise {
		return p.findClaimableInstanceEnterprise(ctx, pool)
	}
	return p.findClaimableInstanceStandard(ctx, pool)
}

func (p *DefaultProvider) findClaimableInstanceStandard(ctx context.Context, pool *linodego.LKENodePool) (*linodego.Instance, string, error) {
	freshPool, err := p.client.GetLKENodePool(ctx, p.clusterID, pool.ID)
	if err != nil {
		return nil, "", fmt.Errorf("getting node pool %d: %w", pool.ID, err)
	}

	nodesProvisioning := false
	for _, node := range freshPool.Linodes {
		if node.InstanceID == 0 {
			nodesProvisioning = true
			continue
			// Don't return early - we need to check all nodes for claimable instances first
		}

		linodeInstance, err := p.client.GetInstance(ctx, node.InstanceID)
		if err != nil {
			if linodego.IsNotFound(err) {
				continue
			}
			return nil, "", fmt.Errorf("getting instance %d: %w", node.InstanceID, err)
		}

		// Check if instance is already claimed by a NodeClaim
		// If not claimed, return it for claiming
		tags := utils.TagListToMap(linodeInstance.Tags)
		if _, exists := tags[v1alpha1.NodeClaimTagKey]; !exists {
			return linodeInstance, node.ID, nil
		}
	}

	if nodesProvisioning {
		return nil, "", ErrNodesProvisioning
	}

	return nil, "", nil
}

func (p *DefaultProvider) findClaimableInstanceEnterprise(ctx context.Context, pool *linodego.LKENodePool) (*linodego.Instance, string, error) {
	// Use Linode's automatic nodepool=<id> tag for enterprise discovery
	filter := linodego.Filter{}
	filter.AddField(linodego.Contains, "tags", fmt.Sprintf("nodepool=%d", pool.ID))
	filter.AddField(linodego.Contains, "tags", fmt.Sprintf("lke%d", p.clusterID))
	filter.AddField(linodego.Contains, "tags", fmt.Sprintf("%v:%v", v1alpha1.LabelLKEManaged, "true"))
	filterJSON, err := filter.MarshalJSON()
	if err != nil {
		return nil, "", fmt.Errorf("building filter: %w", err)
	}

	instances, err := p.client.ListInstances(ctx, linodego.NewListOptions(0, string(filterJSON)))
	if err != nil {
		return nil, "", fmt.Errorf("listing instances: %w", err)
	}

	for _, linodeInstance := range instances {
		if linodeInstance.Type != pool.Type {
			continue
		}
		// Check if instance is already claimed by a NodeClaim
		tags := utils.TagListToMap(linodeInstance.Tags)
		if _, exists := tags[v1alpha1.NodeClaimTagKey]; !exists {
			// For Enterprise tier, instance Label IS the node ID.
			// This is critical because pool.Linodes may not be populated for 30-60s.
			nodeID := linodeInstance.Label
			return &linodeInstance, nodeID, nil
		}
	}

	return nil, "", nil
}

func (p *DefaultProvider) claimInstance(ctx context.Context, linodeInstance *linodego.Instance, nodeClaim *karpv1.NodeClaim, pool *linodego.LKENodePool) (*linodego.Instance, error) {
	instanceTags := utils.GetInstanceTagsForLKE(nodeClaim.Name)
	newTags := append([]string{}, linodeInstance.Tags...)
	newTags = append(newTags, pool.Tags...)
	newTags = append(newTags, utils.MapToTagList(instanceTags)...)
	newTags = utils.DedupeTags(newTags)

	_, err := p.client.UpdateInstance(ctx, linodeInstance.ID, linodego.InstanceUpdateOptions{
		Tags: &newTags,
	})
	if err != nil {
		return nil, fmt.Errorf("updating instance tags: %w", err)
	}

	return p.verifyTagsApplied(ctx, linodeInstance.ID, nodeClaim.Name)
}

func (p *DefaultProvider) verifyTagsApplied(ctx context.Context, instanceID int, nodeClaimName string) (*linodego.Instance, error) {
	deadline := time.Now().Add(DefaultTagVerificationTimeout)
	expectedTag := fmt.Sprintf("%s:%s", v1alpha1.NodeClaimTagKey, nodeClaimName)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		inst, err := p.client.GetInstance(ctx, instanceID)
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}

		if slices.Contains(inst.Tags, expectedTag) {
			return inst, nil
		}
		time.Sleep(1 * time.Second)
	}

	return nil, fmt.Errorf("timed out verifying tags on instance %d", instanceID)
}

func (p *DefaultProvider) hydrateInstanceFromLinode(_ context.Context, linodeInstance *linodego.Instance) (*instance.Instance, error) {
	linodeInstanceID := strconv.Itoa(linodeInstance.ID)
	if cached, found := p.nodeCache.Get(linodeInstanceID); found {
		return cached.(*instance.Instance), nil
	}

	inst := &instance.Instance{
		ID:      linodeInstance.ID,
		Created: linodeInstance.Created,
		Region:  p.region,
		Type:    linodeInstance.Type,
		Tags:    linodeInstance.Tags,
	}
	p.cacheNode(inst)
	return inst, nil
}

func (p *DefaultProvider) Get(_ context.Context, id string, opts ...instance.Options) (*instance.Instance, error) {
	options := option.Resolve(opts...)
	cacheKey := id
	if !options.SkipCache {
		if cached, found := p.nodeCache.Get(cacheKey); found {
			return cached.(*instance.Instance), nil
		}
	}
	instanceID, err := strconv.Atoi(id)
	if err != nil {
		return nil, cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("parsing instance ID: %w", err))
	}
	linodeInstance, err := p.client.GetInstance(context.Background(), instanceID)
	if err != nil {
		return nil, cloudprovider.NewNodeClaimNotFoundError(err)
	}
	inst := &instance.Instance{
		ID:      linodeInstance.ID,
		Created: linodeInstance.Created,
		Region:  p.region,
		Type:    linodeInstance.Type,
		Tags:    linodeInstance.Tags,
	}
	p.cacheNode(inst)
	return inst, nil
}

func (p *DefaultProvider) List(ctx context.Context) ([]*instance.Instance, error) {
	filter := linodego.Filter{}
	filter.AddField(linodego.Contains, "tags", fmt.Sprintf("%s:%s", v1alpha1.LabelLKEManaged, "true"))
	if p.clusterTier == linodego.LKEVersionEnterprise {
		filter.AddField(linodego.Contains, "tags", fmt.Sprintf("lke%d", p.clusterID))
	}
	filterJSON, err := filter.MarshalJSON()
	if err != nil {
		return nil, err
	}
	instances, err := p.client.ListInstances(ctx, linodego.NewListOptions(0, string(filterJSON)))
	if err != nil {
		return nil, err
	}
	var result []*instance.Instance
	for _, linodeInstance := range instances {
		if p.clusterID != linodeInstance.LKEClusterID {
			continue
		}
		inst := &instance.Instance{
			ID:      linodeInstance.ID,
			Created: linodeInstance.Created,
			Region:  p.region,
			Type:    linodeInstance.Type,
			Tags:    linodeInstance.Tags,
		}
		result = append(result, inst)
		p.cacheNode(inst)
	}
	return result, nil
}

func isKarpenterManagedPool(pool *linodego.LKENodePool) bool {
	tags := utils.TagListToMap(pool.Tags)
	_, hasNodePoolLabel := tags[karpv1.NodePoolLabelKey]
	_, hasLKEManagedLabel := tags[v1alpha1.LabelLKEManaged]
	return hasNodePoolLabel && hasLKEManagedLabel
}

func (p *DefaultProvider) Delete(ctx context.Context, id string) error {

	lkePool, err := p.findLKENodePoolFromLinodeInstanceID(ctx, id)
	if err != nil {
		return cloudprovider.NewNodeClaimNotFoundError(err)
	}

	if len(lkePool.Linodes) <= 1 {
		if err := p.client.DeleteLKENodePool(ctx, p.clusterID, lkePool.ID); err != nil {
			if linodego.IsNotFound(err) {
				p.nodeCache.Delete(id)
				return cloudprovider.NewNodeClaimNotFoundError(err)
			}
			return err
		}
		p.nodeCache.Delete(id)
		return nil
	}

	poolNode, err := findNodeInPool(lkePool, id)
	if err != nil {
		p.nodeCache.Delete(id)
		return cloudprovider.NewNodeClaimNotFoundError(err)
	}

	if err := p.client.DeleteLKENodePoolNode(ctx, p.clusterID, poolNode.ID); err != nil {
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
	instanceID, err := strconv.Atoi(id)
	if err != nil {
		return fmt.Errorf("parsing instance ID: %w", err)
	}

	linodeInstance, err := p.client.GetInstance(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("getting instance %d: %w", instanceID, err)
	}

	newTags := append([]string{}, linodeInstance.Tags...)
	newTags = append(newTags, utils.MapToTagList(tags)...)
	newTags = utils.DedupeTags(newTags)

	_, err = p.client.UpdateInstance(ctx, instanceID, linodego.InstanceUpdateOptions{
		Tags: &newTags,
	})
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

func (p *DefaultProvider) findLKENodePoolFromLinodeInstanceID(ctx context.Context, id string) (*linodego.LKENodePool, error) {
	instanceID, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("parsing instance ID: %w", err)
	}
	pools, err := p.client.ListLKENodePools(ctx, p.clusterID, nil)
	if err != nil {
		return nil, err
	}
	for _, pool := range pools {
		if !isKarpenterManagedPool(&pool) {
			continue
		}
		for _, node := range pool.Linodes {
			if node.InstanceID == instanceID {
				return &pool, nil
			}
		}
	}
	return nil, fmt.Errorf("instance %d not found in any Karpenter-managed pool", instanceID)
}

func findNodeInPool(pool *linodego.LKENodePool, id string) (*linodego.LKENodePoolLinode, error) {
	instanceID, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("parsing instance ID: %w", err)
	}
	for _, node := range pool.Linodes {
		if node.InstanceID == instanceID {
			return &node, nil
		}
	}
	return nil, fmt.Errorf("instance %d not found in lke node pool %d", instanceID, pool.ID)
}

func makePoolKey(karpenterNodePoolName, instanceType string) string {
	return fmt.Sprintf("%s|%s", karpenterNodePoolName, instanceType)
}
