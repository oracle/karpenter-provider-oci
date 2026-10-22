/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package utils

import (
	"encoding/json"
	"fmt"
	"strings"
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
			AgentPlugins:            []string{"Bastion"},
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

	// Mutate the AgentPlugins static field, verify hash DOES change
	baselineHash := HashNodeClassSpec(nodeClass)
	nodeClass.Spec.AgentPlugins = []string{"Bastion", "Compute Instance Monitoring"}
	hashAfterAgentPluginsChange := HashNodeClassSpec(nodeClass)
	require.NotEqual(t, baselineHash, hashAfterAgentPluginsChange,
		fmt.Sprintf("Hash must change when AgentPlugins changes, but did not (still '%v')", baselineHash))
}

func platformConfigLegacyFixtures() []*v1beta1.OCINodeClass {
	minimal := &v1beta1.OCINodeClass{Spec: v1beta1.OCINodeClassSpec{
		NetworkConfig: &v1beta1.NetworkConfig{PrimaryVnicConfig: &v1beta1.SimpleVnicConfig{
			SubnetAndNsgConfig: &v1beta1.SubnetAndNsgConfig{
				SubnetConfig: &v1beta1.SubnetConfig{SubnetId: lo.ToPtr("test-subnet")},
			},
		}},
		VolumeConfig: &v1beta1.VolumeConfig{BootVolumeConfig: &v1beta1.BootVolumeConfig{
			ImageConfig: &v1beta1.ImageConfig{ImageType: v1beta1.OKEImage, ImageId: lo.ToPtr("test-image")},
		}},
	}}
	configured := minimal.DeepCopy()
	configured.Spec.ShapeConfigs = []*v1beta1.ShapeConfig{{Ocpus: lo.ToPtr(float32(4)),
		MemoryInGbs: lo.ToPtr(float32(32))}}
	configured.Spec.KubeletConfig = &v1beta1.KubeletConfiguration{
		MaxPods: lo.ToPtr(int32(80)), PodsPerCore: lo.ToPtr(int32(10)), ClusterDNS: []string{"10.0.0.10"},
		KubeReserved: map[string]string{"cpu": "250m", "memory": "512Mi"},
	}
	configured.Spec.NodeCompartmentId = lo.ToPtr("test-compartment")
	configured.Spec.FreeformTags = map[string]string{"team": "platform"}
	configured.Spec.DefinedTags = map[string]map[string]string{"operations": {"owner": "test"}}
	configured.Spec.Metadata = map[string]string{"custom": "value"}
	configured.Spec.PreBootstrapInitScript = lo.ToPtr("ZWNobyB0ZXN0")
	configured.Spec.AgentPlugins = []string{"Bastion"}
	configured.Spec.NetworkConfig.PrimaryVnicConfig.AssignPublicIp = lo.ToPtr(false)
	return []*v1beta1.OCINodeClass{minimal, configured}
}

func TestHashNodeClassSpec_PlatformConfigEmptyForms(t *testing.T) {
	for _, nc := range platformConfigLegacyFixtures() {
		legacyHash := HashNodeClassSpec(nc)
		for _, raw := range []string{`{}`, `{
		  "secureBootEnabled": null,
		  "trustedPlatformModuleEnabled": null,
		  "measuredBootEnabled": null,
		  "memoryEncryptionEnabled": null,
		  "symmetricMultiThreadingEnabled": null,
		  "accessControlServiceEnabled": null,
		  "virtualInstructionsEnabled": null,
		  "inputOutputMemoryManagementUnitEnabled": null,
		  "numaNodesPerSocket": null,
		  "percentageOfCoresEnabled": null
		}`} {
			require.NoError(t, json.Unmarshal([]byte(raw), &nc.Spec.PlatformConfig))
			require.True(t, nc.Spec.PlatformConfig.IsEmpty())
			before := nc.DeepCopy()
			require.Equal(t, legacyHash, HashNodeClassSpec(nc), raw)
			require.Equal(t, before, nc, "hash normalization must not change the NodeClass")
		}
		require.NoError(t, json.Unmarshal([]byte(`null`), &nc.Spec.PlatformConfig))
		require.Equal(t, legacyHash, HashNodeClassSpec(nc))
	}
}

func TestHashNodeClassSpec_PlatformConfigSuppliedFields(t *testing.T) {
	// Omitting any one of these fields from the static clone must fail this test,
	// including an explicit false being treated as an unset pointer.
	requests := []string{
		`{"secureBootEnabled":false}`, `{"trustedPlatformModuleEnabled":false}`,
		`{"measuredBootEnabled":false}`, `{"memoryEncryptionEnabled":false}`,
		`{"symmetricMultiThreadingEnabled":false}`, `{"accessControlServiceEnabled":false}`,
		`{"virtualInstructionsEnabled":false}`, `{"inputOutputMemoryManagementUnitEnabled":false}`,
		`{"numaNodesPerSocket":"NPS1"}`, `{"percentageOfCoresEnabled":25}`,
	}
	for _, nc := range platformConfigLegacyFixtures() {
		legacyHash := HashNodeClassSpec(nc)
		for _, raw := range requests {
			t.Run(raw, func(t *testing.T) {
				nc.Spec.PlatformConfig = nil
				require.NoError(t, json.Unmarshal([]byte(raw), &nc.Spec.PlatformConfig))
				require.False(t, nc.Spec.PlatformConfig.IsEmpty())
				first := HashNodeClassSpec(nc)
				require.NotEqual(t, legacyHash, first)
				// Changing the supplied value must also cause static drift.
				changed := strings.ReplaceAll(raw, "false", "true")
				changed = strings.ReplaceAll(changed, "NPS1", "NPS2")
				changed = strings.ReplaceAll(changed, "25", "50")
				require.NoError(t, json.Unmarshal([]byte(changed), &nc.Spec.PlatformConfig))
				require.NotEqual(t, first, HashNodeClassSpec(nc))
			})
		}
	}
}

func TestHashNodeClassSpec_PlatformConfigLegacyCompatibility(t *testing.T) {
	// Captured from the pre-PlatformConfig implementation; never recompute these expectations.
	want := []string{"5406671265832938312", "18037793294206555663"}
	for i, nc := range platformConfigLegacyFixtures() {
		require.Equal(t, want[i], HashNodeClassSpec(nc))
	}
}

func TestKarpenterHash_NewOptionalAttributes_shouldNotChange(t *testing.T) {
	test1 := struct {
		name string
	}{
		"Hello",
	}

	test2 := struct {
		name    string
		list    []string
		pointer *string
	}{
		"Hello",
		make([]string, 0),
		nil,
	}
	var testInf1 []interface{}
	var testInf2 []interface{}
	testInf1 = append(testInf1, test1)
	testInf2 = append(testInf2, test2)

	require.Equal(t, KarpenterHash(testInf1), KarpenterHash(testInf2))
}
