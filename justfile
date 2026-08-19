LINODE_REGION := env('LINODE_REGION', 'us-ord')
CLUSTER_NAME := env('CLUSTER_NAME', "karpl-dev")
KUBECONFIG := env('KUBECONFIG', CLUSTER_NAME + "-kubeconfig")
KARPENTER_NAMESPACE := env('KARPENTER_NAMESPACE', 'kube-system')
LINODE_CLI_API_VERSION := env('LINODE_CLI_API_VERSION', "v4beta")
LINODE_CLI_API_HOST := env('LINODE_CLI_API_HOST', "api.linode.com")
LINODE_TYPE := env('LINODE_TYPE', 'g6-standard-1')
NODEPOOL_SIZE := env('NODEPOOL_SIZE', '3')
TILT_MODE := env('TILT_MODE', 'ci')
CHAINSAW_FLAGS := env('CHAINSAW_FLAGS', '--config .chainsaw.yaml')
CHAINSAW_SELECTOR := env('CHAINSAW_SELECTOR', 'all')
CLUSTER_ID := env("CLUSTER_ID", "")
CLUSTER_TIER := env("CLUSTER_TIER", "standard")
CLUSTER_ACL_FLAGS := env("CLUSTER_ACL_FLAGS", '--acl.enabled true --acl.addresses.ipv4=$(curl --fail --silent --show-error https://ipv4.icanhazip.com)')
K8S_VERSION := env("K8S_VERSION", if CLUSTER_TIER == "standard" {
    "1.36"
} else {
    "v1.34.6+lke2"
})

## Inject the app version into operator.Version
WITH_GOFLAGS := "GOFLAGS=\"-ldflags=-X=sigs.k8s.io/karpenter/pkg/operator.Version=$(git describe --tags --always | cut -d\"v\" -f2)\""

KO_DOCKER_REPO := env("KO_DOCKER_REPO", "docker.io/linode/karpenter-provider-linode")
KOCACHE := env("KOCACHE", "~/.ko")
## Image tags to publish; defaults to the current branch name when unset
IMAGE_TAGS := env("IMAGE_TAGS", "")
IMAGE_VERSION := env("IMAGE_VERSION", "")
RELEASE_DIR := env("RELEASE_DIR", "release")
ENVTEST_BIN_DIR := justfile_directory() + "/bin"
ENVTEST_K8S_VERSION := env("ENVTEST_K8S_VERSION")
CLOUD_FIREWALL_CRD_CHART_VERSION := "0.2.0"
CLOUD_FIREWALL_CONTROLLER_CHART_VERSION := "0.2.1"

ONESHELL:

default: help

# List available recipes
help:
	just --list

# Run the full developer loop
presubmit: verify test

# Run unit tests and generate coverage reports
ci-test: test coverage

# Run checks other than tests
ci-non-test: verify vulncheck

# Verify Mise-managed development tools are available
tools:
	#!/usr/bin/env bash
	set -euo pipefail
	for tool in controller-gen govulncheck setup-envtest; do
		command -v "$tool" >/dev/null
	done

