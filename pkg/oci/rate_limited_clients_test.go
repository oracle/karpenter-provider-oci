/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package oci

import (
	"context"
	"errors"
	"testing"

	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/stretchr/testify/require"
)

type countingRateLimiter struct {
	waitCalls int
	waitErr   error
}

func (c *countingRateLimiter) TryAccept() bool {
	return c.waitErr == nil
}

func (c *countingRateLimiter) Stop() {}

func (c *countingRateLimiter) Accept() {}

func (c *countingRateLimiter) QPS() float32 {
	return 1
}

func (c *countingRateLimiter) Wait(context.Context) error {
	c.waitCalls++
	return c.waitErr
}

func TestRateLimitedComputeClient_UsesReaderLimiterForReadCalls(t *testing.T) {
	reader := &countingRateLimiter{waitErr: errors.New("reader blocked")}
	writer := &countingRateLimiter{}
	client := &rateLimitedComputeClient{
		inner: &ocicore.ComputeClient{},
		limiter: RateLimiter{
			Reader: reader,
			Writer: writer,
		},
	}

	_, err := client.GetInstance(context.Background(), ocicore.GetInstanceRequest{})

	require.EqualError(t, err, "reader blocked")
	require.Equal(t, 1, reader.waitCalls)
	require.Equal(t, 0, writer.waitCalls)
}

func TestRateLimitedComputeClient_UsesWriterLimiterForWriteCalls(t *testing.T) {
	reader := &countingRateLimiter{}
	writer := &countingRateLimiter{waitErr: errors.New("writer blocked")}
	client := &rateLimitedComputeClient{
		inner: &ocicore.ComputeClient{},
		limiter: RateLimiter{
			Reader: reader,
			Writer: writer,
		},
	}

	_, err := client.LaunchInstance(context.Background(), ocicore.LaunchInstanceRequest{})

	require.EqualError(t, err, "writer blocked")
	require.Equal(t, 0, reader.waitCalls)
	require.Equal(t, 1, writer.waitCalls)
}
