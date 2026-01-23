# Karpenter Provider Linode

<p align="center">
<!-- go doc / reference card -->
<a href="https://pkg.go.dev/github.com/linode/karpenter-provider-linode">
<img src="https://pkg.go.dev/badge/github.com/linode/karpenter-provider-linode.svg"></a>
<!-- goreportcard badge -->
<a href="https://goreportcard.com/report/github.com/linode/karpenter-provider-linode">
<img src="https://goreportcard.com/badge/github.com/linode/karpenter-provider-linode"></a>
<!-- join kubernetes slack channel for linode -->
<a href="https://kubernetes.slack.com/messages/CD4B15LUR">
<img src="https://img.shields.io/badge/join%20slack-%23linode-brightgreen"></a>
<!-- PRs welcome -->
<a href="http://makeapullrequest.com">
<img src="https://img.shields.io/badge/PRs-welcome-brightgreen.svg"></a>
</p>
<p align="center">
<!-- go build / test CI -->
<a href="https://github.com/linode/karpenter-provider-linode/actions/workflows/ci.yml">
<img src="https://github.com/linode/karpenter-provider-linode/actions/workflows/ci.yml/badge.svg"></a>
<!-- codecov badge -->
<a href="https://codecov.io/github/linode/karpenter-provider-linode" > 
<img src="https://codecov.io/github/linode/karpenter-provider-linode/graph/badge.svg?token=YQFKF86KJ6"/> 
</a>
<!-- ko build CI -->
<a href="https://github.com/linode/karpenter-provider-linode/actions/workflows/release.yml">
<img src="https://github.com/linode/karpenter-provider-linode/actions/workflows/release.yml/badge.svg"></a>
</p>

---

*PLEASE NOTE*: This project is considered ALPHA quality and should NOT be used for production, as it is currently in active development. Use at your own risk. APIs, configuration file formats, and functionality are all subject to change frequently. That said, please try it out in your development and test environments and let us know if it works. Contributions welcome! Thanks!

---

