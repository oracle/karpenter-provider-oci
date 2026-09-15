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

	"github.com/coreos/go-semver/semver"
	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/capacitydiscovery"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

const testImageID = "ocid1.image.oc1..a"

// fakeAdvisor stands in for capacity discovery, answering from whatever has been measured into it.
// Modelling should not care where advice comes from, so these tests exercise how it is applied
// without involving image resolution; that half is covered in the capacitydiscovery package.
type fakeAdvisor struct {
	measured map[string]resource.Quantity
	// asked records the shapes advice was sought for, so asking once per generated instance type
	// instead of once per shape is visible rather than merely slower.
	asked []string
}

func newFakeAdvisor() *fakeAdvisor {
	return &fakeAdvisor{measured: map[string]resource.Quantity{}}
}

func (a *fakeAdvisor) measure(instanceTypeName string, memory resource.Quantity) {
	a.measured[instanceTypeName] = memory
}

func (a *fakeAdvisor) AdviseForShape(_ context.Context, shape string,
	_ *ociv1beta1.OCINodeClass) capacitydiscovery.Advice {
	a.asked = append(a.asked, shape)
	return a
}

func (a *fakeAdvisor) MemoryFor(instanceTypeName string) (resource.Quantity, bool) {
	memory, ok := a.measured[instanceTypeName]
	return memory, ok
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

	newProvider := func(advisor *fakeAdvisor) *DefaultProvider {
		return &DefaultProvider{
			shapeToPrice: map[string]*ShapePriceInfo{
				"VM.STANDARD.E4.FLEX": {
					ShapeName: lo.ToPtr("VM.Standard.E4.Flex"), OcpuUnitPrice: 0.05,
					MemoryUnitPrice: 0.01, DiskUnitPrice: 0,
				},
			},
			preemptibleShapes: PreemptibleShapes{"VM.STANDARD.E4": "VM.Standard.E4"},
			capacityAdvisor:   advisor,
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
	modelled := newProvider(newFakeAdvisor())
	it := newInstanceType()
	_ = modelled.decorateInstanceType(context.Background(), it, nodeClass, shapeAndAd, nil,
		newFakeAdvisor())
	modelledMemory := it.Capacity.Memory().Value()
	assert.NotZero(t, modelledMemory)

	// Once a node of this kind has been measured, that value must reach the instance type.
	advisor := newFakeAdvisor()
	measured := resource.MustParse("30890Mi")
	advisor.measure("VM.Standard.E4.Flex", measured)

	discovered := newProvider(advisor)
	it = newInstanceType()
	_ = discovered.decorateInstanceType(context.Background(), it, nodeClass, shapeAndAd, nil, advisor)

	assert.Equal(t, measured.Value(), it.Capacity.Memory().Value(),
		"decorateInstanceType must prefer the measured capacity over the modelled one")
	assert.NotEqual(t, modelledMemory, it.Capacity.Memory().Value())
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
	newProvider := func(advisor *fakeAdvisor) *DefaultProvider {
		return &DefaultProvider{
			shapeToPrice: map[string]*ShapePriceInfo{
				"VM.STANDARD.E5.FLEX": {
					ShapeName: lo.ToPtr("VM.Standard.E5.Flex"), OcpuUnitPrice: 0.05,
					MemoryUnitPrice: 0.01, DiskUnitPrice: 0,
				},
			},
			preemptibleShapes: PreemptibleShapes{"VM.STANDARD.E5": "VM.Standard.E5"},
			vmMemoryOverhead:  defaultVMMemoryOverhead,
			capacityAdvisor:   advisor,
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
		advisor := newFakeAdvisor()
		p := newProvider(advisor)
		it := newInstanceType()

		_ = p.decorateInstanceType(context.Background(), it, nodeClass, shapeAndAd, nil, advisor)

		assert.Equal(t, int64(estimateMiB), memoryMiB(it),
			"with nothing measured, capacity is declared memory minus the configured overhead")
	})

	t.Run("a measurement overrides the static estimate", func(t *testing.T) {
		advisor := newFakeAdvisor()
		p := newProvider(advisor)
		advisor.measure("VM.Standard.E5.Flex",
			resource.MustParse(fmt.Sprintf("%dMi", measuredMiB)))

		it := newInstanceType()
		_ = p.decorateInstanceType(context.Background(), it, nodeClass, shapeAndAd, nil, advisor)

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

		advisor := newFakeAdvisor()
		p := newProvider(advisor)
		advisor.measure("VM.Standard.E5.Flex",
			resource.MustParse(fmt.Sprintf("%dMi", smallerThanEstimate)))

		it := newInstanceType()
		_ = p.decorateInstanceType(context.Background(), it, nodeClass, shapeAndAd, nil, advisor)

		assert.Equal(t, int64(smallerThanEstimate), memoryMiB(it))
	})

	t.Run("discovery disabled leaves the static estimate intact", func(t *testing.T) {
		advisor := newFakeAdvisor()
		p := newProvider(advisor)
		p.capacityAdvisor = nil

		it := newInstanceType()
		// Disabled means no image is resolved, so decoration receives no image id.
		_ = p.decorateInstanceType(context.Background(), it, nodeClass, shapeAndAd, nil, capacitydiscovery.NoAdvice())

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

		advisor := newFakeAdvisor()
		p := newProvider(advisor)
		p.shapeToPrice["VM.STANDARD2.8"] = &ShapePriceInfo{
			ShapeName: lo.ToPtr("VM.Standard2.8"), OcpuUnitPrice: 0.05, MemoryUnitPrice: 0.01,
		}
		// Recorded under the id the fake image provider resolves to, which only matches if
		// makeInstanceTypes actually asks for it and threads the answer through.
		advisor.measure("VM.Standard2.8",
			resource.MustParse(fmt.Sprintf("%dMi", measuredMiB)))

		its, err := p.makeInstanceTypes(context.Background(), fixedShape, nodeClass, nil)

		require.NoError(t, err)
		require.Len(t, its, 1)
		assert.Equal(t, int64(measuredMiB), memoryMiB(its[0]),
			"the measurement must reach capacity through the real resolution path")
	})

	t.Run("both disabled reports declared memory", func(t *testing.T) {
		advisor := newFakeAdvisor()
		p := newProvider(advisor)
		p.vmMemoryOverhead = VMMemoryOverheadConfig{}
		p.capacityAdvisor = nil

		it := newInstanceType()
		_ = p.decorateInstanceType(context.Background(), it, nodeClass, shapeAndAd, nil, capacitydiscovery.NoAdvice())

		assert.Equal(t, int64(declaredMiB), memoryMiB(it),
			"only with both switched off is declared memory reported unchanged")
	})
}

// A flexible shape expands into many instance types that share an image, so advice must be sought
// once for the shape and reused. Asking per instance type would put an OCI call behind every
// generated configuration.
func TestMakeInstanceTypes_AsksForAdviceOncePerShape(t *testing.T) {
	advisor := newFakeAdvisor()
	p := &DefaultProvider{
		shapeToPrice: map[string]*ShapePriceInfo{
			"VM.STANDARD.E5.FLEX": {
				ShapeName: lo.ToPtr("VM.Standard.E5.Flex"), OcpuUnitPrice: 0.05, MemoryUnitPrice: 0.01,
			},
		},
		preemptibleShapes: PreemptibleShapes{},
		capacityAdvisor:   advisor,
		k8sVersion:        semver.New("1.31.0"),
	}
	nodeClass := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			VolumeConfig:  &ociv1beta1.VolumeConfig{BootVolumeConfig: &ociv1beta1.BootVolumeConfig{}},
			NetworkConfig: &ociv1beta1.NetworkConfig{},
			// Two configurations of the one shape, which is the case that matters here.
			ShapeConfigs: []*ociv1beta1.ShapeConfig{
				{Ocpus: lo.ToPtr(float32(4)), MemoryInGbs: lo.ToPtr(float32(16))},
				{Ocpus: lo.ToPtr(float32(8)), MemoryInGbs: lo.ToPtr(float32(32))},
			},
		},
	}
	flexible := &ShapeAndAd{
		Shape: &ocicore.Shape{
			Shape: lo.ToPtr("VM.Standard.E5.Flex"), IsFlexible: lo.ToPtr(true),
			Ocpus: lo.ToPtr(float32(8)), MemoryInGBs: lo.ToPtr(float32(32)),
			BillingType: ocicore.ShapeBillingTypePaid,
			OcpuOptions: &ocicore.ShapeOcpuOptions{
				Min: lo.ToPtr(float32(1)), Max: lo.ToPtr(float32(64)),
			},
			MemoryOptions: &ocicore.ShapeMemoryOptions{
				MinInGBs: lo.ToPtr(float32(1)), MaxInGBs: lo.ToPtr(float32(512)),
				DefaultPerOcpuInGBs: lo.ToPtr(float32(16)),
			},
		},
		Ads: []string{"tenancy:PHX-AD-1"},
	}

	its, err := p.makeInstanceTypes(context.Background(), flexible, nodeClass, nil)

	require.NoError(t, err)
	require.Greater(t, len(its), 1, "a flexible shape should expand into several instance types")
	assert.Equal(t, []string{"VM.Standard.E5.Flex"}, advisor.asked,
		"advice must be sought once for the shape, not once per generated instance type")
}

