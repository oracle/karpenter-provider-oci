/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package capacitydiscovery

import (
	"context"
	"fmt"
	"testing"
	"time"

	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/metrics"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/image"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// resolvedImage is resolveImage's image alone, for the many tests that assert on what resolved
// rather than on why it did not.
func (p *DefaultProvider) resolvedImage(ctx context.Context, shape string,
	nodeClass *ociv1beta1.OCINodeClass) string {
	imageID, _ := p.resolveImage(ctx, shape, nodeClass)
	return imageID
}

// The two halves are separate interfaces so that a consumer of one is not coupled to the other,
// and one provider serves both.
func TestDefaultProviderSatisfiesBothInterfaces(t *testing.T) {
	p := discoveryProvider()

	var advisor Advisor = p
	var recorder Recorder = p
	var both Provider = p

	assert.NotNil(t, advisor)
	assert.NotNil(t, both)
	assert.True(t, recorder.Enabled())
}

const (
	testInstanceTypeName = "VM.Standard.E5.Flex.8o.32g.1_1b"
	testShape            = "VM.Standard.E5.Flex"
	testImageID          = "ocid1.image.oc1..a"
)

// fakeImageProvider resolves every shape to one image, or fails, so the read path's dependency on
// image resolution can be exercised without OCI.
type fakeImageProvider struct {
	imageID string
	err     error
	// block, when set, holds the resolver until released or the context is cancelled, so the
	// timeout can be observed.
	block chan struct{}
	// failShapes, when set, fails only for those shapes and succeeds for the rest, modelling one
	// shape with no compatible image among healthy ones.
	failShapes map[string]bool
	// failConfigs reports no compatible image for NodeClasses whose configured imageId is listed,
	// modelling an image policy that does not cover the shape being asked about.
	failConfigs map[string]bool
	// incompatibleShapes reports no compatible image for those shapes, as distinct from the image
	// service failing. failShapes models the latter.
	incompatibleShapes map[string]bool
	// configErr, when set, is returned instead of resolving, modelling a configuration that cannot
	// yield an image at all rather than one that misses a particular shape.
	configErr error
	// emptyResult returns success with nothing usable in it, modelling the image provider breaking
	// its own contract. nilResult and nilImageID are the other two shapes that can take.
	emptyResult   bool
	nilResult     bool
	nilImageID    bool
	nilImageEntry bool
	// gotShapes records what resolution was asked for, so passing the wrong identifier - the
	// instance type name instead of the shape, say - cannot pass unnoticed.
	gotShapes []string
}

func (f *fakeImageProvider) ResolveImages(context.Context, *ociv1beta1.ImageConfig) (*image.ImageResolveResult, error) {
	return f.resolve()
}

func (f *fakeImageProvider) ResolveImageForShape(ctx context.Context, cfg *ociv1beta1.ImageConfig,
	shape string) (*image.ImageResolveResult, error) {
	f.gotShapes = append(f.gotShapes, shape)
	if cfg != nil && cfg.ImageId != nil && f.failConfigs[*cfg.ImageId] {
		return nil, fmt.Errorf("%w: %s", image.ErrNoCompatibleImage, shape)
	}
	if f.incompatibleShapes[shape] {
		return nil, fmt.Errorf("%w: %s", image.ErrNoCompatibleImage, shape)
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.failShapes[shape] {
		return nil, assert.AnError
	}
	if f.configErr != nil {
		return nil, f.configErr
	}
	if f.emptyResult {
		return &image.ImageResolveResult{}, nil
	}
	if f.nilResult {
		return nil, nil
	}
	if f.nilImageID {
		return &image.ImageResolveResult{Images: []*ocicore.Image{{}}}, nil
	}
	if f.nilImageEntry {
		return &image.ImageResolveResult{Images: []*ocicore.Image{nil}}, nil
	}
	return f.resolve()
}

func (f *fakeImageProvider) resolve() (*image.ImageResolveResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &image.ImageResolveResult{Images: []*ocicore.Image{{Id: lo.ToPtr(f.imageID)}}}, nil
}

// nodeClassWithImage builds a NodeClass whose image configuration is distinct, so two of them
// represent two different image policies - an ARM-only filter and an x86 one, say.
func nodeClassWithImage(imageID string) *ociv1beta1.OCINodeClass {
	nc := discoveryNodeClass(imageID)
	nc.Spec.VolumeConfig.BootVolumeConfig.ImageConfig = &ociv1beta1.ImageConfig{
		ImageId: lo.ToPtr(imageID),
	}
	return nc
}

func discoveryNodeClass(imageIDs ...string) *ociv1beta1.OCINodeClass {
	return &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			VolumeConfig: &ociv1beta1.VolumeConfig{
				BootVolumeConfig: &ociv1beta1.BootVolumeConfig{
					ImageConfig: &ociv1beta1.ImageConfig{},
				},
			},
		},
		Status: ociv1beta1.OCINodeClassStatus{
			Volume: &ociv1beta1.Volume{
				ImageCandidates: lo.Map(imageIDs, func(id string, _ int) *ociv1beta1.Image {
					return &ociv1beta1.Image{ImageId: id, DisplayName: id}
				}),
			},
		},
	}
}

