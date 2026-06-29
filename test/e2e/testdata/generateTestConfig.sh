#!/bin/bash
# Karpenter Provider OCI
#
# Copyright (c) 2026 Oracle and/or its affiliates.
# Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/

set -euo pipefail

PREBAKED_IMAGE_COMPARTMENT_ID="ocid1.compartment.oc1..aaaaaaaab4u67dhgtj5gpdpp3z42xqqsdnufxkatoild46u3hb67vzojfmzq"
PREBAKED_IMAGE_COMPARTMENT_ID_UBUNTU="ocid1.compartment.oc1..aaaaaaaawapv5zqax243hxuvi5xs6ekpsntos2ylg2xyx6qnncctcab53hya"

TENANCY_ID="$1"
COMPARTMENT_NAME="$2"
IMAGE_TAG="$3"

# define constants
DRIFT_COMPARTMENT_NAME="${DRIFT_COMPARTMENT_NAME:-karpenter-e2e-drift}"
KEYS_COMPARTMENT_NAME="${KEYS_COMPARTMENT_NAME:-karpenter-e2e-keys}"
VAULT_NAME="${VAULT_NAME:-karpenter-e2e-vault}"
KMS_KEY1_NAME="${KMS_KEY1_NAME:-karpenter-key-1}"
KMS_KEY2_NAME="${KMS_KEY2_NAME:-karpenter-key-2}"
NODE_SUBNET1_NAME="${NODE_SUBNET1_NAME:-private-worker-subnet}"
NODE_SUBNET2_NAME="${NODE_SUBNET2_NAME:-public-worker-drift-subnet}"
NSG1_NAME="${NSG1_NAME:-karpenter-network-security-group}"
NSG2_NAME="${NSG2_NAME:-karpenter-network-security-group-2}"
CAPACITY_RESERVATION1_NAME="${CAPACITY_RESERVATION1_NAME:-karpenter-cap-res-1}"
CAPACITY_RESERVATION2_NAME="${CAPACITY_RESERVATION2_NAME:-karpenter-cap-res-2}"
COMPUTE_CLUSTER_NAME="${COMPUTE_CLUSTER_NAME:-karpenter-e2e-compute-cluster}"
NPN_CLUSTER_NAME="${NPN_CLUSTER_NAME:-karpenter-npn-cluster}"
FLANNEL_CLUSTER_NAME="${FLANNEL_CLUSTER_NAME:-karpenter-flannel-cluster}"
UBUNTU_IMAGE_NAME="${UBUNTU_IMAGE_NAME:-ubuntu-amd64-minimal-24.04-noble-v20250604.1-OKE-1.32.1}"
CUSTOM_IMAGE_NAME="${CUSTOM_IMAGE_NAME:-custom-image-karpenter-testing}"
IMAGE_REGISTRY="${IMAGE_REGISTRY:-ghcr.io}"
IMAGE_REPOSITORY_NAME="${IMAGE_REPOSITORY_NAME:-oracle/karpenter-provider-oci}"
TEST_DEPLOYMENT_IMAGE="${TEST_DEPLOYMENT_IMAGE:-docker.io/library/busybox:latest}"
if [[ -z "${SSH_PUB_KEY:-}" ]]; then
  for key_file in "$HOME/.ssh/id_rsa.pub" "$HOME/.ssh/id_ed25519.pub" "$HOME/.ssh/id_ecdsa.pub"; do
    if [[ -f "$key_file" ]]; then
      SSH_PUB_KEY="$(tr -d "\n" < "$key_file")"
      break
    fi
  done
fi
: "${SSH_PUB_KEY:?set SSH_PUB_KEY or ensure a public key exists in ~/.ssh}"

# configure oci cli
export OCI_CLI_DEBUG="${OCI_CLI_DEBUG:-false}"
# Default to instance_principal, but allow override from environment
export OCI_CLI_AUTH="${OCI_CLI_AUTH:-instance_principal}"

# auth method for tests (default to PROFILE_SESSION; can be overridden via env)
OCI_AUTH_METHOD_FOR_TEST="${OCI_AUTH_METHOD_FOR_TEST:-PROFILE_SESSION}"

