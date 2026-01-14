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

package controllers

import (
	"context"

	"github.com/awslabs/operatorpkg/controller"
	"github.com/awslabs/operatorpkg/status"
	"github.com/patrickmn/go-cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
	"github.com/linode/karpenter-provider-linode/pkg/controllers/metrics"
	nodeclaimgarbagecollection "github.com/linode/karpenter-provider-linode/pkg/controllers/nodeclaim/garbagecollection"
	nodeclaimtagging "github.com/linode/karpenter-provider-linode/pkg/controllers/nodeclaim/tagging"
	"github.com/linode/karpenter-provider-linode/pkg/controllers/nodeclass"
	controllersinstancetype "github.com/linode/karpenter-provider-linode/pkg/controllers/providers/instancetype"
	controllersinstancetypecapacity "github.com/linode/karpenter-provider-linode/pkg/controllers/providers/instancetype/capacity"
	sdk "github.com/linode/karpenter-provider-linode/pkg/linode"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instance"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instancetype"
	"github.com/linode/karpenter-provider-linode/pkg/providers/lkenode"
)

func NewControllers(
	ctx context.Context,
	region string,
	mgr manager.Manager,
	linodeClient sdk.LinodeAPI,
	kubeClient client.Client,
	recorder events.Recorder,
	validationCache *cache.Cache,
	cloudProvider cloudprovider.CloudProvider,
	instanceProvider instance.Provider,
	lkenodeProvider lkenode.Provider,
	instanceTypeProvider *instancetype.DefaultProvider,
) []controller.Controller {
	controllers := []controller.Controller{
		nodeclass.NewController(
			kubeClient,
			cloudProvider,
			recorder,
			region,
			instanceTypeProvider,
			linodeClient,
			validationCache,
			options.FromContext(ctx).DisableDryRun,
		),
		nodeclaimgarbagecollection.NewController(kubeClient, cloudProvider),
		nodeclaimtagging.NewController(kubeClient, cloudProvider, instanceProvider, lkenodeProvider),
		controllersinstancetype.NewController(instanceTypeProvider),
		controllersinstancetypecapacity.NewController(kubeClient, cloudProvider, instanceTypeProvider),
		status.NewController[*v1.LinodeNodeClass](kubeClient, mgr.GetEventRecorderFor("karpenter"), status.EmitDeprecatedMetrics),
		metrics.NewController(kubeClient, cloudProvider),
	}
	return controllers
}
