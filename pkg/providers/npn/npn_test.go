/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package npn

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"testing"

	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	npnv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/npn/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

var provider *DefaultProvider
var (
	ipV4SingleStack = []network.IpFamily{network.IPv4}
	ipV6SingleStack = []network.IpFamily{network.IPv6}
	ipV6DualStack   = []network.IpFamily{network.IPv4, network.IPv6}
)

func setupTest(t *testing.T) func(t *testing.T) {
	log.Println("setup test")

	var err error
	provider, err = NewProvider(context.TODO(), true, ipV4SingleStack)
	if err != nil {
		t.Fatalf("could not create DefaultProvider")
	}

	return func(tb *testing.T) {
		log.Println("teardown test")
	}
}

func TestCreateNpnCustomObjectInvalidInput(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)

	testCases := []struct {
		npnConfig            *ociv1beta1.NetworkConfig
		networkResolveResult *network.NetworkResolveResult
	}{
		{&ociv1beta1.NetworkConfig{}, nil}, // Everything is nil
		{&ociv1beta1.NetworkConfig{
			SecondaryVnicConfigs: []*ociv1beta1.SecondaryVnicConfig{
				{
					SimpleVnicConfig: ociv1beta1.SimpleVnicConfig{
						SubnetAndNsgConfig: &ociv1beta1.SubnetAndNsgConfig{
							SubnetConfig: &ociv1beta1.SubnetConfig{
								SubnetId: lo.ToPtr("testId"),
							},
						},
					},
				}},
		}, nil}, // Secondary vnic is set, but nil networkResolveResult
		{&ociv1beta1.NetworkConfig{
			SecondaryVnicConfigs: []*ociv1beta1.SecondaryVnicConfig{
				{
					SimpleVnicConfig: ociv1beta1.SimpleVnicConfig{
						SubnetAndNsgConfig: &ociv1beta1.SubnetAndNsgConfig{
							SubnetConfig: &ociv1beta1.SubnetConfig{
								SubnetId: lo.ToPtr("testId"),
							},
						},
					},
				}},
		}, &network.NetworkResolveResult{
			OtherVnicSubnets: nil,
		}}, // Secondary vnic is set, but nil OtherVnics results
	}

	for _, tc := range testCases {
		_, err := provider.createNpnCustomObject(context.TODO(), "testName", "testInstanceId",
			tc.npnConfig, tc.networkResolveResult, nil)
		if err == nil {
			t.Fatalf("Should return error for %v", tc)
		}
		t.Log(err.Error())
	}
}

