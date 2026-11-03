/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package instancetype

import (
	"errors"
	"strings"

	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/utils/resources"
)

// OciInstanceType is our internal instance type implementation with enriched information.
type OciInstanceType struct {
	cloudprovider.InstanceType

	// extra information pass around within provider, be noticed these information only make senses in
	// a specific node class context
	SupportShapeConfig      bool
	Ocpu                    *float32
	MemoryInGbs             *float32
	BaselineOcpuUtilization *ociv1beta1.BaselineOcpuUtilization
	Shape                   string
	PlatformType            ocicore.ShapePlatformConfigOptionsTypeEnum
}

func (it *OciInstanceType) CopyAndUpdateOfferings(newOfferings cloudprovider.Offerings) *OciInstanceType {
	return &OciInstanceType{
		InstanceType: cloudprovider.InstanceType{
			Name:         it.Name,
			Requirements: it.Requirements,
			Offerings:    newOfferings,
			Capacity:     it.Capacity,
			Overhead:     it.Overhead,
		},
		SupportShapeConfig:      it.SupportShapeConfig,
		Ocpu:                    it.Ocpu,
		MemoryInGbs:             it.MemoryInGbs,
		BaselineOcpuUtilization: it.BaselineOcpuUtilization,
		Shape:                   it.Shape,
		PlatformType:            it.PlatformType,
	}
}

func (it *OciInstanceType) Print() string {
	var sb strings.Builder
	sb.WriteString(it.InstanceType.Name)
	sb.WriteString(":")
	capacityTypes := lo.FlatMap(it.Offerings, func(offering *cloudprovider.Offering, index int) []string {
		if value, found := offering.Requirements[corev1.CapacityTypeLabelKey]; found {
			return value.Values()
		}
		return []string{}
	})
	sb.WriteString(strings.Join(capacityTypes, ","))

	return sb.String()
}

func DecorateNodeClaimByInstanceType(nodeClaim *corev1.NodeClaim, ociInstanceType *OciInstanceType) {
	if ociInstanceType != nil {
		for key, req := range ociInstanceType.InstanceType.Requirements {
			if req.Len() == 1 {
				nodeClaim.Labels[key] = req.Values()[0]
			}
		}

		resourceFilter := func(n v1.ResourceName, v resource.Quantity) bool {
			return !resources.IsZero(v)
		}
		nodeClaim.Status.Capacity = lo.PickBy(ociInstanceType.InstanceType.Capacity, resourceFilter)
		nodeClaim.Status.Allocatable = lo.PickBy(ociInstanceType.InstanceType.Allocatable(), resourceFilter)
	}
}

func IsInstanceDriftedFromInstanceType(i *ocicore.Instance, it *OciInstanceType) (cloudprovider.DriftReason, error) {
	if i == nil || it == nil {
		return "", errors.New("invalid input")
	}

	if *i.Shape != it.Shape {
		return "shapeMismatch", nil
	}

	if it.SupportShapeConfig {
		if i.ShapeConfig == nil {
			return "shapeConfigMismatch", nil
		}

		iShapeConfig := i.ShapeConfig
		if it.Ocpu != nil && *it.Ocpu != *iShapeConfig.Ocpus {
			return "ocpuMismatch", nil
		}

		if it.MemoryInGbs != nil && *it.MemoryInGbs != *iShapeConfig.MemoryInGBs {
			return "memoryInGbsMismatch", nil
		}

		if it.BaselineOcpuUtilization != nil &&
			*it.BaselineOcpuUtilization != FromInstanceCpuBaseline(iShapeConfig.BaselineOcpuUtilization) {
			return "cpuBaselineUtilizationMismatch", nil
		}
	}

	return "", nil
}

func IsBurstableShape(it *OciInstanceType) bool {
	if it.BaselineOcpuUtilization == nil || *it.BaselineOcpuUtilization == ociv1beta1.BASELINE_1_1 {
		return false
	}
	return true
}
