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
	"github.com/linode/linodego"
	"github.com/patrickmn/go-cache"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/keymutex"
	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"

	"github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	linodecache "github.com/linode/karpenter-provider-linode/pkg/cache"
	sdk "github.com/linode/karpenter-provider-linode/pkg/linode"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instance"
	"github.com/linode/karpenter-provider-linode/pkg/utils"
)

const (
	DefaultCreateDeadline         = 10 * time.Second
	DefaultTagVerificationTimeout = 4 * time.Second
	DefaultRetryDelay             = 2 * time.Second
)

var ErrNodesProvisioning = errors.New("nodes provisioning")
var ErrNoClaimableInstance = errors.New("no claimable instance")
var ErrClaimFailed = errors.New("claim failed")
var ErrPoolScaleFailed = errors.New("pool scale failed")

type DefaultProvider struct {
	clusterID            int
	clusterTier          linodego.LKEVersionTier
	clusterName          string
	region               string
	recorder             events.Recorder
	client               sdk.LinodeAPI
	unavailableOfferings *linodecache.UnavailableOfferings
	nodeCache            *cache.Cache
	poolMutex            keymutex.KeyMutex
	config               ProviderConfig
}

type ProviderConfig struct {
	CreateDeadline         time.Duration
	TagVerificationTimeout time.Duration
	RetryDelay             time.Duration
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
	config ProviderConfig,
) *DefaultProvider {
	if config.CreateDeadline == 0 {
		config.CreateDeadline = DefaultCreateDeadline
	}
	if config.TagVerificationTimeout == 0 {
		config.TagVerificationTimeout = DefaultTagVerificationTimeout
	}
	if config.RetryDelay == 0 {
		config.RetryDelay = DefaultRetryDelay
	}
	return &DefaultProvider{
		clusterID:            clusterID,
		clusterTier:          clusterTier,
		clusterName:          clusterName,
		region:               region,
		recorder:             recorder,
		client:               client,
		unavailableOfferings: unavailableOfferings,
		nodeCache:            nodePoolCache,
		poolMutex:            keymutex.NewHashed(0),
		config:               config,
	}
}

func (p *DefaultProvider) Create(ctx context.Context, nodeClass *v1alpha1.LinodeNodeClass, nodeClaim *karpv1.NodeClaim, tags map[string]string, instanceTypes []*cloudprovider.InstanceType) (*instance.Instance, error) {
	cheapestType, instanceType, err := p.resolveCreateInstanceType(ctx, instanceTypes, nodeClaim)
	if err != nil {
		return nil, err
	}

	existingInstance, err := p.lookupExistingInstance(ctx, nodeClaim)
	if err != nil {
		return nil, err
	}
	if existingInstance != nil {
		return existingInstance, nil
	}

	poolKey := makePoolKey(nodeClaim.Labels[karpv1.NodePoolLabelKey], instanceType)
	scaledOnce := false
	createdPool := false
	deadline := time.Now().Add(p.config.CreateDeadline)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		inst, err := p.attemptCreate(ctx, nodeClass, nodeClaim, tags, cheapestType, instanceType, poolKey, &createdPool, &scaledOnce)
		if err != nil {
			if isRetryableCreateError(err) || utils.IsRetryableError(err) {
				time.Sleep(p.config.RetryDelay)
				continue
			}
			return nil, err
		}
		return inst, nil
	}

	return nil, cloudprovider.NewCreateError(
		fmt.Errorf("timed out waiting for claimable instance for nodeclaim %s", nodeClaim.Name),
		"NodePoolProvisioning",
		"Timed out waiting for LKE instance to become available",
	)
}

func (p *DefaultProvider) resolveCreateInstanceType(ctx context.Context, instanceTypes []*cloudprovider.InstanceType, nodeClaim *karpv1.NodeClaim) (*cloudprovider.InstanceType, string, error) {
	filteredInstanceTypes, err := utils.FilterInstanceTypes(ctx, instanceTypes, nodeClaim)
	if err != nil {
		return nil, "", err
	}
	cheapestType, err := utils.CheapestInstanceType(filteredInstanceTypes)
	if err != nil {
		return nil, "", err
	}
	return cheapestType, cheapestType.Name, nil
}

func (p *DefaultProvider) lookupExistingInstance(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*instance.Instance, error) {
	logger := log.FromContext(ctx)
	nodeClaimTag := fmt.Sprintf("%s=%s", v1alpha1.NodeClaimTagKey, nodeClaim.Name)
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
	return nil, nil
}

func (p *DefaultProvider) attemptCreate(ctx context.Context, nodeClass *v1alpha1.LinodeNodeClass, nodeClaim *karpv1.NodeClaim, tags map[string]string, cheapestType *cloudprovider.InstanceType, instanceType string, poolKey string, createdPool *bool, scaledOnce *bool) (*instance.Instance, error) {
	logger := log.FromContext(ctx)
	return p.withPoolLock(ctx, poolKey, func() (*instance.Instance, error) {
		pool, err := p.findOrCreatePool(ctx, nodeClass, nodeClaim, tags, instanceType, createdPool)
		if err != nil {
			utils.UpdateUnavailableOfferingsCache(ctx, err, p.region, cheapestType, p.unavailableOfferings)
			return nil, cloudprovider.NewCreateError(err, "NodePoolCreationFailed", fmt.Sprintf("Failed to find or create LKE node pool: %s", err.Error()))
		}

		claimableInstance, err := p.findClaimableInstance(ctx, pool)
		if err != nil {
			if errors.Is(err, ErrNoClaimableInstance) {
				claimableInstance = nil
			} else {
				return nil, err
			}
		}

		if claimableInstance != nil {
			claimedInstance, err := p.claimInstance(ctx, claimableInstance, nodeClaim, pool)
			if err != nil {
				logger.Error(err, "failed to claim instance", "instanceID", claimableInstance.ID)
				return nil, fmt.Errorf("%w: %w", ErrClaimFailed, err)
			}
			inst := instance.NewLKEInstance(claimedInstance.ID, pool.Type, claimedInstance.Tags, p.region, claimedInstance.Created)
			p.cacheNode(inst)
			return inst, nil
		}

		if *createdPool || *scaledOnce {
			return nil, ErrNoClaimableInstance
		}

		_, err = p.client.UpdateLKENodePool(ctx, p.clusterID, pool.ID, linodego.LKENodePoolUpdateOptions{Count: pool.Count + 1})
		if err != nil {
			logger.Error(err, "failed to scale pool", "poolID", pool.ID)
			return nil, fmt.Errorf("%w: %w", ErrPoolScaleFailed, err)
		}
		*scaledOnce = true
		return nil, ErrNoClaimableInstance
	})
}

