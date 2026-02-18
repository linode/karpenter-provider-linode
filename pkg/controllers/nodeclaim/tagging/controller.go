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

	"github.com/awslabs/operatorpkg/reasonable"
	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/api/equality"
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

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instance"
	"github.com/linode/karpenter-provider-linode/pkg/utils"
)

type Controller struct {
	kubeClient    client.Client
	cloudProvider cloudprovider.CloudProvider
	nodeProvider  instance.Provider
}

func NewController(kubeClient client.Client, cloudProvider cloudprovider.CloudProvider, nodeProvider instance.Provider) *Controller {
	return &Controller{
		kubeClient:    kubeClient,
		cloudProvider: cloudProvider,
		nodeProvider:  nodeProvider,
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
	nodeClaim.Annotations = lo.Assign(nodeClaim.Annotations, map[string]string{
		v1.AnnotationInstanceTagged: "true",
	})
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
		v1.NameTagKey:           nc.Status.NodeName,
		v1.LKEClusterNameTagKey: options.FromContext(ctx).ClusterName,
	}

	node, err := c.nodeProvider.Get(ctx, id, instance.SkipCache)
	if err != nil {
		return fmt.Errorf("getting instance for tagging, %w", err)
	}
	existingTags := utils.TagListToMap(node.Tags)
	tags = lo.OmitByKeys(tags, lo.Keys(existingTags))
	if len(tags) == 0 {
		return nil
	}

	if err := c.nodeProvider.CreateTags(ctx, id, tags); err != nil {
		return fmt.Errorf("tagging nodeclaim, %w", err)
	}
	return nil
}

func isTaggable(nc *karpv1.NodeClaim) bool {
	// Instance has already been tagged
	instanceTagged := nc.Annotations[v1.AnnotationInstanceTagged]
	clusterNameTagged := nc.Annotations[v1.AnnotationClusterNameTaggedCompatability]
	if instanceTagged == "true" && clusterNameTagged == "true" {
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
