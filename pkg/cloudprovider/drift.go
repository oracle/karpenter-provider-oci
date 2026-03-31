/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package cloudprovider

import (
	"context"
	"errors"
	"reflect"

	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instance"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancetype"
	"github.com/samber/lo"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

const (
	CapacityReservationMismatch   = "CapacityReservationMismatch"
	ClusterPlacementGroupMismatch = "ClusterPlacementGroupMismatch"
	LaunchOptionMisMatch          = "LaunchOptionMismatch"
)

type InstanceDesiredState struct {
	InstanceType          *instancetype.OciInstanceType
	CompartmentOcid       string
	Image                 *ocicore.Image
	NodeClass             *v1beta1.OCINodeClass
	CapacityReservationId *string
}

func IsInstanceDrifted(d *InstanceDesiredState, i *ocicore.Instance) (cloudprovider.DriftReason, error) {
	if *i.CompartmentId != d.CompartmentOcid {
		return "instanceCompartmentMismatch", nil
	}

	// although karpenter already check i type availability, here we do additional check for flexible,
	// burstable shape config
	it := d.InstanceType
	if it.SupportShapeConfig {
		instanceTypeDriftedReason, iterr := instancetype.IsInstanceDriftedFromInstanceType(i, it)
		if iterr != nil || instanceTypeDriftedReason != "" {
			return instanceTypeDriftedReason, iterr
		}
	}

	instanceSourceDetail, ok := i.SourceDetails.(ocicore.InstanceSourceViaImageDetails)
	if !ok {
		return "", errors.New("i should have image source detail")
	}

	if *instanceSourceDetail.ImageId != *d.Image.Id {
		return "imageMismatch", nil
	}

	if lo.FromPtr(d.CapacityReservationId) != lo.FromPtr(i.CapacityReservationId) {
		return CapacityReservationMismatch, nil
	}

	// don't need to compare compute cluster as it can never be changed. What if customer change compute cluster

	if len(d.NodeClass.Status.ClusterPlacementGroups) > 0 && i.ClusterPlacementGroupId == nil {
		return ClusterPlacementGroupMismatch, nil
	} else if len(d.NodeClass.Spec.ClusterPlacementGroupConfigs) == 0 && i.ClusterPlacementGroupId != nil {
		return ClusterPlacementGroupMismatch, nil
	} else if i.ClusterPlacementGroupId != nil && !lo.ContainsBy(d.NodeClass.Status.ClusterPlacementGroups,
		func(item v1beta1.ClusterPlacementGroup) bool {
			return item.ClusterPlacementGroupId == *i.ClusterPlacementGroupId
		}) {
		return ClusterPlacementGroupMismatch, nil
	}

	desiredLaunchOptions := instance.BuildLaunchOptions(d.NodeClass.Spec.LaunchOptions)
	if isLaunchOptionMismatch(desiredLaunchOptions, i.LaunchOptions) {
		return LaunchOptionMisMatch, nil
	}
	return "", nil
}

func IsInstanceNetworkDrifted(ctx context.Context, d *InstanceDesiredState,
	vnicAttachments []*ocicore.VnicAttachment,
	getVnicFunc func(context.Context, string) (*ocicore.Vnic, error),
	skipSecondaryVnicsCheck bool) (cloudprovider.DriftReason, error) {

	vasToCheck := lo.Filter(vnicAttachments, func(item *ocicore.VnicAttachment, index int) bool {
		return item.LifecycleState == ocicore.VnicAttachmentLifecycleStateAttached && item.VnicId != nil
	})

	network := d.NodeClass.Status.Network

	primaryVnicSubnet := network.PrimaryVnic
	var primaryVnic *ocicore.Vnic
	for _, va := range vasToCheck {
		if *va.SubnetId == primaryVnicSubnet.Subnet.SubnetId {
			vnic, err := getVnicFunc(ctx, *va.VnicId)
			if err != nil {
				return "", err
			}

			if *vnic.IsPrimary {
				primaryVnic = vnic
				break
			}
		}
	}

	if primaryVnic == nil {
		return "PrimaryVnicMismatch", nil
	}

	if !isVnicNsgIdsMatch(primaryVnicSubnet, primaryVnic) {
		return "PrimaryVnicNsgIdsMismatch", nil
	}

	if !skipSecondaryVnicsCheck && len(network.SecondaryVnics) > 0 {
		reason, err, done := isNetworkSecondVnicDrifted(ctx, vasToCheck, network.SecondaryVnics, getVnicFunc)
		if done {
			return reason, err
		}
	}

	return "", nil
}

func isNetworkSecondVnicDrifted(ctx context.Context, vnicAttachments []*ocicore.VnicAttachment,
	secondVnics []*v1beta1.Vnic,
	getVnicFunc func(context.Context, string) (*ocicore.Vnic, error)) (cloudprovider.DriftReason, error, bool) {

	var existingSecondVnics = make(map[string]*ocicore.Vnic)
	for _, va := range vnicAttachments {
		vnic, err := getVnicFunc(ctx, *va.VnicId)
		if err != nil {
			return "", err, true
		}

		if vnic.IsPrimary == nil || !*vnic.IsPrimary {
			existingSecondVnics[*va.VnicId] = vnic
		}
	}

	// Attached vnic number and expected vnic number should be the same other it is a drift
	if len(existingSecondVnics) != len(secondVnics) {
		return "SecondaryVnicsNumberMismatch", nil, true
	}

	for _, secondVnicSubnet := range secondVnics {
		var secondVnicMatched *ocicore.Vnic
		for _, vnicDetails := range existingSecondVnics {
			if secondVnicSubnet.Subnet.SubnetId == *vnicDetails.SubnetId &&
				isVnicNsgIdsMatch(secondVnicSubnet, vnicDetails) {
				secondVnicMatched = vnicDetails
				break
			}
		}
		if secondVnicMatched == nil {
			return "SecondaryVnicNsgIdsMismatch", nil, true
		} else {
			// Delete the matched vnic from map, this is because multiple secondary vnics can use same subnet and nsgId,
			// so once we found a match we need to remove it from map
			delete(existingSecondVnics, *secondVnicMatched.Id)
		}
	}
	return "", nil, false
}

func isVnicNsgIdsMatch(vnicSubnet *v1beta1.Vnic, vnic *ocicore.Vnic) bool {
	if len(vnicSubnet.NetworkSecurityGroups) > 0 {
		for _, dNsg := range vnicSubnet.NetworkSecurityGroups {
			found := false
			for _, aNsgId := range vnic.NsgIds {
				if aNsgId == dNsg.NetworkSecurityGroupId {
					found = true
					break
				}
			}

			if !found {
				return false
			}
		}
	}

	return true
}

func IsInstanceBootVolumeDrifted(ctx context.Context, d *InstanceDesiredState,
	bootVolumeAttachments []*ocicore.BootVolumeAttachment,
	getBootVolumeFunc func(context.Context, string) (*ocicore.BootVolume, error)) (cloudprovider.DriftReason, error) {
	if len(bootVolumeAttachments) == 0 {
		return "BootVolumeMismatch", nil
	}

	firstBootVolumeAtt := lo.MinBy(bootVolumeAttachments, func(i, j *ocicore.BootVolumeAttachment) bool {
		return i.TimeCreated.Before(j.TimeCreated.Time)
	})

	bootVolumeCfg := d.NodeClass.Spec.VolumeConfig.BootVolumeConfig
	if bootVolumeCfg.PvEncryptionInTransit != nil &&
		!reflect.DeepEqual(bootVolumeCfg.PvEncryptionInTransit, firstBootVolumeAtt.IsPvEncryptionInTransitEnabled) {
		return "PvEncryptionInTransitionMismatch", nil
	}

	bootVolume, err := getBootVolumeFunc(ctx, *firstBootVolumeAtt.BootVolumeId)
	if err != nil {
		return "", err
	}

	// TODO: how does oci-growfs impact volume size.
	if bootVolumeCfg.SizeInGBs != nil && *bootVolume.SizeInGBs < *bootVolumeCfg.SizeInGBs {
		return "BootVolumeSizeMismatch", nil
	}

	if bootVolumeCfg.KmsKeyConfig != nil && bootVolumeCfg.KmsKeyConfig.KmsKeyId != nil &&
		(bootVolume.KmsKeyId == nil || *bootVolumeCfg.KmsKeyConfig.KmsKeyId != *bootVolume.KmsKeyId) {
		return "kmsKeyMismatch", nil
	}

	if bootVolumeCfg.VpusPerGB != nil &&
		(bootVolume.VpusPerGB == nil || *bootVolume.VpusPerGB != *bootVolumeCfg.VpusPerGB) {
		return "VpusPerGbMismatch", nil
	}

	return "", nil
}

func isLaunchOptionMismatch(desired *ocicore.LaunchOptions, actual *ocicore.LaunchOptions) bool {
	if desired == nil {
		return false
	}

	if desired.BootVolumeType != "" && desired.BootVolumeType != actual.BootVolumeType {
		return true
	}
	if desired.Firmware != "" && desired.Firmware != actual.Firmware {
		return true
	}
	if desired.NetworkType != "" && desired.NetworkType != actual.NetworkType {
		return true
	}
	if desired.RemoteDataVolumeType != "" && desired.RemoteDataVolumeType != actual.RemoteDataVolumeType {
		return true
	}
	if desired.IsConsistentVolumeNamingEnabled != nil {
		if actual.IsConsistentVolumeNamingEnabled == nil {
			return true
		}
		return *desired.IsConsistentVolumeNamingEnabled != *actual.IsConsistentVolumeNamingEnabled
	}

	// IsPvEncryptionInTransitEnabled is deprecated so not checking it here
	return false
}

func AreStaticFieldsDrifted(ctx context.Context, nodeClaim *corev1.NodeClaim,
	nodeClass *v1beta1.OCINodeClass) cloudprovider.DriftReason {

	nodeClassHash, foundNodeClassHash := nodeClass.Annotations[v1beta1.NodeClassHash]
	nodeClassHashVersion, foundNodeClassHashVersion := nodeClass.Annotations[v1beta1.NodeClassHashVersion]
	nodeClaimHash, foundNodeClaimHash := nodeClaim.Annotations[v1beta1.NodeClassHash]
	nodeClaimHashVersion, foundNodeClaimHashVersion := nodeClaim.Annotations[v1beta1.NodeClassHashVersion]

	if !foundNodeClassHash || !foundNodeClaimHash || !foundNodeClassHashVersion || !foundNodeClaimHashVersion {
		return ""
	}
	// validate that the hash version for the OCINodeClass is the same as the NodeClaim before evaluating for static drift
	if nodeClassHashVersion != nodeClaimHashVersion {
		log.FromContext(ctx).Info("NodeClassVersionDrift",
			"nodeClaim", nodeClaim.Name,
			"nodeClassHashVersion", nodeClassHashVersion,
			"nodeClaimHashVersion", nodeClaimHashVersion)
		return "NodeClassVersionDrift"
	}

	if nodeClassHash != nodeClaimHash {
		log.FromContext(ctx).Info("NodeClassStaticFieldDrift",
			"nodeClaim", nodeClaim.Name,
			"nodeClassHash", nodeClassHash,
			"nodeClaimHash", nodeClaimHash)
		return "NodeClassStaticFieldDrift"
	}

	return ""
}
