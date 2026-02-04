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

package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/linode/linodego"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/karpenter/pkg/utils/atomic"
)

var (
	// TODO: get this list from a static file
	defaultLinodeTypeList = []linodego.LinodeType{
		{
			ID:                 "g6-standard-2",
			Class:              "standard",
			Label:              "Linode 4GB",
			Memory:             4096,
			VCPUs:              2,
			GPUs:               0,
			AcceleratedDevices: 0,
			Disk:               81920,
			Transfer:           1000,
			NetworkOut:         1000,
			Price:              &linodego.LinodePrice{Monthly: 20.0, Hourly: 0.03},
		},
		{
			ID:                 "g6-standard-4",
			Class:              "standard",
			Label:              "Linode 8GB",
			Memory:             8192,
			VCPUs:              4,
			GPUs:               0,
			AcceleratedDevices: 0,
			Disk:               163840,
			Transfer:           2000,
			NetworkOut:         2000,
			Price:              &linodego.LinodePrice{Monthly: 40.0, Hourly: 0.06},
		},
		{
			ID:                 "g6-dedicated-4",
			Class:              "standard",
			Label:              "Linode Dedicated 8GB",
			Memory:             8192,
			VCPUs:              4,
			GPUs:               0,
			AcceleratedDevices: 0,
			Disk:               163840,
			Transfer:           2000,
			NetworkOut:         2000,
			Price:              &linodego.LinodePrice{Monthly: 60.0, Hourly: 0.09},
		},
		{
			ID:                 "g1-gpu-rtx6000-4",
			Class:              "gpu",
			Label:              "Dedicated 128GB + RTX6000 GPU x4",
			Memory:             131072,
			VCPUs:              24,
			GPUs:               4,
			AcceleratedDevices: 0,
			Disk:               2621440,
			Transfer:           20000,
			NetworkOut:         10000,
			Price:              &linodego.LinodePrice{Monthly: 4000.0, Hourly: 6.0},
		},
		{
			ID:                 "g1-accelerated-netint-vpu-t1u2-s",
			Class:              "accelerated",
			Label:              "NETINT Quadra T1U x2 Small",
			Memory:             24576,
			VCPUs:              12,
			GPUs:               0,
			AcceleratedDevices: 2,
			Disk:               307200,
			Transfer:           0,
			NetworkOut:         16000,
			Price:              &linodego.LinodePrice{Monthly: 488.0, Hourly: 0.73},
		},
	}
)

type CapacityPool struct {
	InstanceType string
	Region       string
}

// LinodeAPIBehavior must be reset between tests otherwise tests will
// pollute each other.
type LinodeAPIBehavior struct {
	ListTypesOutput                 AtomicPtr[[]linodego.LinodeType]
	ListRegionsAvailabilityOutput   AtomicPtr[[]linodego.RegionAvailability]
	GetTypeBehavior                 MockedFunction[string, *linodego.LinodeType]
	ListRegionsAvailabilityBehavior MockedFunction[linodego.ListOptions, []linodego.RegionAvailability]
	CreateInstanceBehavior          MockedFunction[linodego.InstanceCreateOptions, *linodego.Instance]
	GetInstanceBehavior             MockedFunction[int, *linodego.Instance]
	DeleteInstanceBehavior          MockedFunction[int, error]
	ListInstancesBehavior           MockedFunction[linodego.ListOptions, []linodego.Instance]
	CreateTagsBehavior              MockedFunction[linodego.TagCreateOptions, linodego.Tag]
	ListTypesBehavior               MockedFunction[linodego.ListOptions, []linodego.LinodeType]
	Instances                       sync.Map
	InsufficientCapacityPools       atomic.Slice[CapacityPool]
	// NodePool storage and behaviors
	NodePools                 sync.Map // key: "clusterID-poolID", value: *linodego.LKENodePool
	CreateLKENodePoolBehavior MockedFunction[struct {
		ClusterID int
		Opts      linodego.LKENodePoolCreateOptions
	}, *linodego.LKENodePool]
	ListLKENodePoolsBehavior MockedFunction[int, []linodego.LKENodePool]
	GetLKENodePoolBehavior   MockedFunction[struct {
		ClusterName int
		PoolID      int
	}, *linodego.LKENodePool]
	UpdateLKENodePoolBehavior MockedFunction[struct {
		ClusterID int
		PoolID    int
		Opts      linodego.LKENodePoolUpdateOptions
	}, *linodego.LKENodePool]
	DeleteLKENodePoolBehavior MockedFunction[struct {
		ClusterName int
		PoolID      int
	}, error]
	DeleteLKENodePoolNodeBehavior MockedFunction[struct {
		ClusterID int
		NodeID    string
	}, error]
	UpdateInstanceBehavior MockedFunction[struct {
		LinodeID int
		Opts     linodego.InstanceUpdateOptions
	}, *linodego.Instance]
}

