/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package instancetype

import (
	"context"
	"sync"
	"testing"
	"time"

	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/cache"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

const testInstanceTypeName = "VM.Standard.E5.Flex.8o.32g.1_1b"

func discoveryNodeClass(imageIDs ...string) *ociv1beta1.OCINodeClass {
	return &ociv1beta1.OCINodeClass{
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
	return &DefaultProvider{discoveredCapacity: cache.NewDiscoveredCapacity(cache.DiscoveredCapacityTTL)}
}

// A node reporting less memory than was modelled is the whole point: record it so the next launch
// of that combination is sized from measurement rather than the estimate.
func TestUpdateInstanceTypeCapacityFromNode_RecordsObservedMemory(t *testing.T) {
	p := discoveryProvider()
	nc := discoveryNodeClass("ocid1.image.oc1..a")

	err := p.UpdateInstanceTypeCapacityFromNode(context.Background(),
		discoveryNode(testInstanceTypeName, "30890Mi"), discoveryNodeClaim("ocid1.image.oc1..a"), nc)
	assert.NoError(t, err)

	got, ok := p.discoveredCapacity.Get(discoveredCapacityCacheKey(testInstanceTypeName, nc))
	assert.True(t, ok, "expected the observation to be recorded")
	want := resource.MustParse("30890Mi")
	assert.Equal(t, want.Value(), got.Value())
}

// Nodes of nominally the same kind can report slightly different totals. Keeping the smallest
// keeps the model on the safe side: over-estimating drives the launch loop this exists to stop,
// while under-estimating only wastes memory.
func TestUpdateInstanceTypeCapacityFromNode_KeepsSmallestObserved(t *testing.T) {
	p := discoveryProvider()
	nc := discoveryNodeClass("ocid1.image.oc1..a")
	ctx := context.Background()
	key := discoveredCapacityCacheKey(testInstanceTypeName, nc)

	for _, mem := range []string{"30890Mi", "30800Mi", "31000Mi"} {
		assert.NoError(t, p.UpdateInstanceTypeCapacityFromNode(ctx,
			discoveryNode(testInstanceTypeName, mem), discoveryNodeClaim("ocid1.image.oc1..a"), nc))
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
		nodeClass *ociv1beta1.OCINodeClass
		reason    string
	}{
		{
			name:      "no instance-type label",
			node:      discoveryNode("", "30890Mi"),
			nodeClaim: discoveryNodeClaim("ocid1.image.oc1..a"),
			nodeClass: discoveryNodeClass("ocid1.image.oc1..a"),
			reason:    "there is nothing to key the measurement on",
		},
		{
			name:      "image no longer a candidate",
			node:      discoveryNode(testInstanceTypeName, "30890Mi"),
			nodeClaim: discoveryNodeClaim("ocid1.image.oc1..old"),
			nodeClass: discoveryNodeClass("ocid1.image.oc1..new"),
			reason:    "the old image's memory says nothing about the new one",
		},
		{
			name:      "nodeclaim has no image id",
			node:      discoveryNode(testInstanceTypeName, "30890Mi"),
			nodeClaim: discoveryNodeClaim(""),
			nodeClass: discoveryNodeClass("ocid1.image.oc1..a"),
			reason:    "we cannot tell which image produced this measurement",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := discoveryProvider()

			assert.NoError(t, p.UpdateInstanceTypeCapacityFromNode(
				context.Background(), tt.node, tt.nodeClaim, tt.nodeClass))

			_, ok := p.discoveredCapacity.Get(discoveredCapacityCacheKey(testInstanceTypeName, tt.nodeClass))
			assert.False(t, ok, "must not record: %s", tt.reason)
		})
	}
}

func TestUpdateInstanceTypeCapacityFromNode_NilInputs(t *testing.T) {
	p := discoveryProvider()
	ctx := context.Background()
	nc := discoveryNodeClass("ocid1.image.oc1..a")

	assert.NoError(t, p.UpdateInstanceTypeCapacityFromNode(ctx, nil, discoveryNodeClaim("a"), nc))
	assert.NoError(t, p.UpdateInstanceTypeCapacityFromNode(ctx, discoveryNode(testInstanceTypeName, "1Gi"), nil, nc))
	assert.NoError(t, p.UpdateInstanceTypeCapacityFromNode(ctx, discoveryNode(testInstanceTypeName, "1Gi"),
		discoveryNodeClaim("a"), nil))
}

// The image half of the key exists so that a measurement taken under one image is never served
// after the candidate list changes.
func TestDiscoveredCapacityCacheKey(t *testing.T) {
	a := discoveryNodeClass("ocid1.image.oc1..a")
	b := discoveryNodeClass("ocid1.image.oc1..b")

	assert.Equal(t, discoveredCapacityCacheKey(testInstanceTypeName, a),
		discoveredCapacityCacheKey(testInstanceTypeName, a), "key must be stable")
	assert.NotEqual(t, discoveredCapacityCacheKey(testInstanceTypeName, a),
		discoveredCapacityCacheKey(testInstanceTypeName, b), "a different image must not reuse the entry")
	assert.NotEqual(t, discoveredCapacityCacheKey(testInstanceTypeName, a),
		discoveredCapacityCacheKey("VM.Standard.E5.Flex.4o.16g.1_1b", a),
		"a different instance type must not reuse the entry")

	// Image selection walks the sorted candidates and takes the first compatible one, so a
	// reordered list can resolve to a different image and must key differently.
	assert.NotEqual(t,
		discoveredCapacityCacheKey(testInstanceTypeName, discoveryNodeClass("a", "b")),
		discoveredCapacityCacheKey(testInstanceTypeName, discoveryNodeClass("b", "a")),
		"candidate order affects which image is chosen, so it must affect the key")
}

func TestApplyDiscoveredCapacity(t *testing.T) {
	nc := discoveryNodeClass("ocid1.image.oc1..a")
	modelled := resource.MustParse("32Gi")

	t.Run("overrides the modelled value once measured", func(t *testing.T) {
		p := discoveryProvider()
		p.discoveredCapacity.Record(context.Background(),
			discoveredCapacityCacheKey(testInstanceTypeName, nc), resource.MustParse("30890Mi"))

		it := &OciInstanceType{}
		it.Name = testInstanceTypeName
		it.Capacity = v1.ResourceList{v1.ResourceMemory: modelled}

		p.applyDiscoveredCapacity(it, nc)

		want := resource.MustParse("30890Mi")
		assert.Equal(t, want.Value(), it.Capacity.Memory().Value())
	})

	t.Run("leaves the estimate alone before anything is measured", func(t *testing.T) {
		p := discoveryProvider()

		it := &OciInstanceType{}
		it.Name = testInstanceTypeName
		it.Capacity = v1.ResourceList{v1.ResourceMemory: modelled}

		p.applyDiscoveredCapacity(it, nc)

		assert.Equal(t, modelled.Value(), it.Capacity.Memory().Value(),
			"the first launch of a combination has nothing to learn from")
	})
}

// A disabled cache must leave modelling exactly as it was, so the feature can be turned off.
func TestDiscoveredCapacityDisabled(t *testing.T) {
	p := &DefaultProvider{discoveredCapacity: cache.NewDiscoveredCapacity(0)}
	nc := discoveryNodeClass("ocid1.image.oc1..a")

	assert.NoError(t, p.UpdateInstanceTypeCapacityFromNode(context.Background(),
		discoveryNode(testInstanceTypeName, "30890Mi"), discoveryNodeClaim("ocid1.image.oc1..a"), nc))

	it := &OciInstanceType{}
	it.Name = testInstanceTypeName
	it.Capacity = v1.ResourceList{v1.ResourceMemory: resource.MustParse("32Gi")}
	p.applyDiscoveredCapacity(it, nc)

	want := resource.MustParse("32Gi")
	assert.Equal(t, want.Value(), it.Capacity.Memory().Value())
}

// A registered node that has not published memory yet must be retried, not dropped: the controller
// only watches the transition into the registered state, so nothing else would bring it back.
func TestUpdateInstanceTypeCapacityFromNode_RetriesWhenMemoryNotReported(t *testing.T) {
	p := discoveryProvider()
	nc := discoveryNodeClass("ocid1.image.oc1..a")

	err := p.UpdateInstanceTypeCapacityFromNode(context.Background(),
		discoveryNode(testInstanceTypeName, ""), discoveryNodeClaim("ocid1.image.oc1..a"), nc)

	assert.ErrorIs(t, err, ErrCapacityNotReported)
	_, ok := p.discoveredCapacity.Get(discoveredCapacityCacheKey(testInstanceTypeName, nc))
	assert.False(t, ok, "nothing should be recorded from a node that reported no memory")
}

// Re-recording an equal value must refresh the TTL, so a combination still in active use does not
// expire and force the estimate to govern launches again.
func TestDiscoveredCapacity_EqualObservationRefreshesTTL(t *testing.T) {
	c := cache.NewDiscoveredCapacity(150 * time.Millisecond)
	ctx := context.Background()
	mem := resource.MustParse("30890Mi")

	c.Record(ctx, "k", mem)
	for i := 0; i < 4; i++ {
		time.Sleep(50 * time.Millisecond)
		c.Record(ctx, "k", mem)
	}

	// Well past the original TTL; only the refreshes can be keeping it alive.
	_, ok := c.Get("k")
	assert.True(t, ok, "an equal re-observation must refresh the entry's TTL")

	time.Sleep(250 * time.Millisecond)
	_, ok = c.Get("k")
	assert.False(t, ok, "the entry must still expire once observations stop")
}

// Pins the known limit of the membership-based staleness guard, so that its behaviour is a
// deliberate choice rather than an accident: an image that is still a candidate is trusted even
// when the candidate list has changed since the node launched. See imageIsCurrent for why this is
// bounded and self-correcting.
func TestUpdateInstanceTypeCapacityFromNode_TrustsAnyCurrentCandidate(t *testing.T) {
	p := discoveryProvider()
	// Node launched from "old"; the list has since gained "new" ahead of it.
	nc := discoveryNodeClass("ocid1.image.oc1..new", "ocid1.image.oc1..old")

	assert.NoError(t, p.UpdateInstanceTypeCapacityFromNode(context.Background(),
		discoveryNode(testInstanceTypeName, "30890Mi"), discoveryNodeClaim("ocid1.image.oc1..old"), nc))

	_, ok := p.discoveredCapacity.Get(discoveredCapacityCacheKey(testInstanceTypeName, nc))
	assert.True(t, ok, "membership is the guard; tightening this needs an OCI call on the read path")
}

// Smallest-wins must hold under concurrent writers, not only the serialised controller.
func TestDiscoveredCapacity_RecordIsAtomic(t *testing.T) {
	c := cache.NewDiscoveredCapacity(cache.DiscoveredCapacityTTL)
	ctx := context.Background()
	small := resource.MustParse("30800Mi")
	large := resource.MustParse("31000Mi")

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); c.Record(ctx, "k", small) }()
		go func() { defer wg.Done(); c.Record(ctx, "k", large) }()
	}
	wg.Wait()

	got, ok := c.Get("k")
	assert.True(t, ok)
	assert.Equal(t, small.Value(), got.Value(), "the smallest observation must survive any interleaving")
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
	_ = modelled.decorateInstanceType(context.Background(), it, nodeClass, shapeAndAd, nil)
	modelledMemory := it.Capacity.Memory().Value()
	assert.NotZero(t, modelledMemory)

	// Once a node of this kind has been measured, that value must reach the instance type.
	discovered := newProvider()
	measured := resource.MustParse("30890Mi")
	discovered.discoveredCapacity.Record(context.Background(),
		discoveredCapacityCacheKey("VM.Standard.E4.Flex", nodeClass), measured)

	it = newInstanceType()
	_ = discovered.decorateInstanceType(context.Background(), it, nodeClass, shapeAndAd, nil)

	assert.Equal(t, measured.Value(), it.Capacity.Memory().Value(),
		"decorateInstanceType must prefer the measured capacity over the modelled one")
	assert.NotEqual(t, modelledMemory, it.Capacity.Memory().Value())
}
