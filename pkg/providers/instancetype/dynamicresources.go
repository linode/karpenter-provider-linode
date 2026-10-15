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
	"fmt"
	"unique"

	"github.com/linode/linodego/v2"
	resourcev1 "k8s.io/api/resource/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// nvidiaGPUResources predicts one exclusive whole GPU per GPU the plan reports.
// Karpenter core ignores the prediction unless DRA scheduling is enabled
// (IGNORE_DRA_REQUESTS=false). Use NodePool requirements to limit the plans that
// run with the NVIDIA DRA driver; the GPU count alone does not establish support.
func nvidiaGPUResources(info *linodego.LinodeType) cloudprovider.DynamicResources {
	if info.GPUs <= 0 {
		return cloudprovider.DynamicResources{}
	}
	devices := make([]cloudprovider.Device, info.GPUs)
	for i := range devices {
		devices[i] = cloudprovider.Device{
			// These identities are local to scheduling simulation. The real driver
			// publishes its own node-specific pool and device names.
			Name: unique.Make(fmt.Sprintf("gpu-%d", i)),
			Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
				"type": {StringValue: new("gpu")},
			},
		}
	}
	return cloudprovider.DynamicResources{
		ResourceSliceTemplates: []*cloudprovider.ResourceSliceTemplate{{
			Driver:  unique.Make("gpu.nvidia.com"),
			Pool:    cloudprovider.ResourcePool{Name: unique.Make("gpus")},
			Devices: devices,
		}},
	}
}
