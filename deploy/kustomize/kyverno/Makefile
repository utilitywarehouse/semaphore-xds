SHELL := /bin/bash
.DEFAULT_GOAL := help

IMAGE_NAME := quay.io/utilitywarehouse/kind-chainsaw-env:main
REPO_DIR := $(shell git rev-parse --show-toplevel)

# Variables for Local Dev
SKIP_DELETE ?= false
REUSE_CLUSTER ?= false
TEST_DIR ?= .

# Point to a local folder (e.g., kyverno/e2e-deploy), a URL, or leave blank to skip
INSTALL_MANIFESTS ?= https://github.com/kyverno/kyverno/raw/main/config/install-latest-testing.yaml
INSTALL_NAMESPACE ?= kyverno
# Leave blank to use the image's baked-in default
CHAINSAW_CONFIG ?=

.PHONY: help
help:
	@echo "Chainsaw Test Runner"
	@echo "============================"
	@echo "Usage: make <target> [VARIABLES]"
	@echo ""
	@echo "Core Targets:"
	@echo "  test-all      Run all chainsaw tests in the repository."
	@echo "  test-target   Run tests for a specific directory. (Requires TEST_DIR=<name>)"
	@echo "  clean         Destroy the persistent kind cluster."
	@echo "  kubectl       Run kubectl commands against the persistent test cluster"
	@echo ""
	@echo "Variables for Local Dev:"
	@echo "  TEST_DIR      Target directory (e.g., TEST_DIR=kyverno/policies/pods)."
	@echo "  REUSE_CLUSTER Set to 'true' to skip cluster teardown."
	@echo "  SKIP_DELETE   Set to 'true' to leave resources in the cluster for debugging."
	@echo ""
	@echo "Debugging (When REUSE_CLUSTER=true):"
	@echo "  To run kubectl commands against the persistent test cluster from your host:"
	@echo "  make kubectl CMD=\"get pods -n kyverno\""
	@echo ""
	@echo "Examples:"
	@echo "  make test-all"
	@echo "  make test-target TEST_DIR=kyverno/policies/pods"
	@echo "  make test-target TEST_DIR=kyverno/policies/pods REUSE_CLUSTER=true"
	@echo "  make kubectl CMD=\"get pods -n kyverno\""

.PHONY: setup-network
setup-network:
	@docker network inspect kind >/dev/null 2>&1 || docker network create kind

.PHONY: pull-image
pull-image:
	@docker pull $(IMAGE_NAME)

.PHONY: test-all
test-all: setup-network
	@echo "Running ALL chainsaw tests..."
	docker run --rm \
		--name chainsaw-runner \
		--network kind \
		-e REUSE_CLUSTER=$(REUSE_CLUSTER) \
		-e SKIP_DELETE=$(SKIP_DELETE) \
		-e INSTALL_MANIFESTS="$(INSTALL_MANIFESTS)" \
		-e INSTALL_NAMESPACE="$(INSTALL_NAMESPACE)" \
		-e CHAINSAW_CONFIG="$(CHAINSAW_CONFIG)" \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v $(REPO_DIR):/repo \
		$(IMAGE_NAME) "."

.PHONY: test-target
test-target: setup-network
	@if [ "$(TEST_DIR)" = "." ]; then \
		echo "Error: TEST_DIR is not set. Usage: make test-target TEST_DIR=argocd"; \
		exit 1; \
	fi
	@echo "Running tests for: $(TEST_DIR)"
	docker run --rm \
		--name chainsaw-runner \
		--network kind \
		-e REUSE_CLUSTER=$(REUSE_CLUSTER) \
		-e SKIP_DELETE=$(SKIP_DELETE) \
		-e INSTALL_MANIFESTS="$(INSTALL_MANIFESTS)" \
		-e INSTALL_NAMESPACE="$(INSTALL_NAMESPACE)" \
		-e CHAINSAW_CONFIG="$(CHAINSAW_CONFIG)" \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v $(REPO_DIR):/repo \
		$(IMAGE_NAME) "$(TEST_DIR)"

.PHONY: clean
clean: setup-network
	@docker run --rm \
		--network kind \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v $(REPO_DIR):/repo \
		$(IMAGE_NAME) "clean"
	@docker stop chainsaw-test-cluster-control-plane chainsaw-runner
	@docker rm chainsaw-test-cluster-control-plane chainsaw-runner

.PHONY: kubectl
kubectl:
	@if [ -z "$(CMD)" ]; then \
		echo "Error: CMD is not set. Usage: make kubectl CMD=\"get ns\""; \
		exit 1; \
	fi
	@docker run --rm -it \
		--network kind \
		-v /var/run/docker.sock:/var/run/docker.sock \
		--entrypoint bash \
		$(IMAGE_NAME) -c "kind get kubeconfig --internal --name chainsaw-test-cluster > /tmp/kubeconfig && KUBECONFIG=/tmp/kubeconfig kubectl $(CMD)"
