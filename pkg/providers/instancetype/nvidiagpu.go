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
	"regexp"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

var (
	nvidiaGPUTypeAttributes = map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
		"type": {StringValue: new("gpu")},
	}
	// Model identity and compute capability are fixed hardware properties; installed CUDA/driver versions are not predicted.
	nvidiaAda = nvidiaGPUModel{attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
		"type":                  {StringValue: new("gpu")},
		"productName":           {StringValue: new("NVIDIA RTX 4000 Ada Generation")},
		"brand":                 {StringValue: new("NvidiaRTX")},
		"architecture":          {StringValue: new("Ada Lovelace")},
		"cudaComputeCapability": {VersionValue: new("8.9.0")}, // sm_89; native compilation starts with CUDA 11.8.
	}, capacity: map[resourcev1.QualifiedName]resourcev1.DeviceCapacity{
		"memory": {Value: resource.MustParse("20475Mi")},
	}}
	nvidiaBlackwell = nvidiaGPUModel{attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
		"type":                  {StringValue: new("gpu")},
		"productName":           {StringValue: new("NVIDIA RTX PRO 6000 Blackwell Server Edition")},
		"brand":                 {StringValue: new("Nvidia")},
		"architecture":          {StringValue: new("Blackwell")},
		"cudaComputeCapability": {VersionValue: new("12.0.0")}, // sm_120; native compilation starts with CUDA 12.8.
	}, capacity: map[resourcev1.QualifiedName]resourcev1.DeviceCapacity{
		"memory": {Value: resource.MustParse("97887Mi")},
	}}
	// Match model families so new GPU counts and VM sizes need no catalog update.
	nvidiaGPUMetadata = map[*regexp.Regexp]nvidiaGPUModel{
		regexp.MustCompile(`^g2-gpu-rtx4000a[1-9]\d*-[a-z]+$`):       nvidiaAda,
		regexp.MustCompile(`^g3-gpu-rtxpro6000-blackwell-[1-9]\d*$`): nvidiaBlackwell,
	}
)

type nvidiaGPUModel struct {
	attributes map[resourcev1.QualifiedName]resourcev1.DeviceAttribute
	capacity   map[resourcev1.QualifiedName]resourcev1.DeviceCapacity
}
