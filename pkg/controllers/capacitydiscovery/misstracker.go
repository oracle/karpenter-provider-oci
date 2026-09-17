/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package capacitydiscovery

import (
	"time"

	"github.com/patrickmn/go-cache"
	"k8s.io/apimachinery/pkg/types"
)

// missTracker remembers when a Node's NodeClaim was first found missing, so that a retry window
// can run from the miss rather than from the Node's creation.
//
// Entries outlive the window they bound. Once we stop retrying a Node the record stays a while
// longer, so a later pass reads "already given up" instead of starting over; only after that does
// it lapse, and a Node arriving again then is treated as a fresh miss and given a fresh window.
//
// They expire on their own rather than being swept. A sweep would have to walk every entry, and
// walking them is what would let one Node's cleanup delete another Node's timestamp and hand it a
// window it had already used up.
type missTracker struct {
	entries *cache.Cache
}

func newMissTracker(window time.Duration) *missTracker {
	return &missTracker{entries: cache.New(2*window, window)}
}

// firstMiss returns when this Node's NodeClaim was first missing, recording now if this is the
// first time of asking.
func (m *missTracker) firstMiss(uid types.UID, now time.Time) time.Time {
	if stored, ok := m.entries.Get(string(uid)); ok {
		if first, ok := stored.(time.Time); ok {
			return first
		}
	}
	m.entries.SetDefault(string(uid), now)

	return now
}

// forget drops what was remembered about a Node, so its next miss starts a fresh window. Called
// when the NodeClaim turns up, which is the point at which the wait is over.
func (m *missTracker) forget(uid types.UID) {
	m.entries.Delete(string(uid))
}
