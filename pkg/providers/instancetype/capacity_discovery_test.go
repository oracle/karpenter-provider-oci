/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package instancetype

import (
	"context"
	"fmt"
	"testing"
	"time"

	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/cache"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/image"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

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
	// failConfigs fails for NodeClasses whose configured imageId is listed, modelling an image
	// policy that does not cover the shape being asked about.
	failConfigs map[string]bool
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
		return nil, assert.AnError
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
		discoveredCapacity: cache.NewDiscoveredCapacity(cache.DiscoveredCapacityTTL),
		imageProvider:      &fakeImageProvider{imageID: testImageID},
	}
}

// A node reporting less memory than was modelled is the whole point: record it so the next launch
// of that combination is sized from measurement rather than the estimate.
func TestUpdateInstanceTypeCapacityFromNode_RecordsObservedMemory(t *testing.T) {
	p := discoveryProvider()

	err := p.UpdateInstanceTypeCapacityFromNode(context.Background(),
		discoveryNode(testInstanceTypeName, "30890Mi"), discoveryNodeClaim("ocid1.image.oc1..a"))
	assert.NoError(t, err)

	got, ok := p.discoveredCapacity.Get(discoveredCapacityCacheKey(testInstanceTypeName, testImageID))
	assert.True(t, ok, "expected the observation to be recorded")
	want := resource.MustParse("30890Mi")
	assert.Equal(t, want.Value(), got.Value())
}

// Nodes of nominally the same kind can report slightly different totals. Keeping the smallest
// keeps the model on the safe side: over-estimating drives the launch loop this exists to stop,
// while under-estimating only wastes memory.
func TestUpdateInstanceTypeCapacityFromNode_KeepsSmallestObserved(t *testing.T) {
	p := discoveryProvider()
	ctx := context.Background()
	key := discoveredCapacityCacheKey(testInstanceTypeName, testImageID)

	for _, mem := range []string{"30890Mi", "30800Mi", "31000Mi"} {
		assert.NoError(t, p.UpdateInstanceTypeCapacityFromNode(ctx,
			discoveryNode(testInstanceTypeName, mem), discoveryNodeClaim("ocid1.image.oc1..a")))
	}

	got, ok := p.discoveredCapacity.Get(key)
	assert.True(t, ok)
	want := resource.MustParse("30800Mi")
	assert.Equal(t, want.Value(), got.Value(),
		"a larger later observation must not raise the recorded capacity")
}

func TestUpdateInstanceTypeCapacityFromNode_Skips(t *testing.T) {
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

			assert.NoError(t, p.UpdateInstanceTypeCapacityFromNode(
				context.Background(), tt.node, tt.nodeClaim))

			_, ok := p.discoveredCapacity.Get(discoveredCapacityCacheKey(testInstanceTypeName, testImageID))
			assert.False(t, ok, "must not record: %s", tt.reason)
		})
	}
}

func TestUpdateInstanceTypeCapacityFromNode_NilInputs(t *testing.T) {
	p := discoveryProvider()
	ctx := context.Background()

	assert.NoError(t, p.UpdateInstanceTypeCapacityFromNode(ctx, nil, discoveryNodeClaim("a")))
	assert.NoError(t, p.UpdateInstanceTypeCapacityFromNode(ctx, discoveryNode(testInstanceTypeName, "1Gi"), nil))

	_, ok := p.discoveredCapacity.Get(discoveredCapacityCacheKey(testInstanceTypeName, "a"))
	assert.False(t, ok, "a nil node or nodeclaim must record nothing")
}

// The key names the image a node actually booted, so a measurement taken under one image is never
// served for another.
func TestDiscoveredCapacityCacheKey(t *testing.T) {
	const a, b = "ocid1.image.oc1..a", "ocid1.image.oc1..b"

	assert.Equal(t, discoveredCapacityCacheKey(testInstanceTypeName, a),
		discoveredCapacityCacheKey(testInstanceTypeName, a), "key must be stable")
	assert.NotEqual(t, discoveredCapacityCacheKey(testInstanceTypeName, a),
		discoveredCapacityCacheKey(testInstanceTypeName, b),
		"a different image must not reuse the entry")
	assert.NotEqual(t, discoveredCapacityCacheKey(testInstanceTypeName, a),
		discoveredCapacityCacheKey("VM.Standard.E5.Flex.4o.16g.1_1b", a),
		"a different instance type must not reuse the entry")
}

