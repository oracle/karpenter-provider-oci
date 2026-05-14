/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package network

import (
	"context"
	"testing"

	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
)

var provider *DefaultProvider

func setupTest(t *testing.T) func(t *testing.T) {
	t.Log("setup test")

	vcnClient := fakes.FakeVirtualNetwork{}
	var err error
	provider, err = NewProvider(context.TODO(), "testVcnCompartmentId", true,
		[]IpFamily{IPv4}, &vcnClient)
	if err != nil {
		t.Fatalf("could not create DefaultProvider")
	}

	return func(tb *testing.T) {
		t.Log("teardown test")
	}
}

func TestResolveSimpleVnicConfigWithNoError(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)

	testConfig := createTestVnicConfig("testSubnet1", "testNsg1")

	_, err := provider.resolveSimpleVnicConfig(context.TODO(), testConfig, "testIdentifier")
	assert.NoError(t, err)
}

func TestResolveSimpleVnicConfigWithDifferentSubnet(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)

	testCases := []struct {
		subnetId string
		nsgId    string
	}{
		{"testSubnet1", "testNsg2"},
		{"testSubnetNilId", "testNsg1"},
		{"testSubnet1", "testNsgNilId"},
	}

	for _, tc := range testCases {
		testConfig := createTestVnicConfig(tc.subnetId, tc.nsgId)

		_, err := provider.resolveSimpleVnicConfig(context.TODO(), testConfig, "testIdentifier")
		assert.Error(t, err)
		assert.Error(t, SubnetAndNsgNotInSameVcn, err.Error())
	}
}

func createTestVnicConfig(subnetId string, nsgId string) v1beta1.SimpleVnicConfig {
	return v1beta1.SimpleVnicConfig{
		SubnetAndNsgConfig: &v1beta1.SubnetAndNsgConfig{
			SubnetConfig: &v1beta1.SubnetConfig{
				SubnetId: lo.ToPtr(subnetId),
			},
			NetworkSecurityGroupConfigs: []*v1beta1.NetworkSecurityGroupConfig{
				{
					NetworkSecurityGroupId: lo.ToPtr(nsgId),
				},
			},
		},
	}
}

func TestResolveSimpleVnicConfig_SubnetNotInProviderVCN(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)

	// Create a config with a subnet that's not in the provider's VCN
	testConfig := createTestVnicConfig("testSubnetNilId", "testNsg1")

	_, err := provider.resolveSimpleVnicConfig(context.TODO(), testConfig, "testIdentifier")
	// Should fail because subnet has no VCN ID (nil) while NSG has VCN ID
	assert.Error(t, err)
	assert.Equal(t, SubnetAndNsgNotInSameVcn, err)
}

func TestResolveSimpleVnicConfig_NoNSGs(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)

	// Create a config with no NSGs
	testConfig := v1beta1.SimpleVnicConfig{
		SubnetAndNsgConfig: &v1beta1.SubnetAndNsgConfig{
			SubnetConfig: &v1beta1.SubnetConfig{
				SubnetId: lo.ToPtr("testSubnet1"),
			},
			NetworkSecurityGroupConfigs: []*v1beta1.NetworkSecurityGroupConfig{}, // Empty
		},
	}

	_, err := provider.resolveSimpleVnicConfig(context.TODO(), testConfig, "testIdentifier")
	// Should succeed because NSGs are optional
	assert.NoError(t, err)
}

func TestValidateSubnetAndNsgInSameVcn_MultiNSGsHappy(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)

	// Create config with multiple NSGs in same VCN
	testConfig := v1beta1.SimpleVnicConfig{
		SubnetAndNsgConfig: &v1beta1.SubnetAndNsgConfig{
			SubnetConfig: &v1beta1.SubnetConfig{
				SubnetId: lo.ToPtr("testSubnet1"),
			},
			NetworkSecurityGroupConfigs: []*v1beta1.NetworkSecurityGroupConfig{
				{NetworkSecurityGroupId: lo.ToPtr("testNsg1")},
				{NetworkSecurityGroupId: lo.ToPtr("testNsg1")}, // Duplicate but same VCN
			},
		},
	}

	_, err := provider.resolveSimpleVnicConfig(context.TODO(), testConfig, "testIdentifier")
	assert.NoError(t, err)
}

func TestResolveNetworkConfig_NoNetworkConfig(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)

	_, err := provider.ResolveNetworkConfig(context.TODO(), nil)
	assert.Error(t, err)
	assert.Equal(t, NoNetworkConfigError, err)
}