func discoveryNode(instanceType, memory string) *v1.Node {
	n := &v1.Node{}
	if instanceType != "" {
		n.Labels = map[string]string{v1.LabelInstanceTypeStable: instanceType}
	}
	if memory != "" {
		n.Status.Capacity = v1.ResourceList{v1.ResourceMemory: resource.MustParse(memory)}
	}
	return n
}

func discoveryNodeClaim(imageID string) *corev1.NodeClaim {
	return &corev1.NodeClaim{Status: corev1.NodeClaimStatus{ImageID: imageID}}
}

func discoveryProvider() *DefaultProvider {
	return &DefaultProvider{
		measured:      newNodeCapacity(DefaultNodeCapacityTTL),
		imageProvider: &fakeImageProvider{imageID: testImageID},
	}
}

// A node reporting less memory than was modelled is the whole point: record it so the next launch
// of that combination is sized from measurement rather than the estimate.
func TestRecordNodeCapacity_RecordsObservedMemory(t *testing.T) {
	p := discoveryProvider()

	err := p.RecordNodeCapacity(context.Background(),
		discoveryNode(testInstanceTypeName, "30890Mi"), discoveryNodeClaim("ocid1.image.oc1..a"))
	assert.NoError(t, err)

	got, ok := p.measured.Get(cacheKey(testInstanceTypeName, testImageID))
	assert.True(t, ok, "expected the observation to be recorded")
	want := resource.MustParse("30890Mi")
	assert.Equal(t, want.Value(), got.Value())
}

// Nodes of nominally the same kind can report slightly different totals. Keeping the smallest
// keeps the model on the safe side: over-estimating drives the launch loop this exists to stop,
// while under-estimating only wastes memory.
func TestRecordNodeCapacity_KeepsSmallestObserved(t *testing.T) {
	p := discoveryProvider()
	ctx := context.Background()
	key := cacheKey(testInstanceTypeName, testImageID)

	for _, mem := range []string{"30890Mi", "30800Mi", "31000Mi"} {
		assert.NoError(t, p.RecordNodeCapacity(ctx,
			discoveryNode(testInstanceTypeName, mem), discoveryNodeClaim("ocid1.image.oc1..a")))
	}

	got, ok := p.measured.Get(key)
	assert.True(t, ok)
	want := resource.MustParse("30800Mi")
	assert.Equal(t, want.Value(), got.Value(),
		"a larger later observation must not raise the recorded capacity")
}

func TestRecordNodeCapacity_Skips(t *testing.T) {
	tests := []struct {
		name      string
		node      *v1.Node
		nodeClaim *corev1.NodeClaim
		reason    string
	}{
		{
			name:      "no instance-type label",
			node:      discoveryNode("", "30890Mi"),
			nodeClaim: discoveryNodeClaim("ocid1.image.oc1..a"),
			reason:    "there is nothing to key the measurement on",
		},
		{
			name:      "nodeclaim has no image id",
			node:      discoveryNode(testInstanceTypeName, "30890Mi"),
			nodeClaim: discoveryNodeClaim(""),
			reason:    "we cannot tell which image produced this measurement",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := discoveryProvider()

			assert.NoError(t, p.RecordNodeCapacity(
				context.Background(), tt.node, tt.nodeClaim))

			_, ok := p.measured.Get(cacheKey(testInstanceTypeName, testImageID))
			assert.False(t, ok, "must not record: %s", tt.reason)
		})
	}
}

func TestRecordNodeCapacity_NilInputs(t *testing.T) {
	p := discoveryProvider()
	ctx := context.Background()

	assert.NoError(t, p.RecordNodeCapacity(ctx, nil, discoveryNodeClaim("a")))
	assert.NoError(t, p.RecordNodeCapacity(ctx, discoveryNode(testInstanceTypeName, "1Gi"), nil))

	_, ok := p.measured.Get(cacheKey(testInstanceTypeName, "a"))
	assert.False(t, ok, "a nil node or nodeclaim must record nothing")
}

// The key names the image a node actually booted, so a measurement taken under one image is never
// served for another.
func TestCacheKey(t *testing.T) {
	const a, b = "ocid1.image.oc1..a", "ocid1.image.oc1..b"

	assert.Equal(t, cacheKey(testInstanceTypeName, a),
		cacheKey(testInstanceTypeName, a), "key must be stable")
	assert.NotEqual(t, cacheKey(testInstanceTypeName, a),
		cacheKey(testInstanceTypeName, b),
		"a different image must not reuse the entry")
	assert.NotEqual(t, cacheKey(testInstanceTypeName, a),
		cacheKey("VM.Standard.E5.Flex.4o.16g.1_1b", a),
		"a different instance type must not reuse the entry")
}

