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
	"context"
	"os"
	"strconv"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/metrics"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/overlay"
	corecontrollers "sigs.k8s.io/karpenter/pkg/controllers"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	coreoperator "sigs.k8s.io/karpenter/pkg/operator"

	"github.com/linode/karpenter-provider-linode/pkg/cloudprovider"
	"github.com/linode/karpenter-provider-linode/pkg/controllers"
	sdk "github.com/linode/karpenter-provider-linode/pkg/linode"
	"github.com/linode/karpenter-provider-linode/pkg/operator"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
)

func main() {
	ctx1, op1 := coreoperator.NewOperator()
	linodeClientConfig := validateEnvironment(ctx1)
	ctx, op := operator.NewOperator(ctx1, op1, linodeClientConfig)

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

func validateEnvironment(ctx context.Context) (linodeConfig sdk.ClientConfig) {
	linodeToken := os.Getenv("LINODE_TOKEN")
	if linodeToken == "" {
		log.FromContext(ctx).Error(nil, "LINODE_TOKEN environment variable is not set, cannot start provider")
		os.Exit(1)
	}
	linodeConfig = sdk.ClientConfig{Token: linodeToken}
	if raw, ok := os.LookupEnv("LINODE_CLIENT_TIMEOUT"); ok {
		if timeout, err := strconv.Atoi(raw); timeout > 0 && err == nil {
			linodeConfig.Timeout = time.Duration(timeout) * time.Second
		} else {
			log.FromContext(ctx).Info("LINODE_CLIENT_TIMEOUT environment variable is invalid, using default timeout")
		}
	}
	return linodeConfig
}
