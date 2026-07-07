/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package instance

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/awslabs/operatorpkg/object"
	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/cache"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/image"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancemeta"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancetype"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/kms"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/npn"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/placement"
	"github.com/oracle/karpenter-provider-oci/pkg/utils"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/oracle/oci-go-sdk/v65/common"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	ociwr "github.com/oracle/oci-go-sdk/v65/workrequests"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

var (
	ipV4SingleStack = []network.IpFamily{network.IPv4}
)

func makeOfferingWithCapType(capType string) *cloudprovider.Offering {
	return &cloudprovider.Offering{
		Available: true,
		Requirements: scheduling.NewRequirements(
			scheduling.NewRequirement(corev1.CapacityTypeLabelKey, v1.NodeSelectorOpIn, capType),
		),
		Price: 0.1,
	}
}

func minimalNodeClass() *ociv1beta1.OCINodeClass {
	return &ociv1beta1.OCINodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{ociv1beta1.NodeClassHash: "h123"},
		},
		Spec: ociv1beta1.OCINodeClassSpec{
			VolumeConfig: &ociv1beta1.VolumeConfig{
				BootVolumeConfig: &ociv1beta1.BootVolumeConfig{
					ImageConfig: &ociv1beta1.ImageConfig{ImageType: ociv1beta1.OKEImage},
				},
			},
			NetworkConfig: &ociv1beta1.NetworkConfig{
				PrimaryVnicConfig: &ociv1beta1.SimpleVnicConfig{
					SubnetAndNsgConfig: &ociv1beta1.SubnetAndNsgConfig{
						SubnetConfig: &ociv1beta1.SubnetConfig{SubnetId: lo.ToPtr("ocid1.subnet.oc1..sn1")},
					},
				},
			},
			DefinedTags: map[string]map[string]string{"ns": {"k1": "v1"}},
			FreeformTags: map[string]string{
				"customA": "1",
			},
		},
	}
}

func minimalNodeClaim() *corev1.NodeClaim {
	return &corev1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nc-1",
			Labels: map[string]string{
				corev1.NodePoolLabelKey: "poolA",
				ociv1beta1.NodeClass:    "classA",
			},
			OwnerReferences: []metav1.OwnerReference{nodePoolOwnerReference("poolA")},
		},
	}
}

func nodePoolOwnerReference(name string) metav1.OwnerReference {
	nodePoolGVK := object.GVK(&corev1.NodePool{})
	return metav1.OwnerReference{
		APIVersion: nodePoolGVK.GroupVersion().String(),
		Kind:       nodePoolGVK.Kind,
		Name:       name,
		UID:        "nodepool-uid",
	}
}

func minimalImageResolve() *image.ImageResolveResult {
	imgID := lo.ToPtr("ocid1.image.oc1..img")
	return &image.ImageResolveResult{ImageType: ociv1beta1.OKEImage, Images: []*ocicore.Image{{Id: imgID}}}
}

func minimalNetworkResolve() *network.NetworkResolveResult {
	return &network.NetworkResolveResult{
		PrimaryVnicSubnet: &network.SubnetAndNsgs{
			Subnet: &ocicore.Subnet{Id: lo.ToPtr("ocid1.subnet.oc1..sn1")},
		},
	}
}

func minimalPlacement() *placement.Proposal {
	pr := &placement.Proposal{Ad: "tenancy:PHX-AD-1", Fd: lo.ToPtr("FAULT-DOMAIN-1")}
	return pr
}

const testShapeE4Flex = "VM.Standard.E4.Flex"

func TestProvider_BuildDefinedTags(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]map[string]string
		want map[string]map[string]interface{}
	}{
		{
			name: "nil map",
			in:   nil,
			want: map[string]map[string]interface{}{},
		},
		{
			name: "converts to interface map",
			in:   map[string]map[string]string{"ns": {"k1": "v1", "k2": "v2"}},
			want: map[string]map[string]interface{}{"ns": {"k1": "v1", "k2": "v2"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildDefinedTags(tt.in)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestProvider_BuildAgentConfig(t *testing.T) {
	enabled := ocicore.InstanceAgentPluginConfigDetailsDesiredStateEnabled
	tests := []struct {
		name string
		in   []string
		want *ocicore.LaunchInstanceAgentConfigDetails
	}{
		{
			name: "nil list returns nil",
			in:   nil,
			want: nil,
		},
		{
			name: "empty list returns nil",
			in:   []string{},
			want: nil,
		},
		{
			name: "single plugin enabled",
			in:   []string{"Bastion"},
			want: &ocicore.LaunchInstanceAgentConfigDetails{
				PluginsConfig: []ocicore.InstanceAgentPluginConfigDetails{
					{Name: lo.ToPtr("Bastion"), DesiredState: enabled},
				},
			},
		},
		{
			name: "multiple plugins enabled in order",
			in:   []string{"Bastion", "Block Volume Management", "Compute Instance Monitoring"},
			want: &ocicore.LaunchInstanceAgentConfigDetails{
				PluginsConfig: []ocicore.InstanceAgentPluginConfigDetails{
					{Name: lo.ToPtr("Bastion"), DesiredState: enabled},
					{Name: lo.ToPtr("Block Volume Management"), DesiredState: enabled},
					{Name: lo.ToPtr("Compute Instance Monitoring"), DesiredState: enabled},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildAgentConfig(tt.in)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestProvider_GetInstanceCompartment(t *testing.T) {
	p := &DefaultProvider{clusterCompartmentId: "cluster-compartment"}
	tests := []struct {
		name     string
		override *string
		want     string
	}{
		{"default", nil, "cluster-compartment"},
		{"override", lo.ToPtr("custom-comp"), "custom-comp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nc := &ociv1beta1.OCINodeClass{}
			if tt.override != nil {
				nc.Spec.NodeCompartmentId = tt.override
			}
			assert.Equal(t, tt.want, p.GetInstanceCompartment(nc))
		})
	}
}

func TestNewProviderRequiresDriftCaches(t *testing.T) {
	_, err := NewProvider(context.Background(), nil, nil, "", nil, nil,
		0, 0, false, 0, nil, false)
	require.ErrorIs(t, err, errDriftCachesRequired)
}

func TestProvider_IsInstanceTerminated(t *testing.T) {
	tests := []struct {
		name string
		in   *InstanceInfo
		want bool
	}{
		{"nil", nil, true},
		{
			"terminated",
			&InstanceInfo{
				Instance: &ocicore.Instance{
					LifecycleState: ocicore.InstanceLifecycleStateTerminated,
				},
			},
			true,
		},
		{"running", &InstanceInfo{Instance: &ocicore.Instance{LifecycleState: ocicore.InstanceLifecycleStateRunning}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsInstanceTerminated(tt.in))
		})
	}
}

func TestProvider_BuildShapeConfigFromInstanceType(t *testing.T) {
	tests := []struct {
		name string
		it   *instancetype.OciInstanceType
		want *ocicore.LaunchInstanceShapeConfigDetails
	}{
		{
			name: "no support",
			it:   &instancetype.OciInstanceType{SupportShapeConfig: false},
			want: nil,
		},
		{
			name: "supported baseline nil",
			it: &instancetype.OciInstanceType{
				SupportShapeConfig: true,
				Ocpu:               lo.ToPtr(float32(4)),
				MemoryInGbs:        lo.ToPtr(float32(16)),
			},
			want: &ocicore.LaunchInstanceShapeConfigDetails{Ocpus: lo.ToPtr(float32(4)), MemoryInGBs: lo.ToPtr(float32(16))},
		},
		{
			name: "supported baseline set",
			it: &instancetype.OciInstanceType{
				SupportShapeConfig:      true,
				Ocpu:                    lo.ToPtr(float32(8)),
				MemoryInGbs:             lo.ToPtr(float32(32)),
				BaselineOcpuUtilization: lo.ToPtr(ociv1beta1.BASELINE_1_2),
			},
			want: &ocicore.LaunchInstanceShapeConfigDetails{
				Ocpus:                   lo.ToPtr(float32(8)),
				MemoryInGBs:             lo.ToPtr(float32(32)),
				BaselineOcpuUtilization: instancetype.ToLaunchInstanceCpuBaseline(ociv1beta1.BASELINE_1_2),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildShapeConfigFromInstanceType(tt.it)
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			assert.NotNil(t, got)
			assert.Equal(t, *tt.want.Ocpus, *got.Ocpus)
			assert.Equal(t, *tt.want.MemoryInGBs, *got.MemoryInGBs)
			assert.Equal(t, tt.want.BaselineOcpuUtilization, got.BaselineOcpuUtilization)
		})
	}
}

func TestProvider_DecideCapacityType(t *testing.T) {
	makeOffering := func(capType string) *cloudprovider.Offering {
		return &cloudprovider.Offering{
			Available: true,
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(corev1.CapacityTypeLabelKey, v1.NodeSelectorOpIn, capType),
			),
			Price: 0.1,
		}
	}
	tests := []struct {
		name        string
		isBurstable bool
		claimReqs   []corev1.NodeSelectorRequirementWithMinValues
		offerings   cloudprovider.Offerings
		want        string
	}{
		{
			name:        "spot when compatible offering exists",
			isBurstable: false,
			claimReqs: []corev1.NodeSelectorRequirementWithMinValues{
				{Key: corev1.CapacityTypeLabelKey, Operator: v1.NodeSelectorOpIn, Values: []string{corev1.CapacityTypeSpot}},
			},
			offerings: cloudprovider.Offerings{makeOffering(corev1.CapacityTypeSpot)},
			want:      corev1.CapacityTypeSpot,
		},
		{
			name:        "on-demand when burstable",
			isBurstable: true,
			claimReqs: []corev1.NodeSelectorRequirementWithMinValues{
				{Key: corev1.CapacityTypeLabelKey, Operator: v1.NodeSelectorOpIn, Values: []string{corev1.CapacityTypeSpot}},
			},
			offerings: cloudprovider.Offerings{makeOffering(corev1.CapacityTypeSpot)},
			want:      corev1.CapacityTypeOnDemand,
		},
		{
			name:        "on-demand when no spot in requirements",
			isBurstable: false,
			claimReqs:   nil,
			offerings:   cloudprovider.Offerings{makeOffering(corev1.CapacityTypeSpot)},
			want:        corev1.CapacityTypeSpot,
		},
		{
			name:        "on-demand when no compatible offering",
			isBurstable: false,
			claimReqs: []corev1.NodeSelectorRequirementWithMinValues{
				{Key: corev1.CapacityTypeLabelKey, Operator: v1.NodeSelectorOpIn, Values: []string{corev1.CapacityTypeSpot}},
			},
			offerings: cloudprovider.Offerings{makeOffering(corev1.CapacityTypeOnDemand)},
			want:      corev1.CapacityTypeOnDemand,
		},
		{
			name:        "on-demand when spot offering is unavailable",
			isBurstable: false,
			claimReqs: []corev1.NodeSelectorRequirementWithMinValues{
				{Key: corev1.CapacityTypeLabelKey, Operator: v1.NodeSelectorOpIn, Values: []string{corev1.CapacityTypeSpot}},
			},
			offerings: cloudprovider.Offerings{
				{
					Available: false,
					Requirements: scheduling.NewRequirements(
						scheduling.NewRequirement(corev1.CapacityTypeLabelKey, v1.NodeSelectorOpIn,
							corev1.CapacityTypeSpot),
					),
					Price: 0.1,
				},
				makeOffering(corev1.CapacityTypeOnDemand),
			},
			want: corev1.CapacityTypeOnDemand,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nc := &corev1.NodeClaim{Spec: corev1.NodeClaimSpec{Requirements: tt.claimReqs}}
			it := &instancetype.OciInstanceType{}
			if tt.isBurstable {
				it.BaselineOcpuUtilization = lo.ToPtr(ociv1beta1.BASELINE_1_2)
			}
			it.Offerings = tt.offerings
			got := decideCapacityType(context.Background(), nc, it)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestProvider_GetCapacityTypeFromInstance(t *testing.T) {
	tests := []struct {
		name string
		in   *ocicore.Instance
		want string
	}{
		{"on-demand by default", &ocicore.Instance{}, corev1.CapacityTypeOnDemand},
		{"spot when preemptible", &ocicore.Instance{
			PreemptibleInstanceConfig: &ocicore.PreemptibleInstanceConfigDetails{
				PreemptionAction: &ocicore.TerminatePreemptionAction{}}},
			corev1.CapacityTypeSpot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, getCapacityTypeFromInstance(tt.in))
		})
	}
}

func TestProvider_GetNodePoolAndClassHashFromInstance(t *testing.T) {
	inst := &ocicore.Instance{
		FreeformTags: map[string]string{
			NodePoolOciFreeFormTagKey:      "np",
			NodePoolUIDOciFreeFormTagKey:   "nodepool-uid",
			NodeClassHashOciFreeFormTagKey: "hash",
		},
	}
	v, ok := GetNodePoolNameFromInstance(inst)
	assert.True(t, ok)
	assert.Equal(t, "np", v)

	h, ok := GetNodeClassHashFromInstance(inst)
	assert.True(t, ok)
	assert.Equal(t, "hash", h)

	nodePoolUID, ok := GetNodePoolUIDFromInstance(inst)
	assert.True(t, ok)
	assert.Equal(t, "nodepool-uid", nodePoolUID)

	empty := &ocicore.Instance{}
	_, ok = GetNodePoolNameFromInstance(empty)
	assert.False(t, ok)
	_, ok = GetNodeClassHashFromInstance(empty)
	assert.False(t, ok)
	_, ok = GetNodePoolUIDFromInstance(empty)
	assert.False(t, ok)
}

func TestProvider_BuildFreeFormTags(t *testing.T) {
	nc := &ociv1beta1.OCINodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				ociv1beta1.NodeClassHash: "hash123",
			},
		},
		Spec: ociv1beta1.OCINodeClassSpec{
			FreeformTags: map[string]string{"a": "1"},
		},
	}
	nclaim := &corev1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				corev1.NodePoolLabelKey: "pool1",
				ociv1beta1.NodeClass:    "class1",
			},
			OwnerReferences: []metav1.OwnerReference{nodePoolOwnerReference("pool1")},
		},
	}
	got, err := buildFreeFormTags(nc, nclaim)
	require.NoError(t, err)
	_, ok := got["a"]
	assert.True(t, ok)
	assert.Equal(t, "1", got["a"])
	assert.Equal(t, "pool1", got[NodePoolOciFreeFormTagKey])
	assert.Equal(t, "nodepool-uid", got[NodePoolUIDOciFreeFormTagKey])
	assert.Equal(t, "class1", got[NodeClassOciFreeFormTagKey])
	assert.Equal(t, "hash123", got[NodeClassHashOciFreeFormTagKey])
}

func TestProvider_BuildFreeFormTagsRequiresNodePoolOwnerReferenceUID(t *testing.T) {
	got, err := buildFreeFormTags(minimalNodeClass(), &corev1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nc-missing-owner",
			Labels: map[string]string{
				corev1.NodePoolLabelKey: "pool1",
				ociv1beta1.NodeClass:    "class1",
			},
		},
	})
	require.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "missing NodePool owner reference UID")
}

