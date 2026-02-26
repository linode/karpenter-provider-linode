# Configuration

This guide details the configuration options for the Karpenter Provider for Linode, covering environment variables, operating modes, and the `LinodeNodeClass` Custom Resource.

## Environment Variables

| Variable | Description | Required? | Default |
|----------|-------------|-----------|---------|
| `LINODE_TOKEN` | Your Linode API Personal Access Token. | **Yes** | - |
| `CLUSTER_NAME` | The name of your LKE cluster. Used to discover the cluster ID in `lke` mode. | **Yes** (in `lke` mode) | - |
| `LINODE_REGION` | The Linode region code. See [available regions](https://www.linode.com/global-infrastructure/availability/). | No | `us-east` |
| `CLUSTER_ENDPOINT` | The external Kubernetes cluster endpoint. Discovered automatically if not provided. | No | - |
| `KARPENTER_MODE` | Operating mode: `lke` or `instance`. See [Modes](#modes) below. | No | `lke` |
| `LINODE_CLIENT_TIMEOUT` | Timeout in seconds for Linode API client requests. | No | - |
| `VM_MEMORY_OVERHEAD_PERCENT`| Additional memory overhead to simplify calculation (0.075 = 7.5%). | No | `0.075` |
| `DISABLE_DRY_RUN` | Set to `true` to disable dry-run validation for LinodeNodeClasses. | No | `false` |

## Modes

The provider can operate in two distinct modes, controlled by the `KARPENTER_MODE` environment variable.

### LKE Mode (`lke`)
*   **Default Mode.**
*   Provisions nodes by creating **LKE Node Pools** with a count of 1.
*   The nodes are automatically joined to your LKE cluster.
*   **Pros**: Simplest setup, managed by LKE.
*   **Cons**: Limited customization (cannot use custom images or advanced networking/storage options usually available to raw Linodes).

### Instance Mode (`instance`)
*   Provisions nodes by creating raw **Linode Instances**.
*   **Status**: ⚠️ **In Development. Not Fully Functional**
    *   This mode represents the roadmap for full instance control but is **not currently functional** for creating working cluster nodes.
    *   It currently **lacks bootstrapping and cluster joining logic** (such as User Data injection). Instances will start but will not join the Kubernetes cluster.
*   **Pros**: Full control over the instance configuration (SSH keys, placement groups, etc.).
*   **Note**: `VPCID` and custom disk configurations are defined in the API but not yet fully implemented in the provider logic.

## LinodeNodeClass Spec

The `LinodeNodeClass` allows you to configure specific settings for the nodes managed by Karpenter. Support for specific fields depends on the [Mode](#modes) you are running.

### Field Reference

| Field | Type | Supported Modes | Description |
|-------|------|-----------------|-------------|
| `tags` | `[]string` | **All** | List of tags to apply to the Instance or Node Pool. |
| `firewallID` | `int` | **All** | The ID of the Cloud Firewall to attach. |
| `lkeK8sVersion` | `string` | **LKE** | Specific Kubernetes version for the node (e.g., "1.29"). This is only available for LKE-E |
| `lkeUpdateStrategy` | `string` | **LKE** | Strategy for pool updates: `rolling_update` or `on_recycle`. This is only available for LKE-E |
| `image` | `string` | **Instance** | The Image ID to deploy (default: `linode/ubuntu22.04`). |
| `authorizedKeys` | `[]string` | **Instance** | SSH Public Keys to add to the `root` user. |
| `authorizedUsers` | `[]string` | **Instance** | List of Linode usernames whose SSH keys will be added. |
| `backupsEnabled` | `bool` | **Instance** | Enable backups service for the instance. |
| `diskEncryption` | `enum` | **Instance** | `enabled` or `disabled`. |
| `swapSize` | `int` | **Instance** | Size of the swap disk in MiB. |
| `placementGroup` | `object` | **Instance** | Place instance in a specific Placement Group (see below). |

### Taints and Standard Labels

It is important to distinguish between Karpenter configuration and Linode-specific configuration:

*   **Taints**: Taints are defined in the **Karpenter `NodePool`** resource (`spec.template.spec.taints`), *not* in the `LinodeNodeClass`.
    *   **LKE Mode**: Taints are passed to the LKE Node Pool configuration and applied to nodes by LKE.
*   **Kubernetes Labels**: Standard scheduling labels are defined in the **Karpenter `NodePool`** (`spec.template.metadata.labels`). Karpenter ensures these are applied to the Node object.
*   **LKE Labels**: LKE Node Pool labels are derived from the labels resolved onto the NodeClaim (originating from NodePool template metadata labels).

### Example: LKE Mode

```yaml
apiVersion: karpenter.k8s.linode/v1alpha1
kind: LinodeNodeClass
metadata:
  name: default
spec:
  tags:
    - "karpenter-node"
    - "env:production"
  firewallID: 12345
```

### Example: Instance Mode

```yaml
apiVersion: karpenter.k8s.linode/v1alpha1
kind: LinodeNodeClass
metadata:
  name: custom-instance
spec:
  tags:
    - "custom-workload"
  image: "linode/ubuntu22.04"
  authorizedUsers:
    - "my-user"
  backupsEnabled: true
  swapSize: 512
  diskEncryption: "enabled"
```

## Selecting nodes

With `nodeSelector` you can ask for a node that matches selected key-value pairs.
This can include well-known labels or custom labels you create yourself.

You can use `affinity` to define more complicated constraints, see [Node Affinity](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#node-affinity) for the complete specification.

### Labels
Well-known labels may be specified as NodePool requirements or pod scheduling constraints. You can also define your own custom labels by specifying `requirements` or `labels` on your NodePool and select them using `nodeAffinity` or `nodeSelectors` on your Pods.

#### Well-Known Labels

| Label                                                          | Example     | Description                                                                                                                                                     |
| -------------------------------------------------------------- | ----------  | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| topology.kubernetes.io/region                                  | us-east     | Regions are defined by your cloud provider ([linode](https://www.linode.com/global-infrastructure/availability/))                     |
| node.kubernetes.io/instance-type                               | g6-dedicated-16| Instance types are defined by your cloud provider ([linode](https://techdocs.akamai.com/cloud-computing/docs/compute-instance-plan-types))                                                           |
| kubernetes.io/os                                               | linux       | Operating systems are defined by [GOOS values](https://github.com/golang/go/blob/master/src/go/build/syslist.go#L10) on the instance                            |
| kubernetes.io/arch                                             | amd64       | Architectures are defined by [GOARCH values](https://github.com/golang/go/blob/master/src/go/build/syslist.go#L50) on the instance                              |
| karpenter.k8s.linode/instance-generation                       | 6           | [Linode Specific] Instance type generation number                                                                                                               |
| karpenter.k8s.linode/instance-cpu                              | 32          | [Linode Specific] Number of CPUs on the instance                                                                                                                |
| karpenter.k8s.linode/instance-memory                           | 32768       | [Linode Specific] Number of megabytes of memory on the instance                                                                                                 |
| karpenter.k8s.linode/instance-disk                             | 655360      | [Linode Specific] Number of megabytes of storage on the instance                                                                                                 |
| karpenter.k8s.linode/instance-gpu-name                         | gtx6000     | [Linode Specific] Name of the GPU on the instance, if available                                                                                                 |
| karpenter.k8s.linode/instance-gpu-count                        | 1           | [Linode Specific] Number of GPUs on the instance                                                                                                                |
| karpenter.k8s.linode/instance-transfer                         | 16000       | [Linode Specific] Number of gigabytes for network transfer                                                                                                      |
| karpenter.k8s.linode/instance-network-out                      | 10000       | [Linode Specific] Number of megabits per second for network outbound bandwidth                                                                                  |
| karpenter.k8s.linode/instance-accelerated-devices-count        | 1           | [Linode Specific] Number of accelerated devices on the instance                                                                                                 |
| karpenter.k8s.linode/instance-class                            | dedicated   | [Linode Specific] Instance class types include `nanode`, `standard`, `dedicated`, `highmem`, and `gpu`                                                          |

#### User-Defined Labels

Karpenter is aware of several well-known labels, deriving them from instance type details. If you specify a `nodeSelector` or a required `nodeAffinity` using a label that is not well-known to Karpenter, it will not launch nodes with these labels and pods will remain pending. For Karpenter to become aware that it can schedule for these labels, you must specify the label in the NodePool requirements with the `Exists` operator:

```yaml
requirements:
  - key: user.defined.label/type
    operator: Exists
```

#### Node selectors

Here is an example of a `nodeSelector` for selecting nodes:

```yaml
nodeSelector:
  topology.kubernetes.io/region: us-east
  karpenter.sh/capacity-type: dedicated
```
This example features a well-known label (`topology.kubernetes.io/region`) and a label that is well known to Karpenter (`karpenter.sh/capacity-type`).

If you want to create a custom label, you should do that at the NodePool level.
Then the pod can declare that custom label.

See [nodeSelector](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#nodeselector) in the Kubernetes documentation for details.
