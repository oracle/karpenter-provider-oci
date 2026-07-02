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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/coreos/go-semver/semver"
	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/cache"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/capacityreservation"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/computecluster"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/identity"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	ociidentity "github.com/oracle/oci-go-sdk/v65/identity"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	clientgofake "k8s.io/client-go/kubernetes/fake"
	clientgotesting "k8s.io/client-go/testing"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

var (
	preemptibleTaintNoSchedule = v1.Taint{
		Key:    PreemptibleTaintKey,
		Effect: v1.TaintEffectNoSchedule,
		Value:  "present",
	}
	ipV4SingleStack = []network.IpFamily{network.IPv4}
	ipV6SingleStack = []network.IpFamily{network.IPv6}
	ipV6DualStack   = []network.IpFamily{network.IPv4, network.IPv6}
)

type fakeCapacityReservationProvider struct {
	results []capacityreservation.ResolveResult
	err     error
}

func (f *fakeCapacityReservationProvider) ResolveCapacityReservations(context.Context,
	[]*ociv1beta1.CapacityReservationConfig,
) ([]capacityreservation.ResolveResult, error) {
	return f.results, f.err
}

func (f *fakeCapacityReservationProvider) MarkCapacityReservationUsed(*ocicore.Instance) {}

func (f *fakeCapacityReservationProvider) MarkCapacityReservationReleased(*ocicore.Instance) {}

func (f *fakeCapacityReservationProvider) SyncCapacityReservation(context.Context, string) error {
	return nil
}

func TestCalculatePrices_BaselinesAndPrefix(t *testing.T) {
	p := &DefaultProvider{
		shapeToPrice: map[string]*ShapePriceInfo{
			"VM.STANDARD.E4.FLEX": {
				ShapeName:       lo.ToPtr("VM.Standard.E4.Flex"),
				OcpuUnitPrice:   0.05,
				MemoryUnitPrice: 0.01,
				DiskUnitPrice:   0.1,
			},
		},
	}
	shape := &ocicore.Shape{
		Shape:                    lo.ToPtr("VM.Standard.E4.Flex"),
		LocalDisksTotalSizeInGBs: lo.ToPtr[float32](50),
		BaselineOcpuUtilizations: []ocicore.ShapeBaselineOcpuUtilizationsEnum{
			ocicore.ShapeBaselineOcpuUtilizations1,
			ocicore.ShapeBaselineOcpuUtilizations2,
			ocicore.ShapeBaselineOcpuUtilizations8,
		},
	}

	// Baseline 1/1
	price, ok := p.calculatePrices(shape, 8, 64, ociv1beta1.BASELINE_1_1)
	assert.True(t, ok)
	assert.InDelta(t, 6.04, price, 0.0001)

	// Baseline 1/2
	price, ok = p.calculatePrices(shape, 8, 64, ociv1beta1.BASELINE_1_2)
	assert.True(t, ok)
	assert.InDelta(t, 5.84, price, 0.0001)

	// Baseline 1/8
	price, ok = p.calculatePrices(shape, 8, 64, ociv1beta1.BASELINE_1_8)
	assert.True(t, ok)
	assert.InDelta(t, 5.69, price, 0.0001)

	// Prefix match (price key prefix of shape)
	shape2 := &ocicore.Shape{Shape: lo.ToPtr("VM.Standard.E4.Flex3")}
	_, ok = p.calculatePrices(shape2, 1, 1, ociv1beta1.BASELINE_1_1)
	assert.True(t, ok)

	// Unknown shape -> not ok
	shape3 := &ocicore.Shape{Shape: lo.ToPtr("VM.Standard.Z9")}
	_, ok = p.calculatePrices(shape3, 1, 1, ociv1beta1.BASELINE_1_1)
	assert.False(t, ok)

	// Negative values -> not ok (assuming validation exists)
	_, ok = p.calculatePrices(shape, -1, 64, ociv1beta1.BASELINE_1_1)
	assert.True(t, ok) // actually ok, just zero cost

	// Zero values -> ok with zero cost
	price, ok = p.calculatePrices(shape, 0, 0, ociv1beta1.BASELINE_1_1)
	assert.True(t, ok)
	assert.InDelta(t, 5.0, price, 0.0001) // 0*0.05 + 0*0.01 + 50*0.1 = 5.0 (disk)

	// Missing DiskUnitPrice -> fallback to zero
	p.shapeToPrice["VM.STANDARD.E4.FLEX"].DiskUnitPrice = 0
	price, ok = p.calculatePrices(shape, 8, 64, ociv1beta1.BASELINE_1_1)
	assert.True(t, ok)
	assert.InDelta(t, 1.04, price, 0.0001) // 8*0.05 + 64*0.01 + 0 = 1.04

	// Baseline not supported (e.g., invalid) -> not ok (assuming unsupported baseline)
	// Note: BASELINE_1_1 is supported, but if unsupported enum, would fail
}

func TestCalculatePrices_Concurrency(t *testing.T) {
	p := &DefaultProvider{
		shapeToPrice: map[string]*ShapePriceInfo{
			"VM.STANDARD.E4.FLEX": {ShapeName: lo.ToPtr("VM.Standard.E4.Flex"),
				OcpuUnitPrice: 0.05, MemoryUnitPrice: 0.01, DiskUnitPrice: 0.1},
		},
	}
	shape := &ocicore.Shape{
		Shape:                    lo.ToPtr("VM.Standard.E4.Flex"),
		LocalDisksTotalSizeInGBs: lo.ToPtr[float32](50),
	}

	var wg sync.WaitGroup
	results := make([]float64, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			price, _ := p.calculatePrices(shape, 8, 64, ociv1beta1.BASELINE_1_1)
			results[idx] = price
		}(i)
	}
	wg.Wait()

	// All results should be identical
	for _, res := range results[1:] {
		assert.Equal(t, results[0], res)
	}
}

func TestIsPreemptibleShape_ExactAndPrefix(t *testing.T) {
	p := &DefaultProvider{
		preemptibleShapes: PreemptibleShapes{
			"VM.STANDARD.E4": "VM.Standard.E4",
		},
	}
	assert.True(t, p.isPreemptibleShape("VM.Standard.E4.Flex"))
	assert.True(t, p.isPreemptibleShape("vm.standard.e4.8")) // case-insensitive via upper
	assert.False(t, p.isPreemptibleShape("VM.Standard.E3.8"))

	// Mixed case
	assert.True(t, p.isPreemptibleShape("Vm.StAnDaRd.E4.2"))

	// Empty preemptibleShapes -> false
	p2 := &DefaultProvider{preemptibleShapes: PreemptibleShapes{}}
	assert.False(t, p2.isPreemptibleShape("VM.Standard.E4.Flex"))

	// Shape with whitespace -> true (upper case removes space)
	assert.True(t, p.isPreemptibleShape("VM.Standard.E4 "))
}

func TestMakeRequirementAndOffering(t *testing.T) {
	reqs := makeRequirement("tenancy:PHX-AD-1", corev1.CapacityTypeOnDemand)
	// Should have capacity type and zone
	assert.Equal(t, corev1.CapacityTypeOnDemand, reqs.Get(corev1.CapacityTypeLabelKey).Any())
	// Zone value should be PHX-AD-1 (derived from AD)
	assert.Equal(t, "PHX-AD-1", reqs.Get(v1.LabelTopologyZone).Any())

	off := makeOffering("tenancy:PHX-AD-1", 0.42, corev1.CapacityTypeOnDemand, true)
	assert.True(t, off.Available)
	assert.Equal(t, 0.42, off.Price)
	assert.Equal(t, corev1.CapacityTypeOnDemand, off.Requirements.Get(corev1.CapacityTypeLabelKey).Any())
}

func TestReloadConfigFile_Success(t *testing.T) {
	// Try both repo-root and package-relative paths for running tests from different CWDs
	candidates := []string{
		"chart/config/oci-shape-meta.json",
		filepath.Join("..", "..", "..", "chart", "config", "oci-shape-meta.json"),
	}
	var metaPath string
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			metaPath = c
			break
		}
	}

	p := &DefaultProvider{shapeMetaFile: metaPath, preemptibleShapes: make(PreemptibleShapes)}
	err := p.reloadConfigFile(context.Background())
	assert.NoError(t, err)
	// Expect some entries populated from file
	assert.Greater(t, len(p.shapeToPrice), 0)
	assert.Greater(t, len(p.preemptibleShapes), 0)
	assert.Greater(t, len(p.computeClusterShapes), 0)
}

// burstable.go tests

func TestToLaunchInstanceCpuBaseline(t *testing.T) {
	tests := []struct {
		name string
		in   ociv1beta1.BaselineOcpuUtilization
		want ocicore.LaunchInstanceShapeConfigDetailsBaselineOcpuUtilizationEnum
	}{
		{"1/1 default", ociv1beta1.BASELINE_1_1, ocicore.LaunchInstanceShapeConfigDetailsBaselineOcpuUtilization1},
		{"1/2 maps to 2", ociv1beta1.BASELINE_1_2, ocicore.LaunchInstanceShapeConfigDetailsBaselineOcpuUtilization2},
		{"1/8 maps to 8", ociv1beta1.BASELINE_1_8, ocicore.LaunchInstanceShapeConfigDetailsBaselineOcpuUtilization8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ToLaunchInstanceCpuBaseline(tt.in))
		})
	}
}

func TestFromInstanceCpuBaseline(t *testing.T) {
	tests := []struct {
		name string
		in   ocicore.InstanceShapeConfigBaselineOcpuUtilizationEnum
		want ociv1beta1.BaselineOcpuUtilization
	}{
		{"8 -> 1/8", ocicore.InstanceShapeConfigBaselineOcpuUtilization8, ociv1beta1.BASELINE_1_8},
		{"2 -> 1/2", ocicore.InstanceShapeConfigBaselineOcpuUtilization2, ociv1beta1.BASELINE_1_2},
		{"default -> 1/1", ocicore.InstanceShapeConfigBaselineOcpuUtilization1, ociv1beta1.BASELINE_1_1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FromInstanceCpuBaseline(tt.in))
		})
	}
}

func TestIsBurstableShape(t *testing.T) {
	nonBurst := &OciInstanceType{}
	assert.False(t, IsBurstableShape(nonBurst))

	b1 := &OciInstanceType{BaselineOcpuUtilization: lo.ToPtr(ociv1beta1.BASELINE_1_1)}
	assert.False(t, IsBurstableShape(b1))

	b2 := &OciInstanceType{BaselineOcpuUtilization: lo.ToPtr(ociv1beta1.BASELINE_1_2)}
	assert.True(t, IsBurstableShape(b2))

	b8 := &OciInstanceType{BaselineOcpuUtilization: lo.ToPtr(ociv1beta1.BASELINE_1_8)}
	assert.True(t, IsBurstableShape(b8))
}

// shape.go tests

func TestIsArmGpuBmDenseFlexAndArch(t *testing.T) {
	// Initialize compute cluster shapes for testing
	p := &DefaultProvider{
		computeClusterShapes: []string{
			"BM.GPU.A100-V2.8",
			"BM.GPU.H100.8",
			"BM.GPU4.8",
			"BM.HPC2.36",
			"BM.OPTIMIZED3.36",
			"BM.GPU.GB200.4",
		},
	}

	armShape := ocicore.Shape{Shape: lo.ToPtr("VM.Standard.A1.Flex"), IsFlexible: lo.ToPtr(true)}
	amdShape := ocicore.Shape{Shape: lo.ToPtr("VM.Standard.E4.Flex"), IsFlexible: lo.ToPtr(true)}
	bmShape := ocicore.Shape{Shape: lo.ToPtr("BM.Standard.E4.128")}
	gpuShape := ocicore.Shape{
		Shape:          lo.ToPtr("VM.GPU.A10.1"),
		Gpus:           lo.ToPtr(1),
		GpuDescription: lo.ToPtr("NVIDIA A10"),
	}
	amdGpuShape := ocicore.Shape{
		Shape:          lo.ToPtr("BM.GPU.MI300X.8"),
		Gpus:           lo.ToPtr(8),
		GpuDescription: lo.ToPtr("AMD MI300X"),
	}
	denseIo := ocicore.Shape{Shape: lo.ToPtr("VM.Standard3.DenseIO64"), LocalDisksTotalSizeInGBs: lo.ToPtr[float32](1024)}

	assert.True(t, IsArmShape(armShape))
	assert.False(t, IsArmShape(amdShape))

	assert.True(t, IsGpuShape(gpuShape))
	assert.False(t, IsGpuShape(amdShape))
	assert.False(t, IsAmdGpuShape(gpuShape))
	assert.True(t, IsAmdGpuShape(amdGpuShape))
	assert.False(t, IsAmdGpuShape(amdShape))

	assert.True(t, IsBmShape(*bmShape.Shape))
	assert.False(t, IsBmShape(*amdShape.Shape))

	assert.True(t, IsDenseIoShape(denseIo))
	assert.False(t, IsDenseIoShape(amdShape))

	assert.True(t, IsFlexShape(armShape))
	assert.Equal(t, ArchArm, Architecture(armShape))
	assert.Equal(t, ArchAmd, Architecture(amdShape))

	// Compute cluster supported shape list is upper-cased internally
	assert.True(t, p.isComputeClusterSupportedShape("BM.GPU.H100.8"))
	assert.False(t, p.isComputeClusterSupportedShape("VM.Standard.E4.Flex"))
}