func TestProvider_DecorateNodeClaimByInstance(t *testing.T) {
	now := time.Now()
	{
		inst := &ocicore.Instance{
			DisplayName:        lo.ToPtr("inst-name"),
			AvailabilityDomain: lo.ToPtr("tenancy:PHX-AD-1"),
			FaultDomain:        lo.ToPtr("FAULT-DOMAIN-1"),
			LifecycleState:     ocicore.InstanceLifecycleStateTerminating,
			TimeCreated:        &common.SDKTime{Time: now},
			Id:                 lo.ToPtr("ocid1.instance.oc1..abcd"),
			FreeformTags: map[string]string{
				NodePoolOciFreeFormTagKey:      "poolA",
				NodeClassHashOciFreeFormTagKey: "h1",
			},
			SourceDetails: ocicore.InstanceSourceViaImageDetails{ImageId: lo.ToPtr("ocid1.image.oc1..img")},
		}
		nc := &corev1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{}}}
		DecorateNodeClaimByInstance(nc, inst)

		assert.Equal(t, "inst-name", nc.Name)
		assert.Equal(t, "PHX-AD-1", nc.Labels[v1.LabelTopologyZone])
		assert.Equal(t, "FAULT-DOMAIN-1", nc.Labels[ociv1beta1.OciFaultDomain])
		assert.Equal(t, corev1.CapacityTypeOnDemand, nc.Labels[corev1.CapacityTypeLabelKey])
		assert.Equal(t, "poolA", nc.Labels[corev1.NodePoolLabelKey])
		assert.Equal(t, "h1", nc.Labels[ociv1beta1.NodeClassHash])
		assert.Equal(t, now, nc.CreationTimestamp.Time)
		assert.NotNil(t, nc.DeletionTimestamp)
		assert.Equal(t, "ocid1.instance.oc1..abcd", nc.Status.ProviderID)
		assert.Equal(t, "ocid1.image.oc1..img", nc.Status.ImageID)
	}
}

func TestProvider_BuildCreateVnicDetails(t *testing.T) {

	testCases := []struct {
		testSubnetId        string
		nsgIds              []string
		resolveIpv6Ip       *bool
		assignIpv6Ip        *bool
		assignPublicIp      *bool
		vnicDisplayName     *string
		ipv6IpCidrPairs     *[]string
		skipSourceDestCheck *bool
		securityAttributes  map[string]map[string]string
	}{
		// Nothing is set
		{"testSubnetId1", nil, nil, nil, nil, nil, nil, nil, nil},
		// Taking Ipv6Ip from network resolution result
		{"testSubnetId1", nil,
			lo.ToPtr(true), nil,
			nil, nil, nil, nil, nil},
		// Taking Ipv6Ip from customer input
		{"testSubnetId1", nil,
			lo.ToPtr(false), lo.ToPtr(true),
			nil, nil, nil, nil, nil},
		// Setting skipSourceDestCheck to false
		{"testSubnetId1", nil, nil, nil, nil, nil, nil,
			lo.ToPtr(false), nil},
		// All value is set
		{"testSubnetId1",
			[]string{"nsg1", "nsg2"},
			lo.ToPtr(true),
			lo.ToPtr(true),
			lo.ToPtr(true),
			lo.ToPtr("testVnic1"),
			lo.ToPtr([]string{"1.0.1.10/16", "1.0.2.10/24"}),
			lo.ToPtr(true),
			map[string]map[string]string{"test1": {"inner1": "value1"},
				"test2": {"inner2": "value2", "inner3": "value3"}}},
	}
	for _, tc := range testCases {
		networkSecurityGroups := npn.NsgIdsToNetworkSecurityGroupObjects(tc.nsgIds)

		networkResolveResult := &network.NetworkResolveResult{
			PrimaryVnicSubnet: &network.SubnetAndNsgs{
				Subnet: &ocicore.Subnet{
					Id: &tc.testSubnetId,
				},
				NetworkSecurityGroups: networkSecurityGroups,
				AllocateIPv6:          tc.resolveIpv6Ip,
			},
		}

		primaryVnicConfig := &ociv1beta1.SimpleVnicConfig{
			VnicDisplayName:                      tc.vnicDisplayName,
			AssignIpV6Ip:                         tc.assignIpv6Ip,
			AssignPublicIp:                       tc.assignPublicIp,
			SkipSourceDestCheck:                  tc.skipSourceDestCheck,
			SecurityAttributes:                   tc.securityAttributes,
			Ipv6AddressIpv6SubnetCidrPairDetails: npn.StringArrayToIpv6AddressIpv6SubnetCidrPairs(tc.ipv6IpCidrPairs),
		}

		expectedAssignIpv6Ip := tc.resolveIpv6Ip
		if tc.assignIpv6Ip != nil {
			expectedAssignIpv6Ip = tc.assignIpv6Ip
		}

		expectedSkipSourceDestCheck := lo.ToPtr(true)
		if tc.skipSourceDestCheck != nil {
			expectedSkipSourceDestCheck = tc.skipSourceDestCheck
		}

		result := buildCreateVnicDetails(networkResolveResult, primaryVnicConfig)
		assert.Equal(t, tc.vnicDisplayName, result.DisplayName)
		assert.Equal(t, expectedAssignIpv6Ip, result.AssignIpv6Ip)
		assert.Equal(t, tc.assignPublicIp, result.AssignPublicIp)
		assert.Equal(t, expectedSkipSourceDestCheck, result.SkipSourceDestCheck)

		if tc.ipv6IpCidrPairs == nil {
			assert.True(t, len(result.Ipv6AddressIpv6SubnetCidrPairDetails) == 0)
		} else {
			for index, value := range result.Ipv6AddressIpv6SubnetCidrPairDetails {
				assert.Equal(t, (*tc.ipv6IpCidrPairs)[index], *value.Ipv6SubnetCidr)
			}
		}

		if tc.securityAttributes == nil {
			assert.True(t, result.SecurityAttributes == nil)
		} else {
			for k, v := range tc.securityAttributes {
				for k1, value1 := range v {
					assert.Equal(t, value1, result.SecurityAttributes[k][k1])
				}
			}
		}
	}
}