# Verify code and regenerate generated artifacts
verify: tools
	#!/usr/bin/env bash
	set -euo pipefail
	for mod_file in $(git ls-files 'go.mod' '**/go.mod' ':!website/**'); do
		(cd "$(dirname "$mod_file")" && go mod tidy && go mod download)
	done
	go generate ./...
	hack/boilerplate.sh
	karpenter_core_dir=$(go list -m -json sigs.k8s.io/karpenter | awk -F '"' '/"Dir"/ { print $4 }')
	cp "$karpenter_core_dir"/pkg/apis/crds/* pkg/apis/crds
	cp pkg/apis/crds/* charts/karpenter-crd/templates
	golangci-lint run
	if ! git diff --quiet; then
		echo "New file modification detected in the Git working tree. Please check in before commit."
		git --no-pager diff --name-only | uniq | awk '{print "  - " $0}'
		if [ "${CI:-}" = true ]; then
			exit 1
		fi
	fi

# Run vulnerability checks
vulncheck: tools
	govulncheck ./pkg/...

# Run unit tests
test: tools
	#!/usr/bin/env bash
	set -euo pipefail
	export KUBEBUILDER_ASSETS="$(setup-envtest use "{{ ENVTEST_K8S_VERSION }}" --bin-dir "{{ ENVTEST_BIN_DIR }}" -p path)"
	go test ./pkg/... -cover -coverprofile=coverage.out -outputdir=. -coverpkg=./...

# Run randomized, racing tests until a failure occurs
deflake:
	ginkgo --race --until-it-fails -v ./pkg/...

# Run local Ginkgo e2e tests against a cluster
ginkgo-e2e test_suite="...": tools
	#!/usr/bin/env bash
	set -euo pipefail
	export KUBEBUILDER_ASSETS="$(setup-envtest use "{{ ENVTEST_K8S_VERSION }}" --bin-dir "{{ ENVTEST_BIN_DIR }}" -p path)"
	cd test
	CLUSTER_ENDPOINT="${CLUSTER_ENDPOINT:-}" \
		CLUSTER_NAME="{{ CLUSTER_NAME }}" \
		INTERRUPTION_QUEUE="{{ CLUSTER_NAME }}" \
		go test -p 1 -count 1 -timeout 3.25h -v "./suites/$(echo '{{ test_suite }}' | tr A-Z a-z)/..." --ginkgo.timeout=3h --ginkgo.grace-period=3m

# Run upstream Karpenter Ginkgo e2e tests against the local cluster
upstream-ginkgo-e2e: tools
	#!/usr/bin/env bash
	set -euo pipefail
	for mod_file in $(git ls-files 'go.mod' '**/go.mod' ':!website/**'); do
		(cd "$(dirname "$mod_file")" && go mod tidy && go mod download)
	done
	tmpfile=$(mktemp)
	trap 'rm -f "$tmpfile"' EXIT
	envsubst < test/pkg/environment/linode/default_linodenodeclass.yaml > "$tmpfile"
	export KUBEBUILDER_ASSETS="$(setup-envtest use "{{ ENVTEST_K8S_VERSION }}" --bin-dir "{{ ENVTEST_BIN_DIR }}" -p path)"
	karpenter_core_dir=$(go list -m -json sigs.k8s.io/karpenter | awk -F '"' '/"Dir"/ { print $4 }')
	CLUSTER_NAME="{{ CLUSTER_NAME }}" go test -count 1 -timeout 3.25h -v "$karpenter_core_dir"/test/suites/... --ginkgo.timeout=3h --ginkgo.grace-period=5m --default-nodeclass="$tmpfile" --default-nodepool="$PWD/test/pkg/environment/linode/default_nodepool.yaml"

# Run local Ginkgo e2e tests repeatedly until a failure occurs
ginkgo-e2e-deflake test_suite="...":
	cd test && CLUSTER_NAME="{{ CLUSTER_NAME }}" ginkgo --timeout=3h --grace-period=3m --until-it-fails --vv "./suites/$(echo '{{ test_suite }}' | tr A-Z a-z)"

# Run performance benchmarks
benchmark: tools
	#!/usr/bin/env bash
	set -euo pipefail
	export KUBEBUILDER_ASSETS="$(setup-envtest use "{{ ENVTEST_K8S_VERSION }}" --bin-dir "{{ ENVTEST_BIN_DIR }}" -p path)"
	go test -tags=test_performance -run=NoTests -bench=. ./...

# Generate the HTML coverage report
coverage:
	go tool cover -html coverage.out -o coverage.html

# Run the controller against the local cluster
run:
	SYSTEM_NAMESPACE="{{ KARPENTER_NAMESPACE }}" KUBERNETES_MIN_VERSION="1.19.0-0" DISABLE_LEADER_ELECTION=true CLUSTER_NAME="{{ CLUSTER_NAME }}" INTERRUPTION_QUEUE="{{ CLUSTER_NAME }}" LOG_LEVEL=debug {{ WITH_GOFLAGS }} go run ./cmd/controller/main.go

# Stamp a version into the chart metadata and controller image tag
set-chart-version version=IMAGE_VERSION:
	#!/usr/bin/env bash
	set -euo pipefail
	if [ -z "{{ version }}" ]; then
		echo "IMAGE_VERSION is required, e.g. just set-chart-version v0.1.6" >&2
		exit 1
	fi
	for chart in ./charts/*/Chart.yaml; do
		IMAGE_VERSION="{{ version }}" yq -i '.version = strenv(IMAGE_VERSION) | .appVersion = strenv(IMAGE_VERSION)' "$chart"
	done
	IMAGE_VERSION="{{ version }}" yq -i '.controller.image.tag = strenv(IMAGE_VERSION)' ./charts/karpenter/values.yaml

