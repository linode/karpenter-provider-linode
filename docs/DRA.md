---
layout: default
nav_title: Experimental DRA
nav_order: 8
---

# Experimental whole-GPU DRA support

This feature lets Karpenter create GPU nodes for pods that request a GPU through a `ResourceClaim`. It is experimental and off by default. See the [example](#example) for a complete setup.

## What the provider predicts

Karpenter must know which devices a node will have before the node exists. The Linode API says how many GPUs each plan has. The provider uses that number to predict the devices.

For a plan with 4 GPUs, the provider predicts 4 devices from the `gpu.nvidia.com` driver. Each device has the attribute `type: gpu`. A claim that asks for `type == 'gpu'` matches these devices.

A plan with no GPUs gets no devices. This includes the NETINT video plans. The API reports an "accelerated devices" count for them, but they have no GPUs.

The pool and device names exist only in the scheduling simulation and do not match the names on a real node. CPU, memory, and pod capacity do not change. Karpenter core uses the prediction only when DRA scheduling is on. The provider has no built-in list of certified plans.

The GPU count does not tell you whether:

- your account and region offer the plan,
- your LKE tier supports the plan,
- the driver publishes each GPU as one exclusive GPU.

Set NodePool instance-type requirements to limit the plans that Karpenter can launch for DRA workloads.

Karpenter core runs the scheduling simulation and narrows the list of instance types for launch. Kubernetes and the installed NVIDIA driver allocate the claim and prepare the device. This feature adds no DRA driver, allocator, CRD, ResourceSlice, or claim allocation.

## Plans

- Supported: RTX 4000 Ada plans (`g2-gpu-rtx4000a*`). The provider is tested with `g2-gpu-rtx4000a1-s` (one RTX 4000 Ada) on standard LKE. The multi-GPU plans use the same GPU.
- Supported: RTX PRO 6000 Blackwell plans. Check the prerequisites below before you use them.
- Limited availability: the Quadro RTX 6000 plans (`g1-gpu-rtx6000-*`). See the [GPU-on-LKE guide](https://techdocs.akamai.com/cloud-computing/docs/gpus-on-lke) for availability.
- No devices predicted: the NETINT plans, because they have no GPUs.

## Enable DRA

DRA scheduling is off by default. Karpenter core controls it with the `IGNORE_DRA_REQUESTS` setting, which defaults to `true`. Check the prerequisites below, then enable DRA in the Helm values:

```yaml
settings:
  mode: lke
  dra:
    enabled: true
```

This sets `IGNORE_DRA_REQUESTS="false"` on the controller. The chart always grants `get`, `list`, and `watch` permissions on the `resource.k8s.io` resources `resourceclaims`, `resourceslices`, and `deviceclasses`. The chart does not install NVIDIA software or change cluster feature gates.

If you deploy the controller without this chart, set `--ignore-dra-requests=false` (or `IGNORE_DRA_REQUESTS=false`) and grant the same read permissions. Do not set `IGNORE_DRA_REQUESTS` in `controller.env` when `settings.dra.enabled` is `true`. Use LKE mode. Instance mode does not yet bootstrap or join Kubernetes nodes.

The setting applies to the whole controller and changes only after a restart. Every NodeClass that uses a GPU plan must have the same whole-GPU DRA bootstrap profile. Do not use the same plan for a separate legacy GPU installation. Changing the setting does not reconfigure existing nodes or claims.

## Example

Limit a NodePool to the GPU plans that you want. Add a taint, so that only GPU workloads run on these nodes:

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

The NVIDIA DRA driver (for example, from the GPU Operator) creates the `gpu.nvidia.com` DeviceClass. Ask for one GPU in your workload:

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

The pod does not request `nvidia.com/gpu`.

What happens:

1. The pod stays `Pending`. Karpenter matches the claim to the predicted devices and creates a NodeClaim for the plan.
2. The node registers. When the driver publishes a complete ResourceSlice for the node, the NodeClaim becomes `Initialized`.
3. The scheduler allocates the GPU to the claim, and the pod runs.
4. When the pod is gone, Kubernetes releases the claim. Karpenter removes the empty node when consolidation runs.

If no node appears, check the Karpenter log:

| Log message | Meaning |
|---|---|
| `no instance type can satisfy the allocation` | The claim asks for more GPUs than the allowed plans have, or it selects an attribute that is not predicted. |
| `all available instance types exceed limits for nodepool` | The NodePool limit has no room for another node. |
| `skipping pod with Dynamic Resource Allocation requirements` | DRA scheduling is off. See [Enable DRA](#enable-dra). |

Two pods with separate claims get two nodes, because each claim needs its own GPU.

## Prerequisites and limits

This feature targets [NVIDIA DRA v0.5.0](https://github.com/kubernetes-sigs/dra-driver-nvidia-gpu/tree/v0.5.0). This page is not a certified compatibility matrix. The driver [needs](https://github.com/kubernetes-sigs/dra-driver-nvidia-gpu/blob/v0.5.0/site/content/docs/prerequisites.md):

- Kubernetes 1.34.2 or newer.
- NVIDIA driver 565 or newer (580 or newer with the GPU Operator).
- NVIDIA container toolkit 1.18 or newer.
- A container runtime with CDI enabled, and the driver's discovery labels.

Also check these items:

- The patch versions of the API server and workers, and that `resource.k8s.io/v1` is available. The LKE Enterprise [1.34 release](https://techdocs.akamai.com/cloud-computing/changelog/jul-7-2026-lke-enterprise-kubernetes-v134-support) enables base DRA, but the minor version does not show whether nodes meet the patch minimum. GPU software differs by LKE tier, so read the [GPU-on-LKE guide](https://techdocs.akamai.com/cloud-computing/docs/gpus-on-lke).
- Your GPU. NVIDIA lists data center GPUs for full GPU allocation, and the RTX 4000 Ada is not on that list. LKE support for a GPU does not mean the NVIDIA driver supports it.
- The NVIDIA setup. Use an exclusive profile (no MIG, no sharing). Disable ComputeDomains, which the reference chart enables by default. Disable the legacy device plugin for the same devices, so that two allocators do not own one GPU. Use a DeviceClass that selects `gpu.nvidia.com` and `type == "gpu"`.
- Each node's ResourceSlice pool. It must be complete, have the correct Node owner reference, and hold the GPU count that the API reports.
- Read permission for Karpenter on the DRA resources. Without it, ordinary scheduling can fail once DRA is on.

NodeClaim initialization waits for a complete pool. It does not compare the published device count or attributes with the prediction.

A pod that does not use DRA can also cause a GPU node. It has no requested-driver annotation, so the node can initialize before the driver publishes its devices. A later GPU workload can then cause extra provisioning. Taint your GPU nodes, as in the example. A NodePool `startupTaint` does not help on LKE, because the provider does not inject startup-only taints into LKE pools. A wrong prediction can also affect consolidation and replacement.

## Not supported

- Anything other than whole-GPU claims. Legacy `nvidia.com/gpu` capacity and the bridge from extended resources to DRA are not available.
- Claims that select on product, GPU memory, architecture, UUID, or PCI, NUMA, or fabric topology. The provider does not predict these. See the next section.
- MIG, GPU sharing, time slicing, VFIO, ComputeDomains, administrative access, and device-taint guarantees. A claim that needs one of them stays `Pending` or causes extra nodes.
- Claims that use `FirstAvailable` requests or `DistinctAttribute` constraints.

### Claims that select on GPU attributes

The Linode API reports only the number of GPUs for each plan. It does not report the GPU model or the GPU memory. For this reason, each predicted device has one attribute: `type: gpu`. A claim that uses any other attribute cannot match a predicted device. Karpenter creates no node, and the pod stays `Pending`. The Karpenter log shows `no instance type can satisfy the allocation`.

The installed NVIDIA driver publishes more attributes, such as `productName`, `architecture`, and a `memory` capacity. A real node can serve these claims, but Karpenter cannot predict the attributes before the node exists.

| Claim selector | Result |
|---|---|
| `device.driver == 'gpu.nvidia.com' && device.attributes['gpu.nvidia.com'].type == 'gpu'` | Works. Karpenter creates a node. |
| `device.attributes['gpu.nvidia.com'].productName == 'NVIDIA RTX 4000 Ada Generation'` | No node. The pod stays `Pending`. |
| A selector that needs at least 16Gi of GPU memory | No node. The pod stays `Pending`. |

To choose a GPU model, set the model in the NodePool, not in the claim:

1. Add an instance-type requirement to the NodePool. Use only the plan that has the GPU model that you need.
2. Use one NodePool for each GPU model.
3. In the claim, ask for any GPU: `type == 'gpu'`.

If one NodePool allows more than one GPU plan, a claim cannot ask for a specific model. Karpenter can choose any allowed plan.
