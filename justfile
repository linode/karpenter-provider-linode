LINODE_REGION := env('LINODE_REGION', 'us-ord')
CLUSTER_NAME := env('CLUSTER_NAME', "karpl-dev")
KUBECONFIG := env('KUBECONFIG', CLUSTER_NAME + "-kubeconfig")
LINODE_CLI_API_VERSION := env('LINODE_CLI_API_VERSION', "v4")
LINODE_CLI_API_HOST := env('LINODE_CLI_API_HOST', "api.linode.com")
LINODE_TYPE := env('LINODE_TYPE', 'g6-standard-2')
TILT_MODE := env('TILT_MODE', 'ci')
CHAINSAW_FLAGS := env('CHAINSAW_FLAGS', '--config .chainsaw.yaml')
CLUSTER_ID := env("CLUSTER_ID", "")
CLUSTER_TIER := env("CLUSTER_TIER", "standard")
CLUSTER_FLAGS := if CLUSTER_TIER == "standard" {
    ""
} else {
    "--tier enterprise"
}
K8S_VERSION := if CLUSTER_TIER == "standard" {
    "1.34"
} else {
    "v1.31.9+lke7"
}

ONESHELL:

# Create an LKE test cluster
create-lke-cluster:
	set -eu; \
	existing_id=$(linode-cli lke clusters-list --label '{{ CLUSTER_NAME }}' --format id --text | sed '1d'); \
	if [ -n "$existing_id" ]; then echo "LKE cluster '{{ CLUSTER_NAME }}' already exists (id: $existing_id); skipping create"; exit 0; fi; \
	linode-cli lke cluster-create --label '{{ CLUSTER_NAME }}' --region '{{ LINODE_REGION }}' --k8s_version {{ K8S_VERSION }} --node_pools.type {{ LINODE_TYPE }} --node_pools.count 3 {{ CLUSTER_FLAGS }} --no-defaults

# Retrying logic to wait for LKE cluster kubeconfig to be ready
wait-for-lke-cluster-readiness cluster_id:
	until OUTPUT=$(linode-cli lke kubeconfig-view "{{ cluster_id }}" --text 2>&1) && ! echo "$OUTPUT" | grep -q 503; do echo "Kubeconfig is not ready yet, retrying in 10s..."; sleep 10; done

# Get the kubeconfig for your LKE cluster
get-lke-kubeconfig cluster_id: (wait-for-lke-cluster-readiness cluster_id)
	linode-cli lke kubeconfig-view {{ cluster_id }} --text | sed '1d' | base64 -d > {{ KUBECONFIG }} && chmod 0600 {{ KUBECONFIG }}

# Get the ID of your LKE development cluster
get-lke-cluster-id:
	@linode-cli lke clusters-list --label '{{ CLUSTER_NAME }}' --format id --text | sed '1d'

init-lke-cluster:
	#!/usr/bin/env bash
	set -e -o pipefail
	CLUSTER_ID=$(just get-lke-cluster-id)
	just get-lke-kubeconfig $CLUSTER_ID

# Destroy your LKE test cluster
destroy-lke-cluster cluster_id:
	linode-cli lke cluster-delete '{{ cluster_id }}'
	-rm {{ KUBECONFIG }}

# Run tilt against the LKE cluster in kubeconfig
run-tilt-lke:
	KUBECONFIG={{ KUBECONFIG }} tilt {{ TILT_MODE }}

# Run tilt down against the LKE cluster in kubeconfig
cleanup-tilt-lke:
	tilt down

# Configures the vanilla LKE cluster with KARPL code
configure-lke-cluster: init-lke-cluster run-tilt-lke

# Run chainsaw tests on an existing LKE cluster
run-e2e:
	KUBECONFIG={{ KUBECONFIG }} chainsaw test e2e {{ CHAINSAW_FLAGS }}

# Set up and run e2e tests
setup-and-test-e2e: create-lke-cluster configure-lke-cluster run-e2e