func TestResolveNetworkConfig_NoPrimaryVnicConfig(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)

	networkCfg := &v1beta1.NetworkConfig{
		// PrimaryVnicConfig is nil
	}

	_, err := provider.ResolveNetworkConfig(context.TODO(), networkCfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), NoPrimaryVnicConfigError.Error())
}

func TestResolveNetworkConfig_NPNCluster_NoSecondaryVnics(t *testing.T) {
	// Create NPN cluster provider
	vcnClient := fakes.FakeVirtualNetwork{}
	npnProvider, err := NewProvider(context.TODO(), "testVcnCompartmentId", true,
		[]IpFamily{IPv4}, &vcnClient)
	assert.NoError(t, err)

	networkCfg := &v1beta1.NetworkConfig{
		PrimaryVnicConfig: &v1beta1.SimpleVnicConfig{
			SubnetAndNsgConfig: &v1beta1.SubnetAndNsgConfig{
				SubnetConfig: &v1beta1.SubnetConfig{
					SubnetId: lo.ToPtr("testSubnet1"),
				},
			},
		},
		// No SecondaryVnicConfigs for NPN cluster
	}

	_, err = npnProvider.ResolveNetworkConfig(context.TODO(), networkCfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), NoSecondaryVnicConfigError.Error())
}

func TestResolveNetworkConfig_NonNPNCluster_WithSecondaryVnics(t *testing.T) {
	// Create non-NPN cluster provider
	vcnClient := fakes.FakeVirtualNetwork{}
	nonNpnProvider, err := NewProvider(context.TODO(), "testVcnCompartmentId", false,
		[]IpFamily{IPv4}, &vcnClient)
	assert.NoError(t, err)

	networkCfg := &v1beta1.NetworkConfig{
		PrimaryVnicConfig: &v1beta1.SimpleVnicConfig{
			SubnetAndNsgConfig: &v1beta1.SubnetAndNsgConfig{
				SubnetConfig: &v1beta1.SubnetConfig{
					SubnetId: lo.ToPtr("testSubnet1"),
				},
			},
		},
		SecondaryVnicConfigs: []*v1beta1.SecondaryVnicConfig{
			{SimpleVnicConfig: createTestVnicConfig("testSubnet1", "testNsg1")},
		},
	}

	_, err = nonNpnProvider.ResolveNetworkConfig(context.TODO(), networkCfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), SecondaryVnicConfigNotAllowedError.Error())
}

func TestResolveSubnet_InvalidConfig(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)

	// Test config with both ID and filter (invalid)
	config := v1beta1.SubnetConfig{
		SubnetId:     lo.ToPtr("testSubnet1"),
		SubnetFilter: &v1beta1.OciResourceSelectorTerm{},
	}

	_, err := provider.resolveSubnet(context.TODO(), config)
	assert.Error(t, err)
	assert.Equal(t, InvalidSubnetConfigError, err)
}

func TestResolveSubnet_NoMatches(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)

	// Test filter that matches no subnets
	config := v1beta1.SubnetConfig{
		SubnetFilter: &v1beta1.OciResourceSelectorTerm{
			DisplayName: lo.ToPtr("nonexistent"),
		},
	}

	_, err := provider.resolveSubnet(context.TODO(), config)
	assert.Error(t, err)
	assert.Equal(t, NoSubnetMatchSelector, err)
}

func TestResolveNsgs_InvalidConfig(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)

	// Test config with both ID and filter (invalid)
	configs := []*v1beta1.NetworkSecurityGroupConfig{
		{
			NetworkSecurityGroupId:     lo.ToPtr("testNsg1"),
			NetworkSecurityGroupFilter: &v1beta1.OciResourceSelectorTerm{},
		},
	}

	_, err := provider.resolveNsgs(context.TODO(), configs)
	assert.Error(t, err)
	assert.Equal(t, InvalidNsgConfigError, err)
}

func TestGetVnic(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)

	_, err := provider.GetVnic(context.TODO(), "testVnicId")
	// Fake client returns empty response, should not error
	assert.NoError(t, err)
}

func TestGetVnicCached(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)

	_, err := provider.GetVnicCached(context.TODO(), "testVnicId")
	// Fake client returns empty response, should not error
	assert.NoError(t, err)
}

