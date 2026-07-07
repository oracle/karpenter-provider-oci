/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package fakes

import (
	"context"
	"errors"

	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
)

var (
	TestSubnets = map[string]ocicore.Subnet{
		"testSubnet1": {
			CidrBlock: lo.ToPtr("10.0.1.0/24"),
			VcnId:     lo.ToPtr("testVcn1"),
		},
		"testSubnetNilId": {
			CidrBlock: lo.ToPtr("10.0.1.0/24"),
		},
	}

	TestNsgs = map[string]ocicore.NetworkSecurityGroup{
		"testNsg1": {
			VcnId: lo.ToPtr("testVcn1"),
		},
		"testNsg2": {
			VcnId: lo.ToPtr("testVcn2"),
		},
		"testNsgNilId": {},
	}

	// Extended test data for comprehensive network coverage
	NetworkTestSubnets = map[string]ocicore.Subnet{
		"subnet-ipv4": {
			Id:             lo.ToPtr("subnet-ipv4"),
			CidrBlock:      lo.ToPtr("10.0.1.0/24"),
			DisplayName:    lo.ToPtr("subnet-ipv4"),
			Ipv4CidrBlocks: []string{"10.0.1.0/24", "10.0.2.0/24"},
			VcnId:          lo.ToPtr("vcn-1"),
		},
		"subnet-dual": {
			Id:             lo.ToPtr("subnet-dual"),
			CidrBlock:      lo.ToPtr("10.0.1.0/24"),
			Ipv4CidrBlocks: []string{"10.0.1.0/24", "10.0.2.0/24"},
			Ipv6CidrBlocks: []string{"2001:db8::/32", "2001:db9::/32"},
			VcnId:          lo.ToPtr("vcn-1"),
		},
		"subnet-noipv4": {
			Id:             lo.ToPtr("subnet-noipv4"),
			Ipv6CidrBlocks: []string{"2001:db8::/32"},
			VcnId:          lo.ToPtr("vcn-1"),
		},
		"subnet-ad": {
			Id:                 lo.ToPtr("subnet-ad"),
			CidrBlock:          lo.ToPtr("10.0.1.0/24"),
			AvailabilityDomain: lo.ToPtr("AD-1"),
			VcnId:              lo.ToPtr("vcn-1"),
		},
		"subnet-private": {
			Id:          lo.ToPtr("subnet-private"),
			CidrBlock:   lo.ToPtr("10.0.2.0/24"),
			DisplayName: lo.ToPtr("private-subnet"),
			VcnId:       lo.ToPtr("vcn-1"),
		},
		"subnet-stage": {
			Id:        lo.ToPtr("subnet-stage"),
			CidrBlock: lo.ToPtr("10.0.3.0/24"),
			FreeformTags: map[string]string{
				"env": "stage",
			},
			VcnId: lo.ToPtr("vcn-1"),
		},
		"subnet-prod": {
			Id:        lo.ToPtr("subnet-prod"),
			CidrBlock: lo.ToPtr("10.0.4.0/24"),
			FreeformTags: map[string]string{
				"env": "prod",
			},
			VcnId: lo.ToPtr("vcn-1"),
		},
		"subnet-noip": {
			Id:    lo.ToPtr("subnet-noip"),
			VcnId: lo.ToPtr("vcn-1"),
		},
		"subnet-single-v4-cidr": {
			Id:             lo.ToPtr("subnet-single-v4-cidr"),
			CidrBlock:      lo.ToPtr("10.0.1.0/24"),
			Ipv4CidrBlocks: []string{"10.0.1.0/24"},
			Ipv6CidrBlocks: []string{"2001:db8::/32", "2001:db9::/32"},
			VcnId:          lo.ToPtr("vcn-1"),
		},
		"subnet-single-v6-cidr": {
			Id:             lo.ToPtr("subnet-single-v6-cidr"),
			CidrBlock:      lo.ToPtr("10.0.1.0/24"),
			Ipv4CidrBlocks: []string{"10.0.1.0/24", "10.0.2.0/24"},
			Ipv6CidrBlocks: []string{"2001:db8::/32"},
			VcnId:          lo.ToPtr("vcn-1"),
		},
	}

	NetworkTestNsgs = map[string]ocicore.NetworkSecurityGroup{
		"nsg-vcn1-a": {
			Id:          lo.ToPtr("nsg-vcn1-a"),
			VcnId:       lo.ToPtr("vcn-1"),
			DisplayName: lo.ToPtr("nsg-vcn1-a"),
		},
		"nsg-vcn1-b": {
			Id:    lo.ToPtr("nsg-vcn1-b"),
			VcnId: lo.ToPtr("vcn-1"),
		},
		"nsg-vcn2": {
			Id:    lo.ToPtr("nsg-vcn2"),
			VcnId: lo.ToPtr("vcn-2"),
		},
		"nsg-app": {
			Id:          lo.ToPtr("nsg-app"),
			DisplayName: lo.ToPtr("app-nsg"),
			FreeformTags: map[string]string{
				"env": "test",
			},
			VcnId: lo.ToPtr("vcn-1"),
		},
		"nsg-web": {
			Id:          lo.ToPtr("nsg-web"),
			DisplayName: lo.ToPtr("web-nsg"),
			FreeformTags: map[string]string{
				"env": "prod",
			},
			VcnId: lo.ToPtr("vcn-1"),
		},
	}

	TestVnics = map[string]ocicore.Vnic{
		"vnic-1": {
			Id: lo.ToPtr("vnic-1"),
		},
	}
)

