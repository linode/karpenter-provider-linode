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
	"github.com/awslabs/operatorpkg/controller"
	"github.com/awslabs/operatorpkg/status"
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
	"github.com/linode/karpenter-provider-linode/pkg/providers/instance"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instancetype"
)

func NewControllers(
	region string,
	mgr manager.Manager,
	kubeClient client.Client,
	recorder events.Recorder,
	cloudProvider cloudprovider.CloudProvider,
	instanceProvider instance.Provider,
	instanceTypeProvider *instancetype.DefaultProvider,
) []controller.Controller {
	controllers := []controller.Controller{
		nodeclass.NewController(
			kubeClient,
			recorder,
			region,
		),
		nodeclaimgarbagecollection.NewController(kubeClient, cloudProvider),
		nodeclaimtagging.NewController(kubeClient, cloudProvider, instanceProvider),
		controllersinstancetype.NewController(instanceTypeProvider),
		status.NewController[*v1.LinodeNodeClass](kubeClient, mgr.GetEventRecorderFor("karpenter"), status.EmitDeprecatedMetrics),
		metrics.NewController(kubeClient, cloudProvider),
	}
	return controllers
}
