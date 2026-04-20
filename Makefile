REG ?= ghcr.io/oracle
BUILD_NUM?=local
GIT_SHA?=$(shell git describe --dirty --always)
TAG_PREFIX=v0.0.1
VERSION=$(TAG_PREFIX)-$(GIT_SHA)-$(BUILD_NUM)
IMG ?= $(REG)/karpenter-provider-oci:$(VERSION)
export GOTOOLCHAIN ?= go1.26.1
CGO_ENABLED?=0
BUILDOPTS:=-v
GOOS ?= linux
ARCH ?= amd64

MOD_DIRS = $(shell find . -name go.mod -type f ! -path "./test/*" | xargs dirname)
KARPENTER_CRD_SOURCE_DIR = vendor/sigs.k8s.io/karpenter/pkg/apis/crds

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development
verify: toolchain tidy download fmt sync-karpenter-crds gogen docs-gen-api docs-gen-helm addlicense-apply addlicense-check test ## Verify code. Includes dependencies, linting, formatting, etc

	$(foreach dir,$(MOD_DIRS),cd $(dir) && golangci-lint run $(newline))
	@git diff --quiet ||\
		{ echo "New file modification detected in the Git working tree. Please check in before commit."; git --no-pager diff --name-only | uniq | awk '{print "  - " $$0}'; \
		if [ "${CI}" = true ]; then\
			exit 1;\
		fi;}
	#actionlint -oneline

toolchain: ## Install developer toolchain
	./hack/toolchain.sh

.PHONY: addlicense-check
addlicense-check: ## Check files for missing UPL header (excludes vendor/bin)
	$(GOBIN)/addlicense -check -f hack/upl-header.tmpl -ignore 'vendor/**' -ignore 'bin/**' .

.PHONY: addlicense-apply
addlicense-apply: ## Add UPL header to files (excludes vendor/bin)
	$(GOBIN)/addlicense -f hack/upl-header.tmpl -ignore 'vendor/**' -ignore 'bin/**'  .

.PHONY: gogen
gogen: controller-gen ## Run go generate with local tools on PATH (generates CRDs, etc.)
	PATH="$(LOCALBIN):$$PATH" go generate -skip="./npn/..." ./...

.PHONY: sync-karpenter-crds
sync-karpenter-crds: ## Copy upstream Karpenter CRDs into pkg/apis/crds
	test -d "$(KARPENTER_CRD_SOURCE_DIR)"
	cp "$(KARPENTER_CRD_SOURCE_DIR)/karpenter.sh_nodepools.yaml" pkg/apis/crds/karpenter.sh_nodepools.yaml
	cp "$(KARPENTER_CRD_SOURCE_DIR)/karpenter.sh_nodeclaims.yaml" pkg/apis/crds/karpenter.sh_nodeclaims.yaml
	cp "$(KARPENTER_CRD_SOURCE_DIR)/karpenter.sh_nodeoverlays.yaml" pkg/apis/crds/karpenter.sh_nodeoverlays.yaml

tidy: ## Recursively "go mod tidy" on all directories where go.mod exists
	$(foreach dir,$(MOD_DIRS),cd $(dir) && go mod tidy $(newline))

download: ## Recursively "go mod download" on all directories where go.mod exists
	$(foreach dir,$(MOD_DIRS),cd $(dir) && go mod download $(newline))


.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: fmt vet setup-envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test $$(go list ./... | grep -v '/e2e\|/apis\|/fakes') -coverprofile cover.out

.PHONY: test-e2e
test-e2e: fmt vet
	$(MAKE) test-e2e-flannel
	if [ -n "$${KUBECONFIG_NPN}" ]; then $(MAKE) test-e2e-npn; fi

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	$(GOLANGCI_LINT) run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	$(GOLANGCI_LINT) config verify

##@ Build

.PHONY: build
build: fmt vet ## Build manager binary.
	GOOS=$(GOOS) GOARCH=$(ARCH) CGO_ENABLED=$(CGO_ENABLED) go build -o bin/manager cmd/main.go

.PHONY: run
run: fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name karpenter-provider-oci-builder
	$(CONTAINER_TOOL) buildx use karpenter-provider-oci-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm karpenter-provider-oci-builder
	rm Dockerfile.cross

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint

## Tool Versions
CONTROLLER_TOOLS_VERSION ?= v0.20.0
#ENVTEST_VERSION is the version of controller-runtime release branch to fetch the envtest setup script (i.e. release-0.20)
ENVTEST_VERSION ?= $(shell go list -m -f "{{ .Version }}" sigs.k8s.io/controller-runtime | awk -F'[v.]' '{printf "release-%d.%d", $$2, $$3}')
#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell go list -m -f "{{ .Version }}" k8s.io/api | awk -F'[v.]' '{printf "1.%d", $$3}')
GOLANGCI_LINT_VERSION ?= v1.63.4

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@$(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f $(1) || true ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
mv $(1) $(1)-$(3) ;\
} ;\
ln -sf $(1)-$(3) $(1)
endef


.PHONY: test-e2e-flannel
test-e2e-flannel:
	./hack/installAndValidateKarpenter.sh "$${KUBECONFIG}" "$${KARPENTER_CHART_TGZ}" "test/e2e/testdata/e2e_test_helm_values_flannel.yaml"; \
	{ KUBECONFIG="$${KUBECONFIG}" go test -v ./test/e2e -run TestKarpenterE2EFlannel -timeout=180m; test_exit=$$?; \
	  if [ "$${SKIP_CLEANUP}" = "true" ]; then echo "SKIP_CLEANUP=true; skipping helm uninstall"; else helm uninstall karpenter; kubectl patch ocinodeclasses.oci.oraclecloud.com --all --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true; kubectl patch nodeclaims.karpenter.sh --all --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true; kubectl patch nodepools.karpenter.sh --all --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true; kubectl patch crd ocinodeclasses.oci.oraclecloud.com nodeclaims.karpenter.sh nodepools.karpenter.sh --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true; kubectl delete crd ocinodeclasses.oci.oraclecloud.com nodeclaims.karpenter.sh nodepools.karpenter.sh --ignore-not-found=true; fi; \
	  exit $$test_exit; } \
	|| { if [ "$${SKIP_CLEANUP}" = "true" ]; then echo "SKIP_CLEANUP=true; skipping helm uninstall"; else helm uninstall karpenter; kubectl patch ocinodeclasses.oci.oraclecloud.com --all --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true; kubectl patch nodeclaims.karpenter.sh --all --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true; kubectl patch nodepools.karpenter.sh --all --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true; kubectl patch crd ocinodeclasses.oci.oraclecloud.com nodeclaims.karpenter.sh nodepools.karpenter.sh --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true; kubectl delete crd ocinodeclasses.oci.oraclecloud.com nodeclaims.karpenter.sh nodepools.karpenter.sh --ignore-not-found=true; fi; exit 1; }

.PHONY: test-e2e-npn
test-e2e-npn:
	./hack/installAndValidateKarpenter.sh "$${KUBECONFIG_NPN}" "$${KARPENTER_CHART_TGZ}" "test/e2e/testdata/e2e_test_helm_values_npn.yaml"; \
	{ KUBECONFIG_NPN="$${KUBECONFIG_NPN}" go test -v ./test/e2e -run TestKarpenterE2ENpn -timeout=180m; test_exit=$$?; \
	  if [ "$${SKIP_CLEANUP}" = "true" ]; then echo "SKIP_CLEANUP=true; skipping helm uninstall"; else helm uninstall karpenter; kubectl patch ocinodeclasses.oci.oraclecloud.com --all --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true; kubectl patch nodeclaims.karpenter.sh --all --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true; kubectl patch nodepools.karpenter.sh --all --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true; kubectl patch crd ocinodeclasses.oci.oraclecloud.com nodeclaims.karpenter.sh nodepools.karpenter.sh --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true; kubectl delete crd ocinodeclasses.oci.oraclecloud.com nodeclaims.karpenter.sh nodepools.karpenter.sh --ignore-not-found=true; fi; \
	  exit $$test_exit; } \
	|| { if [ "$${SKIP_CLEANUP}" = "true" ]; then echo "SKIP_CLEANUP=true; skipping helm uninstall"; else helm uninstall karpenter; kubectl patch ocinodeclasses.oci.oraclecloud.com --all --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true; kubectl patch nodeclaims.karpenter.sh --all --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true; kubectl patch nodepools.karpenter.sh --all --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true; kubectl patch crd ocinodeclasses.oci.oraclecloud.com nodeclaims.karpenter.sh nodepools.karpenter.sh --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true; kubectl delete crd ocinodeclasses.oci.oraclecloud.com nodeclaims.karpenter.sh nodepools.karpenter.sh --ignore-not-found=true; fi; exit 1; }

.PHONY: docs-gen-api
docs-gen-api: ## Generate OCI v1beta1 API docs (Markdown)
	bash hack/gen-api-ref.sh

.PHONY: docs-gen-helm
docs-gen-helm: ## Generate Helm chart docs (Markdown)
	bash hack/gen-helm-ref.sh