type FakeVirtualNetwork struct {
	ListNetworkSecurityGroupsCount int
	GetNetworkSecurityGroupCount   int
	GetSubnetCount                 int
	ListSubnetsCount               int
	GetVnicCount                   int

	// For network tests, use extended data instead of legacy TestSubnets/TestNsgs
	UseNetworkTestData bool
	OnGetVnic          func(context.Context, ocicore.GetVnicRequest) (ocicore.GetVnicResponse, error)
}

func (v *FakeVirtualNetwork) GetNetworkSecurityGroup(ctx context.Context,
	request ocicore.GetNetworkSecurityGroupRequest) (response ocicore.GetNetworkSecurityGroupResponse, err error) {
	v.GetNetworkSecurityGroupCount++

	var nsg ocicore.NetworkSecurityGroup
	var found bool
	if v.UseNetworkTestData {
		nsg, found = NetworkTestNsgs[*request.NetworkSecurityGroupId]
	} else {
		nsg, found = TestNsgs[*request.NetworkSecurityGroupId]
	}

	if !found {
		return ocicore.GetNetworkSecurityGroupResponse{}, errors.New("NSG not found")
	}

	return ocicore.GetNetworkSecurityGroupResponse{
		NetworkSecurityGroup: nsg,
	}, nil
}

func (v *FakeVirtualNetwork) GetSubnet(ctx context.Context,
	request ocicore.GetSubnetRequest) (response ocicore.GetSubnetResponse, err error) {
	v.GetSubnetCount++

	var subnet ocicore.Subnet
	var found bool
	if v.UseNetworkTestData {
		subnet, found = NetworkTestSubnets[*request.SubnetId]
	} else {
		subnet, found = TestSubnets[*request.SubnetId]

	}

	if !found {
		return ocicore.GetSubnetResponse{}, errors.New("subnet not found")
	}

	return ocicore.GetSubnetResponse{
		Subnet: subnet,
	}, nil
}

func (v *FakeVirtualNetwork) ListSubnets(ctx context.Context,
	request ocicore.ListSubnetsRequest) (response ocicore.ListSubnetsResponse, err error) {
	v.ListSubnetsCount++

	if !v.UseNetworkTestData {
		return ocicore.ListSubnetsResponse{}, nil
	}

	allSubnets := make([]ocicore.Subnet, 0, len(NetworkTestSubnets))
	for _, subnet := range NetworkTestSubnets {
		allSubnets = append(allSubnets, subnet)
	}

	// Apply filters
	filtered := make([]ocicore.Subnet, 0, len(allSubnets))
	for _, subnet := range allSubnets {
		// Filter by DisplayName if specified
		if request.DisplayName != nil {
			if subnet.DisplayName == nil || *request.DisplayName != *subnet.DisplayName {
				continue
			}
		}
		filtered = append(filtered, subnet)
	}

	// For network tests, fake two-page pagination
	if request.Page == nil {
		// First page – return first half + next token if there are more
		half := len(filtered) / 2
		if half == 0 {
			half = len(filtered) // If only 1 item, return it all on first page
		}
		items := filtered[:half]
		if len(filtered) > half {
			return ocicore.ListSubnetsResponse{
				Items:       items,
				OpcNextPage: lo.ToPtr("page2"),
			}, nil
		}
		return ocicore.ListSubnetsResponse{
			Items: items,
		}, nil
	}

	// Second page – remaining items, no next token
	half := len(filtered) / 2
	items := filtered[half:]
	return ocicore.ListSubnetsResponse{
		Items: items,
	}, nil
}

