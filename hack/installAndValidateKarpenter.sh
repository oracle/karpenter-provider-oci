#!/bin/bash
# Karpenter Provider OCI
#
# Copyright (c) 2026 Oracle and/or its affiliates.
# Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/

set -euo pipefail

# Args / usage
if [ $# -lt 3 ]; then
  echo "Usage: $0 <kubeconfig> <chart.tgz> <values.yaml> [namespace]" >&2
  exit 1
fi

export KUBECONFIG="$1"
KARPENTER_CHART_TGZ="$2"
VALUES_YAML_FILE="$3"
NAMESPACE="${4:-}"

# Validate inputs
if [ ! -f "$KUBECONFIG" ]; then
  echo "KUBECONFIG not found: $KUBECONFIG" >&2
  exit 1
fi
if [ ! -f "$KARPENTER_CHART_TGZ" ]; then
  echo "Chart archive not found: $KARPENTER_CHART_TGZ" >&2
  exit 1
fi
if [ ! -f "$VALUES_YAML_FILE" ]; then
  echo "Values file not found: $VALUES_YAML_FILE" >&2
  exit 1
fi

# Build namespace flags conditionally
NS_FLAG=()
CREATE_NS_FLAG=()
if [[ -n "${NAMESPACE:-}" ]]; then
  NS_FLAG=(--namespace "$NAMESPACE")
  CREATE_NS_FLAG=(--create-namespace)
fi

helm uninstall karpenter "${NS_FLAG[@]}" --timeout 2m || echo "Release 'karpenter' not found, continuing"
kubectl patch ocinodeclasses.oci.oraclecloud.com --all --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true
kubectl patch nodeclaims.karpenter.sh --all --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true
kubectl patch nodepools.karpenter.sh --all --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true
kubectl patch nodeoverlays.karpenter.sh --all --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true
kubectl patch crd ocinodeclasses.oci.oraclecloud.com nodeclaims.karpenter.sh nodepools.karpenter.sh nodeoverlays.karpenter.sh --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true
kubectl delete crd ocinodeclasses.oci.oraclecloud.com nodeclaims.karpenter.sh nodepools.karpenter.sh nodeoverlays.karpenter.sh --ignore-not-found=true
helm install karpenter "./$KARPENTER_CHART_TGZ" --values "$VALUES_YAML_FILE" "${NS_FLAG[@]}" "${CREATE_NS_FLAG[@]}" --wait --timeout 5m

# Final rollout verification and summary
KNS_FLAG=()
if [[ -n "${NAMESPACE:-}" ]]; then
  KNS_FLAG=(-n "$NAMESPACE")
fi
if kubectl "${KNS_FLAG[@]}" get deploy/karpenter >/dev/null 2>&1; then
  kubectl "${KNS_FLAG[@]}" rollout status deploy/karpenter --timeout=300s
  kubectl "${KNS_FLAG[@]}" get pods
  echo "Karpenter was successfully installed."
else
  echo "Error: deployment/karpenter not found in namespace '${NAMESPACE:-default}' after install" >&2
  # Best-effort diagnostics without failing the script here
  kubectl "${KNS_FLAG[@]}" get deploy,pods || true
  exit 1
fi
