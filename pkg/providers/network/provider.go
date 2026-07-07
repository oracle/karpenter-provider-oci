/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package network

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/cache"
	"github.com/oracle/karpenter-provider-oci/pkg/oci"
	"github.com/oracle/karpenter-provider-oci/pkg/utils"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"go.uber.org/multierr"
)

type Provider interface {
	GetVnic(ctx context.Context, vnicOcid string) (*ocicore.Vnic, error)
	GetVnicCached(ctx context.Context, vnicOcid string) (*ocicore.Vnic, error)
	ResolveNetworkConfig(ctx context.Context,
		networkCfg *v1beta1.NetworkConfig) (*NetworkResolveResult, error)
}

var (
	NoNetworkConfigError     = errors.New("networkConfig is required")
	NoPrimaryVnicConfigError = errors.New("networkConfig.primaryVnicConfig is required")

	InvalidSubnetConfigError = errors.New("define either ocid or subnet selector")
	InvalidNsgConfigError    = errors.New("define either networkSecurityGroup ids or networkSecurityGroup selectors")
	errVnicCacheRequired     = errors.New("VNIC cache is required")

	AdSubnetError                      = errors.New("ad local subnet is not supported")
	NoSecondaryVnicConfigError         = errors.New("no secondaryVnicConfigs found")
	SecondaryVnicConfigNotAllowedError = errors.New("secondaryVnicConfigs is not allowed")
	NpnAndOtherVnicCoExistsError       = errors.New("nativePodNetwork and otherVnicConfigs cannot be defined together")
	NoSubnetMatchSelector              = errors.New("no subnet selector")
	MoreThanOneSubnetMatchSelector     = errors.New("more than one subnet match selector")
	SubnetAndNsgNotInSameVcn           = errors.New("subnet and networkSecurityGroup are not in a same vcn")

	NoCidrBlock     = errors.New("no cidr block for ipv4")
	NoIPv6CidrBlock = errors.New("no cidr block for ipv6")
	CacheTTL        = time.Hour
)

type DefaultProvider struct {
	clusterVcnCompartmentId string
	npnCluster              bool
	ipFamilies              []IpFamily
	vcnClient               oci.VirtualNetworkClient

	subnetCache         *cache.GetOrLoadCache[*ocicore.Subnet]
	subnetSelectorCache *cache.GetOrLoadCache[[]*ocicore.Subnet]

	nsgCache         *cache.GetOrLoadCache[*ocicore.NetworkSecurityGroup]
	nsgSelectorCache *cache.GetOrLoadCache[[]*ocicore.NetworkSecurityGroup]
	vnicCache        *cache.GetOrLoadCache[*ocicore.Vnic]
}

func NewProvider(ctx context.Context, clusterVcnCompartmentId string, npnCluster bool,
	ipFamilies []IpFamily, vcnClient oci.VirtualNetworkClient,
	vnicCache *cache.GetOrLoadCache[*ocicore.Vnic]) (*DefaultProvider, error) {
	if vnicCache == nil {
		return nil, errVnicCacheRequired
	}
	p := &DefaultProvider{
		clusterVcnCompartmentId: clusterVcnCompartmentId,
		npnCluster:              npnCluster,
		ipFamilies:              ipFamilies,
		vcnClient:               vcnClient,

		subnetCache:         cache.NewDefaultGetOrLoadCache[*ocicore.Subnet](),
		subnetSelectorCache: cache.NewDefaultGetOrLoadCache[[]*ocicore.Subnet](),

		nsgCache:         cache.NewDefaultGetOrLoadCache[*ocicore.NetworkSecurityGroup](),
		nsgSelectorCache: cache.NewDefaultGetOrLoadCache[[]*ocicore.NetworkSecurityGroup](),
		vnicCache:        vnicCache,
	}

	return p, nil
}

