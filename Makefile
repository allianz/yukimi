# ====================================================================================
# Setup Project
PROJECT_NAME := yukimi
PROJECT_REPO := github.com/allianz/$(PROJECT_NAME)

PLATFORMS ?= linux_amd64 linux_arm64

# ====================================================================================
# Tool Versions
#
# Every version-pinned tool this Makefile downloads or invokes, in one place. Tools
# pinned in build/makelib/k8s_tools.mk (kind, kubectl, the Crossplane CLI, ...) live
# in that submodule instead, since it is shared build tooling rather than ours to edit.
GOLANGCILINT_VERSION = 2.13.2
OSV_SCANNER_VERSION := v2.6.0
GOMPLATE_VERSION := 3.10.0

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
-include build/makelib/golang.mk

# ====================================================================================
# Setup Kubernetes tools

-include build/makelib/k8s_tools.mk

# ====================================================================================
# Setup Manifests

# Render package/ into a single applyable file, with the controller image set to
# MANIFESTS_IMAGE (kustomize edit writes it into package/kustomization.yaml).
MANIFESTS_IMAGE ?= localhost/yukimi:latest

manifests: $(KUSTOMIZE)
	@$(INFO) kustomize build package
	@mkdir -p $(OUTPUT_DIR)
	@cd $(ROOT_DIR)/package && $(KUSTOMIZE) edit set image localhost/yukimi=$(MANIFESTS_IMAGE) || $(FAIL)
	@$(KUSTOMIZE) build $(ROOT_DIR)/package > $(OUTPUT_DIR)/install.yaml || $(FAIL)
	@$(OK) kustomize build package

# done, not run: it must follow the CRD regeneration in package/crds.
generate.done: manifests

.PHONY: manifests

# ====================================================================================
# Setup Images

IMAGES = yukimi
# `make publish` pushes $(REGISTRY_ORGS)/yukimi:$(VERSION) for all PLATFORMS. Any
# branch: the release workflow runs it from the detached release tag.
REGISTRY_ORGS = ghcr.io/allianz
RELEASE_BRANCH_FILTER = %
-include build/makelib/imagelight.mk

# Set Docker registry for local development
DOCKER_REGISTRY := localhost

# Tag the host-arch image as localhost/yukimi:latest, which package/ deploys.
img.tag.local:
	@docker tag $(BUILD_REGISTRY)/yukimi-$(ARCH) localhost/yukimi:latest
ifeq ($(ARCH),$(SAFEHOSTARCH))
build.done: img.tag.local
endif

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

# This is for running out-of-cluster locally with the binary. Use if you want to test the build
run: go.build
	@$(INFO) Running Crossplane locally out-of-cluster . . .
	@# To see other arguments that can be provided, run the command with --help instead
	$(GO_OUT_DIR)/provider --debug

# Run a local development cluster with kind and install the CRDs and run the controllers
# locally. Useful for development.
dev: $(KIND) $(KUBECTL) $(GOMPLATE)
	@if ! $(KIND) get clusters | grep -q "$(PROJECT_NAME)-dev"; then \
		$(INFO) "Creating kind cluster $(PROJECT_NAME)-dev"; \
		$(KIND) create cluster --name=$(PROJECT_NAME)-dev; \
	else \
		$(INFO) "Using existing kind cluster $(PROJECT_NAME)-dev"; \
	fi
	@$(KUBECTL) cluster-info --context kind-$(PROJECT_NAME)-dev
	@$(INFO) Installing Yukimi CRDs
	@$(KUBECTL) apply -f $(ROOT_DIR)/package/crds/
	@$(INFO) Generating local-dev config files from .env
	@$(ROOT_DIR)/hack/dev/generate-config.sh
	@$(INFO) Labeling tenant namespace for testing
	@set -a && source .env && set +a && $(KUBECTL) create namespace $${SAMPLE_CUSTOMER_NAMESPACE} --dry-run=client -o yaml | $(KUBECTL) apply -f - && $(KUBECTL) label namespace $${SAMPLE_CUSTOMER_NAMESPACE} department="az_tech" costcenter="121212" --overwrite
	@$(INFO) Switching kubectl default namespace to tenant namespace
	@set -a && source .env && set +a && $(KUBECTL) config set-context --current --namespace=$${SAMPLE_CUSTOMER_NAMESPACE}
	@$(INFO) Starting Yukimi controllers
	@set -a && source .env && set +a && $(GO) run cmd/provider/main.go --debug --configDir=$(ROOT_DIR)/_output/config

dev-clean: $(KIND) $(KUBECTL)
	@$(INFO) Deleting kind cluster
	@$(KIND) delete cluster --name=$(PROJECT_NAME)-dev