// Naming the resolved image rather than the NodeClass's whole candidate list is the point of the
// key: OKE publishes images regularly, and a key derived from the list would discard every learned
// value each time one appeared, including for shapes whose selection did not change.
func TestDiscoveredCapacity_UnrelatedImageDoesNotInvalidate(t *testing.T) {
	p := discoveryProvider()
	nc := discoveryNodeClass(testImageID)
	ctx := context.Background()

	assert.NoError(t, p.UpdateInstanceTypeCapacityFromNode(ctx,
		discoveryNode(testInstanceTypeName, "30890Mi"), discoveryNodeClaim(testImageID)))

	// A new image is published and joins the candidate list, but this shape still selects the same
	// one, so the measurement must still be found.
	nc.Status.Volume.ImageCandidates = append(nc.Status.Volume.ImageCandidates,
		&ociv1beta1.Image{ImageId: "ocid1.image.oc1..newly-published"})

	it := &OciInstanceType{}
	it.Name = testInstanceTypeName
	it.Shape = testShape
	it.Capacity = v1.ResourceList{v1.ResourceMemory: resource.MustParse("32Gi")}

	p.applyDiscoveredCapacity(it, testImageID)

	want := resource.MustParse("30890Mi")
	assert.Equal(t, want.Value(), it.Capacity.Memory().Value(),
		"an unrelated image joining the candidate list must not discard what we learned")
}

// Scheduling must not depend on the image API being reachable: if resolution fails there is no key
// to look under, and the modelled estimate - which is deliberately conservative - stands.
func TestResolveImageForDiscovery_FailureYieldsNoKey(t *testing.T) {
	p := discoveryProvider()
	p.imageProvider = &fakeImageProvider{err: assert.AnError}

	assert.Equal(t, "", p.resolveImageForDiscovery(context.Background(), testShape, discoveryNodeClass(testImageID)))
}

// Turning the feature off must skip the resolution too, not just the lookup: resolving is the part
// that can reach OCI, so paying for it while discarding the result would be the worst of both.
func TestResolveImageForDiscovery_DisabledDoesNoWork(t *testing.T) {
	fake := &fakeImageProvider{imageID: testImageID}
	p := &DefaultProvider{
		discoveredCapacity: cache.NewDiscoveredCapacity(0), // 0 == disabled
		imageProvider:      fake,
	}

	assert.Equal(t, "", p.resolveImageForDiscovery(context.Background(), testShape, discoveryNodeClass(testImageID)))
	assert.Empty(t, fake.gotShapes, "a disabled cache must not trigger image resolution at all")
}

// The resolver must be asked about the shape, not the synthetic instance type name: flexible
// instance types are named Shape.<X>o.<Y>g.<Z>b, which no image is compatible with.
func TestResolveImageForDiscovery_AsksForTheShape(t *testing.T) {
	fake := &fakeImageProvider{imageID: testImageID}
	p := &DefaultProvider{
		discoveredCapacity: cache.NewDiscoveredCapacity(cache.DiscoveredCapacityTTL),
		imageProvider:      fake,
	}

	p.resolveImageForDiscovery(context.Background(), testShape, discoveryNodeClass(testImageID))

	assert.Equal(t, []string{testShape}, fake.gotShapes)
}

func TestApplyDiscoveredCapacity(t *testing.T) {
	modelled := resource.MustParse("32Gi")

	t.Run("overrides the modelled value once measured", func(t *testing.T) {
		p := discoveryProvider()
		p.discoveredCapacity.Record(context.Background(),
			discoveredCapacityCacheKey(testInstanceTypeName, testImageID), resource.MustParse("30890Mi"))

		it := &OciInstanceType{}
		it.Name = testInstanceTypeName
		it.Shape = testShape
		it.Capacity = v1.ResourceList{v1.ResourceMemory: modelled}

		p.applyDiscoveredCapacity(it, testImageID)

		want := resource.MustParse("30890Mi")
		assert.Equal(t, want.Value(), it.Capacity.Memory().Value())
	})

	t.Run("leaves the estimate alone before anything is measured", func(t *testing.T) {
		p := discoveryProvider()

		it := &OciInstanceType{}
		it.Name = testInstanceTypeName
		it.Shape = testShape
		it.Capacity = v1.ResourceList{v1.ResourceMemory: modelled}

		p.applyDiscoveredCapacity(it, testImageID)

		assert.Equal(t, modelled.Value(), it.Capacity.Memory().Value(),
			"the first launch of a combination has nothing to learn from")
	})
}