func (p *DefaultProvider) ResolveNetworkConfig(ctx context.Context,
	networkCfg *v1beta1.NetworkConfig) (*NetworkResolveResult, error) {
	// verify subnet: exists, not an ad subnet, ipv6 support, pod subnet, nsgs exists.
	if networkCfg == nil {
		return nil, NoNetworkConfigError
	}

	out := new(NetworkResolveResult)
	var errs error
	if networkCfg.PrimaryVnicConfig == nil {
		errs = multierr.Append(errs, NoPrimaryVnicConfigError)
	} else {
		primarySubnetAndNsgs, err := p.resolveSimpleVnicConfig(ctx, *networkCfg.PrimaryVnicConfig, "primaryVnic")
		if err != nil {
			errs = multierr.Append(errs, err)
		}

		if primarySubnetAndNsgs.Subnet != nil && primarySubnetAndNsgs.Subnet.AvailabilityDomain != nil {
			errs = multierr.Append(errs, AdSubnetError)
		}

		// TODO - verify IPV6 support on the subnet
		out.PrimaryVnicSubnet = &primarySubnetAndNsgs
	}

	if p.npnCluster {
		if len(networkCfg.SecondaryVnicConfigs) == 0 {
			errs = multierr.Append(errs, NoSecondaryVnicConfigError)
		} else {
			validationErr := p.validateSecondaryVnicIpCounts(networkCfg)
			if validationErr != nil {
				errs = multierr.Append(errs, validationErr)
			}

			var otherVnicSubnetAndNsgs []*SubnetAndNsgs
			for idx, vnicConfig := range networkCfg.SecondaryVnicConfigs {
				subnetAndNsg, err := p.resolveSimpleVnicConfig(ctx,
					vnicConfig.SimpleVnicConfig, fmt.Sprintf("second vnic %d", idx))
				if err != nil {
					errs = multierr.Append(errs, err)
				} else {
					cidrNumberValidationError := p.validateSecondaryVnicIpCountsAgainstCidrs(vnicConfig, subnetAndNsg)
					if cidrNumberValidationError != nil {
						errs = multierr.Append(errs, cidrNumberValidationError)
					}
				}

				otherVnicSubnetAndNsgs = append(otherVnicSubnetAndNsgs, &subnetAndNsg)
			}

			out.OtherVnicSubnets = otherVnicSubnetAndNsgs
		}
	} else if len(networkCfg.SecondaryVnicConfigs) > 0 {
		errs = multierr.Append(errs, SecondaryVnicConfigNotAllowedError)
	}

	return out, errs
}

/*
Each Secondary VNIC will have a maximum ipCount of 256 (this is the secondaryVNICs[i] → ipCount).
All Secondary VNICs sum(ipCount) <= 256
The IP count is a power of 2
IPv6 will have some additional restrictions
  - when using v6 Single Stack, the IP Count must meet the ipv6 requirements
    (currently only 1, 16 and 256 will be supported).
    The default will be 256. In the future, we will relax the IP Count.
  - when using IPv4+6 Dual Stack, the user can specify any IP Count
    (as long as it conforms to the IPv4 restrictions; power of 2, up to 256).
*/
func (p *DefaultProvider) validateSecondaryVnicIpCounts(networkCfg *v1beta1.NetworkConfig) error {
	ipCountSum := 0
	for _, vnicConfig := range networkCfg.SecondaryVnicConfigs {
		ipCountVal := p.ipCountToCheck(vnicConfig)

		if ipCountVal > 256 {
			return errors.New("max IP count per VNIC can't be over 256")
		}

		if !utils.IsPowerOfTwo(ipCountVal) {
			return errors.New("IP count must be power of 2")
		}

		if IsIPv6SingleStack(p.ipFamilies) {
			if !(ipCountVal == 1 || ipCountVal == 16 || ipCountVal == 256) {
				return errors.New("single stack IPv6 nodepool only IP count 1, 16 and 256 will be supported")
			}
		}

		ipCountSum += ipCountVal
	}
	if ipCountSum > 256 {
		return errors.New("total IP count of all VNICs can't be over 256")
	}
	return nil
}

