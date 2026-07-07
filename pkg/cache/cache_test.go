/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package cache

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/assert"
)

func TestNewNonExpiringGetOrLoadCache(t *testing.T) {
	c := NewNonExpiringGetOrLoadCache[string]()
	c.Set("key", "first")
	c.Set("key", "second")

	item, found := c.cache.Items()["key"]
	assert.True(t, found)
	assert.Equal(t, "second", item.Object)
	assert.Equal(t, int64(0), item.Expiration)
}

func TestEvictMatching(t *testing.T) {
	c := NewNonExpiringGetOrLoadCache[string]()
	c.Set("instance-1", "one")
	c.Set("instance-2", "two")

	c.EvictMatching(func(key string) bool {
		return key == "instance-2"
	})

	_, found := c.cache.Get("instance-1")
	assert.True(t, found)
	_, found = c.cache.Get("instance-2")
	assert.False(t, found)
}

func TestGetOrLoad_HappyPath(t *testing.T) {
	c := NewDefaultGetOrLoadCache[string]()
	ctx := context.Background()
	key := "test-key"
	expected := "loaded-value"

	loader := func(ctx context.Context, k string) (string, error) {
		assert.Equal(t, key, k)
		return expected, nil
	}

	val, err := c.GetOrLoad(ctx, key, loader)
	assert.NoError(t, err)
	assert.Equal(t, expected, val)

	// Second call should hit cache
	val2, err := c.GetOrLoad(ctx, key, loader)
	assert.NoError(t, err)
	assert.Equal(t, expected, val2)
}

func TestGetOrLoad_LoaderError(t *testing.T) {
	c := NewDefaultGetOrLoadCache[string]()
	ctx := context.Background()
	key := "error-key"
	expectedErr := errors.New("load failed")

	loader := func(ctx context.Context, k string) (string, error) {
		return "", expectedErr
	}

	val, err := c.GetOrLoad(ctx, key, loader)
	assert.Error(t, err)
	assert.Equal(t, expectedErr, err)
	assert.Equal(t, "", val)

	// Second call should try to load again (since error not cached)
	val2, err := c.GetOrLoad(ctx, key, loader)
	assert.Error(t, err)
	assert.Equal(t, expectedErr, err)
	assert.Equal(t, "", val2)
}

func TestGetOrLoad_CacheHit(t *testing.T) {
	c := NewDefaultGetOrLoadCache[string]()
	ctx := context.Background()
	key := "hit-key"
	expected := "cached-value"

	// Manually set cache (for test purposes)
	c.cache.Set(key, expected, cache.DefaultExpiration)

	loader := func(ctx context.Context, k string) (string, error) {
		t.Fatal("loader should not be called")
		return "", nil
	}

	val, err := c.GetOrLoad(ctx, key, loader)
	assert.NoError(t, err)
	assert.Equal(t, expected, val)
}

func TestGetOrLoad_Concurrent(t *testing.T) {
	c := NewDefaultGetOrLoadCache[string]()
	ctx := context.Background()
	key := "concurrent-key"
	expected := "concurrent-value"

	callCount := 0
	var mu sync.Mutex

	loader := func(_ context.Context, _ string) (string, error) { //nolint:unparam
		mu.Lock()
		callCount++
		mu.Unlock()
		time.Sleep(10 * time.Millisecond) // simulate work
		return expected, nil
	}

	const numGoroutines = 10
	var wg sync.WaitGroup
	results := make([]string, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			val, err := c.GetOrLoad(ctx, key, loader)
			assert.NoError(t, err)
			results[idx] = val
		}(i)
	}

	wg.Wait()

	assert.Equal(t, 1, callCount, "loader should be called only once")
	for _, res := range results {
		assert.Equal(t, expected, res)
	}
}

func TestRefresh(t *testing.T) {
	c := NewNonExpiringGetOrLoadCache[string]()
	ctx := context.Background()
	c.Set("key", "old")

	value, err := c.Refresh(ctx, "key", func(context.Context, string) (string, error) {
		return "new", nil
	})
	assert.NoError(t, err)
	assert.Equal(t, "new", value)

	expectedErr := errors.New("refresh failed")
	_, err = c.Refresh(ctx, "key", func(context.Context, string) (string, error) {
		return "", expectedErr
	})
	assert.ErrorIs(t, err, expectedErr)

	value, err = c.GetOrLoad(ctx, "key", func(context.Context, string) (string, error) {
		t.Fatal("preserved value should be served from cache")
		return "", nil
	})
	assert.NoError(t, err)
	assert.Equal(t, "new", value)
}

func TestEvict(t *testing.T) {
	c := NewDefaultGetOrLoadCache[string]()
	ctx := context.Background()
	key := "evict-key"
	expected := "to-evict"

	// Set in cache
	c.cache.Set(key, expected, cache.DefaultExpiration)

	// Verify hit
	val, err := c.GetOrLoad(ctx, key, func(ctx context.Context, k string) (string, error) {
		t.Fatal("should hit cache")
		return "", nil
	})
	assert.NoError(t, err)
	assert.Equal(t, expected, val)

	// Evict
	c.Evict(key)

	// Next call should miss cache
	loaderCalled := false
	val2, err := c.GetOrLoad(ctx, key, func(ctx context.Context, k string) (string, error) {
		loaderCalled = true
		return "new-value", nil
	})
	assert.NoError(t, err)
	assert.True(t, loaderCalled)
	assert.Equal(t, "new-value", val2)
}

func TestOnEvicted(t *testing.T) {
	c := NewNonExpiringGetOrLoadCache[string]()
	var evictedKeys []string
	var evictedValues []string
	c.OnEvicted(func(key, value string) {
		evictedKeys = append(evictedKeys, key)
		evictedValues = append(evictedValues, value)
	})

	c.Set("key", "first")
	c.Set("key", "second")
	assert.Empty(t, evictedKeys, "overwriting an entry must not invoke OnEvicted")

	c.Evict("missing")
	assert.Empty(t, evictedKeys, "evicting a missing entry must not invoke OnEvicted")

	c.Evict("key")
	assert.Equal(t, []string{"key"}, evictedKeys)
	assert.Equal(t, []string{"second"}, evictedValues)
}