type LinodeClient struct {
	LinodeAPIBehavior
}

func (l *LinodeClient) GetType(_ context.Context, typeID string) (*linodego.LinodeType, error) {
	linodeType, err := l.GetTypeBehavior.Invoke(&typeID, func(typeID *string) (**linodego.LinodeType, error) {
		// Find the type in defaultLinodeTypeList
		for _, t := range defaultLinodeTypeList {
			if t.ID == *typeID {
				return ptr.To(&t), nil
			}
		}
		return nil, &linodego.Error{
			Code:    http.StatusNotFound,
			Message: fmt.Sprintf("no linode type found with id %s", *typeID),
		}
	})
	if linodeType == nil {
		return nil, err
	}
	return *linodeType, err
}

func (l *LinodeClient) ListRegionsAvailability(_ context.Context, _ *linodego.ListOptions) ([]linodego.RegionAvailability, error) {
	if !l.ListRegionsAvailabilityOutput.IsNil() {
		return *l.ListRegionsAvailabilityOutput.Clone(), nil
	}
	return MakeInstanceOfferings(defaultLinodeTypeList), nil
}

func (l *LinodeClient) GetInstance(_ context.Context, linodeID int) (*linodego.Instance, error) {
	instance, err := l.GetInstanceBehavior.Invoke(&linodeID, func(linodeID *int) (**linodego.Instance, error) {
		raw, ok := l.Instances.Load(*linodeID)
		if !ok {
			return nil, &linodego.Error{
				Code:    http.StatusNotFound,
				Message: fmt.Sprintf("instance does not exist with id %d", linodeID),
			}
		}
		instance := raw.(linodego.Instance)
		return ptr.To(&instance), nil
	})
	if instance == nil {
		return nil, err
	}
	return *instance, err
}

func NewLinodeClient() *LinodeClient {
	return &LinodeClient{}
}

func (l *LinodeClient) Reset() {
	l.ListTypesOutput.Reset()
	l.ListRegionsAvailabilityOutput.Reset()
	l.GetTypeBehavior.Reset()
	l.ListRegionsAvailabilityBehavior.Reset()
	l.CreateInstanceBehavior.Reset()
	l.GetInstanceBehavior.Reset()
	l.DeleteInstanceBehavior.Reset()
	l.ListInstancesBehavior.Reset()
	l.CreateTagsBehavior.Reset()
	l.ListTypesBehavior.Reset()
	l.Instances.Range(func(k, v any) bool {
		l.Instances.Delete(k)
		return true
	})
	l.InsufficientCapacityPools.Reset()

	l.NodePools.Range(func(k, v any) bool {
		l.NodePools.Delete(k)
		return true
	})
	l.CreateLKENodePoolBehavior.Reset()
	l.ListLKENodePoolsBehavior.Reset()
	l.GetLKENodePoolBehavior.Reset()
	l.UpdateLKENodePoolBehavior.Reset()
	l.DeleteLKENodePoolBehavior.Reset()
	l.DeleteLKENodePoolNodeBehavior.Reset()
	l.UpdateInstanceBehavior.Reset()
}