func TestFilterSubnets_ByDisplayName_Cache(t *testing.T) {
	provider, fake := newProviderForNetworkTests([]IpFamily{IPv4}, false)

	selector := &v1beta1.OciResourceSelectorTerm{
		DisplayName: lo.ToPtr("private-subnet"),
	}

	// first call – should hit fake once (single page since only 1 match)
	subnets, err := provider.filterSubnets(context.TODO(), selector)
	assert.NoError(t, err)
	assert.Len(t, subnets, 1)
	assert.Equal(t, "subnet-private", *subnets[0].Id)
	assert.Equal(t, 1, fake.ListSubnetsCount)

	// second call – must be served from cache
	subnets, err = provider.filterSubnets(context.TODO(), selector)
	assert.NoError(t, err)
	assert.Len(t, subnets, 1)
	assert.Equal(t, 1, fake.ListSubnetsCount) // unchanged ⇒ cache hit
}

func TestResolveSubnet_MultipleMatches(t *testing.T) {
	provider, _ := newProviderForNetworkTests([]IpFamily{IPv4}, false)

	// Test filter that matches multiple subnets (currently all subnets are returned due to fake limitation)
	config := v1beta1.SubnetConfig{
		SubnetFilter: &v1beta1.OciResourceSelectorTerm{
			// Empty filter matches all subnets
		},
	}

	_, err := provider.resolveSubnet(context.TODO(), config)
	assert.Error(t, err)
	assert.Equal(t, MoreThanOneSubnetMatchSelector, err)
}

func TestResolveSubnet_HappyPath(t *testing.T) {
	provider, _ := newProviderForNetworkTests([]IpFamily{IPv4}, false)

	config := v1beta1.SubnetConfig{
		SubnetId: lo.ToPtr("subnet-ipv4"),
	}

	subnet, err := provider.resolveSubnet(context.TODO(), config)
	assert.NoError(t, err)
	assert.Equal(t, "subnet-ipv4", *subnet.Id)
	assert.Equal(t, "10.0.1.0/24", *subnet.CidrBlock)
}