// Naming the resolved image rather than the NodeClass's whole candidate list is the point of the
// key: OKE publishes images regularly, and a key derived from the list would discard every learned
// value each time one appeared, including for shapes whose selection did not change.
func TestUnrelatedImageDoesNotInvalidate(t *testing.T) {
	p := discoveryProvider()
	nc := discoveryNodeClass(testImageID)
	ctx := context.Background()

	assert.NoError(t, p.RecordNodeCapacity(ctx,
		discoveryNode(testInstanceTypeName, "30890Mi"), discoveryNodeClaim(testImageID)))

	// A new image is published and joins the candidate list, but this shape still selects the same
	// one, so the measurement must still be found.
	nc.Status.Volume.ImageCandidates = append(nc.Status.Volume.ImageCandidates,
		&ociv1beta1.Image{ImageId: "ocid1.image.oc1..newly-published"})

	measured, ok := p.AdviseForShape(ctx, testShape, nc).MemoryFor(testInstanceTypeName)

	require.True(t, ok, "an unrelated image joining the candidate list must not discard what we learned")
	want := resource.MustParse("30890Mi")
	assert.Equal(t, want.Value(), measured.Value())
}

// Scheduling must not depend on the image API being reachable: if resolution fails there is no key
// to look under, and the modelled estimate - which is deliberately conservative - stands.
func TestResolveImage_FailureYieldsNoKey(t *testing.T) {
	p := discoveryProvider()
	p.imageProvider = &fakeImageProvider{err: assert.AnError}

	assert.Equal(t, "", p.resolvedImage(context.Background(), testShape, discoveryNodeClass(testImageID)))
}

// Turning the feature off must skip the resolution too, not just the lookup: resolving is the part
// that can reach OCI, so paying for it while discarding the result would be the worst of both.
func TestResolveImage_DisabledDoesNoWork(t *testing.T) {
	fake := &fakeImageProvider{imageID: testImageID}
	p := &DefaultProvider{
		measured:      newNodeCapacity(0), // 0 == disabled
		imageProvider: fake,
	}

	assert.Equal(t, "", p.resolvedImage(context.Background(), testShape, discoveryNodeClass(testImageID)))
	assert.Empty(t, fake.gotShapes, "a disabled cache must not trigger image resolution at all")
}

// The resolver must be asked about the shape, not the synthetic instance type name: flexible
// instance types are named Shape.<X>o.<Y>g.<Z>b, which no image is compatible with.
func TestResolveImage_AsksForTheShape(t *testing.T) {
	fake := &fakeImageProvider{imageID: testImageID}
	p := &DefaultProvider{
		measured:      newNodeCapacity(DefaultNodeCapacityTTL),
		imageProvider: fake,
	}

	p.resolvedImage(context.Background(), testShape, discoveryNodeClass(testImageID))

	assert.Equal(t, []string{testShape}, fake.gotShapes)
}

// Advice is offered only once something has been measured, so that a caller with no advice keeps
// whatever it modelled for itself.
func TestAdviseForShape(t *testing.T) {
	ctx := context.Background()

	t.Run("offers the measured value", func(t *testing.T) {
		p := discoveryProvider()
		p.measured.Record(ctx, cacheKey(testInstanceTypeName, testImageID), resource.MustParse("30890Mi"))

		measured, ok := p.AdviseForShape(ctx, testShape, discoveryNodeClass(testImageID)).
			MemoryFor(testInstanceTypeName)

		require.True(t, ok)
		want := resource.MustParse("30890Mi")
		assert.Equal(t, want.Value(), measured.Value())
	})

	t.Run("declines before anything is measured", func(t *testing.T) {
		p := discoveryProvider()

		_, ok := p.AdviseForShape(ctx, testShape, discoveryNodeClass(testImageID)).
			MemoryFor(testInstanceTypeName)

		assert.False(t, ok, "the first launch of a combination has nothing to learn from")
	})
}

// A disabled cache must leave modelling exactly as it was, so the feature can be turned off.
func TestDisabled(t *testing.T) {
	p := &DefaultProvider{measured: newNodeCapacity(0)}

	assert.NoError(t, p.RecordNodeCapacity(context.Background(),
		discoveryNode(testInstanceTypeName, "30890Mi"), discoveryNodeClaim("ocid1.image.oc1..a")))

	_, ok := p.AdviseForShape(context.Background(), testShape, discoveryNodeClass(testImageID)).
		MemoryFor(testInstanceTypeName)

	assert.False(t, ok, "a disabled provider must have nothing to offer")
}

// A registered node that has not published memory yet must be retried, not dropped: the controller
// only watches the transition into the registered state, so nothing else would bring it back.
func TestRecordNodeCapacity_RetriesWhenMemoryNotReported(t *testing.T) {
	p := discoveryProvider()

	err := p.RecordNodeCapacity(context.Background(),
		discoveryNode(testInstanceTypeName, ""), discoveryNodeClaim("ocid1.image.oc1..a"))

	assert.ErrorIs(t, err, ErrCapacityNotReported)
	_, ok := p.measured.Get(cacheKey(testInstanceTypeName, testImageID))
	assert.False(t, ok, "nothing should be recorded from a node that reported no memory")
}

// With the image in the key, a node that booted an image the NodeClass has since stopped selecting
// files its measurement under that old image. Nothing looks there, so it neither leaks into the
// current image's estimate nor needs a staleness guard to suppress it.
func TestRecordNodeCapacity_OldImageDoesNotLeak(t *testing.T) {
	p := discoveryProvider()
	ctx := context.Background()

	// A node launched earlier, from an image this NodeClass no longer selects.
	assert.NoError(t, p.RecordNodeCapacity(ctx,
		discoveryNode(testInstanceTypeName, "20000Mi"),
		discoveryNodeClaim("ocid1.image.oc1..superseded")))

	// The shape now resolves to testImageID, so the superseded measurement must not be served.
	_, ok := p.AdviseForShape(ctx, testShape, discoveryNodeClass(testImageID)).
		MemoryFor(testInstanceTypeName)

	assert.False(t, ok, "a measurement from a superseded image must not be served for the current one")

	// It is still filed under its own image, which is what makes the guard unnecessary.
	_, filed := p.measured.Get(cacheKey(testInstanceTypeName, "ocid1.image.oc1..superseded"))
	assert.True(t, filed)
}

