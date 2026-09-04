#!/usr/bin/env bash
# Karpenter Provider OCI
#
# Copyright (c) 2026 Oracle and/or its affiliates.
# Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/

set -euo pipefail

K8S_VERSION="${K8S_VERSION:="1.29.x"}"
KUBEBUILDER_ASSETS="/usr/local/kubebuilder/bin"
TOOLCHAIN_FILE="hack/toolchain.sh"
MAKEFILE="Makefile"
K8S_DOWNLOAD_VERSION="v1.21.14"
ADDLICENSE_VERSION="v1.2.0"
GOLANGCI_LINT_VERSION="v1.64.8"
HELM_DOCS_VERSION="v1.14.2"
SETUP_ENVTEST_VERSION="v0.24.1"
CONTROLLER_TOOLS_VERSION="v0.22.0"
GINKGO_VERSION="v2.32.1"
CRDDOC_VERSION="v0.0.0-20260901012920-fc2383cd5109"

main() {
    case "${1:-install}" in
        install)
            tools
            kubebuilder
            ;;
        update-versions)
            update_versions
            ;;
        *)
            echo "Usage: $0 [install|update-versions]" >&2
            exit 1
            ;;
    esac
}

tools() {
    go install "github.com/google/addlicense@${ADDLICENSE_VERSION}"
    go install "github.com/golangci/golangci-lint/cmd/golangci-lint@${GOLANGCI_LINT_VERSION}"
    go install "github.com/norwoodj/helm-docs/cmd/helm-docs@${HELM_DOCS_VERSION}"
    go install "sigs.k8s.io/controller-runtime/tools/setup-envtest@${SETUP_ENVTEST_VERSION}"
    go install "sigs.k8s.io/controller-tools/cmd/controller-gen@${CONTROLLER_TOOLS_VERSION}"
    go install "github.com/onsi/ginkgo/v2/ginkgo@${GINKGO_VERSION}"
    go install "github.com/theunrepentantgeek/crddoc@${CRDDOC_VERSION}"

    if ! echo "$PATH" | grep -q "${GOPATH:-undefined}/bin\|$HOME/go/bin"; then
        echo "Go workspace's \"bin\" directory is not in PATH. Run 'export PATH=\"\$PATH:\${GOPATH:-\$HOME/go}/bin\"'."
    fi
}

install_k8s_binary() {
    local arch="$1"
    local binary="$2"
    local url="https://dl.k8s.io/${K8S_DOWNLOAD_VERSION}/bin/linux/${arch}/${binary}"
    local tmp_dir
    tmp_dir="$(mktemp -d)"
    (
        trap 'rm -rf "${tmp_dir}"' EXIT

        curl --proto '=https' --tlsv1.2 -fsSLo "${tmp_dir}/${binary}" "${url}"
        curl --proto '=https' --tlsv1.2 -fsSLo "${tmp_dir}/${binary}.sha256" "${url}.sha256"

        expected_sha="$(tr -d '[:space:]' < "${tmp_dir}/${binary}.sha256")"
        actual_sha="$(sha256sum "${tmp_dir}/${binary}" | awk '{print $1}')"
        if [[ "${actual_sha}" != "${expected_sha}" ]]; then
            echo "SHA256 mismatch for ${binary}: expected ${expected_sha}, got ${actual_sha}" >&2
            exit 1
        fi

        install -m 0755 "${tmp_dir}/${binary}" "${KUBEBUILDER_ASSETS}/${binary}"
    )
}