# Package the Helm charts into the release directory
release version=IMAGE_VERSION: (set-chart-version version)
	mkdir -p "{{ RELEASE_DIR }}"
	tar -czvf "{{ RELEASE_DIR }}/karpenter-crd-{{ version }}.tgz" -C ./charts/karpenter-crd .
	tar -czvf "{{ RELEASE_DIR }}/karpenter-{{ version }}.tgz" -C ./charts/karpenter .

# Build the controller binary
binary:
	{{ WITH_GOFLAGS }} go build -o "karpenter-provider-linode-$(go env GOARCH)" ./cmd/controller/...

# Install Karpenter onto the current Kubernetes cluster
helm-install:
	helm upgrade --install --namespace karpenter --create-namespace karpenter-crd charts/karpenter-crd
	helm upgrade --install --namespace karpenter --create-namespace karpenter charts/karpenter --set controller.image.repository="{{ KO_DOCKER_REPO }}" --set settings.clusterName="{{ CLUSTER_NAME }}" --set apiToken="${LINODE_TOKEN:?LINODE_TOKEN is required}"

# Remove Karpenter from the current Kubernetes cluster
helm-uninstall:
	helm uninstall karpenter -n karpenter
	helm uninstall karpenter-crd -n karpenter

# Create an LKE test cluster
create-lke-cluster:
	#!/usr/bin/env bash
	set -euo pipefail
	export LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }}
	export LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }}
	existing_id=$(linode-cli lke clusters-list --label '{{ CLUSTER_NAME }}' --format id --text | sed '1d')
	if [ -n "$existing_id" ]; then
		echo "LKE cluster '{{ CLUSTER_NAME }}' already exists (id: $existing_id); skipping create"
		exit 0
	fi
	linode-cli lke cluster-create \
		--label '{{ CLUSTER_NAME }}' \
		--region '{{ LINODE_REGION }}' \
		--k8s_version {{ K8S_VERSION }} \
		--node_pools.type {{ LINODE_TYPE }} \
		--node_pools.count {{ NODEPOOL_SIZE }} \
		--tier {{ CLUSTER_TIER }} \
		--no-defaults

# Retrying logic to wait for LKE cluster kubeconfig to be ready
wait-for-lke-cluster-readiness cluster_id:
	#!/usr/bin/env bash
	set -euo pipefail
	export LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }}
	export LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }}
	until OUTPUT=$(linode-cli lke kubeconfig-view "{{ cluster_id }}" --text 2>&1) && ! echo "$OUTPUT" | grep -q 503; do
		echo "Kubeconfig is not ready yet, retrying in 10s..."
		sleep 10
	done

# Get the kubeconfig for your LKE cluster
get-lke-kubeconfig cluster_id: (wait-for-lke-cluster-readiness cluster_id)
	#!/usr/bin/env bash
	set -euo pipefail
	export LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }}
	export LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }}
	linode-cli lke kubeconfig-view {{ cluster_id }} --text | sed '1d' | base64 -d > {{ KUBECONFIG }}
	chmod 0600 {{ KUBECONFIG }}

