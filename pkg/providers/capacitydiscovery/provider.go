/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

// Package capacitydiscovery learns the memory a shape really presents to a node, by observing
// nodes that have registered, and offers it back for modelling later launches of the same kind.
//
// Karpenter has to size a node before it exists, so it starts from an estimate derived from the
// shape's declared memory. OCI reserves some of that below the guest, so the estimate is slightly
// high, and nothing otherwise compares it against the node it produced. An estimate that is too
// high therefore does not cause one bad launch but an unbounded run of them: the pod never fits,
// stays pending, and the identical decision is taken again.
//
// Everything the correction needs - the measurements, the image resolution that keys them, and
// the backoff that bounds it - lives here, so that instance type modelling stays a calculation
// over shape metadata and this stays an optional refinement of it.
package capacitydiscovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mitchellh/hashstructure/v2"
	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/metrics"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/image"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/log"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// Advisor offers memory capacity measured on real nodes. It is the whole of what instance type
// modelling needs from this package, so that modelling does not acquire image resolution, caches
// or backoff as dependencies.
type Advisor interface {
	// AdviseForShape returns advice covering every instance type of one shape.
	//
	// Advice is per shape rather than per instance type because a flexible shape expands into many
	// instance types that share an image, and resolving that image is the only part of this that
	// can reach OCI. Resolving once per shape keeps a failing image API from being asked once per
	// configuration while the caller holds its lock.
	//
	// It never fails. When nothing can be advised - discovery is off, no image resolves, nothing
	// has been measured - it returns advice that declines every instance type, so the caller keeps
	// its estimate and listing instance types never depends on this succeeding.
	AdviseForShape(ctx context.Context, shape string, nodeClass *ociv1beta1.OCINodeClass) Advice
}

// Advice answers for the instance types of the shape it was obtained for.
type Advice interface {
	// MemoryFor returns the memory measured for this instance type, and whether any was.
	MemoryFor(instanceTypeName string) (resource.Quantity, bool)
}

// Recorder takes the measurements Advisor later hands back. The controller that watches nodes
// needs only this half.
type Recorder interface {
	// RecordNodeCapacity files the memory a registered node reported.
	RecordNodeCapacity(ctx context.Context, node *v1.Node, nodeClaim *corev1.NodeClaim) error

	// Enabled reports whether measurements are used at all. The controller that feeds this
	// provider is not registered when they are not.
	Enabled() bool
}

// Provider is both halves, which is what the operator constructs and shares.
type Provider interface {
	Advisor
	Recorder
}

const (
	// DefaultNodeCapacityTTL is how long a memory capacity measured on a real node is reused for
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
	DefaultNodeCapacityTTL = 60 * 24 * time.Hour
	// nodeCapacityCleanupInterval triggers cleanup of the discovered-capacity cache.
	nodeCapacityCleanupInterval = time.Hour

	// imageResolutionTimeout bounds a single image resolution attempted while listing instance
	// types. Discovery is an optimisation over the modelled estimate, so a slow image API must
	// degrade the model rather than hold up scheduling.
	imageResolutionTimeout = 5 * time.Second
	// imageResolutionFailureTTL is how long a failed resolution suppresses further attempts for
	// the same shape. Failed loads are not cached by the image provider, so without this a broken
	// or expired lookup is retried for every shape on every listing. It is deliberately short:
	// the cost of waiting is only that discovery stays off a little longer once the API recovers.
	imageResolutionFailureTTL = 30 * time.Second

	// imageIncompatibleTTL is how long "this configuration has no image for this shape" suppresses
	// further attempts. Far longer than a failure, because it is a settled answer rather than a
	// condition that recovers: it changes only when the NodeClass or its images change, and both
	// are part of the key, so a change produces a new key rather than needing this to expire. It
	// is what keeps a NodeClass with a narrow image filter from re-asking, and re-logging, for
	// every shape in the catalogue on every listing.
	imageIncompatibleTTL = time.Hour
)

// ErrCapacityNotReported signals that a registered node has not published its memory capacity yet,
// so the caller should retry rather than treat the node as having nothing to teach.
var ErrCapacityNotReported = errors.New("node has not reported memory capacity yet")

type DefaultProvider struct {
	measured      *nodeCapacity
	imageProvider image.Provider
	failures      *imageResolutionFailures
	// resolutionTimeout bounds one resolution attempt. A field rather than the constant so tests
	// can shorten it; production always takes imageResolutionTimeout.
	resolutionTimeout time.Duration
}