// Advice is optional: a provider built without an advisor must model exactly as it did before
// capacity discovery existed, rather than crashing or refusing to list.
func TestMakeInstanceTypes_WithoutAnAdvisor(t *testing.T) {
	p := &DefaultProvider{
		shapeToPrice: map[string]*ShapePriceInfo{
			"VM.STANDARD2.8": {ShapeName: lo.ToPtr("VM.Standard2.8"), OcpuUnitPrice: 0.05, MemoryUnitPrice: 0.01},
		},
		preemptibleShapes: PreemptibleShapes{},
		vmMemoryOverhead:  defaultVMMemoryOverhead,
	}
	nodeClass := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			VolumeConfig:  &ociv1beta1.VolumeConfig{BootVolumeConfig: &ociv1beta1.BootVolumeConfig{}},
			NetworkConfig: &ociv1beta1.NetworkConfig{},
		},
	}
	fixed := &ShapeAndAd{
		Shape: &ocicore.Shape{
			Shape: lo.ToPtr("VM.Standard2.8"), IsFlexible: lo.ToPtr(false),
			Ocpus: lo.ToPtr(float32(8)), MemoryInGBs: lo.ToPtr(float32(32)),
			BillingType: ocicore.ShapeBillingTypePaid,
		},
		Ads: []string{"tenancy:PHX-AD-1"},
	}

	its, err := p.makeInstanceTypes(context.Background(), fixed, nodeClass, nil)

	require.NoError(t, err, "listing must not depend on an advisor being configured")
	require.Len(t, its, 1)

	const declaredMiB = 32 * 1024
	const overheadMiB = DefaultVMMemoryOverheadBaseMiB + DefaultVMMemoryOverheadPerGBMiB*32
	assert.Equal(t, int64(declaredMiB-overheadMiB), its[0].Capacity.Memory().Value()/1024/1024,
		"with no advisor the static estimate governs, exactly as before discovery existed")
}

