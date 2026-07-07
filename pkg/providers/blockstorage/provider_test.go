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
	"testing"

	"github.com/oracle/karpenter-provider-oci/pkg/cache"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBootVolumeFreshReadWritesThroughAndFailurePreservesCache(t *testing.T) {
	ctx := context.Background()
	current := ocicore.BootVolume{Id: lo.ToPtr("boot-volume-1"), DisplayName: lo.ToPtr("old")}
	freshErr := errors.New("temporary cached boot volume read failure")
	fake := &fakes.FakeBlockstorage{
		OnGetBootVolume: func(context.Context, ocicore.GetBootVolumeRequest) (ocicore.GetBootVolumeResponse, error) {
			if freshErr != nil {
				return ocicore.GetBootVolumeResponse{}, freshErr
			}
			return ocicore.GetBootVolumeResponse{BootVolume: current}, nil
		},
	}
	p, err := NewProvider(ctx, fake, cache.NewNonExpiringGetOrLoadCache[*ocicore.BootVolume]())
	require.NoError(t, err)

	_, err = p.GetBootVolumeCached(ctx, "boot-volume-1")
	require.ErrorIs(t, err, freshErr)
	assert.Equal(t, 1, fake.GetCount.Get())

	freshErr = nil
	cached, err := p.GetBootVolumeCached(ctx, "boot-volume-1")
	require.NoError(t, err)
	assert.Equal(t, "old", *cached.DisplayName)
	assert.Equal(t, 2, fake.GetCount.Get())

	current.DisplayName = lo.ToPtr("new")
	cachedAgain, err := p.GetBootVolumeCached(ctx, "boot-volume-1")
	require.NoError(t, err)
	assert.Same(t, cached, cachedAgain)
	assert.Equal(t, "old", *cachedAgain.DisplayName)
	assert.Equal(t, 2, fake.GetCount.Get())

	refreshed, err := p.GetBootVolume(ctx, "boot-volume-1")
	require.NoError(t, err)
	assert.Equal(t, "new", *refreshed.DisplayName)
	assert.Equal(t, 3, fake.GetCount.Get())
	cached, err = p.GetBootVolumeCached(ctx, "boot-volume-1")
	require.NoError(t, err)
	assert.Same(t, refreshed, cached)
	assert.Equal(t, 3, fake.GetCount.Get())

	freshErr = errors.New("temporary boot volume read failure")
	_, err = p.GetBootVolume(ctx, "boot-volume-1")
	require.Error(t, err)
	assert.Equal(t, 4, fake.GetCount.Get())
	cached, err = p.GetBootVolumeCached(ctx, "boot-volume-1")
	require.NoError(t, err)
	assert.Same(t, refreshed, cached)
	assert.Equal(t, 4, fake.GetCount.Get())
}

func TestNewProviderRequiresBootVolumeCache(t *testing.T) {
	_, err := NewProvider(context.Background(), &fakes.FakeBlockstorage{}, nil)
	require.ErrorIs(t, err, errBootVolumeCacheRequired)
}
