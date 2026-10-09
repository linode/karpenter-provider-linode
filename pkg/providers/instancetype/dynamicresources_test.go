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

package instancetype_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"unique"

	"github.com/linode/linodego/v2"
	"github.com/patrickmn/go-cache"
	"github.com/samber/lo"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/scheduling/dynamicresources"
	coretest "sigs.k8s.io/karpenter/pkg/test"

	v1 "github.com/linode/karpenter-provider-linode/pkg/apis/v1alpha1"
	linodecache "github.com/linode/karpenter-provider-linode/pkg/cache"
	"github.com/linode/karpenter-provider-linode/pkg/fake"
	"github.com/linode/karpenter-provider-linode/pkg/operator/options"
	"github.com/linode/karpenter-provider-linode/pkg/providers/instancetype"
	"github.com/linode/karpenter-provider-linode/pkg/test"
)

const draGPUInstanceType = "test-gpu-plan"

// These tests use an in-memory catalog fake only (no envtest). Run with -run '^TestDRA'.
func TestDRAInventory(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                    string
		gpus, accelerated, want int
	}{
		{name: "GPU plan", gpus: 8, want: 8},
		{name: "separate accelerator count", gpus: 2, accelerated: 4, want: 2},
		{name: "accelerators only", accelerated: 4},
		{name: "CPU only"},
		{name: "invalid negative GPU count", gpus: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			info := draType(draGPUInstanceType, tc.gpus)
			info.AcceleratedDevices = tc.accelerated
			it := instancetype.NewDefaultResolver(fake.DefaultRegion).Resolve(draContext(t), &info, draNodeClass())
			assertGPUs(t, it, tc.want)
		})
	}
}

func TestDRAListAndGet(t *testing.T) {
	t.Parallel()
	ctx := draContext(t)
	types := []linodego.LinodeType{draType(draGPUInstanceType, 2), draType("test-cpu-plan", 0)}
	offerings := fake.MakeInstanceOfferings(types)
	api := fake.NewLinodeClient()
	api.ListTypesOutput.Set(&types)
	api.GetRegionAvailabilityOutput.Set(&offerings)
	provider := instancetype.NewDefaultProvider(api, instancetype.NewDefaultResolver(fake.DefaultRegion),
		cache.New(cache.NoExpiration, 0), cache.New(cache.NoExpiration, 0), cache.New(cache.NoExpiration, 0), linodecache.NewUnavailableOfferings())
	if err := provider.UpdateInstanceTypes(ctx); err != nil {
		t.Fatal(err)
	}
	if err := provider.UpdateInstanceTypeOfferings(ctx); err != nil {
		t.Fatal(err)
	}
	nodeClass := draNodeClass()
	// List returns copies built by InjectOfferings, which must keep DynamicResources.
	listed, err := provider.List(ctx, nodeClass)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected two types, got %d", len(listed))
	}
	for _, it := range listed {
		assertGPUs(t, it, lo.Ternary(it.Name == draGPUInstanceType, 2, 0))
	}
	// A refreshed API count invalidates the cached prediction.
	types[0].GPUs = 1
	api.ListTypesOutput.Set(&types)
	if err := provider.UpdateInstanceTypes(ctx); err != nil {
		t.Fatal(err)
	}
	refreshed, err := provider.Get(ctx, nodeClass, draGPUInstanceType)
	if err != nil {
		t.Fatal(err)
	}
	assertGPUs(t, refreshed, 1)
}

// assertGPUs expects exactly n exclusive whole GPUs in one pool, with no
// capacity, sharing, or topology.
func assertGPUs(t *testing.T, it *cloudprovider.InstanceType, n int) {
	t.Helper()
	want := cloudprovider.DynamicResources{}
	if n > 0 {
		devices := make([]cloudprovider.Device, n)
		for i := range devices {
			devices[i] = cloudprovider.Device{
				Name:       unique.Make(fmt.Sprintf("gpu-%d", i)),
				Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{"type": {StringValue: new("gpu")}},
			}
		}
		want.ResourceSliceTemplates = []*cloudprovider.ResourceSliceTemplate{{
			Driver:  unique.Make("gpu.nvidia.com"),
			Pool:    cloudprovider.ResourcePool{Name: unique.Make("gpus")},
			Devices: devices,
		}}
	}
	if !reflect.DeepEqual(it.DynamicResources, want) {
		t.Fatalf("unexpected DRA inventory for %s: %+v", it.Name, it.DynamicResources)
	}
}

func draContext(t *testing.T) context.Context {
	t.Helper()
	return options.ToContext(t.Context(), test.Options())
}

func draNodeClass() *v1.LinodeNodeClass {
	return test.LinodeNodeClass(v1.LinodeNodeClass{ObjectMeta: metav1.ObjectMeta{Name: "test"}})
}

func draType(name string, gpus int) linodego.LinodeType {
	return linodego.LinodeType{ID: name, GPUs: gpus, Memory: 32768, VCPUs: 8}
}

