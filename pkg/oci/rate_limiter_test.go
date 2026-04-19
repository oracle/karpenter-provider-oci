/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package oci

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewRateLimiter_Defaults(t *testing.T) {
	rl := NewRateLimiter(context.Background(), &RateLimiterConfig{})

	require.Equal(t, defaultRateLimitQPSRead, rl.Reader.QPS())
	require.Equal(t, defaultRateLimitQPSWrite, rl.Writer.QPS())
}

func TestNewRateLimiter_DefaultBursts(t *testing.T) {
	rl := NewRateLimiter(context.Background(), &RateLimiterConfig{
		RateLimitQPSRead:  1,
		RateLimitQPSWrite: 1,
	})

	for i := 0; i < defaultRateLimitBurstRead; i++ {
		require.True(t, rl.Reader.TryAccept())
	}
	require.False(t, rl.Reader.TryAccept())

	for i := 0; i < defaultRateLimitBurstWrite; i++ {
		require.True(t, rl.Writer.TryAccept())
	}
	require.False(t, rl.Writer.TryAccept())
}

func TestNewRateLimiter_Disabled(t *testing.T) {
	rl := NewRateLimiter(context.Background(), &RateLimiterConfig{DisableRateLimiter: true})

	require.NoError(t, rl.Reader.Wait(context.Background()))
	require.NoError(t, rl.Writer.Wait(context.Background()))
}
