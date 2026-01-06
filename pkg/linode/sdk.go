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

package sdk

import (
	"context"

	"github.com/linode/linodego"
)

type LinodeAPI interface {
	ListTypes(ctx context.Context, opts *linodego.ListOptions) ([]linodego.LinodeType, error)
	GetType(ctx context.Context, typeID string) (*linodego.LinodeType, error)
	// ListRegionsAvailability returns availability of plan types AND the prices for each, so we shouldn't need a PricingAPI interface
	ListRegionsAvailability(ctx context.Context, opts *linodego.ListOptions) ([]linodego.RegionAvailability, error)
	GetInstance(ctx context.Context, linodeID int) (*linodego.Instance, error)
	DeleteInstance(ctx context.Context, linodeID int) error
	ListInstances(ctx context.Context, opts *linodego.ListOptions) ([]linodego.Instance, error)
	CreateTag(ctx context.Context, opts linodego.TagCreateOptions) (*linodego.Tag, error)
	CreateInstance(ctx context.Context, opts linodego.InstanceCreateOptions) (*linodego.Instance, error)

	// NodePool methods for LKE cluster management
	CreateLKENodePool(ctx context.Context, clusterID int, opts linodego.LKENodePoolCreateOptions) (*linodego.LKENodePool, error)
	ListLKENodePools(ctx context.Context, clusterID int, opts *linodego.ListOptions) ([]linodego.LKENodePool, error)
	GetLKENodePool(ctx context.Context, clusterID, poolID int) (*linodego.LKENodePool, error)
	UpdateLKENodePool(ctx context.Context, clusterID, poolID int, opts linodego.LKENodePoolUpdateOptions) (*linodego.LKENodePool, error)
	DeleteLKENodePool(ctx context.Context, clusterID, poolID int) error
	GetLKENodePoolNode(ctx context.Context, clusterID int, nodeID string) (*linodego.LKENodePoolLinode, error)
}