// New builds a provider that reuses a measurement for ttl. A ttl of zero switches discovery off:
// nothing is recorded, nothing is advised, and the operator does not register the controller.
func New(imageProvider image.Provider, ttl time.Duration) *DefaultProvider {
	return &DefaultProvider{
		measured:          newNodeCapacity(ttl),
		imageProvider:     imageProvider,
		failures:          newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL),
		resolutionTimeout: imageResolutionTimeout,
	}
}

// Enabled reports whether measured capacity is used at all.
func (p *DefaultProvider) Enabled() bool {
	return p != nil && p.measured.Enabled() && p.imageProvider != nil
}

// RecordNodeCapacity files the memory a registered node actually reported, so that later launches
// of the same instance type and image are modelled from that measurement instead of the estimate.
//
// It is deliberately forgiving: anything it cannot establish with confidence is skipped rather
// than guessed, because a wrong entry here is worse than no entry - it would be reused for every
// subsequent launch of that combination.
func (p *DefaultProvider) RecordNodeCapacity(ctx context.Context, node *v1.Node,
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

	p.measured.Record(ctx, cacheKey(instanceTypeName, nodeClaim.Status.ImageID), capacity)
	return nil
}

// AdviseForShape resolves the image this shape would launch with, then hands back the
// measurements filed against it.
func (p *DefaultProvider) AdviseForShape(ctx context.Context, shape string,
	nodeClass *ociv1beta1.OCINodeClass) Advice {
	imageID, outcome := p.resolveImage(ctx, shape, nodeClass)
	if imageID == "" {
		// The outcome travels with the advice rather than being counted here, so that every
		// instance type modelled contributes exactly one sample whatever the outcome. Counting
		// per-shape outcomes here and per-instance-type ones below would make the denominator
		// depend on how far a flexible shape expands.
		return noAdvice{outcome: outcome}
	}

	return shapeAdvice{imageID: imageID, measured: p.measured}
}

// shapeAdvice answers from measurements filed against one resolved image.
type shapeAdvice struct {
	imageID  string
	measured *nodeCapacity
}

func (a shapeAdvice) MemoryFor(instanceTypeName string) (resource.Quantity, bool) {
	measured, ok := a.measured.Get(cacheKey(instanceTypeName, a.imageID))
	if !ok {
		// Nothing has been launched of this kind yet, or what was has expired. Expected, and the
		// ordinary state before the first node of a combination registers.
		metrics.CapacityAdviceCounter.Inc(map[string]string{metrics.OutcomeLabel: metrics.OutcomeNoMeasurement})
		return resource.Quantity{}, false
	}

	metrics.CapacityAdviceCounter.Inc(map[string]string{metrics.OutcomeLabel: metrics.OutcomeApplied})
	return measured, true
}

// NoAdvice is advice that declines every instance type, for a caller with no advisor configured.
// That is indistinguishable from discovery being switched off, and is reported as such.
func NoAdvice() Advice { return noAdvice{outcome: metrics.OutcomeDisabled} }

// noAdvice declines every instance type, so a caller that can get no advice takes the same path as
// one whose instance types have simply not been measured yet. It carries why, so the reason is
// still counted once per instance type modelled.
type noAdvice struct{ outcome string }

func (a noAdvice) MemoryFor(string) (resource.Quantity, bool) {
	metrics.CapacityAdviceCounter.Inc(map[string]string{metrics.OutcomeLabel: a.outcome})
	return resource.Quantity{}, false
}