// A disabled cache must leave modelling exactly as it was, so the feature can be turned off.
func TestDiscoveredCapacityDisabled(t *testing.T) {
	p := &DefaultProvider{discoveredCapacity: cache.NewDiscoveredCapacity(0)}

	assert.NoError(t, p.UpdateInstanceTypeCapacityFromNode(context.Background(),
		discoveryNode(testInstanceTypeName, "30890Mi"), discoveryNodeClaim("ocid1.image.oc1..a")))

	it := &OciInstanceType{}
	it.Name = testInstanceTypeName
	it.Shape = testShape
	it.Capacity = v1.ResourceList{v1.ResourceMemory: resource.MustParse("32Gi")}
	p.applyDiscoveredCapacity(it, testImageID)

	want := resource.MustParse("32Gi")
	assert.Equal(t, want.Value(), it.Capacity.Memory().Value())
}

// A registered node that has not published memory yet must be retried, not dropped: the controller
// only watches the transition into the registered state, so nothing else would bring it back.
func TestUpdateInstanceTypeCapacityFromNode_RetriesWhenMemoryNotReported(t *testing.T) {
	p := discoveryProvider()

	err := p.UpdateInstanceTypeCapacityFromNode(context.Background(),
		discoveryNode(testInstanceTypeName, ""), discoveryNodeClaim("ocid1.image.oc1..a"))

	assert.ErrorIs(t, err, ErrCapacityNotReported)
	_, ok := p.discoveredCapacity.Get(discoveredCapacityCacheKey(testInstanceTypeName, testImageID))
	assert.False(t, ok, "nothing should be recorded from a node that reported no memory")
}

// With the image in the key, a node that booted an image the NodeClass has since stopped selecting
// files its measurement under that old image. Nothing looks there, so it neither leaks into the
// current image's estimate nor needs a staleness guard to suppress it.
func TestUpdateInstanceTypeCapacityFromNode_OldImageDoesNotLeak(t *testing.T) {
	p := discoveryProvider()
	ctx := context.Background()

	// A node launched earlier, from an image this NodeClass no longer selects.
	assert.NoError(t, p.UpdateInstanceTypeCapacityFromNode(ctx,
		discoveryNode(testInstanceTypeName, "20000Mi"),
		discoveryNodeClaim("ocid1.image.oc1..superseded")))

	it := &OciInstanceType{}
	it.Name = testInstanceTypeName
	it.Shape = testShape
	it.Capacity = v1.ResourceList{v1.ResourceMemory: resource.MustParse("32Gi")}

	// The shape now resolves to testImageID, so the superseded measurement must not be used.
	p.applyDiscoveredCapacity(it, testImageID)

	want := resource.MustParse("32Gi")
	assert.Equal(t, want.Value(), it.Capacity.Memory().Value(),
		"a measurement from a superseded image must not be served for the current one")

	// It is still filed under its own image, which is what makes the guard unnecessary.
	_, ok := p.discoveredCapacity.Get(
		discoveredCapacityCacheKey(testInstanceTypeName, "ocid1.image.oc1..superseded"))
	assert.True(t, ok)
}

