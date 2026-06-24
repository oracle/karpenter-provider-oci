/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package instancetype

import (
	"strings"

	ocicore "github.com/oracle/oci-go-sdk/v65/core"
)

const (
	ArchArm = "arm64"
	ArchAmd = "amd64"
)

func IsArmShape(shape ocicore.Shape) bool {
	u := strings.ToUpper(*shape.Shape)

	// TODO: a tribal knowledge to guess architecture from shape name, maybe there is a better way
	return strings.HasPrefix(u, "VM.STANDARD.A") && !strings.Contains(u, "AMD") ||
		strings.HasPrefix(u, "BM.STANDARD.A")
}

func IsGpuShape(shape ocicore.Shape) bool {
	return shape.Gpus != nil && *shape.Gpus > 0
}

func IsAmdGpuShape(shape ocicore.Shape) bool {
	if shape.GpuDescription == nil {
		return false
	}

	u := strings.ToUpper(*shape.GpuDescription)
	return strings.Contains(u, string(ShapeSerialAmd))
}

func IsBmShape(shape string) bool {
	u := strings.ToUpper(shape)

	return strings.HasPrefix(u, "BM")
}

func IsDenseIoShape(shape ocicore.Shape) bool {
	u := strings.ToUpper(*shape.Shape)

	return strings.Contains(u, "DENSEIO") && shape.LocalDisksTotalSizeInGBs != nil &&
		*shape.LocalDisksTotalSizeInGBs > 0
}

func IsFlexShape(shape ocicore.Shape) bool {
	return *shape.IsFlexible
}

func Architecture(shape ocicore.Shape) string {
	if IsArmShape(shape) {
		return ArchArm
	}

	return ArchAmd
}
