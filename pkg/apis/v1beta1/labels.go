/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package v1beta1

import (
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	coreprovider "sigs.k8s.io/karpenter/pkg/cloudprovider"
)

func init() {
	coreprovider.ReservationIDLabel = ReservationIDLabel
	corev1.WellKnownLabels = corev1.WellKnownLabels.Insert(
		OciBmShape,
		OciGpuShape,
		OciDenseIoShape,
		OciFaultDomain,
		OciInstanceShape,
		ReservationIDLabel,
		OciFlexShape,
	)
}

var (
	Group                = "oci.oraclecloud.com"
	NodeClassHash        = Group + "/nodeclass-hash"
	NodeClassHashVersion = Group + "/nodeclass-hash-version"
	NodeClass            = Group + "/ocinodeclass"
	OciGpuShape          = Group + "/gpu-shape"
	OciBmShape           = Group + "/baremetal-shape"
	OciDenseIoShape      = Group + "/denseio-shape"
	OciInstanceShape     = Group + "/instance-shape"
	OciFlexShape         = Group + "/flex-shape"
	OciFaultDomain       = Group + "/fault-domain"
	ReservationIDLabel   = Group + "/capacity-reservation-id"
)

// We need to bump the OCINodeClassHashVersion when we make an update to the OCINodeClass CRD under these conditions:
// 1. A field changes its default value for an existing field that is already hashed
// 2. A field is added to the hash calculation with an already-set value
// 3. A field is removed from the hash calculations
const OCINodeClassHashVersion = "v1"
