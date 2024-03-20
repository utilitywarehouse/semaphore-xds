#!/usr/bin/env bash
#
#https://github.com/kubernetes/sample-controller/blob/master/hack/update-codegen.sh
#

set -o errexit
set -o nounset
set -o pipefail

CODEGEN_PKG="/go/src/k8s.io/code-generator/"
source "${CODEGEN_PKG}/kube_codegen.sh"

kube::codegen::gen_helpers \
    --boilerplate hack/boilerplate.go.tmpl \
    --output-base "$(dirname "${BASH_SOURCE[0]}")/../../../.." \
    --input-pkg-root github.com/utilitywarehouse/semaphore-xds/pkg/apis

kube::codegen::gen_client \
    --with-watch \
    --boilerplate hack/boilerplate.go.tmpl \
    --output-base "$(dirname "${BASH_SOURCE[0]}")/../../../.." \
    --output-pkg-root  github.com/utilitywarehouse/semaphore-xds/pkg/generated \
    --input-pkg-root github.com/utilitywarehouse/semaphore-xds/pkg/apis