// Enabled is what decides whether the controller is registered at all, so tie it to a
// provider built the way production builds one: from a TTL.
func TestEnabled(t *testing.T) {
	tests := []struct {
		name          string
		ttl           time.Duration
		imageProvider image.Provider
		want          bool
	}{
		{"default TTL", DefaultNodeCapacityTTL, &fakeImageProvider{imageID: testImageID}, true},
		{"zero TTL disables", 0, &fakeImageProvider{imageID: testImageID}, false},
		{"no image provider", DefaultNodeCapacityTTL, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &DefaultProvider{
				measured:      newNodeCapacity(tt.ttl),
				imageProvider: tt.imageProvider,
			}

			assert.Equal(t, tt.want, p.Enabled())
		})
	}
}

// Advising and recording are two narrow interfaces rather than one concrete provider, so a
// consumer of either is not coupled to the other. These fail at compile time if one value stops
// serving both, which is what lets the operator construct it once and share it.
var (
	_ Advisor  = (*DefaultProvider)(nil)
	_ Recorder = (*DefaultProvider)(nil)
	_ Provider = (*DefaultProvider)(nil)
	_ Advice   = shapeAdvice{}
	_ Advice   = noAdvice{}
)

// Failed resolutions are not cached by the image provider, so without suppression a broken lookup
// is reissued for every shape on every listing, each with its own retries, while the instance type
// provider holds its read lock.
func TestResolveImage_SuppressesRepeatedFailures(t *testing.T) {
	fake := &fakeImageProvider{err: assert.AnError}
	p := &DefaultProvider{
		measured:      newNodeCapacity(DefaultNodeCapacityTTL),
		imageProvider: fake,
		failures:      newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL),
	}
	nc := discoveryNodeClass(testImageID)

	for i := 0; i < 5; i++ {
		assert.Equal(t, "", p.resolvedImage(context.Background(), testShape, nc))
	}

	assert.Len(t, fake.gotShapes, 1,
		"after the first failure the shape must be suppressed, not retried on every call")
}

// Suppression is per shape, so one shape with no compatible image must not disable discovery for
// the rest of the listing.
func TestResolveImage_SuppressionIsPerShape(t *testing.T) {
	fake := &fakeImageProvider{err: assert.AnError}
	p := &DefaultProvider{
		measured:      newNodeCapacity(DefaultNodeCapacityTTL),
		imageProvider: fake,
		failures:      newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL),
	}
	nc := discoveryNodeClass(testImageID)

	p.resolvedImage(context.Background(), "VM.Standard.E5.Flex", nc)
	p.resolvedImage(context.Background(), "VM.Standard.E5.Flex", nc)
	p.resolvedImage(context.Background(), "VM.Standard.A1.Flex", nc)

	assert.Equal(t, []string{"VM.Standard.E5.Flex", "VM.Standard.A1.Flex"}, fake.gotShapes,
		"the second shape must still be attempted despite the first having failed")
}

// A hung image API must degrade the model, not hold up scheduling: resolution is bounded from
// inside, so it returns no key even when the caller imposes no deadline of its own.
func TestResolveImage_TimesOut(t *testing.T) {
	fake := &fakeImageProvider{imageID: testImageID, block: make(chan struct{})}
	defer close(fake.block)

	p := &DefaultProvider{
		measured:          newNodeCapacity(DefaultNodeCapacityTTL),
		imageProvider:     fake,
		failures:          newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL),
		resolutionTimeout: 50 * time.Millisecond,
	}

	// context.Background() deliberately: the bound must come from the provider, not the caller.
	// The watchdog matters - without the production timeout this call never returns, and a bare
	// assertion would hang until Go's ten-minute test limit rather than failing.
	done := make(chan string, 1)
	go func() {
		done <- p.resolvedImage(context.Background(), testShape, discoveryNodeClass(testImageID))
	}()

	select {
	case got := <-done:
		assert.Equal(t, "", got, "a resolution that does not complete must yield no key")
	case <-time.After(2 * time.Second):
		t.Fatal("resolveImageForDiscovery did not return: the provider is not bounding the attempt")
	}
}

func TestResolveImage_TimeoutFallsBackWhenUnset(t *testing.T) {
	p := &DefaultProvider{
		measured:      newNodeCapacity(DefaultNodeCapacityTTL),
		imageProvider: &fakeImageProvider{imageID: testImageID},
		failures:      newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL),
		// imageResolutionTimeout deliberately zero, as a zero-valued provider would have it.
	}

	assert.Equal(t, testImageID,
		p.resolvedImage(context.Background(), testShape, discoveryNodeClass(testImageID)),
		"an unset timeout must fall back to the default rather than expiring immediately")
}

