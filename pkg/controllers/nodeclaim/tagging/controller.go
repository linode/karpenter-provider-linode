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

package tagging

import (
	"context"
	"fmt"
	"time"

	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	"sigs.k8s.io/karpenter/pkg/utils/nodeclaim"

	"github.com/awslabs/operatorpkg/reasonable"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instance"
	"github.com/linode/karpenter-provider-linode/pkg/providers/lkenode"
	"github.com/linode/karpenter-provider-linode/pkg/utils"
)

type Controller struct {
	kubeClient       client.Client
	cloudProvider    cloudprovider.CloudProvider
	instanceProvider instance.Provider
	lkenodeProvider  lkenode.Provider
}

func NewController(kubeClient client.Client, cloudProvider cloudprovider.CloudProvider, instanceProvider instance.Provider, lkenodeProvider lkenode.Provider) *Controller {
	return &Controller{
		kubeClient:       kubeClient,
		cloudProvider:    cloudProvider,
		instanceProvider: instanceProvider,
		lkenodeProvider:  lkenodeProvider,
	}
}

func (c *Controller) Reconcile(ctx context.Context, nodeClaim *karpv1.NodeClaim) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, "nodeclaim.tagging")

	stored := nodeClaim.DeepCopy()
	if !isTaggable(nodeClaim) {
		return reconcile.Result{}, nil
	}
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues("Node", klog.KRef("", nodeClaim.Status.NodeName), "provider-id", nodeClaim.Status.ProviderID))
	id, err := utils.ParseInstanceID(nodeClaim.Status.ProviderID)
	if err != nil {
		// We don't throw an error here since we don't want to retry until the ProviderID has been updated.
		log.FromContext(ctx).Error(err, "failed parsing instance id")
		return reconcile.Result{}, nil
	}
	if err = c.tagInstance(ctx, nodeClaim, id); err != nil {
		return reconcile.Result{}, cloudprovider.IgnoreNodeClaimNotFoundError(err)
	}
	if !equality.Semantic.DeepEqual(nodeClaim, stored) {
		if err := c.kubeClient.Patch(ctx, nodeClaim, client.MergeFrom(stored)); err != nil {
			return reconcile.Result{}, client.IgnoreNotFound(err)
		}
	}
	return reconcile.Result{}, nil
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("nodeclaim.tagging").
		For(&karpv1.NodeClaim{}, builder.WithPredicates(nodeclaim.IsManagedPredicateFuncs(c.cloudProvider))).
		WithEventFilter(predicate.NewPredicateFuncs(func(o client.Object) bool {
			return isTaggable(o.(*karpv1.NodeClaim))
		})).
		// Ok with using the default MaxConcurrentReconciles of 1 to avoid throttling from CreateTag write API
		WithOptions(controller.Options{
			RateLimiter: reasonable.RateLimiter(),
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}

func (c *Controller) tagInstance(ctx context.Context, nc *karpv1.NodeClaim, id string) error {
	tags := map[string]string{
		v1.NameTagKey:      nc.Status.NodeName,
		v1.NodeClaimTagKey: nc.Name,
	}

	// Resolve NodeClass from NodeClaim's NodeClassRef to determine which provider to use
	nodeClass, err := c.resolveNodeClassFromNodeClaim(ctx, nc)
	if err != nil {
		return fmt.Errorf("resolving nodeclass for tagging, %w", err)
	}

	if nodeClass.IsLKEManaged() {
		return c.tagLKENode(ctx, tags, id)
	}
	return c.tagLinodeInstance(ctx, tags, id)
}

func (c *Controller) resolveNodeClassFromNodeClaim(ctx context.Context, nc *karpv1.NodeClaim) (*v1.LinodeNodeClass, error) {
	nodeClass := &v1.LinodeNodeClass{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: nc.Spec.NodeClassRef.Name}, nodeClass); err != nil {
		return nil, err
	}
	return nodeClass, nil
}

func (c *Controller) tagLKENode(ctx context.Context, tags map[string]string, id string) error {
	lkeNode, err := c.lkenodeProvider.Get(ctx, id, lkenode.SkipCache)
	if err != nil {
		return fmt.Errorf("getting lke node for tagging, %w", err)
	}
	existingTags := utils.TagListToMap(lkeNode.Tags)
	tags = lo.OmitByKeys(tags, lo.Keys(existingTags))
	if len(tags) == 0 {
		return nil
	}
	defer time.Sleep(time.Second)
	if err := c.lkenodeProvider.UpdateTags(ctx, lkeNode.PoolID, tags); err != nil {
		return fmt.Errorf("tagging lke nodeclaim, %w", err)
	}
	return nil
}

func (c *Controller) tagLinodeInstance(ctx context.Context, tags map[string]string, id string) error {
	instance, err := c.instanceProvider.Get(ctx, id, instance.SkipCache)
	if err != nil {
		return fmt.Errorf("getting instance for tagging, %w", err)
	}
	tags = lo.OmitByKeys(tags, instance.Tags)
	if len(tags) == 0 {
		return nil
	}

	// Ensures that no more than 1 CreateTags call is made per second. Rate limiting is required since CreateTags
	// shares a pool with other mutating calls (e.g. CreateFleet).
	defer time.Sleep(time.Second)
	if err := c.instanceProvider.CreateTags(ctx, id, tags); err != nil {
		return fmt.Errorf("tagging nodeclaim, %w", err)
	}
	return nil
}

func isTaggable(nc *karpv1.NodeClaim) bool {
	// Instance has already been tagged
	instanceTagged := nc.Annotations[v1.AnnotationInstanceTagged]
	if instanceTagged == "true" {
		return false
	}
	// Node name is not yet known
	if nc.Status.NodeName == "" {
		return false
	}
	// NodeClaim is currently terminating
	if !nc.DeletionTimestamp.IsZero() {
		return false
	}
	return true
}