func TestResolveSubnetAndNsgConfig_IPv6_Missing(t *testing.T) {
	provider, _ := newProviderForNetworkTests([]IpFamily{IPv6}, false)

	config := v1beta1.SubnetAndNsgConfig{
		SubnetConfig: &v1beta1.SubnetConfig{
			SubnetId: lo.ToPtr("subnet-noip"), // Has neither IPv4 nor IPv6
		},
	}

	_, err := provider.resolveSubnetAndNsgConfig(context.TODO(), config, "test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), NoIPv6CidrBlock.Error())
}

func TestResolveSubnetAndNsgConfig_DualStack_Happy(t *testing.T) {
	provider, _ := newProviderForNetworkTests([]IpFamily{IPv4, IPv6}, false)

	config := v1beta1.SubnetAndNsgConfig{
		SubnetConfig: &v1beta1.SubnetConfig{
			SubnetId: lo.ToPtr("subnet-dual"), // Has both IPv4 and IPv6
		},
	}

	result, err := provider.resolveSubnetAndNsgConfig(context.TODO(), config, "test")
	assert.NoError(t, err)
	assert.Equal(t, "subnet-dual", *result.Subnet.Id)
	assert.Equal(t, "10.0.1.0/24", *result.Subnet.CidrBlock)
	assert.Len(t, result.Subnet.Ipv6CidrBlocks, 2)
	assert.Equal(t, "2001:db8::/32", result.Subnet.Ipv6CidrBlocks[0])
}

func TestIsFromDifferentVcn_BothNil(t *testing.T) {
	provider, _ := newProviderForNetworkTests([]IpFamily{IPv4}, false)

	nsg := &ocicore.NetworkSecurityGroup{} // VcnId is nil
	subnet := &ocicore.Subnet{}            // VcnId is nil

	assert.False(t, provider.isFromDifferentVcn(nsg, subnet)) // Both nil = same VCN
}

func TestIsFromDifferentVcn_NsgNil_SubnetHas(t *testing.T) {
	provider, _ := newProviderForNetworkTests([]IpFamily{IPv4}, false)

	nsg := &ocicore.NetworkSecurityGroup{} // VcnId is nil
	subnet := &ocicore.Subnet{
		VcnId: lo.ToPtr("vcn-1"),
	}

	assert.True(t, provider.isFromDifferentVcn(nsg, subnet))
}

func TestIsFromDifferentVcn_SubnetNil_NsgHas(t *testing.T) {
	provider, _ := newProviderForNetworkTests([]IpFamily{IPv4}, false)

	nsg := &ocicore.NetworkSecurityGroup{
		VcnId: lo.ToPtr("vcn-1"),
	}
	subnet := &ocicore.Subnet{} // VcnId is nil

	assert.True(t, provider.isFromDifferentVcn(nsg, subnet))
}

func TestIsFromDifferentVcn_DifferentIds(t *testing.T) {
	provider, _ := newProviderForNetworkTests([]IpFamily{IPv4}, false)

	nsg := &ocicore.NetworkSecurityGroup{
		VcnId: lo.ToPtr("vcn-1"),
	}
	subnet := &ocicore.Subnet{
		VcnId: lo.ToPtr("vcn-2"),
	}

	assert.True(t, provider.isFromDifferentVcn(nsg, subnet))
}

func TestIsFromDifferentVcn_SameId(t *testing.T) {
	provider, _ := newProviderForNetworkTests([]IpFamily{IPv4}, false)

	nsg := &ocicore.NetworkSecurityGroup{
		VcnId: lo.ToPtr("vcn-1"),
	}
	subnet := &ocicore.Subnet{
		VcnId: lo.ToPtr("vcn-1"),
	}

	assert.False(t, provider.isFromDifferentVcn(nsg, subnet))
}

func TestGetVnicCached_CacheHit(t *testing.T) {
	provider, fake := newProviderForNetworkTests([]IpFamily{IPv4}, false)

	// First call
	_, err := provider.GetVnicCached(context.TODO(), "vnic-1")
	assert.NoError(t, err)
	assert.Equal(t, 1, fake.GetVnicCount)

	// Second call - should be from cache
	_, err = provider.GetVnicCached(context.TODO(), "vnic-1")
	assert.NoError(t, err)
	assert.Equal(t, 1, fake.GetVnicCount) // unchanged = cache hit
}

func newProviderForNetworkTests(ipFamilies []IpFamily, npnCluster bool) (*DefaultProvider, *fakes.FakeVirtualNetwork) {
	vcn := fakes.NewFakeVcnForNetworkTests()
	p, _ := NewProvider(context.TODO(), "vcn-1", npnCluster, ipFamilies, vcn)
	return p, vcn
}

func TestFilterNsg_ByDisplayName_Cache(t *testing.T) {
	provider, fake := newProviderForNetworkTests([]IpFamily{IPv4}, false)

	selector := &v1beta1.OciResourceSelectorTerm{
		DisplayName: lo.ToPtr("app-nsg"),
	}

	// first call – should hit fake once (single page since only 1 match)
	nsgs, err := provider.filterNsg(context.TODO(), selector)
	assert.NoError(t, err)
	assert.Len(t, nsgs, 1)
	assert.Equal(t, "nsg-app", *nsgs[0].Id)
	assert.Equal(t, 1, fake.ListNetworkSecurityGroupsCount)

	// second call – must be served from cache
	nsgs, err = provider.filterNsg(context.TODO(), selector)
	assert.NoError(t, err)
	assert.Len(t, nsgs, 1)
	assert.Equal(t, 1, fake.ListNetworkSecurityGroupsCount) // unchanged ⇒ cache hit
}

func TestResolveNsgs_HappyPath(t *testing.T) {
	provider, _ := newProviderForNetworkTests([]IpFamily{IPv4}, false)

	configs := []*v1beta1.NetworkSecurityGroupConfig{
		{NetworkSecurityGroupId: lo.ToPtr("nsg-vcn1-a")},
		{NetworkSecurityGroupId: lo.ToPtr("nsg-vcn1-b")},
	}

	nsgs, err := provider.resolveNsgs(context.TODO(), configs)
	assert.NoError(t, err)
	assert.Len(t, nsgs, 2)
	assert.Equal(t, "nsg-vcn1-a", *nsgs[0].Id)
	assert.Equal(t, "nsg-vcn1-b", *nsgs[1].Id)
}

func TestResolveNsgs_MixedIdsAndSelectors(t *testing.T) {
	provider, _ := newProviderForNetworkTests([]IpFamily{IPv4}, false)

	configs := []*v1beta1.NetworkSecurityGroupConfig{
		{NetworkSecurityGroupId: lo.ToPtr("nsg-vcn1-a")},
		{NetworkSecurityGroupFilter: &v1beta1.OciResourceSelectorTerm{
			DisplayName: lo.ToPtr("app-nsg"),
		}},
	}

	nsgs, err := provider.resolveNsgs(context.TODO(), configs)
	assert.NoError(t, err)
	assert.Len(t, nsgs, 2)
	assert.Equal(t, "nsg-vcn1-a", *nsgs[0].Id)
	assert.Equal(t, "nsg-app", *nsgs[1].Id)
}

func TestResolveNetworkConfig_PrimarySubnet_AD_Error(t *testing.T) {
	provider, _ := newProviderForNetworkTests([]IpFamily{IPv4}, false)

	networkCfg := &v1beta1.NetworkConfig{
		PrimaryVnicConfig: &v1beta1.SimpleVnicConfig{
			SubnetAndNsgConfig: &v1beta1.SubnetAndNsgConfig{
				SubnetConfig: &v1beta1.SubnetConfig{
					SubnetId: lo.ToPtr("subnet-ad"), // Has AvailabilityDomain set
				},
			},
		},
	}

	_, err := provider.ResolveNetworkConfig(context.TODO(), networkCfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), AdSubnetError.Error())
}