func TestProvider_FreshReadFailuresPreserveCachedDriftValues(t *testing.T) {
	ctx := context.Background()
	failFreshReads := false
	instanceDisplayName := "old"
	vnicID := "vnic-1"
	bootVolumeID := "boot-volume-1"
	fc := &fakes.FakeCompute{}
	fc.OnGet = func(context.Context, ocicore.GetInstanceRequest) (ocicore.GetInstanceResponse, error) {
		if failFreshReads {
			return ocicore.GetInstanceResponse{}, errors.New("temporary instance read failure")
		}
		return ocicore.GetInstanceResponse{
			Instance: ocicore.Instance{
				Id:             lo.ToPtr("instance-1"),
				DisplayName:    lo.ToPtr(instanceDisplayName),
				LifecycleState: ocicore.InstanceLifecycleStateRunning,
			},
			Etag: lo.ToPtr("etag-1"),
		}, nil
	}
	fc.OnListVnics = func(context.Context, ocicore.ListVnicAttachmentsRequest) (
		ocicore.ListVnicAttachmentsResponse, error) {
		if failFreshReads {
			return ocicore.ListVnicAttachmentsResponse{}, errors.New("temporary VNIC attachment read failure")
		}
		return ocicore.ListVnicAttachmentsResponse{Items: []ocicore.VnicAttachment{{
			VnicId: lo.ToPtr(vnicID),
		}}}, nil
	}
	fc.OnListBoot = func(context.Context, ocicore.ListBootVolumeAttachmentsRequest) (
		ocicore.ListBootVolumeAttachmentsResponse, error) {
		if failFreshReads {
			return ocicore.ListBootVolumeAttachmentsResponse{}, errors.New("temporary boot attachment read failure")
		}
		return ocicore.ListBootVolumeAttachmentsResponse{Items: []ocicore.BootVolumeAttachment{{
			BootVolumeId: lo.ToPtr(bootVolumeID),
		}}}, nil
	}

	driftCaches := NewDriftCaches()
	p := &DefaultProvider{
		computeClient:      fc,
		instanceCache:      driftCaches.instances,
		vnicAttachCache:    driftCaches.vnicAttachments,
		bootVolAttachCache: driftCaches.bootVolumeAttachments,
	}

	instanceInfo, err := p.GetInstanceCached(ctx, "instance-1")
	require.NoError(t, err)
	vnicAttachments, err := p.ListInstanceVnicAttachmentsCached(ctx, "compartment-1", "instance-1")
	require.NoError(t, err)
	bootAttachments, err := p.ListInstanceBootVolumeAttachmentsCached(ctx, "compartment-1", "instance-1", "ad-1")
	require.NoError(t, err)
	assert.Equal(t, "old", *instanceInfo.DisplayName)
	assert.Equal(t, "vnic-1", *vnicAttachments[0].VnicId)
	assert.Equal(t, "boot-volume-1", *bootAttachments[0].BootVolumeId)
	assert.Equal(t, 1, fc.GetCount.Get())
	assert.Equal(t, 1, fc.ListVnicCount.Get())
	assert.Equal(t, 1, fc.ListBootCount.Get())
	oldVnic := &ocicore.Vnic{Id: lo.ToPtr(vnicID)}
	oldBootVolume := &ocicore.BootVolume{Id: lo.ToPtr(bootVolumeID)}
	driftCaches.vnics.Set(vnicID, oldVnic)
	driftCaches.bootVolumes.Set(bootVolumeID, oldBootVolume)

	instanceDisplayName = "new"
	vnicID = "vnic-2"
	bootVolumeID = "boot-volume-2"
	cachedInstance, err := p.GetInstanceCached(ctx, "instance-1")
	require.NoError(t, err)
	assert.Same(t, instanceInfo, cachedInstance)
	cachedVnicAttachments, err := p.ListInstanceVnicAttachmentsCached(ctx, "compartment-1", "instance-1")
	require.NoError(t, err)
	assert.Equal(t, vnicAttachments, cachedVnicAttachments)
	cachedBootAttachments, err := p.ListInstanceBootVolumeAttachmentsCached(ctx, "compartment-1", "instance-1", "ad-1")
	require.NoError(t, err)
	assert.Equal(t, bootAttachments, cachedBootAttachments)
	assert.Equal(t, 1, fc.GetCount.Get())
	assert.Equal(t, 1, fc.ListVnicCount.Get())
	assert.Equal(t, 1, fc.ListBootCount.Get())

	instanceInfo, err = p.GetInstance(ctx, "instance-1")
	require.NoError(t, err)
	vnicAttachments, err = p.ListInstanceVnicAttachments(ctx, "compartment-1", "instance-1")
	require.NoError(t, err)
	bootAttachments, err = p.ListInstanceBootVolumeAttachments(ctx, "compartment-1", "instance-1", "ad-1")
	require.NoError(t, err)
	assert.Equal(t, "new", *instanceInfo.DisplayName)
	assert.Equal(t, "vnic-2", *vnicAttachments[0].VnicId)
	assert.Equal(t, "boot-volume-2", *bootAttachments[0].BootVolumeId)
	assert.Equal(t, 2, fc.GetCount.Get())
	assert.Equal(t, 2, fc.ListVnicCount.Get())
	assert.Equal(t, 2, fc.ListBootCount.Get())
	assertCacheEntryReloads(t, ctx, driftCaches.vnics, "vnic-1", oldVnic)
	assertCacheEntryReloads(t, ctx, driftCaches.bootVolumes, "boot-volume-1", oldBootVolume)
	driftCaches.vnics.Evict("vnic-1")
	driftCaches.bootVolumes.Evict("boot-volume-1")
	newVnic := &ocicore.Vnic{Id: lo.ToPtr("vnic-2")}
	newBootVolume := &ocicore.BootVolume{Id: lo.ToPtr("boot-volume-2")}
	driftCaches.vnics.Set("vnic-2", newVnic)
	driftCaches.bootVolumes.Set("boot-volume-2", newBootVolume)

	failFreshReads = true
	_, err = p.GetInstance(ctx, "instance-1")
	require.Error(t, err)
	_, err = p.ListInstanceVnicAttachments(ctx, "compartment-1", "instance-1")
	require.Error(t, err)
	_, err = p.ListInstanceBootVolumeAttachments(ctx, "compartment-1", "instance-1", "ad-1")
	require.Error(t, err)
	assert.Equal(t, 3, fc.GetCount.Get())
	assert.Equal(t, 3, fc.ListVnicCount.Get())
	assert.Equal(t, 3, fc.ListBootCount.Get())

	cachedInstance, err = p.GetInstanceCached(ctx, "instance-1")
	require.NoError(t, err)
	assert.Same(t, instanceInfo, cachedInstance)
	cachedVnicAttachments, err = p.ListInstanceVnicAttachmentsCached(ctx, "compartment-1", "instance-1")
	require.NoError(t, err)
	assert.Equal(t, vnicAttachments, cachedVnicAttachments)
	cachedBootAttachments, err = p.ListInstanceBootVolumeAttachmentsCached(ctx, "compartment-1", "instance-1", "ad-1")
	require.NoError(t, err)
	assert.Equal(t, bootAttachments, cachedBootAttachments)
	assertCacheEntryRetained(t, ctx, driftCaches.vnics, "vnic-2", newVnic)
	assertCacheEntryRetained(t, ctx, driftCaches.bootVolumes, "boot-volume-2", newBootVolume)
	assert.Equal(t, 3, fc.GetCount.Get())
	assert.Equal(t, 3, fc.ListVnicCount.Get())
	assert.Equal(t, 3, fc.ListBootCount.Get())
}

func TestProvider_InstanceReadsEvictGoneInstances(t *testing.T) {
	testCases := []struct {
		name       string
		cachedRead bool
		notFound   bool
	}{
		{name: "fresh NotFound", notFound: true},
		{name: "fresh terminated"},
		{name: "cached NotFound", cachedRead: true, notFound: true},
		{name: "cached terminated", cachedRead: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			const instanceID = "instance-gone"
			driftCaches := NewDriftCaches()
			entries := driftCacheEntriesForInstance(instanceID)
			if testCase.cachedRead {
				seedDriftChildCacheEntries(driftCaches, instanceID, entries)
			} else {
				seedDriftCacheEntries(driftCaches, instanceID)
			}
			fc := &fakes.FakeCompute{
				OnGet: func(context.Context, ocicore.GetInstanceRequest) (ocicore.GetInstanceResponse, error) {
					if testCase.notFound {
						return ocicore.GetInstanceResponse{}, fakeServiceError{
							httpStatus: http.StatusNotFound,
							code:       "NotAuthorizedOrNotFound",
							message:    "missing",
						}
					}
					terminated := *entries.instance.Instance
					terminated.LifecycleState = ocicore.InstanceLifecycleStateTerminated
					return ocicore.GetInstanceResponse{Instance: terminated, Etag: lo.ToPtr("etag")}, nil
				},
			}
			p := &DefaultProvider{
				computeClient:      fc,
				instanceCache:      driftCaches.instances,
				vnicAttachCache:    driftCaches.vnicAttachments,
				bootVolAttachCache: driftCaches.bootVolumeAttachments,
			}

			var err error
			if testCase.cachedRead {
				_, err = p.GetInstanceCached(ctx, instanceID)
			} else {
				_, err = p.GetInstance(ctx, instanceID)
			}
			if testCase.notFound {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assertDriftCacheEntriesEvicted(t, ctx, driftCaches, entries)
		})
	}
}

func TestProvider_DeleteInstanceEvictsDriftCaches(t *testing.T) {
	testCases := []struct {
		name              string
		terminateNotFound bool
	}{
		{name: "terminate NotFound", terminateNotFound: true},
		{name: "completed termination"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			const instanceID = "instance-deleted"
			driftCaches := NewDriftCaches()
			entries := seedDriftCacheEntries(driftCaches, instanceID)
			fc := &fakes.FakeCompute{
				OnTerminate: func(context.Context, ocicore.TerminateInstanceRequest) (
					ocicore.TerminateInstanceResponse, error) {
					if testCase.terminateNotFound {
						return ocicore.TerminateInstanceResponse{}, fakeServiceError{
							httpStatus: http.StatusNotFound,
							code:       "NotAuthorizedOrNotFound",
							message:    "missing",
						}
					}
					return ocicore.TerminateInstanceResponse{}, nil
				},
				OnGet: func(context.Context, ocicore.GetInstanceRequest) (ocicore.GetInstanceResponse, error) {
					terminated := *entries.instance.Instance
					terminated.LifecycleState = ocicore.InstanceLifecycleStateTerminated
					return ocicore.GetInstanceResponse{Instance: terminated, Etag: lo.ToPtr("etag")}, nil
				},
			}
			p := &DefaultProvider{
				computeClient:      fc,
				instanceCache:      driftCaches.instances,
				vnicAttachCache:    driftCaches.vnicAttachments,
				bootVolAttachCache: driftCaches.bootVolumeAttachments,
				pollInterval:       time.Millisecond,
			}

			err := p.DeleteInstance(ctx, instanceID)
			if testCase.terminateNotFound {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Zero(t, fc.ListVnicCount.Get())
			assert.Zero(t, fc.ListBootCount.Get())
			assertDriftCacheEntriesEvicted(t, ctx, driftCaches, entries)
		})
	}
}