// The override only matters if decorateInstanceType actually applies it. Testing
// applyDiscoveredCapacity alone would still pass if the call site were deleted, which is the whole
// mechanism, so drive it through decorateInstanceType instead.
func TestDecorateInstanceType_AppliesDiscoveredCapacity(t *testing.T) {
	nodeClass := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			VolumeConfig:  &ociv1beta1.VolumeConfig{BootVolumeConfig: &ociv1beta1.BootVolumeConfig{}},
			NetworkConfig: &ociv1beta1.NetworkConfig{},
		},
		Status: ociv1beta1.OCINodeClassStatus{
			Volume: &ociv1beta1.Volume{
				ImageCandidates: []*ociv1beta1.Image{{ImageId: "ocid1.image.oc1..a"}},
			},
		},
	}

	newProvider := func() *DefaultProvider {
		return &DefaultProvider{
			shapeToPrice: map[string]*ShapePriceInfo{
				"VM.STANDARD.E4.FLEX": {
					ShapeName: lo.ToPtr("VM.Standard.E4.Flex"), OcpuUnitPrice: 0.05,
					MemoryUnitPrice: 0.01, DiskUnitPrice: 0,
				},
			},
			preemptibleShapes:  PreemptibleShapes{"VM.STANDARD.E4": "VM.Standard.E4"},
			discoveredCapacity: cache.NewDiscoveredCapacity(cache.DiscoveredCapacityTTL),
			imageProvider:      &fakeImageProvider{imageID: testImageID},
		}
	}
	shapeAndAd := &ShapeAndAd{
		Shape: &ocicore.Shape{
			Shape: lo.ToPtr("VM.Standard.E4.Flex"), Ocpus: lo.ToPtr(float32(4)),
			MemoryInGBs: lo.ToPtr(float32(32)), BillingType: ocicore.ShapeBillingTypePaid,
		},
		Ads: []string{"tenancy:PHX-AD-1"},
	}
	newInstanceType := func() *OciInstanceType {
		return &OciInstanceType{
			InstanceType: cloudprovider.InstanceType{Name: "VM.Standard.E4.Flex"},
			Shape:        "VM.Standard.E4.Flex",
			Ocpu:         lo.ToPtr(float32(4)),
			MemoryInGbs:  lo.ToPtr(float32(32)),
		}
	}

	// Without a measurement, the modelled figure stands.
	modelled := newProvider()
	it := newInstanceType()
	_ = modelled.decorateInstanceType(context.Background(), it, nodeClass, shapeAndAd, nil, testImageID)
	modelledMemory := it.Capacity.Memory().Value()
	assert.NotZero(t, modelledMemory)

	// Once a node of this kind has been measured, that value must reach the instance type.
	discovered := newProvider()
	measured := resource.MustParse("30890Mi")
	discovered.discoveredCapacity.Record(context.Background(),
		discoveredCapacityCacheKey("VM.Standard.E4.Flex", testImageID), measured)

	it = newInstanceType()
	_ = discovered.decorateInstanceType(context.Background(), it, nodeClass, shapeAndAd, nil, testImageID)

	assert.Equal(t, measured.Value(), it.Capacity.Memory().Value(),
		"decorateInstanceType must prefer the measured capacity over the modelled one")
	assert.NotEqual(t, modelledMemory, it.Capacity.Memory().Value())
}

// DiscoveryEnabled is what decides whether the controller is registered at all, so tie it to a
// provider built the way production builds one: from a TTL.
func TestDiscoveryEnabled(t *testing.T) {
	tests := []struct {
		name          string
		ttl           time.Duration
		imageProvider image.Provider
		want          bool
	}{
		{"default TTL", cache.DiscoveredCapacityTTL, &fakeImageProvider{imageID: testImageID}, true},
		{"zero TTL disables", 0, &fakeImageProvider{imageID: testImageID}, false},
		{"no image provider", cache.DiscoveredCapacityTTL, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &DefaultProvider{
				discoveredCapacity: cache.NewDiscoveredCapacity(tt.ttl),
				imageProvider:      tt.imageProvider,
			}

			assert.Equal(t, tt.want, p.DiscoveryEnabled())
		})
	}
}

// The operator exposes listing and discovery as two narrow interfaces rather than the concrete
// provider, so a consumer that only lists instance types is not coupled to capacity discovery.
// Both are satisfied by the same value; these assertions fail at compile time if that stops being
// true, which is what keeps the operator from having to name *DefaultProvider again.
var (
	_ Provider                  = (*DefaultProvider)(nil)
	_ CapacityDiscoveryProvider = (*DefaultProvider)(nil)
)

func TestDefaultProviderSatisfiesBothInterfaces(t *testing.T) {
	p := discoveryProvider()

	// Listing knows nothing about discovery, and discovery knows nothing about listing.
	var listing Provider = p
	var discovery CapacityDiscoveryProvider = p

	assert.NotNil(t, listing)
	assert.True(t, discovery.DiscoveryEnabled())
}