func TestResolveNetworkConfig_PrimarySubnet_Missing_IPv4(t *testing.T) {
	provider, _ := newProviderForNetworkTests([]IpFamily{IPv4}, false)

	networkCfg := &v1beta1.NetworkConfig{
		PrimaryVnicConfig: &v1beta1.SimpleVnicConfig{
			SubnetAndNsgConfig: &v1beta1.SubnetAndNsgConfig{
				SubnetConfig: &v1beta1.SubnetConfig{
					SubnetId: lo.ToPtr("subnet-noip"), // Has no IP addresses
				},
			},
		},
	}

	_, err := provider.ResolveNetworkConfig(context.TODO(), networkCfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), NoCidrBlock.Error())
}

func TestResolveNetworkConfig_HappyPath_NonNPN(t *testing.T) {
	provider, _ := newProviderForNetworkTests([]IpFamily{IPv4}, false)

	networkCfg := &v1beta1.NetworkConfig{
		PrimaryVnicConfig: &v1beta1.SimpleVnicConfig{
			SubnetAndNsgConfig: &v1beta1.SubnetAndNsgConfig{
				SubnetConfig: &v1beta1.SubnetConfig{
					SubnetId: lo.ToPtr("subnet-ipv4"),
				},
				NetworkSecurityGroupConfigs: []*v1beta1.NetworkSecurityGroupConfig{
					{NetworkSecurityGroupId: lo.ToPtr("nsg-vcn1-a")},
				},
			},
		},
	}

	result, err := provider.ResolveNetworkConfig(context.TODO(), networkCfg)
	assert.NoError(t, err)
	assert.NotNil(t, result.PrimaryVnicSubnet)
	assert.Equal(t, "subnet-ipv4", *result.PrimaryVnicSubnet.Subnet.Id)
	assert.Len(t, result.PrimaryVnicSubnet.NetworkSecurityGroups, 1)
	assert.Equal(t, "nsg-vcn1-a", *result.PrimaryVnicSubnet.NetworkSecurityGroups[0].Id)
	assert.Nil(t, result.OtherVnicSubnets)
}

func TestResolveNetworkConfig_HappyPath_NPN_DualStack(t *testing.T) {
	provider, _ := newProviderForNetworkTests([]IpFamily{IPv4, IPv6}, true) // NPN + dual-stack

	networkCfg := &v1beta1.NetworkConfig{
		PrimaryVnicConfig: &v1beta1.SimpleVnicConfig{
			SubnetAndNsgConfig: &v1beta1.SubnetAndNsgConfig{
				SubnetConfig: &v1beta1.SubnetConfig{
					SubnetId: lo.ToPtr("subnet-dual"),
				},
			},
		},
		SecondaryVnicConfigs: []*v1beta1.SecondaryVnicConfig{
			{
				SimpleVnicConfig: v1beta1.SimpleVnicConfig{
					SubnetAndNsgConfig: &v1beta1.SubnetAndNsgConfig{
						SubnetConfig: &v1beta1.SubnetConfig{
							SubnetId: lo.ToPtr("subnet-dual"),
						},
						NetworkSecurityGroupConfigs: []*v1beta1.NetworkSecurityGroupConfig{
							{NetworkSecurityGroupId: lo.ToPtr("nsg-vcn1-a")},
							{NetworkSecurityGroupId: lo.ToPtr("nsg-vcn1-b")},
						},
					},
				},
			},
			{
				SimpleVnicConfig: v1beta1.SimpleVnicConfig{
					SubnetAndNsgConfig: &v1beta1.SubnetAndNsgConfig{
						SubnetConfig: &v1beta1.SubnetConfig{
							SubnetId: lo.ToPtr("subnet-dual"),
						},
					},
				},
			},
		},
	}

	result, err := provider.ResolveNetworkConfig(context.TODO(), networkCfg)
	assert.NoError(t, err)
	assert.NotNil(t, result.PrimaryVnicSubnet)
	assert.Equal(t, "subnet-dual", *result.PrimaryVnicSubnet.Subnet.Id)
	assert.Len(t, result.OtherVnicSubnets, 2)

	// First secondary VNIC
	assert.Equal(t, "subnet-dual", *result.OtherVnicSubnets[0].Subnet.Id)
	assert.Len(t, result.OtherVnicSubnets[0].NetworkSecurityGroups, 2)

	// Second secondary VNIC
	assert.Equal(t, "subnet-dual", *result.OtherVnicSubnets[1].Subnet.Id)
	assert.Len(t, result.OtherVnicSubnets[1].NetworkSecurityGroups, 0)
}

