/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package cache

import (
	"sync"
	"time"

	"github.com/patrickmn/go-cache"
)

// ImageResolutionFailures remembers shapes whose image could not be resolved, so a failing lookup
// is attempted once per TTL rather than once per shape on every instance type listing.
//
// The image provider caches successful lookups but not failures, so during an outage every listing
// would otherwise reissue the request - and its retries - for each shape, while the instance type
// provider holds its read lock. Capacity discovery is an optimisation over the modelled estimate,
// so the right response to a failing image API is to stop asking for a while, not to keep paying
// for it on the scheduling path.
//
// The TTL is deliberately short. The only cost of a stale entry is that discovery stays switched
// off slightly longer than necessary after the API recovers, during which launches are modelled
// from the estimate exactly as they were before any of this existed.
type ImageResolutionFailures struct {
	cache *cache.Cache

	// Per-key suppression alone does not bound a broken image API: a listing walks every shape, so
	// the first pass would still pay one timeout per shape before any of them is suppressed. A few
	// service failures in a row therefore suppress everything at once.
	//
	// Only service failures are counted here. A shape with no compatible image is an answer, not an
	// outage: it comes back immediately, costs nothing to have asked, and says nothing about any
	// other key - so counting it would let one NodeClass's narrow image filter switch discovery off
	// for every other NodeClass. A single success clears the count.
	mu                  sync.Mutex
	consecutiveFailures int
	globalUntil         time.Time
	ttl                 time.Duration
}

// consecutiveFailureLimit is how many service failures in a row, across any keys, are tolerated
// before resolution is suppressed wholesale.
const consecutiveFailureLimit = 3

func NewImageResolutionFailures(ttl time.Duration) *ImageResolutionFailures {
	if ttl <= 0 {
		ttl = ImageResolutionFailureTTL
	}
	return &ImageResolutionFailures{cache: cache.New(ttl, ttl), ttl: ttl}
}

// RecentlyFailed reports whether resolution for this key failed within the TTL.
func (f *ImageResolutionFailures) RecentlyFailed(key string) bool {
	if f == nil {
		return false
	}

	f.mu.Lock()
	suppressed := time.Now().Before(f.globalUntil)
	f.mu.Unlock()
	if suppressed {
		return true
	}

	_, found := f.cache.Get(key)
	return found
}

// RecordSuccess clears the consecutive-failure count, so a healthy API is never suppressed because
// of failures spread across unrelated keys earlier in a listing.
func (f *ImageResolutionFailures) RecordSuccess() {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consecutiveFailures = 0
}

// RecordIncompatible suppresses further attempts for this key until the TTL expires, without
// counting towards wholesale suppression. Use it when resolution answered that this configuration
// yields no image for the shape: nothing is wrong with the service, and no other key is implicated.
//
// It deliberately does not clear the consecutive count either. An answer of "incompatible" can be
// served entirely from the shape-compatibility cache, so it is not evidence that the service is
// healthy, and treating it as such would let cached answers interleaved with real failures hold
// the breaker open through an outage. The cost of leaving the count alone is a breaker that can
// trip slightly early, which only means falling back to the modelled estimate for one TTL.
func (f *ImageResolutionFailures) RecordIncompatible(key string) {
	if f == nil {
		return
	}
	f.cache.SetDefault(key, struct{}{})
}

// RecordFailure suppresses further resolution attempts for this key until the TTL expires, and
// counts towards suppressing every key at once. Use it when the image service could not answer.
// Recording again restarts that window, so a persistently failing API is asked at a steady low
// rate rather than on every listing.
func (f *ImageResolutionFailures) RecordFailure(key string) {
	if f == nil {
		return
	}
	f.cache.SetDefault(key, struct{}{})

	f.mu.Lock()
	defer f.mu.Unlock()
	f.consecutiveFailures++
	if f.consecutiveFailures >= consecutiveFailureLimit {
		f.globalUntil = time.Now().Add(f.ttl)
	}
}
