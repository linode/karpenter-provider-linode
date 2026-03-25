LINODE_REGION := env('LINODE_REGION', 'us-ord')
CLUSTER_NAME := env('CLUSTER_NAME', "karpl-dev")
KUBECONFIG := env('KUBECONFIG', CLUSTER_NAME + "-kubeconfig")
LINODE_CLI_API_VERSION := env('LINODE_CLI_API_VERSION', "v4beta")
LINODE_CLI_API_HOST := env('LINODE_CLI_API_HOST', "api.linode.com")
LINODE_TYPE := env('LINODE_TYPE', 'g6-standard-1')
BOOTSTRAP_NODEPOOL_COUNT := env('BOOTSTRAP_NODEPOOL_COUNT', '3')
TILT_MODE := env('TILT_MODE', 'ci')
CHAINSAW_FLAGS := env('CHAINSAW_FLAGS', '--config .chainsaw.yaml')
CHAINSAW_SELECTOR := env('CHAINSAW_SELECTOR', 'all')
CLUSTER_ID := env("CLUSTER_ID", "")
CLUSTER_TIER := env("CLUSTER_TIER", "standard")
CLUSTER_ACL_FLAGS := env("CLUSTER_ACL_FLAGS", '--acl.enabled true --acl.addresses.ipv4=$(curl --silent ipv4.icanhazip.com)')
K8S_VERSION := env("K8S_VERSION", if CLUSTER_TIER == "standard" {
    "1.34"
} else {
    "v1.31.9+lke7"
})

## Inject the app version into operator.Version
WITH_GOFLAGS := "GOFLAGS=\"-ldflags=-X=sigs.k8s.io/karpenter/pkg/operator.Version=$(git describe --tags --always | cut -d\"v\" -f2)\""

KO_DOCKER_REPO := env("KO_DOCKER_REPO", "docker.io/linode/karpenter-provider-linode")
KOCACHE := env("KOCACHE", "~/.ko")

ONESHELL:

# Create an LKE test cluster
create-lke-cluster:
	set -euo pipefail; \
	existing_id=$(LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }} LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }} linode-cli lke clusters-list --label '{{ CLUSTER_NAME }}' --format id --text | sed '1d'); \
	if [ -n "$existing_id" ]; then echo "LKE cluster '{{ CLUSTER_NAME }}' already exists (id: $existing_id); skipping create"; exit 0; fi; \
	LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }} LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }} linode-cli lke cluster-create --label '{{ CLUSTER_NAME }}' --region '{{ LINODE_REGION }}' --k8s_version {{ K8S_VERSION }} --node_pools.type {{ LINODE_TYPE }} --node_pools.count {{ BOOTSTRAP_NODEPOOL_COUNT }} --tier {{ CLUSTER_TIER }} --no-defaults

# Retrying logic to wait for LKE cluster kubeconfig to be ready
wait-for-lke-cluster-readiness cluster_id:
	until OUTPUT=$(LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }} LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }} linode-cli lke kubeconfig-view "{{ cluster_id }}" --text 2>&1) && ! echo "$OUTPUT" | grep -q 503; do echo "Kubeconfig is not ready yet, retrying in 10s..."; sleep 10; done

# Get the kubeconfig for your LKE cluster
get-lke-kubeconfig cluster_id: (wait-for-lke-cluster-readiness cluster_id)
	LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }} LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }} linode-cli lke kubeconfig-view {{ cluster_id }} --text | sed '1d' | base64 -d > {{ KUBECONFIG }} && chmod 0600 {{ KUBECONFIG }}

# Get the ID of your LKE development cluster
get-lke-cluster-id:
	@LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }} LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }} linode-cli lke clusters-list --label '{{ CLUSTER_NAME }}' --format id --text | sed '1d'

init-lke-cluster:
	#!/usr/bin/env bash
	set -euo pipefail
	CLUSTER_ID=$(LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }} LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }} linode-cli lke clusters-list --label '{{ CLUSTER_NAME }}' --format id --text | sed '1d')
	if [ -z "$CLUSTER_ID" ]; then
		echo "Unable to determine LKE cluster ID for '{{ CLUSTER_NAME }}'"
		exit 1
	fi
	LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }} LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }} linode-cli lke cluster-acl-update "$CLUSTER_ID" {{ CLUSTER_ACL_FLAGS }}
	just get-lke-kubeconfig $CLUSTER_ID

# Destroy your LKE test cluster
destroy-lke-cluster cluster_id:
	LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }} LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }} linode-cli lke cluster-delete '{{ cluster_id }}'
	-rm {{ KUBECONFIG }}

