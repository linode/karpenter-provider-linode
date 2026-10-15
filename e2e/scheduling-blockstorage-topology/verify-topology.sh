#!/usr/bin/env bash
set -euo pipefail

namespace=$1
pod_name=$2
nodepool_name=$3

node_name=$(kubectl -n "$namespace" get pod "$pod_name" -o jsonpath='{.spec.nodeName}')
if [ -z "$node_name" ]; then
  echo "Pod '$pod_name' has not been assigned to a node" >&2
  exit 1
fi

csi_region=$(kubectl get node "$node_name" -o jsonpath='{.metadata.labels.topology\.linode\.com/region}')
kubernetes_region=$(kubectl get node "$node_name" -o jsonpath='{.metadata.labels.topology\.kubernetes\.io/region}')
nodeclaim_name=$(kubectl get nodeclaim -l "karpenter.sh/nodepool=$nodepool_name" -o jsonpath='{.items[0].metadata.name}')
if [ -z "$nodeclaim_name" ]; then
  echo "No NodeClaim found for NodePool '$nodepool_name'" >&2
  exit 1
fi

predicted_region=$(kubectl get nodeclaim "$nodeclaim_name" -o jsonpath='{.spec.requirements[?(@.key=="topology.linode.com/region")].values[0]}')

if [ -z "$csi_region" ] || [ -z "$kubernetes_region" ] || [ -z "$predicted_region" ]; then
  echo "Missing CSI, Kubernetes, or predicted region label" >&2
  exit 1
fi

if [ "$csi_region" != "$kubernetes_region" ] || [ "$csi_region" != "$predicted_region" ]; then
  echo "CSI region '$csi_region', Kubernetes region '$kubernetes_region', and predicted region '$predicted_region' do not match" >&2
  exit 1
fi

for claim_name in blockstorage-unconstrained blockstorage-explicit; do
  pvc_phase=$(kubectl -n "$namespace" get pvc "$claim_name" -o jsonpath='{.status.phase}')
  pv_name=$(kubectl -n "$namespace" get pvc "$claim_name" -o jsonpath='{.spec.volumeName}')
  if [ "$pvc_phase" != "Bound" ] || [ -z "$pv_name" ]; then
    echo "PVC '$claim_name' is in phase '$pvc_phase' with PV '$pv_name'" >&2
    exit 1
  fi

  pv_region=$(kubectl get pv "$pv_name" -o jsonpath='{.spec.nodeAffinity.required.nodeSelectorTerms[*].matchExpressions[?(@.key=="topology.linode.com/region")].values[*]}')
  if [ "$pv_region" != "$csi_region" ]; then
    echo "PVC '$claim_name' has PV region '$pv_region', expected '$csi_region'" >&2
    exit 1
  fi
done

printf 'Pod %s on %s matched predicted and CSI region %s\n' "$pod_name" "$node_name" "$csi_region"
