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

	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
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
	nodeClaim *corev1.NodeClaim) error {
	if node == nil || nodeClaim == nil {
		return nil
	}

	instanceTypeName := node.Labels[v1.LabelInstanceTypeStable]
	if instanceTypeName == "" {
		// Nothing to key on. A managed node without this label is not something we can model.
		return nil
	}

	// Label the measurement with the image this node actually booted from, taken from the launched
	// instance rather than re-resolved. Re-resolving would be wrong: if selection moved between
	// launch and registration, we would file this node's memory under an image it never ran.
	if nodeClaim.Status.ImageID == "" {
		return nil
	}

	capacity, ok := node.Status.Capacity[v1.ResourceMemory]
	if !ok || capacity.IsZero() {
		// The node is registered but has not published memory yet. The controller only watches the
		// transition into the registered state, so nothing would bring us back here on its own;
		// report that so the caller can retry rather than losing this node's measurement entirely.
		return ErrCapacityNotReported
	}

	p.discoveredCapacity.Record(ctx, discoveredCapacityCacheKey(instanceTypeName, nodeClaim.Status.ImageID), capacity)
	return nil
}

// DiscoveryEnabled reports whether measured capacity is used at all.
func (p *DefaultProvider) DiscoveryEnabled() bool {
	return p.discoveredCapacity.Enabled() && p.imageProvider != nil
}

// resolveImageForDiscovery returns the image the given shape would launch with, or "" if that
// cannot be established. It is resolved exactly as CloudProvider.Create resolves it, so the key
// derived from it matches the key the measurement was filed under.
//
// It returns "" rather than an error, and does no work at all when discovery is switched off,
// because this sits on the scheduling path: capacity discovery is an optimisation over the
// modelled estimate, and neither scheduling nor its latency should depend on the image API.
func (p *DefaultProvider) resolveImageForDiscovery(ctx context.Context, shape string,
	nodeClass *ociv1beta1.OCINodeClass) string {
	if !p.DiscoveryEnabled() || nodeClass == nil {
		return ""
	}
	if nodeClass.Spec.VolumeConfig == nil || nodeClass.Spec.VolumeConfig.BootVolumeConfig == nil {
		return ""
	}

	resolved, err := p.imageProvider.ResolveImageForShape(ctx,
		nodeClass.Spec.VolumeConfig.BootVolumeConfig.ImageConfig, shape)
	if err != nil || resolved == nil || len(resolved.Images) == 0 || resolved.Images[0].Id == nil {
		log.FromContext(ctx).V(1).Info("skipping discovered capacity: cannot resolve image for shape",
			"shape", shape)
		return ""
	}

	return *resolved.Images[0].Id
}

// applyDiscoveredCapacity overrides an instance type's modelled memory with a value measured on a
// node of the same instance type and image. It is a no-op until such a node has registered, so the
// modelled figure governs only the first launch of a combination.
//
// Overhead (kubeReserved, eviction thresholds) is deliberately left as modelled. It is derived
// from the shape's declared memory, which is slightly larger than the real figure, so the reserve
// is marginally generous and allocatable stays on the conservative side.
func (p *DefaultProvider) applyDiscoveredCapacity(it *OciInstanceType, imageID string) {
	if imageID == "" || it.Capacity == nil {
		return
	}

	if discovered, ok := p.discoveredCapacity.Get(discoveredCapacityCacheKey(it.Name, imageID)); ok {
		it.Capacity[v1.ResourceMemory] = discovered
	}
}

// discoveredCapacityCacheKey identifies a measurement by the instance type it was taken on and the
// image that instance type booted.
//
// The instance type name already encodes shape, OCPU, memory and CPU baseline, so for flexible
// shapes it distinguishes configurations without further work.
//
// Keying on the resolved image rather than on the NodeClass's whole candidate list means adding an
// image to the list only affects the combinations whose selection actually changes; every other
// learned value survives. OKE publishes images regularly, so a key that invalidated everything on
// each publication would spend much of its life empty.
//
// Shape and image are an empirical grouping, not a documented OCI contract. Oracle publishes a
// shape's allocated memory and image/shape compatibility, but not the memory a guest ends up
// seeing. The closest it comes is the Dedicated VM Host table, which does carry a "usable memory"
// column and attributes the shortfall to "the need to reserve OCPUs and memory for hypervisor
// use" - there is no equivalent column for the ordinary VM shapes this models:
// https://docs.oracle.com/en-us/iaas/Content/Compute/References/computeshapes.htm
//
// So other factors - host generation, firmware, hypervisor version - may also move the figure.
// The grouping does not have to be exact to be useful: Record keeps the smallest value observed
// for a key, so if several host variants share one, the model converges on the least roomy of
// them. A coarse key costs a little capacity; it does not cost correctness.
func discoveredCapacityCacheKey(instanceTypeName, imageID string) string {
	return fmt.Sprintf("%s-%s", instanceTypeName, imageID)
}