/*
If the subnet only has one CIDR block for IPv6 enabled cluster and assigned IPv6 IP, the max allowed IP is 16,
If the subnet only has one CIDR block for IPv4 subnet, the max allowed IP is 32
*/
func (p *DefaultProvider) validateSecondaryVnicIpCountsAgainstCidrs(vnicConfig *v1beta1.SecondaryVnicConfig,
	subnetAndNsg SubnetAndNsgs) error {
	ipCountVal := p.ipCountToCheck(vnicConfig)
	if subnetAndNsg.Subnet != nil {
		// When the subnet is resolved, and it is IPv6 enabled and assigned IPv6 IP,
		// for single CIDR block, KPO only allows <=16 IPs
		if IsIPv6(p.ipFamilies) && vnicConfig.AssignIpV6Ip != nil && *vnicConfig.AssignIpV6Ip {
			ipV6CidrBlocks := getCidrBlocks(subnetAndNsg.Subnet.Ipv6CidrBlock, subnetAndNsg.Subnet.Ipv6CidrBlocks)

			if len(ipV6CidrBlocks) == 1 && ipCountVal > 16 {
				return fmt.Errorf("max IP count for single CIDR IPv6 subnet '%s' can't over 16",
					strPtrToStr(subnetAndNsg.Subnet.Id))
			}
		} else {
			// When the subnet is resolved and IPv6, for single CIDR block, it only allows <=32 IPs
			ipv4CidrBlocks := getCidrBlocks(subnetAndNsg.Subnet.CidrBlock, subnetAndNsg.Subnet.Ipv4CidrBlocks)
			if len(ipv4CidrBlocks) == 1 && ipCountVal > 32 {
				return fmt.Errorf("max IP count for single CIDR IPv4 subnet '%s' can't over 32",
					strPtrToStr(subnetAndNsg.Subnet.Id))
			}
		}
	}
	return nil
}

/*
*
According the ocicore Subnet contract. CidrBlock or Ipv6CidrBlock is the default cidr block of the subnet,
Ipv4CidrBlocks or Ipv6CidrBlocks contains all Cidr blocks it has, which will include all cidr blocks including
the one from CidrBlock or Ipv6CidrBlock. In theory Ipv4CidrBlocks or Ipv6CidrBlocks will not be nil or empty.
This code is just for any unexpect situations if Ipv4CidrBlocks or Ipv6CidrBlocks is empty, we will use default
cider blocks for the check instead.
*/
func getCidrBlocks(defaultCidrBlock *string, allCidrBlocks []string) []string {
	cidrBlocks := make([]string, 0)
	if len(allCidrBlocks) == 0 && defaultCidrBlock != nil &&
		strings.TrimSpace(*defaultCidrBlock) != "" {
		cidrBlocks = append(cidrBlocks, *defaultCidrBlock)
	} else {
		cidrBlocks = allCidrBlocks
	}
	return cidrBlocks
}

func (p *DefaultProvider) ipCountToCheck(vnicConfig *v1beta1.SecondaryVnicConfig) int {
	ipCountVal := GetDefaultSecondaryVnicIPCount(p.ipFamilies)
	if vnicConfig.IpCount != nil {
		ipCountVal = *vnicConfig.IpCount
	}
	return ipCountVal
}

func (p *DefaultProvider) resolveSimpleVnicConfig(ctx context.Context, config v1beta1.SimpleVnicConfig,
	identifier string) (sn SubnetAndNsgs, err error) {
	return p.resolveSubnetAndNsgConfig(ctx, *config.SubnetAndNsgConfig, identifier)
}

func (p *DefaultProvider) resolveSubnetAndNsgConfig(ctx context.Context, config v1beta1.SubnetAndNsgConfig,
	identifier string) (sn SubnetAndNsgs, err error) {
	subnet, err := p.resolveSubnet(ctx, *config.SubnetConfig)
	if err != nil {
		return sn, fmt.Errorf("%s: %v", identifier, err)
	}

	sn.Subnet = subnet

	if len(config.NetworkSecurityGroupConfigs) > 0 {
		nsgs, err := p.resolveNsgs(ctx, config.NetworkSecurityGroupConfigs)
		if err != nil {
			return sn, fmt.Errorf("%s: %v", identifier, err)
		}

		for _, nsgDetail := range nsgs {
			if p.isFromDifferentVcn(nsgDetail, subnet) {
				return sn, SubnetAndNsgNotInSameVcn
			}
		}

		sn.NetworkSecurityGroups = nsgs
	}

	for _, ipf := range p.ipFamilies {
		switch ipf {
		case IPv4:
			if subnet.CidrBlock == nil {
				return sn, fmt.Errorf("%s: %v", identifier, NoCidrBlock)
			}
		case IPv6:
			if subnet.Ipv6CidrBlock == nil && len(subnet.Ipv6CidrBlocks) == 0 {
				return sn, fmt.Errorf("%s: %v", identifier, NoIPv6CidrBlock)
			}
		}
	}

	return sn, nil
}

func (p *DefaultProvider) isFromDifferentVcn(nsgDetail *ocicore.NetworkSecurityGroup, subnet *ocicore.Subnet) bool {
	return (nsgDetail.VcnId == nil && subnet.VcnId != nil) ||
		(nsgDetail.VcnId != nil && subnet.VcnId == nil) ||
		(nsgDetail.VcnId != nil && subnet.VcnId != nil && *nsgDetail.VcnId != *subnet.VcnId)
}

