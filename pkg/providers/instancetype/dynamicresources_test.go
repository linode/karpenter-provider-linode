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
	"reflect"
	"sort"
	"testing"
	"unique"

	"github.com/linode/linodego/v2"
	"github.com/patrickmn/go-cache"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	dracel "k8s.io/dynamic-resource-allocation/cel"
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

const (
	draGPUInstanceType       = "test-gpu-plan"
	adaGPUInstanceType       = "g2-gpu-rtx4000a1-s"
	adaTwoGPUInstanceType    = "g2-gpu-rtx4000a2-s"
	blackwellGPUInstanceType = "g3-gpu-rtxpro6000-blackwell-1"
)

// These tests use an in-memory catalog fake only (no envtest). Run with -run '^TestDRA'.
func TestDRAInventory(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                    string
		plan                    string
		gpus, accelerated, want int
	}{
		{name: "unknown GPU plan keeps API count", plan: draGPUInstanceType, gpus: 8, want: 8},
		{name: "known plan keeps API count instead of plan suffix", plan: adaGPUInstanceType, gpus: 3, want: 3},
		{name: "known plan with no GPUs", plan: adaTwoGPUInstanceType},
		{name: "separate accelerator count", plan: draGPUInstanceType, gpus: 2, accelerated: 4, want: 2},
		{name: "accelerators only", plan: draGPUInstanceType, accelerated: 4},
		{name: "CPU only", plan: "test-cpu-plan"},
		{name: "invalid negative GPU count", plan: draGPUInstanceType, gpus: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			info := draType(tc.plan, tc.gpus)
			info.AcceleratedDevices = tc.accelerated
			it := instancetype.NewDefaultResolver(fake.DefaultRegion).Resolve(draContext(t), &info, draNodeClass())
			assertGPUCount(t, it, tc.want)
		})
	}
}

func TestDRAListAndGet(t *testing.T) {
	t.Parallel()
	ctx := draContext(t)
	types := []linodego.LinodeType{draType(adaTwoGPUInstanceType, 2), draType("test-cpu-plan", 0)}
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
		if it.Name == adaTwoGPUInstanceType {
			assertGPUCount(t, it, 2)
			assertAdaMetadataSelectors(t, ctx, it)
		} else {
			assertGPUCount(t, it, 0)
		}
	}
	// List populates the cache; this Get must run before the refresh flushes it.
	cachedAda, err := provider.Get(ctx, nodeClass, adaTwoGPUInstanceType)
	if err != nil {
		t.Fatal(err)
	}
	assertGPUCount(t, cachedAda, 2)
	assertAdaMetadataSelectors(t, ctx, cachedAda)

	// The catalog must not override a refreshed API-reported GPU count.
	types[0].GPUs = 1
	api.ListTypesOutput.Set(&types)
	if err := provider.UpdateInstanceTypes(ctx); err != nil {
		t.Fatal(err)
	}
	refreshed, err := provider.Get(ctx, nodeClass, adaTwoGPUInstanceType)
	if err != nil {
		t.Fatal(err)
	}
	assertGPUCount(t, refreshed, 1)
}

// assertGPUCount checks only the predicted whole-device count, not incidental
// template details.
func assertGPUCount(t *testing.T, it *cloudprovider.InstanceType, n int) {
	t.Helper()
	got := 0
	for _, template := range it.DynamicResources.ResourceSliceTemplates {
		got += len(template.Devices)
	}
	if got != n {
		t.Fatalf("expected %d GPUs for %s, got %d", n, it.Name, got)
	}
}

