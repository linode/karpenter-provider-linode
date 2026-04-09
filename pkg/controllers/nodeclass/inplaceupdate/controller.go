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

package inplaceupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/awslabs/operatorpkg/reasonable"
	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/api/equality"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	corenodeclaimutils "sigs.k8s.io/karpenter/pkg/utils/nodeclaim"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instance"
	"github.com/linode/karpenter-provider-linode/pkg/utils"
)

type Controller struct {
	cloudProvider cloudprovider.CloudProvider
	kubeClient    client.Client
	nodeProvider  instance.Provider
}

func NewController(kubeClient client.Client, cloudProvider cloudprovider.CloudProvider, nodeProvider instance.Provider) *Controller {
	return &Controller{
		cloudProvider: cloudProvider,
		kubeClient:    kubeClient,
		nodeProvider:  nodeProvider,
	}
}

func (c *Controller) Reconcile(ctx context.Context, nodeClaim *karpv1.NodeClaim) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, "nodeclass.inplaceupdate")
	nodeClass, err := c.getNodeClass(ctx, nodeClaim)
	if err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if nodeClass == nil {
		return reconcile.Result{}, nil
	}
	if err := c.updateNodeClaimTags(ctx, nodeClaim, nodeClass); err != nil {
		return reconcile.Result{}, cloudprovider.IgnoreNodeClaimNotFoundError(err)
	}
	return reconcile.Result{}, nil
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("nodeclass.inplaceupdate").
		For(
			&karpv1.NodeClaim{},
			builder.WithPredicates(
				corenodeclaimutils.IsManagedPredicateFuncs(c.cloudProvider),
				predicate.NewPredicateFuncs(isUpdatable),
			),
		).
		Watches(
			&v1.LinodeNodeClass{},
			corenodeclaimutils.NodeClassEventHandler(m.GetClient()),
			builder.WithPredicates(predicate.Funcs{
				CreateFunc:  func(event.CreateEvent) bool { return false },
				DeleteFunc:  func(event.DeleteEvent) bool { return false },
				GenericFunc: func(event.GenericEvent) bool { return false },
				UpdateFunc:  nodeClassTagsChanged,
			}),
		).
		WithOptions(controller.Options{
			RateLimiter:             reasonable.RateLimiter(),
			MaxConcurrentReconciles: 10,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}

func (c *Controller) updateNodeClaimTags(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1.LinodeNodeClass) error {
	logger := log.FromContext(ctx).WithValues("NodeClaim", nodeClaim.Name, "provider-id", nodeClaim.Status.ProviderID)
	ctx = log.IntoContext(ctx, logger)

	id, err := utils.ParseInstanceID(nodeClaim.Status.ProviderID)
	if err != nil {
		logger.Error(err, "failed parsing instance id")
		return nil
	}

	node, err := c.nodeProvider.Get(ctx, id, instance.SkipCache)
	if err != nil {
		return cloudprovider.IgnoreNodeClaimNotFoundError(fmt.Errorf("getting instance for in-place tag update, %w", err))
	}

	// We only own tags previously applied from LinodeNodeClass.spec.tags. Everything
	// else, including opaque tags added by LKE or other external actors, must be
	// preserved. TODO: simplify this once upstream LKE instance tag semantics are
	// better understood and we can narrow what must be preserved here.
	finalTagList := desiredUserTagsForInstance(node.Tags, previousUserTags(nodeClaim), nodeClass.Spec.Tags)
	finalTagList = normalizeTagList(finalTagList)

	currentTagList := normalizeTagList(node.Tags)

	if !slices.Equal(currentTagList, finalTagList) {
		if err := c.nodeProvider.UpdateTags(ctx, id, finalTagList); err != nil {
			return fmt.Errorf("updating nodeclaim tags in place, %w", err)
		}
	}

	lastApplied, err := marshalUserTags(nodeClass.Spec.Tags)
	if err != nil {
		return err
	}

	stored := nodeClaim.DeepCopy()
	nodeClaim.Annotations = lo.Assign(nodeClaim.Annotations, map[string]string{
		v1.AnnotationLastAppliedUserTags: lastApplied,
	})
	if equality.Semantic.DeepEqual(nodeClaim, stored) {
		return nil
	}
	if err := c.kubeClient.Patch(ctx, nodeClaim, client.MergeFrom(stored)); err != nil {
		return client.IgnoreNotFound(err)
	}
	return nil
}

func isUpdatable(o client.Object) bool {
	nodeClaim, ok := o.(*karpv1.NodeClaim)
	if !ok {
		return false
	}
	if nodeClaim.Status.ProviderID == "" {
		return false
	}
	if !nodeClaim.DeletionTimestamp.IsZero() {
		return false
	}
	return true
}

func (c *Controller) getNodeClass(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*v1.LinodeNodeClass, error) {
	nodeClass := &v1.LinodeNodeClass{}
	if err := c.kubeClient.Get(ctx, client.ObjectKey{Name: nodeClaim.Spec.NodeClassRef.Name}, nodeClass); err != nil {
		return nil, err
	}
	return nodeClass, nil
}

func desiredUserTagsForInstance(existingTags, previousUserTags, desiredUserTags []string) []string {
	existingKVTags := utils.TagListToMap(existingTags)
	previousUserKVTags := utils.TagListToMap(previousUserTags)
	desiredUserKVTags := utils.TagListToMap(desiredUserTags)

	finalKVTags := lo.Assign(map[string]string{}, existingKVTags)
	for key := range previousUserKVTags {
		delete(finalKVTags, key)
	}
	finalKVTags = lo.Assign(finalKVTags, desiredUserKVTags)

	previousOpaqueTags := utils.OpaqueTags(previousUserTags)
	previousOpaqueSet := make(map[string]struct{}, len(previousOpaqueTags))
	for _, tag := range previousOpaqueTags {
		previousOpaqueSet[tag] = struct{}{}
	}

	finalOpaqueTags := make([]string, 0, len(existingTags))
	for _, tag := range utils.OpaqueTags(existingTags) {
		if _, exists := previousOpaqueSet[tag]; exists {
			continue
		}
		finalOpaqueTags = append(finalOpaqueTags, tag)
	}
	finalOpaqueTags = append(finalOpaqueTags, utils.OpaqueTags(desiredUserTags)...)

	finalTagList := append(utils.MapToTagList(finalKVTags), finalOpaqueTags...)
	return utils.DedupeTags(finalTagList)
}

func normalizeTagList(tags []string) []string {
	normalizedTags := append(utils.MapToTagList(utils.TagListToMap(tags)), utils.OpaqueTags(tags)...)
	normalizedTags = utils.DedupeTags(normalizedTags)
	slices.Sort(normalizedTags)
	return normalizedTags
}

func previousUserTags(nodeClaim *karpv1.NodeClaim) []string {
	previousUserTags, err := unmarshalUserTags(nodeClaim.Annotations[v1.AnnotationLastAppliedUserTags])
	if err != nil {
		return []string{}
	}
	return previousUserTags
}

func marshalUserTags(tags []string) (string, error) {
	normalizedTags := append(utils.MapToTagList(utils.TagListToMap(tags)), utils.OpaqueTags(tags)...)
	normalizedTags = utils.DedupeTags(normalizedTags)
	if normalizedTags == nil {
		normalizedTags = []string{}
	}
	slices.Sort(normalizedTags)
	bytes, err := json.Marshal(normalizedTags)
	if err != nil {
		return "", fmt.Errorf("marshaling user tags, %w", err)
	}
	return string(bytes), nil
}

func unmarshalUserTags(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		return nil, fmt.Errorf("unmarshalling user tags, %w", err)
	}
	return tags, nil
}

func nodeClassTagsChanged(e event.UpdateEvent) bool {
	if e.ObjectOld == nil || e.ObjectNew == nil {
		return true
	}
	oldNodeClass, ok := e.ObjectOld.(*v1.LinodeNodeClass)
	if !ok {
		return true
	}
	newNodeClass, ok := e.ObjectNew.(*v1.LinodeNodeClass)
	if !ok {
		return true
	}
	oldTags := normalizeTagList(oldNodeClass.Spec.Tags)
	newTags := normalizeTagList(newNodeClass.Spec.Tags)
	return !slices.Equal(oldTags, newTags)
}