// The static VM memory overhead (#73) and discovered capacity (#77) both decide what memory an
// instance type reports, so what matters is their precedence and that neither silently defeats the
// other. The earlier tests here predate #73 and ran against a capacity calculation with no
// overhead in it, so they could not observe the interaction at all.
func TestDiscoveredCapacityAndStaticOverhead(t *testing.T) {
	const (
		declaredGiB = 32
		declaredMiB = declaredGiB * 1024               // 32768
		overheadMiB = DefaultVMMemoryOverheadBaseMiB + // 600
			DefaultVMMemoryOverheadPerGBMiB*declaredGiB // + 19*32 = 1208
		estimateMiB = declaredMiB - overheadMiB // 31560
		measuredMiB = 31631                     // real E5 32 GB, from issue #7
	)

	nodeClass := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			VolumeConfig:  &ociv1beta1.VolumeConfig{BootVolumeConfig: &ociv1beta1.BootVolumeConfig{}},
			NetworkConfig: &ociv1beta1.NetworkConfig{},
		},
	}
	shapeAndAd := &ShapeAndAd{
		Shape: &ocicore.Shape{
			Shape: lo.ToPtr("VM.Standard.E5.Flex"), Ocpus: lo.ToPtr(float32(8)),
			MemoryInGBs: lo.ToPtr(float32(declaredGiB)), BillingType: ocicore.ShapeBillingTypePaid,
		},
		Ads: []string{"tenancy:PHX-AD-1"},
	}
	newProvider := func() *DefaultProvider {
		return &DefaultProvider{
			shapeToPrice: map[string]*ShapePriceInfo{
				"VM.STANDARD.E5.FLEX": {
					ShapeName: lo.ToPtr("VM.Standard.E5.Flex"), OcpuUnitPrice: 0.05,
					MemoryUnitPrice: 0.01, DiskUnitPrice: 0,
				},
			},
			preemptibleShapes:  PreemptibleShapes{"VM.STANDARD.E5": "VM.Standard.E5"},
			vmMemoryOverhead:   defaultVMMemoryOverhead,
			discoveredCapacity: cache.NewDiscoveredCapacity(cache.DiscoveredCapacityTTL),
			imageProvider:      &fakeImageProvider{imageID: testImageID},
		}
	}
	newInstanceType := func() *OciInstanceType {
		return &OciInstanceType{
			InstanceType: cloudprovider.InstanceType{Name: "VM.Standard.E5.Flex"},
			Shape:        "VM.Standard.E5.Flex",
			Ocpu:         lo.ToPtr(float32(8)),
			MemoryInGbs:  lo.ToPtr(float32(declaredGiB)),
		}
	}
	memoryMiB := func(it *OciInstanceType) int64 { return it.Capacity.Memory().Value() / 1024 / 1024 }

	t.Run("before any measurement the static estimate governs", func(t *testing.T) {
		p := newProvider()
		it := newInstanceType()

		_ = p.decorateInstanceType(context.Background(), it, nodeClass, shapeAndAd, nil, testImageID)

		assert.Equal(t, int64(estimateMiB), memoryMiB(it),
			"with nothing measured, capacity is declared memory minus the configured overhead")
	})

	t.Run("a measurement overrides the static estimate", func(t *testing.T) {
		p := newProvider()
		p.discoveredCapacity.Record(context.Background(),
			discoveredCapacityCacheKey("VM.Standard.E5.Flex", testImageID),
			resource.MustParse(fmt.Sprintf("%dMi", measuredMiB)))

		it := newInstanceType()
		_ = p.decorateInstanceType(context.Background(), it, nodeClass, shapeAndAd, nil, testImageID)

		assert.Equal(t, int64(measuredMiB), memoryMiB(it),
			"measurement wins over the estimate")
		assert.Greater(t, memoryMiB(it), int64(estimateMiB),
			"this shape really is roomier than the deliberately pessimistic estimate, so the "+
				"override must be able to raise capacity as well as lower it")
	})

	t.Run("the estimate does not clamp a measurement", func(t *testing.T) {
		// A node smaller than the estimate must be believed: the estimate is a guess, the
		// measurement is a fact, and refusing to go below it would reintroduce over-modelling.
		const smallerThanEstimate = estimateMiB - 500

		p := newProvider()
		p.discoveredCapacity.Record(context.Background(),
			discoveredCapacityCacheKey("VM.Standard.E5.Flex", testImageID),
			resource.MustParse(fmt.Sprintf("%dMi", smallerThanEstimate)))

		it := newInstanceType()
		_ = p.decorateInstanceType(context.Background(), it, nodeClass, shapeAndAd, nil, testImageID)

		assert.Equal(t, int64(smallerThanEstimate), memoryMiB(it))
	})

	t.Run("discovery disabled leaves the static estimate intact", func(t *testing.T) {
		p := newProvider()
		p.discoveredCapacity = cache.NewDiscoveredCapacity(0)

		it := newInstanceType()
		// Disabled means no image is resolved, so decoration receives no image id.
		_ = p.decorateInstanceType(context.Background(), it, nodeClass, shapeAndAd, nil, "")

		assert.Equal(t, int64(estimateMiB), memoryMiB(it),
			"turning discovery off must fall back to #73's estimate, not to declared memory")
		assert.Less(t, memoryMiB(it), int64(declaredMiB))
	})

	// The subtests above hand decorateInstanceType an image id directly, which skips the step that
	// produces it. Drive one case through makeInstanceTypes so the resolution handoff is covered:
	// resolveImageForDiscovery returning "" would otherwise silently disable discovery in real
	// listings while every assertion above still passed.
	t.Run("resolution handoff reaches the lookup", func(t *testing.T) {
		fixedShape := &ShapeAndAd{
			Shape: &ocicore.Shape{
				Shape: lo.ToPtr("VM.Standard2.8"), IsFlexible: lo.ToPtr(false),
				Ocpus: lo.ToPtr(float32(8)), MemoryInGBs: lo.ToPtr(float32(declaredGiB)),
				BillingType: ocicore.ShapeBillingTypePaid,
			},
			Ads: []string{"tenancy:PHX-AD-1"},
		}

		p := newProvider()
		p.shapeToPrice["VM.STANDARD2.8"] = &ShapePriceInfo{
			ShapeName: lo.ToPtr("VM.Standard2.8"), OcpuUnitPrice: 0.05, MemoryUnitPrice: 0.01,
		}
		// Recorded under the id the fake image provider resolves to, which only matches if
		// makeInstanceTypes actually asks for it and threads the answer through.
		p.discoveredCapacity.Record(context.Background(),
			discoveredCapacityCacheKey("VM.Standard2.8", testImageID),
			resource.MustParse(fmt.Sprintf("%dMi", measuredMiB)))

		its, err := p.makeInstanceTypes(context.Background(), fixedShape, nodeClass, nil)

		require.NoError(t, err)
		require.Len(t, its, 1)
		assert.Equal(t, int64(measuredMiB), memoryMiB(its[0]),
			"the measurement must reach capacity through the real resolution path")
	})

	t.Run("both disabled reports declared memory", func(t *testing.T) {
		p := newProvider()
		p.vmMemoryOverhead = VMMemoryOverheadConfig{}
		p.discoveredCapacity = cache.NewDiscoveredCapacity(0)

		it := newInstanceType()
		_ = p.decorateInstanceType(context.Background(), it, nodeClass, shapeAndAd, nil, "")

		assert.Equal(t, int64(declaredMiB), memoryMiB(it),
			"only with both switched off is declared memory reported unchanged")
	})
}

