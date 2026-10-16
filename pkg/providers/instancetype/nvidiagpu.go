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