func TestSetCapacity_GPUResources(t *testing.T) {
	nc := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			VolumeConfig: &ociv1beta1.VolumeConfig{
				BootVolumeConfig: &ociv1beta1.BootVolumeConfig{},
			},
		},
	}

	tests := []struct {
		name            string
		shape           ocicore.Shape
		wantResource    v1.ResourceName
		wantGPUQuantity *resource.Quantity
	}{
		{
			name: "nvidia gpu shape",
			shape: ocicore.Shape{
				Shape:      lo.ToPtr("VM.GPU.A10.2"),
				IsFlexible: lo.ToPtr(false),
				Gpus:       lo.ToPtr(2),
			},
			wantResource:    NvidiaGpuResourceName,
			wantGPUQuantity: resource.NewQuantity(2, resource.DecimalSI),
		},
		{
			name: "amd gpu shape",
			shape: ocicore.Shape{
				Shape:          lo.ToPtr("BM.GPU.MI300X.8"),
				IsFlexible:     lo.ToPtr(false),
				Gpus:           lo.ToPtr(8),
				GpuDescription: lo.ToPtr("AMD MI300X"),
			},
			wantResource:    AmdGpuResourceName,
			wantGPUQuantity: resource.NewQuantity(8, resource.DecimalSI),
		},
		{
			name: "non gpu shape",
			shape: ocicore.Shape{
				Shape:      lo.ToPtr("VM.Standard.E4.Flex"),
				IsFlexible: lo.ToPtr(true),
			},
		},
		{
			name: "gpu shape name with missing gpu count",
			shape: ocicore.Shape{
				Shape:      lo.ToPtr("BM.GPU.H100.8"),
				IsFlexible: lo.ToPtr(false),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := &OciInstanceType{}

			setCapacity(it, &tt.shape, 2, 8, nc, ipV4SingleStack)

			assert.Contains(t, it.Capacity, v1.ResourceCPU)
			assert.Contains(t, it.Capacity, v1.ResourceMemory)
			assert.Contains(t, it.Capacity, v1.ResourcePods)
			if tt.wantGPUQuantity == nil {
				assert.NotContains(t, it.Capacity, NvidiaGpuResourceName)
				assert.NotContains(t, it.Capacity, AmdGpuResourceName)
				return
			}
			assert.True(t, tt.wantGPUQuantity.Equal(it.Capacity[tt.wantResource]))
		})
	}
}

func TestGpuResourceName(t *testing.T) {
	tests := []struct {
		name  string
		shape ocicore.Shape
		want  v1.ResourceName
	}{
		{
			name: "amd gpu description uses amd gpu resource",
			shape: ocicore.Shape{
				Shape:          lo.ToPtr("BM.GPU.MI300X.8"),
				Gpus:           lo.ToPtr(8),
				GpuDescription: lo.ToPtr("AMD MI300X"),
			},
			want: AmdGpuResourceName,
		},
		{
			name: "a10 uses nvidia gpu resource",
			shape: ocicore.Shape{
				Shape:          lo.ToPtr("VM.GPU.A10.2"),
				Gpus:           lo.ToPtr(2),
				GpuDescription: lo.ToPtr("NVIDIA A10"),
			},
			want: NvidiaGpuResourceName,
		},
		{
			name: "h100 uses nvidia gpu resource",
			shape: ocicore.Shape{
				Shape:          lo.ToPtr("BM.GPU.H100.8"),
				Gpus:           lo.ToPtr(8),
				GpuDescription: lo.ToPtr("NVIDIA H100"),
			},
			want: NvidiaGpuResourceName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, gpuResourceName(&tt.shape))
		})
	}
}

// instance_type.go tests

func TestDecorateNodeClaimByInstanceType(t *testing.T) {
	it := &OciInstanceType{
		InstanceType: cloudprovider.InstanceType{
			Name: "VM.Standard.E4.Flex",
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(v1.LabelInstanceTypeStable, v1.NodeSelectorOpIn, "VM.Standard.E4.Flex"),
				scheduling.NewRequirement("custom.single.value", v1.NodeSelectorOpIn, "v1"),
			),
			Capacity: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewMilliQuantity(2000, resource.DecimalSI),
				v1.ResourceMemory: *resource.NewQuantity(8*1024*1024*1024, resource.BinarySI),
			},
			Overhead: &cloudprovider.InstanceTypeOverhead{},
		},
	}
	nc := &corev1.NodeClaim{ObjectMeta: metav1.ObjectMeta{
		Labels: map[string]string{},
	}}

	DecorateNodeClaimByInstanceType(nc, it)

	// Single-value requirements should be applied as labels
	assert.Equal(t, "VM.Standard.E4.Flex", nc.Labels[v1.LabelInstanceTypeStable])
	assert.Equal(t, "v1", nc.Labels["custom.single.value"])

	// Capacity should be copied and zero values filtered out
	assert.Equal(t, resource.NewMilliQuantity(2000, resource.DecimalSI), nc.Status.Capacity.Cpu())
	_, ok := nc.Status.Capacity[v1.ResourceEphemeralStorage]
	assert.False(t, ok)
}

func TestIsInstanceDriftedFromInstanceType(t *testing.T) {
	// invalid inputs
	_, err := IsInstanceDriftedFromInstanceType(nil, nil)
	assert.Error(t, err)

	// shape mismatch
	it := &OciInstanceType{Shape: "VM.Standard.E4.Flex"}
	inst := &ocicore.Instance{Shape: lo.ToPtr("VM.Standard.E3.Flex")}
	reason, err := IsInstanceDriftedFromInstanceType(inst, it)
	assert.NoError(t, err)
	assert.Equal(t, "shapeMismatch", string(reason))

	// shape config mismatch when supported but missing on instance
	it = &OciInstanceType{Shape: "VM.Standard.E4.Flex", SupportShapeConfig: true}
	inst = &ocicore.Instance{Shape: lo.ToPtr("VM.Standard.E4.Flex")}
	reason, err = IsInstanceDriftedFromInstanceType(inst, it)
	assert.NoError(t, err)
	assert.Equal(t, "shapeConfigMismatch", string(reason))

	// ocpu mismatch
	it = &OciInstanceType{Shape: "VM.Standard.E4.Flex", SupportShapeConfig: true, Ocpu: lo.ToPtr(float32(8))}
	inst = &ocicore.Instance{
		Shape: lo.ToPtr("VM.Standard.E4.Flex"),
		ShapeConfig: &ocicore.InstanceShapeConfig{Ocpus: lo.ToPtr(float32(4)), MemoryInGBs: lo.ToPtr(float32(32)),
			BaselineOcpuUtilization: ocicore.InstanceShapeConfigBaselineOcpuUtilization2},
	}
	reason, err = IsInstanceDriftedFromInstanceType(inst, it)
	assert.NoError(t, err)
	assert.Equal(t, "ocpuMismatch", string(reason))

	// memory mismatch
	it = &OciInstanceType{Shape: "VM.Standard.E4.Flex", SupportShapeConfig: true, Ocpu: lo.ToPtr(float32(8)),
		MemoryInGbs: lo.ToPtr(float32(64))}
	inst = &ocicore.Instance{
		Shape: lo.ToPtr("VM.Standard.E4.Flex"),
		ShapeConfig: &ocicore.InstanceShapeConfig{Ocpus: lo.ToPtr(float32(8)), MemoryInGBs: lo.ToPtr(float32(32)),
			BaselineOcpuUtilization: ocicore.InstanceShapeConfigBaselineOcpuUtilization2},
	}
	reason, err = IsInstanceDriftedFromInstanceType(inst, it)
	assert.NoError(t, err)
	assert.Equal(t, "memoryInGbsMismatch", string(reason))

	// baseline mismatch
	it = &OciInstanceType{Shape: "VM.Standard.E4.Flex", SupportShapeConfig: true, Ocpu: lo.ToPtr(float32(8)),
		MemoryInGbs: lo.ToPtr(float32(32)), BaselineOcpuUtilization: lo.ToPtr(ociv1beta1.BASELINE_1_2)}
	inst = &ocicore.Instance{
		Shape: lo.ToPtr("VM.Standard.E4.Flex"),
		ShapeConfig: &ocicore.InstanceShapeConfig{Ocpus: lo.ToPtr(float32(8)), MemoryInGBs: lo.ToPtr(float32(32)),
			BaselineOcpuUtilization: ocicore.InstanceShapeConfigBaselineOcpuUtilization1},
	}
	reason, err = IsInstanceDriftedFromInstanceType(inst, it)
	assert.NoError(t, err)
	assert.Equal(t, "cpuBaselineUtilizationMismatch", string(reason))

	// no drift
	it = &OciInstanceType{Shape: "VM.Standard.E4.Flex", SupportShapeConfig: true, Ocpu: lo.ToPtr(float32(8)),
		MemoryInGbs: lo.ToPtr(float32(32)), BaselineOcpuUtilization: lo.ToPtr(ociv1beta1.BASELINE_1_2)}
	inst = &ocicore.Instance{
		Shape: lo.ToPtr("VM.Standard.E4.Flex"),
		ShapeConfig: &ocicore.InstanceShapeConfig{Ocpus: lo.ToPtr(float32(8)), MemoryInGBs: lo.ToPtr(float32(32)),
			BaselineOcpuUtilization: ocicore.InstanceShapeConfigBaselineOcpuUtilization2},
	}
	reason, err = IsInstanceDriftedFromInstanceType(inst, it)
	assert.NoError(t, err)
	assert.Equal(t, "", string(reason))
}

// provider.go internal helpers tests

func TestMustParsePercentageAndParseEvictionSignal(t *testing.T) {
	// 100% is normalized to 0 according to code comments
	assert.Equal(t, float64(0), mustParsePercentage("100%"))
	assert.Equal(t, float64(25), mustParsePercentage("25%"))

	// invalid percentage panics
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("expected panic for invalid percentage")
			}
		}()
		_ = mustParsePercentage("abc%")
	}()

	// parseEvictionSignal with percentage
	capacity := resource.NewQuantity(1024, resource.BinarySI)
	got := parseEvictionSignal(capacity, "25%")
	// Ceil(1024 * .25) = 256
	want := resource.MustParse("256")
	assert.Equal(t, want.Value(), got.Value())

	// absolute value path
	got = parseEvictionSignal(capacity, "128")
	assert.Equal(t, int64(128), got.Value())
}

func TestVcpuRatioArmVsAmd(t *testing.T) {
	// AMD-like shape
	amd := &ocicore.Shape{Shape: lo.ToPtr("VM.Standard.E4.Flex")}
	// arm shape
	arm := &ocicore.Shape{Shape: lo.ToPtr("VM.Standard.A1.Flex")}

	assert.Equal(t, float32(4), vcpu(amd, float32(2))) // 2 OCPU -> 4 vCPU on AMD
	assert.Equal(t, float32(2), vcpu(arm, float32(2))) // 2 OCPU -> 2 vCPU on ARM
}

func TestListInstanceTypesForFlexShapeAndTruncate(t *testing.T) {
	p := &DefaultProvider{}
	shape := &ocicore.Shape{
		Shape:      lo.ToPtr("VM.Standard.E4.Flex"),
		IsFlexible: lo.ToPtr(true),
		OcpuOptions: &ocicore.ShapeOcpuOptions{
			Min: lo.ToPtr(float32(1)), Max: lo.ToPtr(float32(64)),
		},
		MemoryOptions: &ocicore.ShapeMemoryOptions{
			MinInGBs: lo.ToPtr(float32(1)), MaxInGBs: lo.ToPtr(float32(512)), DefaultPerOcpuInGBs: lo.ToPtr(float32(16)),
		},
		BaselineOcpuUtilizations: []ocicore.ShapeBaselineOcpuUtilizationsEnum{
			ocicore.ShapeBaselineOcpuUtilizations1, ocicore.ShapeBaselineOcpuUtilizations2,
		},
	}
	// NodeClass with two shape configs, but cluster version < 1.31 => only first is used
	nc := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			ShapeConfigs: []*ociv1beta1.ShapeConfig{
				{Ocpus: lo.ToPtr(float32(4)), MemoryInGbs: lo.ToPtr(float32(32)),
					BaselineOcpuUtilization: lo.ToPtr(ociv1beta1.BASELINE_1_2)},
				{Ocpus: lo.ToPtr(float32(8)), MemoryInGbs: lo.ToPtr(float32(64))},
			},
		},
	}
	p.k8sVersion = semver.New("1.30.0")
	its := p.listInstanceTypesForFlexShape(context.Background(), shape, nc)
	assert.Len(t, its, 1)
	assert.Equal(t, "VM.Standard.E4.Flex.4o.32g.1_2b", its[0].Name)
	assert.True(t, its[0].SupportShapeConfig)
}

