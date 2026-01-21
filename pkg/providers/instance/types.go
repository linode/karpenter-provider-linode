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

package instance

import (
	"context"
	"time"

	"github.com/linode/linodego"
)

// Instance is an internal data representation of a linode.Instance
// It contains all the common data that is needed to inject into the Machine from this responses
type Instance struct {
	ID                  int
	Created             *time.Time
	Region              string
	Image               string
	Group               string
	Label               string
	Type                string
	Status              linodego.InstanceStatus
	WatchdogEnabled     bool
	Tags                []string
	Labels              map[string]string
	Taints              []linodego.LKENodePoolTaint
	PoolID              int
	NodeID              string
	LKEStatus           linodego.LKELinodeStatus
	PlacementGroup      *linodego.InstancePlacementGroup
	DiskEncryption      linodego.InstanceDiskEncryption
	InterfaceGeneration linodego.InterfaceGeneration
}

func NewInstance(ctx context.Context, instance linodego.Instance) *Instance {
	return &Instance{
		ID:              instance.ID,
		Created:         instance.Created,
		Region:          instance.Region,
		Image:           instance.Image,
		Group:           instance.Group,
		Label:           instance.Label,
		Type:            instance.Type,
		Status:          instance.Status,
		WatchdogEnabled: instance.WatchdogEnabled,
		Tags:            instance.Tags,
		PlacementGroup:  instance.PlacementGroup,

		// NOTE: Disk encryption may not currently be available to all users.
		DiskEncryption: instance.DiskEncryption,
		// Note: Linode interfaces may not currently be available to all users.
		InterfaceGeneration: instance.InterfaceGeneration,
	}
}

// NewLKEInstance returns an Instance representing an LKE node pool linode.
func NewLKEInstance(pool *linodego.LKENodePool, node linodego.LKENodePoolLinode, region string) *Instance {
	// Use current time as Created since LKE API doesn't return node creation time.
	// This is important for GC's 30-second grace period to work correctly.
	now := time.Now()
	return &Instance{
		ID:        node.InstanceID,
		Created:   &now,
		Region:    region,
		Type:      pool.Type,
		Tags:      pool.Tags,
		Labels:    pool.Labels,
		Taints:    pool.Taints,
		PoolID:    pool.ID,
		NodeID:    node.ID,
		LKEStatus: node.Status,
	}
}
