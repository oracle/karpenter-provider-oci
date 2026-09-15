/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package capacitydiscovery

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestImageResolutionFailures_RecordAndQuery(t *testing.T) {
	f := newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL)

	assert.False(t, f.RecentlyFailed("VM.Standard.E5.Flex"), "nothing has failed yet")

	f.RecordFailure("VM.Standard.E5.Flex")
	assert.True(t, f.RecentlyFailed("VM.Standard.E5.Flex"))
}

// Suppression is per shape: one shape with no compatible image must not stop discovery for every
// other shape in the listing.
func TestImageResolutionFailures_ScopedToShape(t *testing.T) {
	f := newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL)

	f.RecordFailure("VM.Standard.E5.Flex")

	assert.True(t, f.RecentlyFailed("VM.Standard.E5.Flex"))
	assert.False(t, f.RecentlyFailed("VM.Standard.A1.Flex"), "an unrelated shape must be unaffected")
}

// Suppression must lift on its own, or one outage would disable discovery until a restart.
func TestImageResolutionFailures_Expires(t *testing.T) {
	f := newImageResolutionFailures(80*time.Millisecond, imageIncompatibleTTL)

	f.RecordFailure("VM.Standard.E5.Flex")
	assert.True(t, f.RecentlyFailed("VM.Standard.E5.Flex"))

	time.Sleep(200 * time.Millisecond)
	assert.False(t, f.RecentlyFailed("VM.Standard.E5.Flex"),
		"once the window passes, resolution must be attempted again")
}

// A repeated failure restarts the window, so a persistently broken API is retried at a steady low
// rate rather than on every listing.
func TestImageResolutionFailures_RepeatedFailureExtendsSuppression(t *testing.T) {
	const ttl = 200 * time.Millisecond
	f := newImageResolutionFailures(ttl, imageIncompatibleTTL)

	f.RecordFailure("VM.Standard.E5.Flex") // suppressed until t=200ms
	time.Sleep(120 * time.Millisecond)
	f.RecordFailure("VM.Standard.E5.Flex") // now suppressed until t=320ms

	time.Sleep(130 * time.Millisecond) // t=250ms: past the first window, inside the second
	assert.True(t, f.RecentlyFailed("VM.Standard.E5.Flex"))
}

func TestImageResolutionFailures_NonPositiveTTLFallsBack(t *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Second} {
		f := newImageResolutionFailures(ttl, imageIncompatibleTTL)
		f.RecordFailure("VM.Standard.E5.Flex")

		assert.True(t, f.RecentlyFailed("VM.Standard.E5.Flex"),
			"a nonsensical TTL must fall back to the default, not disable suppression")
	}
}

// The provider holds this by pointer; a nil one must behave as "nothing has failed" rather than
// panicking on the scheduling path.
func TestImageResolutionFailures_NilIsSafe(t *testing.T) {
	var f *imageResolutionFailures

	assert.NotPanics(t, func() { f.RecordFailure("VM.Standard.E5.Flex") })
	assert.False(t, f.RecentlyFailed("VM.Standard.E5.Flex"))
}

// Per-shape suppression alone does not bound a broken image API: a listing walks every shape, so
// the first pass pays one attempt per shape before any is suppressed. Consecutive failures across
// different shapes mean the API is unwell, so everything is suppressed together.
func TestImageResolutionFailures_GlobalAfterConsecutiveFailures(t *testing.T) {
	f := newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL)

	for _, shape := range []string{"shape-a", "shape-b"} {
		f.RecordFailure(shape)
	}
	assert.False(t, f.RecentlyFailed("shape-untouched"),
		"two failures must not yet suppress an unrelated shape")

	f.RecordFailure("shape-c") // third in a row

	assert.True(t, f.RecentlyFailed("shape-untouched"),
		"once the API looks broken, every shape is suppressed, not just the ones already tried")
}

// A shape that genuinely has no compatible image must not, on its own, disable discovery for the
// healthy shapes around it.
func TestImageResolutionFailures_SuccessResetsTheRun(t *testing.T) {
	f := newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL)

	f.RecordFailure("unresolvable")
	f.RecordSuccess()
	f.RecordFailure("unresolvable")
	f.RecordSuccess()
	f.RecordFailure("unresolvable")

	assert.True(t, f.RecentlyFailed("unresolvable"), "the failing shape is still suppressed")
	assert.False(t, f.RecentlyFailed("healthy"),
		"interleaved successes mean the API is fine, so nothing global should trip")
}

func TestImageResolutionFailures_GlobalSuppressionExpires(t *testing.T) {
	f := newImageResolutionFailures(80*time.Millisecond, imageIncompatibleTTL)

	for i := 0; i < consecutiveFailureLimit; i++ {
		f.RecordFailure("shape-a")
	}
	assert.True(t, f.RecentlyFailed("shape-untouched"))

	time.Sleep(200 * time.Millisecond)
	assert.False(t, f.RecentlyFailed("shape-untouched"),
		"global suppression must lift on its own, or one outage disables discovery until restart")
}

func TestImageResolutionFailures_ConcurrentUse(t *testing.T) {
	f := newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); f.RecordFailure("shape-a") }()
		go func() { defer wg.Done(); f.RecordSuccess() }()
		go func() { defer wg.Done(); f.RecentlyFailed("shape-a") }()
	}
	wg.Wait()
}
