/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package utils

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/mitchellh/hashstructure/v2"
	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/samber/lo"
)

func HashFor(any interface{}) (string, error) {
	// Marshal the struct to JSON
	jsonData, err := json.Marshal(any)
	if err != nil {
		return "", err
	}

	return Digest(jsonData), nil
}

func HashForMultiObjects(anys []interface{}) (string, error) {
	var bytes []byte

	for _, inf := range anys {
		if inf == nil {
			continue
		}

		jsonData, err := json.Marshal(inf)
		if err != nil {
			return "", err
		}

		bytes = append(bytes, jsonData...)
	}

	return Digest(bytes), nil
}

func Digest(b []byte) string {
	// Generate SHA256 hash
	hash := sha256.Sum256(b)

	// Convert the hash to a hex string
	return fmt.Sprintf("%x", hash)
}

func HashNodeClassSpec(nodeClass *v1beta1.OCINodeClass) string {
	var infs []interface{}

	originalSpec := nodeClass.Spec.DeepCopy()
	ncSpecToSerialize := createStaticFieldsClone(originalSpec)

	infs = append(infs, ncSpecToSerialize)

	return KarpenterHash(infs)
}

func createStaticFieldsClone(originalSpec *v1beta1.OCINodeClassSpec) *v1beta1.OCINodeClassSpec {
	ncSpecToSerialize := newEmptyOCINodeClassSpec()

	// Set ShapeConfigs static fields
	for _, originalShapeConfig := range originalSpec.ShapeConfigs {
		newShapeConfig := new(v1beta1.ShapeConfig)
		newShapeConfig.BaselineOcpuUtilization = originalShapeConfig.BaselineOcpuUtilization
		newShapeConfig.Ocpus = originalShapeConfig.Ocpus
		newShapeConfig.MemoryInGbs = originalShapeConfig.MemoryInGbs
		ncSpecToSerialize.ShapeConfigs = append(ncSpecToSerialize.ShapeConfigs, newShapeConfig)
	}

	// Only set network static fields, dynamic fields is drift checked by IsInstanceNetworkDrifted method
	ncSpecToSerialize.NetworkConfig.PrimaryVnicConfig =
		copySimpleVnicConfigStaticFields(originalSpec.NetworkConfig.PrimaryVnicConfig)
	for _, originalSecondaryVnicConfig := range originalSpec.NetworkConfig.SecondaryVnicConfigs {
		ncSpecToSerialize.NetworkConfig.SecondaryVnicConfigs = append(ncSpecToSerialize.NetworkConfig.SecondaryVnicConfigs,
			copySecondaryVnicConfigStaticFields(originalSecondaryVnicConfig))
	}

	// Set KubeletConfig static fields
	if originalSpec.KubeletConfig != nil {
		ncSpecToSerialize.KubeletConfig = new(v1beta1.KubeletConfiguration)
		ncSpecToSerialize.KubeletConfig.ClusterDNS = originalSpec.KubeletConfig.ClusterDNS
		ncSpecToSerialize.KubeletConfig.ExtraArgs = originalSpec.KubeletConfig.ExtraArgs
		ncSpecToSerialize.KubeletConfig.NodeLabels = originalSpec.KubeletConfig.NodeLabels
		ncSpecToSerialize.KubeletConfig.MaxPods = originalSpec.KubeletConfig.MaxPods
		ncSpecToSerialize.KubeletConfig.PodsPerCore = originalSpec.KubeletConfig.PodsPerCore
		ncSpecToSerialize.KubeletConfig.SystemReserved = originalSpec.KubeletConfig.SystemReserved
		ncSpecToSerialize.KubeletConfig.KubeReserved = originalSpec.KubeletConfig.KubeReserved
		ncSpecToSerialize.KubeletConfig.EvictionHard = originalSpec.KubeletConfig.EvictionHard
		ncSpecToSerialize.KubeletConfig.EvictionSoft = originalSpec.KubeletConfig.EvictionSoft
		ncSpecToSerialize.KubeletConfig.EvictionSoftGracePeriod = originalSpec.KubeletConfig.EvictionSoftGracePeriod
		ncSpecToSerialize.KubeletConfig.EvictionMaxPodGracePeriod = originalSpec.KubeletConfig.EvictionMaxPodGracePeriod
		ncSpecToSerialize.KubeletConfig.ImageGCHighThresholdPercent = originalSpec.KubeletConfig.ImageGCHighThresholdPercent
		ncSpecToSerialize.KubeletConfig.ImageGCLowThresholdPercent = originalSpec.KubeletConfig.ImageGCLowThresholdPercent
	}

	ncSpecToSerialize.NodeCompartmentId = originalSpec.NodeCompartmentId
	ncSpecToSerialize.Metadata = originalSpec.Metadata
	ncSpecToSerialize.FreeformTags = originalSpec.FreeformTags
	ncSpecToSerialize.DefinedTags = originalSpec.DefinedTags
	ncSpecToSerialize.PreBootstrapInitScript = originalSpec.PreBootstrapInitScript
	ncSpecToSerialize.PostBootstrapInitScript = originalSpec.PostBootstrapInitScript
	ncSpecToSerialize.SshAuthorizedKeys = originalSpec.SshAuthorizedKeys

	// Below is covered by IsInstanceDrifted
	// ncSpecToSerialize.ClusterPlacementGroupConfigs
	// ncSpecToSerialize.CapacityReservationConfigs
	// ncSpecToSerialize.ComputeClusterConfig
	// ncSpecToSerialize.LaunchOptions

	// Below is covered by IsInstanceBootVolumeDrifted
	// ncSpecToSerialize.VolumeConfig

	return ncSpecToSerialize
}

