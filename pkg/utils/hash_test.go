/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package utils

import (
	"fmt"
	"testing"

	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

type sample struct {
	A string
	B int
}

func TestHashFor(t *testing.T) {
	obj := sample{"hello", 42}

	h1, err := HashFor(obj)
	require.NoError(t, err)

	// Deterministic
	h2, err := HashFor(obj)
	require.NoError(t, err)
	require.Equal(t, h1, h2, "hash must be deterministic")

	// Different object -> different hash
	other := sample{"bye", 24}
	h3, err := HashFor(other)
	require.NoError(t, err)
	require.NotEqual(t, h1, h3, "different objects must hash differently")

	// Nil object - json.Marshal(nil) works, returns "null"
	hNil, err := HashFor(nil)
	require.NoError(t, err)
	require.Equal(t, Digest([]byte("null")), hNil)

	// Unmarshalable object (channel)
	ch := make(chan int)
	_, err = HashFor(ch)
	require.Error(t, err)
}

func TestHashForMultiObjects(t *testing.T) {
	arr := []interface{}{sample{"a", 1}, sample{"b", 2}}

	h1, err := HashForMultiObjects(arr)
	require.NoError(t, err)

	// same order, same hash
	h2, err := HashForMultiObjects(arr)
	require.NoError(t, err)
	require.Equal(t, h1, h2)

	// with nil in slice should produce same hash
	arrWithNil := append(arr, nil)
	h3, err := HashForMultiObjects(arrWithNil)
	require.NoError(t, err)
	require.Equal(t, h1, h3)

	// with unmarshalable object
	ch := make(chan int)
	arrWithChan := []interface{}{sample{"a", 1}, ch}
	_, err = HashForMultiObjects(arrWithChan)
	require.Error(t, err)
}

func TestHashForMarshalError(t *testing.T) {
	// Test that HashFor returns an error when json.Marshal fails
	// This happens with types that cannot be marshaled (like functions, channels, etc.)
	ch := make(chan int)
	_, err := HashFor(ch)
	require.Error(t, err)

	// Test with a function
	fn := func() {}
	_, err = HashFor(fn)
	require.Error(t, err)
}

func TestHashNodeClassSpec_StaticFieldsCloning(t *testing.T) {
	// Prepare a nodeClass spec with both static and non-static fields
	staticValue := "STATIC"
	dynamicValue := "DYNAMIC"
	dynamicValue2 := "DYNAMIC_CHANGED"
	assignIPv6 := true
	assignPub := true
	ipv6Detail := &v1beta1.Ipv6AddressIpv6SubnetCidrPairDetails{SubnetCidr: "subnet-cidr"}
	ipv6List := []*v1beta1.Ipv6AddressIpv6SubnetCidrPairDetails{ipv6Detail}
	skipSrcDest := true
	securityAttrs := map[string]map[string]string{"foo": {"bar": "baz"}}

	primaryVnic := &v1beta1.SimpleVnicConfig{
		AssignIpV6Ip:                         &assignIPv6,
		AssignPublicIp:                       &assignPub,
		Ipv6AddressIpv6SubnetCidrPairDetails: ipv6List,
		SkipSourceDestCheck:                  &skipSrcDest,
		SecurityAttributes:                   securityAttrs,
		VnicDisplayName:                      &dynamicValue, // dynamic
	}
	secondaryVnic := &v1beta1.SecondaryVnicConfig{
		SimpleVnicConfig:    *primaryVnic,
		ApplicationResource: &staticValue,
		IpCount:             lo.ToPtr(4),
		NicIndex:            lo.ToPtr(2),
	}
	staticCompartmentID := staticValue
	nodeClass := &v1beta1.OCINodeClass{
		Spec: v1beta1.OCINodeClassSpec{
			ShapeConfigs: []*v1beta1.ShapeConfig{
				{
					BaselineOcpuUtilization: nil,
					Ocpus:                   lo.ToPtr(float32(2)),
					MemoryInGbs:             lo.ToPtr(float32(1)),
				},
			},
			NodeCompartmentId: &staticCompartmentID,
			NetworkConfig: &v1beta1.NetworkConfig{
				PrimaryVnicConfig:    primaryVnic,
				SecondaryVnicConfigs: []*v1beta1.SecondaryVnicConfig{secondaryVnic},
			},
			Metadata:                map[string]string{"foo": "bar"},
			FreeformTags:            map[string]string{"myTag": "myVal"},
			DefinedTags:             map[string]map[string]string{"myNs": {"key": "val"}},
			PreBootstrapInitScript:  &staticValue,
			PostBootstrapInitScript: &staticValue,
			SshAuthorizedKeys:       []string{"ssh-rsa AAA..."},
		},
	}

	// Hash with all fields set
	initialHash := HashNodeClassSpec(nodeClass)

	// Mutate dynamic fields, verify hash does NOT change
	nodeClass.Spec.NetworkConfig.PrimaryVnicConfig.VnicDisplayName = &dynamicValue2

	hashAfterDynamicChanges := HashNodeClassSpec(nodeClass)
	require.Equal(t, initialHash, hashAfterDynamicChanges,
		fmt.Sprintf("Hash should not change when non-static (dynamic) fields changed; got changed from '%v' to '%v'",
			initialHash, hashAfterDynamicChanges))

	// Mutate a static field, verify hash DOES change
	nodeClass.Spec.NodeCompartmentId = lo.ToPtr("DIFFERENT")
	hashAfterStaticChange := HashNodeClassSpec(nodeClass)
	require.NotEqual(t, initialHash, hashAfterStaticChange,
		fmt.Sprintf("Hash must change when static field changes, but did not (still '%v')", initialHash))
}
