/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package npn

import (
	"context"
	"errors"
	"fmt"
	"strings"

	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	npnv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/npn/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/utils"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	NPN_CUSTOM_RESOURCE_GROUP_NAME  = "oci.oraclecloud.com"
	NPN_CUSTOM_RESOURCE_KIND        = "NativePodNetwork"
	NPN_CUSTOM_RESOURCE_PLURAL_NAME = "nativepodnetworks"
	NPN_CUSTOM_RESOURCE_V1_BETA1    = "v1beta1"
	DEFAULT_MAX_PODS_PER_NODE       = 31
)

type Provider interface {
	CreateNpnCustomObject(ctx context.Context,
		instance *ocicore.Instance,
		nodeClass *ociv1beta1.OCINodeClass,
		networkResolveResult *network.NetworkResolveResult) (*npnv1beta1.NativePodNetwork, error)

	NpnCluster() bool
}

type DefaultProvider struct {
	npnCluster bool
	ipFamilies []network.IpFamily
}

func NewProvider(ctx context.Context, npnCluster bool, ipFamilies []network.IpFamily) (*DefaultProvider, error) {
	p := &DefaultProvider{
		npnCluster: npnCluster,
		ipFamilies: ipFamilies,
	}

	return p, nil
}

func (p *DefaultProvider) createNpnCustomObject(ctx context.Context,
	name string,
	instanceId string,
	networkConfig *ociv1beta1.NetworkConfig,
	networkResolveResult *network.NetworkResolveResult,
	kubeletConfig *ociv1beta1.KubeletConfiguration) (*npnv1beta1.NativePodNetwork, error) {
	if networkConfig == nil {
		return nil, errors.New("NativePodNetworkConfig can't be nil")
	}

	if len(networkConfig.SecondaryVnicConfigs) == 0 {
		return nil, errors.New("need to specify list of SecondaryVnicConfigs for NativePodNetworking")
	}

	log.FromContext(ctx).Info("Creating NpnCustomObject values",
		"Name", name,
		"InstanceId", instanceId)

	secondaryVnicSpec, err := p.constructSecondaryVnicsNpnSpec(ctx,
		instanceId,
		networkConfig.SecondaryVnicConfigs,
		networkResolveResult,
		kubeletConfig)
	if err != nil {
		return nil, err
	}

	npnSpec := *secondaryVnicSpec

	npnCustomObject := &npnv1beta1.NativePodNetwork{
		TypeMeta: metav1.TypeMeta{
			APIVersion: NPN_CUSTOM_RESOURCE_GROUP_NAME + "/" + NPN_CUSTOM_RESOURCE_V1_BETA1,
			Kind:       NPN_CUSTOM_RESOURCE_KIND,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: npnSpec,
	}

	return npnCustomObject, nil
}

func (p *DefaultProvider) constructSecondaryVnicsNpnSpec(ctx context.Context,
	instanceId string,
	secondaryVnicConfigs []*ociv1beta1.SecondaryVnicConfig,
	networkResolveResult *network.NetworkResolveResult,
	kubeletConfig *ociv1beta1.KubeletConfiguration) (*npnv1beta1.NativePodNetworkSpec, error) {

	if len(secondaryVnicConfigs) == 0 {
		return nil, errors.New("secondary vnics configs are nil or empty")
	}

	if networkResolveResult == nil || len(networkResolveResult.OtherVnicSubnets) == 0 {
		return nil, errors.New("secondary vnics networks are not resolved")
	}

	defaultSecondaryVnicIpCount := network.GetDefaultSecondaryVnicIPCount(p.ipFamilies)
	maxPods := int64(utils.GetKubeletMaxPods(kubeletConfig, secondaryVnicConfigs, defaultSecondaryVnicIpCount))

	var secondaryVnicsSpec []npnv1beta1.SecondaryVnic
	for index, secondaryVnicConfig := range secondaryVnicConfigs {
		secondaryVnicNetworkRevolveResult := networkResolveResult.OtherVnicSubnets[index]

		if secondaryVnicNetworkRevolveResult != nil {
			var applicationResources []string
			if secondaryVnicConfig.ApplicationResource != nil {
				applicationResources = []string{*secondaryVnicConfig.ApplicationResource}
			}

			ipv6IpCidrPairs := ToNpnIpvAddressCidrPair(secondaryVnicConfig.Ipv6AddressIpv6SubnetCidrPairDetails)

			var nsgIds []string
			if len(secondaryVnicNetworkRevolveResult.NetworkSecurityGroups) > 0 {
				for _, value := range secondaryVnicNetworkRevolveResult.NetworkSecurityGroups {
					if value != nil {
						nsgIds = append(nsgIds, *value.Id)
					}
				}
			}

			securityAttributes := MapValueStringToMapValueInterface(secondaryVnicConfig.SecurityAttributes)

			displayName := fmt.Sprintf("SVnic %d", index)
			if secondaryVnicConfig.VnicDisplayName != nil {
				displayName = *secondaryVnicConfig.VnicDisplayName
			}

			skipSourceDestCheck := lo.ToPtr(true)
			if secondaryVnicConfig.SkipSourceDestCheck != nil {
				skipSourceDestCheck = secondaryVnicConfig.SkipSourceDestCheck
			}

			ipCount := defaultSecondaryVnicIpCount
			if secondaryVnicConfig.IpCount != nil {
				ipCount = *secondaryVnicConfig.IpCount
			}

			var secondaryVnicSpec = &npnv1beta1.SecondaryVnic{
				DisplayName: displayName,
				NicIndex:    secondaryVnicConfig.NicIndex,
				CreateVnicDetails: npnv1beta1.CreateVnicDetails{
					SubnetId:                             secondaryVnicNetworkRevolveResult.Subnet.Id,
					AssignIpv6Ip:                         secondaryVnicConfig.AssignIpV6Ip,
					AssignPublicIp:                       secondaryVnicConfig.AssignPublicIp,
					DisplayName:                          &displayName,
					IpCount:                              &ipCount,
					ApplicationResources:                 applicationResources,
					SecurityAttributes:                   securityAttributes,
					Ipv6AddressIpv6SubnetCidrPairDetails: ipv6IpCidrPairs,
					NsgIds:                               nsgIds,
					SkipSourceDestCheck:                  skipSourceDestCheck,
				},
			}

			secondaryVnicsSpec = append(secondaryVnicsSpec, *secondaryVnicSpec)
		}
	}

	// Populate flat fields for OKE NPN controller compatibility.
	// The OKE NPN controller requires podSubnetIds, networkSecurityGroupIds,
	// and ipFamilies at the top level to complete reconciliation.
	podSubnetIds := lo.Uniq(lo.FilterMap(secondaryVnicsSpec, func(sv npnv1beta1.SecondaryVnic, _ int) (string, bool) {
		if sv.CreateVnicDetails.SubnetId != nil {
			return *sv.CreateVnicDetails.SubnetId, true
		}
		return "", false
	}))

	var allNsgIds []string
	for _, sv := range secondaryVnicsSpec {
		allNsgIds = append(allNsgIds, sv.CreateVnicDetails.NsgIds...)
	}
	allNsgIds = lo.Uniq(allNsgIds)

	ipFamilyStrings := lo.Map(p.ipFamilies, func(f network.IpFamily, _ int) string {
		return string(f)
	})

	log.FromContext(ctx).Info("Creating Secondary Npn Spec values",
		"MaxPodCount", maxPods,
		"SecondaryVnics", secondaryVnicsSpec)

	npnSpec := &npnv1beta1.NativePodNetworkSpec{
		ID:                      instanceId,
		MaxPodCount:             maxPods,
		SecondaryVnics:          secondaryVnicsSpec,
		PodSubnetIDs:            podSubnetIds,
		NetworkSecurityGroupIDs: allNsgIds,
		IpFamilies:              ipFamilyStrings,
	}
	return npnSpec, nil
}

func (p *DefaultProvider) NpnCluster() bool {
	return p.npnCluster
}

func (p *DefaultProvider) CreateNpnCustomObject(ctx context.Context,
	instance *ocicore.Instance,
	nodeClass *ociv1beta1.OCINodeClass,
	networkResolveResult *network.NetworkResolveResult) (*npnv1beta1.NativePodNetwork, error) {

	if instance == nil || instance.Id == nil {
		return nil, errors.New("empty instance OCID")
	}

	if nodeClass == nil || nodeClass.Spec.NetworkConfig == nil {
		return nil, errors.New("network config is nil")
	}

	instanceId := *instance.Id
	name := p.ocidSuffix(instanceId)
	networkConfig := nodeClass.Spec.NetworkConfig

	return p.createNpnCustomObject(ctx, name, instanceId, networkConfig, networkResolveResult,
		nodeClass.Spec.KubeletConfig)
}

func (p *DefaultProvider) ocidSuffix(ocid string) string {
	tokens := strings.Split(ocid, ".")
	return tokens[len(tokens)-1]
}
