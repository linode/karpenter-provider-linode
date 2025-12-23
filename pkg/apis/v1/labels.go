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

package v1

import (
	coreapis "sigs.k8s.io/karpenter/pkg/apis"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
)

func init() {
}

// Well known labels and resources
const (
	CapacityTypeDedicated = "dedicated"
	CapacityTypeStandard  = "standard"
)

var (
	AnnotationInstanceTagged             = apis.Group + "/tagged"
	NodeClaimTagKey                      = coreapis.Group + "/nodeclaim"
	NameTagKey                           = "Name"
	NodeClassTagKey                      = apis.Group + "/linodenodeclass"
	AnnotationLinodeNodeClassHash        = apis.Group + "/linodenodeclass-hash"
	AnnotationLinodeNodeClassHashVersion = apis.Group + "/linodenodeclass-hash-version"
	LabelInstanceNetworkBandwidth        = apis.Group + "/instance-network-bandwidth"
	LabelInstanceCPU                     = apis.Group + "/instance-cpu"
	LabelInstanceGPUCount                = apis.Group + "/instance-gpu-count"
	LabelInstanceMemory                  = apis.Group + "/instance-memory"
)