// An advisor that answers with nothing must be treated as one that had nothing to say. Modelling
// cannot be allowed to fail because of an optional refinement to it.
func TestMakeInstanceTypes_AdvisorReturningNil(t *testing.T) {
	p := &DefaultProvider{
		shapeToPrice: map[string]*ShapePriceInfo{
			"VM.STANDARD2.8": {ShapeName: lo.ToPtr("VM.Standard2.8"), OcpuUnitPrice: 0.05, MemoryUnitPrice: 0.01},
		},
		preemptibleShapes: PreemptibleShapes{},
		vmMemoryOverhead:  defaultVMMemoryOverhead,
		capacityAdvisor:   nilAdvisor{},
	}
	nodeClass := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			VolumeConfig:  &ociv1beta1.VolumeConfig{BootVolumeConfig: &ociv1beta1.BootVolumeConfig{}},
			NetworkConfig: &ociv1beta1.NetworkConfig{},
		},
	}
	fixed := &ShapeAndAd{
		Shape: &ocicore.Shape{
			Shape: lo.ToPtr("VM.Standard2.8"), IsFlexible: lo.ToPtr(false),
			Ocpus: lo.ToPtr(float32(8)), MemoryInGBs: lo.ToPtr(float32(32)),
			BillingType: ocicore.ShapeBillingTypePaid,
		},
		Ads: []string{"tenancy:PHX-AD-1"},
	}

	its, err := p.makeInstanceTypes(context.Background(), fixed, nodeClass, nil)

	require.NoError(t, err)
	require.Len(t, its, 1)

	const declaredMiB = 32 * 1024
	const overheadMiB = DefaultVMMemoryOverheadBaseMiB + DefaultVMMemoryOverheadPerGBMiB*32
	assert.Equal(t, int64(declaredMiB-overheadMiB), its[0].Capacity.Memory().Value()/1024/1024,
		"the static estimate must stand when an advisor answers with nothing")
}

// nilAdvisor answers with a nil Advice, which a caller must not dereference.
type nilAdvisor struct{}

func (nilAdvisor) AdviseForShape(context.Context, string, *ociv1beta1.OCINodeClass) capacitydiscovery.Advice {
	return nil
}