// Several shapes with no compatible image, scattered among healthy ones, is the normal case on a
// mixed cluster - a GPU image that suits few shapes, say. Those must be suppressed individually
// without tripping the global breaker, which exists for a broken API rather than for shapes that
// legitimately have no match.
//
// The failing shapes must be distinct: per-shape suppression means one bad shape fails only once,
// so repeating it would never exercise the consecutive-failure count at all.
func TestResolveImage_ScatteredBadShapesDoNotSuppressTheRest(t *testing.T) {
	fake := &fakeImageProvider{
		imageID: testImageID,
		failShapes: map[string]bool{
			"VM.Unresolvable.A": true, "VM.Unresolvable.B": true,
			"VM.Unresolvable.C": true, "VM.Unresolvable.D": true,
		},
	}
	p := &DefaultProvider{
		measured:      newNodeCapacity(DefaultNodeCapacityTTL),
		imageProvider: fake,
		failures:      newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL),
	}
	nc := discoveryNodeClass(testImageID)
	ctx := context.Background()

	// Interleaved as a listing would walk them. More failures than the breaker's limit, but each
	// is followed by a success, so the run never reaches it.
	for _, bad := range []string{"VM.Unresolvable.A", "VM.Unresolvable.B", "VM.Unresolvable.C", "VM.Unresolvable.D"} {
		assert.Equal(t, "", p.resolvedImage(ctx, bad, nc))
		assert.Equal(t, testImageID, p.resolvedImage(ctx, "VM.Standard.E5.Flex", nc),
			"a healthy shape must keep resolving despite unresolvable ones beside it")
	}

	assert.Equal(t, testImageID, p.resolvedImage(ctx, "VM.Standard.A1.Flex", nc),
		"a shape not yet seen must not be suppressed either")
}

// Resolution depends on the shape and the NodeClass's image configuration together. A NodeClass
// whose images do not cover a shape must not suppress a different NodeClass whose images do - that
// one would silently fall back to the static estimate despite having a usable measurement cached.
func TestResolveImage_FailureDoesNotLeakAcrossNodeClasses(t *testing.T) {
	const shape = "VM.Standard.E5.Flex"

	armOnly := nodeClassWithImage("ocid1.image.oc1..arm")
	x86 := nodeClassWithImage("ocid1.image.oc1..x86")

	// The ARM NodeClass cannot resolve this x86 shape; the x86 one can.
	fake := &fakeImageProvider{imageID: testImageID, failConfigs: map[string]bool{"ocid1.image.oc1..arm": true}}
	p := &DefaultProvider{
		measured:      newNodeCapacity(DefaultNodeCapacityTTL),
		imageProvider: fake,
		failures:      newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL),
	}
	ctx := context.Background()

	assert.Equal(t, "", p.resolvedImage(ctx, shape, armOnly),
		"the ARM NodeClass has no image for this shape")

	assert.Equal(t, testImageID, p.resolvedImage(ctx, shape, x86),
		"a different NodeClass whose images do cover this shape must still resolve")
}

// Two NodeClasses sharing an image policy resolve identically, so they should share one entry
// rather than each paying their own attempt.
func TestResolveImage_SameImageConfigSharesSuppression(t *testing.T) {
	const shape = "VM.Standard.E5.Flex"

	a := nodeClassWithImage("ocid1.image.oc1..same")
	b := nodeClassWithImage("ocid1.image.oc1..same")

	fake := &fakeImageProvider{err: assert.AnError}
	p := &DefaultProvider{
		measured:      newNodeCapacity(DefaultNodeCapacityTTL),
		imageProvider: fake,
		failures:      newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL),
	}
	ctx := context.Background()

	p.resolvedImage(ctx, shape, a)
	p.resolvedImage(ctx, shape, b)

	assert.Len(t, fake.gotShapes, 1,
		"the same image policy must not be retried once per NodeClass that uses it")
}

// A shape with no compatible image is an answer, not an outage. Counting it towards wholesale
// suppression would let one NodeClass with a narrow filter - which reports "incompatible" for most
// of the catalogue - switch discovery off for every NodeClass, including those whose images
// resolve fine and whose measurements are already cached.
func TestResolveImage_IncompatibleShapesDoNotSuppressTheRest(t *testing.T) {
	narrow := []string{"BM.GPU.H100.8", "BM.GPU.A100-v2.8", "VM.GPU.A10.1"}

	fake := &fakeImageProvider{
		imageID:            testImageID,
		incompatibleShapes: map[string]bool{narrow[0]: true, narrow[1]: true, narrow[2]: true},
	}
	p := &DefaultProvider{
		measured:      newNodeCapacity(DefaultNodeCapacityTTL),
		imageProvider: fake,
		failures:      newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL),
	}
	ctx := context.Background()
	nodeClass := discoveryNodeClass(testImageID)

	// More consecutive incompatible answers than the service-failure limit tolerates.
	for _, shape := range narrow {
		assert.Equal(t, "", p.resolvedImage(ctx, shape, nodeClass), shape)
	}

	assert.Equal(t, testImageID, p.resolvedImage(ctx, "VM.Standard.E5.Flex", nodeClass),
		"a shape whose image resolves must still be discovered after a run of incompatible ones")
}