func (l *LinodeClient) CreateInstance(_ context.Context, opts linodego.InstanceCreateOptions) (*linodego.Instance, error) {
	instance, err := l.CreateInstanceBehavior.Invoke(&opts, func(opts *linodego.InstanceCreateOptions) (**linodego.Instance, error) {
		var icedPools []CapacityPool
		skipInstance := false
		l.InsufficientCapacityPools.Range(func(pool CapacityPool) bool {
			if pool.InstanceType == opts.Type &&
				pool.Region == opts.Region {
				icedPools = append(icedPools, pool)
				skipInstance = true
				return false
			}
			return true
		})
		if skipInstance {
			return nil, &linodego.Error{
				Code:    http.StatusBadRequest,
				Message: fmt.Sprintf("Insufficient capacity for instance type %s in region %s", opts.Type, opts.Region),
			}
		}
		// create and store the instance
		instance := linodego.Instance{
			ID:                  len(opts.Label) + 1, // just a simple way to generate an ID
			Image:               opts.Image,
			Label:               opts.Label,
			Type:                opts.Type,
			Region:              opts.Region,
			InterfaceGeneration: opts.InterfaceGeneration,
			Created:             ptr.To(time.Now()),
			Status:              linodego.InstanceRunning,
		}
		l.Instances.Store(instance.ID, instance)

		return ptr.To(ptr.To(instance)), nil
	})
	if instance == nil {
		return nil, err
	}
	return *instance, err
}

func (l *LinodeClient) ListInstances(_ context.Context, opts *linodego.ListOptions) ([]linodego.Instance, error) {
	instances, err := l.ListInstancesBehavior.Invoke(opts, func(opts *linodego.ListOptions) (*[]linodego.Instance, error) {
		var instances []linodego.Instance
		requiredTags := extractTagsFromFilter(opts)

		l.Instances.Range(func(k interface{}, v interface{}) bool {
			inst := v.(linodego.Instance)
			if !instanceMatchesTags(inst, requiredTags) {
				return true
			}
			instances = append(instances, inst)
			return true
		})
		return &instances, nil
	})
	if instances == nil {
		return nil, err
	}
	return *instances, err
}

type tagFilter struct {
	value    string
	contains bool
}

// extractTagsFromFilter parses the linodego X-Filter JSON carried in ListOptions.Filter.
//
// This fake only implements the "tags" shapes used by this repo:
//   - {"tags":"tag-a,tag-b"} => exact tag match (each comma-separated value must equal a tag string)
//   - {"tags":{"+contains":"substr"}} => contains match (an instance matches if any tag contains substr)
//
// If the filter is missing/unparseable or uses unsupported operators, it is treated as no tag filtering.
func extractTagsFromFilter(opts *linodego.ListOptions) []tagFilter {
	if opts == nil || opts.Filter == "" {
		return nil
	}
	var filterMap map[string]any
	if err := json.Unmarshal([]byte(opts.Filter), &filterMap); err != nil {
		return nil
	}
	tagsVal, ok := filterMap["tags"]
	if !ok {
		return nil
	}
	switch v := tagsVal.(type) {
	case string:
		var filters []tagFilter
		for _, tag := range strings.Split(v, ",") {
			if tag != "" {
				filters = append(filters, tagFilter{value: tag, contains: false})
			}
		}
		return filters
	case map[string]any:
		if containsVal, ok := v["+contains"]; ok {
			if s, ok := containsVal.(string); ok {
				return []tagFilter{{value: s, contains: true}}
			}
		}
	}
	return nil
}

