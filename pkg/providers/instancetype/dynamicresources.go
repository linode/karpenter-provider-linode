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

package instancetype

import (
	"context"
	"fmt"
	"unique"

	"github.com/linode/linodego/v2"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
)

// nvidiaGPUResource is the extended resource advertised by the NVIDIA device plugin.
const nvidiaGPUResource corev1.ResourceName = "nvidia.com/gpu"

// nvidiaGPUCount returns the number of nvidia.com/gpu units to advertise for a plan so pods
// requesting the extended resource can be scheduled before the device plugin registers it on
// a real node. It returns 0 when DRA scheduling is enabled (IGNORE_DRA_REQUESTS=false), because
// the NVIDIA DRA driver then owns the GPUs and the legacy device plugin must not.
func nvidiaGPUCount(ctx context.Context, info *linodego.LinodeType) int64 {
	if info.GPUs <= 0 || !coreoptions.FromContext(ctx).IgnoreDRARequests {
		return 0
	}
	return int64(info.GPUs)
}

// nvidiaGPUResources predicts one exclusive whole GPU per GPU the plan reports.
// Karpenter core ignores the prediction unless DRA scheduling is enabled
// (IGNORE_DRA_REQUESTS=false). Use NodePool requirements to limit the plans that
// run with the NVIDIA DRA driver; the GPU count alone does not establish support.
func nvidiaGPUResources(info *linodego.LinodeType) cloudprovider.DynamicResources {
	if info.GPUs <= 0 {
		return cloudprovider.DynamicResources{}
	}
	model := nvidiaGPUModel{attributes: nvidiaGPUTypeAttributes}
	for pattern, metadata := range nvidiaGPUMetadata {
		if pattern.MatchString(info.ID) {
			model = metadata
			break
		}
	}
	devices := make([]cloudprovider.Device, info.GPUs)
	for i := range devices {
		devices[i] = cloudprovider.Device{
			// These identities are local to scheduling simulation. The real driver
			// publishes its own node-specific pool and device names.
			Name:       unique.Make(fmt.Sprintf("gpu-%d", i)),
			Attributes: model.attributes,
			Capacity:   model.capacity,
		}
	}
	return cloudprovider.DynamicResources{
		ResourceSliceTemplates: []*cloudprovider.ResourceSliceTemplate{{
			Driver: unique.Make("gpu.nvidia.com"),
			// Core caches selector results by driver/pool/device across plans.
			// Include the plan ID so one plan cannot reuse another's match.
			Pool:    cloudprovider.ResourcePool{Name: unique.Make("gpus-" + info.ID)},
			Devices: devices,
		}},
	}
}