func assertAdaMetadataSelectors(t *testing.T, ctx context.Context, it *cloudprovider.InstanceType) {
	t.Helper()
	assertAllocatorPlans(t, ctx, []*cloudprovider.InstanceType{it}, adaMetadataSelector, []string{it.Name})
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

const (
	nvidiaWholeGPUSelector = "device.driver == 'gpu.nvidia.com' && device.attributes['gpu.nvidia.com'].type == 'gpu'"
	adaMetadataSelector    = nvidiaWholeGPUSelector +
		" && device.attributes['gpu.nvidia.com'].productName == 'NVIDIA RTX 4000 Ada Generation'" +
		" && device.attributes['gpu.nvidia.com'].brand == 'NvidiaRTX'" +
		" && device.attributes['gpu.nvidia.com'].architecture == 'Ada Lovelace'" +
		" && device.attributes['gpu.nvidia.com'].cudaComputeCapability.compareTo(semver('8.9.0')) == 0" +
		" && device.capacity['gpu.nvidia.com'].memory.compareTo(quantity('20475Mi')) == 0"
)

// CEL evaluation errors on absent map keys; these builders guard optional metadata.
func nvidiaAttributeSelector(attribute, predicate string) string {
	return nvidiaWholeGPUSelector + " && '" + attribute + "' in device.attributes['gpu.nvidia.com'] && " + predicate
}

func nvidiaCapacitySelector(capacity, predicate string) string {
	return nvidiaWholeGPUSelector + " && '" + capacity + "' in device.capacity['gpu.nvidia.com'] && " + predicate
}

// Exercise the pinned core's actual allocation/CEL contract using a fake
// DeviceClass client and in-memory NodeClaims. No API server is started.
func TestDRAWholeGPUAllocation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, plan, selector string }{
		{name: "unknown plan count-only fallback", plan: draGPUInstanceType, selector: nvidiaWholeGPUSelector},
		{name: "metadata on every known-plan GPU", plan: adaTwoGPUInstanceType, selector: adaMetadataSelector},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := draContext(t)
			info := draType(tc.plan, 2)
			it := instancetype.NewDefaultResolver(fake.DefaultRegion).Resolve(ctx, &info, draNodeClass())
			allocator := draAllocator(tc.selector)
			nodeA := &draNodeClaimMock{name: "node-a", instanceTypes: []*cloudprovider.InstanceType{it}}
			nodeB := &draNodeClaimMock{name: "node-b", instanceTypes: []*cloudprovider.InstanceType{it}}
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
		})
	}
}

func TestDRAPlanFamilies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		plan, architecture string
	}{
		{plan: adaGPUInstanceType, architecture: "Ada Lovelace"},
		{plan: "g2-gpu-rtx4000a4-m", architecture: "Ada Lovelace"},
		{plan: "g2-gpu-rtx4000a16-future", architecture: "Ada Lovelace"},
		{plan: blackwellGPUInstanceType, architecture: "Blackwell"},
		{plan: "g3-gpu-rtxpro6000-blackwell-16", architecture: "Blackwell"},
		{plan: "g2-gpu-rtx5000a1-s"},
		{plan: "g4-gpu-rtxpro6000-blackwell-1"},
		{plan: "prefix-g2-gpu-rtx4000a1-s"},
		{plan: "g2-gpu-rtx4000a1-s-extra"},
		{plan: "g2-gpu-rtx4000a0-s"},
		{plan: "g2-gpu-rtx4000a1-"},
		{plan: "g3-gpu-rtxpro6000-blackwell-0"},
		{plan: "g3-gpu-rtxpro6000-blackwell-1-extra"},
	} {
		t.Run(tc.plan, func(t *testing.T) {
			t.Parallel()
			ctx := draContext(t)
			it := instancetype.NewDefaultResolver(fake.DefaultRegion).Resolve(ctx, ptrToType(tc.plan, 2), draNodeClass())
			assertGPUCount(t, it, 2)
			assertAllocatorPlans(t, ctx, []*cloudprovider.InstanceType{it}, nvidiaWholeGPUSelector, []string{tc.plan})
			var want []string
			if tc.architecture != "" {
				want = []string{tc.plan}
			}
			selector := nvidiaAttributeSelector("architecture", "device.attributes['gpu.nvidia.com'].architecture in ['Ada Lovelace', 'Blackwell']")
			if tc.architecture != "" {
				selector = nvidiaAttributeSelector("architecture", "device.attributes['gpu.nvidia.com'].architecture == '"+tc.architecture+"'")
			}
			assertAllocatorPlans(t, ctx, []*cloudprovider.InstanceType{it}, selector, want)
		})
	}
}

func assertAllocatorPlans(t *testing.T, ctx context.Context, candidates []*cloudprovider.InstanceType, selector string, want []string) {
	t.Helper()
	// Core's allocator converts selector errors into non-matches. Check clean
	// evaluation directly so a broken expression cannot pass a rejection case.
	if len(want) == 0 {
		celCache := dracel.NewCache(0, dracel.Features{EnableConsumableCapacity: true})
		for _, candidate := range candidates {
			for _, template := range candidate.DynamicResources.ResourceSliceTemplates {
				for _, device := range template.Devices {
					id := dynamicresources.DeviceID{
						DeviceID: cloudprovider.DeviceID{Driver: template.Driver, Pool: template.Pool.Name, Device: device.Name},
						Template: true,
					}
					matched, err := dynamicresources.DeviceMatchesSelectors(ctx, device, id, []resourcev1.DeviceSelector{{CEL: &resourcev1.CELDeviceSelector{Expression: selector}}}, celCache)
					if err != nil {
						t.Fatalf("evaluating rejection selector %q: %v", selector, err)
					}
					if matched {
						t.Fatalf("rejection selector %q matched a device on %s", selector, candidate.Name)
					}
				}
			}
		}
	}
	node := &draNodeClaimMock{name: "metadata-selector", instanceTypes: candidates}
	result, err := draAllocator(selector).Allocate(ctx, node, []*resourcev1.ResourceClaim{draClaim("metadata-claim")})
	if len(want) == 0 {
		if err == nil {
			t.Fatalf("selector unexpectedly matched instance types %v", result.InstanceTypes)
		}
		return
	}
	if err != nil {
		t.Fatalf("allocating for selector %q: %v", selector, err)
	}
	if result.Allocation == nil {
		t.Fatal("expected an allocation")
	}
	got := make([]string, len(result.InstanceTypes))
	for i, id := range result.InstanceTypes {
		got[i] = id.Value()
	}
	sort.Strings(got)
	expected := append([]string(nil), want...)
	sort.Strings(expected)
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("selector %q matched %v, want %v", selector, got, expected)
	}
}