const nvidiaWholeGPUSelector = "device.driver == 'gpu.nvidia.com' && device.attributes['gpu.nvidia.com'].type == 'gpu'"

// Exercise the pinned core's actual allocation/CEL contract using a fake
// DeviceClass client and in-memory NodeClaims. No API server is started.
func TestDRAWholeGPUAllocation(t *testing.T) {
	t.Parallel()
	ctx := draContext(t)
	info := draType(draGPUInstanceType, 2)
	it := instancetype.NewDefaultResolver(fake.DefaultRegion).Resolve(ctx, &info, draNodeClass())
	allocator := draAllocator(nvidiaWholeGPUSelector)
	nodeA := &draNodeClaimMock{name: "node-a", instanceType: it}
	nodeB := &draNodeClaimMock{name: "node-b", instanceType: it}
	for _, name := range []string{"claim-a", "claim-b"} {
		result, err := allocator.Allocate(ctx, nodeA, []*resourcev1.ResourceClaim{draClaim(name)})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(result.InstanceTypes, nodeA.InstanceTypes()) || result.Allocation == nil {
			t.Fatalf("expected a GPU allocation on the selected instance type, got %+v", result)
		}
		result.Allocation.Commit(ctx)
	}
	if _, err := allocator.Allocate(ctx, nodeA, []*resourcev1.ResourceClaim{draClaim("claim-c")}); err == nil {
		t.Fatal("a third exclusive claim must not fit on two GPUs")
	}
	if result, err := allocator.Allocate(ctx, nodeB, []*resourcev1.ResourceClaim{draClaim("claim-c")}); err != nil || result.Allocation == nil {
		t.Fatalf("identical templates on a different node must have independent capacity: %v", err)
	}
}

func TestDRASelectorRejection(t *testing.T) {
	t.Parallel()
	ctx := draContext(t)
	info := draType(draGPUInstanceType, 2)
	it := instancetype.NewDefaultResolver(fake.DefaultRegion).Resolve(ctx, &info, draNodeClass())
	for _, tc := range []struct{ name, selector string }{
		{name: "wrong driver", selector: "device.driver == 'other.example.com'"},
		{name: "MIG", selector: "device.attributes['gpu.nvidia.com'].type == 'mig'"},
		{name: "unmodeled product", selector: nvidiaWholeGPUSelector + " && device.attributes['gpu.nvidia.com'].productName == 'NVIDIA H100'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			node := &draNodeClaimMock{name: "node", instanceType: it}
			if _, err := draAllocator(tc.selector).Allocate(ctx, node, []*resourcev1.ResourceClaim{draClaim("claim")}); err == nil {
				t.Fatal("unsupported request unexpectedly matched the predicted inventory")
			}
		})
	}
}

func draAllocator(selector string) *dynamicresources.Allocator {
	class := coretest.DeviceClass(resourcev1.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu.nvidia.com"},
		Spec: resourcev1.DeviceClassSpec{Selectors: []resourcev1.DeviceSelector{{
			CEL: &resourcev1.CELDeviceSelector{Expression: selector},
		}}},
	})
	client := clientfake.NewClientBuilder().WithObjects(class).Build()
	return dynamicresources.NewAllocator(nil, dynamicresources.AllocatedDeviceState{}, nil, client, nil)
}

func draClaim(name string) *resourcev1.ResourceClaim {
	return coretest.ResourceClaimForRequests(name, coretest.ExactDeviceRequest("gpu", "gpu.nvidia.com", 1))
}

// Core's equivalent allocator test fixture is unexported, so this adapter supplies
// in-memory identity and the provider's real ResourceSlice templates.
var _ dynamicresources.NodeClaim = (*draNodeClaimMock)(nil)

type draNodeClaimMock struct {
	name         string
	instanceType *cloudprovider.InstanceType
}

func (n *draNodeClaimMock) ID() dynamicresources.NodeClaimID        { return unique.Make(n.name) }
func (n *draNodeClaimMock) NodeName() string                        { return "" }
func (n *draNodeClaimMock) NodePoolID() dynamicresources.NodePoolID { return unique.Make("test-pool") }
func (n *draNodeClaimMock) Requirements() scheduling.Requirements {
	return scheduling.NewRequirements()
}
func (n *draNodeClaimMock) InstanceTypes() []dynamicresources.InstanceTypeID {
	return []dynamicresources.InstanceTypeID{unique.Make(n.instanceType.Name)}
}
func (n *draNodeClaimMock) ResourceSlices() map[dynamicresources.InstanceTypeID][]dynamicresources.ResourceSlice {
	slices := make([]dynamicresources.ResourceSlice, len(n.instanceType.DynamicResources.ResourceSliceTemplates))
	for i, template := range n.instanceType.DynamicResources.ResourceSliceTemplates {
		slices[i] = dynamicresources.NewTemplateSlice(template)
	}
	return map[dynamicresources.InstanceTypeID][]dynamicresources.ResourceSlice{unique.Make(n.instanceType.Name): slices}
}