func TestIpFamilyValue_Set_Single(t *testing.T) {
	var ipv IpFamilyValue

	err := ipv.Set("IPv4")
	assert.NoError(t, err)
	assert.Len(t, ipv.IpFamilies, 1)
	assert.Equal(t, IPv4, ipv.IpFamilies[0])
}

func TestIpFamilyValue_Set_Multiple(t *testing.T) {
	var ipv IpFamilyValue

	err := ipv.Set("IPv4,IPv6")
	assert.NoError(t, err)
	assert.Len(t, ipv.IpFamilies, 2)
	assert.Equal(t, IPv4, ipv.IpFamilies[0])
	assert.Equal(t, IPv6, ipv.IpFamilies[1])
}

func TestIpFamilyValue_Set_Invalid(t *testing.T) {
	var ipv IpFamilyValue

	err := ipv.Set("IPv4,Invalid")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported ip family")
}

func TestIpFamilyValue_Set_Empty(t *testing.T) {
	var ipv IpFamilyValue

	// Empty string results in one empty token which is invalid
	err := ipv.Set("")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported ip family")
}

func TestIpFamilyValue_Set_WithSpaces(t *testing.T) {
	var ipv IpFamilyValue

	err := ipv.Set(" IPv4 , IPv6 ")
	assert.NoError(t, err)
	assert.Len(t, ipv.IpFamilies, 2)
	assert.Equal(t, IPv4, ipv.IpFamilies[0])
	assert.Equal(t, IPv6, ipv.IpFamilies[1])
}

func TestIpFamilyValue_String(t *testing.T) {
	var ipv IpFamilyValue
	ipv.IpFamilies = []IpFamily{IPv4, IPv6}

	result := ipv.String()
	// The JSON marshaling will produce a specific format
	assert.Contains(t, result, "IPv4")
	assert.Contains(t, result, "IPv6")
}

func TestSubnetAndNsgs_NsgIds(t *testing.T) {
	nsg1 := &ocicore.NetworkSecurityGroup{Id: lo.ToPtr("nsg-1")}
	nsg2 := &ocicore.NetworkSecurityGroup{Id: lo.ToPtr("nsg-2")}

	san := SubnetAndNsgs{
		NetworkSecurityGroups: []*ocicore.NetworkSecurityGroup{nsg1, nsg2},
	}

	ids := san.NsgIds()
	assert.Len(t, ids, 2)
	assert.Equal(t, "nsg-1", ids[0])
	assert.Equal(t, "nsg-2", ids[1])
}