func (p *DefaultProvider) resolveNsgs(ctx context.Context,
	configs []*v1beta1.NetworkSecurityGroupConfig) ([]*ocicore.NetworkSecurityGroup, error) {
	var out []*ocicore.NetworkSecurityGroup

	for _, c := range configs {
		if c.NetworkSecurityGroupId != nil && c.NetworkSecurityGroupFilter != nil {
			return nil, InvalidNsgConfigError
		} else if c.NetworkSecurityGroupId != nil {
			nsg, err := p.getNsg(ctx, *c.NetworkSecurityGroupId)
			if err != nil {
				return nil, err
			}

			out = append(out, nsg)
		} else {
			nsgs, err := p.filterNsg(ctx, c.NetworkSecurityGroupFilter)
			if err != nil {
				return nil, err
			}

			out = append(out, nsgs...)
		}
	}

	return out, nil
}

func (p *DefaultProvider) resolveSubnet(ctx context.Context, config v1beta1.SubnetConfig) (*ocicore.Subnet, error) {
	if config.SubnetId != nil && config.SubnetFilter != nil {
		return nil, InvalidSubnetConfigError
	} else if config.SubnetId != nil {
		return p.getSubnet(ctx, *config.SubnetId)
	} else if config.SubnetFilter != nil {
		subnets, err := p.filterSubnets(ctx, config.SubnetFilter)
		if err != nil {
			return nil, err
		}

		if len(subnets) == 0 {
			return nil, NoSubnetMatchSelector
		}

		if len(subnets) > 1 {
			return nil, MoreThanOneSubnetMatchSelector
		}

		return subnets[0], nil
	}

	return nil, InvalidSubnetConfigError
}

func (p *DefaultProvider) getNsg(ctx context.Context, nsgOcid string) (*ocicore.NetworkSecurityGroup, error) {
	return p.nsgCache.GetOrLoad(ctx, nsgOcid,
		func(ctx context.Context, s string) (*ocicore.NetworkSecurityGroup, error) {
			resp, err := p.vcnClient.GetNetworkSecurityGroup(ctx, ocicore.GetNetworkSecurityGroupRequest{
				NetworkSecurityGroupId: &nsgOcid,
			})

			if err != nil {
				return nil, err
			}

			return &resp.NetworkSecurityGroup, nil
		})
}

func (p *DefaultProvider) getSubnet(ctx context.Context, subnetOcid string) (*ocicore.Subnet, error) {
	return p.subnetCache.GetOrLoad(ctx, subnetOcid, func(ctx context.Context, k string) (*ocicore.Subnet, error) {
		resp, err := p.vcnClient.GetSubnet(ctx, ocicore.GetSubnetRequest{
			SubnetId: &k,
		})

		if err != nil {
			return nil, err
		}

		return &resp.Subnet, nil
	})
}

//nolint:dupl
func (p *DefaultProvider) filterSubnets(ctx context.Context,
	subnetSelector *v1beta1.OciResourceSelectorTerm) ([]*ocicore.Subnet, error) {
	key, err := utils.HashFor(subnetSelector)
	if err != nil {
		return nil, err
	}

	compartmentId := p.clusterVcnCompartmentId
	if subnetSelector.CompartmentId != nil {
		compartmentId = *subnetSelector.CompartmentId
	}

	var displayName *string
	if subnetSelector.DisplayName != nil {
		displayName = subnetSelector.DisplayName
	}

	listSubnetReq := ocicore.ListSubnetsRequest{
		CompartmentId: &compartmentId,
		DisplayName:   displayName,
	}

	tagFilterFunc := utils.ToTagFilterFunc(subnetSelector, func(s *ocicore.Subnet) map[string]string {
		return s.FreeformTags
	}, func(s *ocicore.Subnet) map[string]map[string]interface{} {
		return s.DefinedTags
	})

	return p.subnetSelectorCache.GetOrLoad(ctx, key, func(ctx context.Context, s string) ([]*ocicore.Subnet, error) {
		return p.listAndFilterSubnets(ctx, listSubnetReq, tagFilterFunc)
	})
}