// resolveImage returns the image the given shape would launch with, and the outcome to report when
// it cannot. The image is resolved exactly as CloudProvider.Create resolves it, so the key derived
// from it matches the key the measurement was filed under.
//
// It returns "" rather than an error, and does no work at all when discovery is switched off,
// because this sits on the scheduling path: this is an optimisation over the modelled estimate,
// and neither scheduling nor its latency should depend on the image API.
func (p *DefaultProvider) resolveImage(ctx context.Context, shape string,
	nodeClass *ociv1beta1.OCINodeClass) (imageID, outcome string) {
	if !p.Enabled() {
		return "", metrics.OutcomeDisabled
	}
	if nodeClass == nil || nodeClass.Spec.VolumeConfig == nil ||
		nodeClass.Spec.VolumeConfig.BootVolumeConfig == nil {
		return "", metrics.OutcomeNoImage
	}

	// Suppression is keyed by shape and image configuration together, because resolution depends on
	// both: it picks whichever of this NodeClass's images is compatible with this shape. A failure
	// means "none of these images cover this shape", which is specific to the pair. Keying on the
	// shape alone would let one NodeClass's ARM-only filter suppress a different NodeClass whose
	// x86 images resolve that shape perfectly well.
	//
	// The image provider does not cache failed loads, so without this a broken or expired lookup is
	// retried for every shape on every listing, each with its own retries, while the caller holds
	// its lock. Suppression is also what keeps logging restrained: one line per key per TTL rather
	// than one per shape per listing.
	failureKey := failureKey(shape, nodeClass.Spec.VolumeConfig.BootVolumeConfig.ImageConfig)
	if p.failures.RecentlyFailed(failureKey) {
		return "", metrics.OutcomeSuppressed
	}

	// Bound a single attempt. This is an optimisation over the modelled estimate, so a slow image
	// API must degrade the model rather than delay scheduling.
	timeout := p.resolutionTimeout
	if timeout <= 0 {
		timeout = imageResolutionTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resolved, err := p.imageProvider.ResolveImageForShape(ctx,
		nodeClass.Spec.VolumeConfig.BootVolumeConfig.ImageConfig, shape)
	if err == nil && !usableResult(resolved) {
		// Success carrying nothing usable is the image provider breaking its own contract. Name it
		// so the failure branch below has something to report rather than logging a nil error.
		err = fmt.Errorf("%w: resolution succeeded but returned no usable image", errUnusableResult)
	}
	switch {
	case image.IsConfigurationError(err):
		// Resolution answered, and the answer is that this configuration yields no image for this
		// shape. That is a settled fact about the pair, not a fault: retrying cannot change it, it
		// cost nothing to ask, and it says nothing about any other key. So it suppresses this key
		// alone - a NodeClass whose filter covers few shapes must not switch discovery off for
		// every other NodeClass - and is not logged as an error, because it is not one.
		p.failures.RecordIncompatible(failureKey)
		log.FromContext(ctx).V(1).Info("no image for shape; modelling capacity from the estimate",
			"shape", shape, "reason", err, "not-retrying-for", imageIncompatibleTTL)
		return "", metrics.OutcomeNoImage
	case err != nil:
		// Either the service could not answer, or it answered with something unusable. Both say
		// something about the image API rather than about this shape, so both count towards
		// suppressing resolution wholesale - that is what stops a listing paying one timeout per
		// shape while the API is down - and both carry the underlying error.
		p.failures.RecordFailure(failureKey)
		log.FromContext(ctx).Error(err, "resolving image for capacity discovery; modelling capacity "+
			"from the estimate", "shape", shape, "not-retrying-for", imageResolutionFailureTTL)
		return "", metrics.OutcomeResolutionFailed
	}

	p.failures.RecordSuccess()

	return *resolved.Images[0].Id, ""
}

// errUnusableResult stands in for the error the image provider did not return when it answered
// successfully with nothing an instance type could be keyed on.
var errUnusableResult = errors.New("image provider returned an unusable result")

// usableResult reports whether a resolution result actually names an image. Every field is checked
// rather than assumed, because this is the value the measurement cache key is built from: a wrong
// or empty one would file and serve measurements under a key no node ever booted.
func usableResult(resolved *image.ImageResolveResult) bool {
	if resolved == nil || len(resolved.Images) == 0 {
		return false
	}
	first := resolved.Images[0]

	return first != nil && first.Id != nil && *first.Id != ""
}

// failureKey identifies a resolution attempt by the two things it depends on: the shape, and the
// NodeClass image configuration the candidate images come from.
//
// Hashing the configuration rather than naming it keeps NodeClasses that share an image policy on
// one entry - they will resolve identically - while separating those that do not.
func failureKey(shape string, imageCfg *ociv1beta1.ImageConfig) string {
	hash, _ := hashstructure.Hash(imageCfg, hashstructure.FormatV2, &hashstructure.HashOptions{})

	return fmt.Sprintf("%s-%016x", shape, hash)
}

// cacheKey identifies a measurement by the instance type it was taken on and the image that
// instance type booted.
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
func cacheKey(instanceTypeName, imageID string) string {
	return fmt.Sprintf("%s-%s", instanceTypeName, imageID)
}
