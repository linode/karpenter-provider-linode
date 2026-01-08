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
	"time"

	"github.com/linode/linodego"
)

// LKENodePool represents a single Linode node within an LKE NodePool.
type LKENodePool struct {
	PoolID     int
	InstanceID int // From LKENodePoolLinode.InstanceID
	NodeID     string
	Type       string // Plan type (e.g., "g6-standard-2")
	Region     string
	Status     linodego.LKELinodeStatus
	Tags       []string                    // Linode tags applied at the pool level
	Labels     map[string]string           // Kubernetes node labels from LKENodePool.Labels
	Taints     []linodego.LKENodePoolTaint // Kubernetes taints from LKENodePool.Taints
	Created    *time.Time
}

func NewLKENodePool(pool *linodego.LKENodePool, node linodego.LKENodePoolLinode, region string) *LKENodePool {
	return &LKENodePool{
		PoolID:     pool.ID,
		InstanceID: node.InstanceID,
		NodeID:     node.ID,
		Type:       pool.Type,
		Region:     region,
		Status:     node.Status,
		Tags:       pool.Tags,
		Labels:     pool.Labels,
		Taints:     pool.Taints,
	}
}
