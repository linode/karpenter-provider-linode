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

package metrics

import (
	"context"
	"time"

	"github.com/awslabs/operatorpkg/reconciler"
	"github.com/awslabs/operatorpkg/singleton"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
)

type metricDimensions struct {
	instanceType string
	capacityType string
	region       string
}

type Controller struct {
	kubeClient    client.Client
	cloudProvider cloudprovider.CloudProvider
}

func NewController(kubeClient client.Client, cloudProvider cloudprovider.CloudProvider) *Controller {
	return &Controller{
		kubeClient:    kubeClient,
		cloudProvider: cloudProvider,
	}
}

func (c *Controller) Reconcile(ctx context.Context) (reconciler.Result, error) {
	nodePools := &karpv1.NodePoolList{}
	if err := c.kubeClient.List(ctx, nodePools); err != nil {
		return reconciler.Result{}, err
	}
	availability := map[metricDimensions]bool{}
	price := map[metricDimensions]float64{}
	for _, nodePool := range nodePools.Items {
		instanceTypes, err := c.cloudProvider.GetInstanceTypes(ctx, &nodePool)
		if err != nil {
			return reconciler.Result{}, err
		}
		for _, instanceType := range instanceTypes {
			regions := sets.New[string]()
			for _, offering := range instanceType.Offerings {
				dimensions := metricDimensions{instanceType: instanceType.Name, capacityType: offering.CapacityType(), region: offering.Requirements.Get(corev1.LabelTopologyRegion).Any()}
				availability[dimensions] = availability[dimensions] || offering.Available
				price[dimensions] = offering.Price
				regions.Insert(offering.Requirements.Get(corev1.LabelTopologyRegion).Any())
			}
			if coreoptions.FromContext(ctx).FeatureGates.ReservedCapacity {
				for region := range regions {
					dimensions := metricDimensions{instanceType: instanceType.Name, capacityType: karpv1.CapacityTypeReserved, region: region}
					if _, ok := availability[dimensions]; !ok {
						availability[dimensions] = false
						price[dimensions] = 0
					}
				}
			}
		}
	}

	for dimensions, available := range availability {
		InstanceTypeOfferingAvailable.Set(float64(lo.Ternary(available, 1, 0)), map[string]string{
			instanceTypeLabel: dimensions.instanceType,
			capacityTypeLabel: dimensions.capacityType,
			regionLabel:       dimensions.region,
		})
	}
	for dimensions, p := range price {
		InstanceTypeOfferingPriceEstimate.Set(p, map[string]string{
			instanceTypeLabel: dimensions.instanceType,
			capacityTypeLabel: dimensions.capacityType,
			regionLabel:       dimensions.region,
		})
	}
	return reconciler.Result{RequeueAfter: time.Minute}, nil
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("cloudprovider.metrics").
		WatchesRawSource(singleton.Source()).
		Complete(singleton.AsReconciler(c))
}