func (p *DefaultProvider) withPoolLock(ctx context.Context, key string, fn func() (*instance.Instance, error)) (*instance.Instance, error) {
	p.poolMutex.LockKey(key)
	defer func() {
		if err := p.poolMutex.UnlockKey(key); err != nil {
			log.FromContext(ctx).Error(err, "failed to unlock pool mutex", "key", key)
		}
	}()
	return fn()
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

func (p *DefaultProvider) findClaimableInstance(ctx context.Context, pool *linodego.LKENodePool) (*linodego.Instance, error) {
	if p.clusterTier == linodego.LKEVersionEnterprise {
		return p.findClaimableInstanceEnterprise(ctx, pool)
	}
	return p.findClaimableInstanceStandard(ctx, pool)
}

func (p *DefaultProvider) findClaimableInstanceStandard(ctx context.Context, pool *linodego.LKENodePool) (*linodego.Instance, error) {
	freshPool, err := p.client.GetLKENodePool(ctx, p.clusterID, pool.ID)
	if err != nil {
		return nil, fmt.Errorf("getting node pool %d: %w", pool.ID, err)
	}

	nodesProvisioning := false
	for _, node := range freshPool.Linodes {
		// Don't return early - we need to check all nodes for claimable instances first
		if node.InstanceID == 0 {
			nodesProvisioning = true
			continue
		}

		linodeInstance, err := p.client.GetInstance(ctx, node.InstanceID)
		if err != nil {
			if linodego.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("getting instance %d: %w", node.InstanceID, err)
		}

		// Check if instance is already claimed by a NodeClaim
		// If not claimed, return it for claiming
		tags := utils.TagListToMap(linodeInstance.Tags)
		if _, exists := tags[v1alpha1.NodeClaimTagKey]; !exists {
			return linodeInstance, nil
		}
	}

	if nodesProvisioning {
		return nil, ErrNodesProvisioning
	}

	return nil, ErrNoClaimableInstance
}

func (p *DefaultProvider) findClaimableInstanceEnterprise(ctx context.Context, pool *linodego.LKENodePool) (*linodego.Instance, error) {
	// Use Linode's automatic nodepool=<id> tag for enterprise discovery
	filter := linodego.Filter{}
	filter.AddField(linodego.Contains, "tags", fmt.Sprintf("nodepool=%d", pool.ID))
	filter.AddField(linodego.Contains, "tags", fmt.Sprintf("lke%d", p.clusterID))
	filter.AddField(linodego.Contains, "tags", fmt.Sprintf("%v=%v", v1alpha1.LabelLKEManaged, "true"))
	filterJSON, err := filter.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("building filter: %w", err)
	}

	instances, err := p.client.ListInstances(ctx, linodego.NewListOptions(0, string(filterJSON)))
	if err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}

	for _, linodeInstance := range instances {
		if linodeInstance.Type != pool.Type {
			continue
		}
		// Check if instance is already claimed by a NodeClaim
		tags := utils.TagListToMap(linodeInstance.Tags)
		if _, exists := tags[v1alpha1.NodeClaimTagKey]; !exists {
			return &linodeInstance, nil
		}
	}

	return nil, ErrNoClaimableInstance
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
	deadline := time.Now().Add(p.config.TagVerificationTimeout)
	expectedTag := fmt.Sprintf("%s=%s", v1alpha1.NodeClaimTagKey, nodeClaimName)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		inst, err := p.client.GetInstance(ctx, instanceID)
		if err != nil {
			if utils.IsRetryableError(err) || linodego.IsNotFound(err) {
				time.Sleep(p.config.RetryDelay)
				continue
			}
			return nil, fmt.Errorf("getting instance %d: %w", instanceID, err)
		}

		if slices.Contains(inst.Tags, expectedTag) {
			return inst, nil
		}
		time.Sleep(p.config.RetryDelay)
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

func (p *DefaultProvider) Get(ctx context.Context, id string, opts ...instance.Options) (*instance.Instance, error) {
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
	linodeInstance, err := p.client.GetInstance(ctx, instanceID)
	if err != nil {
		if linodego.IsNotFound(err) {
			return nil, cloudprovider.NewNodeClaimNotFoundError(err)
		}
		return nil, fmt.Errorf("getting instance %d: %w", instanceID, err)
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
	filter.AddField(linodego.Contains, "tags", fmt.Sprintf("%s=%s", v1alpha1.LabelLKEManaged, "true"))
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

func isRetryableCreateError(err error) bool {
	return errors.Is(err, ErrNodesProvisioning) ||
		errors.Is(err, ErrNoClaimableInstance) ||
		errors.Is(err, ErrClaimFailed) ||
		errors.Is(err, ErrPoolScaleFailed)
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
