---
layout: default
nav_title: GPU DRA (alpha)
nav_order: 8
---

# GPU scheduling with DRA (alpha)

> **Alpha feature.** Do not use it for production workloads.
>
> - It is off by default.
> - Behavior, settings, and the plan catalog can change or be removed in any release without notice.
> - Report problems in the project issue tracker.

[Dynamic Resource Allocation (DRA)](https://kubernetes.io/docs/concepts/resource-management/dynamic-resource-allocation/) is a Kubernetes feature that lets workloads request devices such as GPUs through `ResourceClaim` objects. Kubernetes allocates matching devices to claims and schedules pods on nodes that can access those devices.

DRA was first available in Kubernetes 1.30 and is stable from 1.35. The NVIDIA DRA v0.5.0 setup described here requires Kubernetes 1.34.2 or newer.

With DRA enabled in this provider, Karpenter creates GPU nodes for pending pods that request a GPU through a `ResourceClaim` instead of `nvidia.com/gpu`.

**On this page**

1. [How it works](#how-it-works)
2. [Before you start](#before-you-start)
3. [Enable DRA](#enable-dra)
4. [Example](#example)
5. [Troubleshooting](#troubleshooting)
6. [Select GPUs in a claim](#select-gpus-in-a-claim)
7. [Plan catalog](#plan-catalog)
8. [Limits](#limits)
9. [Maintain the catalog](#maintain-the-catalog)

## How it works

The Linode API reports the GPU count of each plan. For each GPU, the provider predicts one exclusive whole-GPU device. The device uses the driver name `gpu.nvidia.com` and `type: gpu`.

For plans in the [catalog](#plan-catalog), the provider also predicts these fields:

- product name
- brand
- architecture
- CUDA compute capability
- memory per GPU

Claims can select on these fields.

Each part has one job:

- Karpenter core runs the scheduling simulation and narrows the list of instance types for launch.
- Kubernetes and the NVIDIA DRA driver allocate the claim and prepare the device.
- This provider only predicts the devices. It adds no DRA driver, allocator, CRD, ResourceSlice, or claim allocation.

The pool and device names exist only in the simulation. They do not match the names on a real node. CPU, memory, and pod capacity do not change. Karpenter core uses the prediction only when DRA scheduling is on.

Set instance-type requirements on the NodePool to limit the plans that Karpenter can launch for DRA workloads to limit plan options.

## Before you start

You need:

- LKE mode. Instance mode does not yet bootstrap or join Kubernetes nodes.
- Kubernetes 1.34.2 or newer, with `resource.k8s.io/v1` available.
- A GPU plan that your account, region, and LKE tier offer.
- The NVIDIA DRA driver, set up as described below.

The [LKE Enterprise 1.34 release](https://techdocs.akamai.com/cloud-computing/changelog/jul-7-2026-lke-enterprise-kubernetes-v134-support) enables base DRA. The minor version does not show whether your nodes meet the patch minimum. Check the patch versions of the API server and the workers. GPU software differs by LKE tier. Read the [GPU-on-LKE guide](https://techdocs.akamai.com/cloud-computing/docs/gpus-on-lke).

### NVIDIA driver requirements

This feature targets [NVIDIA DRA v0.5.0](https://github.com/kubernetes-sigs/dra-driver-nvidia-gpu/tree/v0.5.0). The driver [needs](https://github.com/kubernetes-sigs/dra-driver-nvidia-gpu/blob/v0.5.0/site/content/docs/prerequisites.md):

- NVIDIA driver 565 or newer (580 or newer with the GPU Operator).
- NVIDIA container toolkit 1.18 or newer.
- A container runtime with CDI enabled, and the driver's discovery labels.

### NVIDIA driver setup

1. Use an exclusive profile. Do not use Multi-Instance GPU (MIG) or GPU sharing. That is not supported yet.
2. Disable ComputeDomains. The reference chart enables them by default.
3. Disable the legacy device plugin for the same devices. Two allocators must not own one GPU.
4. Use a DeviceClass that selects `gpu.nvidia.com` and `type == "gpu"`.
5. Check that each node's ResourceSlice pool is complete, has the correct Node owner reference, and holds the GPU count that the API reports.

## Enable DRA

Karpenter core controls DRA scheduling with the `IGNORE_DRA_REQUESTS` setting. It defaults to `true`, so DRA is off.

Set this in the Helm values:

```yaml
settings:
  mode: lke
  dra:
    enabled: true
```

This sets `IGNORE_DRA_REQUESTS="false"` on the controller. Do not also set `IGNORE_DRA_REQUESTS` in `controller.env`.

The chart always grants `get`, `list`, and `watch` on the `resource.k8s.io` resources `resourceclaims`, `resourceslices`, and `deviceclasses`. Karpenter needs this read access. Without it, ordinary scheduling can fail once DRA is on. The chart does not install NVIDIA software or change cluster feature gates.

If you deploy the controller without this chart:

1. Set `--ignore-dra-requests=false` (or `IGNORE_DRA_REQUESTS=false`).
2. Grant the same read permissions.

Note these effects:

- The setting applies to the whole controller. It changes only after a restart.
- Every NodeClass that uses a GPU plan must have the same whole-GPU DRA bootstrap profile.
- Do not use the same plan for a separate legacy GPU installation.
- Changing the setting does not reconfigure existing nodes or claims.

## Example

### 1. Create a NodePool

Limit the NodePool to the GPU plans that you want. Add a taint so that only GPU workloads run on these nodes.

```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: gpu
spec:
  template:
    spec:
      nodeClassRef:
        group: karpenter.k8s.linode
        kind: LinodeNodeClass
        name: default
      requirements:
        - key: node.kubernetes.io/instance-type
          operator: In
          values: ["g2-gpu-rtx4000a1-s"]
      taints:
        - key: nvidia.com/gpu
          value: "true"
          effect: NoSchedule
  limits:
    cpu: "16"
```

### 2. Request a GPU in a workload

The NVIDIA DRA driver (for example, from the GPU Operator) creates the `gpu.nvidia.com` DeviceClass. The pod below does not request `nvidia.com/gpu`.

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: one-gpu
spec:
  spec:
    devices:
      requests:
        - name: gpu
          exactly:
            deviceClassName: gpu.nvidia.com
            allocationMode: ExactCount
            count: 1
---
apiVersion: v1
kind: Pod
metadata:
  name: gpu-job
spec:
  nodeSelector:
    karpenter.sh/nodepool: gpu
  tolerations:
    - key: nvidia.com/gpu
      operator: Equal
      value: "true"
      effect: NoSchedule
  resourceClaims:
    - name: gpu
      resourceClaimTemplateName: one-gpu
  containers:
    - name: app
      image: nvcr.io/nvidia/k8s/cuda-sample:vectoradd-cuda12.5.0
      resources:
        claims:
          - name: gpu
```

### 3. Expected result

This flow applies when DRA is on, the predicted plan matches the claim, and the cluster meets the prerequisites.

1. The pod stays `Pending` while Karpenter matches the claim and creates a NodeClaim.
2. The node registers and the driver publishes a complete ResourceSlice. The NodeClaim can then become `Initialized`.
3. Kubernetes can allocate the GPU to the claim and start the pod.
4. When the pod is gone, Kubernetes releases the claim. Karpenter can remove an empty node when consolidation runs.

Separate claims need separate GPUs. They do not guarantee one node for each claim. Node placement depends on the available predicted capacity and on scheduling.

## Troubleshooting

If no node appears, check the Karpenter log.

| Log message | Meaning |
|---|---|
| `no instance type can satisfy the allocation` | The claim asks for more GPUs than the allowed plans have, or it selects an attribute that the provider does not predict. |
| `all available instance types exceed limits for nodepool` | The NodePool limit has no room for another node. |
| `skipping pod with Dynamic Resource Allocation requirements` | DRA scheduling is off. See [Enable DRA](#enable-dra). |


## Select GPUs in a claim

Every predicted GPU device has the `type` attribute. The metadata selectors in the table below work only for the plan IDs in the [Plan catalog](#plan-catalog).

| Field | Compare with |
|---|---|
| `productName` | `NVIDIA RTX 4000 Ada Generation` |
| `brand` | `NvidiaRTX` |
| `architecture` | `Ada Lovelace` |
| `cudaComputeCapability` | A semantic version, for example `.compareTo(semver('8.9.0')) >= 0` |
| `memory` (capacity) | A Kubernetes quantity, for example `device.capacity['gpu.nvidia.com'].memory.compareTo(quantity('16Gi')) >= 0` |

`memory` is a capacity for each device. It is not a total for all GPUs on a node. Ask for the GPU count in the `count` field of the claim.

This claim requires the RTX 4000 Ada product, CUDA compute capability 8.9 or newer, and at least 16Gi of memory on each allocated GPU:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: ada-gpu
spec:
  devices:
    requests:
      - name: gpu
        exactly:
          deviceClassName: gpu.nvidia.com
          allocationMode: ExactCount
          count: 1
          selectors:
            - cel:
                expression: >-
                  device.driver == 'gpu.nvidia.com' &&
                  device.attributes['gpu.nvidia.com'].type == 'gpu' &&
                  'productName' in device.attributes['gpu.nvidia.com'] &&
                  device.attributes['gpu.nvidia.com'].productName == 'NVIDIA RTX 4000 Ada Generation' &&
                  'brand' in device.attributes['gpu.nvidia.com'] &&
                  device.attributes['gpu.nvidia.com'].brand == 'NvidiaRTX' &&
                  'architecture' in device.attributes['gpu.nvidia.com'] &&
                  device.attributes['gpu.nvidia.com'].architecture == 'Ada Lovelace' &&
                  'cudaComputeCapability' in device.attributes['gpu.nvidia.com'] &&
                  device.attributes['gpu.nvidia.com'].cudaComputeCapability.compareTo(semver('8.9.0')) >= 0 &&
                  'memory' in device.capacity['gpu.nvidia.com'] &&
                  device.capacity['gpu.nvidia.com'].memory.compareTo(quantity('16Gi')) >= 0
```

Always guard optional fields with a CEL `in` check before you read them. A missing map key can cause an evaluation error. With the guards, a selector that needs missing metadata does not match the plan, and scheduling does not choose that plan for the claim.

Plans outside the supported model-family patterns keep count/type-only predictions. They do not match selectors that require model metadata.

## Plan catalog

The catalog matches plan IDs with these anchored regular expressions. New GPU counts and VM sizes within these model families inherit the same per-GPU metadata without a provider release.

| GPU | Plan ID pattern |
|---|---|
| RTX 4000 Ada | `^g2-gpu-rtx4000a[1-9]\d*-[a-z]+$` |
| RTX PRO 6000 Blackwell | `^g3-gpu-rtxpro6000-blackwell-[1-9]\d*$` |

This assumes that matching plans retain the named GPU model. A different model or naming convention needs a catalog change. To list current GPU plans, use `linode-cli linodes types --class gpu`; the catalog does not track availability.

Model identity and CUDA compute capability are fixed hardware properties, not installed software versions. Ada has compute capability 8.9; RTX PRO 6000 Blackwell has 12.0. Native compilation starts with CUDA Toolkit [11.8 for Ada](https://docs.nvidia.com/cuda/ada-compatibility-guide/index.html#building-applications-using-cuda-toolkit-11-8) and [12.8 for Blackwell](https://docs.nvidia.com/cuda/blackwell-compatibility-guide/index.html#building-applications-using-cuda-toolkit-12-8). Older applications can still work through compatible cubins or forward-compatible PTX; these toolkit versions are not runtime requirements predicted by the provider.

Memory quantities describe the capacity of one GPU, not aggregate VM memory.

The API GPU count sets the number of predicted devices. It is separate from the model metadata. A plan ID in the catalog does not guarantee that:

- your account and region offer the plan,
- your LKE tier supports the plan,
- the installed driver publishes the GPUs as exclusive whole GPUs.

### Plans without catalog entries

- **Quadro RTX 6000 Turing** (`g1-gpu-rtx6000-*`). Akamai's [GPU-on-LKE guide](https://techdocs.akamai.com/cloud-computing/docs/gpus-on-lke) says that you cannot deploy these GPUs in LKE or LKE-E clusters at this time, because of limited availability.
- **NETINT.** These plans have no GPUs and no predicted devices.

## Limits

This feature supports only whole-GPU claims. It does not support:

- MIG, GPU sharing, and time slicing,
- VFIO,
- ComputeDomains,
- administrative access and device-taint guarantees,
- claims that rely on UUID, PCI, NUMA, or fabric topology,
- `FirstAvailable` requests,
- `DistinctAttribute` constraints.

## Maintain the catalog

This section is for contributors.

Update the model-family regex map only for a new GPU model or naming convention, not for each new plan size. Add a family only when you have evidence for its GPU model and per-device metadata.

1. Record the source of each value. Mark which values come from captured device data and which come from online estimates.
2. Add focused tests for family matches, new GPU counts and sizes, and near-miss IDs that must remain count/type-only. Keep device counts API-derived.
3. Compare the selectors with the NVIDIA DRA attribute and capacity types.

Keep patterns anchored and model-specific. Do not infer metadata for a different GPU model or use aggregate VM memory.
