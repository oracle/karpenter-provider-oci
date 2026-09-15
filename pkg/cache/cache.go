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
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	DefaultTTL = time.Hour

	LongTTL = 24 * time.Hour
	// DefaultCleanupInterval triggers cache cleanup (lazy eviction) at this interval.
	DefaultCleanupInterval = 10 * time.Minute

	// UnavailableOfferingsTTL is the default duration an offering observed to be out of host
	// capacity is considered unavailable before Karpenter retries it. It is overridable via the
	// --unavailable-offerings-ttl-seconds option.
	UnavailableOfferingsTTL = 3 * time.Minute
	// UnavailableOfferingsCleanupInterval triggers cleanup of the unavailable-offerings cache.
	UnavailableOfferingsCleanupInterval = time.Minute

	// DiscoveredCapacityTTL is how long a memory capacity measured on a real node is reused for
	// later launches of the same instance type and image.
	//
	// It is long because the value is close to a property of the shape and image rather than of
	// the moment. It expires at all for three reasons:
	//
	//   - Recording keeps the smallest value seen, so an entry can never recover upward. One
	//     unusually small node would otherwise suppress that combination's capacity permanently,
	//     with no path back. Expiry is what allows it to be re-learned.
	//   - The key includes the image, so entries for an image that is no longer selected are never
	//     read again rather than being overwritten. Without expiry those orphans accumulate for
	//     the lifetime of the process.
	//   - A host firmware or hypervisor change can alter what a shape presents to the guest
	//     without changing anything in the key, so nothing else would invalidate the entry.
	//
	// Set the TTL to zero to disable capacity discovery entirely; see
	// --discovered-node-capacity-ttl-hours.
	DiscoveredCapacityTTL = 60 * 24 * time.Hour
	// DiscoveredCapacityCleanupInterval triggers cleanup of the discovered-capacity cache.
	DiscoveredCapacityCleanupInterval = time.Hour

	// ImageResolutionTimeout bounds a single image resolution attempted while listing instance
	// types. Discovery is an optimisation over the modelled estimate, so a slow image API must
	// degrade the model rather than hold up scheduling.
	ImageResolutionTimeout = 5 * time.Second
	// ImageResolutionFailureTTL is how long a failed resolution suppresses further attempts for
	// the same shape. Failed loads are not cached by the image provider, so without this a broken
	// or expired lookup is retried for every shape on every listing. It is deliberately short:
	// the cost of waiting is only that discovery stays off a little longer once the API recovers.
	ImageResolutionFailureTTL = 30 * time.Second
)

type LoaderFunc[T any] func(ctx context.Context, key string) (T, error)

type GetOrLoadCache[T any] struct {
	cache *cache.Cache
	locks sync.Map // map[string]*sync.Mutex
}

func NewGetOrLoadCache[T any](defaultExpiration, cleanupInterval time.Duration) *GetOrLoadCache[T] {
	return &GetOrLoadCache[T]{
		cache: cache.New(defaultExpiration, cleanupInterval),
	}
}

func NewDefaultGetOrLoadCache[T any]() *GetOrLoadCache[T] {
	return &GetOrLoadCache[T]{
		cache: cache.New(DefaultTTL, DefaultCleanupInterval),
	}
}

// NewNonExpiringGetOrLoadCache creates a cache whose entries are retained until
// they are explicitly replaced or evicted.
func NewNonExpiringGetOrLoadCache[T any]() *GetOrLoadCache[T] {
	return &GetOrLoadCache[T]{
		cache: cache.New(cache.NoExpiration, cache.NoExpiration),
	}
}

func (c *GetOrLoadCache[T]) GetOrLoad(ctx context.Context, key string,
	loader LoaderFunc[T]) (T, error) {
	t, found := c.getFromCache(ctx, key)
	if found {
		return t, nil
	}

	// Lock for this key to avoid duplicate loads.
	muIface, _ := c.locks.LoadOrStore(key, &sync.Mutex{})
	mu := muIface.(*sync.Mutex)
	mu.Lock()
	defer c.locks.Delete(key)
	defer mu.Unlock()

	// Double-check after acquiring the lock.
	t, found = c.getFromCache(ctx, key)
	if found {
		return t, nil
	}

	return c.load(ctx, key, loader)
}

// Evict removes the current entry. An in-flight load may repopulate it after eviction.
func (c *GetOrLoadCache[T]) Evict(key string) {
	c.cache.Delete(key)
}

// Set exposes the underlying typed cache write operation.
func (c *GetOrLoadCache[T]) Set(key string, value T) {
	c.cache.Set(key, value, cache.DefaultExpiration)
}

// Refresh always invokes the loader and replaces the cached value only after
// a successful load.
func (c *GetOrLoadCache[T]) Refresh(ctx context.Context, key string,
	loader LoaderFunc[T]) (T, error) {
	return c.load(ctx, key, loader)
}

// OnEvicted registers a typed callback that runs when an existing entry is
// explicitly evicted or expires. Overwriting an entry does not invoke it.
func (c *GetOrLoadCache[T]) OnEvicted(callback func(key string, value T)) {
	if callback == nil {
		c.cache.OnEvicted(nil)
		return
	}
	c.cache.OnEvicted(func(key string, value interface{}) {
		typed, ok := value.(T)
		if ok {
			callback(key, typed)
		}
	})
}

// EvictMatching removes all current entries whose keys match the predicate. An in-flight load may
// repopulate an entry after eviction.
func (c *GetOrLoadCache[T]) EvictMatching(match func(key string) bool) {
	for key := range c.cache.Items() {
		if match(key) {
			c.cache.Delete(key)
		}
	}
}

func (c *GetOrLoadCache[T]) getFromCache(ctx context.Context, key string) (T, bool) {
	// get from cache with safe type assertion
	if v, found := c.cache.Get(key); found {
		if typed, ok := v.(T); ok {
			log.FromContext(ctx).V(1).Info("serving from cache", "key", key)
			return typed, true
		}

		c.cache.Delete(key)
	}

	var zero T
	return zero, false
}

func (c *GetOrLoadCache[T]) load(ctx context.Context, key string, loader LoaderFunc[T]) (T, error) {
	// Load, store, return
	log.FromContext(ctx).V(1).Info("cache loading", "key", key)
	v, err := loader(ctx, key)
	if err == nil {
		c.cache.Set(key, v, cache.DefaultExpiration)
		return v, nil
	}

	var zero T
	return zero, err
}