func TestCreateSecondaryVnicsNpnCustomObjectValidInput(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)

	ipFamiliesToTest := []struct {
		ipFamilies []network.IpFamily
	}{
		{ipV4SingleStack},
		{ipV6DualStack},
		{ipV6SingleStack},
	}

	testCases := []struct {
		nicIndex             *int
		testSubnetId         string
		assignIpv6Ip         *bool
		assignPublicIp       *bool
		vnicDisplayName      *string
		ipCount              *int
		applicationResources *string
		ipv6IpCidrPairs      *[]string
		nsgIds               []string
		skipSourceDestCheck  *bool
		securityAttributes   map[string]map[string]string
	}{
		{nil, "testSubnet1", nil, nil,
			nil, nil, nil, nil, nil, nil,
			nil},
		{lo.ToPtr(1), "testSubnet2", nil, nil,
			nil, nil, nil, nil, nil,
			lo.ToPtr(false),
			nil},
		{lo.ToPtr(1), "testSubnet3",
			lo.ToPtr(true),
			lo.ToPtr(true),
			lo.ToPtr("vnic1"), lo.ToPtr(24), lo.ToPtr("resourceInfo"),
			lo.ToPtr([]string{"10.0.1.0/16", "10.1.0.0/16"}),
			[]string{"nsg1", "nsg2", "nsg3"},
			lo.ToPtr(true),
			map[string]map[string]string{"test1": {"inner1": "value1"},
				"test2": {"inner2": "value2", "inner3": "value3"}}},
	}

	otherVnicResolveResults := make([]*network.SubnetAndNsgs, 0)
	secondaryVnicConfigs := make([]*ociv1beta1.SecondaryVnicConfig, 0)
	for _, tc := range testCases {
		ipv6IpCidrPairObjects := StringArrayToIpv6AddressIpv6SubnetCidrPairs(tc.ipv6IpCidrPairs)

		nsgConfigs := lo.Map(tc.nsgIds, func(item string, _ int) *ociv1beta1.NetworkSecurityGroupConfig {
			return &ociv1beta1.NetworkSecurityGroupConfig{
				NetworkSecurityGroupId: &item,
			}
		})

		secondaryVnicConfigs = append(secondaryVnicConfigs,
			&ociv1beta1.SecondaryVnicConfig{
				SimpleVnicConfig: ociv1beta1.SimpleVnicConfig{
					SubnetAndNsgConfig: &ociv1beta1.SubnetAndNsgConfig{
						SubnetConfig: &ociv1beta1.SubnetConfig{
							SubnetId: &tc.testSubnetId,
						},
						NetworkSecurityGroupConfigs: nsgConfigs,
					},
					AssignIpV6Ip:                         tc.assignIpv6Ip,
					AssignPublicIp:                       tc.assignPublicIp,
					VnicDisplayName:                      tc.vnicDisplayName,
					Ipv6AddressIpv6SubnetCidrPairDetails: ipv6IpCidrPairObjects,
					SkipSourceDestCheck:                  tc.skipSourceDestCheck,
					SecurityAttributes:                   tc.securityAttributes,
				},
				ApplicationResource: tc.applicationResources,
				IpCount:             tc.ipCount,
				NicIndex:            tc.nicIndex,
			})

		nsgDetails := NsgIdsToNetworkSecurityGroupObjects(tc.nsgIds)
		otherVnicResolveResults = append(otherVnicResolveResults, &network.SubnetAndNsgs{
			Subnet: &ocicore.Subnet{
				Id: &tc.testSubnetId,
			},
			NetworkSecurityGroups: nsgDetails,
		})
	}

	log.Printf("OtherVniceResolveResults:%v", otherVnicResolveResults)
	for _, value := range otherVnicResolveResults {
		log.Printf("OtherVniceResolveResult:%v", value.NetworkSecurityGroups)
	}

	testMaxPods := int32(31)
	for _, ipFamilyTc := range ipFamiliesToTest {
		provider.ipFamilies = ipFamilyTc.ipFamilies
		result, err := provider.createNpnCustomObject(context.TODO(), "testName", "testInstanceId",
			&ociv1beta1.NetworkConfig{
				SecondaryVnicConfigs: secondaryVnicConfigs,
			},
			&network.NetworkResolveResult{
				OtherVnicSubnets: otherVnicResolveResults,
			},
			&ociv1beta1.KubeletConfiguration{
				MaxPods: lo.ToPtr(testMaxPods),
			})

		if err != nil {
			t.Fatalf("Should not return errors")
		} else {
			assert.Equal(t, int64(testMaxPods), result.Spec.MaxPodCount)

			// Verify flat fields are populated for OKE NPN controller compatibility
			assert.NotEmpty(t, result.Spec.PodSubnetIDs, "podSubnetIds should be populated")
			assert.NotEmpty(t, result.Spec.IpFamilies, "ipFamilies should be populated")
			for _, tc := range testCases {
				assert.Contains(t, result.Spec.PodSubnetIDs, tc.testSubnetId)
			}
			expectedIpFamilies := lo.Map(ipFamilyTc.ipFamilies, func(f network.IpFamily, _ int) string {
				return string(f)
			})
			assert.Equal(t, expectedIpFamilies, result.Spec.IpFamilies)
			// testCases[2] has nsgIds ["nsg1", "nsg2", "nsg3"]
			assert.NotEmpty(t, result.Spec.NetworkSecurityGroupIDs, "networkSecurityGroupIds should be populated")
			for _, nsgId := range testCases[2].nsgIds {
				assert.Contains(t, result.Spec.NetworkSecurityGroupIDs, nsgId)
			}

			for index, resultSecondaryVnic := range result.Spec.SecondaryVnics {
				expectedValues := testCases[index]

				bytesValue, err := json.Marshal(resultSecondaryVnic)
				if err != nil {
					t.Fatalf("Result can't be serialized")
				}

				t.Logf("Secondary Vnic %d ='%s'", index, string(bytesValue))

				expectedDisplayName := fmt.Sprintf("SVnic %d", index)
				if expectedValues.vnicDisplayName != nil {
					expectedDisplayName = *expectedValues.vnicDisplayName
				}

				expectedSkipSourceDestCheck := lo.ToPtr(true)
				if expectedValues.skipSourceDestCheck != nil {
					expectedSkipSourceDestCheck = expectedValues.skipSourceDestCheck
				}

				expectedIpCount := lo.ToPtr(network.DefaultSecondaryVnicIPCount)
				if network.IsIPv6SingleStack(ipFamilyTc.ipFamilies) {
					expectedIpCount = lo.ToPtr(network.DefaultIPv6SecondaryVnicIPCount)
				}
				if expectedValues.ipCount != nil {
					expectedIpCount = expectedValues.ipCount
				}

				assert.Equal(t, expectedValues.testSubnetId, *resultSecondaryVnic.CreateVnicDetails.SubnetId)
				assert.Equal(t, expectedDisplayName, resultSecondaryVnic.DisplayName)
				assert.Equal(t, expectedDisplayName, *resultSecondaryVnic.CreateVnicDetails.DisplayName)
				assert.Equal(t, expectedIpCount, resultSecondaryVnic.CreateVnicDetails.IpCount)
				assert.Equal(t, expectedValues.nicIndex, resultSecondaryVnic.NicIndex)
				assert.Equal(t, expectedValues.nicIndex, resultSecondaryVnic.NicIndex)
				assert.Equal(t, expectedValues.assignIpv6Ip, resultSecondaryVnic.CreateVnicDetails.AssignIpv6Ip)
				assert.Equal(t, expectedValues.assignPublicIp, resultSecondaryVnic.CreateVnicDetails.AssignPublicIp)
				assert.Equal(t, expectedSkipSourceDestCheck, resultSecondaryVnic.CreateVnicDetails.SkipSourceDestCheck)
				assert.Equal(t, expectedValues.nsgIds, resultSecondaryVnic.CreateVnicDetails.NsgIds)

				if expectedValues.applicationResources == nil {
					assert.True(t, len(resultSecondaryVnic.CreateVnicDetails.ApplicationResources) == 0)
				} else {
					assert.Equal(t, *expectedValues.applicationResources,
						resultSecondaryVnic.CreateVnicDetails.ApplicationResources[0])
				}

				if expectedValues.ipv6IpCidrPairs == nil {
					assert.True(t, len(resultSecondaryVnic.CreateVnicDetails.Ipv6AddressIpv6SubnetCidrPairDetails) == 0)
				} else {
					for index, value := range resultSecondaryVnic.CreateVnicDetails.Ipv6AddressIpv6SubnetCidrPairDetails {
						assert.Equal(t, (*expectedValues.ipv6IpCidrPairs)[index], value.Ipv6SubnetCidr)
					}
				}

				if expectedValues.securityAttributes == nil {
					assert.True(t, len(resultSecondaryVnic.CreateVnicDetails.SecurityAttributes) == 0)
				} else {
					for k, v := range expectedValues.securityAttributes {
						for k1, v1 := range v {
							assert.Equal(t, v1, resultSecondaryVnic.CreateVnicDetails.SecurityAttributes[k][k1])
						}
					}
				}
			}
		}
	}

}

