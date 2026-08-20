/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package instancetype

import (
	"context"
	"errors"
	"fmt"

	"github.com/mitchellh/hashstructure/v2"
	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// ErrCapacityNotReported signals that a registered node has not published its memory capacity yet,
// so the caller should retry rather than treat the node as having nothing to teach.
var ErrCapacityNotReported = errors.New("node has not reported memory capacity yet")

// UpdateInstanceTypeCapacityFromNode records the memory a registered node actually reported, so
// that later launches of the same instance type and image are modelled from that measurement
// instead of the estimate.
//
// It is deliberately forgiving: anything it cannot establish with confidence is skipped rather
// than guessed, because a wrong entry here is worse than no entry — it would be reused for every
// subsequent launch of that combination.
func (p *DefaultProvider) UpdateInstanceTypeCapacityFromNode(ctx context.Context, node *v1.Node,
	nodeClaim *corev1.NodeClaim, nodeClass *ociv1beta1.OCINodeClass) error {
	if node == nil || nodeClaim == nil || nodeClass == nil {
		return nil
	}

	instanceTypeName := node.Labels[v1.LabelInstanceTypeStable]
	if instanceTypeName == "" {
		// Nothing to key on. A managed node without this label is not something we can model.
		return nil
	}

	// Only trust the measurement if the image the node booted from is still one this NodeClass
	// would choose. After an image update the previous image's memory is not evidence about the
	// new one, and the entry would otherwise outlive the image it describes.
	if !imageIsCurrent(nodeClaim.Status.ImageID, nodeClass) {
		return nil
	}

	capacity, ok := node.Status.Capacity[v1.ResourceMemory]
	if !ok || capacity.IsZero() {
		// The node is registered but has not published memory yet. The controller only watches the
		// transition into the registered state, so nothing would bring us back here on its own;
		// report that so the caller can retry rather than losing this node's measurement entirely.
		return ErrCapacityNotReported
	}

	p.discoveredCapacity.Record(ctx, discoveredCapacityCacheKey(instanceTypeName, nodeClass), capacity)
	return nil
}

// applyDiscoveredCapacity overrides an instance type's modelled memory with a measured value when
// one is known. It is a no-op until a node of that combination has registered, so the modelled
// figure governs only the first launch.
//
// Overhead (kubeReserved, eviction thresholds) is deliberately left as modelled. It is derived
// from the shape's declared memory, which is slightly larger than the real figure, so the reserve
// is marginally generous and allocatable stays on the conservative side.
func (p *DefaultProvider) applyDiscoveredCapacity(it *OciInstanceType, nodeClass *ociv1beta1.OCINodeClass) {
	if p.discoveredCapacity == nil || nodeClass == nil || it.Capacity == nil {
		return
	}

	if discovered, ok := p.discoveredCapacity.Get(discoveredCapacityCacheKey(it.Name, nodeClass)); ok {
		it.Capacity[v1.ResourceMemory] = discovered
	}
}

// imageIsCurrent reports whether imageID is still among the NodeClass's resolved image candidates.
//
// This is weaker than checking that the node's shape would resolve to precisely this image: OCI
// expresses image/shape compatibility as a server-side relation rather than as requirements
// recorded on the NodeClass, so establishing it here would cost an API call on a path that must
// stay cheap.
//
// The gap it leaves is narrow and self-correcting. A change to the candidate list changes the cache
// key, so entries never outlive the list they were measured under. What membership alone does not
// catch is a list that changes between a node launching and registering: the node's image may still
// be a candidate while no longer being the one its shape would now select, and its measurement is
// then recorded under the new list's key. The next node launched from the newly selected image
// records its own, and because the smallest observation wins, an over-estimate survives at most
// until then - the same one-launch bound this mechanism offers generally.
func imageIsCurrent(imageID string, nodeClass *ociv1beta1.OCINodeClass) bool {
	if imageID == "" {
		return false
	}
	if nodeClass.Status.Volume == nil {
		return false
	}

	return lo.ContainsBy(nodeClass.Status.Volume.ImageCandidates, func(img *ociv1beta1.Image) bool {
		return img != nil && img.ImageId == imageID
	})
}

// discoveredCapacityCacheKey identifies an instance type together with the images it could boot.
//
// The instance type name already encodes shape, OCPU, memory and CPU baseline, so for flexible
// shapes it distinguishes configurations without further work.
//
// The image half hashes the whole candidate list rather than a single resolved image, so the key
// can be computed while scheduling without asking OCI which image a shape would get. The hash is
// order-sensitive on purpose: image selection walks the sorted candidates and takes the first
// compatible one, so a reordered list can select a different image and must not reuse the same
// entry.
func discoveredCapacityCacheKey(instanceTypeName string, nodeClass *ociv1beta1.OCINodeClass) string {
	var candidates []*ociv1beta1.Image
	if nodeClass.Status.Volume != nil {
		candidates = nodeClass.Status.Volume.ImageCandidates
	}

	hash, _ := hashstructure.Hash(candidates, hashstructure.FormatV2, &hashstructure.HashOptions{
		SlicesAsSets: false,
	})

	return fmt.Sprintf("%s-%016x", instanceTypeName, hash)
}
