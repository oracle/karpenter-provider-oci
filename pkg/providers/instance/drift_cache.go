/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package instance

import (
	"github.com/oracle/karpenter-provider-oci/pkg/cache"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
)

// DriftCaches owns the non-expiring instance, attachment, and child-resource
// caches used by drift detection. Instance eviction is the root of the cleanup
// cascade.
type DriftCaches struct {
	instances             *cache.GetOrLoadCache[*InstanceInfo]
	vnicAttachments       *cache.GetOrLoadCache[[]*ocicore.VnicAttachment]
	bootVolumeAttachments *cache.GetOrLoadCache[[]*ocicore.BootVolumeAttachment]
	vnics                 *cache.GetOrLoadCache[*ocicore.Vnic]
	bootVolumes           *cache.GetOrLoadCache[*ocicore.BootVolume]
}

func NewDriftCaches() *DriftCaches {
	caches := &DriftCaches{
		instances:             cache.NewNonExpiringGetOrLoadCache[*InstanceInfo](),
		vnicAttachments:       cache.NewNonExpiringGetOrLoadCache[[]*ocicore.VnicAttachment](),
		bootVolumeAttachments: cache.NewNonExpiringGetOrLoadCache[[]*ocicore.BootVolumeAttachment](),
		vnics:                 cache.NewNonExpiringGetOrLoadCache[*ocicore.Vnic](),
		bootVolumes:           cache.NewNonExpiringGetOrLoadCache[*ocicore.BootVolume](),
	}

	caches.instances.OnEvicted(func(instanceID string, _ *InstanceInfo) {
		caches.vnicAttachments.Evict(instanceID)
		caches.bootVolumeAttachments.Evict(instanceID)
	})
	caches.vnicAttachments.OnEvicted(func(_ string, attachments []*ocicore.VnicAttachment) {
		for _, attachment := range attachments {
			if attachment != nil && attachment.VnicId != nil {
				caches.vnics.Evict(*attachment.VnicId)
			}
		}
	})
	caches.bootVolumeAttachments.OnEvicted(func(_ string, attachments []*ocicore.BootVolumeAttachment) {
		for _, attachment := range attachments {
			if attachment != nil && attachment.BootVolumeId != nil {
				caches.bootVolumes.Evict(*attachment.BootVolumeId)
			}
		}
	})

	return caches
}

func (c *DriftCaches) VnicCache() *cache.GetOrLoadCache[*ocicore.Vnic] {
	return c.vnics
}

func (c *DriftCaches) BootVolumeCache() *cache.GetOrLoadCache[*ocicore.BootVolume] {
	return c.bootVolumes
}
