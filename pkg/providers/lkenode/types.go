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

package lkenode

import (
	"time"

	"github.com/linode/linodego"
)

// LKENode represents a single Linode node within an LKE NodePool.
type LKENode struct {
	// From linodego.LKENodePool
	PoolID int
	Type   string // Plan type (e.g., "g6-standard-2")
	Tags   []string
	Labels map[string]string
	Taints []linodego.LKENodePoolTaint

	// From linodego.LKENodePoolLinode
	InstanceID int
	NodeID     string
	Status     linodego.LKELinodeStatus

	// Derived / supplemental
	Region  string
	Created *time.Time
}

func NewLKENode(pool *linodego.LKENodePool, node linodego.LKENodePoolLinode, region string) *LKENode {
	return &LKENode{
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