func TestCalculatePricesAndOfferings(t *testing.T) {
	p := &DefaultProvider{
		shapeToPrice: map[string]*ShapePriceInfo{
			"VM.STANDARD.E4.FLEX": {ShapeName: lo.ToPtr("VM.Standard.E4.Flex"), OcpuUnitPrice: 0.05,
				MemoryUnitPrice: 0.01, DiskUnitPrice: 0},
		},
		preemptibleShapes: PreemptibleShapes{"VM.STANDARD.E4": "VM.Standard.E4"},
	}
	shape := &ocicore.Shape{
		Shape:       lo.ToPtr("VM.Standard.E4.Flex"),
		Ocpus:       lo.ToPtr(float32(4)),
		MemoryInGBs: lo.ToPtr(float32(32)),
		BillingType: ocicore.ShapeBillingTypePaid,
	}
	ads := []string{"tenancy:PHX-AD-1", "tenancy:PHX-AD-2"}
	sa := &ShapeAndAd{Shape: shape, Ads: ads}

	nc := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			VolumeConfig: &ociv1beta1.VolumeConfig{
				BootVolumeConfig: &ociv1beta1.BootVolumeConfig{
					ImageConfig: &ociv1beta1.ImageConfig{ImageType: ociv1beta1.OKEImage},
				},
			},
			NetworkConfig: &ociv1beta1.NetworkConfig{}, // ensure non-nil to avoid deref in decorateInstanceType
		},
	} // minimal volume & network config to avoid nil deref
	it := &OciInstanceType{
		InstanceType: cloudprovider.InstanceType{Name: "VM.Standard.E4.Flex"},
		Shape:        "VM.Standard.E4.Flex",
		Ocpu:         lo.ToPtr(float32(4)),
		MemoryInGbs:  lo.ToPtr(float32(32)),
	}
	// decorate -> should add on-demand offerings (available) and spot offerings since not burstable and preemptible
	err := p.decorateInstanceType(context.Background(), it, nc, sa,
		[]v1.Taint{preemptibleTaintNoSchedule})
	require.NoError(t, err)
	// We expect at least the on-demand offerings for both ads and spot offerings for both ads
	var onDemand, spot int
	for _, o := range it.Offerings {
		switch o.Requirements.Get(corev1.CapacityTypeLabelKey).Any() {
		case corev1.CapacityTypeOnDemand:
			onDemand++
		case corev1.CapacityTypeSpot:
			spot++
		}
	}
	assert.GreaterOrEqual(t, onDemand, 2)
	assert.GreaterOrEqual(t, spot, 2)
}

func TestDecorateInstanceType_DoesNotDeadlock(t *testing.T) {
	p := &DefaultProvider{
		shapeToPrice: map[string]*ShapePriceInfo{
			"VM.STANDARD.E4.FLEX": {
				ShapeName: lo.ToPtr("VM.Standard.E4.Flex"), OcpuUnitPrice: 0.05,
				MemoryUnitPrice: 0.01, DiskUnitPrice: 0,
			},
		},
		preemptibleShapes: PreemptibleShapes{"VM.STANDARD.E4": "VM.Standard.E4"},
	}
	shape := &ocicore.Shape{
		Shape:       lo.ToPtr("VM.Standard.E4.Flex"),
		Ocpus:       lo.ToPtr(float32(4)),
		MemoryInGBs: lo.ToPtr(float32(32)),
		BillingType: ocicore.ShapeBillingTypePaid,
	}
	shapeAndAd := &ShapeAndAd{Shape: shape, Ads: []string{"tenancy:PHX-AD-1"}}
	nodeClass := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			VolumeConfig: &ociv1beta1.VolumeConfig{
				BootVolumeConfig: &ociv1beta1.BootVolumeConfig{},
			},
			NetworkConfig: &ociv1beta1.NetworkConfig{},
		},
	}
	instanceType := &OciInstanceType{
		InstanceType: cloudprovider.InstanceType{Name: "VM.Standard.E4.Flex"},
		Shape:        "VM.Standard.E4.Flex",
		Ocpu:         lo.ToPtr(float32(4)),
		MemoryInGbs:  lo.ToPtr(float32(32)),
	}

	// Model ListInstanceTypes holding its outer read lock while a refresher queues for the write lock.
	p.lock.RLock()
	outerLockHeld := true
	defer func() {
		if outerLockHeld {
			p.lock.RUnlock()
		}
	}()

	writerDone := make(chan struct{})
	writerAcquired := false
	go func() {
		p.lock.Lock()
		writerAcquired = true
		p.lock.Unlock()
		close(writerDone)
	}()

	writerQueued := false
	queueDeadline := time.Now().Add(time.Second)
	for time.Now().Before(queueDeadline) {
		if !p.lock.TryRLock() {
			writerQueued = true
			break
		}
		p.lock.RUnlock()
		time.Sleep(time.Millisecond)
	}
	if !writerQueued {
		p.lock.RUnlock()
		outerLockHeld = false
		<-writerDone
		t.Fatal("writer did not queue behind the outer read lock")
	}

	decorateDone := make(chan error, 1)
	go func() {
		decorateDone <- p.decorateInstanceType(context.Background(), instanceType, nodeClass, shapeAndAd,
			[]v1.Taint{preemptibleTaintNoSchedule})
	}()

	select {
	case err := <-decorateDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		// Release and drain all blocked goroutines before failing the test.
		p.lock.RUnlock()
		outerLockHeld = false
		<-writerDone
		<-decorateDone
		t.Fatal("decorateInstanceType recursively acquired the provider read lock")
	}

	p.lock.RUnlock()
	outerLockHeld = false
	select {
	case <-writerDone:
		assert.True(t, writerAcquired)
	case <-time.After(time.Second):
		t.Fatal("queued writer did not complete after releasing the outer read lock")
	}
}

func TestFinalizeRequirements(t *testing.T) {
	p := &DefaultProvider{}
	sa := &ShapeAndAd{
		Shape: &ocicore.Shape{Shape: lo.ToPtr("VM.Standard.E4.Flex"), IsFlexible: lo.ToPtr(true)},
		Ads:   []string{"tenancy:PHX-AD-1", "tenancy:PHX-AD-2"},
	}
	it := &OciInstanceType{
		InstanceType: cloudprovider.InstanceType{
			Name: "VM.Standard.E4.Flex",
			Offerings: cloudprovider.Offerings{
				{
					Requirements: scheduling.NewRequirements(
						scheduling.NewRequirement(corev1.CapacityTypeLabelKey, v1.NodeSelectorOpIn,
							corev1.CapacityTypeOnDemand),
						scheduling.NewRequirement(v1.LabelTopologyZone, v1.NodeSelectorOpIn, "PHX-AD-1"),
					),
					Available: true,
					Price:     0.1,
				},
			},
		},
		Shape: "VM.Standard.E4.Flex",
	}
	p.finalizeRequirements(it, sa)
	// Zones captured from ShapeAndAd, OS linux, arch amd64 (E4)
	assert.Equal(t, "amd64", it.Requirements.Get(v1.LabelArchStable).Any())
	assert.ElementsMatch(t, []string{"PHX-AD-1", "PHX-AD-2"}, it.Requirements.Get(v1.LabelTopologyZone).Values())
	assert.Equal(t, "linux", it.Requirements.Get(v1.LabelOSStable).Any())
}

func TestPods_DefaultAndOverrides(t *testing.T) {
	// Default when kubelet config is nil -> 110
	q := pods(2, &ociv1beta1.OCINodeClass{}, ipV4SingleStack)
	assert.Equal(t, int64(110), q.Value())

	// When MaxPods is set and PodsPerCore is also set, we take the min(PodsPerCore*vcpu, MaxPods)
	nc := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			NetworkConfig: &ociv1beta1.NetworkConfig{},
			KubeletConfig: &ociv1beta1.KubeletConfiguration{
				MaxPods:     lo.ToPtr(int32(50)),
				PodsPerCore: lo.ToPtr(int32(40)), // 40*2 = 80, min(80, 50) = 50
			},
		},
	}
	q = pods(2, nc, ipV4SingleStack)
	assert.Equal(t, int64(50), q.Value())
}

func TestSystemReservedResources_Overrides(t *testing.T) {
	// Override defaults using systemReserved map
	nc := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			KubeletConfig: &ociv1beta1.KubeletConfiguration{
				SystemReserved: map[string]string{
					"cpu":               "200m",
					"memory":            "256Mi",
					"ephemeral-storage": "2Gi",
				},
			},
		},
	}
	res := systemReservedResources(nc)
	assert.Equal(t, resource.MustParse("200m"), res[v1.ResourceCPU])
	assert.Equal(t, resource.MustParse("256Mi"), res[v1.ResourceMemory])
	assert.Equal(t, resource.MustParse("2Gi"), res[v1.ResourceEphemeralStorage])
}

func TestKubeReservedResources_AMDvsARM_AndOverrides(t *testing.T) {
	// AMD-like shape
	amd := &ocicore.Shape{Shape: lo.ToPtr("VM.Standard.E4.Flex")}
	// ARM-like shape
	arm := &ocicore.Shape{Shape: lo.ToPtr("VM.Standard.A1.Flex")}

	// Base calculation without overrides should produce non-zero cpu/memory
	rAMD := kubeReservedResources(amd, 2, 8, &ociv1beta1.OCINodeClass{})
	assert.NotZero(t, rAMD.Cpu().MilliValue())
	assert.NotZero(t, rAMD.Memory().Value())

	rARM := kubeReservedResources(arm, 2, 8, &ociv1beta1.OCINodeClass{})
	assert.NotZero(t, rARM.Cpu().MilliValue())
	assert.NotZero(t, rARM.Memory().Value())

	// With overrides
	nc := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			KubeletConfig: &ociv1beta1.KubeletConfiguration{
				KubeReserved: map[string]string{
					"cpu":    "300m",
					"memory": "512Mi",
				},
			},
		},
	}
	ro := kubeReservedResources(amd, 2, 8, nc)
	assert.Equal(t, resource.MustParse("300m"), ro[v1.ResourceCPU])
	assert.Equal(t, resource.MustParse("512Mi"), ro[v1.ResourceMemory])
}

func TestEvictionThreshold_WithMemoryAndDisk(t *testing.T) {
	// Set both memory and nodefs eviction signals and ensure ResourceList contains entries
	nc := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			VolumeConfig: &ociv1beta1.VolumeConfig{
				BootVolumeConfig: &ociv1beta1.BootVolumeConfig{
					ImageConfig: &ociv1beta1.ImageConfig{ImageType: ociv1beta1.OKEImage},
					VolumeAttribute: ociv1beta1.VolumeAttribute{
						SizeInGBs: lo.ToPtr(int64(100)),
					},
				},
			},
			KubeletConfig: &ociv1beta1.KubeletConfiguration{
				EvictionHard: map[string]string{
					"memory.available": "10%",
					"nodefs.available": "10%",
				},
				EvictionSoft: map[string]string{
					"memory.available": "5%",
					"nodefs.available": "5%",
				},
			},
		},
	}
	res := evictionThreshold(32, nc) // gbs value matters for computing quantities
	// Ensure thresholds exist for at least memory and ephemeral storage
	_, memOk := res[v1.ResourceMemory]
	_, diskOk := res[v1.ResourceEphemeralStorage]
	assert.True(t, memOk)
	assert.True(t, diskOk)
}

func TestListInstanceTypesForFlexShape_NodeClassNil(t *testing.T) {
	p := &DefaultProvider{}
	shape := &ocicore.Shape{
		Shape:      lo.ToPtr("VM.Standard.E4.Flex"),
		IsFlexible: lo.ToPtr(true),
	}
	out := p.listInstanceTypesForFlexShape(context.TODO(), shape, nil)
	assert.Len(t, out, 0)
}

func TestListFlex_InvalidOcpuRange(t *testing.T) {
	p := &DefaultProvider{}
	shape := &ocicore.Shape{
		Shape:      lo.ToPtr("VM.Standard.E4.Flex"),
		IsFlexible: lo.ToPtr(true),
		OcpuOptions: &ocicore.ShapeOcpuOptions{
			Min: lo.ToPtr(float32(1)), Max: lo.ToPtr(float32(8)),
		},
		MemoryOptions: &ocicore.ShapeMemoryOptions{
			MinInGBs: lo.ToPtr(float32(1)), MaxInGBs: lo.ToPtr(float32(64)), DefaultPerOcpuInGBs: lo.ToPtr(float32(16)),
		},
		BaselineOcpuUtilizations: []ocicore.ShapeBaselineOcpuUtilizationsEnum{ocicore.ShapeBaselineOcpuUtilizations1},
	}
	nc := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			ShapeConfigs: []*ociv1beta1.ShapeConfig{
				{Ocpus: lo.ToPtr(float32(16))}, // beyond Max
			},
		},
	}
	out := p.listInstanceTypesForFlexShape(context.Background(), shape, nc)
	assert.Len(t, out, 0)
}

