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

	// Per-shape suppression alone does not bound a broken image API: a listing walks every shape,
	// so the first pass would still pay one timeout per shape before any of them is suppressed.
	// Consecutive failures across different shapes indicate the API itself is unwell rather than
	// one shape lacking a compatible image, so after a few in a row every shape is suppressed
	// together. A single success clears it, so a persistently unresolvable shape sitting among
	// healthy ones never trips it.
	mu                  sync.Mutex
	consecutiveFailures int
	globalUntil         time.Time
	ttl                 time.Duration
}

// consecutiveFailureLimit is how many failures in a row, across any shapes, are tolerated before
// resolution is suppressed wholesale.
const consecutiveFailureLimit = 3

func NewImageResolutionFailures(ttl time.Duration) *ImageResolutionFailures {
	if ttl <= 0 {
		ttl = ImageResolutionFailureTTL
	}
	return &ImageResolutionFailures{cache: cache.New(ttl, ttl), ttl: ttl}
}

// RecentlyFailed reports whether resolution for this shape failed within the TTL.
func (f *ImageResolutionFailures) RecentlyFailed(shape string) bool {
	if f == nil {
		return false
	}

	f.mu.Lock()
	suppressed := time.Now().Before(f.globalUntil)
	f.mu.Unlock()
	if suppressed {
		return true
	}

	_, found := f.cache.Get(shape)
	return found
}

// RecordSuccess clears the consecutive-failure count, so a healthy API is never suppressed because
// of failures spread across unrelated shapes earlier in a listing.
func (f *ImageResolutionFailures) RecordSuccess() {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consecutiveFailures = 0
}

// RecordFailure suppresses further resolution attempts for this shape until the TTL expires.
// Recording again restarts that window, so a persistently failing API is asked at a steady low
// rate rather than on every listing.
func (f *ImageResolutionFailures) RecordFailure(shape string) {
	if f == nil {
		return
	}
	f.cache.SetDefault(shape, struct{}{})

	f.mu.Lock()
	defer f.mu.Unlock()
	f.consecutiveFailures++
	if f.consecutiveFailures >= consecutiveFailureLimit {
		f.globalUntil = time.Now().Add(f.ttl)
	}
}
