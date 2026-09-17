# syntax=docker/dockerfile:1.7
#
# Karpenter Provider OCI
#
# Copyright (c) 2026 Oracle and/or its affiliates.
# Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/

# --- Builder Stage ---
ARG BUILDER_IMAGE=golang:1.26.6-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83
ARG BASE_IMAGE=oraclelinux:8-slim@sha256:cd3890d5f46aea09e0a9dfce7f7e1ba3308f77ccd8f7179a7393c67b5876cc6b
FROM --platform=$BUILDPLATFORM $BUILDER_IMAGE AS builder

ARG TARGETOS=linux
ARG TARGETARCH
ARG GOPROXY

WORKDIR /workspace
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    if [ -n "$GOPROXY" ]; then \
      GOPROXY="$GOPROXY" go mod download; \
    else \
      go mod download; \
    fi

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