build-karpl-image:
	{{ WITH_GOFLAGS }} KOCACHE={{ KOCACHE }} KO_DOCKER_REPO={{ KO_DOCKER_REPO }} ko build -t $(git rev-parse --abbrev-ref HEAD) --bare github.com/linode/karpenter-provider-linode/cmd/controller

# Run tilt against the LKE cluster in kubeconfig
run-tilt-lke: build-karpl-image
	KUBECONFIG={{ KUBECONFIG }} tilt {{ TILT_MODE }}

# Run tilt down against the LKE cluster in kubeconfig
cleanup-tilt-lke:
	tilt down

# Configures the vanilla LKE cluster with KARPL code
configure-lke-cluster: init-lke-cluster run-tilt-lke

# Collect useful diagnostics for E2E failures
collect-e2e-diagnostics:
	#!/usr/bin/env bash
	set +e
	echo "=== NodeClaims ==="
	KUBECONFIG={{ KUBECONFIG }} kubectl get nodeclaims -A -o wide
	echo "=== Nodes ==="
	KUBECONFIG={{ KUBECONFIG }} kubectl get nodes -o wide
	echo "=== Events (last 50) ==="
	KUBECONFIG={{ KUBECONFIG }} kubectl get events -A --sort-by=.lastTimestamp | tail -n 50
	echo "=== Karpenter logs ==="
	KUBECONFIG={{ KUBECONFIG }} kubectl -n kube-system logs -l app.kubernetes.io/name=karpenter --tail=100

# Cleanup common test leftovers and enforce a clean NodeClaim starting point
pre-e2e-cleanup-and-sanity:
	#!/usr/bin/env bash
	set -euo pipefail
	KUBECONFIG={{ KUBECONFIG }} kubectl -n default delete deployment -l e2e.linode.dev/cleanup=true --ignore-not-found=true
	KUBECONFIG={{ KUBECONFIG }} kubectl -n default delete pod -l e2e.linode.dev/cleanup=true --ignore-not-found=true
	KUBECONFIG={{ KUBECONFIG }} kubectl delete nodepool -l e2e.linode.dev/cleanup=true --ignore-not-found=true
	KUBECONFIG={{ KUBECONFIG }} kubectl delete linodenodeclass -l e2e.linode.dev/cleanup=true --ignore-not-found=true
	KUBECONFIG={{ KUBECONFIG }} kubectl delete nodeclaims --all --ignore-not-found=true
	for _ in $(seq 1 10); do
		count=$(KUBECONFIG={{ KUBECONFIG }} kubectl get nodeclaims -o jsonpath='{.items[*].metadata.name}' | wc -w | tr -d ' ')
		if [ "$count" = "0" ]; then
			echo "NodeClaims are clean"
			exit 0
		fi
		echo "Waiting for NodeClaims to be deleted (remaining: $count)"
		sleep 5
	done
	echo "Timed out waiting for NodeClaims to be deleted"
	just collect-e2e-diagnostics
	exit 1

# Restart Karpenter so reused clusters pick up the latest image before tests
restart-karpenter-before-e2e:
	#!/usr/bin/env bash
	set -euo pipefail
	deployment_name=$(KUBECONFIG={{ KUBECONFIG }} kubectl -n kube-system get deployment -l app.kubernetes.io/name=karpenter -o jsonpath='{.items[0].metadata.name}')
	if [ -z "$deployment_name" ]; then
		echo "Unable to locate Karpenter deployment in kube-system"
		just collect-e2e-diagnostics
		exit 1
	fi
	echo "Restarting deployment/$deployment_name in kube-system"
	KUBECONFIG={{ KUBECONFIG }} kubectl -n kube-system rollout restart deployment/"$deployment_name"
	if ! KUBECONFIG={{ KUBECONFIG }} kubectl -n kube-system rollout status deployment/"$deployment_name" --timeout=5m; then
		echo "Karpenter rollout did not become healthy"
		just collect-e2e-diagnostics
		exit 1
	fi

# Run chainsaw tests on an existing LKE cluster
run-e2e:
	KUBECONFIG={{ KUBECONFIG }} chainsaw test e2e --selector {{ CHAINSAW_SELECTOR }} {{ CHAINSAW_FLAGS }}

# Set up and run e2e tests
setup-and-test-e2e: create-lke-cluster configure-lke-cluster pre-e2e-cleanup-and-sanity restart-karpenter-before-e2e run-e2e
