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

package cloudprovider

import (
	"context"

	"github.com/samber/lo"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	"github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
)

const (
	NodeClassDrift cloudprovider.DriftReason = "NodeClassDrift"
)

func (c *CloudProvider) isNodeClassDrifted(_ context.Context, nodeClaim *karpv1.NodeClaim, _ *karpv1.NodePool, nodeClass *v1alpha1.LinodeNodeClass) cloudprovider.DriftReason {
	return c.areStaticFieldsDrifted(nodeClaim, nodeClass)
}

func (c *CloudProvider) areStaticFieldsDrifted(nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.LinodeNodeClass) cloudprovider.DriftReason {
	nodeClassHash, foundNodeClassHash := nodeClass.Annotations[v1alpha1.AnnotationLinodeNodeClassHash]
	nodeClassHashVersion, foundNodeClassHashVersion := nodeClass.Annotations[v1alpha1.AnnotationLinodeNodeClassHashVersion]
	nodeClaimHash, foundNodeClaimHash := nodeClaim.Annotations[v1alpha1.AnnotationLinodeNodeClassHash]
	nodeClaimHashVersion, foundNodeClaimHashVersion := nodeClaim.Annotations[v1alpha1.AnnotationLinodeNodeClassHashVersion]

	if !foundNodeClassHash || !foundNodeClaimHash || !foundNodeClassHashVersion || !foundNodeClaimHashVersion {
		return ""
	}
	// validate that the hash version for the LinodeNodeClass is the same as the NodeClaim before evaluating for static drift
	if nodeClassHashVersion != nodeClaimHashVersion {
		return ""
	}
	return lo.Ternary(nodeClassHash != nodeClaimHash, NodeClassDrift, "")
}