func (v *FakeVirtualNetwork) ListNetworkSecurityGroups(ctx context.Context,
	request ocicore.ListNetworkSecurityGroupsRequest) (response ocicore.ListNetworkSecurityGroupsResponse, err error) {
	v.ListNetworkSecurityGroupsCount++

	if !v.UseNetworkTestData {
		if request.Page == nil {
			// First page – matching items + next token
			items := []ocicore.NetworkSecurityGroup{
				{
					Id:          lo.ToPtr("app-nsg-1"),
					DisplayName: lo.ToPtr("app-nsg"),
					FreeformTags: map[string]string{
						"env": "test",
					},
					VcnId: lo.ToPtr("testVcn1"),
				},
			}
			return ocicore.ListNetworkSecurityGroupsResponse{
				Items:       items,
				OpcNextPage: lo.ToPtr("page2"),
			}, nil
		}

		// Second page – one non-matching item, no next token
		items := []ocicore.NetworkSecurityGroup{
			{
				Id:          lo.ToPtr("other-nsg"),
				DisplayName: lo.ToPtr("other"),
				FreeformTags: map[string]string{
					"env": "prod",
				},
				VcnId: lo.ToPtr("testVcn2"),
			},
		}
		return ocicore.ListNetworkSecurityGroupsResponse{
			Items: items,
		}, nil
	}

	// New network test data behavior
	allNsgs := make([]ocicore.NetworkSecurityGroup, 0, len(NetworkTestNsgs))
	for _, nsg := range NetworkTestNsgs {
		allNsgs = append(allNsgs, nsg)
	}

	// Apply filters
	filtered := make([]ocicore.NetworkSecurityGroup, 0, len(allNsgs))
	for _, nsg := range allNsgs {
		// Filter by DisplayName if specified
		if request.DisplayName != nil {
			if nsg.DisplayName == nil || *request.DisplayName != *nsg.DisplayName {
				continue
			}
		}
		filtered = append(filtered, nsg)
	}

	// For network tests, fake two-page pagination
	if request.Page == nil {
		// First page – return first half + next token if there are more
		half := len(filtered) / 2
		if half == 0 {
			half = len(filtered) // If only 1 item, return it all on first page
		}
		items := filtered[:half]
		if len(filtered) > half {
			return ocicore.ListNetworkSecurityGroupsResponse{
				Items:       items,
				OpcNextPage: lo.ToPtr("page2"),
			}, nil
		}
		return ocicore.ListNetworkSecurityGroupsResponse{
			Items: items,
		}, nil
	}

	// Second page – remaining items, no next token
	half := len(filtered) / 2
	items := filtered[half:]
	return ocicore.ListNetworkSecurityGroupsResponse{
		Items: items,
	}, nil
}

func (v *FakeVirtualNetwork) GetVnic(ctx context.Context,
	request ocicore.GetVnicRequest) (response ocicore.GetVnicResponse, err error) {
	v.GetVnicCount++
	if v.OnGetVnic != nil {
		return v.OnGetVnic(ctx, request)
	}

	var vnic ocicore.Vnic
	if v.UseNetworkTestData {
		vnic = TestVnics[*request.VnicId]
	}

	return ocicore.GetVnicResponse{
		Vnic: vnic,
	}, nil
}

// NewFakeVcnForNetworkTests creates a FakeVirtualNetwork pre-configured for comprehensive network testing
func NewFakeVcnForNetworkTests() *FakeVirtualNetwork {
	return &FakeVirtualNetwork{
		UseNetworkTestData: true,
	}
}
