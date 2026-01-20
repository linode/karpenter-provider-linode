CLUSTER_NAME ?= $(shell kubectl config view --minify -o jsonpath='{.clusters[].name}' | rev | cut -d"/" -f1 | rev | cut -d"." -f1)
ENVTEST_K8S_VERSION := $(shell go list -m -f '{{.Version}}' k8s.io/client-go)

## Inject the app version into operator.Version
LDFLAGS ?= -ldflags=-X=sigs.k8s.io/karpenter/pkg/operator.Version=$(shell git describe --tags --always | cut -d"v" -f2)

GOFLAGS ?= $(LDFLAGS)
WITH_GOFLAGS = GOFLAGS="$(GOFLAGS)"

# CR for local builds of Karpenter
KARPENTER_NAMESPACE ?= kube-system
KARPENTER_VERSION ?= $(shell git tag --sort=committerdate | tail -1 | cut -d"v" -f2)
KO_DOCKER_REPO ?= docker.io/linode/karpenter-provider-linode
KOCACHE ?= ~/.ko

# Common Directories
MOD_DIRS = $(shell find . -path "./website" -prune -o -name go.mod -type f -print | xargs dirname)
KARPENTER_CORE_DIR = $(shell go list -m -f '{{ .Dir }}' sigs.k8s.io/karpenter)
RELEASE_DIR ?= release

# TEST_SUITE enables you to select a specific test suite directory to run "make e2etests" against
TEST_SUITE ?= "..."
TMPFILE := $(shell mktemp)

# Filename when building the binary controller only
GOARCH ?= $(shell go env GOARCH)
BINARY_FILENAME = karpenter-provider-linode-$(GOARCH)

help: ## Display help
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

presubmit: verify test ## Run all steps in the developer loop

ci-test: test coverage ## Runs tests and submits coverage

ci-non-test: verify vulncheck ## Runs checks other than tests

run: ## Run Karpenter controller binary against your local cluster
	SYSTEM_NAMESPACE=${KARPENTER_NAMESPACE} \
		KUBERNETES_MIN_VERSION="1.19.0-0" \
		DISABLE_LEADER_ELECTION=true \
		CLUSTER_NAME=${CLUSTER_NAME} \
		INTERRUPTION_QUEUE=${CLUSTER_NAME} \
		LOG_LEVEL="debug" \
		go run ./cmd/controller/main.go