func TestProvider_ListInstancesAggregatesAndEvictsUnseen(t *testing.T) {
	ctx := context.Background()
	const (
		clusterCompartment = "compartment-cluster"
		otherCompartment   = "compartment-other"
		seenInstanceAID    = "instance-seen-a"
		seenInstanceBID    = "instance-seen-b"
		unseenInstanceID   = "instance-unseen"
		orphanInstanceID   = "instance-orphan"
	)
	driftCaches := NewDriftCaches()
	seenA := seedDriftCacheEntries(driftCaches, seenInstanceAID)
	seenB := driftCacheEntriesForInstance(seenInstanceBID)
	unseen := seedDriftCacheEntries(driftCaches, unseenInstanceID)
	orphan := driftCacheEntriesForInstance(orphanInstanceID)
	seedDriftChildCacheEntries(driftCaches, orphanInstanceID, orphan)
	fc := &fakes.FakeCompute{
		OnListInstances: func(_ context.Context,
			request ocicore.ListInstancesRequest) (ocicore.ListInstancesResponse, error) {
			switch lo.FromPtr(request.CompartmentId) {
			case clusterCompartment:
				return ocicore.ListInstancesResponse{Items: []ocicore.Instance{*seenA.instance.Instance}}, nil
			case otherCompartment:
				return ocicore.ListInstancesResponse{Items: []ocicore.Instance{*seenB.instance.Instance}}, nil
			default:
				return ocicore.ListInstancesResponse{}, fmt.Errorf("unexpected compartment")
			}
		},
	}
	p := &DefaultProvider{
		computeClient:        fc,
		clusterCompartmentId: clusterCompartment,
		instanceCache:        driftCaches.instances,
		vnicAttachCache:      driftCaches.vnicAttachments,
		bootVolAttachCache:   driftCaches.bootVolumeAttachments,
	}

	instances, err := p.ListInstances(ctx, []string{
		"", clusterCompartment, "", otherCompartment, otherCompartment,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, fc.ListInstancesCount.Get())
	assert.ElementsMatch(t, []string{seenInstanceAID, seenInstanceBID},
		lo.Map(instances, func(item *ocicore.Instance, _ int) string { return lo.FromPtr(item.Id) }))
	assertDriftCacheEntriesRetained(t, ctx, driftCaches, seenA)
	assertCacheEntryRetained(t, ctx, driftCaches.instances, seenInstanceBID, seenB.instance)
	assertDriftCacheEntriesEvicted(t, ctx, driftCaches, unseen)
	assertDriftCacheEntriesEvicted(t, ctx, driftCaches, orphan)
}

func TestProvider_ListInstancesFailureDoesNotPrune(t *testing.T) {
	ctx := context.Background()
	driftCaches := NewDriftCaches()
	retained := seedDriftCacheEntries(driftCaches, "instance-retained")
	listed := driftCacheEntriesForInstance("instance-listed")
	fc := &fakes.FakeCompute{
		OnListInstances: func(_ context.Context,
			request ocicore.ListInstancesRequest) (ocicore.ListInstancesResponse, error) {
			if lo.FromPtr(request.CompartmentId) == "compartment-b" {
				return ocicore.ListInstancesResponse{}, errors.New("list failed")
			}
			return ocicore.ListInstancesResponse{Items: []ocicore.Instance{*listed.instance.Instance}}, nil
		},
	}
	p := &DefaultProvider{
		computeClient:      fc,
		instanceCache:      driftCaches.instances,
		vnicAttachCache:    driftCaches.vnicAttachments,
		bootVolAttachCache: driftCaches.bootVolumeAttachments,
	}

	_, err := p.ListInstances(ctx, []string{"compartment-a", "compartment-b"})
	require.EqualError(t, err, "list failed")
	assert.Equal(t, 2, fc.ListInstancesCount.Get())
	assertDriftCacheEntriesRetained(t, ctx, driftCaches, retained)
	assertCacheEntryRetained(t, ctx, driftCaches.instances, "instance-listed", listed.instance)
}

func TestProvider_ListInstancesEmptyScopeEvictsAll(t *testing.T) {
	ctx := context.Background()
	driftCaches := NewDriftCaches()
	cached := seedDriftCacheEntries(driftCaches, "instance-cached")
	orphan := driftCacheEntriesForInstance("instance-orphan")
	seedDriftChildCacheEntries(driftCaches, "instance-orphan", orphan)
	fc := &fakes.FakeCompute{}
	p := &DefaultProvider{
		computeClient:      fc,
		instanceCache:      driftCaches.instances,
		vnicAttachCache:    driftCaches.vnicAttachments,
		bootVolAttachCache: driftCaches.bootVolumeAttachments,
	}

	instances, err := p.ListInstances(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, instances)
	assert.Zero(t, fc.ListInstancesCount.Get())
	assertDriftCacheEntriesEvicted(t, ctx, driftCaches, cached)
	assertDriftCacheEntriesEvicted(t, ctx, driftCaches, orphan)
}

type driftCacheEntries struct {
	instance              *InstanceInfo
	vnicAttachments       []*ocicore.VnicAttachment
	bootVolumeAttachments []*ocicore.BootVolumeAttachment
	vnic                  *ocicore.Vnic
	bootVolume            *ocicore.BootVolume
}

func driftCacheEntriesForInstance(instanceID string) driftCacheEntries {
	vnicID := instanceID + "-vnic"
	bootVolumeID := instanceID + "-boot"
	return driftCacheEntries{
		instance: &InstanceInfo{Instance: &ocicore.Instance{
			Id:             lo.ToPtr(instanceID),
			LifecycleState: ocicore.InstanceLifecycleStateRunning,
			FreeformTags:   map[string]string{NodePoolOciFreeFormTagKey: "nodepool"},
		}},
		vnicAttachments: []*ocicore.VnicAttachment{{VnicId: lo.ToPtr(vnicID)}},
		bootVolumeAttachments: []*ocicore.BootVolumeAttachment{{
			BootVolumeId: lo.ToPtr(bootVolumeID),
		}},
		vnic:       &ocicore.Vnic{Id: lo.ToPtr(vnicID)},
		bootVolume: &ocicore.BootVolume{Id: lo.ToPtr(bootVolumeID)},
	}
}

func seedDriftCacheEntries(caches *DriftCaches, instanceID string) driftCacheEntries {
	entries := driftCacheEntriesForInstance(instanceID)
	caches.instances.Set(instanceID, entries.instance)
	seedDriftChildCacheEntries(caches, instanceID, entries)
	return entries
}

func seedDriftChildCacheEntries(caches *DriftCaches, instanceID string, entries driftCacheEntries) {
	caches.vnicAttachments.Set(instanceID, entries.vnicAttachments)
	caches.bootVolumeAttachments.Set(instanceID, entries.bootVolumeAttachments)
	caches.vnics.Set(lo.FromPtr(entries.vnic.Id), entries.vnic)
	caches.bootVolumes.Set(lo.FromPtr(entries.bootVolume.Id), entries.bootVolume)
}

func assertDriftCacheEntriesRetained(t *testing.T, ctx context.Context, caches *DriftCaches,
	entries driftCacheEntries) {
	t.Helper()
	instanceID := lo.FromPtr(entries.instance.Id)
	assertCacheEntryRetained(t, ctx, caches.instances, instanceID, entries.instance)
	assertCacheEntryRetained(t, ctx, caches.vnicAttachments, instanceID, entries.vnicAttachments)
	assertCacheEntryRetained(t, ctx, caches.bootVolumeAttachments, instanceID, entries.bootVolumeAttachments)
	assertCacheEntryRetained(t, ctx, caches.vnics, lo.FromPtr(entries.vnic.Id), entries.vnic)
	assertCacheEntryRetained(t, ctx, caches.bootVolumes, lo.FromPtr(entries.bootVolume.Id), entries.bootVolume)
}

func assertDriftCacheEntriesEvicted(t *testing.T, ctx context.Context, caches *DriftCaches,
	entries driftCacheEntries) {
	t.Helper()
	instanceID := lo.FromPtr(entries.instance.Id)
	assertCacheEntryReloads(t, ctx, caches.instances, instanceID, entries.instance)
	assertCacheEntryReloads(t, ctx, caches.vnicAttachments, instanceID, entries.vnicAttachments)
	assertCacheEntryReloads(t, ctx, caches.bootVolumeAttachments, instanceID, entries.bootVolumeAttachments)
	assertCacheEntryReloads(t, ctx, caches.vnics, lo.FromPtr(entries.vnic.Id), entries.vnic)
	assertCacheEntryReloads(t, ctx, caches.bootVolumes, lo.FromPtr(entries.bootVolume.Id), entries.bootVolume)
}

func assertCacheEntryReloads[T any](t *testing.T, ctx context.Context, c *cache.GetOrLoadCache[T],
	key string, value T) {
	t.Helper()
	loads := 0
	_, err := c.GetOrLoad(ctx, key, func(context.Context, string) (T, error) {
		loads++
		return value, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, loads)
}

func assertCacheEntryRetained[T any](t *testing.T, ctx context.Context, c *cache.GetOrLoadCache[T],
	key string, value T) {
	t.Helper()
	loads := 0
	loaded, err := c.GetOrLoad(ctx, key, func(context.Context, string) (T, error) {
		loads++
		return value, nil
	})
	require.NoError(t, err)
	assert.Equal(t, value, loaded)
	assert.Zero(t, loads)
}

func TestProvider_ListInstancesFiltersAndWritesThrough(t *testing.T) {
	fc := &fakes.FakeCompute{}
	fc.OnListInstances = func(context.Context,
		ocicore.ListInstancesRequest) (ocicore.ListInstancesResponse, error) {
		return ocicore.ListInstancesResponse{
			Items: []ocicore.Instance{
				{Id: lo.ToPtr("ocid1.instance.oc1..term"), DisplayName: lo.ToPtr("term"),
					AvailabilityDomain: lo.ToPtr("tenancy:PHX-AD-1"),
					LifecycleState:     ocicore.InstanceLifecycleStateTerminated,
					FreeformTags: map[string]string{
						NodePoolOciFreeFormTagKey: "poolA", NodeClassHashOciFreeFormTagKey: "h"}},
				{Id: lo.ToPtr("ocid1.instance.oc1..ok"), DisplayName: lo.ToPtr("ok"),
					AvailabilityDomain: lo.ToPtr("tenancy:PHX-AD-1"),
					LifecycleState:     ocicore.InstanceLifecycleStateRunning,
					FreeformTags: map[string]string{
						NodePoolOciFreeFormTagKey: "poolA", NodeClassHashOciFreeFormTagKey: "h"},
					SourceDetails: ocicore.InstanceSourceViaImageDetails{ImageId: lo.ToPtr("ocid1.image.oc1..img")}},
			},
		}, nil
	}

	p := &DefaultProvider{
		computeClient:        fc,
		clusterCompartmentId: "ocid1.compartment.oc1..parent",
		instanceCache:        cache.NewDefaultGetOrLoadCache[*InstanceInfo](),
	}

	ctx := context.TODO()
	insts, err := p.ListInstances(ctx, []string{""})
	assert.NoError(t, err)
	assert.Len(t, insts, 1)
	getCountAfterList := fc.GetCount.Get()
	listedInfo, err := p.GetInstanceCached(ctx, "ocid1.instance.oc1..ok")
	assert.NoError(t, err)
	assert.Equal(t, "ocid1.instance.oc1..ok", *listedInfo.Id)
	assert.Equal(t, getCountAfterList, fc.GetCount.Get(), "ListInstances should write through")
}

func TestProvider_LaunchInstance_RequestConstruction(t *testing.T) {
	// NOTE: This test's sub-tests share mutable fakes; running them in
	// parallel caused data races and nil-pointer panics.  Disable parallel
	// execution so each sub-test runs sequentially.
	// t.Parallel()

	// Shared minimal inputs
	baseNodeClass := minimalNodeClass()
	baseClaim := minimalNodeClaim()
	imgRes := minimalImageResolve()
	netRes := minimalNetworkResolve()
	pp := minimalPlacement()

	// Build DefaultProvider with capturing fake
	fc := &fakes.FakeCompute{
		LaunchResp: ocicore.LaunchInstanceResponse{
			Instance:         ocicore.Instance{Id: lo.ToPtr("ocid1.instance.oc1..new")},
			Etag:             lo.ToPtr("etag-new"),
			OpcWorkRequestId: lo.ToPtr("wr1"),
			RawResponse: &http.Response{
				StatusCode: 200,
				Header: http.Header{
					"Opc-Work-Request-Id": []string{"wr1"},
				},
			},
		},
	}
	// Set up fake work request client to avoid polling timeout
	fwr := &fakes.FakeWorkRequest{}
	fwr.OnGet = func(ctx context.Context, r ociwr.GetWorkRequestRequest) (ociwr.GetWorkRequestResponse, error) {
		return ociwr.GetWorkRequestResponse{
			WorkRequest: ociwr.WorkRequest{
				Status:       ociwr.WorkRequestStatusSucceeded,
				TimeFinished: &common.SDKTime{Time: time.Now()},
				TimeStarted:  &common.SDKTime{Time: time.Now()},
			},
		}, nil
	}

	p := &DefaultProvider{
		computeClient:        fc,
		workRequestClient:    fwr,
		clusterCompartmentId: "ocid1.compartment.oc1..parent",
		launchTimeoutVM:      10 * time.Minute,
		launchTimeoutBM:      20 * time.Minute,
		// use real instancemeta provider for metadata construction
	}
	imdsp, err := instancemeta.NewProvider(context.TODO(), "10.0.0.1", []byte("CA"), ipV4SingleStack)
	require.NoError(t, err)
	p.instanceMetaProvider = imdsp

	// Cases
	t.Run("spot for non-burstable with required spot", func(t *testing.T) {
		claim := baseClaim.DeepCopy()
		// require spot
		claim.Spec.Requirements = []corev1.NodeSelectorRequirementWithMinValues{
			{Key: corev1.CapacityTypeLabelKey, Operator: v1.NodeSelectorOpIn, Values: []string{corev1.CapacityTypeSpot}},
		}
		it := &instancetype.OciInstanceType{Shape: "VM.Standard.E4.Flex"}
		it.Offerings = cloudprovider.Offerings{makeOfferingWithCapType(corev1.CapacityTypeSpot)}
		nodeClass := baseNodeClass.DeepCopy()

		_, err := p.LaunchInstance(context.TODO(), claim, nodeClass,
			it, imgRes, netRes, nil, pp)
		require.NoError(t, err)
		require.NotNil(t, fc.LastLaunchReq.LaunchInstanceDetails.PreemptibleInstanceConfig, "expected preemptible config")
	})

	t.Run("on-demand fallback when spot offering is unavailable", func(t *testing.T) {
		claim := baseClaim.DeepCopy()
		// NodePool allows both spot and on-demand, with spot preferred.
		claim.Spec.Requirements = []corev1.NodeSelectorRequirementWithMinValues{
			{Key: corev1.CapacityTypeLabelKey, Operator: v1.NodeSelectorOpIn,
				Values: []string{corev1.CapacityTypeSpot, corev1.CapacityTypeOnDemand}},
		}
		// The shape's only spot offering is out of host capacity (Available=false, as the
		// unavailable-offerings cache surfaces it via setOfferings), leaving on-demand available.
		it := &instancetype.OciInstanceType{Shape: "VM.Standard.E4.Flex"}
		spotUnavailable := makeOfferingWithCapType(corev1.CapacityTypeSpot)
		spotUnavailable.Available = false
		it.Offerings = cloudprovider.Offerings{
			spotUnavailable,
			makeOfferingWithCapType(corev1.CapacityTypeOnDemand),
		}
		nodeClass := baseNodeClass.DeepCopy()

		_, err := p.LaunchInstance(context.TODO(), claim, nodeClass,
			it, imgRes, netRes, nil, pp)
		require.NoError(t, err)
		// decideCapacityType should fall back to on-demand within the same pool, so the launch
		// request must not carry a preemptible (spot) config.
		require.Nil(t, fc.LastLaunchReq.LaunchInstanceDetails.PreemptibleInstanceConfig,
			"expected on-demand fallback (no preemptible config) when the spot offering is unavailable")
	})

	t.Run("non-flexible should not set shape config", func(t *testing.T) {
		claim := baseClaim.DeepCopy()
		claim.Spec.Requirements = []corev1.NodeSelectorRequirementWithMinValues{
			{Key: corev1.CapacityTypeLabelKey, Operator: v1.NodeSelectorOpIn, Values: []string{corev1.CapacityTypeOnDemand}},
		}
		it := &instancetype.OciInstanceType{Shape: "VM.Standard2.1"}
		it.Offerings = cloudprovider.Offerings{makeOfferingWithCapType(corev1.CapacityTypeOnDemand)}
		nodeClass := baseNodeClass.DeepCopy()

		_, err := p.LaunchInstance(context.TODO(), claim, nodeClass,
			it, imgRes, netRes, nil, pp)
		require.NoError(t, err)
		require.Nil(t, fc.LastLaunchReq.LaunchInstanceDetails.ShapeConfig, "did not expect shape config")
	})

	t.Run("flexible should set shape config", func(t *testing.T) {
		claim := baseClaim.DeepCopy()
		claim.Spec.Requirements = []corev1.NodeSelectorRequirementWithMinValues{
			{Key: corev1.CapacityTypeLabelKey, Operator: v1.NodeSelectorOpIn, Values: []string{corev1.CapacityTypeOnDemand}},
		}
		ocpu := float32(1)
		memoryInGbs := float32(16)
		baseline := ociv1beta1.BASELINE_1_2
		it := &instancetype.OciInstanceType{
			Shape:                   "VM.Standard.E5.Flex",
			Ocpu:                    &ocpu,
			MemoryInGbs:             &memoryInGbs,
			BaselineOcpuUtilization: &baseline,
			SupportShapeConfig:      true}
		it.Offerings = cloudprovider.Offerings{makeOfferingWithCapType(corev1.CapacityTypeOnDemand)}
		nodeClass := baseNodeClass.DeepCopy()

		_, err := p.LaunchInstance(context.TODO(), claim, nodeClass,
			it, imgRes, netRes, nil, pp)
		require.NoError(t, err)
		require.NotNil(t, fc.LastLaunchReq.LaunchInstanceDetails.ShapeConfig, "expected shape config")
		require.Equal(t, *fc.LastLaunchReq.LaunchInstanceDetails.ShapeConfig.Ocpus, ocpu, "ocpu should match")
		require.Equal(t, *fc.LastLaunchReq.LaunchInstanceDetails.ShapeConfig.MemoryInGBs, memoryInGbs,
			"memoryInGbs should match")
		require.Equal(t, fc.LastLaunchReq.LaunchInstanceDetails.ShapeConfig.BaselineOcpuUtilization,
			instancetype.ToLaunchInstanceCpuBaseline(baseline), "baselineOcpuUtilization should match")
	})

	t.Run("on-demand for burstable", func(t *testing.T) {
		claim := baseClaim.DeepCopy()
		// still require spot, but burstable cannot be preemptible
		claim.Spec.Requirements = []corev1.NodeSelectorRequirementWithMinValues{
			{Key: corev1.CapacityTypeLabelKey, Operator: v1.NodeSelectorOpIn, Values: []string{corev1.CapacityTypeSpot}},
		}
		it := &instancetype.OciInstanceType{Shape: "VM.Standard.E4.Flex",
			BaselineOcpuUtilization: lo.ToPtr(ociv1beta1.BASELINE_1_2)}
		it.Offerings = cloudprovider.Offerings{makeOfferingWithCapType(corev1.CapacityTypeSpot)}
		nodeClass := baseNodeClass.DeepCopy()

		_, err := p.LaunchInstance(context.TODO(), claim, nodeClass,
			it, imgRes, netRes, nil, pp)
		require.NoError(t, err)
		require.Nil(t, fc.LastLaunchReq.LaunchInstanceDetails.PreemptibleInstanceConfig,
			"burstable should not be preemptible")
	})

	t.Run("boot volume size and KMS key and IMDS/Flags", func(t *testing.T) {
		claim := baseClaim.DeepCopy()
		it := &instancetype.OciInstanceType{Shape: "VM.Standard.E4.Flex"}
		it.Offerings = cloudprovider.Offerings{makeOfferingWithCapType(corev1.CapacityTypeOnDemand)}
		nodeClass := baseNodeClass.DeepCopy()
		// set boot volume size
		gb := int64(120)
		nodeClass.Spec.VolumeConfig.BootVolumeConfig.SizeInGBs = &gb

		vpuPerGb := int64(10)
		nodeClass.Spec.VolumeConfig.BootVolumeConfig.VpusPerGB = &vpuPerGb

		// set PV encryption in transit
		nodeClass.Spec.VolumeConfig.BootVolumeConfig.PvEncryptionInTransit = lo.ToPtr(true)
		// provide kms key
		k := &kms.KmsKeyResolveResult{Ocid: "ocid1.key.oc1.iad.xxxxx"}

		_, err := p.LaunchInstance(context.TODO(), claim, nodeClass,
			it, imgRes, netRes, k, pp)
		require.NoError(t, err)

		// Assert IMDS v1 disabled
		require.NotNil(t, fc.LastLaunchReq.LaunchInstanceDetails.InstanceOptions)
		require.NotNil(t, fc.LastLaunchReq.LaunchInstanceDetails.InstanceOptions.AreLegacyImdsEndpointsDisabled)
		require.True(t, *fc.LastLaunchReq.LaunchInstanceDetails.InstanceOptions.AreLegacyImdsEndpointsDisabled)

		// Assert PV encryption flag
		require.NotNil(t, fc.LastLaunchReq.LaunchInstanceDetails.IsPvEncryptionInTransitEnabled)
		require.True(t, *fc.LastLaunchReq.LaunchInstanceDetails.IsPvEncryptionInTransitEnabled)

		// Assert KMS key and boot size on source details
		src := fc.LastLaunchReq.LaunchInstanceDetails.SourceDetails.(ocicore.InstanceSourceViaImageDetails)
		require.NotNil(t, src.KmsKeyId)
		require.Equal(t, "ocid1.key.oc1.iad.xxxxx", *src.KmsKeyId)
		require.NotNil(t, src.BootVolumeSizeInGBs)
		require.Equal(t, gb, *src.BootVolumeSizeInGBs)
		require.Equal(t, vpuPerGb, *src.BootVolumeVpusPerGB)
	})

	t.Run("vnic ipv6 precedence and tags", func(t *testing.T) {
		claim := baseClaim.DeepCopy()
		// add labels to verify freeform tags
		claim.Labels[corev1.NodePoolLabelKey] = "poolZ"
		claim.Labels[ociv1beta1.NodeClass] = "classZ"

		it := &instancetype.OciInstanceType{Shape: "VM.Standard.E4.Flex"}
		it.Offerings = cloudprovider.Offerings{makeOfferingWithCapType(corev1.CapacityTypeOnDemand)}

		nodeClass := baseNodeClass.DeepCopy()
		// Add NSGs and display name and flags into SimpleVnicConfig
		nsg1 := "nsg1"
		nsg2 := "nsg2"
		nodeClass.Spec.NetworkConfig.PrimaryVnicConfig.SubnetAndNsgConfig.NetworkSecurityGroupConfigs =
			[]*ociv1beta1.NetworkSecurityGroupConfig{
				{
					NetworkSecurityGroupId: &nsg1,
				},
				{
					NetworkSecurityGroupId: &nsg2,
				}}

		nodeClass.Spec.NetworkConfig.PrimaryVnicConfig.VnicDisplayName = lo.ToPtr("vnic-name")
		nodeClass.Spec.NetworkConfig.PrimaryVnicConfig.AssignPublicIp = lo.ToPtr(true)
		nodeClass.Spec.NetworkConfig.PrimaryVnicConfig.SkipSourceDestCheck = lo.ToPtr(true)
		nodeClass.Spec.NetworkConfig.PrimaryVnicConfig.SecurityAttributes = map[string]map[string]string{"s": {"k": "v"}}

		// Network resolution provides AllocateIPv6 = true, SimpleVnicConfig.AssignIpV6Ip == nil -> use network
		nr := &network.NetworkResolveResult{
			PrimaryVnicSubnet: &network.SubnetAndNsgs{
				Subnet:                &ocicore.Subnet{Id: lo.ToPtr("ocid1.subnet.oc1..sn1")},
				NetworkSecurityGroups: npn.NsgIdsToNetworkSecurityGroupObjects([]string{"nsg1", "nsg2"}),
				AllocateIPv6:          lo.ToPtr(true),
			},
		}

		_, err := p.LaunchInstance(context.TODO(), claim, nodeClass,
			it, imgRes, nr, nil, pp)
		require.NoError(t, err)

		// Validate VNIC details precedence
		v := fc.LastLaunchReq.LaunchInstanceDetails.CreateVnicDetails
		require.NotNil(t, v.AssignIpv6Ip)
		require.True(t, *v.AssignIpv6Ip)
		require.NotNil(t, v.NsgIds)
		require.ElementsMatch(t, []string{"nsg1", "nsg2"}, v.NsgIds)
		require.NotNil(t, v.DisplayName)
		require.Equal(t, "vnic-name", *v.DisplayName)
		require.NotNil(t, v.AssignPublicIp)
		require.True(t, *v.AssignPublicIp)
		require.NotNil(t, v.SkipSourceDestCheck)
		require.True(t, *v.SkipSourceDestCheck)
		require.NotNil(t, v.SecurityAttributes)
		require.Equal(t, "v", v.SecurityAttributes["s"]["k"])

		// Validate freeform/defined tags
		require.Equal(t, "poolZ", fc.LastLaunchReq.LaunchInstanceDetails.FreeformTags[NodePoolOciFreeFormTagKey])
		require.Equal(t, "nodepool-uid", fc.LastLaunchReq.LaunchInstanceDetails.FreeformTags[NodePoolUIDOciFreeFormTagKey])
		require.Equal(t, "classZ", fc.LastLaunchReq.LaunchInstanceDetails.FreeformTags[NodeClassOciFreeFormTagKey])
		require.Equal(t, "h123", fc.LastLaunchReq.LaunchInstanceDetails.FreeformTags[NodeClassHashOciFreeFormTagKey])
		require.Equal(t, "v1", fc.LastLaunchReq.LaunchInstanceDetails.DefinedTags["ns"]["k1"])
	})
}

func TestProvider_ListAttachments_Pagination(t *testing.T) {
	fc := &fakes.FakeCompute{
		VnicPages: [][]ocicore.VnicAttachment{
			{{Id: lo.ToPtr("v1")}},
			{{Id: lo.ToPtr("v2")}},
		},
		BootPages: [][]ocicore.BootVolumeAttachment{
			{{Id: lo.ToPtr("b1")}},
			{{Id: lo.ToPtr("b2")}},
		},
	}
	p := &DefaultProvider{
		computeClient:        fc,
		clusterCompartmentId: "ocid1.compartment.oc1..parent",
		instanceCache:        cache.NewDefaultGetOrLoadCache[*InstanceInfo](),
		vnicAttachCache:      cache.NewDefaultGetOrLoadCache[[]*ocicore.VnicAttachment](),
		bootVolAttachCache:   cache.NewDefaultGetOrLoadCache[[]*ocicore.BootVolumeAttachment](),
		launchTimeoutVM:      10 * time.Minute,
		launchTimeoutBM:      20 * time.Minute,
	}

	ctx := context.TODO()
	vas, err := p.ListInstanceVnicAttachments(ctx, "ocid1.compartment.oc1..c", "ocid1.instance.oc1..id")
	require.NoError(t, err)
	require.Len(t, vas, 2)

	bvas, err := p.ListInstanceBootVolumeAttachments(ctx, "ocid1.compartment.oc1..c",
		"ocid1.instance.oc1..id", "tenancy:PHX-AD-1")
	require.NoError(t, err)
	require.Len(t, bvas, 2)
}

func TestProvider_DecorateNodeClaimByInstance_EdgeCases(t *testing.T) {
	// Non-terminating => no DeletionTimestamp and fault-domain label is populated
	inst := &ocicore.Instance{
		DisplayName:        lo.ToPtr("name"),
		AvailabilityDomain: lo.ToPtr("tenancy:PHX-AD-1"),
		FaultDomain:        lo.ToPtr("FAULT-DOMAIN-2"),
		LifecycleState:     ocicore.InstanceLifecycleStateRunning,
		TimeCreated:        &common.SDKTime{Time: time.Now()},
		Id:                 lo.ToPtr("ocid1.instance.oc1..id"),
		SourceDetails:      ocicore.InstanceSourceViaImageDetails{ImageId: lo.ToPtr("ocid1.image.oc1..img")},
	}
	nc := &corev1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{}}}
	DecorateNodeClaimByInstance(nc, inst)
	require.Nil(t, nc.DeletionTimestamp)
	require.Equal(t, "FAULT-DOMAIN-2", nc.Labels[ociv1beta1.OciFaultDomain])

	// Preemptible => capacity type spot
	inst2 := &ocicore.Instance{
		DisplayName:        lo.ToPtr("name"),
		AvailabilityDomain: lo.ToPtr("tenancy:PHX-AD-1"),
		FaultDomain:        lo.ToPtr("FAULT-DOMAIN-3"),
		LifecycleState:     ocicore.InstanceLifecycleStateRunning,
		TimeCreated:        &common.SDKTime{Time: time.Now()},
		Id:                 lo.ToPtr("ocid1.instance.oc1..id2"),
		PreemptibleInstanceConfig: &ocicore.PreemptibleInstanceConfigDetails{
			PreemptionAction: &ocicore.TerminatePreemptionAction{},
		},
		SourceDetails: ocicore.InstanceSourceViaImageDetails{ImageId: lo.ToPtr("ocid1.image.oc1..img")},
	}
	nc2 := &corev1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{}}}
	DecorateNodeClaimByInstance(nc2, inst2)
	require.Equal(t, corev1.CapacityTypeSpot, nc2.Labels[corev1.CapacityTypeLabelKey])
	require.Equal(t, "FAULT-DOMAIN-3", nc2.Labels[ociv1beta1.OciFaultDomain])
}