// The converse: consecutive failures of the image service itself must still suppress wholesale,
// which is what bounds a listing when the API is down.
func TestResolveImage_ServiceFailuresStillSuppressGlobally(t *testing.T) {
	broken := []string{"VM.Standard.E4.Flex", "VM.Standard.E5.Flex", "VM.Standard3.Flex"}

	fake := &fakeImageProvider{
		imageID:    testImageID,
		failShapes: map[string]bool{broken[0]: true, broken[1]: true, broken[2]: true},
	}
	p := &DefaultProvider{
		measured:      newNodeCapacity(DefaultNodeCapacityTTL),
		imageProvider: fake,
		failures:      newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL),
	}
	ctx := context.Background()
	nodeClass := discoveryNodeClass(testImageID)

	for _, shape := range broken {
		assert.Equal(t, "", p.resolvedImage(ctx, shape, nodeClass), shape)
	}

	before := len(fake.gotShapes)
	assert.Equal(t, "", p.resolvedImage(ctx, "VM.Standard.A1.Flex", nodeClass),
		"an unwell image service suppresses every shape")
	assert.Len(t, fake.gotShapes, before, "and is not asked again while suppressed")
}

// A configuration that cannot yield an image at all - contradictory, or matching nothing - is as
// settled an answer as an incompatible shape, and must not be mistaken for an outage either.
func TestResolveImage_ConfigurationErrorsAreNotOutages(t *testing.T) {
	configErrs := []error{
		fmt.Errorf("%w: no image match", image.ErrImageConfiguration),
		fmt.Errorf("%w: cannot define image ocid and image filter together", image.ErrImageConfiguration),
		fmt.Errorf("%w: VM.GPU.A10.1", image.ErrNoCompatibleImage),
	}
	shapes := []string{"VM.Standard.E4.Flex", "VM.Standard3.Flex", "VM.GPU.A10.1"}

	fake := &fakeImageProvider{imageID: testImageID}
	p := &DefaultProvider{
		measured:      newNodeCapacity(DefaultNodeCapacityTTL),
		imageProvider: fake,
		failures:      newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL),
	}
	ctx := context.Background()
	nodeClass := discoveryNodeClass(testImageID)

	for i, shape := range shapes {
		fake.configErr = configErrs[i]
		assert.Equal(t, "", p.resolvedImage(ctx, shape, nodeClass), shape)
	}

	fake.configErr = nil
	assert.Equal(t, testImageID, p.resolvedImage(ctx, "VM.Standard.E5.Flex", nodeClass),
		"configuration answers must not trip the breaker that guards against an unwell service")
}

// A success carrying nothing usable is the image provider breaking its contract, not an answer
// about this shape, so it keeps the bounding that a service failure gets.
func TestResolveImage_UnusableResultCountsAsFailure(t *testing.T) {
	shapes := []string{"VM.Standard.E4.Flex", "VM.Standard3.Flex", "VM.Standard.E5.Flex"}

	for name, broken := range map[string]*fakeImageProvider{
		"no images":    {imageID: testImageID, emptyResult: true},
		"nil result":   {imageID: testImageID, nilResult: true},
		"nil image id": {imageID: testImageID, nilImageID: true},
	} {
		t.Run(name, func(t *testing.T) {
			p := &DefaultProvider{
				measured:      newNodeCapacity(DefaultNodeCapacityTTL),
				imageProvider: broken,
				failures:      newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL),
			}
			ctx := context.Background()
			nodeClass := discoveryNodeClass(testImageID)

			for _, shape := range shapes {
				assert.Equal(t, "", p.resolvedImage(ctx, shape, nodeClass), shape)
			}

			before := len(broken.gotShapes)
			assert.Equal(t, "", p.resolvedImage(ctx, "VM.Standard.A1.Flex", nodeClass))
			assert.Len(t, broken.gotShapes, before,
				"an image provider returning nothing usable is bounded too")
		})
	}
}

// The cost that matters is across a whole listing, not one call: shapes are walked serially, so
// per-key suppression alone would still pay one attempt per shape before anything was suppressed.
// Ask about many shapes with a failing resolver, as a listing would.
func TestAdviseForShape_BoundsAttemptsAcrossAListing(t *testing.T) {
	fake := &fakeImageProvider{err: assert.AnError}
	p := &DefaultProvider{
		measured:          newNodeCapacity(DefaultNodeCapacityTTL),
		imageProvider:     fake,
		failures:          newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL),
		resolutionTimeout: 50 * time.Millisecond,
	}
	nodeClass := discoveryNodeClass(testImageID)

	for _, shape := range []string{
		"VM.Standard2.1", "VM.Standard2.2", "VM.Standard2.4",
		"VM.Standard2.8", "VM.Standard2.16", "VM.Standard3.1",
	} {
		advice := p.AdviseForShape(context.Background(), shape, nodeClass)

		_, ok := advice.MemoryFor(testInstanceTypeName)
		assert.False(t, ok, "a failing resolver leaves the caller with its own estimate")
	}

	// Without the consecutive-failure breaker this would be one attempt per shape; the point is
	// that a broken image API costs a bounded amount regardless of how many shapes exist.
	assert.LessOrEqual(t, len(fake.gotShapes), 3,
		"a failing image API must stop being asked, not be retried once per shape in the listing")
	assert.NotEmpty(t, fake.gotShapes, "it must still try before giving up")
}

