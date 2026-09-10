/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package instancetype

import (
	"fmt"
	"testing"

	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
)

// vmShape and bmShape stand in for the two cases the overhead distinguishes: the tax is a
// hypervisor artefact, so it applies to virtual machines and not to bare metal.
var (
	vmShape = &ocicore.Shape{Shape: lo.ToPtr("VM.Standard.E5.Flex")}
	bmShape = &ocicore.Shape{Shape: lo.ToPtr("BM.Standard.E5.192")}
)

// defaultVMMemoryOverhead is built from the shipped constants rather than a copy of them, so the
// safety property below is asserted against what actually ships. pkg/operator/options and the Helm
// chart are pinned to the same constants by TestVMMemoryOverheadDefaults*.
var defaultVMMemoryOverhead = VMMemoryOverheadConfig{
	BaseMiB:  DefaultVMMemoryOverheadBaseMiB,
	PerGBMiB: DefaultVMMemoryOverheadPerGBMiB,
	Percent:  DefaultVMMemoryOverheadPercent,
}

// publishedMeasurements are every measurement of OCI's VM memory overhead reported on
// oracle/karpenter-provider-oci#7, across four CPU families and 8-128 GB. Both sources measure the
// same quantity: the kubelet publishes /proc/meminfo MemTotal as node.status.capacity.memory.
//
//   - Ten in the issue body (A1, E4 and Standard3 at 8/50/128 GB, plus A1 at 12 GB), read from
//     /proc/meminfo MemTotal:
//     https://github.com/oracle/karpenter-provider-oci/issues/7
//
//   - Three in a follow-up comment (E5 at 16/28/32 GB), read from node.status.capacity.memory as
//     reported by the kubelet:
//     https://github.com/oracle/karpenter-provider-oci/issues/7#issuecomment-5320644834
//     Collected on OKE Enhanced 1.35.2, Oracle Linux 9.8, VCN-native CNI, in ap-singapore-1, on
//     VM.Standard.E5.Flex (AMD EPYC Genoa).
//
// The E5 rows matter disproportionately: they are the closest points to the default, and set its
// 71 MiB safety margin. The other three families clear it by 175 MiB or more.
//
// The property that matters is that Karpenter's modelled capacity never exceeds the real
// capacity. Exact agreement is neither achievable nor desirable: over-estimating makes Karpenter
// launch a node the pending pod cannot fit on, and with no feedback loop it repeats that launch
// indefinitely, while under-estimating merely wastes a little memory.
var publishedMeasurements = []struct {
	family      string
	declaredMiB int64
	actualMiB   int64
}{
	{"A1", 8192, 7657},
	{"E4", 8192, 7646},
	{"Std3", 8192, 7644},
	{"A1", 12288, 11669},
	{"E5", 16384, 15698},
	{"E5", 28672, 27628},
	{"E5", 32768, 31631},
	{"A1", 51200, 49825},
	{"E4", 51200, 49896},
	{"Std3", 51200, 49894},
	{"A1", 131072, 128292},
	{"E4", 131072, 128286},
	{"Std3", 131072, 128284},
}

// TestMemoryCapacity_NeverOverEstimates is the regression test for #7: the shipped defaults must
// not over-estimate capacity against any published measurement.
func TestMemoryCapacity_NeverOverEstimates(t *testing.T) {
	for _, m := range publishedMeasurements {
		t.Run(fmt.Sprintf("%s/%dMiB", m.family, m.declaredMiB), func(t *testing.T) {
			gbs := float32(m.declaredMiB) / 1024
			modelled := memoryCapacityMiB(vmShape, gbs, defaultVMMemoryOverhead)

			// Clearing every measured point is necessary but not sufficient. These are 13 points
			// from a handful of images; a default that clears the closest of them by a hair is
			// one unmeasured image build away from over-estimating again. Require real headroom
			// so that a future retune cannot quietly reintroduce the bug this test exists to
			// catch, while staying far enough below the shipped margin (71 MiB) not to be brittle.
			const minMarginMiB = 64

			margin := m.actualMiB - modelled
			assert.GreaterOrEqual(t, margin, int64(minMarginMiB),
				"modelled capacity %d MiB leaves only %d MiB of headroom against measured capacity "+
					"%d MiB for a %g GiB %s shape; below %d MiB an unmeasured image build could push "+
					"real capacity under the model, and Karpenter would launch nodes too small for "+
					"the pods that triggered them",
				modelled, margin, m.actualMiB, gbs, m.family, minMarginMiB)

			// The other direction: a safe estimate is still meant to be useful, so it should stay
			// within 1 GiB of the real figure rather than throwing memory away.
			assert.Less(t, margin, int64(1024),
				"modelled capacity %d MiB under-estimates measured capacity %d MiB by more than 1 GiB",
				modelled, m.actualMiB)
		})
	}
}

func TestMemoryCapacity_Defaults(t *testing.T) {
	tests := []struct {
		name string
		gbs  float32
		want int64
	}{
		// declared MiB - (600 + 19*GiB)
		{"8 GiB", 8, 8192 - 752},
		{"16 GiB", 16, 16384 - 904},
		{"32 GiB", 32, 32768 - 1208},
		{"128 GiB", 128, 131072 - 3032},
		// Fractional declarations round the overhead up, never down: 600 + 19*1.5 = 628.5.
		{"1.5 GiB", 1.5, 1536 - 629},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, memoryCapacityMiB(vmShape, tt.gbs, defaultVMMemoryOverhead))
		})
	}
}

