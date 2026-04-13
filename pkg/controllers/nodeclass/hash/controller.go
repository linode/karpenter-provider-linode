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

package hash

import (
	"context"

	"github.com/awslabs/operatorpkg/reasonable"
	"github.com/samber/lo"
	"go.uber.org/multierr"
	"k8s.io/apimachinery/pkg/api/equality"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	nodeclaimutils "sigs.k8s.io/karpenter/pkg/utils/nodeclaim"

	v1alpha1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
)

type Controller struct {
	kubeClient client.Client
}

func NewController(kubeClient client.Client) *Controller {
	return &Controller{kubeClient: kubeClient}
}

func (c *Controller) Reconcile(ctx context.Context, nodeClass *v1alpha1.LinodeNodeClass) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, "nodeclass.hash")

	stored := nodeClass.DeepCopy()
	if nodeClass.Annotations[v1alpha1.AnnotationLinodeNodeClassHashVersion] != v1alpha1.LinodeNodeClassHashVersion {
		if err := c.updateNodeClaimHash(ctx, nodeClass); err != nil {
			return reconcile.Result{}, err
		}
	}
	nodeClass.Annotations = lo.Assign(nodeClass.Annotations, map[string]string{
		v1alpha1.AnnotationLinodeNodeClassHash:        nodeClass.Hash(),
		v1alpha1.AnnotationLinodeNodeClassHashVersion: v1alpha1.LinodeNodeClassHashVersion,
	})

	if !equality.Semantic.DeepEqual(stored, nodeClass) {
		if err := c.kubeClient.Patch(ctx, nodeClass, client.MergeFrom(stored)); err != nil {
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("nodeclass.hash").
		For(&v1alpha1.LinodeNodeClass{}).
		WithOptions(controller.Options{
			RateLimiter:             reasonable.RateLimiter(),
			MaxConcurrentReconciles: 10,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}

func (c *Controller) updateNodeClaimHash(ctx context.Context, nodeClass *v1alpha1.LinodeNodeClass) error {
	nodeClaims := &karpv1.NodeClaimList{}
	if err := c.kubeClient.List(ctx, nodeClaims, nodeclaimutils.ForNodeClass(nodeClass)); err != nil {
		return err
	}

	errs := make([]error, len(nodeClaims.Items))
	for i := range nodeClaims.Items {
		nc := &nodeClaims.Items[i]
		stored := nc.DeepCopy()

		if nc.Annotations[v1alpha1.AnnotationLinodeNodeClassHashVersion] != v1alpha1.LinodeNodeClassHashVersion {
			nc.Annotations = lo.Assign(nc.Annotations, map[string]string{
				v1alpha1.AnnotationLinodeNodeClassHashVersion: v1alpha1.LinodeNodeClassHashVersion,
			})
			if nc.StatusConditions().Get(karpv1.ConditionTypeDrifted) == nil {
				nc.Annotations = lo.Assign(nc.Annotations, map[string]string{
					v1alpha1.AnnotationLinodeNodeClassHash: nodeClass.Hash(),
				})
			}
			if !equality.Semantic.DeepEqual(stored, nc) {
				if err := c.kubeClient.Patch(ctx, nc, client.MergeFrom(stored)); err != nil {
					errs[i] = client.IgnoreNotFound(err)
				}
			}
		}
	}
	return multierr.Combine(errs...)
}
