/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package capacitydiscovery

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
)

func mib(v string) resource.Quantity { return resource.MustParse(v) }

func TestDiscoveredCapacity_RecordAndGet(t *testing.T) {
	d := newNodeCapacity(DefaultNodeCapacityTTL)

	_, ok := d.Get("absent")
	assert.False(t, ok, "an unrecorded key must not report a value")

	d.Record(context.Background(), "k", mib("30890Mi"))

	got, ok := d.Get("k")
	require.True(t, ok)
	want := mib("30890Mi")
	assert.Equal(t, want.Value(), got.Value())
}

// Nodes of nominally the same kind report slightly different totals. Keeping the smallest is what
// makes a coarse key safe: over-stating capacity is what drives the launch loop this exists to
// stop, while under-stating it only wastes a little memory.
func TestDiscoveredCapacity_SmallestObservationWins(t *testing.T) {
	tests := []struct {
		name     string
		observed []string
		want     string
	}{
		{"decreasing", []string{"31000Mi", "30890Mi", "30800Mi"}, "30800Mi"},
		{"increasing", []string{"30800Mi", "30890Mi", "31000Mi"}, "30800Mi"},
		{"smallest in the middle", []string{"30890Mi", "30700Mi", "31000Mi"}, "30700Mi"},
		{"single observation", []string{"30890Mi"}, "30890Mi"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newNodeCapacity(DefaultNodeCapacityTTL)
			for _, o := range tt.observed {
				d.Record(context.Background(), "k", mib(o))
			}

			got, ok := d.Get("k")
			require.True(t, ok)
			want := mib(tt.want)
			assert.Equal(t, want.Value(), got.Value())
		})
	}
}

// Smallest-wins is a read-compare-write, so it only holds if the whole sequence is atomic.
func TestDiscoveredCapacity_RecordIsAtomic(t *testing.T) {
	d := newNodeCapacity(DefaultNodeCapacityTTL)
	ctx := context.Background()
	small, large := mib("30800Mi"), mib("31000Mi")

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); d.Record(ctx, "k", small) }()
		go func() { defer wg.Done(); d.Record(ctx, "k", large) }()
	}
	wg.Wait()

	got, ok := d.Get("k")
	require.True(t, ok)
	assert.Equal(t, small.Value(), got.Value(),
		"the smallest observation must survive any interleaving")
}

// Re-observing the same value refreshes the entry, so a combination still in active use does not
// expire and force the estimate to govern launches again.
//
// Record exactly once mid-life and assert after the ORIGINAL deadline but before the refreshed
// one. A loop would prove nothing: if the refresh never happened the entry would expire and the
// next iteration would simply insert it again, and the assertion would pass regardless.
func TestDiscoveredCapacity_EqualObservationRefreshesTTL(t *testing.T) {
	const ttl = 200 * time.Millisecond
	d := newNodeCapacity(ttl)
	ctx := context.Background()
	mem := mib("30890Mi")

	d.Record(ctx, "k", mem) // expires at t=200ms
	time.Sleep(120 * time.Millisecond)
	d.Record(ctx, "k", mem) // if this refreshes, it now expires at t=320ms

	time.Sleep(130 * time.Millisecond) // t=250ms: past the original deadline, before the new one
	_, ok := d.Get("k")
	assert.True(t, ok, "an equal re-observation must refresh the entry, not be discarded")
}

func TestDiscoveredCapacity_Expiry(t *testing.T) {
	d := newNodeCapacity(80 * time.Millisecond)
	d.Record(context.Background(), "k", mib("30890Mi"))

	_, ok := d.Get("k")
	require.True(t, ok)

	time.Sleep(200 * time.Millisecond)
	_, ok = d.Get("k")
	assert.False(t, ok, "entries must not outlive their TTL")
}

// A zero TTL is the documented way to switch the feature off, so it must record nothing, report
// nothing and answer Enabled() honestly - callers use that to skip the work entirely.
func TestDiscoveredCapacity_Disabled(t *testing.T) {
	d := newNodeCapacity(0)

	assert.False(t, d.Enabled())

	d.Record(context.Background(), "k", mib("30890Mi"))
	_, ok := d.Get("k")
	assert.False(t, ok, "a disabled cache must not retain observations")
}

// A negative TTL is nonsense rather than a request to disable; treating it as "off" would silently
// turn the feature off for a typo. It falls back to DefaultNodeCapacityTTL, which this asserts only
// as "still enabled and retaining" - pinning the exact duration would mean either exposing the
// field or waiting out a 60-day TTL.
func TestDiscoveredCapacity_NegativeTTLDoesNotDisable(t *testing.T) {
	d := newNodeCapacity(-1 * time.Hour)

	assert.True(t, d.Enabled(), "a negative TTL must not disable the cache")

	d.Record(context.Background(), "k", mib("30890Mi"))
	_, ok := d.Get("k")
	assert.True(t, ok)
}

func TestDiscoveredCapacity_Enabled(t *testing.T) {
	assert.True(t, newNodeCapacity(DefaultNodeCapacityTTL).Enabled())
	assert.False(t, newNodeCapacity(0).Enabled())

	var nilCache *nodeCapacity
	assert.False(t, nilCache.Enabled(), "a nil cache must be safe to ask")
}

// Keys are opaque to the cache; entries must not bleed between them.
func TestDiscoveredCapacity_KeysAreIndependent(t *testing.T) {
	d := newNodeCapacity(DefaultNodeCapacityTTL)
	ctx := context.Background()

	d.Record(ctx, "a", mib("30800Mi"))
	d.Record(ctx, "b", mib("15400Mi"))

	a, ok := d.Get("a")
	require.True(t, ok)
	b, ok := d.Get("b")
	require.True(t, ok)

	wantA, wantB := mib("30800Mi"), mib("15400Mi")
	assert.Equal(t, wantA.Value(), a.Value())
	assert.Equal(t, wantB.Value(), b.Value())
}