func TestProvider_LaunchInstance_FailurePaths(t *testing.T) {
	baseNodeClass := minimalNodeClass()
	baseClaim := minimalNodeClaim()
	imgRes := minimalImageResolve()
	netRes := minimalNetworkResolve()
	pp := minimalPlacement()

	fc := &fakes.FakeCompute{
		LaunchResp: ocicore.LaunchInstanceResponse{
			Instance:         ocicore.Instance{Id: lo.ToPtr("ocid1.instance.oc1..new")},
			Etag:             lo.ToPtr("etag-new"),
			OpcWorkRequestId: lo.ToPtr("wr1"),
		},
	}
	p := &DefaultProvider{
		computeClient:        fc,
		clusterCompartmentId: "ocid1.compartment.oc1..parent",
		instanceCache:        cache.NewDefaultGetOrLoadCache[*InstanceInfo](),
		vnicAttachCache:      cache.NewDefaultGetOrLoadCache[[]*ocicore.VnicAttachment](),
		bootVolAttachCache:   cache.NewDefaultGetOrLoadCache[[]*ocicore.BootVolumeAttachment](),
		launchTimeoutVM:      10 * time.Minute,
		launchTimeoutBM:      20 * time.Minute,
		pollInterval:         time.Second,
	}
	imdsp, err := instancemeta.NewProvider(context.TODO(), "10.0.0.1", []byte("CA"), ipV4SingleStack)
	require.NoError(t, err)
	p.instanceMetaProvider = imdsp

	t.Run("launch error propagates", func(t *testing.T) {
		fc.LaunchErr = errors.New("launch failed")
		_, err := p.LaunchInstance(context.TODO(), baseClaim, baseNodeClass,
			&instancetype.OciInstanceType{
				Shape: "VM.Standard.E4.Flex"}, imgRes, netRes, nil, pp)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "launch failed")
		fc.LaunchErr = nil // reset
	})

	t.Run("retryable error", func(t *testing.T) {
		fc.LaunchErr = errors.New("TooManyRequests: rate limited")
		_, err := p.LaunchInstance(context.TODO(), baseClaim, baseNodeClass,
			&instancetype.OciInstanceType{
				Shape: "VM.Standard.E4.Flex"}, imgRes, netRes, nil, pp)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rate limited")
		fc.LaunchErr = nil
	})

	t.Run("out of host capacity launch error returns no capacity", func(t *testing.T) {
		fc.LaunchErr = errors.New("Out of host capacity in selected AD")
		_, err := p.LaunchInstance(context.TODO(), baseClaim, baseNodeClass,
			&instancetype.OciInstanceType{
				Shape: "VM.Standard.E4.Flex"}, imgRes, netRes, nil, pp)
		require.Error(t, err)
		assert.Equal(t, NoCapacityError{}, err)
		fc.LaunchErr = nil
	})

}