// And the instance type provider must ask once per shape, not once per generated instance type: a
// flexible shape expands into many configurations that all share an image.
func TestAdviseForShape_AdviceIsReusableAcrossInstanceTypes(t *testing.T) {
	p := discoveryProvider()
	ctx := context.Background()
	fake := p.imageProvider.(*fakeImageProvider)

	p.measured.Record(ctx, cacheKey("VM.Standard.E5.Flex.8o.32g.1_1b", testImageID),
		resource.MustParse("30890Mi"))
	p.measured.Record(ctx, cacheKey("VM.Standard.E5.Flex.4o.16g.1_1b", testImageID),
		resource.MustParse("15000Mi"))

	advice := p.AdviseForShape(ctx, testShape, discoveryNodeClass(testImageID))

	big, ok := advice.MemoryFor("VM.Standard.E5.Flex.8o.32g.1_1b")
	require.True(t, ok)
	small, ok := advice.MemoryFor("VM.Standard.E5.Flex.4o.16g.1_1b")
	require.True(t, ok)

	assert.NotEqual(t, big.Value(), small.Value(), "each configuration keeps its own measurement")
	assert.Len(t, fake.gotShapes, 1, "one resolution serves every configuration of the shape")
}

// Every reason for having no advice reports a distinct outcome, because the whole point of the
// metric is that an operator can tell "nothing measured yet" from "the image API is failing"
// without either being logged as an error.
func TestResolveImage_ReportsDistinctOutcomes(t *testing.T) {
	const shape = "VM.Standard.E5.Flex"
	nodeClass := discoveryNodeClass(testImageID)

	tests := []struct {
		name      string
		provider  func() *DefaultProvider
		nodeClass *ociv1beta1.OCINodeClass
		want      string
	}{
		{
			name:      "discovery switched off",
			provider:  func() *DefaultProvider { return New(&fakeImageProvider{imageID: testImageID}, 0) },
			nodeClass: nodeClass,
			want:      metrics.OutcomeDisabled,
		},
		{
			name:      "nothing to resolve from",
			provider:  func() *DefaultProvider { return New(&fakeImageProvider{imageID: testImageID}, DefaultNodeCapacityTTL) },
			nodeClass: &ociv1beta1.OCINodeClass{},
			want:      metrics.OutcomeNoImage,
		},
		{
			name: "no image covers this shape",
			provider: func() *DefaultProvider {
				return New(&fakeImageProvider{incompatibleShapes: map[string]bool{shape: true}},
					DefaultNodeCapacityTTL)
			},
			nodeClass: nodeClass,
			want:      metrics.OutcomeNoImage,
		},
		{
			name: "the image service failed",
			provider: func() *DefaultProvider {
				return New(&fakeImageProvider{err: assert.AnError}, DefaultNodeCapacityTTL)
			},
			nodeClass: nodeClass,
			want:      metrics.OutcomeResolutionFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imageID, outcome := tt.provider().resolveImage(context.Background(), shape, tt.nodeClass)

			assert.Equal(t, "", imageID)
			assert.Equal(t, tt.want, outcome)
		})
	}

	// A second attempt is suppressed rather than repeating whatever happened the first time, which
	// is what keeps both the API calls and the log lines to one per key per TTL.
	p := New(&fakeImageProvider{err: assert.AnError}, DefaultNodeCapacityTTL)
	_, first := p.resolveImage(context.Background(), shape, nodeClass)
	_, second := p.resolveImage(context.Background(), shape, nodeClass)

	assert.Equal(t, metrics.OutcomeResolutionFailed, first)
	assert.Equal(t, metrics.OutcomeSuppressed, second)
}

// And a success reports nothing, because the outcome is then whether a measurement exists, which
// only the lookup can say.
func TestResolveImage_SuccessHasNoOutcome(t *testing.T) {
	p := discoveryProvider()

	imageID, outcome := p.resolveImage(context.Background(), testShape, discoveryNodeClass(testImageID))

	assert.Equal(t, testImageID, imageID)
	assert.Equal(t, "", outcome)
}

// A success that carries nothing an instance type could be keyed on is the image provider breaking
// its contract. Each shape of that is a failure with an error to report, not a silent miss and not
// a panic.
func TestResolveImage_UnusableResultsAreContractViolations(t *testing.T) {
	tests := map[string]*fakeImageProvider{
		"nil result":      {nilResult: true},
		"no images":       {emptyResult: true},
		"nil image entry": {nilImageEntry: true},
		"nil image id":    {nilImageID: true},
		"empty image id":  {imageID: ""},
	}

	for name, fake := range tests {
		t.Run(name, func(t *testing.T) {
			p := &DefaultProvider{
				measured:      newNodeCapacity(DefaultNodeCapacityTTL),
				imageProvider: fake,
				failures:      newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL),
			}

			imageID, outcome := p.resolveImage(context.Background(), testShape,
				discoveryNodeClass(testImageID))

			assert.Equal(t, "", imageID)
			assert.Equal(t, metrics.OutcomeResolutionFailed, outcome,
				"an unusable result is a failure of the image provider, not a settled answer")
		})
	}
}

