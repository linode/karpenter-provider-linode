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

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
)

// TODO: Implement drift detection
func (c *CloudProvider) isNodeClassDrifted(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodePool *karpv1.NodePool, nodeClass *v1.LinodeNodeClass) (cloudprovider.DriftReason, error) {
	return "", nil
}