func TestProvider_LaunchInstance_MarksUnavailableOnSkippableError(t *testing.T) {
	baseNodeClass := minimalNodeClass()
	baseClaim := minimalNodeClaim()
	imgRes := minimalImageResolve()
	netRes := minimalNetworkResolve()
	// An unscoped (generic) proposal: no capacity reservation, compute cluster, cluster placement
	// group, or fault domain, so the offering is eligible to be cached as unavailable.
	pp := minimalPlacement()
	pp.Fd = nil

	shape := testShapeE4Flex
	zone := utils.AdToZoneLabelValue(pp.Ad)
	ocpu := lo.ToPtr(float32(2))
	memory := lo.ToPtr(float32(16))

	testCases := []struct {
		name             string
		err              error
		enableCacheEntry bool
	}{
		{name: "out of host capacity", err: outOfHostCapacityError(), enableCacheEntry: true},
		{name: "service limit exceeded", err: limitExceededError(), enableCacheEntry: false},
		{name: "quota exceeded", err: quotaExceededError(), enableCacheEntry: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakes.FakeCompute{LaunchErr: tc.err}
			unavailable := cache.NewUnavailableOfferings(cache.UnavailableOfferingsTTL)
			p := &DefaultProvider{
				computeClient:        fc,
				clusterCompartmentId: "ocid1.compartment.oc1..parent",
				instanceCache:        cache.NewDefaultGetOrLoadCache[*InstanceInfo](),
				vnicAttachCache:      cache.NewDefaultGetOrLoadCache[[]*ocicore.VnicAttachment](),
				bootVolAttachCache:   cache.NewDefaultGetOrLoadCache[[]*ocicore.BootVolumeAttachment](),
				launchTimeoutVM:      10 * time.Minute,
				launchTimeoutBM:      20 * time.Minute,
				pollInterval:         time.Second,
				unavailableOfferings: unavailable,
			}
			imdsp, err := instancemeta.NewProvider(context.TODO(), "10.0.0.1", []byte("CA"), ipV4SingleStack)
			require.NoError(t, err)
			p.instanceMetaProvider = imdsp

			// baseClaim has no capacity-type requirement, so decideCapacityType returns on-demand.
			_, err = p.LaunchInstance(context.TODO(), baseClaim, baseNodeClass,
				&instancetype.OciInstanceType{Shape: shape, SupportShapeConfig: true, Ocpu: ocpu, MemoryInGbs: memory},
				imgRes, netRes, nil, pp)
			require.Error(t, err)
			assert.Equal(t, tc.enableCacheEntry,
				unavailable.IsUnavailable(shape, ocpu, memory, zone, corev1.CapacityTypeOnDemand, ""),
				"unexpected unavailable-offering cache entry")

			// A different CPU/memory configuration of the same shape/zone/capacity-type must remain
			// available, since only the failed config was observed to be out of capacity.
			assert.False(t,
				unavailable.IsUnavailable(shape, lo.ToPtr(float32(4)), memory, zone, corev1.CapacityTypeOnDemand, ""),
				"a different flex config must not be suppressed by the failed config")
		})
	}
}

func TestProvider_LaunchInstance_MarksServiceLimitExceededWhenEnabled(t *testing.T) {
	baseClaim := minimalNodeClaim()
	baseNodeClass := minimalNodeClass()
	imgRes := minimalImageResolve()
	netRes := minimalNetworkResolve()
	pp := minimalPlacement()
	pp.Fd = nil

	shape := testShapeE4Flex
	zone := utils.AdToZoneLabelValue(pp.Ad)
	ocpu := lo.ToPtr(float32(2))
	memory := lo.ToPtr(float32(16))
	unavailable := cache.NewUnavailableOfferings(cache.UnavailableOfferingsTTL)
	p := &DefaultProvider{
		computeClient:        &fakes.FakeCompute{LaunchErr: limitExceededError()},
		clusterCompartmentId: "ocid1.compartment.oc1..parent",
		instanceCache:        cache.NewDefaultGetOrLoadCache[*InstanceInfo](),
		vnicAttachCache:      cache.NewDefaultGetOrLoadCache[[]*ocicore.VnicAttachment](),
		bootVolAttachCache:   cache.NewDefaultGetOrLoadCache[[]*ocicore.BootVolumeAttachment](),
		launchTimeoutVM:      10 * time.Minute,
		launchTimeoutBM:      20 * time.Minute,
		pollInterval:         time.Second,
		unavailableOfferings: unavailable,
		enableUnavailableOfferingsOnServiceLimitExceeded: true,
	}
	imdsp, err := instancemeta.NewProvider(context.TODO(), "10.0.0.1", []byte("CA"), ipV4SingleStack)
	require.NoError(t, err)
	p.instanceMetaProvider = imdsp

	_, err = p.LaunchInstance(context.TODO(), baseClaim, baseNodeClass,
		&instancetype.OciInstanceType{Shape: shape, SupportShapeConfig: true, Ocpu: ocpu, MemoryInGbs: memory},
		imgRes, netRes, nil, pp)
	require.Error(t, err)
	assert.True(t, unavailable.IsUnavailable(shape, ocpu, memory, zone, corev1.CapacityTypeOnDemand, ""))
}

func TestProvider_LaunchInstance_MarksQuotaExceededScopedToCompartment(t *testing.T) {
	baseClaim := minimalNodeClaim()
	imgRes := minimalImageResolve()
	netRes := minimalNetworkResolve()
	// An unscoped (generic) proposal so the QuotaExceeded failure is eligible for caching.
	pp := minimalPlacement()
	pp.Fd = nil

	shape := testShapeE4Flex
	zone := utils.AdToZoneLabelValue(pp.Ad)
	ocpu := lo.ToPtr(float32(2))
	memory := lo.ToPtr(float32(16))

	const (
		clusterCompartment = "ocid1.compartment.oc1..parent"
		nodeCompartment    = "ocid1.compartment.oc1..node"
		otherCompartment   = "ocid1.compartment.oc1..other"
	)

	// The NodeClass launches into an explicit node compartment distinct from the cluster
	// compartment, so a QuotaExceeded failure must be scoped to that node compartment.
	nodeClass := minimalNodeClass()
	nodeClass.Spec.NodeCompartmentId = lo.ToPtr(nodeCompartment)

	fc := &fakes.FakeCompute{LaunchErr: quotaExceededError()}
	unavailable := cache.NewUnavailableOfferings(cache.UnavailableOfferingsTTL)
	p := &DefaultProvider{
		computeClient:        fc,
		clusterCompartmentId: clusterCompartment,
		instanceCache:        cache.NewDefaultGetOrLoadCache[*InstanceInfo](),
		vnicAttachCache:      cache.NewDefaultGetOrLoadCache[[]*ocicore.VnicAttachment](),
		bootVolAttachCache:   cache.NewDefaultGetOrLoadCache[[]*ocicore.BootVolumeAttachment](),
		launchTimeoutVM:      10 * time.Minute,
		launchTimeoutBM:      20 * time.Minute,
		pollInterval:         time.Second,
		unavailableOfferings: unavailable,
		enableUnavailableOfferingsOnServiceLimitExceeded: true,
	}
	imdsp, err := instancemeta.NewProvider(context.TODO(), "10.0.0.1", []byte("CA"), ipV4SingleStack)
	require.NoError(t, err)
	p.instanceMetaProvider = imdsp

	_, err = p.LaunchInstance(context.TODO(), baseClaim, nodeClass,
		&instancetype.OciInstanceType{Shape: shape, SupportShapeConfig: true, Ocpu: ocpu, MemoryInGbs: memory},
		imgRes, netRes, nil, pp)
	require.Error(t, err)

	// The offering is unavailable only for the target node compartment.
	assert.True(t, unavailable.IsUnavailable(shape, ocpu, memory, zone, corev1.CapacityTypeOnDemand, nodeCompartment),
		"expected the offering to be marked unavailable for the target node compartment")
	// Other compartments and the tenancy-wide scope stay available.
	assert.False(t, unavailable.IsUnavailable(shape, ocpu, memory, zone, corev1.CapacityTypeOnDemand, otherCompartment),
		"a QuotaExceeded failure must not suppress the offering for another compartment")
	assert.False(t, unavailable.IsUnavailable(shape, ocpu, memory, zone, corev1.CapacityTypeOnDemand, ""),
		"a QuotaExceeded failure must not suppress the offering tenancy-wide")
}