func TestVMMemoryOverhead_PercentIsAFloor(t *testing.T) {
	// The percentage is an alternative way of expressing the same overhead. Combining it with
	// max() means enabling it can only ever make the estimate more conservative, never less.
	fixedOnly := VMMemoryOverheadConfig{BaseMiB: 600, PerGBMiB: 19}
	withPercent := VMMemoryOverheadConfig{BaseMiB: 600, PerGBMiB: 19, Percent: 0.075}

	for _, gbs := range []float64{1, 8, 16, 32, 64, 128, 512} {
		assert.GreaterOrEqual(t, withPercent.OverheadMiB(gbs), fixedOnly.OverheadMiB(gbs),
			"adding a percentage must never reduce the overhead (gbs=%g)", gbs)
	}

	// 7.5% dominates on small shapes only once the shape is large enough; at 128 GiB the flat
	// percentage reserves far more than the fitted form, which is why it is not the default.
	assert.Equal(t, int64(9831), withPercent.OverheadMiB(128)) // 0.075*128*1024 = 9830.4, rounded up
	assert.Equal(t, int64(3032), fixedOnly.OverheadMiB(128))
}

func TestMemoryCapacity_ClampsToPositive(t *testing.T) {
	// A shape too small to survive the overhead must still report positive capacity.
	huge := VMMemoryOverheadConfig{BaseMiB: 100000, PerGBMiB: 0}

	assert.Equal(t, int64(1), memoryCapacityMiB(vmShape, 1, huge))
	assert.Equal(t, int64(1), memoryCapacityMiB(vmShape, 0, huge))
}

func TestMemoryCapacity_ZeroOverheadIsDeclaredMemory(t *testing.T) {
	// Setting the overhead to zero must actually disable it, restoring pre-#7 behaviour.
	none := VMMemoryOverheadConfig{}

	assert.Equal(t, int64(32768), memoryCapacityMiB(vmShape, 32, none))
	assert.Equal(t, int64(0), none.OverheadMiB(32))
}

// The overhead models memory taken by a hypervisor. Bare metal has none, so applying a VM-derived
// figure there would shrink advertised capacity with no evidence behind it.
//
// The table spans several BM families rather than one example: a gate written as an exact match on
// a single shape name would satisfy a narrower test while still taxing every other BM family.
func TestMemoryCapacity_BareMetalKeepsDeclaredMemory(t *testing.T) {
	bmShapes := []string{
		"BM.Standard.E5.192",
		"BM.Standard3.64",
		"BM.DenseIO.E5.128",
		"BM.GPU.H100.8",
		"BM.Optimized3.36",
		"bm.standard.e5.192", // shape strings are matched case-insensitively
	}
	vmShapes := []string{
		"VM.Standard.E5.Flex",
		"VM.Standard.A1.Flex",
		"VM.GPU.A10.2",
		"VM.Optimized3.Flex",
	}

	for _, gbs := range []float32{8, 32, 128, 768} {
		declared := int64(gbs) * 1024

		for _, name := range bmShapes {
			t.Run(fmt.Sprintf("%s/%gGiB", name, gbs), func(t *testing.T) {
				shape := &ocicore.Shape{Shape: lo.ToPtr(name)}

				assert.Equal(t, declared, memoryCapacityMiB(shape, gbs, defaultVMMemoryOverhead),
					"bare metal must report its declared memory unchanged")
			})
		}

		for _, name := range vmShapes {
			t.Run(fmt.Sprintf("%s/%gGiB", name, gbs), func(t *testing.T) {
				shape := &ocicore.Shape{Shape: lo.ToPtr(name)}

				assert.Less(t, memoryCapacityMiB(shape, gbs, defaultVMMemoryOverhead), declared,
					"a VM shape must still be adjusted for hypervisor overhead")
			})
		}
	}
}

// A nil or unnamed shape must not panic, and must fall through to the VM path so that an
// unidentifiable shape is treated conservatively rather than trusted at its declared size.
func TestMemoryCapacity_NilShapeUsesVMPath(t *testing.T) {
	declared := int64(32 * 1024)

	assert.Less(t, memoryCapacityMiB(nil, 32, defaultVMMemoryOverhead), declared)
	assert.Less(t, memoryCapacityMiB(&ocicore.Shape{}, 32, defaultVMMemoryOverhead), declared)
}

// Fractional GiB is reachable: flexible-shape memory is derived as
// ocpu * defaultPerOcpuInGBs / baselineFactor. Declared memory must never round up, or a shape
// would be modelled as marginally larger than it is.
func TestMemoryCapacity_FractionalGiBRoundsDown(t *testing.T) {
	tests := []struct {
		gbs         float32
		declaredMiB int64
	}{
		{1.2, 1228},   // exactly 1228.8 MiB
		{2.5, 2560},   // MiB-aligned
		{6.4, 6553},   // exactly 6553.6 MiB
		{12.7, 13004}, // exactly 13004.8 MiB
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%gGiB", tt.gbs), func(t *testing.T) {
			// Bare metal reports declared memory directly, so it isolates the rounding.
			assert.Equal(t, tt.declaredMiB, memoryCapacityMiB(bmShape, tt.gbs, defaultVMMemoryOverhead),
				"declared memory must be rounded down, never up")

			assert.LessOrEqual(t, memoryCapacityMiB(vmShape, tt.gbs, defaultVMMemoryOverhead), tt.declaredMiB,
				"a VM shape must never exceed declared memory either")
		})
	}
}
