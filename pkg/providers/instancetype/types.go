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

	"github.com/linode/linodego"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

type Resolver interface {
	// CacheKey tells the InstanceType cache if something changes about the InstanceTypes or Offerings based on the NodeClass.
	CacheKey(NodeClass) string
	// Resolve generates an InstanceType based on raw LinodeType and NodeClass setting data
	Resolve(ctx context.Context, info linodego.LinodeType, zones []string, nodeClass NodeClass) *cloudprovider.InstanceType
}

type DefaultResolver struct {
	region string
}

func (d DefaultResolver) CacheKey(class NodeClass) string {
	//TODO implement me
	panic("implement me")
}

func (d DefaultResolver) Resolve(ctx context.Context, info linodego.LinodeType, zones []string, nodeClass NodeClass) *cloudprovider.InstanceType {
	//TODO implement me
	panic("implement me")
}

func NewDefaultResolver(region string) *DefaultResolver {
	return &DefaultResolver{
		region: region,
	}
}
