# ====================================================================================
# Setup Project
PROJECT_NAME := yukimi
PROJECT_REPO := github.com/allianz/$(PROJECT_NAME)

PLATFORMS ?= linux_amd64 linux_arm64
-include build/makelib/common.mk

# ====================================================================================
# Setup Output

-include build/makelib/output.mk

# ====================================================================================
# Setup Go

NPROCS ?= 1
GO_TEST_PARALLEL := $(shell echo $$(( $(NPROCS) / 2 )))
GO_STATIC_PACKAGES = $(GO_PROJECT)/cmd/provider
GO_LDFLAGS += -X $(GO_PROJECT)/internal/version.Version=$(VERSION)
GO_SUBDIRS += cmd internal apis
GO111MODULE = on
GOLANGCILINT_VERSION = 2.1.2
-include build/makelib/golang.mk

# ====================================================================================
# Setup Kubernetes tools

-include build/makelib/k8s_tools.mk

# ====================================================================================
# Setup Images

IMAGES = yukimi
-include build/makelib/imagelight.mk

# Set Docker registry for local development
DOCKER_REGISTRY := localhost

# ====================================================================================
# Setup XPKG

XPKG_REG_ORGS ?= xpkg.upbound.io/crossplane
# NOTE(hasheddan): skip promoting on xpkg.upbound.io as channel tags are
# inferred.
XPKG_REG_ORGS_NO_PROMOTE ?= xpkg.upbound.io/crossplane
XPKGS = yukimi
-include build/makelib/xpkg.mk

# NOTE(hasheddan): we force image building to happen prior to xpkg build so that
# we ensure image is present in daemon.
xpkg.build.yukimi: do.build.images

fallthrough: submodules
	@echo Initial setup complete. Running make again . . .
	@make

# Update the submodules, such as the common build scripts.
submodules:
	@git submodule sync
	@git submodule update --init --recursive

# NOTE(hasheddan): the build submodule currently overrides XDG_CACHE_HOME in
# order to force the Helm 3 to use the .work/helm directory. This causes Go on
# Linux machines to use that directory as the build cache as well. We should
# adjust this behavior in the build submodule because it is also causing Linux
# users to duplicate their build cache, but for now we just make it easier to
# identify its location in CI so that we cache between builds.
go.cachedir:
	@go env GOCACHE

go.mod.cachedir:
	@go env GOMODCACHE

# NOTE(hasheddan): we must ensure up is installed in tool cache prior to build
# as including the k8s_tools machinery prior to the xpkg machinery sets UP to
# point to tool cache.
build.init: $(CROSSPLANE_CLI)

# This is for running out-of-cluster locally with the binary. Use if you want to test the build
run: go.build
	@$(INFO) Running Crossplane locally out-of-cluster . . .
	@# To see other arguments that can be provided, run the command with --help instead
	$(GO_OUT_DIR)/provider --debug

# Run a local development cluster with kind and install the CRDs and run the controllers
# locally. Useful for development.
dev: $(KIND) $(KUBECTL)
	@if ! $(KIND) get clusters | grep -q "$(PROJECT_NAME)-dev"; then \
		$(INFO) "Creating kind cluster $(PROJECT_NAME)-dev"; \
		$(KIND) create cluster --name=$(PROJECT_NAME)-dev; \
	else \
		$(INFO) "Using existing kind cluster $(PROJECT_NAME)-dev"; \
	fi
	@$(KUBECTL) cluster-info --context kind-$(PROJECT_NAME)-dev
	@$(INFO) Setting up AWS credentials from local profile
	@KUBECTL=$(KUBECTL) $(ROOT_DIR)/cluster/local/sync-aws-credentials.sh
	@$(INFO) Labeling tenant namespace for testing
	@set -a && source .env && set +a && $(KUBECTL) create namespace $${SAMPLE_CUSTOMER_NAMESPACE} --dry-run=client -o yaml | $(KUBECTL) apply -f - && $(KUBECTL) label namespace $${SAMPLE_CUSTOMER_NAMESPACE} department="az_tech" costcenter="121212" --overwrite
	@$(INFO) Switching kubectl default namespace to tenant namespace
	@set -a && source .env && set +a && $(KUBECTL) config set-context --current --namespace=$${SAMPLE_CUSTOMER_NAMESPACE}
	@$(INFO) Generating local provider config from .env
	@$(ROOT_DIR)/cluster/local/generate-local-config.sh
	@$(INFO) Starting Yukimi controllers
	@$(GO) run cmd/provider/main.go --configDir=$(ROOT_DIR)/local/config --debug

dev-clean: $(KIND) $(KUBECTL)
	@$(INFO) Deleting kind cluster
	@$(KIND) delete cluster --name=$(PROJECT_NAME)-dev

.PHONY: submodules fallthrough run dev dev-clean publish promote tag release.tag e2e.automated e2e.manual

# ====================================================================================
# Disabled Targets

# Override publish, promote, and tag targets to disable them
publish:
	@echo "ERROR: 'make publish' has been disabled for this project"
	@exit 1

promote:
	@echo "ERROR: 'make promote' has been disabled for this project"
	@exit 1

