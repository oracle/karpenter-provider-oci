/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package oci

import (
	"context"

	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	defaultRateLimitQPSRead    float32 = 20
	defaultRateLimitBurstRead          = 5
	defaultRateLimitQPSWrite   float32 = 20
	defaultRateLimitBurstWrite         = 5
)

type RateLimiterConfig struct {
	DisableRateLimiter  bool
	RateLimitQPSRead    float32
	RateLimitBurstRead  int
	RateLimitQPSWrite   float32
	RateLimitBurstWrite int
}

type RateLimiter struct {
	Reader flowcontrol.RateLimiter
	Writer flowcontrol.RateLimiter
}

func NewRateLimiter(ctx context.Context, config *RateLimiterConfig) RateLimiter {
	logger := log.FromContext(ctx)
	if config == nil {
		config = &RateLimiterConfig{}
	}

	if config.DisableRateLimiter {
		logger.Info("OCI rate limiter is disabled")
		return alwaysAllowRateLimiter()
	}

	if config.RateLimitQPSRead == 0 {
		config.RateLimitQPSRead = defaultRateLimitQPSRead
	}
	if config.RateLimitBurstRead == 0 {
		config.RateLimitBurstRead = defaultRateLimitBurstRead
	}
	if config.RateLimitQPSWrite == 0 {
		config.RateLimitQPSWrite = defaultRateLimitQPSWrite
	}
	if config.RateLimitBurstWrite == 0 {
		config.RateLimitBurstWrite = defaultRateLimitBurstWrite
	}

	logger.Info("OCI rate limiter is enabled",
		"readQPS", config.RateLimitQPSRead,
		"readBurst", config.RateLimitBurstRead,
		"writeQPS", config.RateLimitQPSWrite,
		"writeBurst", config.RateLimitBurstWrite)

	return RateLimiter{
		Reader: flowcontrol.NewTokenBucketRateLimiter(config.RateLimitQPSRead, config.RateLimitBurstRead),
		Writer: flowcontrol.NewTokenBucketRateLimiter(config.RateLimitQPSWrite, config.RateLimitBurstWrite),
	}
}

func normalizeRateLimiter(rateLimiter *RateLimiter) RateLimiter {
	if rateLimiter != nil {
		return *rateLimiter
	}
	return alwaysAllowRateLimiter()
}

func alwaysAllowRateLimiter() RateLimiter {
	return RateLimiter{
		Reader: flowcontrol.NewFakeAlwaysRateLimiter(),
		Writer: flowcontrol.NewFakeAlwaysRateLimiter(),
	}
}