// A skippable (out-of-capacity) launch failure for a placement-scoped proposal must not pollute the
// generic (shape, ocpu/memory, zone, capacity-type) offering. The unavailable-offerings cache key
// omits placement scope (capacity reservation, compute cluster, cluster placement group, fault
// domain), so recording a placement-scoped failure under that key would wrongly hide otherwise-valid
// generic capacity from other NodeClaims.
func TestProvider_LaunchInstance_SkipsMarkUnavailableForPlacementScopedProposals(t *testing.T) {
	baseNodeClass := minimalNodeClass()
	baseClaim := minimalNodeClaim()
	imgRes := minimalImageResolve()
	netRes := minimalNetworkResolve()

	shape := testShapeE4Flex
	ocpu := lo.ToPtr(float32(2))
	memory := lo.ToPtr(float32(16))

	testCases := []struct {
		name       string
		decorateFn func(pp *placement.Proposal)
	}{
		{
			name: "capacity reservation",
			decorateFn: func(pp *placement.Proposal) {
				pp.CapacityReservationId = lo.ToPtr("ocid1.capacityreservation.oc1..reservation")
			},
		},
		{
			name: "compute cluster",
			decorateFn: func(pp *placement.Proposal) {
				pp.ComputeClusterId = lo.ToPtr("ocid1.computecluster.oc1..cluster")
			},
		},
		{
			name: "cluster placement group",
			decorateFn: func(pp *placement.Proposal) {
				pp.ClusterPlacementGroupId = lo.ToPtr("ocid1.clusterplacementgroup.oc1..cpg")
			},
		},
		{
			name: "fault domain",
			decorateFn: func(pp *placement.Proposal) {
				pp.Fd = lo.ToPtr("FAULT-DOMAIN-1")
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Start from an unscoped proposal (minimalPlacement sets a fault domain by default) so
			// each case isolates the single placement-scope field under test.
			pp := minimalPlacement()
			pp.Fd = nil
			tc.decorateFn(pp)
			zone := utils.AdToZoneLabelValue(pp.Ad)

			fc := &fakes.FakeCompute{LaunchErr: outOfHostCapacityError()}
			unavailable := cache.NewUnavailableOfferings(cache.UnavailableOfferingsTTL)
			p := &DefaultProvider{
				computeClient:        fc,
				clusterCompartmentId: "ocid1.compartment.oc1..parent",
				instanceCache:        cache.NewDefaultGetOrLoadCache[*InstanceInfo](),
				vnicAttachCache:      cache.NewDefaultGetOrLoadCache[[]*ocicore.VnicAttachment](),
				bootVolAttachCache:   cache.NewDefaultGetOrLoadCache[[]*ocicore.BootVolumeAttachment](),
				launchTimeoutVM:      10 * time.Minute,
				launchTimeoutBM:      20 * time.Minute,
				pollInterval:         time.Second,
				unavailableOfferings: unavailable,
			}
			imdsp, err := instancemeta.NewProvider(context.TODO(), "10.0.0.1", []byte("CA"), ipV4SingleStack)
			require.NoError(t, err)
			p.instanceMetaProvider = imdsp

			_, err = p.LaunchInstance(context.TODO(), baseClaim, baseNodeClass,
				&instancetype.OciInstanceType{Shape: shape, SupportShapeConfig: true, Ocpu: ocpu, MemoryInGbs: memory},
				imgRes, netRes, nil, pp)
			require.Error(t, err)
			assert.False(t,
				unavailable.IsUnavailable(shape, ocpu, memory, zone, corev1.CapacityTypeOnDemand, ""),
				"a placement-scoped launch failure must not mark the generic offering unavailable")
		})
	}
}

func TestProvider_Cached_Wrappers_Negative(t *testing.T) {
	fc := &fakes.FakeCompute{}
	callCount := 0
	fc.OnGet = func(ctx context.Context, r ocicore.GetInstanceRequest) (ocicore.GetInstanceResponse, error) {
		callCount++
		if callCount == 1 {
			return ocicore.GetInstanceResponse{}, errors.New("first call fails")
		}
		return ocicore.GetInstanceResponse{
			Instance: ocicore.Instance{
				Id:                 lo.ToPtr("ocid1.instance.oc1..xyz"),
				DisplayName:        lo.ToPtr("inst1"),
				AvailabilityDomain: lo.ToPtr("tenancy:PHX-AD-1"),
				LifecycleState:     ocicore.InstanceLifecycleStateRunning,
				SourceDetails:      ocicore.InstanceSourceViaImageDetails{ImageId: lo.ToPtr("ocid1.image.oc1..img")},
			},
			Etag: lo.ToPtr("etag-1"),
		}, nil
	}

	p := &DefaultProvider{
		computeClient: fc,
		instanceCache: cache.NewDefaultGetOrLoadCache[*InstanceInfo](),
	}

	ctx := context.TODO()

	// First call fails, should not cache
	_, err := p.GetInstanceCached(ctx, "ocid1.instance.oc1..xyz")
	require.Error(t, err)

	// Second call succeeds and caches
	i2, err := p.GetInstanceCached(ctx, "ocid1.instance.oc1..xyz")
	require.NoError(t, err)
	assert.Equal(t, "ocid1.instance.oc1..xyz", *i2.Id)

	// Third call hits cache
	i3, err := p.GetInstanceCached(ctx, "ocid1.instance.oc1..xyz")
	require.NoError(t, err)
	assert.Equal(t, "ocid1.instance.oc1..xyz", *i3.Id)

	assert.Equal(t, 2, callCount, "expected two calls: first fails, second succeeds and caches")
}

func TestProvider_ListAttachments_ZeroPages(t *testing.T) {
	fc := &fakes.FakeCompute{
		VnicPages: [][]ocicore.VnicAttachment{}, // empty pages
		BootPages: [][]ocicore.BootVolumeAttachment{},
	}
	p := &DefaultProvider{
		computeClient:        fc,
		clusterCompartmentId: "ocid1.compartment.oc1..parent",
		instanceCache:        cache.NewDefaultGetOrLoadCache[*InstanceInfo](),
		vnicAttachCache:      cache.NewDefaultGetOrLoadCache[[]*ocicore.VnicAttachment](),
		bootVolAttachCache:   cache.NewDefaultGetOrLoadCache[[]*ocicore.BootVolumeAttachment](),
	}

	ctx := context.TODO()
	vas, err := p.ListInstanceVnicAttachments(ctx, "ocid1.compartment.oc1..c", "ocid1.instance.oc1..id")
	require.NoError(t, err)
	assert.Len(t, vas, 0)

	bvas, err := p.ListInstanceBootVolumeAttachments(ctx, "ocid1.compartment.oc1..c",
		"ocid1.instance.oc1..id", "tenancy:PHX-AD-1")
	require.NoError(t, err)
	assert.Len(t, bvas, 0)
}

func TestProvider_GetKmsClient_Concurrency(t *testing.T) {
	fc := &fakes.FakeCompute{}
	fc.OnGet = func(ctx context.Context, r ocicore.GetInstanceRequest) (ocicore.GetInstanceResponse, error) {
		return ocicore.GetInstanceResponse{
			Instance: ocicore.Instance{
				Id:                 lo.ToPtr("ocid1.instance.oc1..xyz"),
				DisplayName:        lo.ToPtr("inst1"),
				AvailabilityDomain: lo.ToPtr("tenancy:PHX-AD-1"),
				LifecycleState:     ocicore.InstanceLifecycleStateRunning,
				SourceDetails:      ocicore.InstanceSourceViaImageDetails{ImageId: lo.ToPtr("ocid1.image.oc1..img")},
			},
			Etag: lo.ToPtr("etag-1"),
		}, nil
	}
	p := &DefaultProvider{
		computeClient: fc,
		instanceCache: cache.NewDefaultGetOrLoadCache[*InstanceInfo](),
	}

	var wg sync.WaitGroup
	const numGoroutines = 50
	results := make([]*InstanceInfo, numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			info, err := p.GetInstanceCached(context.TODO(), "ocid1.instance.oc1..xyz")
			require.NoError(t, err)
			results[idx] = info
		}(i)
	}
	wg.Wait()

	// All should be the same instance
	first := results[0]
	for _, r := range results {
		assert.Same(t, first, r)
	}
	assert.Equal(t, 1, fc.GetCount.Get(), "only one underlying call")
}

func TestProvider_DeleteInstance_Error(t *testing.T) {
	fc := &fakes.FakeCompute{
		TerminateErr: errors.New("terminate failed"),
	}
	p := &DefaultProvider{
		computeClient: fc,
	}

	err := p.DeleteInstance(context.TODO(), "ocid1.instance.oc1..xyz")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminate failed")
	assert.Equal(t, 1, fc.TerminateCount.Get())
}

func TestProvider_LaunchInstance_WorkRequestSuccess(t *testing.T) {
	fc := &fakes.FakeCompute{}
	fwr := &fakes.FakeWorkRequest{}
	callCount := 0

	// Mock LaunchInstance to return a response with work request ID in headers
	fc.OnLaunch = func(ctx context.Context, r ocicore.LaunchInstanceRequest) (ocicore.LaunchInstanceResponse, error) {
		return ocicore.LaunchInstanceResponse{
			Instance: ocicore.Instance{Id: lo.ToPtr("ocid1.instance.oc1..new")},
			Etag:     lo.ToPtr("etag-new"),
			RawResponse: &http.Response{
				StatusCode: 200,
				Header: http.Header{
					"Opc-Work-Request-Id": []string{"wr-launch-123"},
				},
			},
		}, nil
	}

	// Set up work request polling - simulate status progression to SUCCEEDED
	fwr.OnGet = func(ctx context.Context, r ociwr.GetWorkRequestRequest) (ociwr.GetWorkRequestResponse, error) {
		callCount++
		var status ociwr.WorkRequestStatusEnum
		switch {
		case callCount <= 2:
			status = ociwr.WorkRequestStatusAccepted
		case callCount <= 4:
			status = ociwr.WorkRequestStatusInProgress
		default:
			status = ociwr.WorkRequestStatusSucceeded
		}
		return ociwr.GetWorkRequestResponse{
			WorkRequest: ociwr.WorkRequest{
				Status:       status,
				TimeFinished: &common.SDKTime{Time: time.Now()},
				TimeStarted:  &common.SDKTime{Time: time.Now()},
			},
		}, nil
	}

	imdsp, err := instancemeta.NewProvider(context.TODO(), "10.0.0.1", []byte("CA"), ipV4SingleStack)
	require.NoError(t, err)
	p := &DefaultProvider{
		computeClient:        fc,
		workRequestClient:    fwr,
		instanceMetaProvider: imdsp,
		clusterCompartmentId: "ocid1.compartment.oc1..parent",
		launchTimeoutVM:      10 * time.Minute,
		launchTimeoutBM:      20 * time.Minute,
		pollInterval:         time.Second,
		instanceCache:        cache.NewNonExpiringGetOrLoadCache[*InstanceInfo](),
	}

	it := &instancetype.OciInstanceType{
		InstanceType: cloudprovider.InstanceType{
			Name:      "VM.Standard.E4.Flex",
			Offerings: cloudprovider.Offerings{makeOfferingWithCapType(corev1.CapacityTypeOnDemand)},
		},
		Shape: "VM.Standard.E4.Flex",
	}

	placementProposal := minimalPlacement()
	inst, err := p.LaunchInstance(context.TODO(), minimalNodeClaim(), minimalNodeClass(), it, minimalImageResolve(),
		minimalNetworkResolve(), nil, placementProposal)

	require.NoError(t, err)
	assert.NotNil(t, inst)
	assert.Equal(t, "ocid1.instance.oc1..new", *inst.Id)
	assert.Greater(t, callCount, 0, "expected GetWorkRequest called at least once")
	cached, err := p.GetInstanceCached(context.TODO(), "ocid1.instance.oc1..new")
	require.NoError(t, err)
	assert.Same(t, inst, cached)
	assert.Equal(t, 0, fc.GetCount.Get(), "successful launch should seed the instance cache")
}