tag:
	@echo "ERROR: 'make tag' has been disabled for this project"
	@exit 1

release.tag:
	@echo "ERROR: 'make tag' has been disabled for this project"
	@exit 1

e2e:
	@echo "ERROR: 'make e2e' has been disabled for this project"
	@exit 1

e2e.run:
	@echo "ERROR: 'make e2e.run' has been disabled for this project"
	@echo "Use 'make e2e' or 'make e2e.automated' instead"
	@exit 1

test-integration:
	@$(INFO) go test integration-tests
	@mkdir -p $(GO_TEST_OUTPUT)
	@CGO_ENABLED=$(GO_CGO_ENABLED) $(GOHOST) test -v -run Integration $(GO_TEST_FLAGS) $(GO_STATIC_FLAGS) $(GO_PACKAGES) 2>&1 | tee $(GO_TEST_OUTPUT)/integration-tests.log || $(FAIL)
	@$(OK) go test integration-tests

# New e2e test targets
e2e.automated:
	@$(INFO) Running fully automated e2e tests
	@$(ROOT_DIR)/cluster/local/e2e_automated.sh || $(FAIL)
	@$(OK) e2e tests passed

e2e.manual:
	@$(INFO) Running e2e tests against running provider
	@$(INFO) Make sure 'make dev' is running in another terminal
	@$(ROOT_DIR)/test/e2e/e2e_tests.sh || $(FAIL)
	@$(OK) e2e tests passed

# Override go.test.unit to add -short flag for skipping integration tests
go.test.unit:
	@$(INFO) go test unit-tests
	@mkdir -p $(GO_TEST_OUTPUT)
	@CGO_ENABLED=$(GO_CGO_ENABLED) $(GOHOST) test -short -cover $(GO_STATIC_FLAGS) $(GO_PACKAGES) || $(FAIL)
	@CGO_ENABLED=$(GO_CGO_ENABLED) $(GOHOST) test -short -v -covermode=$(GO_COVER_MODE) -coverprofile=$(GO_TEST_OUTPUT)/coverage.txt $(GO_TEST_FLAGS) $(GO_STATIC_FLAGS) $(GO_PACKAGES) 2>&1 | tee $(GO_TEST_OUTPUT)/unit-tests.log || $(FAIL)
	@$(OK) go test unit-tests

# ====================================================================================
# Special Targets

# Install gomplate
GOMPLATE_VERSION := 3.10.0
GOMPLATE := $(TOOLS_HOST_DIR)/gomplate-$(GOMPLATE_VERSION)

$(GOMPLATE):
	@$(INFO) installing gomplate $(SAFEHOSTPLATFORM)
	@mkdir -p $(TOOLS_HOST_DIR)
	@curl -fsSLo $(GOMPLATE) https://github.com/hairyhenderson/gomplate/releases/download/v$(GOMPLATE_VERSION)/gomplate_$(SAFEHOSTPLATFORM) || $(FAIL)
	@chmod +x $(GOMPLATE)
	@$(OK) installing gomplate $(SAFEHOSTPLATFORM)

export GOMPLATE

# This target prepares repo for your provider by replacing all "snowflake"
# occurrences with your provider name.
# This target can only be run once, if you want to rerun for some reason,
# consider stashing/resetting your git state.
# Arguments:
#   provider: Camel case name of your provider, e.g. GitHub, PlanetScale
#provider.prepare:
#	@[ "${provider}" ] || ( echo "argument \"provider\" is not set"; exit 1 )
#	@PROVIDER=$(provider) ./hack/helpers/prepare.sh

# This target adds a new api type and its controller.
# You would still need to register new api in "apis/<provider>.go" and
# controller in "internal/controller/<provider>.go".
# Arguments:
#   provider: Camel case name of your provider, e.g. GitHub, PlanetScale
#   group: API group for the type you want to add.
#   kind: Kind of the type you want to add
#	apiversion: API version of the type you want to add. Optional and defaults to "v1alpha1"
provider.addtype: $(GOMPLATE)
	@[ "${provider}" ] || ( echo "argument \"provider\" is not set"; exit 1 )
	@[ "${group}" ] || ( echo "argument \"group\" is not set"; exit 1 )
	@[ "${kind}" ] || ( echo "argument \"kind\" is not set"; exit 1 )
	@PROVIDER=$(provider) GROUP=$(group) KIND=$(kind) APIVERSION=$(apiversion) PROJECT_REPO=$(PROJECT_REPO) ./hack/helpers/addtype.sh

define CROSSPLANE_MAKE_HELP
Crossplane Targets:
    submodules            Update the submodules, such as the common build scripts.
    run                   Run crossplane locally, out-of-cluster. Useful for development.

endef
# The reason CROSSPLANE_MAKE_HELP is used instead of CROSSPLANE_HELP is because the crossplane
# binary will try to use CROSSPLANE_HELP if it is set, and this is for something different.
export CROSSPLANE_MAKE_HELP

crossplane.help:
	@echo "$$CROSSPLANE_MAKE_HELP"

help-special: crossplane.help

.PHONY: crossplane.help help-special