func TestCreateNpnCustomObjectInstanceIdConversion(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)

	testCases := []struct {
		input    *string
		expected *string
	}{
		{nil, nil},
		{lo.ToPtr("ocid1.instance.oc1.phx.anyhqljsh4gjgpycnkwuoji7fbpakw6y7c5ryg2stb626sksly4234pqlwsa"),
			lo.ToPtr("anyhqljsh4gjgpycnkwuoji7fbpakw6y7c5ryg2stb626sksly4234pqlwsa")},
		{lo.ToPtr("abcd"), lo.ToPtr("abcd")},
	}

	testNodeClass := &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			NetworkConfig: &ociv1beta1.NetworkConfig{
				SecondaryVnicConfigs: []*ociv1beta1.SecondaryVnicConfig{
					{
						SimpleVnicConfig: ociv1beta1.SimpleVnicConfig{
							SubnetAndNsgConfig: &ociv1beta1.SubnetAndNsgConfig{
								SubnetConfig: &ociv1beta1.SubnetConfig{
									SubnetId: lo.ToPtr("testSubnetId"),
								},
							},
						},
					}},
			},
		},
	}

	testNetworkResolveResult := &network.NetworkResolveResult{
		OtherVnicSubnets: []*network.SubnetAndNsgs{
			{
				Subnet: &ocicore.Subnet{
					Id: lo.ToPtr("testSubnetId"),
				}},
		},
	}

	for _, tc := range testCases {
		instanceInfo := &ocicore.Instance{
			Id: tc.input,
		}

		npnObjectResult, err := provider.CreateNpnCustomObject(context.TODO(),
			instanceInfo, testNodeClass, testNetworkResolveResult)
		if tc.input == nil {
			t.Logf("Error message '%s'", err.Error())
			assert.NotNil(t, err)
		} else {
			assert.NoError(t, err)
			t.Logf("Input = '%s' Name='%s'", *tc.input, npnObjectResult.Name)
			assert.Equal(t, *tc.expected, npnObjectResult.Name)
		}
	}
}