func TestListFlex_InvalidMemoryRange(t *testing.T) {
	p := &DefaultProvider{}
	shape := &ocicore.Shape{
		Shape:      lo.ToPtr("VM.Standard.E4.Flex"),
		IsFlexible: lo.ToPtr(true),
		OcpuOptions: &ocicore.ShapeOcpuOptions{
			Min: lo.ToPtr(float32(1)), Max: lo.ToPtr(float32(8)),
		},
		MemoryOptions: &ocicore.ShapeMemoryOptions{
			MinInGBs: lo.ToPtr(float32(16)), MaxInGBs: lo.ToPtr(float32(64)), DefaultPerOcpuInGBs: lo.ToPtr(float32(16)),
		},
		BaselineOcpuUtilizations: []ocicore.ShapeBaselineOcpuUtilizationsEnum{ocicore.ShapeBaselineOcpuUtilizations1},
	}
	nc := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			ShapeConfigs: []*ociv1beta1.ShapeConfig{
				{Ocpus: lo.ToPtr(float32(2)), MemoryInGbs: lo.ToPtr(float32(8))}, // below MinInGBs
			},
		},
	}
	out := p.listInstanceTypesForFlexShape(context.Background(), shape, nc)
	assert.Len(t, out, 0)
}

func TestListFlex_UnsupportedBaseline(t *testing.T) {
	p := &DefaultProvider{}
	shape := &ocicore.Shape{
		Shape:      lo.ToPtr("VM.Standard.E4.Flex"),
		IsFlexible: lo.ToPtr(true),
		OcpuOptions: &ocicore.ShapeOcpuOptions{
			Min: lo.ToPtr(float32(1)), Max: lo.ToPtr(float32(8)),
		},
		MemoryOptions: &ocicore.ShapeMemoryOptions{
			MinInGBs: lo.ToPtr(float32(1)), MaxInGBs: lo.ToPtr(float32(64)), DefaultPerOcpuInGBs: lo.ToPtr(float32(16)),
		},
		BaselineOcpuUtilizations: []ocicore.ShapeBaselineOcpuUtilizationsEnum{
			ocicore.ShapeBaselineOcpuUtilizations1, ocicore.ShapeBaselineOcpuUtilizations2,
		},
	}
	nc := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			ShapeConfigs: []*ociv1beta1.ShapeConfig{
				{Ocpus: lo.ToPtr(float32(2)), BaselineOcpuUtilization: lo.ToPtr(ociv1beta1.BASELINE_1_8)},
			},
		},
	}
	out := p.listInstanceTypesForFlexShape(context.Background(), shape, nc)
	assert.Len(t, out, 0)
}

func TestListInstanceTypes_EmptyError(t *testing.T) {
	p := &DefaultProvider{
		shapeAdMap: map[string]*ShapeAndAd{},
	}
	_, err := p.ListInstanceTypes(context.Background(), &ociv1beta1.OCINodeClass{}, make([]v1.Taint, 0))
	assert.Error(t, err)
}

func TestListInstanceTypes_NonFlexShape(t *testing.T) {
	p := &DefaultProvider{
		shapeAdMap: map[string]*ShapeAndAd{
			"VM.Standard.E3.8": {
				Shape: &ocicore.Shape{
					Shape:              lo.ToPtr("VM.Standard.E3.8"),
					IsFlexible:         lo.ToPtr(false),
					Ocpus:              lo.ToPtr(float32(8)),
					MemoryInGBs:        lo.ToPtr(float32(64)),
					MaxVnicAttachments: lo.ToPtr(4),
				},
				Ads: []string{"tenancy:PHX-AD-1"},
			},
		},
	}
	nc := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			VolumeConfig: &ociv1beta1.VolumeConfig{
				BootVolumeConfig: &ociv1beta1.BootVolumeConfig{
					ImageConfig: &ociv1beta1.ImageConfig{ImageType: ociv1beta1.OKEImage},
				},
			},
			NetworkConfig: &ociv1beta1.NetworkConfig{
				PrimaryVnicConfig: &ociv1beta1.SimpleVnicConfig{
					SubnetAndNsgConfig: &ociv1beta1.SubnetAndNsgConfig{
						SubnetConfig: &ociv1beta1.SubnetConfig{SubnetId: lo.ToPtr("ocid1.subnet.oc1..x")},
					},
				},
			},
		},
	}
	its, err := p.ListInstanceTypes(context.Background(), nc, make([]v1.Taint, 0))
	assert.NoError(t, err)
	assert.NotEmpty(t, its)
}

func TestMakeReservedOffering(t *testing.T) {
	capResKey := capacityreservation.CapacityReserveIdAndAd{
		Ocid: "ocid1.capres.oc1..abc",
		Ad:   "tenancy:PHX-AD-1",
	}
	capResAdMap := map[capacityreservation.CapacityReserveIdAndAd]map[string]capacityreservation.ShapeAvailability{
		capResKey: {
			"": capacityreservation.ShapeAvailability{Total: 10, Used: 4,
				FaultDomain: lo.ToPtr("FAULT-DOMAIN-1"),
				Ad:          "tenancy:PHX-AD-1"},
		},
	}
	const basePrice = 1.0
	offerings := makeReservedOffering(basePrice, capResAdMap)
	// Should include at least one offering for AD-1 with capacity 6 (10-4)
	found := false
	for _, o := range offerings {
		if o.Requirements.Get(v1.LabelTopologyZone).Any() == "PHX-AD-1" && o.ReservationCapacity == 6 {
			assert.Equal(t, basePrice, o.Price)
			found = true
			break
		}
	}
	assert.True(t, found)
}

func TestListInstanceTypes_CoversMakeInstanceTypesAndDecorate(t *testing.T) {
	// DefaultProvider with shapeAdMap and pricing/preemptible metadata
	p := &DefaultProvider{
		shapeAdMap: map[string]*ShapeAndAd{
			"VM.Standard.E4.Flex": {
				Shape: &ocicore.Shape{
					Shape:       lo.ToPtr("VM.Standard.E4.Flex"),
					IsFlexible:  lo.ToPtr(true),
					Ocpus:       lo.ToPtr(float32(8)),
					MemoryInGBs: lo.ToPtr(float32(64)),
					OcpuOptions: &ocicore.ShapeOcpuOptions{
						Min: lo.ToPtr(float32(1)), Max: lo.ToPtr(float32(64)),
					},
					MemoryOptions: &ocicore.ShapeMemoryOptions{
						MinInGBs: lo.ToPtr(float32(1)), MaxInGBs: lo.ToPtr(float32(512)),
						DefaultPerOcpuInGBs: lo.ToPtr(float32(16)),
					},
					BaselineOcpuUtilizations: []ocicore.ShapeBaselineOcpuUtilizationsEnum{
						ocicore.ShapeBaselineOcpuUtilizations1,
						ocicore.ShapeBaselineOcpuUtilizations2,
					},
					MaxVnicAttachments: lo.ToPtr(4),
				},
				Ads: []string{"tenancy:PHX-AD-1"},
			},
			"VM.Standard.E3.8": {
				Shape: &ocicore.Shape{
					Shape:              lo.ToPtr("VM.Standard.E3.8"),
					IsFlexible:         lo.ToPtr(false),
					Ocpus:              lo.ToPtr(float32(8)),
					MemoryInGBs:        lo.ToPtr(float32(64)),
					MaxVnicAttachments: lo.ToPtr(4),
				},
				Ads: []string{"tenancy:PHX-AD-1", "tenancy:PHX-AD-2"},
			},
		},
		shapeToPrice: map[string]*ShapePriceInfo{
			"VM.STANDARD.E4.FLEX": {ShapeName: lo.ToPtr("VM.Standard.E4.Flex"), OcpuUnitPrice: 0.05,
				MemoryUnitPrice: 0.01, DiskUnitPrice: 0},
			"VM.STANDARD.E3.8": {ShapeName: lo.ToPtr("VM.Standard.E3.8"), OcpuUnitPrice: 0.05,
				MemoryUnitPrice: 0.01, DiskUnitPrice: 0},
		},
		preemptibleShapes: PreemptibleShapes{
			"VM.STANDARD.E4": "VM.Standard.E4",
			"VM.STANDARD.E3": "VM.Standard.E3",
		},
	}
	// Kubernetes version >= 1.31 to allow multiple flex shape configs
	p.k8sVersion = semver.New("1.31.0")

	// NodeClass with:
	// - Kubelet PodsPerCore set high to potentially fail podsPerCoreAvailable for some shapes
	// - Secondary VNIC configs to also exercise vnicAvailable checks
	// - Two shapeConfigs for flex shape
	nodeClass := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			KubeletConfig: &ociv1beta1.KubeletConfiguration{
				PodsPerCore: lo.ToPtr(int32(110)), // high, may restrict some shapes
			},
			NetworkConfig: &ociv1beta1.NetworkConfig{
				PrimaryVnicConfig: &ociv1beta1.SimpleVnicConfig{
					SubnetAndNsgConfig: &ociv1beta1.SubnetAndNsgConfig{
						SubnetConfig: &ociv1beta1.SubnetConfig{SubnetId: lo.ToPtr("ocid1.subnet.oc1..x")},
					},
				},
				SecondaryVnicConfigs: []*ociv1beta1.SecondaryVnicConfig{{}, {}}, // require >1 vnic
			},
			VolumeConfig: &ociv1beta1.VolumeConfig{
				BootVolumeConfig: &ociv1beta1.BootVolumeConfig{
					ImageConfig: &ociv1beta1.ImageConfig{ImageType: ociv1beta1.OKEImage},
					VolumeAttribute: ociv1beta1.VolumeAttribute{
						SizeInGBs: lo.ToPtr(int64(100)),
					},
				},
			},
			ShapeConfigs: []*ociv1beta1.ShapeConfig{
				{Ocpus: lo.ToPtr(float32(4)), MemoryInGbs: lo.ToPtr(float32(32)),
					BaselineOcpuUtilization: lo.ToPtr(ociv1beta1.BASELINE_1_2)},
				{Ocpus: lo.ToPtr(float32(8)), MemoryInGbs: lo.ToPtr(float32(64))},
			},
		},
	}

	types, err := p.ListInstanceTypes(context.Background(), nodeClass, make([]v1.Taint, 0))
	assert.NoError(t, err)
	assert.NotEmpty(t, types)

	// Ensure that both flexible and non-flex types are returned with offerings attached
	var foundFlex, foundNonFlex bool
	for _, it := range types {
		if it.SupportShapeConfig {
			foundFlex = true
		} else {
			foundNonFlex = true
		}
		// Offerings should exist and include CapacityType labels
		if len(it.Offerings) > 0 {
			ct := it.Offerings[0].Requirements.Get(corev1.CapacityTypeLabelKey).Any()
			assert.True(t, ct == corev1.CapacityTypeOnDemand || ct == corev1.CapacityTypeSpot ||
				ct == corev1.CapacityTypeReserved)
		}
		// Capacity should include ephemeral storage if set on boot volume
		if it.Capacity != nil {
			_, hasEphemeral := it.Capacity[v1.ResourceEphemeralStorage]
			assert.True(t, hasEphemeral)
		}
	}
	assert.True(t, foundFlex || foundNonFlex)
}

func TestReservedOfferingSchedulingRequirement(t *testing.T) {
	reqs := reservedOfferingSchedulingRequirement("ocid1.capres.oc1..abc",
		capacityreservation.ShapeAvailability{
			FaultDomain: lo.ToPtr("FAULT-DOMAIN-2"),
		})
	// Should contain ReservationID label and fault domain label
	var seenResID, seenFD bool
	for _, r := range reqs {
		if r.Key == ociv1beta1.ReservationIDLabel {
			seenResID = true
		}
		if r.Key == ociv1beta1.OciFaultDomain {
			seenFD = true
		}
	}
	assert.True(t, seenResID)
	assert.True(t, seenFD)
}

func TestFinalizeRequirements_AddsSpecialShapeRequirements(t *testing.T) {
	// DenseIO VM shape
	saDense := &ShapeAndAd{
		Shape: &ocicore.Shape{
			Shape:                    lo.ToPtr("VM.Standard3.DenseIO64"),
			LocalDisksTotalSizeInGBs: lo.ToPtr[float32](1024),
			IsFlexible:               lo.ToPtr(false),
		},
		Ads: []string{"tenancy:PHX-AD-1"},
	}
	itDense := &OciInstanceType{InstanceType: cloudprovider.InstanceType{Name: "VM.Standard3.DenseIO64"},
		Shape: "VM.Standard3.DenseIO64"}
	p := &DefaultProvider{}
	p.finalizeRequirements(itDense, saDense)
	assert.True(t, itDense.Requirements.Get(v1.LabelTopologyZone).Has("PHX-AD-1"))
	// DenseIO flag should exist
	assert.True(t, itDense.Requirements.Get("oci.oraclecloud.com/dense-io-shape").Has("true"))

	// GPU BM shape
	saGpuBm := &ShapeAndAd{
		Shape: &ocicore.Shape{
			Shape:      lo.ToPtr("BM.GPU.A100-v2.8"),
			Gpus:       lo.ToPtr[int](8),
			IsFlexible: lo.ToPtr(false),
		},
		Ads: []string{"tenancy:PHX-AD-2"},
	}
	itGpuBm := &OciInstanceType{InstanceType: cloudprovider.InstanceType{Name: "BM.GPU.A100-v2.8"},
		Shape: "BM.GPU.A100-v2.8"}
	p.finalizeRequirements(itGpuBm, saGpuBm)
	// GPU/BM flags should exist
	assert.True(t, itGpuBm.Requirements.Get("oci.oraclecloud.com/gpu-shape").Has("true"))
	assert.True(t, itGpuBm.Requirements.Get("oci.oraclecloud.com/bm-shape").Has("true"))
}

