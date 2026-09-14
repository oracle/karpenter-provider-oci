/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package instancetype

import (
	"context"
	"testing"
	"time"

	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/cache"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/image"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
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
	// gotShapes records what resolution was asked for, so passing the wrong identifier - the
	// instance type name instead of the shape, say - cannot pass unnoticed.
	gotShapes []string
}

func (f *fakeImageProvider) ResolveImages(context.Context, *ociv1beta1.ImageConfig) (*image.ImageResolveResult, error) {
	return f.resolve()
}

func (f *fakeImageProvider) ResolveImageForShape(_ context.Context, _ *ociv1beta1.ImageConfig,
	shape string) (*image.ImageResolveResult, error) {
	f.gotShapes = append(f.gotShapes, shape)
	return f.resolve()
}

func (f *fakeImageProvider) resolve() (*image.ImageResolveResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &image.ImageResolveResult{Images: []*ocicore.Image{{Id: lo.ToPtr(f.imageID)}}}, nil
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