func TestTypeToStructuredConvertion(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)

	npnCustomObjectTest := &npnv1beta1.NativePodNetwork{
		TypeMeta: metav1.TypeMeta{
			APIVersion: NPN_CUSTOM_RESOURCE_GROUP_NAME + "/" + NPN_CUSTOM_RESOURCE_V1_BETA1,
			Kind:       NPN_CUSTOM_RESOURCE_KIND,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "testName",
		},
		Spec: npnv1beta1.NativePodNetworkSpec{
			ID:                      "testId",
			MaxPodCount:             int64(20),
			PodSubnetIDs:            []string{"testPodSubnetId"},
			NetworkSecurityGroupIDs: []string{"ngs1", "nsg2"},
			IpFamilies:              []string{"IPv4", "IPv6"},
		},
	}

	requestUnstructured, err := runtime.DefaultUnstructuredConverter.ToUnstructured(npnCustomObjectTest)
	if err != nil {
		t.Fatalf("Conversion error for %v", npnCustomObjectTest)
	}

	originalJson := assertMarshalJson(t, npnCustomObjectTest)
	unstructuredJson := assertMarshalJson(t, requestUnstructured)
	t.Logf("original = '%s', unstructured = '%s'", originalJson, unstructuredJson)

	var npnUnmarshal = new(npnv1beta1.NativePodNetwork)
	err = json.Unmarshal([]byte(unstructuredJson), npnUnmarshal)
	if err != nil {
		t.Fatalf("Unmarshal error for %v", unstructuredJson)
	}

	assert.Equal(t, *npnCustomObjectTest, *npnUnmarshal)
}

func assertMarshalJson(t *testing.T, result any) string {
	resultJson, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Can not conver result: %v", result)
	}
	marshalResult := string(resultJson)
	log.Printf("CreateNpnCustomObject = %v", marshalResult)
	return marshalResult
}