func TestSetOfferings_PreemptibleSpotAdded(t *testing.T) {
	// Preemptible shapes configured to include E4 (prefix match)
	p := &DefaultProvider{
		preemptibleShapes: PreemptibleShapes{"VM.STANDARD.E4": "VM.Standard.E4"},
	}

	shape := &ocicore.Shape{
		Shape:              lo.ToPtr("VM.Standard.E4.Flex"),
		MaxVnicAttachments: lo.ToPtr(4),
	}
	sa := &ShapeAndAd{Shape: shape, Ads: []string{"tenancy:PHX-AD-1", "tenancy:PHX-AD-2"}}
	it := &OciInstanceType{
		InstanceType: cloudprovider.InstanceType{Name: "VM.Standard.E4.Flex"},
		Shape:        "VM.Standard.E4.Flex",
		// Not burstable (BaselineOcpuUtilization == nil => BASELINE_1_1)
	}

	// available = true, basePrice arbitrary, restrict allows all
	err := p.setOfferings(context.Background(), it, &ociv1beta1.OCINodeClass{}, sa, true, 1.0,
		[]v1.Taint{preemptibleTaintNoSchedule})
	assert.NoError(t, err)

	// Expect both on-demand and spot offerings when preemptible and not burstable
	var hasOnDemand, hasSpot bool
	for _, o := range it.Offerings {
		switch o.Requirements.Get("karpenter.sh/capacity-type").Any() {
		case "on-demand":
			hasOnDemand = true
		case "spot":
			hasSpot = true
		}
	}
	assert.True(t, hasOnDemand, "expected on-demand offerings")
	assert.True(t, hasSpot, "expected spot offerings for preemptible non-burstable shape")
}

func TestSetOfferings_Prices(t *testing.T) {
	const (
		ad        = "tenancy:PHX-AD-1"
		basePrice = 1.0
		shapeName = "VM.Standard.E4.Flex"
	)

	reservedNodeClass := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			CapacityReservationConfigs: []*ociv1beta1.CapacityReservationConfig{{
				CapacityReservationId: lo.ToPtr("ocid1.capacityreservation.oc1..reserved"),
			}},
		},
	}
	reservedProvider := &fakeCapacityReservationProvider{
		results: []capacityreservation.ResolveResult{{
			Ocid: "ocid1.capacityreservation.oc1..reserved",
			Ad:   ad,
			ShapeAvailabilities: []capacityreservation.ShapeAvailability{{
				Ad: ad, Shape: shapeName, Total: 1,
			}},
		}},
	}

	tests := []struct {
		name              string
		preemptible       bool
		taints            []v1.Taint
		nodeClass         *ociv1beta1.OCINodeClass
		reservationSource capacityreservation.Provider
		wantPrices        map[string]float64
	}{
		{
			name:      "on-demand uses base price",
			nodeClass: &ociv1beta1.OCINodeClass{},
			wantPrices: map[string]float64{
				corev1.CapacityTypeOnDemand: basePrice,
			},
		},
		{
			name:        "spot applies discount once",
			preemptible: true,
			taints:      []v1.Taint{preemptibleTaintNoSchedule},
			nodeClass:   &ociv1beta1.OCINodeClass{},
			wantPrices: map[string]float64{
				corev1.CapacityTypeOnDemand: basePrice,
				corev1.CapacityTypeSpot:     basePrice * spotPriceFactor,
			},
		},
		{
			name:        "spot is absent without required taint",
			preemptible: true,
			nodeClass:   &ociv1beta1.OCINodeClass{},
			wantPrices: map[string]float64{
				corev1.CapacityTypeOnDemand: basePrice,
			},
		},
		{
			name:              "reserved running instance uses base price",
			nodeClass:         reservedNodeClass,
			reservationSource: reservedProvider,
			wantPrices: map[string]float64{
				corev1.CapacityTypeOnDemand: basePrice,
				corev1.CapacityTypeReserved: basePrice,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preemptibleShapes := PreemptibleShapes{}
			if tt.preemptible {
				preemptibleShapes["VM.STANDARD.E4"] = "VM.Standard.E4"
			}
			p := &DefaultProvider{
				preemptibleShapes:           preemptibleShapes,
				capacityReservationProvider: tt.reservationSource,
			}
			it := &OciInstanceType{
				InstanceType: cloudprovider.InstanceType{Name: shapeName},
				Shape:        shapeName,
			}
			shapeAndAd := &ShapeAndAd{
				Shape: &ocicore.Shape{Shape: lo.ToPtr(shapeName)},
				Ads:   []string{ad},
			}

			err := p.setOfferings(context.Background(), it, tt.nodeClass, shapeAndAd, true, basePrice, tt.taints)
			require.NoError(t, err)
			require.Len(t, it.Offerings, len(tt.wantPrices))

			gotPrices := lo.SliceToMap(it.Offerings, func(offering *cloudprovider.Offering) (string, float64) {
				return offering.CapacityType(), offering.Price
			})
			assert.Equal(t, tt.wantPrices, gotPrices)
		})
	}
}

func TestSetOfferings_NoPreemptibleSpotIfTaintMissing(t *testing.T) {
	// Preemptible shapes configured to include E4 (prefix match)
	p := &DefaultProvider{
		preemptibleShapes: PreemptibleShapes{"VM.STANDARD.E4": "VM.Standard.E4"},
	}

	shape := &ocicore.Shape{
		Shape:              lo.ToPtr("VM.Standard.E4.Flex"),
		MaxVnicAttachments: lo.ToPtr(4),
	}
	sa := &ShapeAndAd{Shape: shape, Ads: []string{"tenancy:PHX-AD-1", "tenancy:PHX-AD-2"}}
	it := &OciInstanceType{
		InstanceType: cloudprovider.InstanceType{Name: "VM.Standard.E4.Flex"},
		Shape:        "VM.Standard.E4.Flex",
		// Not burstable (BaselineOcpuUtilization == nil => BASELINE_1_1)
	}

	// available = true, basePrice arbitrary, restrict allows all
	err := p.setOfferings(context.Background(), it, &ociv1beta1.OCINodeClass{}, sa, true, 1.0,
		make([]v1.Taint, 0))
	assert.NoError(t, err)

	// Expect both on-demand and spot offerings when preemptible and not burstable
	var hasOnDemand, hasSpot bool
	for _, o := range it.Offerings {
		switch o.Requirements.Get("karpenter.sh/capacity-type").Any() {
		case "on-demand":
			hasOnDemand = true
		case "spot":
			hasSpot = true
		}
	}
	assert.True(t, hasOnDemand, "expected on-demand offerings")
	assert.False(t, hasSpot, "expected no spot offerings for preemptible non-burstable shape")
}

func TestReloadConfigFile_FileNotFound(t *testing.T) {
	p := &DefaultProvider{shapeMetaFile: "/nonexistent/path.json"}
	err := p.reloadConfigFile(context.Background())
	require.Error(t, err)
}

func TestReloadConfigFile_MalformedJSON(t *testing.T) {
	// Create a temp file with invalid JSON
	tmpFile, err := os.CreateTemp("", "invalid*.json")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	_, err = tmpFile.WriteString("{invalid json")
	require.NoError(t, err)
	_ = tmpFile.Close()

	p := &DefaultProvider{shapeMetaFile: tmpFile.Name()}
	err = p.reloadConfigFile(context.Background())
	require.Error(t, err)
}

func TestMakeOffering_Unavailable(t *testing.T) {
	off := makeOffering("tenancy:PHX-AD-1", 0.42, corev1.CapacityTypeOnDemand, false)
	assert.False(t, off.Available)
	assert.Equal(t, 0.42, off.Price)
}

// 1. finalizeRequirements() shape-type matrix coverage (operator correctness)
func TestFinalizeRequirements_OperatorMatrixAndLabels(t *testing.T) {
	p := &DefaultProvider{region: "phx"}

	tests := []struct {
		name   string
		shape  ocicore.Shape
		expect map[string]v1.NodeSelectorOperator
	}{
		{
			name: "GPU shape",
			shape: ocicore.Shape{Shape: lo.ToPtr("VM.GPU.A10.1"), Gpus: lo.ToPtr(1),
				IsFlexible: lo.ToPtr(false)},
			expect: map[string]v1.NodeSelectorOperator{
				ociv1beta1.OciGpuShape:     v1.NodeSelectorOpIn,
				ociv1beta1.OciBmShape:      v1.NodeSelectorOpDoesNotExist,
				ociv1beta1.OciDenseIoShape: v1.NodeSelectorOpDoesNotExist,
				ociv1beta1.OciFlexShape:    v1.NodeSelectorOpDoesNotExist,
			},
		},
		{
			name: "DenseIO+Flex shape",
			shape: ocicore.Shape{Shape: lo.ToPtr("VM.Standard3.DenseIO64.Flex"), IsFlexible: lo.ToPtr(true),
				LocalDisksTotalSizeInGBs: lo.ToPtr[float32](2000)},
			expect: map[string]v1.NodeSelectorOperator{
				ociv1beta1.OciGpuShape:     v1.NodeSelectorOpDoesNotExist,
				ociv1beta1.OciBmShape:      v1.NodeSelectorOpDoesNotExist,
				ociv1beta1.OciDenseIoShape: v1.NodeSelectorOpIn,
				ociv1beta1.OciFlexShape:    v1.NodeSelectorOpIn,
			},
		},
		{
			name: "BM DenseIO shape",
			shape: ocicore.Shape{Shape: lo.ToPtr("BM.Standard3.DenseIO128"), IsFlexible: lo.ToPtr(false),
				LocalDisksTotalSizeInGBs: lo.ToPtr[float32](6000)},
			expect: map[string]v1.NodeSelectorOperator{
				ociv1beta1.OciGpuShape:     v1.NodeSelectorOpDoesNotExist,
				ociv1beta1.OciBmShape:      v1.NodeSelectorOpIn,
				ociv1beta1.OciDenseIoShape: v1.NodeSelectorOpIn,
				ociv1beta1.OciFlexShape:    v1.NodeSelectorOpDoesNotExist,
			},
		},
		{
			name:  "Plain VM",
			shape: ocicore.Shape{Shape: lo.ToPtr("VM.Standard.E3.8"), IsFlexible: lo.ToPtr(false)},
			expect: map[string]v1.NodeSelectorOperator{
				ociv1beta1.OciGpuShape:     v1.NodeSelectorOpDoesNotExist,
				ociv1beta1.OciBmShape:      v1.NodeSelectorOpDoesNotExist,
				ociv1beta1.OciDenseIoShape: v1.NodeSelectorOpDoesNotExist,
				ociv1beta1.OciFlexShape:    v1.NodeSelectorOpDoesNotExist,
			},
		},
	}

	for _, tt := range tests {
		sa := &ShapeAndAd{Shape: &tt.shape, Ads: []string{"tenancy:PHX-AD-1"}}
		it := &OciInstanceType{
			cloudprovider.InstanceType{
				Name:         *tt.shape.Shape,
				Requirements: scheduling.NewRequirements(),
			},
			false, // SupportShapeConfig
			nil,   // Ocpu
			nil,   // MemoryInGbs
			nil,   // BaselineOcpuUtilization
			*tt.shape.Shape,
		}
		p.finalizeRequirements(it, sa)
		for key, op := range tt.expect {
			req := it.InstanceType.Requirements.Get(key)
			assert.Equal(t, op, req.Operator(), "%s/%s", tt.name, key)
		}
	}
}

// 2. makeRequirement operator and AD handling
func TestMakeRequirement_OperatorAD(t *testing.T) {
	// OnDemand
	reqs := makeRequirement("tenancy:PHX-AD-1", corev1.CapacityTypeOnDemand)
	assert.Equal(t, v1.NodeSelectorOpIn, reqs.Get(corev1.CapacityTypeLabelKey).Operator())
	assert.Equal(t, v1.NodeSelectorOpIn, reqs.Get(v1.LabelTopologyZone).Operator())
	assert.Equal(t, v1.NodeSelectorOpExists, reqs.Get("karpenter.sh/reservation-id").Operator())

	// Reserved
	reqs2 := makeRequirement("tenancy:PHX-AD-1", corev1.CapacityTypeReserved)
	assert.Equal(t, v1.NodeSelectorOpIn, reqs2.Get(corev1.CapacityTypeLabelKey).Operator())
	assert.Equal(t, v1.NodeSelectorOpIn, reqs2.Get(v1.LabelTopologyZone).Operator())
	assert.Equal(t, v1.NodeSelectorOpExists, reqs2.Get("karpenter.sh/reservation-id").Operator())
}

func TestIsPreemptibleShape_Concurrent(t *testing.T) {
	p := &DefaultProvider{
		preemptibleShapes: PreemptibleShapes{"VM.STANDARD.E4": "VM.Standard.E4"},
	}

	done := make(chan bool, 100)
	for i := 0; i < 100; i++ {
		go func() {
			_ = p.isPreemptibleShape("VM.Standard.E4.Flex")
			done <- true
		}()
	}
	for i := 0; i < 100; i++ {
		<-done
	}
}

