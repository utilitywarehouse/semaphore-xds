SHELL := /bin/bash

CODEGEN_IMAGE := kubernetes-codegen:latest
CURRENT_DIR := $(shell pwd)
KUBE_CODE_GEN_VERSION := v0.29.1
KUBE_CONTROLLER_TOOLS_VERSION := v0.14.0
PROJECT_MODULE := github.com/utilitywarehouse/semaphore-xds
UID := $(shell id -u)
GID := $(shell id -g)

release:
	@sd "newTag: master" "newTag: $(VERSION)" deploy/kustomize/namespaced/kustomization.yaml
	@git add deploy/kustomize/namespaced/kustomization.yaml
	@git commit -m "Release $(VERSION)"
	@git tag -m "Release $(VERSION)" -a $(VERSION)
	@sd "newTag: $(VERSION)" "newTag: master" deploy/kustomize/namespaced/kustomization.yaml
	@git add deploy/kustomize/namespaced/kustomization.yaml
	@git commit -m "Clean up release $(VERSION)"

codegen-build:
	docker build --build-arg KUBE_VERSION=${KUBE_CODE_GEN_VERSION} \
		--build-arg CONTROLLER_TOOLS_VERSION=${KUBE_CONTROLLER_TOOLS_VERSION} \
		--build-arg USER="${USER}" \
		--build-arg UID="${UID}" \
		--build-arg GID="${GID}" \
		-f "./codegen.Dockerfile" \
		-t "${CODEGEN_IMAGE}" \
		"."

generate-code: codegen-build
	docker run --rm \
		-v "${CURRENT_DIR}:/go/src/${PROJECT_MODULE}" \
		-w "/go/src/${PROJECT_MODULE}" \
		"${CODEGEN_IMAGE}" \
		./hack/update-codegen.sh

generate-manifests: codegen-build
	docker run --rm \
		-v "${CURRENT_DIR}:/go/src/${PROJECT_MODULE}" \
		-w "/go/src/${PROJECT_MODULE}" \
		"${CODEGEN_IMAGE}" \
		controller-gen crd:crdVersions=v1 paths=./... output:crd:artifacts:config=deploy/kustomize/cluster
	@{ \
	cd deploy/kustomize/cluster ;\
	kustomize edit add resource semaphore-xds.uw.systems_* ;\
	}

generate: generate-code generate-manifests
