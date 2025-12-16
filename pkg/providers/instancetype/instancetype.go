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
	"github.com/patrickmn/go-cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"
	//	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1"
)

type NodeClass interface {
	client.Object
	//  AMIFamily() string
	//	AMIs() []v1.AMI
	//	BlockDeviceMappings() []*v1.BlockDeviceMapping
	//	CapacityReservations() []v1.CapacityReservation
	//	InstanceStorePolicy() *v1.InstanceStorePolicy
	//	KubeletConfiguration() *v1.KubeletConfiguration
	//	ZoneInfo() []v1.ZoneInfo
}

type Provider interface {
	Get(context.Context, NodeClass, linodego.Instance) (*cloudprovider.InstanceType, error)
	List(context.Context, NodeClass) ([]*cloudprovider.InstanceType, error)
}

type DefaultProvider struct {
	region        string
	recorder      events.Recorder
	client        *linodego.Client
	instanceCache *cache.Cache
}

func (d DefaultProvider) Get(ctx context.Context, class NodeClass, instance linodego.Instance) (*cloudprovider.InstanceType, error) {
	//TODO implement me
	panic("implement me")
}

func (d DefaultProvider) List(ctx context.Context, class NodeClass) ([]*cloudprovider.InstanceType, error) {
	//TODO implement me
	panic("implement me")
}

func NewDefaultProvider(
	region string,
	recorder events.Recorder,
	client *linodego.Client,
	instanceCache *cache.Cache,
) *DefaultProvider {

	return &DefaultProvider{
		region:        region,
		recorder:      recorder,
		client:        client,
		instanceCache: instanceCache,
	}
}
