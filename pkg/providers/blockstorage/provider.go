/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package blockstorage

import (
	"context"
	"errors"

	"github.com/oracle/karpenter-provider-oci/pkg/cache"
	"github.com/oracle/karpenter-provider-oci/pkg/oci"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
)

var errBootVolumeCacheRequired = errors.New("boot volume cache is required")

type Provider interface {
	GetBootVolume(ctx context.Context, bootVolumeOcid string) (*ocicore.BootVolume, error)
	GetBootVolumeCached(ctx context.Context, bootVolumeOcid string) (*ocicore.BootVolume, error)
}

type DefaultProvider struct {
	blockStorageClient oci.BlockStorageClient
	bootVolumeCache    *cache.GetOrLoadCache[*ocicore.BootVolume]
}

func NewProvider(ctx context.Context, blockStorageClient oci.BlockStorageClient,
	bootVolumeCache *cache.GetOrLoadCache[*ocicore.BootVolume]) (*DefaultProvider, error) {
	if bootVolumeCache == nil {
		return nil, errBootVolumeCacheRequired
	}
	p := &DefaultProvider{
		blockStorageClient: blockStorageClient,
		bootVolumeCache:    bootVolumeCache,
	}

	return p, nil
}

func (p *DefaultProvider) GetBootVolume(ctx context.Context,
	bootVolumeOcid string) (*ocicore.BootVolume, error) {
	return p.bootVolumeCache.Refresh(ctx, bootVolumeOcid,
		func(ctx context.Context, _ string) (*ocicore.BootVolume, error) {
			return p.getBootVolumeImpl(ctx, bootVolumeOcid)
		})
}

func (p *DefaultProvider) getBootVolumeImpl(ctx context.Context, bootVolumeOcid string) (*ocicore.BootVolume, error) {
	getResp, err := p.blockStorageClient.GetBootVolume(ctx, ocicore.GetBootVolumeRequest{
		BootVolumeId: &bootVolumeOcid,
	})
	if err != nil {
		return nil, err
	}
	return &getResp.BootVolume, nil
}

func (p *DefaultProvider) GetBootVolumeCached(ctx context.Context,
	bootVolumeOcid string) (*ocicore.BootVolume, error) {
	return p.bootVolumeCache.GetOrLoad(ctx, bootVolumeOcid,
		func(ctx context.Context, _ string) (*ocicore.BootVolume, error) {
			return p.getBootVolumeImpl(ctx, bootVolumeOcid)

		})
}