func TestResolveNetworkConfig_FlexCiderValidation(t *testing.T) {
	testCases := []struct {
		ipFamily     []IpFamily
		sVnicIds     []string
		sVnicIpCount []*int
		assignIPv6   *bool
		expectedErr  *string
	}{
		{
			[]IpFamily{IPv4},
			[]string{"subnet-dual"},
			[]*int{lo.ToPtr(16)},
			nil,
			nil,
		},
		{
			[]IpFamily{IPv4},
			[]string{"subnet-dual"},
			[]*int{lo.ToPtr(512)},
			nil,
			lo.ToPtr("max IP count per VNIC can't be over 256"),
		},
		{
			[]IpFamily{IPv4},
			[]string{"subnet-dual"},
			[]*int{lo.ToPtr(34)},
			nil,
			lo.ToPtr("IP count must be power of 2"),
		},
		{
			[]IpFamily{IPv4, IPv6},
			[]string{"subnet-dual"},
			[]*int{lo.ToPtr(34)},
			nil,
			lo.ToPtr("IP count must be power of 2"),
		},
		{
			[]IpFamily{IPv6},
			[]string{"subnet-dual"},
			[]*int{lo.ToPtr(32)},
			nil,
			lo.ToPtr("single stack IPv6 nodepool only IP count 1, 16 and 256 will be supported"),
		},
		{
			[]IpFamily{IPv6},
			[]string{"subnet-dual"},
			[]*int{lo.ToPtr(1)},
			nil,
			nil,
		},
		{
			[]IpFamily{IPv6},
			[]string{"subnet-dual"},
			[]*int{lo.ToPtr(16)},
			nil,
			nil,
		},
		{
			[]IpFamily{IPv6},
			[]string{"subnet-dual"},
			[]*int{lo.ToPtr(256)},
			nil,
			nil,
		},
		{
			[]IpFamily{IPv4},
			[]string{"subnet-dual", "subnet-dual", "subnet-dual"},
			[]*int{lo.ToPtr(128), lo.ToPtr(128), lo.ToPtr(128)},
			nil,
			lo.ToPtr("total IP count of all VNICs can't be over 256"),
		},
		// Test for default ipCount
		{
			[]IpFamily{IPv4},
			[]string{"subnet-dual", "subnet-dual"},
			nil,
			nil,
			nil,
		},
		{
			[]IpFamily{IPv4, IPv6},
			[]string{"subnet-dual"},
			nil,
			nil,
			nil,
		},
		{
			[]IpFamily{IPv6},
			[]string{"subnet-dual"},
			nil,
			nil,
			nil,
		},
		{
			[]IpFamily{IPv6},
			[]string{"subnet-dual", "subnet-dual"},
			nil,
			nil,
			lo.ToPtr("total IP count of all VNICs can't be over 256"),
		},
		{
			[]IpFamily{IPv4},
			[]string{"subnet-single-v4-cidr"},
			nil,
			nil,
			nil,
		},
		{
			[]IpFamily{IPv4},
			[]string{"subnet-single-v4-cidr"},
			[]*int{lo.ToPtr(64)},
			nil,
			lo.ToPtr("max IP count for single CIDR IPv4 subnet 'subnet-single-v4-cidr' can't over 32"),
		},
		{
			[]IpFamily{IPv4},
			[]string{"subnet-single-v4-cidr", "subnet-single-v4-cidr"},
			[]*int{lo.ToPtr(64), lo.ToPtr(128)},
			nil,
			lo.ToPtr("max IP count for single CIDR IPv4 subnet 'subnet-single-v4-cidr' can't over 32; max IP count for single CIDR IPv4 subnet 'subnet-single-v4-cidr' can't over 32"),
		},
		{
			[]IpFamily{IPv4, IPv6},
			[]string{"subnet-single-v6-cidr"},
			nil,
			nil, // Singe IPv6 CIDR but not assigning IPv6 IP
			nil,
		},
		{
			[]IpFamily{IPv4, IPv6},
			[]string{"subnet-dual"},
			nil,
			lo.ToPtr(true), // Assigning IPv6 IP but have two CIDRs
			nil,
		},
		{
			[]IpFamily{IPv4, IPv6},
			[]string{"subnet-single-v6-cidr"},
			nil,
			lo.ToPtr(true),
			lo.ToPtr("max IP count for single CIDR IPv6 subnet 'subnet-single-v6-cidr' can't over 16"),
		},
		{
			[]IpFamily{IPv4, IPv6},
			[]string{"subnet-single-v6-cidr"},
			[]*int{lo.ToPtr(256)},
			lo.ToPtr(true),
			lo.ToPtr("max IP count for single CIDR IPv6 subnet 'subnet-single-v6-cidr' can't over 16"),
		},
		{
			[]IpFamily{IPv4, IPv6},
			[]string{"subnet-single-v6-cidr", "subnet-single-v6-cidr"},
			[]*int{lo.ToPtr(256), nil},
			lo.ToPtr(true),
			lo.ToPtr("total IP count of all VNICs can't be over 256; max IP count for single CIDR IPv6 subnet 'subnet-single-v6-cidr' can't over 16; max IP count for single CIDR IPv6 subnet 'subnet-single-v6-cidr' can't over 16"),
		},
	}

	for _, tc := range testCases {
		provider, _ := newProviderForNetworkTests(tc.ipFamily, true)

		secondaryVnicConfigs := make([]*v1beta1.SecondaryVnicConfig, 0)
		for index, subnetId := range tc.sVnicIds {
			var ipCount *int
			if len(tc.sVnicIpCount) == 0 {
				ipCount = nil
			} else {
				ipCount = tc.sVnicIpCount[index]
			}
			secondaryVnicConfigs = append(secondaryVnicConfigs,
				&v1beta1.SecondaryVnicConfig{
					SimpleVnicConfig: v1beta1.SimpleVnicConfig{
						SubnetAndNsgConfig: &v1beta1.SubnetAndNsgConfig{
							SubnetConfig: &v1beta1.SubnetConfig{
								SubnetId: &subnetId,
							},
						},
						AssignIpV6Ip: tc.assignIPv6,
					},
					IpCount: ipCount,
				})
		}

		networkCfg := &v1beta1.NetworkConfig{
			PrimaryVnicConfig: &v1beta1.SimpleVnicConfig{
				SubnetAndNsgConfig: &v1beta1.SubnetAndNsgConfig{
					SubnetConfig: &v1beta1.SubnetConfig{
						SubnetId: lo.ToPtr("subnet-dual"),
					},
				},
			},
			SecondaryVnicConfigs: secondaryVnicConfigs,
		}

		result, err := provider.ResolveNetworkConfig(context.TODO(), networkCfg)
		if tc.expectedErr != nil {
			assert.Equal(t, *tc.expectedErr, err.Error())
		} else {
			assert.NoError(t, err)
			assert.NotNil(t, result)
		}
	}
}