func TestMaxPodSetting(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)

	testCases := []struct {
		maxPods  *int32
		ipCounts []*int
		expect   int64
	}{
		{
			maxPods:  nil,
			ipCounts: []*int{lo.ToPtr(16)},
			expect:   int64(16),
		},
		{
			maxPods:  lo.ToPtr(int32(50)),
			ipCounts: []*int{lo.ToPtr(16), lo.ToPtr(10)},
			expect:   int64(26),
		},
		{
			maxPods:  lo.ToPtr(int32(20)),
			ipCounts: []*int{lo.ToPtr(16), lo.ToPtr(10)},
			expect:   int64(20),
		},
	}

	testSubnetId := "ocid1.subnet.oc1..aaa"
	for _, tc := range testCases {
		otherVnicResolveResults := make([]*network.SubnetAndNsgs, 0)
		secondaryVnicConfigs := make([]*ociv1beta1.SecondaryVnicConfig, 0)
		for _, ipCount := range tc.ipCounts {

			secondaryVnicConfigs = append(secondaryVnicConfigs, &ociv1beta1.SecondaryVnicConfig{
				SimpleVnicConfig: ociv1beta1.SimpleVnicConfig{
					SubnetAndNsgConfig: &ociv1beta1.SubnetAndNsgConfig{
						SubnetConfig: &ociv1beta1.SubnetConfig{SubnetId: &testSubnetId},
					},
				},
				IpCount: ipCount,
			})
			otherVnicResolveResults = append(otherVnicResolveResults, &network.SubnetAndNsgs{
				Subnet: &ocicore.Subnet{
					Id: &testSubnetId,
				},
			})
		}

		result, err := provider.createNpnCustomObject(context.TODO(), "testName", "testInstanceId",
			&ociv1beta1.NetworkConfig{
				SecondaryVnicConfigs: secondaryVnicConfigs,
			},
			&network.NetworkResolveResult{
				OtherVnicSubnets: otherVnicResolveResults,
			},
			&ociv1beta1.KubeletConfiguration{
				MaxPods: tc.maxPods,
			})

		assert.NoError(t, err)
		assert.Equal(t, tc.expect, result.Spec.MaxPodCount)

		// Verify flat fields are populated
		assert.Contains(t, result.Spec.PodSubnetIDs, testSubnetId)
		assert.Equal(t, []string{"IPv4"}, result.Spec.IpFamilies)
	}
}

func TestFlatFieldsDeduplication(t *testing.T) {
	teardownTest := setupTest(t)
	defer teardownTest(t)

	// Two secondary VNICs sharing the same subnet but different NSGs
	sharedSubnetId := "ocid1.subnet.shared"
	nsg1 := "ocid1.nsg.1"
	nsg2 := "ocid1.nsg.2"

	secondaryVnicConfigs := []*ociv1beta1.SecondaryVnicConfig{
		{
			SimpleVnicConfig: ociv1beta1.SimpleVnicConfig{
				SubnetAndNsgConfig: &ociv1beta1.SubnetAndNsgConfig{
					SubnetConfig: &ociv1beta1.SubnetConfig{SubnetId: &sharedSubnetId},
				},
			},
			IpCount: lo.ToPtr(16),
		},
		{
			SimpleVnicConfig: ociv1beta1.SimpleVnicConfig{
				SubnetAndNsgConfig: &ociv1beta1.SubnetAndNsgConfig{
					SubnetConfig: &ociv1beta1.SubnetConfig{SubnetId: &sharedSubnetId},
				},
			},
			IpCount: lo.ToPtr(16),
		},
	}

	otherVnicSubnets := []*network.SubnetAndNsgs{
		{
			Subnet:                &ocicore.Subnet{Id: &sharedSubnetId},
			NetworkSecurityGroups: NsgIdsToNetworkSecurityGroupObjects([]string{nsg1, nsg2}),
		},
		{
			Subnet:                &ocicore.Subnet{Id: &sharedSubnetId},
			NetworkSecurityGroups: NsgIdsToNetworkSecurityGroupObjects([]string{nsg1}),
		},
	}

	// Test with dual-stack IP families
	provider.ipFamilies = ipV6DualStack

	result, err := provider.createNpnCustomObject(context.TODO(), "testName", "testInstanceId",
		&ociv1beta1.NetworkConfig{SecondaryVnicConfigs: secondaryVnicConfigs},
		&network.NetworkResolveResult{OtherVnicSubnets: otherVnicSubnets},
		nil)

	assert.NoError(t, err)

	// Subnet IDs should be deduplicated
	assert.Equal(t, []string{sharedSubnetId}, result.Spec.PodSubnetIDs,
		"duplicate subnet IDs should be deduplicated")

	// NSG IDs should be deduplicated across VNICs
	assert.Len(t, result.Spec.NetworkSecurityGroupIDs, 2,
		"NSG IDs should be deduplicated across VNICs")
	assert.Contains(t, result.Spec.NetworkSecurityGroupIDs, nsg1)
	assert.Contains(t, result.Spec.NetworkSecurityGroupIDs, nsg2)

	// IP families should reflect provider config
	assert.Equal(t, []string{"IPv4", "IPv6"}, result.Spec.IpFamilies)

	// Secondary VNICs should still be populated (nested format preserved)
	assert.Len(t, result.Spec.SecondaryVnics, 2)
}