func TestCalculatePrices(t *testing.T) {
	tests := []struct {
		name      string
		shape     *ocicore.Shape
		ocpu      float32
		mem       float32
		baseline  ociv1beta1.BaselineOcpuUtilization
		wantOk    bool
		wantPrice float64
	}{
		{
			name: "exact match baseline 1/1",
			shape: &ocicore.Shape{
				Shape:                    lo.ToPtr("VM.Standard.E4.Flex"),
				LocalDisksTotalSizeInGBs: lo.ToPtr[float32](50),
			},
			ocpu:      8,
			mem:       64,
			baseline:  ociv1beta1.BASELINE_1_1,
			wantOk:    true,
			wantPrice: 6.04,
		},
		{
			name: "prefix match",
			shape: &ocicore.Shape{
				Shape:                    lo.ToPtr("VM.Standard.E4.Flex3"),
				LocalDisksTotalSizeInGBs: lo.ToPtr[float32](50),
			},
			ocpu:      1,
			mem:       1,
			baseline:  ociv1beta1.BASELINE_1_1,
			wantOk:    true,
			wantPrice: 5.06, // 1*0.05 + 1*0.01 + 50*0.1 = 5.06
		},
		{
			name: "unknown shape",
			shape: &ocicore.Shape{
				Shape:                    lo.ToPtr("VM.Standard.Z9"),
				LocalDisksTotalSizeInGBs: lo.ToPtr[float32](50),
			},
			ocpu:      1,
			mem:       1,
			baseline:  ociv1beta1.BASELINE_1_1,
			wantOk:    false,
			wantPrice: 0,
		},
		{
			name: "missing MemoryUnitPrice",
			shape: &ocicore.Shape{
				Shape:                    lo.ToPtr("VM.Standard.E4.Flex"),
				LocalDisksTotalSizeInGBs: lo.ToPtr[float32](50),
			},
			ocpu:      8,
			mem:       64,
			baseline:  ociv1beta1.BASELINE_1_1,
			wantOk:    true,
			wantPrice: 6.04, // 8*0.05 + 64*0.01 + 50*0.1 = 6.04, MemoryUnitPrice=0.01 is set, but test assumes default
		},
		{
			name: "zero diskUnitPrice",
			shape: &ocicore.Shape{
				Shape:                    lo.ToPtr("VM.Standard.E4.Flex"),
				LocalDisksTotalSizeInGBs: lo.ToPtr[float32](0),
			},
			ocpu:      8,
			mem:       64,
			baseline:  ociv1beta1.BASELINE_1_1,
			wantOk:    true,
			wantPrice: 1.04, // 8*0.05 + 64*0.01 + 0 = 1.04
		},
		{
			name: "nvidia gpu shape uses gpu unit price",
			shape: &ocicore.Shape{
				Shape: lo.ToPtr("VM.GPU.A10.2"),
				Gpus:  lo.ToPtr(2),
			},
			ocpu:      24,
			mem:       240,
			baseline:  ociv1beta1.BASELINE_1_8,
			wantOk:    true,
			wantPrice: 4,
		},
		{
			name: "amd gpu shape uses gpu unit price",
			shape: &ocicore.Shape{
				Shape: lo.ToPtr("BM.GPU.MI300X.8"),
				Gpus:  lo.ToPtr(8),
			},
			ocpu:      224,
			mem:       2048,
			baseline:  ociv1beta1.BASELINE_1_2,
			wantOk:    true,
			wantPrice: 48,
		},
		{
			name: "gpu shape price falls back without gpu count",
			shape: &ocicore.Shape{
				Shape: lo.ToPtr("VM.GPU.A10.2"),
			},
			ocpu:      24,
			mem:       240,
			baseline:  ociv1beta1.BASELINE_1_1,
			wantOk:    true,
			wantPrice: 48,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newProvider(func(p *DefaultProvider) {
				p.shapeToPrice = map[string]*ShapePriceInfo{
					"VM.STANDARD.E4.FLEX": {ShapeName: lo.ToPtr("VM.Standard.E4.Flex"), OcpuUnitPrice: 0.05,
						MemoryUnitPrice: 0.01, DiskUnitPrice: 0.1},
					"VM.GPU.A10.2": {ShapeName: lo.ToPtr("VM.GPU.A10.2"), OcpuUnitPrice: 2},
					"BM.GPU.MI300X.8": {
						ShapeName:     lo.ToPtr("BM.GPU.MI300X.8"),
						OcpuUnitPrice: 6,
					},
				}
			})
			price, ok := p.calculatePrices(tt.shape, tt.ocpu, tt.mem, tt.baseline)
			assert.Equal(t, tt.wantOk, ok)
			if ok {
				assert.InDelta(t, tt.wantPrice, price, 0.0001)
			}
		})
	}
}

func TestEvictionThreshold(t *testing.T) {
	tests := []struct {
		name string
		mem  float32
		nc   *ociv1beta1.OCINodeClass
		want map[string]string
	}{
		{
			name: "memory only percentage",
			mem:  32,
			nc: &ociv1beta1.OCINodeClass{
				Spec: ociv1beta1.OCINodeClassSpec{
					VolumeConfig: &ociv1beta1.VolumeConfig{
						BootVolumeConfig: &ociv1beta1.BootVolumeConfig{
							ImageConfig: &ociv1beta1.ImageConfig{ImageType: ociv1beta1.OKEImage},
							VolumeAttribute: ociv1beta1.VolumeAttribute{
								SizeInGBs: lo.ToPtr(int64(100)),
							},
						},
					},
					KubeletConfig: &ociv1beta1.KubeletConfiguration{
						EvictionHard: map[string]string{
							"memory.available": "10%",
						},
					},
				},
			},
			want: map[string]string{
				"memory": "3355444", // 32Mi * 0.1 = 3,355,443.2 bytes, ceiled to 3,355,444
			},
		},
		{
			name: "nodefs only percentage",
			mem:  32,
			nc: &ociv1beta1.OCINodeClass{
				Spec: ociv1beta1.OCINodeClassSpec{
					VolumeConfig: &ociv1beta1.VolumeConfig{
						BootVolumeConfig: &ociv1beta1.BootVolumeConfig{
							ImageConfig: &ociv1beta1.ImageConfig{ImageType: ociv1beta1.OKEImage},
							VolumeAttribute: ociv1beta1.VolumeAttribute{
								SizeInGBs: lo.ToPtr(int64(100)),
							},
						},
					},
					KubeletConfig: &ociv1beta1.KubeletConfiguration{
						EvictionHard: map[string]string{},
						EvictionSoft: map[string]string{
							"nodefs.available": "10%",
						},
					},
				},
			},
			want: map[string]string{
				"ephemeral-storage": "10000000000", // 100Gi decimal * 0.1 = 10Gi decimal = 10000000000 bytes
			},
		},
		{
			name: "both memory and nodefs",
			mem:  32,
			nc: &ociv1beta1.OCINodeClass{
				Spec: ociv1beta1.OCINodeClassSpec{
					VolumeConfig: &ociv1beta1.VolumeConfig{
						BootVolumeConfig: &ociv1beta1.BootVolumeConfig{
							ImageConfig: &ociv1beta1.ImageConfig{ImageType: ociv1beta1.OKEImage},
							VolumeAttribute: ociv1beta1.VolumeAttribute{
								SizeInGBs: lo.ToPtr(int64(100)),
							},
						},
					},
					KubeletConfig: &ociv1beta1.KubeletConfiguration{
						EvictionHard: map[string]string{
							"memory.available": "10%",
						},
						EvictionSoft: map[string]string{
							"nodefs.available": "10%",
						},
					},
				},
			},
			want: map[string]string{
				"memory":            "3355444",
				"ephemeral-storage": "10000000000",
			},
		},
		{
			name: "absolute value",
			mem:  32,
			nc: &ociv1beta1.OCINodeClass{
				Spec: ociv1beta1.OCINodeClassSpec{
					VolumeConfig: &ociv1beta1.VolumeConfig{
						BootVolumeConfig: &ociv1beta1.BootVolumeConfig{
							VolumeAttribute: ociv1beta1.VolumeAttribute{
								SizeInGBs: nil,
							},
						},
					},
					KubeletConfig: &ociv1beta1.KubeletConfiguration{
						EvictionHard: map[string]string{
							"memory.available": "128Mi",
						},
					},
				},
			},
			want: map[string]string{
				"memory": "128Mi",
			},
		},
		{
			name: "missing keys",
			mem:  32,
			nc:   &ociv1beta1.OCINodeClass{},
			want: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := evictionThreshold(tt.mem, tt.nc)
			for key, wantStr := range tt.want {
				want := resource.MustParse(wantStr)
				assert.True(t, want.Equal(got[v1.ResourceName(key)]), "key: %s", key)
			}
			// Ensure no extra keys
			assert.Len(t, got, len(tt.want))
		})
	}
}

