/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package network

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
)

type IpFamily string

const (
	IPv4                            = IpFamily("IPv4")
	IPv6                            = IpFamily("IPv6")
	DefaultSecondaryVnicIPCount     = 32
	DefaultIPv6SecondaryVnicIPCount = 256
)

type IpFamilyValue struct {
	IpFamilies []IpFamily
}

func (i *IpFamilyValue) String() string {
	bytes := lo.Must(json.Marshal(i))
	return string(bytes)
}

func (i *IpFamilyValue) Set(s string) error {
	ipfs := make([]IpFamily, 0)
	for _, f := range strings.Split(s, ",") {
		ipf := IpFamily(strings.TrimSpace(f))

		if ipf != IPv4 && ipf != IPv6 {
			return fmt.Errorf("unsupported ip family: %s", ipf)
		}

		ipfs = append(ipfs, ipf)
	}

	if i.IpFamilies == nil {
		i.IpFamilies = make([]IpFamily, 0)
	}

	i.IpFamilies = append(i.IpFamilies, ipfs...)

	return nil
}

type NetworkResolveResult struct {
	PrimaryVnicSubnet *SubnetAndNsgs
	OtherVnicSubnets  []*SubnetAndNsgs
}

type SubnetAndNsgs struct {
	Subnet                *ocicore.Subnet
	NetworkSecurityGroups []*ocicore.NetworkSecurityGroup
	AllocateIPv6          *bool
	AllocateIPv4          *bool
}

func (san *SubnetAndNsgs) NsgIds() []string {
	return lo.Map(san.NetworkSecurityGroups, func(item *ocicore.NetworkSecurityGroup, _ int) string {
		return *item.Id
	})
}

func IsIPv6SingleStack(ipFamilies []IpFamily) bool {
	return len(ipFamilies) == 1 && slices.Contains(ipFamilies, IPv6)
}

func IsIPv6(ipFamilies []IpFamily) bool {
	return slices.Contains(ipFamilies, IPv6)
}

func GetDefaultSecondaryVnicIPCount(ipFamilies []IpFamily) int {
	if IsIPv6SingleStack(ipFamilies) {
		return DefaultIPv6SecondaryVnicIPCount
	}

	return DefaultSecondaryVnicIPCount
}