func ptrToType(name string, gpus int) *linodego.LinodeType {
	info := draType(name, gpus)
	return &info
}

func TestDRASelectors(t *testing.T) {
	t.Parallel()
	ctx := draContext(t)
	resolver := instancetype.NewDefaultResolver(fake.DefaultRegion)
	candidates := []*cloudprovider.InstanceType{
		resolver.Resolve(ctx, ptrToType(adaGPUInstanceType, 1), draNodeClass()),
		resolver.Resolve(ctx, ptrToType(adaTwoGPUInstanceType, 2), draNodeClass()),
		resolver.Resolve(ctx, ptrToType(blackwellGPUInstanceType, 1), draNodeClass()),
		resolver.Resolve(ctx, ptrToType("g2-gpu-rtx5000a1-s", 1), draNodeClass()),
		resolver.Resolve(ctx, ptrToType("test-cpu-plan", 0), draNodeClass()),
	}
	type selectorCase struct {
		name, selector string
		want           []string
	}
	tests := []selectorCase{
		{
			name:     "product name selects Ada",
			selector: nvidiaAttributeSelector("productName", "device.attributes['gpu.nvidia.com'].productName == 'NVIDIA RTX 4000 Ada Generation'"),
			want:     []string{adaGPUInstanceType, adaTwoGPUInstanceType},
		},
		{
			name:     "Blackwell product name selects Blackwell",
			selector: nvidiaAttributeSelector("productName", "device.attributes['gpu.nvidia.com'].productName == 'NVIDIA RTX PRO 6000 Blackwell Server Edition'"),
			want:     []string{blackwellGPUInstanceType},
		},
		{
			name:     "wrong product name selects nothing",
			selector: nvidiaAttributeSelector("productName", "device.attributes['gpu.nvidia.com'].productName == 'NVIDIA H100'"),
		},
		{
			name:     "brand selects Ada",
			selector: nvidiaAttributeSelector("brand", "device.attributes['gpu.nvidia.com'].brand == 'NvidiaRTX'"),
			want:     []string{adaGPUInstanceType, adaTwoGPUInstanceType},
		},
		{
			name:     "brand selects Blackwell",
			selector: nvidiaAttributeSelector("brand", "device.attributes['gpu.nvidia.com'].brand == 'Nvidia'"),
			want:     []string{blackwellGPUInstanceType},
		},
		{
			name:     "wrong brand selects nothing",
			selector: nvidiaAttributeSelector("brand", "device.attributes['gpu.nvidia.com'].brand == 'NVIDIA'"),
		},
		{
			name:     "architecture selects Ada",
			selector: nvidiaAttributeSelector("architecture", "device.attributes['gpu.nvidia.com'].architecture == 'Ada Lovelace'"),
			want:     []string{adaGPUInstanceType, adaTwoGPUInstanceType},
		},
		{
			name:     "architecture selects Blackwell",
			selector: nvidiaAttributeSelector("architecture", "device.attributes['gpu.nvidia.com'].architecture == 'Blackwell'"),
			want:     []string{blackwellGPUInstanceType},
		},
		{
			name:     "wrong architecture selects nothing",
			selector: nvidiaAttributeSelector("architecture", "device.attributes['gpu.nvidia.com'].architecture == 'Hopper'"),
		},
		{
			name:     "compute capability exact semver selects Ada",
			selector: nvidiaAttributeSelector("cudaComputeCapability", "device.attributes['gpu.nvidia.com'].cudaComputeCapability.compareTo(semver('8.9.0')) == 0"),
			want:     []string{adaGPUInstanceType, adaTwoGPUInstanceType},
		},
		{
			name:     "compute capability threshold selects Blackwell",
			selector: nvidiaAttributeSelector("cudaComputeCapability", "device.attributes['gpu.nvidia.com'].cudaComputeCapability.compareTo(semver('12.0.0')) >= 0"),
			want:     []string{blackwellGPUInstanceType},
		},
		{
			name:     "compute capability above maximum selects nothing",
			selector: nvidiaAttributeSelector("cudaComputeCapability", "device.attributes['gpu.nvidia.com'].cudaComputeCapability.compareTo(semver('12.0.1')) >= 0"),
		},
		{
			name:     "minimum per-GPU memory includes equality",
			selector: nvidiaCapacitySelector("memory", "device.capacity['gpu.nvidia.com'].memory.compareTo(quantity('20475Mi')) >= 0"),
			want:     []string{adaGPUInstanceType, adaTwoGPUInstanceType, blackwellGPUInstanceType},
		},
		{
			name:     "per-GPU memory exact Blackwell quantity",
			selector: nvidiaCapacitySelector("memory", "device.capacity['gpu.nvidia.com'].memory.compareTo(quantity('97887Mi')) == 0"),
			want:     []string{blackwellGPUInstanceType},
		},
		{
			name:     "per-GPU memory one byte above Ada selects Blackwell",
			selector: nvidiaCapacitySelector("memory", "device.capacity['gpu.nvidia.com'].memory.compareTo(quantity('21469593601')) >= 0"),
			want:     []string{blackwellGPUInstanceType},
		},
	}
	knownPlans := []string{adaGPUInstanceType, adaTwoGPUInstanceType, blackwellGPUInstanceType}
	for _, attribute := range []string{"productName", "brand", "architecture", "cudaComputeCapability"} {
		tests = append(tests, selectorCase{
			name:     "unknown plan omits " + attribute,
			selector: nvidiaWholeGPUSelector + " && '" + attribute + "' in device.attributes['gpu.nvidia.com']",
			want:     knownPlans,
		})
	}
	tests = append(tests,
		selectorCase{
			name:     "unknown plan omits memory capacity",
			selector: nvidiaWholeGPUSelector + " && 'memory' in device.capacity['gpu.nvidia.com']",
			want:     knownPlans,
		},
		selectorCase{name: "wrong driver", selector: "device.driver == 'other.example.com'"},
		selectorCase{name: "MIG", selector: "device.attributes['gpu.nvidia.com'].type == 'mig'"},
		selectorCase{name: "runtime UUID is not predicted", selector: nvidiaWholeGPUSelector + " && 'uuid' in device.attributes['gpu.nvidia.com']"},
	)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, reverse := range []bool{false, true} {
				ordered := make([]*cloudprovider.InstanceType, len(candidates))
				copy(ordered, candidates)
				orderName := "catalog order"
				if reverse {
					orderName = "reverse order"
					for left, right := 0, len(ordered)-1; left < right; left, right = left+1, right-1 {
						ordered[left], ordered[right] = ordered[right], ordered[left]
					}
				}
				t.Run(orderName, func(t *testing.T) {
					assertAllocatorPlans(t, ctx, ordered, tc.selector, tc.want)
				})
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
	name          string
	instanceTypes []*cloudprovider.InstanceType
}

func (n *draNodeClaimMock) ID() dynamicresources.NodeClaimID {
	return unique.Make(n.name)
}

func (n *draNodeClaimMock) NodeName() string {
	return ""
}

func (n *draNodeClaimMock) NodePoolID() dynamicresources.NodePoolID {
	return unique.Make("test-pool")
}

func (n *draNodeClaimMock) Requirements() scheduling.Requirements {
	return scheduling.NewRequirements()
}

func (n *draNodeClaimMock) InstanceTypes() []dynamicresources.InstanceTypeID {
	result := make([]dynamicresources.InstanceTypeID, len(n.instanceTypes))
	for i, it := range n.instanceTypes {
		result[i] = unique.Make(it.Name)
	}
	return result
}

func (n *draNodeClaimMock) ResourceSlices() map[dynamicresources.InstanceTypeID][]dynamicresources.ResourceSlice {
	result := make(map[dynamicresources.InstanceTypeID][]dynamicresources.ResourceSlice)
	for _, it := range n.instanceTypes {
		slices := make([]dynamicresources.ResourceSlice, len(it.DynamicResources.ResourceSliceTemplates))
		for i, template := range it.DynamicResources.ResourceSliceTemplates {
			slices[i] = dynamicresources.NewTemplateSlice(template)
		}
		result[unique.Make(it.Name)] = slices
	}
	return result
}