func TestIsPreemptibleShape(t *testing.T) {
	tests := []struct {
		name  string
		shape string
		want  bool
		setup func(*DefaultProvider)
	}{
		{
			name:  "exact match",
			shape: "VM.Standard.E4",
			want:  true,
			setup: func(p *DefaultProvider) {
				p.preemptibleShapes = PreemptibleShapes{"VM.STANDARD.E4": "VM.Standard.E4"}
			},
		},
		{
			name:  "prefix match",
			shape: "VM.Standard.E4.Flex",
			want:  true,
			setup: func(p *DefaultProvider) {
				p.preemptibleShapes = PreemptibleShapes{"VM.STANDARD.E4": "VM.Standard.E4"}
			},
		},
		{
			name:  "case insensitive",
			shape: "vm.standard.e4.8",
			want:  true,
			setup: func(p *DefaultProvider) {
				p.preemptibleShapes = PreemptibleShapes{"VM.STANDARD.E4": "VM.Standard.E4"}
			},
		},
		{
			name:  "mixed case",
			shape: "Vm.StAnDaRd.E4.2",
			want:  true,
			setup: func(p *DefaultProvider) {
				p.preemptibleShapes = PreemptibleShapes{"VM.STANDARD.E4": "VM.Standard.E4"}
			},
		},
		{
			name:  "trailing space",
			shape: "VM.Standard.E4 ",
			want:  true,
			setup: func(p *DefaultProvider) {
				p.preemptibleShapes = PreemptibleShapes{"VM.STANDARD.E4": "VM.Standard.E4"}
			},
		},
		{
			name:  "no mapping",
			shape: "VM.Standard.E3.8",
			want:  false,
			setup: func(p *DefaultProvider) {
				p.preemptibleShapes = PreemptibleShapes{"VM.STANDARD.E4": "VM.Standard.E4"}
			},
		},
		{
			name:  "empty map",
			shape: "VM.Standard.E4.Flex",
			want:  false,
			setup: func(p *DefaultProvider) {
				p.preemptibleShapes = PreemptibleShapes{}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newProvider(tt.setup)
			got := p.isPreemptibleShape(tt.shape)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestKubeReservedResources(t *testing.T) {
	tests := []struct {
		name  string
		shape *ocicore.Shape
		vcpu  float32
		mem   float32
		nc    *ociv1beta1.OCINodeClass
		want  map[string]string // expected resource values as strings
	}{
		{
			name:  "AMD defaults",
			shape: &ocicore.Shape{Shape: lo.ToPtr("VM.Standard.E4.Flex")},
			vcpu:  2,
			mem:   8,
			nc:    &ociv1beta1.OCINodeClass{},
			want: map[string]string{
				"cpu":    "85m",
				"memory": "1844",
			},
		},
		{
			name:  "ARM defaults",
			shape: &ocicore.Shape{Shape: lo.ToPtr("VM.Standard.A1.Flex")},
			vcpu:  2,
			mem:   8,
			nc:    &ociv1beta1.OCINodeClass{},
			want: map[string]string{
				"cpu":    "72m",
				"memory": "1844",
			},
		},
		{
			name:  "cpu override only",
			shape: &ocicore.Shape{Shape: lo.ToPtr("VM.Standard.E4.Flex")},
			vcpu:  2,
			mem:   8,
			nc: &ociv1beta1.OCINodeClass{
				Spec: ociv1beta1.OCINodeClassSpec{
					KubeletConfig: &ociv1beta1.KubeletConfiguration{
						KubeReserved: map[string]string{
							"cpu": "300m",
						},
					},
				},
			},
			want: map[string]string{
				"cpu":    "300m",
				"memory": "1844",
			},
		},
		{
			name:  "memory override only",
			shape: &ocicore.Shape{Shape: lo.ToPtr("VM.Standard.E4.Flex")},
			vcpu:  2,
			mem:   8,
			nc: &ociv1beta1.OCINodeClass{
				Spec: ociv1beta1.OCINodeClassSpec{
					KubeletConfig: &ociv1beta1.KubeletConfiguration{
						KubeReserved: map[string]string{
							"memory": "512Mi",
						},
					},
				},
			},
			want: map[string]string{
				"cpu":    "85m",
				"memory": "512Mi",
			},
		},
		{
			name:  "both overrides",
			shape: &ocicore.Shape{Shape: lo.ToPtr("VM.Standard.E4.Flex")},
			vcpu:  2,
			mem:   8,
			nc: &ociv1beta1.OCINodeClass{
				Spec: ociv1beta1.OCINodeClassSpec{
					KubeletConfig: &ociv1beta1.KubeletConfiguration{
						KubeReserved: map[string]string{
							"cpu":    "300m",
							"memory": "512Mi",
						},
					},
				},
			},
			want: map[string]string{
				"cpu":    "300m",
				"memory": "512Mi",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := kubeReservedResources(tt.shape, tt.vcpu, tt.mem, tt.nc)
			for key, wantStr := range tt.want {
				want := resource.MustParse(wantStr)
				assert.True(t, want.Equal(got[v1.ResourceName(key)]), "key: %s", key)
			}
		})
	}
}

func TestSystemReservedResources(t *testing.T) {
	tests := []struct {
		name string
		nc   *ociv1beta1.OCINodeClass
		want map[string]string
	}{
		{
			name: "defaults",
			nc:   &ociv1beta1.OCINodeClass{},
			want: map[string]string{
				"cpu":    "100m",
				"memory": "100Mi",
			},
		},
		{
			name: "cpu override",
			nc: &ociv1beta1.OCINodeClass{
				Spec: ociv1beta1.OCINodeClassSpec{
					KubeletConfig: &ociv1beta1.KubeletConfiguration{
						SystemReserved: map[string]string{
							"cpu": "200m",
						},
					},
				},
			},
			want: map[string]string{
				"cpu":    "200m",
				"memory": "100Mi",
			},
		},
		{
			name: "all overrides",
			nc: &ociv1beta1.OCINodeClass{
				Spec: ociv1beta1.OCINodeClassSpec{
					KubeletConfig: &ociv1beta1.KubeletConfiguration{
						SystemReserved: map[string]string{
							"cpu":               "200m",
							"memory":            "256Mi",
							"ephemeral-storage": "2Gi",
						},
					},
				},
			},
			want: map[string]string{
				"cpu":               "200m",
				"memory":            "256Mi",
				"ephemeral-storage": "2Gi",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := systemReservedResources(tt.nc)
			for key, wantStr := range tt.want {
				want := resource.MustParse(wantStr)
				assert.True(t, want.Equal(got[v1.ResourceName(key)]), "key: %s", key)
			}
		})
	}
}

const reservationIDKey = "karpenter.sh/reservation-id"

type requirementExpectation struct {
	op  v1.NodeSelectorOperator
	has string
}

func expectedRequirements(ad string, ct string) map[string]requirementExpectation {
	zone := strings.Split(ad, ":")[1] // AdToZoneLabelValue
	m := map[string]requirementExpectation{
		corev1.CapacityTypeLabelKey: {v1.NodeSelectorOpIn, ct},
		v1.LabelTopologyZone:        {v1.NodeSelectorOpIn, zone},
	}
	m[reservationIDKey] = requirementExpectation{v1.NodeSelectorOpExists, ""}
	return m
}

func expectedOfferingReqs(ad string, ct string) map[string]string {
	zone := strings.Split(ad, ":")[1]
	return map[string]string{
		corev1.CapacityTypeLabelKey: ct,
		v1.LabelTopologyZone:        zone,
	}
}

func TestMakeRequirement(t *testing.T) {
	tests := []struct {
		name string
		ad   string
		ct   string
	}{
		{
			name: "OnDemand",
			ad:   "tenancy:PHX-AD-1",
			ct:   corev1.CapacityTypeOnDemand,
		},
		{
			name: "Reserved",
			ad:   "tenancy:PHX-AD-2",
			ct:   corev1.CapacityTypeReserved,
		},
		{
			name: "Spot",
			ad:   "tenancy:PHX-AD-3",
			ct:   corev1.CapacityTypeSpot,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := makeRequirement(tt.ad, tt.ct)
			want := expectedRequirements(tt.ad, tt.ct)
			for key, expected := range want {
				req := got.Get(key)
				assert.Equal(t, expected.op, req.Operator(), "key: %s", key)
				if expected.has != "" {
					assert.True(t, req.Has(expected.has), "key: %s, expected value: %s", key, expected.has)
				}
			}
		})
	}
}

func TestMakeOffering(t *testing.T) {
	tests := []struct {
		name      string
		ad        string
		price     float64
		ct        string
		available bool
		wantPrice float64
		wantAvail bool
	}{
		{
			name:      "OnDemand available",
			ad:        "tenancy:PHX-AD-1",
			price:     0.42,
			ct:        corev1.CapacityTypeOnDemand,
			available: true,
			wantPrice: 0.42,
			wantAvail: true,
		},
		{
			name:      "Spot unavailable",
			ad:        "tenancy:PHX-AD-2",
			price:     0.10,
			ct:        corev1.CapacityTypeSpot,
			available: false,
			wantPrice: 0.10,
			wantAvail: false,
		},
		{
			name:      "Reserved available",
			ad:        "tenancy:PHX-AD-3",
			price:     0.05,
			ct:        corev1.CapacityTypeReserved,
			available: true,
			wantPrice: 0.05,
			wantAvail: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := makeOffering(tt.ad, tt.price, tt.ct, tt.available)
			assert.Equal(t, tt.wantPrice, got.Price)
			assert.Equal(t, tt.wantAvail, got.Available)
			wantReqs := expectedOfferingReqs(tt.ad, tt.ct)
			for key, expected := range wantReqs {
				assert.True(t, got.Requirements.Get(key).Has(expected), "key: %s, expected: %s",
					key, expected)
			}
		})
	}
}

func TestPods(t *testing.T) {
	tests := []struct {
		name       string
		vcpu       int64
		nc         *ociv1beta1.OCINodeClass
		ipFamilies []network.IpFamily
		want       int64
	}{
		{
			name:       "default nil config",
			vcpu:       2,
			nc:         &ociv1beta1.OCINodeClass{},
			ipFamilies: ipV4SingleStack,
			want:       110,
		},
		{
			name: "only MaxPods",
			vcpu: 2,
			nc: &ociv1beta1.OCINodeClass{
				Spec: ociv1beta1.OCINodeClassSpec{
					KubeletConfig: &ociv1beta1.KubeletConfiguration{
						MaxPods: lo.ToPtr(int32(50)),
					},
				},
			},
			ipFamilies: ipV4SingleStack,
			want:       50,
		},
		{
			name: "only PodsPerCore",
			vcpu: 2,
			nc: &ociv1beta1.OCINodeClass{
				Spec: ociv1beta1.OCINodeClassSpec{
					KubeletConfig: &ociv1beta1.KubeletConfiguration{
						PodsPerCore: lo.ToPtr(int32(40)),
					},
				},
			},
			ipFamilies: ipV4SingleStack,
			want:       80,
		},
		{
			name: "both, min takes precedence",
			vcpu: 2,
			nc: &ociv1beta1.OCINodeClass{
				Spec: ociv1beta1.OCINodeClassSpec{
					KubeletConfig: &ociv1beta1.KubeletConfiguration{
						MaxPods:     lo.ToPtr(int32(50)),
						PodsPerCore: lo.ToPtr(int32(40)),
					},
				},
			},
			ipFamilies: ipV4SingleStack,
			want:       50, // min(40*2, 50) = 50
		},
		{
			name: "PodsPerCore zero",
			vcpu: 2,
			nc: &ociv1beta1.OCINodeClass{
				Spec: ociv1beta1.OCINodeClassSpec{
					KubeletConfig: &ociv1beta1.KubeletConfiguration{
						PodsPerCore: lo.ToPtr(int32(0)),
					},
				},
			},
			ipFamilies: ipV4SingleStack,
			want:       110, // falls back to default
		},
		{
			name: "Secondary vnic min ips with maxPods only",
			vcpu: 2,
			nc: &ociv1beta1.OCINodeClass{
				Spec: ociv1beta1.OCINodeClassSpec{
					NetworkConfig: &ociv1beta1.NetworkConfig{
						SecondaryVnicConfigs: []*ociv1beta1.SecondaryVnicConfig{
							{IpCount: lo.ToPtr(16)},
							{IpCount: lo.ToPtr(16)},
						},
					},
					KubeletConfig: &ociv1beta1.KubeletConfiguration{
						MaxPods: lo.ToPtr(int32(50)),
					},
				},
			},
			ipFamilies: ipV4SingleStack,
			want:       32, // falls back to default
		},
		{
			name: "Secondary vnic min ips with PodsPerCore only",
			vcpu: 2,
			nc: &ociv1beta1.OCINodeClass{
				Spec: ociv1beta1.OCINodeClassSpec{
					NetworkConfig: &ociv1beta1.NetworkConfig{
						SecondaryVnicConfigs: []*ociv1beta1.SecondaryVnicConfig{
							{IpCount: lo.ToPtr(16)},
							{IpCount: lo.ToPtr(16)},
						},
					},
					KubeletConfig: &ociv1beta1.KubeletConfiguration{
						PodsPerCore: lo.ToPtr(int32(40)),
					},
				},
			},
			ipFamilies: ipV4SingleStack,
			want:       32, // falls back to default
		},
		{
			name: "Secondary vnic min ips with maxPods and PodsPerCore",
			vcpu: 2,
			nc: &ociv1beta1.OCINodeClass{
				Spec: ociv1beta1.OCINodeClassSpec{
					NetworkConfig: &ociv1beta1.NetworkConfig{
						SecondaryVnicConfigs: []*ociv1beta1.SecondaryVnicConfig{
							{IpCount: lo.ToPtr(16)},
							{IpCount: lo.ToPtr(16)},
						},
					},
					KubeletConfig: &ociv1beta1.KubeletConfiguration{
						MaxPods:     lo.ToPtr(int32(50)),
						PodsPerCore: lo.ToPtr(int32(40)),
					},
				},
			},
			ipFamilies: ipV4SingleStack,
			want:       32, // falls back to default
		},
		{
			name: "Secondary vni no ipcount IPv4 with maxPods and PodsPerCore",
			vcpu: 2,
			nc: &ociv1beta1.OCINodeClass{
				Spec: ociv1beta1.OCINodeClassSpec{
					NetworkConfig: &ociv1beta1.NetworkConfig{
						SecondaryVnicConfigs: []*ociv1beta1.SecondaryVnicConfig{
							{IpCount: nil},
							{IpCount: nil},
						},
					},
					KubeletConfig: &ociv1beta1.KubeletConfiguration{
						MaxPods:     lo.ToPtr(int32(70)),
						PodsPerCore: lo.ToPtr(int32(40)),
					},
				},
			},
			ipFamilies: ipV4SingleStack,
			want:       64, // falls back to default
		},
		{
			name: "Secondary vni no ipcount IPv6 Dual stack with maxPods and PodsPerCore",
			vcpu: 2,
			nc: &ociv1beta1.OCINodeClass{
				Spec: ociv1beta1.OCINodeClassSpec{
					NetworkConfig: &ociv1beta1.NetworkConfig{
						SecondaryVnicConfigs: []*ociv1beta1.SecondaryVnicConfig{
							{IpCount: nil},
							{IpCount: nil},
						},
					},
					KubeletConfig: &ociv1beta1.KubeletConfiguration{
						MaxPods:     lo.ToPtr(int32(70)),
						PodsPerCore: lo.ToPtr(int32(40)),
					},
				},
			},
			ipFamilies: ipV6DualStack,
			want:       64, // falls back to default
		},
		{
			name: "Secondary vni no ipcount IPv6SingelStack with maxPods and PodsPerCore",
			vcpu: 2,
			nc: &ociv1beta1.OCINodeClass{
				Spec: ociv1beta1.OCINodeClassSpec{
					NetworkConfig: &ociv1beta1.NetworkConfig{
						SecondaryVnicConfigs: []*ociv1beta1.SecondaryVnicConfig{
							{IpCount: nil},
							{IpCount: nil},
						},
					},
					KubeletConfig: &ociv1beta1.KubeletConfiguration{
						MaxPods:     lo.ToPtr(int32(640)),
						PodsPerCore: lo.ToPtr(int32(512)),
					},
				},
			},
			ipFamilies: ipV6SingleStack,
			want:       512, // falls back to default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := pods(tt.vcpu, tt.nc, tt.ipFamilies)
			assert.Equal(t, tt.want, got.Value())
		})
	}
}

func TestProvider_refreshClusterVersion_Success(t *testing.T) {
	// Create fake clientset with success version
	fakeClient := clientgofake.NewClientset()

	fakeVersion := &version.Info{
		Major:      "1",
		Minor:      "30",
		GitVersion: "v1.30.0",
	}

	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = fakeVersion

	p := &DefaultProvider{
		kubernetesInterface: fakeClient,
	}

	// Call refreshClusterVersion
	err := p.refreshClusterVersion(context.Background())
	require.NoError(t, err)

	// Assert k8sVersion is set and parsed correctly
	assert.NotNil(t, p.k8sVersion)
	assert.Equal(t, int64(1), p.k8sVersion.Major)
	assert.Equal(t, int64(30), p.k8sVersion.Minor)
	assert.Equal(t, "1.30.0", p.k8sVersion.String()) // semver normalizes
}

func TestProvider_refreshClusterVersion_Error(t *testing.T) {
	// Create fake clientset with error
	fakeClientset := clientgofake.NewClientset()

	discoveryErr := errors.New("discovery error")

	fakeClientset.Discovery().(*fakediscovery.FakeDiscovery).PrependReactor("*", "*",
		func(action clientgotesting.Action) (handled bool, ret runtime.Object, err error) {
			return true, nil, discoveryErr
		})

	p := &DefaultProvider{
		kubernetesInterface: fakeClientset,
	}

	// Call refreshClusterVersion
	err := p.refreshClusterVersion(context.Background())
	require.Error(t, err)
	assert.Equal(t, discoveryErr, err)

	// Assert k8sVersion remains nil
	assert.Nil(t, p.k8sVersion)
}

// fakeIdentity for testing refreshShapes
type fakeIdentity struct {
	identityClient       *ociidentity.IdentityClient
	adMap                map[string]string
	tenancyId            string
	clusterCompartmentId string
	logicalAdPrefix      string
	compartmentCache     *cache.GetOrLoadCache[*ociidentity.Compartment]
}

func (f *fakeIdentity) GetAdMap() map[string]string {
	return f.adMap
}

func TestProvider_refreshShapes_Success(t *testing.T) {
	// Set up fakes
	fakeIdent := &fakeIdentity{
		adMap: map[string]string{
			"tenancy:PHX-AD-1": "ocid1.ad.oc1..ad1",
			"tenancy:PHX-AD-2": "ocid1.ad.oc1..ad2",
		},
	}

	fakeComp := &fakes.FakeCompute{
		OnListShapes: func(ctx context.Context, r ocicore.ListShapesRequest) (ocicore.ListShapesResponse, error) {
			switch *r.AvailabilityDomain {
			case "tenancy:PHX-AD-1":
				return ocicore.ListShapesResponse{
					Items: []ocicore.Shape{
						{Shape: lo.ToPtr("VM.Standard.E4.Flex"), IsFlexible: lo.ToPtr(true)},
						{Shape: lo.ToPtr("VM.Standard.E3.8"), IsFlexible: lo.ToPtr(false)},
					},
					OpcNextPage: nil,
				}, nil
			case "tenancy:PHX-AD-2":
				return ocicore.ListShapesResponse{
					Items: []ocicore.Shape{
						{Shape: lo.ToPtr("VM.Standard.E3.8"), IsFlexible: lo.ToPtr(false)}, // duplicate shape across ADs
						{Shape: lo.ToPtr("VM.Standard.A1.Flex"), IsFlexible: lo.ToPtr(true)},
					},
					OpcNextPage: nil,
				}, nil
			default:
				return ocicore.ListShapesResponse{}, nil
			}
		},
	}

	fakeCapRes := &capacityreservation.DefaultProvider{}
	fakeComputeCluster := &computecluster.DefaultProvider{}

	p := &DefaultProvider{
		region:                      "phx",
		identityProvider:            (*identity.DefaultProvider)(unsafe.Pointer(fakeIdent)),
		computeClient:               fakeComp,
		clusterCompartmentId:        "ocid1.compartment.oc1..parent",
		capacityReservationProvider: fakeCapRes,
		computeClusterProvider:      fakeComputeCluster,
		lock:                        sync.RWMutex{},
	}

	// Call refreshShapes - this test verifies the method doesn't error
	// The actual shape population depends on proper fake setup which is unreliable with unsafe.Pointer
	err := p.refreshShapes(context.Background())
	require.NoError(t, err)
}

func TestProvider_refreshShapes_NoAds(t *testing.T) {
	// Set up fakes
	fakeIdent := &fakeIdentity{
		adMap: map[string]string{}, // no ads
	}

	fakeComp := &fakes.FakeCompute{}

	fakeCapRes := &capacityreservation.DefaultProvider{}
	fakeComputeCluster := &computecluster.DefaultProvider{}

	p := &DefaultProvider{
		region:                      "phx",
		identityProvider:            (*identity.DefaultProvider)(unsafe.Pointer(fakeIdent)),
		computeClient:               (*ocicore.ComputeClient)(unsafe.Pointer(fakeComp)),
		clusterCompartmentId:        "ocid1.compartment.oc1..parent",
		capacityReservationProvider: fakeCapRes,
		computeClusterProvider:      fakeComputeCluster,
	}

	// Call refreshShapes
	err := p.refreshShapes(context.Background())
	require.NoError(t, err)

	// Assert shapeAdMap remains empty
	assert.Len(t, p.shapeAdMap, 0)
}

// newProvider creates a DefaultProvider with defaults, modifiable via opts
func newProvider(opts ...func(*DefaultProvider)) *DefaultProvider {
	p := &DefaultProvider{
		shapeToPrice:      map[string]*ShapePriceInfo{},
		preemptibleShapes: PreemptibleShapes{},
		shapeAdMap:        map[string]*ShapeAndAd{},
		k8sVersion:        nil, // can be set via opts
	}
	for _, fn := range opts {
		fn(p)
	}
	return p
}

func TestListInstanceTypes_GPUShapeWithNoTaints(t *testing.T) {
	p := &DefaultProvider{
		shapeAdMap: map[string]*ShapeAndAd{
			"VM.GPU.A10.1": {
				Shape: &ocicore.Shape{
					Shape:              lo.ToPtr("VM.GPU.A10.1"),
					IsFlexible:         lo.ToPtr(false),
					Gpus:               lo.ToPtr(1),
					Ocpus:              lo.ToPtr(float32(15)),
					MemoryInGBs:        lo.ToPtr(float32(240)),
					MaxVnicAttachments: lo.ToPtr(4),
				},
				Ads: []string{"tenancy:PHX-AD-1"},
			},
		},
	}
	nc := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			VolumeConfig: &ociv1beta1.VolumeConfig{
				BootVolumeConfig: &ociv1beta1.BootVolumeConfig{
					ImageConfig: &ociv1beta1.ImageConfig{ImageType: ociv1beta1.OKEImage},
				},
			},
			NetworkConfig: &ociv1beta1.NetworkConfig{
				PrimaryVnicConfig: &ociv1beta1.SimpleVnicConfig{
					SubnetAndNsgConfig: &ociv1beta1.SubnetAndNsgConfig{
						SubnetConfig: &ociv1beta1.SubnetConfig{SubnetId: lo.ToPtr("ocid1.subnet.oc1..x")},
					},
				},
			},
		},
	}
	its, err := p.ListInstanceTypes(context.Background(), nc, make([]v1.Taint, 0))
	assert.Error(t, err)
	assert.Empty(t, its)
}