kubebuilder() {
    sudo mkdir -p "${KUBEBUILDER_ASSETS}"
    sudo chown "${USER}" "${KUBEBUILDER_ASSETS}"
    arch=$(go env GOARCH)
    ln -sf "$(setup-envtest use -p path "${K8S_VERSION}" --arch="${arch}" --bin-dir="${KUBEBUILDER_ASSETS}")"/* "${KUBEBUILDER_ASSETS}"
    find "$KUBEBUILDER_ASSETS"

    # Install latest binaries for 1.25.x (contains CEL fix)
    if [[ "${K8S_VERSION}" = "1.25.x" ]] && [[ "$OSTYPE" == "linux"* ]]; then
        for binary in 'kube-apiserver' 'kubectl'; do
            install_k8s_binary "${arch}" "${binary}"
        done
    fi
}

latest_go_version() {
    local module="$1"
    local version

    version="$(GOFLAGS=-mod=mod go list -m -versions "${module}" |
        awk '{ for (i = 2; i <= NF; i++) if ($i ~ /^v[0-9]+([.][0-9]+){2}$/) latest = $i } END { if (latest) print latest; else exit 1 }')" || {
        echo "Error: no stable semver version found for ${module}" >&2
        return 1
    }
    printf '%s\n' "${version}"
}

latest_go_pseudo_version() {
    local module="$1"
    local version

    version="$(GOFLAGS=-mod=mod go list -m -json "${module}@latest" |
        awk -F'"' '/"Version":/ { print $4; found = 1; exit } END { exit found ? 0 : 1 }')" || {
        echo "Error: failed to resolve latest pseudo-version for ${module}" >&2
        return 1
    }
    printf '%s\n' "${version}"
}

latest_k8s_patch_version() {
    local current="$1"
    local minor="${current%.*}"
    local version

    version="$(curl --proto '=https' --tlsv1.2 -fsSL "https://dl.k8s.io/release/stable-${minor#v}.txt")" || {
        echo "Error: failed to resolve latest Kubernetes patch for ${minor}" >&2
        return 1
    }
    printf '%s\n' "${version}"
}

current_shell_version() {
    local name="$1"

    awk -F'"' -v name="${name}" '$0 ~ "^" name "=" { print $2; found = 1; exit } END { exit found ? 0 : 1 }' "${TOOLCHAIN_FILE}"
}

set_shell_version() {
    local name="$1"
    local version="$2"
    local current tmp

    current="$(current_shell_version "${name}")"
    [[ "${current}" != "${version}" ]] || return 0

    tmp="$(mktemp)"
    awk -v name="${name}" -v version="${version}" '
        $0 ~ "^" name "=" { print name "=\"" version "\""; next }
        { print }
    ' "${TOOLCHAIN_FILE}" > "${tmp}"
    mv "${tmp}" "${TOOLCHAIN_FILE}"
    chmod +x "${TOOLCHAIN_FILE}"
    echo "Updated ${TOOLCHAIN_FILE}: ${name} ${current} -> ${version}"
}

set_make_version() {
    local name="$1"
    local version="$2"
    local current tmp

    current="$(awk -v name="${name}" '$1 == name && $2 == "?=" { print $3; found = 1; exit } END { exit found ? 0 : 1 }' "${MAKEFILE}")"
    [[ "${current}" != "${version}" ]] || return 0

    tmp="$(mktemp)"
    awk -v name="${name}" -v version="${version}" '
        $1 == name && $2 == "?=" { print name " ?= " version; next }
        { print }
    ' "${MAKEFILE}" > "${tmp}"
    mv "${tmp}" "${MAKEFILE}"
    echo "Updated ${MAKEFILE}: ${name} ${current} -> ${version}"
}

update_versions() {
    local k8s_download addlicense golangci_lint helm_docs setup_envtest controller_tools ginkgo crddoc

    k8s_download="$(latest_k8s_patch_version "$(current_shell_version K8S_DOWNLOAD_VERSION)")"
    addlicense="$(latest_go_version github.com/google/addlicense)"
    golangci_lint="$(latest_go_version github.com/golangci/golangci-lint)"
    helm_docs="$(latest_go_version github.com/norwoodj/helm-docs)"
    setup_envtest="$(latest_go_version sigs.k8s.io/controller-runtime)"
    controller_tools="$(latest_go_version sigs.k8s.io/controller-tools)"
    ginkgo="$(latest_go_version github.com/onsi/ginkgo/v2)"
    crddoc="$(latest_go_pseudo_version github.com/theunrepentantgeek/crddoc)"

    set_shell_version K8S_DOWNLOAD_VERSION "${k8s_download}"
    set_shell_version ADDLICENSE_VERSION "${addlicense}"
    set_shell_version GOLANGCI_LINT_VERSION "${golangci_lint}"
    set_shell_version HELM_DOCS_VERSION "${helm_docs}"
    set_shell_version SETUP_ENVTEST_VERSION "${setup_envtest}"
    set_shell_version CONTROLLER_TOOLS_VERSION "${controller_tools}"
    set_shell_version GINKGO_VERSION "${ginkgo}"
    set_shell_version CRDDOC_VERSION "${crddoc}"

    set_make_version CONTROLLER_TOOLS_VERSION "${controller_tools}"
    set_make_version GOLANGCI_LINT_VERSION "${golangci_lint}"
}

main "$@"
