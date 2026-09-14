/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package instancetype

type ShapeSerial string

const (
	ShapeSerialAmd         = ShapeSerial("AMD")
	ShapeSerialIntel       = ShapeSerial("INTEL")
	ShapeSerialArm         = ShapeSerial("AMPERE")
	ShapeSerialGpu         = ShapeSerial("GPU")
	ShapeSerialHpc         = ShapeSerial("HPC")
	ShapeSerialDenseIO     = ShapeSerial("DENSEIO")
	ShapeSerialUnspecified = ShapeSerial("UNSPECIFIED")
)

type Category string

const (
	CategoryVM = Category("VM")
	CategoryBM = Category("BM")
)

type OciShapeMeta struct {
	Prices               []ShapePriceInfo `json:"prices"`
	PreemptibleShapes    []string         `json:"preemptibleShapes"`
	ComputeClusterShapes []string         `json:"computeClusterShapes"`
	OneVcpuPerOcpuShapes []string         `json:"oneVcpuPerOcpuShapes"`
}

// ShapePriceInfo is the structure for the price calculation.
type ShapePriceInfo struct {
	ShapeName         *string `json:"shapeName"`
	OcpuUnitPrice     float64 `json:"ocpuUnitPrice"`
	MemoryUnitPrice   float64 `json:"memoryUnitPrice"`
	DiskUnitPrice     float64 `json:"diskUnitPrice"`
	MonthlyPriceInUSD float64 `json:"monthlyPriceInUSD,omitempty"`
}
type PreemptibleShapes map[string]string
