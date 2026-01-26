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

package main

import (
	"os"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/metrics"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/overlay"
	corecontrollers "sigs.k8s.io/karpenter/pkg/controllers"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	coreoperator "sigs.k8s.io/karpenter/pkg/operator"

	"github.com/linode/karpenter-provider-linode/pkg/cloudprovider"
	"github.com/linode/karpenter-provider-linode/pkg/controllers"
	"github.com/linode/karpenter-provider-linode/pkg/operator"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
)

func main() {
	coreCtx, coreOp := coreoperator.NewOperator()
	ctx, op, err := operator.NewOperator(coreCtx, coreOp, nil)
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to create Linode operator")
		os.Exit(1)
	}

	linodeCloudProvider := cloudprovider.New(
		op.InstanceTypesProvider,
		op.NodeProvider,
		op.EventRecorder,
		op.GetClient(),
	)
	overlayUndecoratedCloudProvider := metrics.Decorate(linodeCloudProvider)
	cloudProvider := overlay.Decorate(overlayUndecoratedCloudProvider, op.GetClient(), op.InstanceTypeStore)
	clusterState := state.NewCluster(op.Clock, op.GetClient(), cloudProvider)

	op.
		WithControllers(ctx, corecontrollers.NewControllers(
			ctx,
			op.Manager,
			op.Clock,
			op.GetClient(),
			op.EventRecorder,
			cloudProvider,
			overlayUndecoratedCloudProvider,
			clusterState,
			op.InstanceTypeStore,
		)...).
		WithControllers(ctx, controllers.NewControllers(
			ctx,
			options.FromContext(ctx).ClusterRegion,
			op.Manager,
			op.LinodeClient,
			op.GetClient(),
			op.EventRecorder,
			op.ValidationCache,
			cloudProvider,
			op.NodeProvider,
			op.InstanceTypesProvider,
		)...).
		Start(ctx)
}