test: envtest ## Run tests
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use ${ENVTEST_K8S_VERSION#v} --bin-dir $(CACHE_BIN) -p path)" \
		go test ./pkg/... \
		-cover -coverprofile=coverage.out -outputdir=. -coverpkg=./...

deflake: ## Run randomized, racing tests until the test fails to catch flakes
	ginkgo \
		--race \
		--until-it-fails \
		-v \
		./pkg/...

e2etests: envtest ## Run the e2e suite against your local cluster
	cd test && CLUSTER_ENDPOINT=${CLUSTER_ENDPOINT} \
		CLUSTER_NAME=${CLUSTER_NAME} \
		INTERRUPTION_QUEUE=${CLUSTER_NAME} \
		KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use ${ENVTEST_K8S_VERSION#v} --bin-dir $(CACHE_BIN) -p path)" \
		go test \
		-p 1 \
		-count 1 \
		-timeout 3.25h \
		-v \
		./suites/$(shell echo $(TEST_SUITE) | tr A-Z a-z)/... \
		--ginkgo.timeout=3h \
		--ginkgo.grace-period=3m

upstream-e2etests: tidy download envtest
	CLUSTER_NAME=${CLUSTER_NAME} envsubst < $(shell pwd)/test/pkg/environment/linode/default_linodenodeclass.yaml > ${TMPFILE}
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use ${ENVTEST_K8S_VERSION#v} --bin-dir $(CACHE_BIN) -p path)" \
	go test \
		-count 1 \
		-timeout 3.25h \
		-v \
		$(KARPENTER_CORE_DIR)/test/suites/... \
		--ginkgo.timeout=3h \
		--ginkgo.grace-period=5m \
		--default-nodeclass="$(TMPFILE)"\
		--default-nodepool="$(shell pwd)/test/pkg/environment/linode/default_nodepool.yaml"

e2etests-deflake: ## Run the e2e suite against your local cluster
	cd test && CLUSTER_NAME=${CLUSTER_NAME} ginkgo \
		--timeout=3h \
		--grace-period=3m \
		--until-it-fails \
		--vv \
		./suites/$(shell echo $(TEST_SUITE) | tr A-Z a-z) \

benchmark: envtest
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use ${ENVTEST_K8S_VERSION#v} --bin-dir $(CACHE_BIN) -p path)" \
		go test -tags=test_performance -run=NoTests -bench=. ./...

coverage:
	go tool cover -html coverage.out -o coverage.html

verify: tidy download controller-gen ## Verify code. Includes dependencies, linting, formatting, etc
	go generate ./...
	hack/boilerplate.sh
	cp  $(KARPENTER_CORE_DIR)/pkg/apis/crds/* pkg/apis/crds
	# cp pkg/apis/crds/* charts/karpenter-crd/templates
	$(foreach dir,$(MOD_DIRS),cd $(dir) && golangci-lint run $(newline))
	@git diff --quiet ||\
		{ echo "New file modification detected in the Git working tree. Please check in before commit."; git --no-pager diff --name-only | uniq | awk '{print "  - " $$0}'; \
		if [ "${CI}" = true ]; then\
			exit 1;\
		fi;}

vulncheck: ## Verify code vulnerabilities
	@govulncheck ./pkg/...

image: ## Build the Karpenter controller images using ko build
	$(eval CONTROLLER_IMG=$(shell $(WITH_GOFLAGS) KOCACHE=$(KOCACHE) KO_DOCKER_REPO="$(KO_DOCKER_REPO)" ko build --bare github.com/linode/karpenter-provider-linode/cmd/controller))
	$(eval IMG_REPOSITORY=$(shell echo $(CONTROLLER_IMG) | cut -d "@" -f 1 | cut -d ":" -f 1))
	$(eval IMG_TAG=$(shell echo $(CONTROLLER_IMG) | cut -d "@" -f 1 | cut -d ":" -f 2 -s))
	$(eval IMG_DIGEST=$(shell echo $(CONTROLLER_IMG) | cut -d "@" -f 2))

release:
	mkdir -p $(RELEASE_DIR)
	sed -e 's/appVersion: "latest"/appVersion: "$(IMAGE_VERSION)"/g' ./chart/*/Chart.yaml
	tar -czvf ./$(RELEASE_DIR)/karpenter-crd-$(IMAGE_VERSION).tgz -C ./chart/karpenter-crd .
	tar -czvf ./$(RELEASE_DIR)/karpenter-$(IMAGE_VERSION).tgz -C ./chart/karpenter .

binary: ## Build the Karpenter controller binary using go build
	go build $(GOFLAGS) -o $(BINARY_FILENAME) ./cmd/controller/...

tidy: ## Recursively "go mod tidy" on all directories where go.mod exists
	$(foreach dir,$(MOD_DIRS),cd $(dir) && go mod tidy $(newline))

download: ## Recursively "go mod download" on all directories where go.mod exists
	$(foreach dir,$(MOD_DIRS),cd $(dir) && go mod download $(newline))

update-karpenter: ## Update kubernetes-sigs/karpenter to latest
	go get -u sigs.k8s.io/karpenter@HEAD
	go mod tidy

helm-install: ## install karpenter onto an existing cluster (requires k8s context to be set)
	@helm upgrade --install --namespace karpenter --create-namespace karpenter-crd charts/karpenter-crd
	@helm upgrade --install --namespace karpenter --create-namespace karpenter charts/karpenter \
		--set controller.image.repository=$(KO_DOCKER_REPO) \
		--set region=${LINODE_REGION} \
		--set settings.clusterName=${CLUSTER_NAME} \
		--set apiToken=${LINODE_TOKEN}

helm-uninstall: ## remove both charts from the existing cluster (requires k8s context to be set)
	@helm uninstall karpenter -n karpenter
	@helm uninstall karpenter-crd -n karpenter

.PHONY: help presubmit ci-test ci-non-test run test deflake e2etests e2etests-deflake benchmark coverage verify vulncheck image apply install delete docgen codegen tidy download update-karpenter

## --------------------------------------
## Build Dependencies
## --------------------------------------

##@ Build Dependencies:

## Location to install dependencies to

# Use CACHE_BIN for tools that cannot use devbox
CACHE_BIN ?= $(CURDIR)/bin

# if the $DEVBOX_PACKAGES_DIR env variable exists that means we are within a devbox shell and can safely
# use devbox's bin for our tools
ifdef DEVBOX_PACKAGES_DIR
	CACHE_BIN = $(DEVBOX_PACKAGES_DIR)/bin
endif

export PATH := $(CACHE_BIN):$(PATH)
$(CACHE_BIN):
	mkdir -p $(CACHE_BIN)


## --------------------------------------
## Tooling Binaries
## --------------------------------------

##@ Tooling Binaries:
# setup-envtest does not have devbox support so always use CACHE_BIN

ENVTEST   ?= $(CACHE_BIN)/setup-envtest
CONTROLLER_GEN ?= $(CACHE_BIN)/controller-gen

## Tool Versions
# renovate: datasource=go depName=sigs.k8s.io/controller-runtime/tools/setup-envtest
ENVTEST_VERSION ?= release-0.22

# renovate: datasource=go depName=sigs.k8s.io/controller-tools
CONTROLLER_TOOLS_VERSION ?= v0.19.0

.PHONY: tools
tools: $(CONTROLLER_GEN) $(ENVTEST)

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(CACHE_BIN)
	GOBIN=$(CACHE_BIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	GOBIN=$(CACHE_BIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

define newline


endef