# Wait for the Kubernetes API to accept requests after ACL/kubeconfig changes
wait-for-lke-kube-api:
	#!/usr/bin/env bash
	set -euo pipefail
	export KUBECONFIG={{ KUBECONFIG }}
	for _ in $(seq 1 12); do
		if kubectl get --raw=/version >/dev/null 2>&1; then
			exit 0
		fi
		echo "Kubernetes API is not reachable yet, retrying in 5s..."
		sleep 5
	done
	echo "Timed out waiting for Kubernetes API reachability"
	exit 1

# Get the ID of your LKE development cluster
get-lke-cluster-id:
	#!/usr/bin/env bash
	set -euo pipefail
	export LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }}
	export LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }}
	linode-cli lke clusters-list --label '{{ CLUSTER_NAME }}' --format id --text | sed '1d'

init-lke-cluster:
	#!/usr/bin/env bash
	set -euo pipefail
	export LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }}
	export LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }}
	CLUSTER_ID=$(linode-cli lke clusters-list --label '{{ CLUSTER_NAME }}' --format id --text | sed '1d')
	if [ -z "$CLUSTER_ID" ]; then
		echo "Unable to determine LKE cluster ID for '{{ CLUSTER_NAME }}'"
		exit 1
	fi
	linode-cli lke cluster-acl-update "$CLUSTER_ID" {{ CLUSTER_ACL_FLAGS }}
	just get-lke-kubeconfig $CLUSTER_ID
	just wait-for-lke-kube-api

# Destroy your LKE test cluster
destroy-lke-cluster cluster_id:
	#!/usr/bin/env bash
	set -euo pipefail
	export KUBECONFIG={{ KUBECONFIG }}
	export LINODE_CLI_API_VERSION={{ LINODE_CLI_API_VERSION }}
	export LINODE_CLI_API_HOST={{ LINODE_CLI_API_HOST }}
	if [ "{{ CLUSTER_TIER }}" = "standard" ] && [ -f "{{ KUBECONFIG }}" ]; then
		if kubectl get crd/cloudfirewalls.networking.linode.com >/dev/null 2>&1; then
			kubectl -n kube-system delete \
				cloudfirewall.networking.linode.com/primary \
				--ignore-not-found=true
			kubectl -n kube-system wait \
				--for=delete cloudfirewall.networking.linode.com/primary \
				--timeout=5m || true
		fi
	fi
	linode-cli lke cluster-delete '{{ cluster_id }}'
	rm -f {{ KUBECONFIG }}

# Build and push the controller image with ko
build-karpl-image:
	#!/usr/bin/env bash
	set -euo pipefail
	tags="{{ IMAGE_TAGS }}"
	if [ -z "$tags" ]; then
		tags=$(git rev-parse --abbrev-ref HEAD)
	fi
	{{ WITH_GOFLAGS }} KOCACHE={{ KOCACHE }} KO_DOCKER_REPO={{ KO_DOCKER_REPO }} \
		ko build --bare --tags "$tags" github.com/linode/karpenter-provider-linode/cmd/controller

# Run tilt against the LKE cluster in kubeconfig
run-tilt-lke:
	#!/usr/bin/env bash
	set -euo pipefail
	export KUBECONFIG={{ KUBECONFIG }}
	tilt {{ TILT_MODE }}

# Run tilt down against the LKE cluster in kubeconfig
cleanup-tilt-lke:
	tilt down