//nolint:dupl
func (p *DefaultProvider) listAndFilterSubnets(ctx context.Context, request ocicore.ListSubnetsRequest,
	extraFilterFunc func(image *ocicore.Subnet) bool) ([]*ocicore.Subnet, error) {
	var subnets []*ocicore.Subnet
	for {
		resp, err := p.vcnClient.ListSubnets(ctx, request)

		if err != nil {
			return nil, err
		}

		subnets = append(subnets, lo.Map(lo.Filter(resp.Items, func(item ocicore.Subnet, _ int) bool {
			if extraFilterFunc != nil {
				return extraFilterFunc(&item)
			}

			return true
		}), func(item ocicore.Subnet, _ int) *ocicore.Subnet {
			return lo.ToPtr(item)
		})...)

		request.Page = resp.OpcNextPage
		if request.Page == nil {
			break
		}
	}

	return subnets, nil
}

//nolint:dupl
func (p *DefaultProvider) filterNsg(ctx context.Context,
	nsgFilter *v1beta1.OciResourceSelectorTerm) ([]*ocicore.NetworkSecurityGroup, error) {
	key, err := utils.HashFor(nsgFilter)
	if err != nil {
		return nil, err
	}

	compartmentId := p.clusterVcnCompartmentId
	if nsgFilter.CompartmentId != nil {
		compartmentId = *nsgFilter.CompartmentId
	}

	var displayName *string
	if nsgFilter.DisplayName != nil {
		displayName = nsgFilter.DisplayName
	}

	listNsgReq := ocicore.ListNetworkSecurityGroupsRequest{
		CompartmentId: &compartmentId,
		DisplayName:   displayName,
	}

	tagFilterFunc := utils.ToTagFilterFunc(nsgFilter,
		func(n *ocicore.NetworkSecurityGroup) map[string]string {
			return n.FreeformTags
		},
		func(n *ocicore.NetworkSecurityGroup) map[string]map[string]interface{} {
			return n.DefinedTags
		})

	return p.nsgSelectorCache.GetOrLoad(ctx, key,
		func(ctx context.Context, s string) ([]*ocicore.NetworkSecurityGroup, error) {
			return p.listAndFilterNsgs(ctx, listNsgReq, tagFilterFunc)
		})
}

//nolint:dupl
func (p *DefaultProvider) listAndFilterNsgs(ctx context.Context, req ocicore.ListNetworkSecurityGroupsRequest,
	filterFunc func(n *ocicore.NetworkSecurityGroup) bool) ([]*ocicore.NetworkSecurityGroup, error) {
	var nsgs []*ocicore.NetworkSecurityGroup
	for {
		resp, err := p.vcnClient.ListNetworkSecurityGroups(ctx, req)

		if err != nil {
			return nil, err
		}

		nsgs = append(nsgs, lo.Map(lo.Filter(resp.Items, func(item ocicore.NetworkSecurityGroup, _ int) bool {
			if filterFunc != nil {
				return filterFunc(&item)
			}

			return true
		}), func(item ocicore.NetworkSecurityGroup, _ int) *ocicore.NetworkSecurityGroup {
			return lo.ToPtr(item)
		})...)

		req.Page = resp.OpcNextPage
		if req.Page == nil {
			break
		}
	}

	return nsgs, nil
}

func (p *DefaultProvider) GetVnic(ctx context.Context, vnicOcid string) (*ocicore.Vnic, error) {
	return p.vnicCache.Refresh(ctx, vnicOcid,
		func(ctx context.Context, _ string) (*ocicore.Vnic, error) {
			return p.getVnicImpl(ctx, vnicOcid)
		})
}

func (p *DefaultProvider) getVnicImpl(ctx context.Context, vnicOcid string) (*ocicore.Vnic, error) {
	getVnicResp, err := p.vcnClient.GetVnic(ctx, ocicore.GetVnicRequest{
		VnicId: &vnicOcid,
	})

	if err != nil {
		return nil, err
	}

	return &getVnicResp.Vnic, nil
}

func (p *DefaultProvider) GetVnicCached(ctx context.Context, vnicOcid string) (*ocicore.Vnic, error) {
	return p.vnicCache.GetOrLoad(ctx, vnicOcid,
		func(ctx context.Context, _ string) (*ocicore.Vnic, error) {
			return p.getVnicImpl(ctx, vnicOcid)
		})
}

func strPtrToStr(strValue *string) string {
	if strValue == nil {
		return ""
	}

	return *strValue
}
