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
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	coreapis "sigs.k8s.io/karpenter/pkg/apis"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/linode/karpenter-provider-linode/pkg/apis"
)

func init() {
	// GoLand users, expect your IDE to complain here:
	// https://youtrack.jetbrains.com/projects/GO/issues/GO-19359/Incorrect-Cannot-infer-T-error
	unused := []string{
		v1.LabelWindowsBuild,
		v1.LabelTopologyZone,
	}
	karpv1.RestrictedLabelDomains = karpv1.RestrictedLabelDomains.Insert(RestrictedLabelDomains...)
	karpv1.WellKnownLabels = karpv1.WellKnownLabels.Union(LinodeWellKnownLabels)
	karpv1.WellKnownLabels = karpv1.WellKnownLabels.Delete(unused...)

}

var (
	RestrictedLabelDomains = []string{
		apis.Group,
	}

	// LinodeWellKnownLabels are labels that belong to the RestrictedLabelDomains but allowed.
	// Karpenter is aware of these labels, and they can be used to further narrow down
	// the range of the corresponding values by either nodepool or pods.
	LinodeWellKnownLabels = sets.New(
		LabelInstanceCPU,
		LabelInstanceMemory,
		LabelInstanceNetworkBandwidth,
		LabelInstanceGPUCount,
	)

	TerminationFinalizer                 = apis.Group + "/termination"
	AnnotationInstanceTagged             = apis.Group + "/tagged"
	NodeClaimTagKey                      = coreapis.Group + "/nodeclaim"
	NameTagKey                           = "Name"
	LabelNodeClass                       = apis.Group + "/linodenodeclass"
	AnnotationLinodeNodeClassHash        = apis.Group + "/linodenodeclass-hash"
	AnnotationLinodeNodeClassHashVersion = apis.Group + "/linodenodeclass-hash-version"
	LabelInstanceNetworkBandwidth        = apis.Group + "/instance-network-bandwidth"
	LabelInstanceCPU                     = apis.Group + "/instance-cpu"
	LabelInstanceGPUCount                = apis.Group + "/instance-gpu-count"
	LabelInstanceMemory                  = apis.Group + "/instance-memory"
	LKEClusterNameTagKey                 = "lke-cluster-name"
	NodePoolTagKey                       = karpv1.NodePoolLabelKey
	NodeClassTagKey                      = LabelNodeClass

	// LKENodeClass labels and annotations
	LabelLKENodeClass                 = apis.Group + "/lkenodeclass"
	AnnotationLKENodeClassHash        = apis.Group + "/lkenodeclass-hash"
	AnnotationLKENodeClassHashVersion = apis.Group + "/lkenodeclass-hash-version"
)