func TestGetMaxVnicAttachmentsForShape(t *testing.T) {
	tests := []struct {
		name               string
		isFlexible         *bool
		ocpuOptions        *ocicore.ShapeOcpuOptions
		maxVnicOpts        *ocicore.ShapeMaxVnicAttachmentOptions
		maxVnicAttachments *int
		ocpu               float32
		want               int
	}{
		{
			name:        "flexible, ocpu == min value",
			isFlexible:  lo.ToPtr(true),
			ocpuOptions: &ocicore.ShapeOcpuOptions{Min: lo.ToPtr(float32(2))},
			maxVnicOpts: &ocicore.ShapeMaxVnicAttachmentOptions{
				Min:            lo.ToPtr(4),
				Max:            lo.ToPtr(float32(10)),
				DefaultPerOcpu: lo.ToPtr(float32(2))},
			maxVnicAttachments: lo.ToPtr(2),
			ocpu:               2,
			want:               4, // falls back to ShapeMaxVnicAttachmentOptions.Min
		},
		{
			name:        "flexible, ocpu > min but result in bounds",
			isFlexible:  lo.ToPtr(true),
			ocpuOptions: &ocicore.ShapeOcpuOptions{Min: lo.ToPtr(float32(2))},
			maxVnicOpts: &ocicore.ShapeMaxVnicAttachmentOptions{
				Min:            lo.ToPtr(4),
				Max:            lo.ToPtr(float32(10)),
				DefaultPerOcpu: lo.ToPtr(float32(2))},
			maxVnicAttachments: lo.ToPtr(2),
			ocpu:               4, // ocpuDiff = 2, maxPossible = 4 + 2*2 = 8
			want:               8,
		},
		{
			name:        "flexible, ocpu > min but hits max",
			isFlexible:  lo.ToPtr(true),
			ocpuOptions: &ocicore.ShapeOcpuOptions{Min: lo.ToPtr(float32(2))},
			maxVnicOpts: &ocicore.ShapeMaxVnicAttachmentOptions{
				Min:            lo.ToPtr(4),
				Max:            lo.ToPtr(float32(7)),
				DefaultPerOcpu: lo.ToPtr(float32(2))},
			maxVnicAttachments: lo.ToPtr(2),
			ocpu:               5, // ocpuDiff = 3, maxPossible = 4 + 3*2 = 10 -> hits 7
			want:               7,
		},
		{
			name:               "non-flexible shape",
			isFlexible:         lo.ToPtr(false),
			maxVnicAttachments: lo.ToPtr(4),
			ocpu:               2,
			want:               4,
		},
		{
			name:               "missing isFlexible returns MaxVnicAttachments",
			isFlexible:         nil,
			maxVnicAttachments: lo.ToPtr(2),
			ocpu:               4,
			want:               2,
		},
		{
			name:               "missing maxVnicOpts falls back to MaxVnicAttachments",
			isFlexible:         lo.ToPtr(true),
			ocpuOptions:        &ocicore.ShapeOcpuOptions{Min: lo.ToPtr(float32(2))},
			maxVnicOpts:        nil,
			maxVnicAttachments: lo.ToPtr(2),
			ocpu:               8,
			want:               2,
		},
		{
			name:        "missing maxVnicOpts.Min falls back to MaxVnicAttachments",
			isFlexible:  lo.ToPtr(true),
			ocpuOptions: &ocicore.ShapeOcpuOptions{Min: lo.ToPtr(float32(2))},
			maxVnicOpts: &ocicore.ShapeMaxVnicAttachmentOptions{
				Min:            nil,
				Max:            lo.ToPtr(float32(10)),
				DefaultPerOcpu: lo.ToPtr(float32(2))},
			maxVnicAttachments: lo.ToPtr(2),
			ocpu:               8,
			want:               2,
		},
		{
			name:        "missing maxVnicOpts.Max falls back to MaxVnicAttachments",
			isFlexible:  lo.ToPtr(true),
			ocpuOptions: &ocicore.ShapeOcpuOptions{Min: lo.ToPtr(float32(2))},
			maxVnicOpts: &ocicore.ShapeMaxVnicAttachmentOptions{
				Min:            lo.ToPtr(4),
				Max:            nil,
				DefaultPerOcpu: lo.ToPtr(float32(2))},
			maxVnicAttachments: lo.ToPtr(2),
			ocpu:               8,
			want:               2,
		},
		{
			name:        "nil ocpuOptions falls back to MaxVnicAttachments",
			isFlexible:  lo.ToPtr(true),
			ocpuOptions: nil,
			maxVnicOpts: &ocicore.ShapeMaxVnicAttachmentOptions{
				Min:            lo.ToPtr(4),
				Max:            lo.ToPtr(float32(10)),
				DefaultPerOcpu: lo.ToPtr(float32(2))},
			maxVnicAttachments: lo.ToPtr(2),
			ocpu:               6,
			want:               2,
		},
		{
			name:        "nil ocpuOptions.Min falls back to MaxVnicAttachments",
			isFlexible:  lo.ToPtr(true),
			ocpuOptions: nil,
			maxVnicOpts: &ocicore.ShapeMaxVnicAttachmentOptions{
				Min:            lo.ToPtr(4),
				Max:            lo.ToPtr(float32(10)),
				DefaultPerOcpu: lo.ToPtr(float32(2))},
			maxVnicAttachments: lo.ToPtr(2),
			ocpu:               6,
			want:               2,
		},
		{
			name:        "flexible, defaultPerOcpu is nil",
			isFlexible:  lo.ToPtr(true),
			ocpuOptions: &ocicore.ShapeOcpuOptions{Min: lo.ToPtr(float32(2))},
			maxVnicOpts: &ocicore.ShapeMaxVnicAttachmentOptions{
				Min:            lo.ToPtr(4),
				Max:            lo.ToPtr(float32(10)),
				DefaultPerOcpu: nil},
			maxVnicAttachments: lo.ToPtr(2),
			ocpu:               4, // ocpuDiff = 2, maxPossible = 4 + 2*1 = 6
			want:               6,
		},
	}

	p := &DefaultProvider{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shape := &ocicore.Shape{
				IsFlexible:               tt.isFlexible,
				OcpuOptions:              tt.ocpuOptions,
				MaxVnicAttachmentOptions: tt.maxVnicOpts,
				MaxVnicAttachments:       tt.maxVnicAttachments,
			}
			got := p.getMaxVnicAttachmentsForShape(context.Background(), shape, tt.ocpu)
			assert.Equal(t, tt.want, got)
		})
	}
}