# start doing lookups
CE_ENDPOINT_ARGS=()
if [[ -n "${ENDPOINT:-}" ]]; then
  CE_ENDPOINT_ARGS=(--endpoint "$ENDPOINT")
fi

export COMPARTMENT_ID=$(oci iam compartment list --all --compartment-id "$TENANCY_ID" --name "$COMPARTMENT_NAME" --query 'data[0].id' --raw-output)
DRIFT_COMPARTMENT_ID=$(oci iam compartment list --all --compartment-id "$COMPARTMENT_ID" --name "$DRIFT_COMPARTMENT_NAME" --query 'data[0].id' --raw-output)
KEYS_COMPARTMENT_ID=$(oci iam compartment list --all --compartment-id "$TENANCY_ID" --name "$KEYS_COMPARTMENT_NAME" --query 'data[0].id' --raw-output)
VCN_ID=$(oci network vcn list --compartment-id "$COMPARTMENT_ID" --display-name karpenter_vcn --query 'data[0].id' --raw-output)
NODE_SUBNET1_ID=$(oci network subnet list --compartment-id "$COMPARTMENT_ID" --vcn-id "$VCN_ID" --display-name "$NODE_SUBNET1_NAME" --query 'data[0].id' --raw-output)
NODE_SUBNET2_ID=$(oci network subnet list --compartment-id "$COMPARTMENT_ID" --vcn-id "$VCN_ID" --display-name "$NODE_SUBNET2_NAME" --query 'data[0].id' --raw-output)
VAULT_ID=$(oci kms management vault list --compartment-id "$KEYS_COMPARTMENT_ID" --query "data[?\"display-name\"=='$VAULT_NAME'].id | [0]" --raw-output)
KMS_ENDPOINT=$(oci kms management vault get --vault-id "$VAULT_ID" --query 'data."management-endpoint"' --raw-output)
KMS_KEY1_ID=$(oci kms management key list --endpoint "$KMS_ENDPOINT" --all --compartment-id "$COMPARTMENT_ID" --query "data[?\"display-name\"=='$KMS_KEY1_NAME' && \"lifecycle-state\"=='ENABLED'].id | [0]" --raw-output)
KMS_KEY2_ID=$(oci kms management key list --endpoint "$KMS_ENDPOINT" --all --compartment-id "$COMPARTMENT_ID" --query "data[?\"display-name\"=='$KMS_KEY2_NAME' && \"lifecycle-state\"=='ENABLED'].id | [0]" --raw-output)
NSG1_ID=$(oci network nsg list --compartment-id "$COMPARTMENT_ID" --vcn-id "$VCN_ID" --display-name "$NSG1_NAME" --query 'data[0].id' --raw-output)
NSG2_ID=$(oci network nsg list --compartment-id "$COMPARTMENT_ID" --vcn-id "$VCN_ID" --display-name "$NSG2_NAME" --query 'data[0].id' --raw-output)
CAPACITY_RESERVATION1_ID=$(oci compute capacity-reservation list --compartment-id "$COMPARTMENT_ID" --query "data[?\"display-name\"=='$CAPACITY_RESERVATION1_NAME'].id | [0]" --raw-output)
CAPACITY_RESERVATION2_ID=$(oci compute capacity-reservation list --compartment-id "$COMPARTMENT_ID" --query "data[?\"display-name\"=='$CAPACITY_RESERVATION2_NAME'].id | [0]" --raw-output)
COMPUTE_CLUSTER_ID=$(oci compute compute-cluster list --compartment-id "$COMPARTMENT_ID" --query "data.items[?\"display-name\"=='$COMPUTE_CLUSTER_NAME'].id | [0]" --raw-output)
export NPN_CLUSTER_ID=$(oci ce cluster list "${CE_ENDPOINT_ARGS[@]}" --compartment-id "$COMPARTMENT_ID" --lifecycle-state ACTIVE --name "$NPN_CLUSTER_NAME" --query 'data[0].id' --raw-output)
NPN_KUBEAPI_ENDPOINT_IP=$(oci ce cluster get "${CE_ENDPOINT_ARGS[@]}" --cluster-id "$NPN_CLUSTER_ID" --query 'data.endpoints' --raw-output | jq -r '.["private-endpoint"] | split(":")[0]' )
export FLANNEL_CLUSTER_ID=$(oci ce cluster list "${CE_ENDPOINT_ARGS[@]}" --compartment-id "$COMPARTMENT_ID" --lifecycle-state ACTIVE --name "$FLANNEL_CLUSTER_NAME" --query 'data[0].id' --raw-output)
FLANNEL_KUBEAPI_ENDPOINT_IP=$(oci ce cluster get "${CE_ENDPOINT_ARGS[@]}" --cluster-id "$FLANNEL_CLUSTER_ID" --query 'data.endpoints' --raw-output | jq -r '.["private-endpoint"] | split(":")[0]')