// instanceMatchesTags applies the tag filters returned by extractTagsFromFilter.
// All filters must match (logical AND). For exact filters, a tag must equal the filter value.
// For contains filters, at least one tag must contain the filter value.
func instanceMatchesTags(inst linodego.Instance, filters []tagFilter) bool {
	for _, filter := range filters {
		found := false
		for _, instTag := range inst.Tags {
			if filter.contains {
				if strings.Contains(instTag, filter.value) {
					found = true
					break
				}
			} else {
				if instTag == filter.value {
					found = true
					break
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (l *LinodeClient) DeleteInstance(_ context.Context, linodeID int) error {
	_, err := l.DeleteInstanceBehavior.Invoke(&linodeID, func(linodeID *int) (*error, error) {
		l.Instances.LoadAndDelete(*linodeID)
		return nil, nil
	})
	return err
}

func (l *LinodeClient) CreateTag(_ context.Context, opts linodego.TagCreateOptions) (*linodego.Tag, error) {
	tag, err := l.CreateTagsBehavior.Invoke(&opts, func(opts *linodego.TagCreateOptions) (*linodego.Tag, error) {
		linodeIDs := opts.Linodes
		for _, linodeID := range linodeIDs {
			raw, ok := l.Instances.Load(linodeID)
			if !ok {
				return nil, &linodego.Error{
					Code:    http.StatusNotFound,
					Message: fmt.Sprintf("instance does not exist with id %d", linodeID),
				}
			}
			instance := raw.(linodego.Instance)
			instance.Tags = append(instance.Tags, opts.Label)
			l.Instances.Store(linodeID, instance)
		}
		return &linodego.Tag{
			Label: opts.Label,
		}, nil
	})
	return tag, err
}

func (l *LinodeClient) ListTypes(_ context.Context, _ *linodego.ListOptions) ([]linodego.LinodeType, error) {
	if !l.ListTypesOutput.IsNil() {
		return *l.ListTypesOutput.Clone(), nil
	}
	return MakeInstances(), nil
}

func (l *LinodeClient) CreateLKENodePool(_ context.Context, clusterID int, opts linodego.LKENodePoolCreateOptions) (*linodego.LKENodePool, error) {
	params := struct {
		ClusterID int
		Opts      linodego.LKENodePoolCreateOptions
	}{
		ClusterID: clusterID,
		Opts:      opts,
	}

	pool, err := l.CreateLKENodePoolBehavior.Invoke(&params, func(params *struct {
		ClusterID int
		Opts      linodego.LKENodePoolCreateOptions
	}) (**linodego.LKENodePool, error) {
		skipInstance := false
		l.InsufficientCapacityPools.Range(func(pool CapacityPool) bool {
			if pool.InstanceType == params.Opts.Type &&
				pool.Region == DefaultRegion {
				skipInstance = true
				return false
			}
			return true
		})
		if skipInstance {
			return nil, &linodego.Error{
				Code:    http.StatusBadRequest,
				Message: fmt.Sprintf("Insufficient capacity for instance type %s in region %s", params.Opts.Type, DefaultRegion),
			}
		}

		var poolCount int
		l.NodePools.Range(func(k, v any) bool {
			poolCount++
			return true
		})
		poolID := 100 + poolCount

		// Create instances based on the count field - We will just create 1 node per pool for now
		var nodes []linodego.LKENodePoolLinode
		for i := 0; i < params.Opts.Count; i++ {
			nodeID := fmt.Sprintf("instance-%d-%d", poolID, i)

			// Create and store the instance
			instance := linodego.Instance{
				ID:           1000 + poolID*10 + i, // Generate unique instance ID
				Label:        fmt.Sprintf("%s-%d", ptr.Deref(params.Opts.Label, "node"), i),
				Type:         params.Opts.Type,
				Region:       DefaultRegion,
				Status:       linodego.InstanceRunning,
				Tags:         params.Opts.Tags,
				LKEClusterID: params.ClusterID,
			}
			l.Instances.Store(instance.ID, instance)

			nodes = append(nodes, linodego.LKENodePoolLinode{
				ID:         nodeID,
				InstanceID: instance.ID,
				Status:     linodego.LKELinodeReady,
			})
		}

		newPool := &linodego.LKENodePool{
			ID:         poolID,
			Label:      params.Opts.Label,
			Type:       params.Opts.Type,
			Count:      params.Opts.Count,
			Tags:       params.Opts.Tags,
			Labels:     params.Opts.Labels,
			Taints:     params.Opts.Taints,
			FirewallID: params.Opts.FirewallID,
			Linodes:    nodes,
			// Set other fields as needed
		}

		l.NodePools.Store(fmt.Sprintf("%d-%d", params.ClusterID, poolID), newPool)

		return ptr.To(newPool), nil
	})

	if pool == nil {
		return nil, err
	}
	return *pool, err
}

func (l *LinodeClient) ListLKENodePools(_ context.Context, clusterID int, opts *linodego.ListOptions) ([]linodego.LKENodePool, error) {
	pools, err := l.ListLKENodePoolsBehavior.Invoke(&clusterID, func(clusterID *int) (*[]linodego.LKENodePool, error) {
		var poolList []linodego.LKENodePool
		l.NodePools.Range(func(k, v any) bool {
			key := k.(string)
			pool, ok := v.(*linodego.LKENodePool)
			if !ok {
				return true
			}
			// Extract clusterID from key
			var poolClusterName int
			if n, err := fmt.Sscanf(key, "%d-", &poolClusterName); n != 1 || err != nil {
				return true
			}
			if poolClusterName == *clusterID {
				poolList = append(poolList, *pool)
			}
			return true
		})
		return &poolList, nil
	})

	if pools == nil {
		return nil, err
	}
	return *pools, err
}

func (l *LinodeClient) GetLKENodePool(_ context.Context, clusterID, poolID int) (*linodego.LKENodePool, error) {
	params := struct {
		ClusterName int
		PoolID      int
	}{
		ClusterName: clusterID,
		PoolID:      poolID,
	}

	pool, err := l.GetLKENodePoolBehavior.Invoke(&params, func(params *struct {
		ClusterName int
		PoolID      int
	}) (**linodego.LKENodePool, error) {
		key := fmt.Sprintf("%d-%d", params.ClusterName, params.PoolID)
		raw, ok := l.NodePools.Load(key)
		if !ok {
			return nil, &linodego.Error{
				Code:    http.StatusNotFound,
				Message: fmt.Sprintf("node pool does not exist with id %d in cluster %d", params.PoolID, params.ClusterName),
			}
		}
		pool, ok := raw.(*linodego.LKENodePool)
		if !ok {
			return nil, &linodego.Error{
				Code:    http.StatusInternalServerError,
				Message: "unexpected node pool storage type",
			}
		}

		return ptr.To(pool), nil
	})

	if pool == nil {
		return nil, err
	}
	return *pool, err
}

// nolint:gocyclo // fix this later
func (l *LinodeClient) UpdateLKENodePool(_ context.Context, clusterID, poolID int, opts linodego.LKENodePoolUpdateOptions) (*linodego.LKENodePool, error) {
	params := struct {
		ClusterID int
		PoolID    int
		Opts      linodego.LKENodePoolUpdateOptions
	}{
		ClusterID: clusterID,
		PoolID:    poolID,
		Opts:      opts,
	}

	pool, err := l.UpdateLKENodePoolBehavior.Invoke(&params, func(params *struct {
		ClusterID int
		PoolID    int
		Opts      linodego.LKENodePoolUpdateOptions
	}) (**linodego.LKENodePool, error) {
		key := fmt.Sprintf("%d-%d", params.ClusterID, params.PoolID)
		raw, ok := l.NodePools.Load(key)
		if !ok {
			return nil, &linodego.Error{
				Code:    http.StatusNotFound,
				Message: fmt.Sprintf("node pool does not exist with id %d in cluster %d", params.PoolID, params.ClusterID),
			}
		}

		pool, ok := raw.(*linodego.LKENodePool)
		if !ok {
			return nil, &linodego.Error{
				Code:    http.StatusInternalServerError,
				Message: "unexpected node pool storage type",
			}
		}

		// Update fields if provided.
		// LKENodePoolUpdateOptions in the real API exposes a subset of fields compared
		// to the create options (for example, you can typically change count/size but
		// not immutable properties like region or type). In this fake implementation
		// we intentionally only model updates to Count (scaling up/down) and ignore
		// any other fields that might be present on the real update options.
		// Handle count changes (scaling up/down) - only if Count > 0 (explicitly set).
		// Rationale: tags-only updates omit Count (zero value). Without this guard we would
		// zero pool.Count and pool.Linodes during a tags update, effectively deleting nodes
		// in the fake and breaking list/get expectations.
		nodes := pool.Linodes
		if params.Opts.Count > 0 && params.Opts.Count > pool.Count {
			skipInstance := false
			l.InsufficientCapacityPools.Range(func(capacityPool CapacityPool) bool {
				if capacityPool.InstanceType == pool.Type &&
					capacityPool.Region == DefaultRegion {
					skipInstance = true
					return false
				}
				return true
			})
			if skipInstance {
				return nil, &linodego.Error{
					Code:    http.StatusBadRequest,
					Message: fmt.Sprintf("Insufficient capacity for instance type %s in region %s", pool.Type, DefaultRegion),
				}
			}

			// Scale up - add new instances
			for i := len(nodes); i < params.Opts.Count; i++ {
				nodeID := fmt.Sprintf("instance-%d-%d", params.PoolID, i)

				// Create and store the new instance
				instance := linodego.Instance{
					ID:           1000 + params.PoolID*10 + i,
					Label:        fmt.Sprintf("%s-%d", ptr.Deref(pool.Label, "node"), i),
					Type:         pool.Type,
					Region:       DefaultRegion,
					LKEClusterID: params.ClusterID,
					Tags:         append([]string{}, pool.Tags...),
					Status:       linodego.InstanceRunning,
				}
				l.Instances.Store(instance.ID, instance)

				nodes = append(nodes, linodego.LKENodePoolLinode{
					ID:         nodeID,
					InstanceID: instance.ID,
					Status:     linodego.LKELinodeReady,
				})
			}
			pool.Linodes = nodes
			pool.Count = params.Opts.Count
		} else if params.Opts.Count > 0 && params.Opts.Count < pool.Count {
			// Scale down - remove excess instances
			for i := params.Opts.Count; i < len(nodes); i++ {
				l.Instances.Delete(nodes[i].InstanceID)
			}
			pool.Linodes = nodes[:params.Opts.Count]
			pool.Count = params.Opts.Count
		}

		// Update other optional fields
		if params.Opts.Tags != nil {
			pool.Tags = *params.Opts.Tags
		}
		if params.Opts.Labels != nil {
			pool.Labels = *params.Opts.Labels
		}
		if params.Opts.Taints != nil {
			pool.Taints = *params.Opts.Taints
		}
		if params.Opts.Label != nil {
			pool.Label = params.Opts.Label
		}
		if params.Opts.Autoscaler != nil {
			pool.Autoscaler = *params.Opts.Autoscaler
		}
		if params.Opts.FirewallID != nil {
			pool.FirewallID = params.Opts.FirewallID
		}
		if params.Opts.K8sVersion != nil {
			pool.K8sVersion = params.Opts.K8sVersion
		}
		if params.Opts.UpdateStrategy != nil {
			pool.UpdateStrategy = params.Opts.UpdateStrategy
		}

		// Store updated pool
		l.NodePools.Store(key, pool)

		return ptr.To(pool), nil
	})

	if pool == nil {
		return nil, err
	}
	return *pool, err
}

func (l *LinodeClient) DeleteLKENodePool(_ context.Context, clusterID, poolID int) error {
	params := struct {
		ClusterName int
		PoolID      int
	}{
		ClusterName: clusterID,
		PoolID:      poolID,
	}

	_, err := l.DeleteLKENodePoolBehavior.Invoke(&params, func(params *struct {
		ClusterName int
		PoolID      int
	}) (*error, error) {
		key := fmt.Sprintf("%d-%d", params.ClusterName, params.PoolID)
		pool, ok := l.NodePools.Load(key)
		if !ok {
			return nil, &linodego.Error{
				Code:    http.StatusNotFound,
				Message: fmt.Sprintf("node pool does not exist with id %d in cluster %d", params.PoolID, params.ClusterName),
			}
		}
		for _, v := range pool.(*linodego.LKENodePool).Linodes {
			// Delete instances associated with this pool
			l.Instances.Delete(v.InstanceID)
		}
		l.NodePools.Delete(key)

		return nil, nil
	})

	return err
}

func (l *LinodeClient) ListLKEClusters(_ context.Context, _ *linodego.ListOptions) ([]linodego.LKECluster, error) {
	// For simplicity, return a canned response
	return []linodego.LKECluster{{ID: DefaultClusterID, Tier: string(DefaultClusterTier)}}, nil
}

func (l *LinodeClient) UpdateInstance(_ context.Context, linodeID int, opts linodego.InstanceUpdateOptions) (*linodego.Instance, error) {
	params := struct {
		LinodeID int
		Opts     linodego.InstanceUpdateOptions
	}{
		LinodeID: linodeID,
		Opts:     opts,
	}

	instance, err := l.UpdateInstanceBehavior.Invoke(&params, func(params *struct {
		LinodeID int
		Opts     linodego.InstanceUpdateOptions
	}) (**linodego.Instance, error) {
		raw, ok := l.Instances.Load(params.LinodeID)
		if !ok {
			return nil, &linodego.Error{
				Code:    http.StatusNotFound,
				Message: fmt.Sprintf("instance does not exist with id %d", params.LinodeID),
			}
		}
		instance := raw.(linodego.Instance)

		if params.Opts.Label != "" {
			instance.Label = params.Opts.Label
		}
		if params.Opts.Tags != nil {
			instance.Tags = *params.Opts.Tags
		}
		l.Instances.Store(params.LinodeID, instance)

		return ptr.To(&instance), nil
	})

	if instance == nil {
		return nil, err
	}
	return *instance, err
}

func (l *LinodeClient) DeleteLKENodePoolNode(_ context.Context, clusterID int, nodeID string) error {
	params := struct {
		ClusterID int
		NodeID    string
	}{
		ClusterID: clusterID,
		NodeID:    nodeID,
	}

	_, err := l.DeleteLKENodePoolNodeBehavior.Invoke(&params, func(params *struct {
		ClusterID int
		NodeID    string
	}) (*error, error) {
		var foundPoolKey string
		var foundPool *linodego.LKENodePool
		var nodeIndex = -1
		var nodeInstanceID int

		l.NodePools.Range(func(k, v any) bool {
			key := k.(string)
			pool, ok := v.(*linodego.LKENodePool)
			if !ok {
				return true
			}
			var poolClusterID int
			if n, err := fmt.Sscanf(key, "%d-", &poolClusterID); n != 1 || err != nil {
				return true
			}
			if poolClusterID != params.ClusterID {
				return true
			}

			for i, node := range pool.Linodes {
				if node.ID == params.NodeID {
					foundPoolKey = key
					foundPool = pool
					nodeIndex = i
					nodeInstanceID = node.InstanceID
					return false
				}
			}
			return true
		})

		if foundPool == nil || nodeIndex == -1 {
			return nil, &linodego.Error{
				Code:    http.StatusNotFound,
				Message: fmt.Sprintf("node %s not found in cluster %d", params.NodeID, params.ClusterID),
			}
		}

		l.Instances.Delete(nodeInstanceID)

		foundPool.Linodes = append(foundPool.Linodes[:nodeIndex], foundPool.Linodes[nodeIndex+1:]...)
		foundPool.Count = len(foundPool.Linodes)

		if len(foundPool.Linodes) == 0 {
			l.NodePools.Delete(foundPoolKey)
		}

		return nil, nil
	})

	return err
}