// Failed resolutions are not cached by the image provider, so without suppression a broken lookup
// is reissued for every shape on every listing, each with its own retries, while the instance type
// provider holds its read lock.
func TestResolveImageForDiscovery_SuppressesRepeatedFailures(t *testing.T) {
	fake := &fakeImageProvider{err: assert.AnError}
	p := &DefaultProvider{
		discoveredCapacity:      cache.NewDiscoveredCapacity(cache.DiscoveredCapacityTTL),
		imageProvider:           fake,
		imageResolutionFailures: cache.NewImageResolutionFailures(cache.ImageResolutionFailureTTL),
	}
	nc := discoveryNodeClass(testImageID)

	for i := 0; i < 5; i++ {
		assert.Equal(t, "", p.resolveImageForDiscovery(context.Background(), testShape, nc))
	}

	assert.Len(t, fake.gotShapes, 1,
		"after the first failure the shape must be suppressed, not retried on every call")
}

// Suppression is per shape, so one shape with no compatible image must not disable discovery for
// the rest of the listing.
func TestResolveImageForDiscovery_SuppressionIsPerShape(t *testing.T) {
	fake := &fakeImageProvider{err: assert.AnError}
	p := &DefaultProvider{
		discoveredCapacity:      cache.NewDiscoveredCapacity(cache.DiscoveredCapacityTTL),
		imageProvider:           fake,
		imageResolutionFailures: cache.NewImageResolutionFailures(cache.ImageResolutionFailureTTL),
	}
	nc := discoveryNodeClass(testImageID)

	p.resolveImageForDiscovery(context.Background(), "VM.Standard.E5.Flex", nc)
	p.resolveImageForDiscovery(context.Background(), "VM.Standard.E5.Flex", nc)
	p.resolveImageForDiscovery(context.Background(), "VM.Standard.A1.Flex", nc)

	assert.Equal(t, []string{"VM.Standard.E5.Flex", "VM.Standard.A1.Flex"}, fake.gotShapes,
		"the second shape must still be attempted despite the first having failed")
}