# Install the cloud firewall controller for standard-tier LKE clusters
install-cloud-firewall-controller:
	#!/usr/bin/env bash
	set -euo pipefail
	export KUBECONFIG={{ KUBECONFIG }}
	if [ "{{ CLUSTER_TIER }}" != "standard" ]; then
		echo "Skipping cloud firewall install for cluster tier '{{ CLUSTER_TIER }}'"
		exit 0
	fi
	helm repo add linode-cfw https://linode.github.io/cloud-firewall-controller
	helm repo update linode-cfw
	helm upgrade --install cloud-firewall-crd \
		linode-cfw/cloud-firewall-crd \
		--namespace kube-system \
		--create-namespace \
		--version {{ CLOUD_FIREWALL_CRD_CHART_VERSION }} \
		--wait
	kubectl wait --for=condition=established --timeout=60s crd/cloudfirewalls.networking.linode.com
	helm upgrade --install cloud-firewall-controller \
		linode-cfw/cloud-firewall-controller \
		--namespace kube-system \
		--create-namespace \
		--version {{ CLOUD_FIREWALL_CONTROLLER_CHART_VERSION }} \
		--set-json 'firewall={"inbound":[]}' \
		--wait
	if ! kubectl -n kube-system rollout status deployment/cloud-firewall-controller --timeout=5m; then
		kubectl -n kube-system get deployment cloud-firewall-controller -o yaml
		kubectl -n kube-system logs deployment/cloud-firewall-controller --tail=100
		exit 1
	fi
	if ! kubectl -n kube-system get cloudfirewall.networking.linode.com/primary -o yaml; then
		kubectl -n kube-system logs deployment/cloud-firewall-controller --tail=100
		exit 1
	fi

# Configures the vanilla LKE cluster with KARPL code
configure-lke-cluster: init-lke-cluster install-cloud-firewall-controller run-tilt-lke

# Collect useful diagnostics for E2E failures
collect-e2e-diagnostics:
	#!/usr/bin/env bash
	set +e
	export KUBECONFIG={{ KUBECONFIG }}
	echo "=== NodeClaims ==="
	kubectl get nodeclaims -A -o wide
	echo "=== Nodes ==="
	kubectl get nodes -o wide
	echo "=== Events (last 50) ==="
	kubectl get events -A --sort-by=.lastTimestamp | tail -n 50
	echo "=== Karpenter logs ==="
	kubectl -n kube-system logs -l app.kubernetes.io/name=karpenter --tail=100

# Cleanup common test leftovers and enforce a clean NodeClaim starting point
pre-e2e-cleanup-and-sanity:
	#!/usr/bin/env bash
	set -euo pipefail
	export KUBECONFIG={{ KUBECONFIG }}
	kubectl -n default delete deployment -l e2e.linode.dev/cleanup=true --ignore-not-found=true
	kubectl -n default delete pod -l e2e.linode.dev/cleanup=true --ignore-not-found=true
	kubectl delete nodepool -l e2e.linode.dev/cleanup=true --ignore-not-found=true
	kubectl delete linodenodeclass -l e2e.linode.dev/cleanup=true --ignore-not-found=true
	kubectl delete nodeclaims --all --ignore-not-found=true
	for _ in $(seq 1 10); do
		count=$(kubectl get nodeclaims -o jsonpath='{.items[*].metadata.name}' | wc -w | tr -d ' ')
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
	export KUBECONFIG={{ KUBECONFIG }}
	deployment_name=$(kubectl -n kube-system get deployment -l app.kubernetes.io/name=karpenter -o jsonpath='{.items[0].metadata.name}')
	if [ -z "$deployment_name" ]; then
		echo "Unable to locate Karpenter deployment in kube-system"
		just collect-e2e-diagnostics
		exit 1
	fi
	echo "Restarting deployment/$deployment_name in kube-system"
	kubectl -n kube-system rollout restart deployment/"$deployment_name"
	if ! kubectl -n kube-system rollout status deployment/"$deployment_name" --timeout=5m; then
		echo "Karpenter rollout did not become healthy"
		just collect-e2e-diagnostics
		exit 1
	fi

# Run chainsaw tests on an existing LKE cluster
run-e2e:
	#!/usr/bin/env bash
	set -euo pipefail
	export KUBECONFIG={{ KUBECONFIG }}
	chainsaw test e2e --selector {{ CHAINSAW_SELECTOR }} {{ CHAINSAW_FLAGS }}

# Set up and run e2e tests
setup-and-test-e2e: create-lke-cluster configure-lke-cluster pre-e2e-cleanup-and-sanity restart-karpenter-before-e2e run-e2e
