/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package instance

import (
	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
)

// genericLaunchPlatformConfig preserves the catalogue's metadata-derived type,
// including types added after the pinned SDK, without duplicating its subtypes.
type genericLaunchPlatformConfig struct {
	Type ocicore.ShapePlatformConfigOptionsTypeEnum `json:"type"`

	IsSecureBootEnabled              *bool   `mandatory:"false" json:"isSecureBootEnabled,omitempty"`
	IsTrustedPlatformModuleEnabled   *bool   `mandatory:"false" json:"isTrustedPlatformModuleEnabled,omitempty"`
	IsMeasuredBootEnabled            *bool   `mandatory:"false" json:"isMeasuredBootEnabled,omitempty"`
	IsMemoryEncryptionEnabled        *bool   `mandatory:"false" json:"isMemoryEncryptionEnabled,omitempty"`
	IsSymmetricMultiThreadingEnabled *bool   `mandatory:"false" json:"isSymmetricMultiThreadingEnabled,omitempty"`
	IsAccessControlServiceEnabled    *bool   `mandatory:"false" json:"isAccessControlServiceEnabled,omitempty"`
	AreVirtualInstructionsEnabled    *bool   `mandatory:"false" json:"areVirtualInstructionsEnabled,omitempty"`
	IOMMUEnabled                     *bool   `mandatory:"false" json:"isInputOutputMemoryManagementUnitEnabled,omitempty"`
	NumaNodesPerSocket               *string `mandatory:"false" json:"numaNodesPerSocket,omitempty"`
	PercentageOfCoresEnabled         *int    `mandatory:"false" json:"percentageOfCoresEnabled,omitempty"`
}

var _ ocicore.LaunchInstancePlatformConfig = (*genericLaunchPlatformConfig)(nil)

func (p *genericLaunchPlatformConfig) GetIsSecureBootEnabled() *bool {
	return p.IsSecureBootEnabled
}

func (p *genericLaunchPlatformConfig) GetIsTrustedPlatformModuleEnabled() *bool {
	return p.IsTrustedPlatformModuleEnabled
}

func (p *genericLaunchPlatformConfig) GetIsMeasuredBootEnabled() *bool {
	return p.IsMeasuredBootEnabled
}

func (p *genericLaunchPlatformConfig) GetIsMemoryEncryptionEnabled() *bool {
	return p.IsMemoryEncryptionEnabled
}

func buildPlatformConfig(
	request *v1beta1.PlatformConfig, platformType ocicore.ShapePlatformConfigOptionsTypeEnum,
) ocicore.LaunchInstancePlatformConfig {
	if request.IsEmpty() {
		return nil
	}
	config := request.DeepCopy()
	return &genericLaunchPlatformConfig{
		Type:                             platformType,
		IsSecureBootEnabled:              config.SecureBootEnabled,
		IsTrustedPlatformModuleEnabled:   config.TrustedPlatformModuleEnabled,
		IsMeasuredBootEnabled:            config.MeasuredBootEnabled,
		IsMemoryEncryptionEnabled:        config.MemoryEncryptionEnabled,
		IsSymmetricMultiThreadingEnabled: config.SymmetricMultiThreadingEnabled,
		IsAccessControlServiceEnabled:    config.AccessControlServiceEnabled,
		AreVirtualInstructionsEnabled:    config.VirtualInstructionsEnabled,
		IOMMUEnabled:                     config.InputOutputMemoryManagementUnitEnabled,
		NumaNodesPerSocket:               config.NumaNodesPerSocket,
		PercentageOfCoresEnabled:         config.PercentageOfCoresEnabled,
	}
}
