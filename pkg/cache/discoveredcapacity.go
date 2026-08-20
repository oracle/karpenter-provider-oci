/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package cache

import (
	"context"
	"sync"
	"time"

	"github.com/patrickmn/go-cache"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// DiscoveredCapacity stores memory capacity actually observed on registered nodes, so that later
// launches of the same instance type and image are modelled from measurement rather than estimate.
//
// Karpenter must predict a node's capacity before the node exists. When that prediction is too
// high the pod that triggered the launch does not fit, stays pending, and — because nothing
// compares the prediction against the node it produced — the identical launch repeats. Recording
// what a node actually reported closes that loop: the estimate then only governs the first launch
// of a given combination.
//
// Entries are keyed by caller-supplied strings; see instancetype for the key scheme.
type DiscoveredCapacity struct {
	cache *cache.Cache
	// mu makes the read-compare-write in Record atomic. The underlying cache is already safe for
	// concurrent access, but smallest-wins is an invariant across two operations: without this,
	// two racing observations could both read the old value and the larger one could land last.
	mu sync.Mutex
	// disabled reports whether the feature is turned off (ttl of 0), in which case Record is a
	// no-op and Get never returns a value, so modelled capacity is used unchanged.
	disabled bool
}

// NewDiscoveredCapacity creates the cache with the given entry TTL. A ttl of 0 disables discovery
// entirely; a negative ttl is treated as invalid and falls back to DiscoveredCapacityTTL.
func NewDiscoveredCapacity(ttl time.Duration) *DiscoveredCapacity {
	if ttl == 0 {
		return &DiscoveredCapacity{disabled: true}
	}
	if ttl < 0 {
		ttl = DiscoveredCapacityTTL
	}
	return &DiscoveredCapacity{
		cache: cache.New(ttl, DiscoveredCapacityCleanupInterval),
	}
}

// Get returns the memory capacity observed for key, if one has been recorded.
func (d *DiscoveredCapacity) Get(key string) (resource.Quantity, bool) {
	if d.disabled {
		return resource.Quantity{}, false
	}
	if v, ok := d.cache.Get(key); ok {
		return v.(resource.Quantity), true
	}
	return resource.Quantity{}, false
}

// Record stores observed against key, keeping the smallest value seen.
//
// Smallest-wins matters because the two errors are not symmetric. Modelling more memory than a
// node really has produces a node the pod cannot fit on, and Karpenter retries that launch
// indefinitely; modelling less wastes a little memory. Nodes of nominally the same kind can report
// slightly different totals, so taking the minimum keeps the model on the safe side of the spread
// rather than letting one roomy node raise the estimate for every subsequent launch.
//
// Re-recording an equal value refreshes the TTL, so a combination still in active use does not
// expire and force a re-measurement.
func (d *DiscoveredCapacity) Record(ctx context.Context, key string, observed resource.Quantity) {
	if d.disabled {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	cached, ok := d.Get(key)
	if ok && observed.Cmp(cached) > 0 {
		return
	}

	d.cache.SetDefault(key, observed)

	// Log only genuinely new information, not the TTL refreshes, which happen on every node.
	if !ok || observed.Cmp(cached) < 0 {
		log.FromContext(ctx).V(1).Info("recording discovered memory capacity",
			"key", key, "memory", observed.String())
	}
}
