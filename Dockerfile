# syntax=docker/dockerfile:1.7
#
# Karpenter Provider OCI
#
# Copyright (c) 2026 Oracle and/or its affiliates.
# Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/

# --- Builder Stage ---
ARG BUILDER_IMAGE=golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2
ARG BASE_IMAGE=oraclelinux:8-slim@sha256:2379235158e7715ddfe4f4a26eb1638edb12a6b52ad9ed852a84e38beeaa6642
FROM --platform=$BUILDPLATFORM $BUILDER_IMAGE AS builder

ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd/ ./cmd/
COPY pkg/ ./pkg/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GO111MODULE=on GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -mod=mod -o /workspace/dist/operator ./cmd/main.go

FROM $BASE_IMAGE

WORKDIR /usr/local/bin/karpenter-provider-oci

COPY --from=builder /workspace/dist/operator .

USER 65532:65532

# Entrypoint
ENTRYPOINT ["/usr/local/bin/karpenter-provider-oci/operator"]