func TestProvider_LaunchInstance_WorkRequestOutOfHostCapacity(t *testing.T) {
	fc := &fakes.FakeCompute{}
	fwr := &fakes.FakeWorkRequest{}

	fc.OnLaunch = func(ctx context.Context, r ocicore.LaunchInstanceRequest) (ocicore.LaunchInstanceResponse, error) {
		return ocicore.LaunchInstanceResponse{
			Instance: ocicore.Instance{Id: lo.ToPtr("ocid1.instance.oc1..new")},
			Etag:     lo.ToPtr("etag-new"),
			RawResponse: &http.Response{
				StatusCode: 200,
				Header: http.Header{
					"Opc-Work-Request-Id": []string{"wr-launch-123"},
				},
			},
		}, nil
	}

	fwr.GetResp = ociwr.GetWorkRequestResponse{
		WorkRequest: ociwr.WorkRequest{
			Status:       ociwr.WorkRequestStatusFailed,
			TimeFinished: &common.SDKTime{Time: time.Now()},
			TimeStarted:  &common.SDKTime{Time: time.Now()},
		},
	}
	fwr.ListErrorsResp = ociwr.ListWorkRequestErrorsResponse{
		Items: []ociwr.WorkRequestError{{
			Code:      lo.ToPtr("InternalError"),
			Message:   lo.ToPtr("Out of host capacity in selected AD"),
			Timestamp: &common.SDKTime{Time: time.Now()},
		}},
	}

	imdsp, err := instancemeta.NewProvider(context.TODO(), "10.0.0.1", []byte("CA"), ipV4SingleStack)
	require.NoError(t, err)
	p := &DefaultProvider{
		computeClient:        fc,
		workRequestClient:    fwr,
		instanceMetaProvider: imdsp,
		clusterCompartmentId: "ocid1.compartment.oc1..parent",
		launchTimeoutVM:      10 * time.Minute,
		launchTimeoutBM:      20 * time.Minute,
		pollInterval:         time.Millisecond,
	}

	it := &instancetype.OciInstanceType{
		InstanceType: cloudprovider.InstanceType{
			Name:      "VM.Standard.E4.Flex",
			Offerings: cloudprovider.Offerings{makeOfferingWithCapType(corev1.CapacityTypeOnDemand)},
		},
		Shape: "VM.Standard.E4.Flex",
	}

	_, err = p.LaunchInstance(context.TODO(), minimalNodeClaim(), minimalNodeClass(), it, minimalImageResolve(),
		minimalNetworkResolve(), nil, minimalPlacement())
	require.Error(t, err)
	assert.Equal(t, NoCapacityError{}, err)
	assert.Equal(t, 1, fwr.ListCount.Get())
}

func TestProvider_LaunchInstance_Timeout(t *testing.T) {
	fc := &fakes.FakeCompute{}
	fwr := &fakes.FakeWorkRequest{}

	displayName1 := "i1"
	displayName2 := "i2"
	displayName3 := "i3"
	displayName4 := "i4"

	newInstanceId := func(name string) string {
		return fmt.Sprintf("ocid1.instance.oc1.%s", name)
	}
	instanceId1 := newInstanceId(displayName1)
	instanceId2 := newInstanceId(displayName2)
	instanceId3 := newInstanceId(displayName3)
	instanceId4 := newInstanceId(displayName4)

	displayNameToInstanceIdMap := map[string]string{
		displayName1: instanceId1,
		displayName2: instanceId2,
		displayName3: instanceId3,
		displayName4: instanceId4,
	}

	instanceIdToListBootVolumeCall := map[string][]ocicore.BootVolumeAttachment{
		instanceId1: {
			ocicore.BootVolumeAttachment{
				Id: lo.ToPtr("ocid1.bootvolumeattachment.ocid..new"),
			},
		},
		instanceId2: {},
		instanceId3: {},
	}

	successDeleteInstanceId := instanceId2

	// Mock LaunchInstance to return a response with work request ID in headers
	fc.OnLaunch = func(ctx context.Context, r ocicore.LaunchInstanceRequest) (ocicore.LaunchInstanceResponse, error) {
		return ocicore.LaunchInstanceResponse{
			Instance: ocicore.Instance{Id: lo.ToPtr(displayNameToInstanceIdMap[*r.DisplayName]),
				Shape:              r.Shape,
				LifecycleState:     ocicore.InstanceLifecycleStateProvisioning,
				CompartmentId:      lo.ToPtr("compartmentId"),
				AvailabilityDomain: lo.ToPtr("ad-1"),
			},
			Etag: lo.ToPtr("etag-new"),
			RawResponse: &http.Response{
				StatusCode: 200,
				Header: http.Header{
					"Opc-Work-Request-Id": []string{"wr-launch-123"},
				},
			},
		}, nil
	}

	fc.OnListBoot = func(ctx context.Context,
		request ocicore.ListBootVolumeAttachmentsRequest) (ocicore.ListBootVolumeAttachmentsResponse, error) {
		bvas, ok := instanceIdToListBootVolumeCall[*request.InstanceId]
		if !ok {
			return ocicore.ListBootVolumeAttachmentsResponse{}, errors.New("not found")
		}

		return ocicore.ListBootVolumeAttachmentsResponse{
			Items: bvas,
		}, nil
	}

	fc.OnTerminate = func(ctx context.Context,
		request ocicore.TerminateInstanceRequest) (ocicore.TerminateInstanceResponse, error) {
		if *request.InstanceId == successDeleteInstanceId {
			return ocicore.TerminateInstanceResponse{}, nil
		}

		return ocicore.TerminateInstanceResponse{}, errors.New("terminate instance error")
	}

	fc.OnGet = func(ctx context.Context, request ocicore.GetInstanceRequest) (ocicore.GetInstanceResponse, error) {
		if *request.InstanceId == successDeleteInstanceId {
			return ocicore.GetInstanceResponse{
				Instance: ocicore.Instance{
					Id:             request.InstanceId,
					LifecycleState: ocicore.InstanceLifecycleStateTerminated,
				},
				Etag: lo.ToPtr("etag-new"),
			}, nil
		}

		return ocicore.GetInstanceResponse{}, errors.New("instance not found")
	}

	// Set up work request polling - simulate status progression to SUCCEEDED
	fwr.OnGet = func(ctx context.Context, r ociwr.GetWorkRequestRequest) (ociwr.GetWorkRequestResponse, error) {
		return ociwr.GetWorkRequestResponse{
			WorkRequest: ociwr.WorkRequest{
				Status:       ociwr.WorkRequestStatusInProgress,
				TimeFinished: &common.SDKTime{Time: time.Now()},
				TimeStarted:  &common.SDKTime{Time: time.Now()},
			},
		}, nil
	}

	imdsp, err := instancemeta.NewProvider(context.TODO(), "10.0.0.1", []byte("CA"), []network.IpFamily{network.IPv4})
	require.NoError(t, err)
	p := &DefaultProvider{
		computeClient:         fc,
		workRequestClient:     fwr,
		instanceMetaProvider:  imdsp,
		clusterCompartmentId:  "ocid1.compartment.oc1..parent",
		launchTimeoutVM:       2 * time.Second,
		launchTimeoutBM:       1 * time.Second,
		launchTimeOutFailOver: true,
		pollInterval:          time.Second,
		instanceCache:         cache.NewDefaultGetOrLoadCache[*InstanceInfo](),
		vnicAttachCache:       cache.NewDefaultGetOrLoadCache[[]*ocicore.VnicAttachment](),
		bootVolAttachCache:    cache.NewDefaultGetOrLoadCache[[]*ocicore.BootVolumeAttachment](),
	}

	newInstanceType := func(shape string) *instancetype.OciInstanceType {
		return &instancetype.OciInstanceType{
			InstanceType: cloudprovider.InstanceType{
				Name:      shape,
				Offerings: cloudprovider.Offerings{makeOfferingWithCapType(corev1.CapacityTypeOnDemand)},
			},
			Shape: shape,
		}
	}

	newNodeClaim := func(name string) *corev1.NodeClaim {
		nodeClaim := minimalNodeClaim()
		nodeClaim.Name = name
		return nodeClaim
	}

	t.Run("launch a bm shape timeout should return an instance without error", func(t *testing.T) {
		inst, launchErr := p.LaunchInstance(context.TODO(), newNodeClaim(displayName1),
			minimalNodeClass(), newInstanceType("BM.Standard1.1"), minimalImageResolve(),
			minimalNetworkResolve(), nil, minimalPlacement())

		require.NoError(t, launchErr)
		assert.NotNil(t, inst)
		assert.Equal(t, instanceId1, *inst.Id)
		cached, cacheErr := p.GetInstanceCached(context.TODO(), instanceId1)
		require.NoError(t, cacheErr)
		assert.Same(t, inst, cached)
	})

	t.Run("launch a vm shape timeout, then retrieve volumes success with attached volume,"+
		" should return an instance without error", func(t *testing.T) {
		inst, launchErr := p.LaunchInstance(context.TODO(), newNodeClaim(displayName1), minimalNodeClass(),
			newInstanceType("VM.Standard1.1"), minimalImageResolve(), minimalNetworkResolve(), nil, minimalPlacement())

		require.NoError(t, launchErr)
		assert.NotNil(t, inst)
		assert.Equal(t, instanceId1, *inst.Id)
	})

	t.Run("launch a vm shape timeout, then retrieve volumes failed, "+
		"should return an instance without error", func(t *testing.T) {
		inst, launchErr := p.LaunchInstance(context.TODO(), newNodeClaim(displayName4), minimalNodeClass(),
			newInstanceType("VM.Standard1.1"), minimalImageResolve(), minimalNetworkResolve(), nil, minimalPlacement())

		require.NoError(t, launchErr)
		assert.NotNil(t, inst)
		assert.Equal(t, instanceId4, *inst.Id)
	})

	t.Run("launch a vm shape timeout, then retrieve volumes success but no volume attached, "+
		"then try to terminate instance but failed, should return an instance without error", func(t *testing.T) {
		inst, launchErr := p.LaunchInstance(context.TODO(), newNodeClaim(displayName3), minimalNodeClass(),
			newInstanceType("VM.Standard1.1"), minimalImageResolve(), minimalNetworkResolve(), nil, minimalPlacement())

		require.NoError(t, launchErr)
		assert.NotNil(t, inst)
		assert.Equal(t, instanceId3, *inst.Id)
	})

	t.Run("launch a vm shape timeout, then retrieve volumes success but no volume attached, "+
		"then try to terminate instance and success, should return no capacity error", func(t *testing.T) {
		inst, launchErr := p.LaunchInstance(context.TODO(), newNodeClaim(displayName2), minimalNodeClass(),
			newInstanceType("VM.Standard1.1"), minimalImageResolve(), minimalNetworkResolve(), nil, minimalPlacement())

		require.NotNil(t, launchErr)
		assert.Nil(t, inst)
		assert.Equal(t, launchErr, NoCapacityError{})
	})

	p.launchTimeOutFailOver = false
	t.Run("launch a vm shape timeout but fail over disabled, should return an instance without error",
		func(t *testing.T) {
			inst, launchErr := p.LaunchInstance(context.TODO(), newNodeClaim(displayName1), minimalNodeClass(),
				newInstanceType("VM.Standard1.1"), minimalImageResolve(),
				minimalNetworkResolve(), nil, minimalPlacement())

			require.NoError(t, launchErr)
			assert.NotNil(t, inst)
			assert.Equal(t, instanceId1, *inst.Id)
		})
}

func TestBuildLaunchOptions(t *testing.T) {
	tests := []struct {
		name     string
		slo      *ociv1beta1.LaunchOptions
		expected *ocicore.LaunchOptions
	}{
		{
			name:     "Nil input returns nil",
			slo:      nil,
			expected: nil,
		},
		{
			name: "Full LaunchOptions",
			slo: &ociv1beta1.LaunchOptions{
				Firmware:             lo.ToPtr(ociv1beta1.FirmwareUefi64),
				RemoteDataVolumeType: lo.ToPtr(ociv1beta1.Paravirtualized),
				BootVolumeType:       lo.ToPtr(ociv1beta1.Paravirtualized),
				NetworkType:          lo.ToPtr(ociv1beta1.NetworkTypeParavirtualized),
			},
			expected: &ocicore.LaunchOptions{
				Firmware:             ocicore.LaunchOptionsFirmwareUefi64,
				RemoteDataVolumeType: ocicore.LaunchOptionsRemoteDataVolumeTypeParavirtualized,
				BootVolumeType:       ocicore.LaunchOptionsBootVolumeTypeParavirtualized,
				NetworkType:          ocicore.LaunchOptionsNetworkTypeParavirtualized,
			},
		},
		{
			name: "Partial LaunchOptions",
			slo: &ociv1beta1.LaunchOptions{
				Firmware: lo.ToPtr(ociv1beta1.FirmwareUefi64),
			},
			expected: &ocicore.LaunchOptions{
				Firmware: ocicore.LaunchOptionsFirmwareUefi64,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildLaunchOptions(tt.slo)
			if tt.slo == nil {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			if tt.slo.Firmware != nil {
				require.Equal(t, tt.expected.Firmware, got.Firmware)
			}
			if tt.slo.RemoteDataVolumeType != nil {
				require.Equal(t, tt.expected.RemoteDataVolumeType, got.RemoteDataVolumeType)
			}
			if tt.slo.BootVolumeType != nil {
				require.Equal(t, tt.expected.BootVolumeType, got.BootVolumeType)
			}
			if tt.slo.NetworkType != nil {
				require.Equal(t, tt.expected.NetworkType, got.NetworkType)
			}
		})
	}
}