# print out the variables
echo "TENANCY_ID: $TENANCY_ID"
echo "COMPARTMENT_NAME: $COMPARTMENT_NAME"
echo "IMAGE_TAG: $IMAGE_TAG"
echo "COMPARTMENT_ID: $COMPARTMENT_ID"
echo "DRIFT_COMPARTMENT_ID: $DRIFT_COMPARTMENT_ID"
echo "KEYS_COMPARTMENT_ID: $KEYS_COMPARTMENT_ID"
echo "NPN_CLUSTER_ID: $NPN_CLUSTER_ID"
echo "NPN_KUBEAPI_ENDPOINT_IP: $NPN_KUBEAPI_ENDPOINT_IP"
echo "FLANNEL_CLUSTER_ID: $FLANNEL_CLUSTER_ID"
echo "FLANNEL_KUBEAPI_ENDPOINT_IP: $FLANNEL_KUBEAPI_ENDPOINT_IP"
echo "VCN_ID: $VCN_ID"
echo "NODE_SUBNET1_ID: $NODE_SUBNET1_ID"
echo "NODE_SUBNET2_ID: $NODE_SUBNET2_ID"
echo "VAULT_ID: $VAULT_ID"
echo "KMS_ENDPOINT: $KMS_ENDPOINT"
echo "KMS_KEY1_ID: $KMS_KEY1_ID"
echo "KMS_KEY2_ID: $KMS_KEY2_ID"
echo "NSG1_ID: $NSG1_ID"
echo "NSG2_ID: $NSG2_ID"
echo "CAPACITY_RESERVATION1_ID: $CAPACITY_RESERVATION1_ID"
echo "CAPACITY_RESERVATION2_ID: $CAPACITY_RESERVATION2_ID"
echo "COMPUTE_CLUSTER_ID: $COMPUTE_CLUSTER_ID"

# Derive image IDs per provider filterAndSortImages logic and set display name placeholders
# Template and output file paths
TEMPLATE_FLANNEL_JSON="e2e_test_config_flannel.template"
TEMPLATE_NPN_JSON="e2e_test_config_npn.template"
TEMPLATE_FLANNEL_VALUES="e2e_test_helm_values_flannel.template"
TEMPLATE_NPN_VALUES="e2e_test_helm_values_npn.template"

# Output generated files
OUT_FLANNEL_JSON="e2e_test_config_flannel.json"
OUT_NPN_JSON="e2e_test_config_npn.json"
OUT_FLANNEL_VALUES="e2e_test_helm_values_flannel.yaml"
OUT_NPN_VALUES="e2e_test_helm_values_npn.yaml"

IMAGE_ID=""
IMAGE_DISPLAY_NAME=""
DRIFT_IMAGE_ID=""
DRIFT_IMAGE_DISPLAY_NAME=""

FLANNEL_IMAGE_ID=""
FLANNEL_IMAGE_DISPLAY_NAME=""
FLANNEL_DRIFT_IMAGE_ID=""
FLANNEL_DRIFT_IMAGE_DISPLAY_NAME=""
NPN_IMAGE_ID=""
NPN_IMAGE_DISPLAY_NAME=""
NPN_DRIFT_IMAGE_ID=""
NPN_DRIFT_IMAGE_DISPLAY_NAME=""

# New template variables: Ubuntu/custom image ID lookup by display name
UBUNTU_IMAGE_ID=""
CUSTOM_IMAGE_ID=""