func newEmptyOCINodeClassSpec() *v1beta1.OCINodeClassSpec {
	ncSpecToSerialize := new(v1beta1.OCINodeClassSpec)
	ncSpecToSerialize.ShapeConfigs = make([]*v1beta1.ShapeConfig, 0)
	ncSpecToSerialize.NetworkConfig = &v1beta1.NetworkConfig{
		PrimaryVnicConfig:    &v1beta1.SimpleVnicConfig{},
		SecondaryVnicConfigs: make([]*v1beta1.SecondaryVnicConfig, 0),
	}
	return ncSpecToSerialize
}

func copySimpleVnicConfigStaticFields(originalVnicConfig *v1beta1.SimpleVnicConfig) *v1beta1.SimpleVnicConfig {
	newVnicConfig := &v1beta1.SimpleVnicConfig{}
	newVnicConfig.AssignIpV6Ip = originalVnicConfig.AssignIpV6Ip
	newVnicConfig.AssignPublicIp = originalVnicConfig.AssignPublicIp
	newVnicConfig.Ipv6AddressIpv6SubnetCidrPairDetails = originalVnicConfig.Ipv6AddressIpv6SubnetCidrPairDetails
	newVnicConfig.SkipSourceDestCheck = originalVnicConfig.SkipSourceDestCheck
	newVnicConfig.SecurityAttributes = originalVnicConfig.SecurityAttributes

	return newVnicConfig
}

func copySecondaryVnicConfigStaticFields(
	originalSecondaryVnicConfig *v1beta1.SecondaryVnicConfig) *v1beta1.SecondaryVnicConfig {
	newSecondaryVnicConfig := &v1beta1.SecondaryVnicConfig{
		SimpleVnicConfig:    *copySimpleVnicConfigStaticFields(&originalSecondaryVnicConfig.SimpleVnicConfig),
		ApplicationResource: originalSecondaryVnicConfig.ApplicationResource,
		IpCount:             originalSecondaryVnicConfig.IpCount,
		NicIndex:            originalSecondaryVnicConfig.NicIndex,
	}

	return newSecondaryVnicConfig
}

// KarpenterHash This method is using the same hash algorithm from Karpenter code
func KarpenterHash(data []interface{}) string {
	hash := lo.Must(hashstructure.Hash(data,
		hashstructure.FormatV2,
		&hashstructure.HashOptions{
			SlicesAsSets:    true,
			IgnoreZeroValue: true,
			ZeroNil:         true,
		}))

	return fmt.Sprint(hash)
}
