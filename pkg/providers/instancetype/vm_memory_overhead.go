/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package instancetype

import (
	"math"

	ocicore "github.com/oracle/oci-go-sdk/v65/core"
)

// Default VM memory overhead. These reserve at least as much as every measurement reported on
// oracle/karpenter-provider-oci#7 (A1/E4/E5/Standard3, 8-128 GB), so the capacity derived from
// them is a lower bound on what a node really has. They are the single source of truth for the
// flag defaults in pkg/operator/options and the Helm chart; changing them changes what ships, so
// TestMemoryCapacity_NeverOverEstimates asserts the safety property against these exact values.
//
// They are not the tightest fit to those measurements. The tightest lower bound clears the
// closest point by only ~9 MiB, which is too little to survive a different image build or a
// family we have not measured; these clear it by ~71 MiB and still reserve under 3 GiB on a
// 128 GB shape, against 9.6 GiB for a flat 7.5%.
const (
	DefaultVMMemoryOverheadBaseMiB  = 600
	DefaultVMMemoryOverheadPerGBMiB = 19
	DefaultVMMemoryOverheadPercent  = 0
)

// VMMemoryOverheadConfig models the memory OCI reserves below the guest. A shape advertised as
// N GB never presents a full N GiB of MemTotal to Linux, so modelling capacity as the declared
// figure over-estimates what a node can hold. Karpenter has to make that estimate before the node
// exists; when it is too high, the launched node cannot fit the pod that triggered it and — with
// no feedback loop — the provisioning loop repeats indefinitely.
//
// The two failure modes are therefore wildly asymmetric: over-estimating costs an unbounded launch
// loop, under-estimating costs a little wasted memory. These values are chosen to under-estimate.
// They are a bootstrap guess only, and are expected to be superseded per shape+image by capacity
// discovery once a launched node reports its real MemTotal.
type VMMemoryOverheadConfig struct {
	// BaseMiB is the fixed part of the overhead, in MiB.
	BaseMiB float64
	// PerGBMiB is the part that scales with declared memory, in MiB per GiB.
	PerGBMiB float64
	// Percent expresses the overhead as a fraction of declared memory (0.075 == 7.5%) instead of
	// as a fixed and per-GiB amount. Zero disables it. When set, the larger of the two forms wins.
	Percent float64
}

// OverheadMiB returns the memory overhead, in MiB, to subtract from a shape declaring gbs GiB.
//
// The fixed-plus-proportional form fits the observed behaviour far better than a flat percentage:
// most of the reservation is fixed structure, with a small per-GiB page-table cost. A flat
// percentage large enough to cover small shapes over-reserves badly on large ones.
//
// The percentage form is offered as an alternative way of expressing the same overhead, and is
// combined with max() so that enabling it can only ever make the estimate more conservative.
func (c VMMemoryOverheadConfig) OverheadMiB(gbs float64) int64 {
	fixed := c.BaseMiB + c.PerGBMiB*gbs
	percent := c.Percent * gbs * 1024

	overhead := math.Ceil(math.Max(math.Max(fixed, percent), 0))

	// Options validation rejects non-finite configuration, but converting a NaN or out-of-range
	// float to int64 is implementation-defined in Go, so refuse to produce a garbage overhead
	// here too. Falling back to the declared size means capacity clamps to its floor: useless,
	// but never an over-estimate.
	// Compared with >=, not >: float64(math.MaxInt64) rounds up to 2^63, which is one past the
	// largest representable int64, so a > test would let exactly that value through to overflow.
	if math.IsNaN(overhead) || overhead >= math.MaxInt64 {
		return math.MaxInt64
	}

	return int64(overhead)
}

// memoryCapacityMiB returns the memory, in MiB, to report as a shape's capacity.
//
// The overhead is applied to capacity rather than to InstanceType.Overhead because it is not
// reserved memory in the kubelet's sense — it is memory the guest never sees at all. Core's
// InstanceTypeOverhead has slots only for KubeReserved, SystemReserved and EvictionThreshold,
// none of which describes memory the guest never receives.
//
// Bare metal shapes keep their declared memory. The measurements behind the default, and the
// mechanism they describe, are a hypervisor artefact; a BM instance has no hypervisor beneath it,
// so subtracting a VM-derived figure there would shrink advertised capacity with nothing to
// support it.
func memoryCapacityMiB(shape *ocicore.Shape, gbs float32, vmMemoryOverhead VMMemoryOverheadConfig) int64 {
	// Floor, not round: flexible shapes can declare a fractional number of GiB (memory is derived
	// as ocpu * defaultPerOcpu / baselineFactor), and rounding 1.2 GiB up to 1229 MiB would report
	// a shape as marginally larger than declared - the one direction this whole calculation exists
	// to avoid. Whole-GiB shapes are unaffected.
	declaredMiB := int64(math.Floor(float64(gbs) * 1024))

	if shape != nil && shape.Shape != nil && IsBmShape(*shape.Shape) {
		return declaredMiB
	}

	capacityMiB := declaredMiB - vmMemoryOverhead.OverheadMiB(float64(gbs))
	if capacityMiB < 1 {
		// Guard shapes small enough for the overhead to swallow the whole declaration. Such a
		// shape cannot run a kubelet anyway, but capacity must stay positive.
		return 1
	}

	return capacityMiB
}