resolve_oke_image_for_shape() {
  local cluster_name="$1"
  local cluster_id="$2"
  local template_json="$3"
  local expected_shape="${4:-${E2E_EXPECTED_SHAPE:-}}"

  local image_os image_os_version target_k8s_version ver_trimmed major rest minor
  if [[ -z "$expected_shape" ]]; then
    expected_shape=$(jq -r '.NodePool.InstanceTypes[-1] // empty' "$template_json")
  fi
  : "${expected_shape:?expected shape is required; set E2E_EXPECTED_SHAPE or define NodePool.InstanceTypes in $template_json}"

  image_os=$(jq -r '.OCINodeClass.ImageOsFilter // empty' "$template_json")
  image_os_version=$(jq -r '.OCINodeClass.ImageOsVersionFilter // empty' "$template_json")
  target_k8s_version=$(oci ce cluster get "${CE_ENDPOINT_ARGS[@]}" --cluster-id "$cluster_id" \
    --query 'data."kubernetes-version"' --raw-output)

  ver_trimmed="${target_k8s_version#v}"
  major="${ver_trimmed%%.*}"
  rest="${ver_trimmed#*.}"
  minor="${rest%%.*}"

  echo "Resolving OKE image for ${cluster_name}: clusterVersion=${target_k8s_version}, expectedShape=${expected_shape}, os=${image_os}, osVersion=${image_os_version}" >&2

  local compute_list_args=(--compartment-id "$PREBAKED_IMAGE_COMPARTMENT_ID" --all)
  if [[ -n "$image_os" ]]; then compute_list_args+=(--operating-system "$image_os"); fi
  if [[ -n "$image_os_version" ]]; then compute_list_args+=(--operating-system-version "$image_os_version"); fi

  local candidates_json compatible_json row image_id image_name image_score image_shapes
  candidates_json=$(
    oci compute image list "${compute_list_args[@]}" --query 'data' |
    jq -r --arg maj "$major" --arg min "$minor" '
      # Exclude ARM/aarch64 images; e2e currently targets amd64/x86_64 only.
      map(select(."display-name" | test("aarch64"; "i") | not)) |
      map(select(."freeform-tags".k8s_version != null)) |
      map(
        . as $img |
        (."freeform-tags".k8s_version | ascii_downcase | ltrimstr("v") | split(".") | {maj: (.[0]|tonumber), min: (.[1]|tonumber)}) as $iv |
        (($iv.maj != ($maj|tonumber)) or ($iv.min > ($min|tonumber))) as $incompatibleHigher |
        ((($min|tonumber) - $iv.min)) as $diff |
        ((($iv.maj==1) and ($iv.min < 25) and ($diff > 2)) or ($diff > 3)) as $exceedsSkew |
        ($img."time-created"
          | try (
              capture("^(?<d>\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2})(?:\\.\\d+)?(?:(?<z>Z)|(?<sign>[+-])(?<hh>\\d{2}):(?<mm>\\d{2}))$")
              | "\(.d)\(if .z then "+0000" else "\(.sign)\(.hh)\(.mm)" end)"
              | strptime("%Y-%m-%dT%H:%M:%S%z")
              | mktime
            ) catch 0
          ) as $tc |
        . + {
          score: (if $incompatibleHigher or $exceedsSkew then -1 else $diff end),
          tc: $tc
        }
      ) |
      map(select(.score >= 0)) |
      sort_by(.score, - .tc)
    '
  )

  compatible_json="[]"
  while IFS= read -r row; do
    image_id=$(jq -r '.id' <<<"$row")
    image_name=$(jq -r '."display-name"' <<<"$row")
    image_score=$(jq -r '.score' <<<"$row")
    image_shapes=$(oci compute image-shape-compatibility-entry list --image-id "$image_id" --all \
      --query 'data[].shape')

    if jq -e --arg shape "$expected_shape" 'index($shape) != null' >/dev/null <<<"$image_shapes"; then
      echo "Selected-compatible-candidate ${cluster_name}: image=${image_id}, displayName=${image_name}, score=${image_score}" >&2
      compatible_json=$(jq --argjson img "$row" '. + [$img]' <<<"$compatible_json")
    else
      echo "Skipped-incompatible-candidate ${cluster_name}: image=${image_id}, displayName=${image_name}, missingShape=${expected_shape}, score=${image_score}" >&2
    fi
  done < <(jq -c '.[]' <<<"$candidates_json")

  if [[ "$(jq -r 'length' <<<"$compatible_json")" == "0" ]]; then
    echo "No OKE image candidates for ${cluster_name} support ${expected_shape}" >&2
    return 1
  fi

  jq -r '
    .[0] as $primary |
    ([ .[] | select(.score > $primary.score and .id != $primary.id) ] | first) as $drift |
    [
      $primary.id,
      $primary."display-name",
      ($drift.id // ""),
      ($drift."display-name" // "")
    ] | @tsv
  ' <<<"$compatible_json"
}

IFS=$'\t' read -r FLANNEL_IMAGE_ID FLANNEL_IMAGE_DISPLAY_NAME FLANNEL_DRIFT_IMAGE_ID FLANNEL_DRIFT_IMAGE_DISPLAY_NAME < <(
  resolve_oke_image_for_shape "$FLANNEL_CLUSTER_NAME" "$FLANNEL_CLUSTER_ID" "$TEMPLATE_FLANNEL_JSON"
)
IFS=$'\t' read -r NPN_IMAGE_ID NPN_IMAGE_DISPLAY_NAME NPN_DRIFT_IMAGE_ID NPN_DRIFT_IMAGE_DISPLAY_NAME < <(
  resolve_oke_image_for_shape "$NPN_CLUSTER_NAME" "$NPN_CLUSTER_ID" "$TEMPLATE_NPN_JSON"
)

# Keep legacy variable names for shared logging and any non-cluster-specific placeholders.
IMAGE_ID="$FLANNEL_IMAGE_ID"
IMAGE_DISPLAY_NAME="$FLANNEL_IMAGE_DISPLAY_NAME"
DRIFT_IMAGE_ID="$FLANNEL_DRIFT_IMAGE_ID"
DRIFT_IMAGE_DISPLAY_NAME="$FLANNEL_DRIFT_IMAGE_DISPLAY_NAME"

# Resolve Ubuntu/custom image IDs by image display-name
# - Ubuntu images live in PREBAKED_IMAGE_COMPARTMENT_ID_UBUNTU
# - Custom image lives in COMPARTMENT_ID (test compartment)
if [[ -n "${UBUNTU_IMAGE_NAME:-}" ]]; then
  UBUNTU_IMAGE_ID=$(oci compute image list --all --compartment-id "$PREBAKED_IMAGE_COMPARTMENT_ID_UBUNTU" \
    --query "data[?\"display-name\"=='${UBUNTU_IMAGE_NAME}'].id | [0]" --raw-output)
fi

if [[ -n "${CUSTOM_IMAGE_NAME:-}" ]]; then
  CUSTOM_IMAGE_ID=$(oci compute image list --all --compartment-id "$COMPARTMENT_ID" \
    --query "data[?\"display-name\"=='${CUSTOM_IMAGE_NAME}'].id | [0]" --raw-output)
fi

# Print resolved values
echo "IMAGE_ID: ${IMAGE_ID:-}"
echo "IMAGE_DISPLAY_NAME: ${IMAGE_DISPLAY_NAME:-}"
echo "DRIFT_IMAGE_ID: ${DRIFT_IMAGE_ID:-}"
echo "DRIFT_IMAGE_DISPLAY_NAME: ${DRIFT_IMAGE_DISPLAY_NAME:-}"
echo "FLANNEL_IMAGE_ID: ${FLANNEL_IMAGE_ID:-}"
echo "FLANNEL_IMAGE_DISPLAY_NAME: ${FLANNEL_IMAGE_DISPLAY_NAME:-}"
echo "FLANNEL_DRIFT_IMAGE_ID: ${FLANNEL_DRIFT_IMAGE_ID:-}"
echo "FLANNEL_DRIFT_IMAGE_DISPLAY_NAME: ${FLANNEL_DRIFT_IMAGE_DISPLAY_NAME:-}"
echo "NPN_IMAGE_ID: ${NPN_IMAGE_ID:-}"
echo "NPN_IMAGE_DISPLAY_NAME: ${NPN_IMAGE_DISPLAY_NAME:-}"
echo "NPN_DRIFT_IMAGE_ID: ${NPN_DRIFT_IMAGE_ID:-}"
echo "NPN_DRIFT_IMAGE_DISPLAY_NAME: ${NPN_DRIFT_IMAGE_DISPLAY_NAME:-}"
echo "UBUNTU_IMAGE_ID: ${UBUNTU_IMAGE_ID:-}"
echo "CUSTOM_IMAGE_ID: ${CUSTOM_IMAGE_ID:-}"

# Prepare generated files from templates
cp "$TEMPLATE_FLANNEL_JSON" "$OUT_FLANNEL_JSON"
cp "$TEMPLATE_NPN_JSON" "$OUT_NPN_JSON"
cp "$TEMPLATE_FLANNEL_VALUES" "$OUT_FLANNEL_VALUES"
cp "$TEMPLATE_NPN_VALUES" "$OUT_NPN_VALUES"

# Replace place-holders with the lookup values in generated files
FILES=("$OUT_FLANNEL_JSON" "$OUT_NPN_JSON" "$OUT_FLANNEL_VALUES" "$OUT_NPN_VALUES")

VARIABLES=(
  "COMPARTMENT_NAME" "DRIFT_COMPARTMENT_NAME" "COMPARTMENT_ID" "DRIFT_COMPARTMENT_ID"
  "KMS_KEY1_NAME" "KMS_KEY2_NAME" "KMS_KEY1_ID" "KMS_KEY2_ID"
  "NODE_SUBNET1_NAME" "NODE_SUBNET2_NAME" "NODE_SUBNET1_ID" "NODE_SUBNET2_ID"
  "NSG1_NAME" "NSG2_NAME" "NSG1_ID" "NSG2_ID"
  "CAPACITY_RESERVATION1_NAME" "CAPACITY_RESERVATION2_NAME" "CAPACITY_RESERVATION1_ID" "CAPACITY_RESERVATION2_ID"
  "COMPUTE_CLUSTER_NAME" "COMPUTE_CLUSTER_ID"
  "NPN_KUBEAPI_ENDPOINT_IP" "FLANNEL_KUBEAPI_ENDPOINT_IP"
  "OCI_AUTH_METHOD_FOR_TEST" "IMAGE_TAG"
  "IMAGE_REGISTRY" "IMAGE_REPOSITORY_NAME" "TEST_DEPLOYMENT_IMAGE" "SSH_PUB_KEY"
  "UBUNTU_IMAGE_ID" "CUSTOM_IMAGE_ID"
)

sedit() {
  # GNU sed supports '-i' without arg; BSD/macOS sed requires a backup suffix ('' for none)
  if sed --version >/dev/null 2>&1; then
    sed -i "$@"
  else
    sed -i '' "$@"
  fi
}

replace_var() {
  local file="$1"
  local var="$2"
  local val="$3"

  [ -n "$val" ] || return 0
  sedit "s/VAR_${var}/$(printf '%s' "$val" | sed -e 's/[\/&]/\\&/g')/g" "$file"
}

replace_var "$OUT_FLANNEL_JSON" "IMAGE_ID" "$FLANNEL_IMAGE_ID"
replace_var "$OUT_FLANNEL_JSON" "IMAGE_DISPLAY_NAME" "$FLANNEL_IMAGE_DISPLAY_NAME"
replace_var "$OUT_FLANNEL_JSON" "DRIFT_IMAGE_ID" "$FLANNEL_DRIFT_IMAGE_ID"
replace_var "$OUT_FLANNEL_JSON" "DRIFT_IMAGE_DISPLAY_NAME" "$FLANNEL_DRIFT_IMAGE_DISPLAY_NAME"
replace_var "$OUT_NPN_JSON" "IMAGE_ID" "$NPN_IMAGE_ID"
replace_var "$OUT_NPN_JSON" "IMAGE_DISPLAY_NAME" "$NPN_IMAGE_DISPLAY_NAME"
replace_var "$OUT_NPN_JSON" "DRIFT_IMAGE_ID" "$NPN_DRIFT_IMAGE_ID"
replace_var "$OUT_NPN_JSON" "DRIFT_IMAGE_DISPLAY_NAME" "$NPN_DRIFT_IMAGE_DISPLAY_NAME"

for file in "${FILES[@]}"; do
  for VAR in "${VARIABLES[@]}"; do
    val="${!VAR:-}"
    [ -n "$val" ] || continue
    replace_var "$file" "$VAR" "$val"
  done
done
