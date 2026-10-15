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
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Ada metadata was observed in a live DRA 0.5.0 ResourceSlice for
// g2-gpu-rtx4000a1-s (commit 90b3a5917f0cbeb73a3c669e1e63690f89d0f437,
// host driver 615.71.09, GPU Operator 26.7.1). Other Ada plan IDs below are
// documented plans with the same-model mapping assumption; their counts come
// from the public Linode type API, not separate live DRA captures.
// Blackwell attributes and per-GPU memory are based on full nvidia-smi records
// from driver 580.65.06 and 580.95.05 at the NVIDIA forum URLs below. The
// `Nvidia` brand is inferred from ProductBrandNVIDIA. These observations had
// ECC enabled and MIG disabled. Akamai plan IDs are documented at the onboarding
// guide and quickstart; their DRA capacity is not live-verified.
// https://forums.developer.nvidia.com/t/rtx-6000-pro-server-power-limit-of-450w-instead-of-expected-600w/345003
// https://forums.developer.nvidia.com/t/ecc-errors-on-nvidia-rtx-pro6000/371504
// https://techdocs.akamai.com/cloud-computing/docs/nvidia-rtx-pro-6000-blackwell-gpu-onboarding
// https://github.com/akamai-developers/kuberay-gpu-llm-quickstart
// These maps are shared by every device template and must remain immutable.
var (
	nvidiaGPUTypeAttributes = map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
		"type": {StringValue: new("gpu")},
	}
	nvidiaAda = nvidiaGPUModel{attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
		"type":                  {StringValue: new("gpu")},
		"productName":           {StringValue: new("NVIDIA RTX 4000 Ada Generation")},
		"brand":                 {StringValue: new("NvidiaRTX")},
		"architecture":          {StringValue: new("Ada Lovelace")},
		"cudaComputeCapability": {VersionValue: new("8.9.0")},
	}, capacity: map[resourcev1.QualifiedName]resourcev1.DeviceCapacity{
		"memory": {Value: resource.MustParse("20475Mi")},
	}}
	nvidiaBlackwell = nvidiaGPUModel{attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
		"type":                  {StringValue: new("gpu")},
		"productName":           {StringValue: new("NVIDIA RTX PRO 6000 Blackwell Server Edition")},
		"brand":                 {StringValue: new("Nvidia")},
		"architecture":          {StringValue: new("Blackwell")},
		"cudaComputeCapability": {VersionValue: new("12.0.0")},
	}, capacity: map[resourcev1.QualifiedName]resourcev1.DeviceCapacity{
		"memory": {Value: resource.MustParse("97887Mi")},
	}}
	nvidiaGPUMetadata = map[string]nvidiaGPUModel{
		"g2-gpu-rtx4000a1-s":            nvidiaAda,
		"g2-gpu-rtx4000a1-m":            nvidiaAda,
		"g2-gpu-rtx4000a1-l":            nvidiaAda,
		"g2-gpu-rtx4000a1-xl":           nvidiaAda,
		"g2-gpu-rtx4000a2-s":            nvidiaAda,
		"g2-gpu-rtx4000a2-m":            nvidiaAda,
		"g2-gpu-rtx4000a2-hs":           nvidiaAda,
		"g2-gpu-rtx4000a4-s":            nvidiaAda,
		"g2-gpu-rtx4000a4-m":            nvidiaAda,
		"g3-gpu-rtxpro6000-blackwell-1": nvidiaBlackwell,
		"g3-gpu-rtxpro6000-blackwell-2": nvidiaBlackwell,
		"g3-gpu-rtxpro6000-blackwell-4": nvidiaBlackwell,
	}
)

type nvidiaGPUModel struct {
	attributes map[resourcev1.QualifiedName]resourcev1.DeviceAttribute
	capacity   map[resourcev1.QualifiedName]resourcev1.DeviceCapacity
}