func TestValidateSecondaryVnicIpCountsAgainstCidrs_SingleCidr_EmptyCidrBlockList(t *testing.T) {
	provider, _ := newProviderForNetworkTests([]IpFamily{IPv4, IPv6}, true)

	t.Run("Ipv6CidrBlocks empty, Ipv6CidrBlock set, AssignIpV6Ip set, exceed max", func(t *testing.T) {
		subnet := &ocicore.Subnet{
			Id:             lo.ToPtr("test-v6-subnet"),
			Ipv6CidrBlocks: []string{},
			Ipv6CidrBlock:  lo.ToPtr("2001:db8::/56"),
		}
		vnicConfig := &v1beta1.SecondaryVnicConfig{
			SimpleVnicConfig: v1beta1.SimpleVnicConfig{
				AssignIpV6Ip: lo.ToPtr(true),
			},
			IpCount: lo.ToPtr(32),
		}
		sn := SubnetAndNsgs{Subnet: subnet}
		err := provider.validateSecondaryVnicIpCountsAgainstCidrs(vnicConfig, sn)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "max IP count for single CIDR IPv6 subnet")
	})

	t.Run("Ipv6CidrBlocks empty, Ipv6CidrBlock set, AssignIpV6Ip set, within max", func(t *testing.T) {
		subnet := &ocicore.Subnet{
			Id:             lo.ToPtr("test-v6-subnet"),
			Ipv6CidrBlocks: []string{},
			Ipv6CidrBlock:  lo.ToPtr("2001:db8::/56"),
		}
		vnicConfig := &v1beta1.SecondaryVnicConfig{
			SimpleVnicConfig: v1beta1.SimpleVnicConfig{
				AssignIpV6Ip: lo.ToPtr(true),
			},
			IpCount: lo.ToPtr(16),
		}
		sn := SubnetAndNsgs{Subnet: subnet}
		err := provider.validateSecondaryVnicIpCountsAgainstCidrs(vnicConfig, sn)
		assert.NoError(t, err)
	})

	t.Run("Ipv4CidrBlocks empty, CidrBlock set, not IPv6, exceed max", func(t *testing.T) {
		provider, _ := newProviderForNetworkTests([]IpFamily{IPv4}, true)
		subnet := &ocicore.Subnet{
			Id:             lo.ToPtr("test-v4-subnet"),
			Ipv4CidrBlocks: []string{},
			CidrBlock:      lo.ToPtr("10.0.0.0/24"),
		}
		vnicConfig := &v1beta1.SecondaryVnicConfig{
			IpCount: lo.ToPtr(64),
		}
		sn := SubnetAndNsgs{Subnet: subnet}
		err := provider.validateSecondaryVnicIpCountsAgainstCidrs(vnicConfig, sn)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "max IP count for single CIDR IPv4 subnet")
	})

	t.Run("Ipv4CidrBlocks empty, CidrBlock set, not IPv6, within max", func(t *testing.T) {
		provider, _ := newProviderForNetworkTests([]IpFamily{IPv4}, true)
		subnet := &ocicore.Subnet{
			Id:             lo.ToPtr("test-v4-subnet"),
			Ipv4CidrBlocks: []string{},
			CidrBlock:      lo.ToPtr("10.0.0.0/24"),
		}
		vnicConfig := &v1beta1.SecondaryVnicConfig{
			IpCount: lo.ToPtr(16),
		}
		sn := SubnetAndNsgs{Subnet: subnet}
		err := provider.validateSecondaryVnicIpCountsAgainstCidrs(vnicConfig, sn)
		assert.NoError(t, err)
	})
}
