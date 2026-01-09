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

package nodepool

import (
	"context"

	"github.com/awslabs/operatorpkg/option"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
)

// Provider defines the Linode LKE NodePool provider contract. This mirrors the
// instance Provider interface but targets NodePool-backed provisioning.
type Provider interface {
	Create(ctx context.Context, nodeClass *v1.LinodeNodeClass, nodeClaim *karpv1.NodeClaim, tags map[string]string, instanceTypes []*cloudprovider.InstanceType) (*LKENodePool, error)
	Get(ctx context.Context, providerID string, opts ...Options) (*LKENodePool, error)
	List(ctx context.Context) ([]*LKENodePool, error)
	Delete(ctx context.Context, providerID string) error
	UpdateTags(ctx context.Context, poolID int, tags map[string]string) error
}

// options captures request-scoped behaviors (e.g., bypass cache).
type options struct {
	SkipCache bool
}

// Options is the functional option type for provider requests.
type Options = option.Function[options]

// SkipCache bypasses any cached NodePoolInstance state.
var SkipCache = func(opts *options) {
	opts.SkipCache = true
}