// A hung image API must degrade the model, not hold up scheduling: resolution is bounded from
// inside, so it returns no key even when the caller imposes no deadline of its own.
func TestResolveImageForDiscovery_TimesOut(t *testing.T) {
	fake := &fakeImageProvider{imageID: testImageID, block: make(chan struct{})}
	defer close(fake.block)

	p := &DefaultProvider{
		discoveredCapacity:      cache.NewDiscoveredCapacity(cache.DiscoveredCapacityTTL),
		imageProvider:           fake,
		imageResolutionFailures: cache.NewImageResolutionFailures(cache.ImageResolutionFailureTTL),
		imageResolutionTimeout:  50 * time.Millisecond,
	}

	// context.Background() deliberately: the bound must come from the provider, not the caller.
	// The watchdog matters - without the production timeout this call never returns, and a bare
	// assertion would hang until Go's ten-minute test limit rather than failing.
	done := make(chan string, 1)
	go func() {
		done <- p.resolveImageForDiscovery(context.Background(), testShape, discoveryNodeClass(testImageID))
	}()

	select {
	case got := <-done:
		assert.Equal(t, "", got, "a resolution that does not complete must yield no key")
	case <-time.After(2 * time.Second):
		t.Fatal("resolveImageForDiscovery did not return: the provider is not bounding the attempt")
	}
}

func TestResolveImageForDiscovery_TimeoutFallsBackWhenUnset(t *testing.T) {
	p := &DefaultProvider{
		discoveredCapacity:      cache.NewDiscoveredCapacity(cache.DiscoveredCapacityTTL),
		imageProvider:           &fakeImageProvider{imageID: testImageID},
		imageResolutionFailures: cache.NewImageResolutionFailures(cache.ImageResolutionFailureTTL),
		// imageResolutionTimeout deliberately zero, as a zero-valued provider would have it.
	}

	assert.Equal(t, testImageID,
		p.resolveImageForDiscovery(context.Background(), testShape, discoveryNodeClass(testImageID)),
		"an unset timeout must fall back to the default rather than expiring immediately")
}

// The maintainer's concern was the cost across a whole listing, not one call: shapes are walked
// serially, so per-shape suppression alone would still pay one attempt per shape before anything
// was suppressed. Drive makeInstanceTypes over several shapes with a failing resolver.
func TestListing_BoundsResolutionAttemptsWhenImageAPIFails(t *testing.T) {
	fake := &fakeImageProvider{err: assert.AnError}
	p := &DefaultProvider{
		shapeToPrice:            map[string]*ShapePriceInfo{},
		preemptibleShapes:       PreemptibleShapes{},
		discoveredCapacity:      cache.NewDiscoveredCapacity(cache.DiscoveredCapacityTTL),
		imageProvider:           fake,
		imageResolutionFailures: cache.NewImageResolutionFailures(cache.ImageResolutionFailureTTL),
		imageResolutionTimeout:  50 * time.Millisecond,
	}
	nodeClass := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			VolumeConfig:  &ociv1beta1.VolumeConfig{BootVolumeConfig: &ociv1beta1.BootVolumeConfig{}},
			NetworkConfig: &ociv1beta1.NetworkConfig{},
		},
	}

	shapes := []string{
		"VM.Standard2.1", "VM.Standard2.2", "VM.Standard2.4",
		"VM.Standard2.8", "VM.Standard2.16", "VM.Standard3.1",
	}
	for _, name := range shapes {
		sa := &ShapeAndAd{
			Shape: &ocicore.Shape{
				Shape: lo.ToPtr(name), IsFlexible: lo.ToPtr(false),
				Ocpus: lo.ToPtr(float32(1)), MemoryInGBs: lo.ToPtr(float32(15)),
				BillingType: ocicore.ShapeBillingTypePaid,
			},
			Ads: []string{"tenancy:PHX-AD-1"},
		}
		_, _ = p.makeInstanceTypes(context.Background(), sa, nodeClass, nil)
	}

	// Without the consecutive-failure breaker this would be one attempt per shape; the point is
	// that a broken image API costs a bounded amount regardless of how many shapes exist.
	assert.LessOrEqual(t, len(fake.gotShapes), 3,
		"a failing image API must stop being asked, not be retried once per shape in the listing")
	assert.NotEmpty(t, fake.gotShapes, "it must still try before giving up")
}