// A settled answer is suppressed for much longer than a recovering one. Re-asking would also mean
// re-logging, once per shape per listing, for a NodeClass whose images cover little of the
// catalogue.
func TestImageResolutionFailures_IncompatibilityOutlastsFailure(t *testing.T) {
	f := newImageResolutionFailures(20*time.Millisecond, time.Hour)

	f.RecordFailure("failing")
	f.RecordIncompatible("incompatible")

	time.Sleep(40 * time.Millisecond)

	assert.False(t, f.RecentlyFailed("failing"), "a service failure must be retried soon")
	assert.True(t, f.RecentlyFailed("incompatible"), "a settled answer must not be re-asked soon")
}

// adviceCount reads the counter for one outcome, so tests can assert what was actually emitted
// rather than what the code said it would emit.
func adviceCount(t *testing.T, outcome string) float64 {
	t.Helper()

	gathered, err := crmetrics.Registry.Gather()
	require.NoError(t, err)

	for _, family := range gathered {
		if family.GetName() != "karpenter_cloudprovider_capacity_discovery_advice_count" {
			continue
		}
		for _, m := range family.GetMetric() {
			for _, label := range m.GetLabel() {
				if label.GetName() == metrics.OutcomeLabel && label.GetValue() == outcome {
					return m.GetCounter().GetValue()
				}
			}
		}
	}

	return 0
}

// Every instance type modelled contributes exactly one sample, whatever the outcome. Counting
// resolution outcomes per shape and lookups per instance type would make the denominator depend on
// how far a flexible shape expands, so the ratios would not mean anything.
func TestAdviseForShape_CountsOneSamplePerInstanceType(t *testing.T) {
	ctx := context.Background()

	t.Run("applied and no_measurement", func(t *testing.T) {
		p := discoveryProvider()
		p.measured.Record(ctx, cacheKey(testInstanceTypeName, testImageID), resource.MustParse("30890Mi"))

		appliedBefore := adviceCount(t, metrics.OutcomeApplied)
		missBefore := adviceCount(t, metrics.OutcomeNoMeasurement)

		advice := p.AdviseForShape(ctx, testShape, discoveryNodeClass(testImageID))
		advice.MemoryFor(testInstanceTypeName)
		advice.MemoryFor("VM.Standard.E5.Flex.4o.16g.1_1b")

		assert.Equal(t, appliedBefore+1, adviceCount(t, metrics.OutcomeApplied))
		assert.Equal(t, missBefore+1, adviceCount(t, metrics.OutcomeNoMeasurement))
	})

	t.Run("a resolution failure counts per instance type too", func(t *testing.T) {
		p := New(&fakeImageProvider{err: assert.AnError}, DefaultNodeCapacityTTL)

		before := adviceCount(t, metrics.OutcomeResolutionFailed)

		advice := p.AdviseForShape(ctx, testShape, discoveryNodeClass(testImageID))
		advice.MemoryFor(testInstanceTypeName)
		advice.MemoryFor("VM.Standard.E5.Flex.4o.16g.1_1b")

		assert.Equal(t, before+2, adviceCount(t, metrics.OutcomeResolutionFailed),
			"two instance types modelled, two samples - not one per shape")
	})

	t.Run("no advisor configured reports disabled", func(t *testing.T) {
		before := adviceCount(t, metrics.OutcomeDisabled)

		NoAdvice().MemoryFor(testInstanceTypeName)

		assert.Equal(t, before+1, adviceCount(t, metrics.OutcomeDisabled))
	})
}

// The failure branch logs the error it was given, so a successful call carrying nothing usable has
// to be given one: logging a nil error would report a failure with nothing to diagnose it by.
// usableResult is what decides that, so pin every field it looks at.
func TestUsableResult(t *testing.T) {
	tests := map[string]struct {
		resolved *image.ImageResolveResult
		want     bool
	}{
		"nil result":      {nil, false},
		"no images":       {&image.ImageResolveResult{}, false},
		"nil image entry": {&image.ImageResolveResult{Images: []*ocicore.Image{nil}}, false},
		"nil image id":    {&image.ImageResolveResult{Images: []*ocicore.Image{{}}}, false},
		"empty image id":  {&image.ImageResolveResult{Images: []*ocicore.Image{{Id: lo.ToPtr("")}}}, false},
		"usable":          {&image.ImageResolveResult{Images: []*ocicore.Image{{Id: lo.ToPtr(testImageID)}}}, true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, usableResult(tt.resolved))
		})
	}
}

// And the error it is reported as names the contract that was broken, rather than being an
// anonymous string that reads like an OCI failure.
func TestUnusableResultIsReportedAsAnError(t *testing.T) {
	p := &DefaultProvider{
		measured:      newNodeCapacity(DefaultNodeCapacityTTL),
		imageProvider: &fakeImageProvider{emptyResult: true},
		failures:      newImageResolutionFailures(imageResolutionFailureTTL, imageIncompatibleTTL),
	}

	_, outcome := p.resolveImage(context.Background(), testShape, discoveryNodeClass(testImageID))

	require.Equal(t, metrics.OutcomeResolutionFailed, outcome)
	assert.False(t, image.IsConfigurationError(errUnusableResult),
		"a broken contract must not be classified as a settled configuration answer")
}