Table of contents:
- [Features Overview](#features-overview)
- [Installation](#installation)
  - [Install utilities](#install-tools)
  - [Create a cluster](#create-a-cluster)
  - [Configure Helm chart values](#configure-helm-chart-values)
  - [Install Karpenter](#install-karpenter)
- [Using Karpenter](#using-karpenter)
  - [Create NodePool](#create-nodepool)
  - [Scale up deployment](#scale-up-deployment)
  - [Scale down deployment](#scale-down-deployment)
  - [Delete Karpenter nodes manually](#delete-karpenter-nodes-manually)
- [Cleanup](#cleanup)
  - [Delete the cluster](#delete-the-cluster)

## Features Overview
The LKE Karpenter Provider enables node autoprovisioning using [Karpenter](https://karpenter.sh/) on your LKE cluster.
Karpenter improves the efficiency and cost of running workloads on Kubernetes clusters by:

* **Watching** for pods that the Kubernetes scheduler has marked as unschedulable
* **Evaluating** scheduling constraints (resource requests, nodeselectors, affinities, tolerations, and topology spread constraints) requested by the pods
* **Provisioning** nodes that meet the requirements of the pods
* **Removing** the nodes when the nodes are no longer needed

## Provider Modes

This provider supports two operating modes:

1.  **LKE Mode (Default)**: Creates LKE Node Pools for each provisioned node. This is the simplest method and recommended for most users.
2.  **Instance Mode**: Creates standard Linode Instances. This offers granular control over instance settings (SSH keys, placement groups, etc.) but requires more manual configuration. This is currently in **development and not yet fully functional**.

See [Configuration Documentation](docs/CONFIGURATION.md) for full details on modes and available settings.

## Installation

### Install tools

Install these tools before proceeding:
* [kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl/)
* [Helm](https://helm.sh/docs/intro/install/)

### Create a cluster

1. Create a new LKE cluster with any amount of nodes in any region.
This can be easily done in [Linode Cloud Manager](https://cloud.linode.com/) or via the [Linode CLI](https://techdocs.akamai.com/cloud-computing/docs/getting-started-with-the-linode-cli).
2. Download the cluster's kubeconfig when ready.

### Configure Helm chart values

The Karpenter Helm chart requires specific configuration values to work with an LKE cluster.

1. Create a Linode PAT if you don't already have a `LINODE_TOKEN` env var set. Karpenter will use this for managing nodes in the LKE cluster.
2. Set the variables:

    ```bash
    export CLUSTER_NAME=<cluster name>
    export KUBECONFIG=<path to your LKE kubeconfig>
    export KARPENTER_NAMESPACE=kube-system
    export LINODE_TOKEN=<your api token>
    # Optional: specify region explicitly (auto-discovered in LKE mode if not set)
    # export LINODE_REGION=<region>
    # Optional: Set mode directly (default is lke)
    # export KARPENTER_MODE=lke
    ```

**Note**: In LKE mode (default), Karpenter automatically discovers the cluster region from the Linode API using the cluster name. You can optionally set `LINODE_REGION` to override this behavior.

### Install Karpenter

Use the configured environment variables to install Karpenter using Helm:

```bash
helm upgrade --install --namespace "${KARPENTER_NAMESPACE}" --create-namespace karpenter-crd charts/karpenter-crd
helm upgrade --install --namespace "${KARPENTER_NAMESPACE}" --create-namespace karpenter charts/karpenter \
		--set settings.clusterName=${CLUSTER_NAME} \
		--set apiToken=${LINODE_TOKEN} \
        --wait
```

**Optional Configuration:**

- **Region**: Specify the region explicitly (only required for `instance` mode):
  ```bash
  --set region=${LINODE_REGION}
  ```

- **Mode**: Choose the operating mode (default is `lke`):
  - `lke`: Provisions nodes using LKE NodePools (recommended for LKE clusters)
  - `instance`: Provisions nodes as direct Linode instances
  ```bash
  --set settings.mode=lke
  ```

Check karpenter deployed successfully:

```bash
kubectl get pods --namespace "${KARPENTER_NAMESPACE}" -l app.kubernetes.io/name=karpenter
```

Check its logs:

```bash
kubectl logs -f -n "${KARPENTER_NAMESPACE}" -l app.kubernetes.io/name=karpenter -c controller
```

## Using Karpenter

### Create NodePool

A single Karpenter NodePool is capable of handling many different pod shapes. Karpenter makes scheduling and provisioning decisions based on pod attributes such as labels and affinity. In other words, Karpenter eliminates the need to manage many different node groups.

Create a default NodePool using the command below. (Additional examples available in the repository under [`examples/v1`](/examples/v1).) The `consolidationPolicy` set to `WhenUnderutilized` in the `disruption` block configures Karpenter to reduce cluster cost by removing and replacing nodes. As a result, consolidation will terminate any empty nodes on the cluster. This behavior can be disabled by setting consolidateAfter to `Never`, telling Karpenter that it should never consolidate nodes.

Note: This NodePool will create capacity as long as the sum of all created capacity is less than the specified limit.

```bash
cat <<EOF | kubectl apply -f -
---
apiVersion: karpenter.k8s.linode/v1
kind: LinodeNodeClass
metadata:
  name: default
spec:
  image: "linode/ubuntu22.04"
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: default
spec:
  template:
    spec:
      requirements:
        - key: kubernetes.io/arch
          operator: In
          values: ["amd64"]
        - key: kubernetes.io/os
          operator: In
          values: ["linux"]
        - key: karpenter.sh/capacity-type
          operator: In
          values: ["on-demand"]
      nodeClassRef:
        group: karpenter.k8s.linode
        kind: LinodeNodeClass
        name: default
      expireAfter: 720h # 30 * 24h = 720h
  limits:
    cpu: 1000
EOF
```

Karpenter is now active and ready to begin provisioning nodes.

### Scale up deployment

This deployment uses the [pause image](https://www.ianlewis.org/en/almighty-pause-container) and starts with zero replicas.

```bash
cat <<EOF | kubectl apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: inflate
spec:
  replicas: 0
  selector:
    matchLabels:
      app: inflate
  template:
    metadata:
      labels:
        app: inflate
    spec:
      terminationGracePeriodSeconds: 0
      securityContext:
        runAsUser: 1000
        runAsGroup: 3000
        fsGroup: 2000
      containers:
      - name: inflate
        image: public.ecr.aws/eks-distro/kubernetes/pause:3.7
        resources:
          requests:
            cpu: 1
        securityContext:
          allowPrivilegeEscalation: false
EOF

kubectl scale deployment inflate --replicas 5
kubectl logs -f -n "${KARPENTER_NAMESPACE}" -l app.kubernetes.io/name=karpenter -c controller
```

### Scale down deployment

Now, delete the deployment. After a short amount of time, Karpenter should terminate the empty nodes due to consolidation.

```bash
kubectl delete deployment inflate
kubectl logs -f -n "${KARPENTER_NAMESPACE}" -l app.kubernetes.io/name=karpenter -c controller
```

### Delete Karpenter nodes manually

If you delete a node with kubectl, Karpenter will gracefully cordon, drain, and shutdown the corresponding instance. Under the hood, Karpenter adds a finalizer to the node object, which blocks deletion until all pods are drained and the instance is terminated. Keep in mind, this only works for nodes provisioned by Karpenter.

```bash
kubectl delete node $NODE_NAME
```

## Cleanup

### Delete the cluster

To avoid additional charges, remove the demo infrastructure from your Linode account.

```bash
helm uninstall karpenter --namespace "${KARPENTER_NAMESPACE}"
linode-cli lke cluster-delete --label "${CLUSTER_NAME}"
```

---

### Source Attribution

Notice: Files in this source code originated from a fork of https://github.com/aws/karpenter-provider-aws
which is under an Apache 2.0 license. Those files have been modified to reflect environmental requirements in LKE and Linode.

---
### Community, discussion, contribution, and support
This project follows the [Linode Community Code of Conduct](https://www.linode.com/community/questions/conduct). 

Come discuss Karpenter in the [#karpenter](https://kubernetes.slack.com/archives/C02SFFZSA2K) channel in the [Kubernetes slack](https://slack.k8s.io/)!

Check out the [Docs](https://karpenter.sh/) to learn more.

