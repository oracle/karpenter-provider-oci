# syntax=docker/dockerfile:1.7
#
# Karpenter Provider OCI
#
# Copyright (c) 2026 Oracle and/or its affiliates.
# Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/

# --- Builder Stage ---
ARG BUILDER_IMAGE=golang:1.26.10-alpine@sha256:a2012b4a1d1f9f7e9f44e0d39b805a901e856b1dd80b4e6ee4338db547050308
ARG BASE_IMAGE=oraclelinux:8-slim@sha256:3a1318bf9606038d8056f765243a050bdd9473b9bc4f049169ce3b1fb407f6b5
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