// Several shapes with no compatible image, scattered among healthy ones, is the normal case on a
// mixed cluster - a GPU image that suits few shapes, say. Those must be suppressed individually
// without tripping the global breaker, which exists for a broken API rather than for shapes that
// legitimately have no match.
//
// The failing shapes must be distinct: per-shape suppression means one bad shape fails only once,
// so repeating it would never exercise the consecutive-failure count at all.
func TestResolveImageForDiscovery_ScatteredBadShapesDoNotSuppressTheRest(t *testing.T) {
	fake := &fakeImageProvider{
		imageID: testImageID,
		failShapes: map[string]bool{
			"VM.Unresolvable.A": true, "VM.Unresolvable.B": true,
			"VM.Unresolvable.C": true, "VM.Unresolvable.D": true,
		},
	}
	p := &DefaultProvider{
		discoveredCapacity:      cache.NewDiscoveredCapacity(cache.DiscoveredCapacityTTL),
		imageProvider:           fake,
		imageResolutionFailures: cache.NewImageResolutionFailures(cache.ImageResolutionFailureTTL),
	}
	nc := discoveryNodeClass(testImageID)
	ctx := context.Background()

	// Interleaved as a listing would walk them. More failures than the breaker's limit, but each
	// is followed by a success, so the run never reaches it.
	for _, bad := range []string{"VM.Unresolvable.A", "VM.Unresolvable.B", "VM.Unresolvable.C", "VM.Unresolvable.D"} {
		assert.Equal(t, "", p.resolveImageForDiscovery(ctx, bad, nc))
		assert.Equal(t, testImageID, p.resolveImageForDiscovery(ctx, "VM.Standard.E5.Flex", nc),
			"a healthy shape must keep resolving despite unresolvable ones beside it")
	}

	assert.Equal(t, testImageID, p.resolveImageForDiscovery(ctx, "VM.Standard.A1.Flex", nc),
		"a shape not yet seen must not be suppressed either")
}

// Resolution depends on the shape and the NodeClass's image configuration together. A NodeClass
// whose images do not cover a shape must not suppress a different NodeClass whose images do - that
// one would silently fall back to the static estimate despite having a usable measurement cached.
func TestResolveImageForDiscovery_FailureDoesNotLeakAcrossNodeClasses(t *testing.T) {
	const shape = "VM.Standard.E5.Flex"

	armOnly := nodeClassWithImage("ocid1.image.oc1..arm")
	x86 := nodeClassWithImage("ocid1.image.oc1..x86")

	// The ARM NodeClass cannot resolve this x86 shape; the x86 one can.
	fake := &fakeImageProvider{imageID: testImageID, failConfigs: map[string]bool{"ocid1.image.oc1..arm": true}}
	p := &DefaultProvider{
		discoveredCapacity:      cache.NewDiscoveredCapacity(cache.DiscoveredCapacityTTL),
		imageProvider:           fake,
		imageResolutionFailures: cache.NewImageResolutionFailures(cache.ImageResolutionFailureTTL),
	}
	ctx := context.Background()

	assert.Equal(t, "", p.resolveImageForDiscovery(ctx, shape, armOnly),
		"the ARM NodeClass has no image for this shape")

	assert.Equal(t, testImageID, p.resolveImageForDiscovery(ctx, shape, x86),
		"a different NodeClass whose images do cover this shape must still resolve")
}

// Two NodeClasses sharing an image policy resolve identically, so they should share one entry
// rather than each paying their own attempt.
func TestResolveImageForDiscovery_SameImageConfigSharesSuppression(t *testing.T) {
	const shape = "VM.Standard.E5.Flex"

	a := nodeClassWithImage("ocid1.image.oc1..same")
	b := nodeClassWithImage("ocid1.image.oc1..same")

	fake := &fakeImageProvider{err: assert.AnError}
	p := &DefaultProvider{
		discoveredCapacity:      cache.NewDiscoveredCapacity(cache.DiscoveredCapacityTTL),
		imageProvider:           fake,
		imageResolutionFailures: cache.NewImageResolutionFailures(cache.ImageResolutionFailureTTL),
	}
	ctx := context.Background()

	p.resolveImageForDiscovery(ctx, shape, a)
	p.resolveImageForDiscovery(ctx, shape, b)

	assert.Len(t, fake.gotShapes, 1,
		"the same image policy must not be retried once per NodeClass that uses it")
}