.PHONY: submodules fallthrough run dev dev-clean promote tag release.tag e2e e2e.run test-integration

# ====================================================================================
# Disabled Targets

# Override promote and tag targets to disable them
promote:
	@echo "ERROR: 'make promote' has been disabled for this project"
	@exit 1

tag:
	@echo "ERROR: 'make tag' has been disabled for this project"
	@exit 1

release.tag:
	@echo "ERROR: 'make release.tag' has been disabled for this project"
	@exit 1

# There is no end-to-end suite. The template's e2e machinery deploys a built
# package to a kind cluster, which cannot work here: the package is not published
# and the controller needs a real Snowflake organization and AWS Secrets Manager
# to get past startup. Anything needing those is covered by `make
# test-integration` instead, which is why those tests are guarded by
# testing.Short() rather than run in CI.
#
# Both targets fail loudly rather than being deleted, because the submodule
# defines them and a silent inherited definition is worse than an explicit no.
e2e:
	@echo "ERROR: 'make e2e' is not supported for this project -- there is no e2e suite."
	@echo "       For tests against real Snowflake and AWS, use 'make test-integration'."
	@exit 1

e2e.run: e2e

test-integration:
	@$(INFO) go test integration-tests
	@mkdir -p $(GO_TEST_OUTPUT)
	@CGO_ENABLED=$(GO_CGO_ENABLED) $(GOHOST) test -v -run Integration $(GO_TEST_FLAGS) $(GO_STATIC_FLAGS) $(GO_PACKAGES) 2>&1 | tee $(GO_TEST_OUTPUT)/integration-tests.log || $(FAIL)
	@$(OK) go test integration-tests

# Override go.test.unit to add -short flag for skipping integration tests
go.test.unit:
	@$(INFO) go test unit-tests
	@mkdir -p $(GO_TEST_OUTPUT)
	@CGO_ENABLED=$(GO_CGO_ENABLED) $(GOHOST) test -short -cover $(GO_STATIC_FLAGS) $(GO_PACKAGES) || $(FAIL)
	@CGO_ENABLED=$(GO_CGO_ENABLED) $(GOHOST) test -short -v -covermode=$(GO_COVER_MODE) -coverprofile=$(GO_TEST_OUTPUT)/coverage.txt $(GO_TEST_FLAGS) $(GO_STATIC_FLAGS) $(GO_PACKAGES) 2>&1 | tee $(GO_TEST_OUTPUT)/unit-tests.log || $(FAIL)
	@$(OK) go test unit-tests

# ====================================================================================
# Setup Security Scanning

# Exits non-zero on any unsuppressed vulnerability against the OSV database. A
# finding is fixed by upgrading, or -- if it genuinely does not apply -- by adding
# an [[IgnoredVulns]] entry to osv-scanner.toml with a reason that says how that
# was established, in the style of the entry already there.

osv-scan:
	@$(INFO) osv-scanner
	@go run github.com/google/osv-scanner/v2/cmd/osv-scanner@$(OSV_SCANNER_VERSION) scan source --config=osv-scanner.toml . || $(FAIL)
	@$(OK) osv-scanner

lint.run: osv-scan

.PHONY: osv-scan

# ====================================================================================
# Special Targets

# Install gomplate
GOMPLATE := $(TOOLS_HOST_DIR)/gomplate-$(GOMPLATE_VERSION)

$(GOMPLATE):
	@$(INFO) installing gomplate $(SAFEHOSTPLATFORM)
	@mkdir -p $(TOOLS_HOST_DIR)
	@curl -fsSLo $(GOMPLATE) https://github.com/hairyhenderson/gomplate/releases/download/v$(GOMPLATE_VERSION)/gomplate_$(SAFEHOSTPLATFORM) || $(FAIL)
	@chmod +x $(GOMPLATE)
	@$(OK) installing gomplate $(SAFEHOSTPLATFORM)

export GOMPLATE

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

Manifest Targets:
    manifests             Render package/ into _output/install.yaml.

Test Targets:
    test                  Unit tests only (-short, which skips the integration tests).
    test-integration      Integration tests only. Needs real AWS and Snowflake access via .env.

endef
# The reason CROSSPLANE_MAKE_HELP is used instead of CROSSPLANE_HELP is because the crossplane
# binary will try to use CROSSPLANE_HELP if it is set, and this is for something different.
export CROSSPLANE_MAKE_HELP

crossplane.help:
	@echo "$$CROSSPLANE_MAKE_HELP"

help-special: crossplane.help

.PHONY: crossplane.help help-special
