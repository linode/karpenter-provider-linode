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
| `labels` | `map[string]string`| **LKE** | Labels to apply specifically to the **LKE Node Pool** infrastructure. |
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
*   **LKE Labels**: The `labels` field in `LinodeNodeClass` is specific to **LKE Node Pools** infrastructure. While they often propagate to Kubernetes nodes via the CCM, this field is intended for LKE-level organization.

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
