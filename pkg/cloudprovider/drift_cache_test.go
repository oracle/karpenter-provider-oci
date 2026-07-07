/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package cloudprovider

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	ocicache "github.com/oracle/karpenter-provider-oci/pkg/cache"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/blockstorage"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instance"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancetype"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	"github.com/oracle/oci-go-sdk/v65/common"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8scorev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
)

func TestDriftCachesLoadOnceAndEvictByInstance(t *testing.T) {
	ctx := context.Background()
	require.NoError(t, v1beta1.AddToScheme(scheme.Scheme))
	const (
		instanceID  = "instance-1"
		compartment = "compartment-1"
		ad          = "PHX-AD-1"
		subnetID    = "subnet-1"
	)

	nodeClass := testReadyNodeClass(uniqueName("drift-cache"))
	nodeClass.Spec.NodeCompartmentId = lo.ToPtr(compartment)
	nodeClass.Status.Network = &v1beta1.Network{
		PrimaryVnic: &v1beta1.Vnic{Subnet: v1beta1.Subnet{SubnetId: subnetID}},
	}
	nodeClaim := testNodeClaim(nodeClass.Name)
	nodeClaim.Labels[k8scorev1.LabelInstanceTypeStable] = "shape-a"
	nodeClaim.Status.ProviderID = instanceID

	baseInstance := testInstanceWithShape("shape-a", "pool-a")
	baseInstance.CompartmentId = lo.ToPtr(compartment)
	baseInstance.AvailabilityDomain = lo.ToPtr(ad)
	computeClient := &fakes.FakeCompute{}
	computeClient.OnGet = func(_ context.Context, request ocicore.GetInstanceRequest) (
		ocicore.GetInstanceResponse, error) {
		result := *baseInstance
		result.Id = request.InstanceId
		return ocicore.GetInstanceResponse{Instance: result, Etag: lo.ToPtr("etag-1")}, nil
	}
	computeClient.OnListVnics = func(_ context.Context, request ocicore.ListVnicAttachmentsRequest) (
		ocicore.ListVnicAttachmentsResponse, error) {
		return ocicore.ListVnicAttachmentsResponse{Items: []ocicore.VnicAttachment{{
			VnicId:         lo.ToPtr(*request.InstanceId + "-vnic"),
			SubnetId:       lo.ToPtr(subnetID),
			LifecycleState: ocicore.VnicAttachmentLifecycleStateAttached,
		}}}, nil
	}
	computeClient.OnListBoot = func(_ context.Context, request ocicore.ListBootVolumeAttachmentsRequest) (
		ocicore.ListBootVolumeAttachmentsResponse, error) {
		return ocicore.ListBootVolumeAttachmentsResponse{Items: []ocicore.BootVolumeAttachment{{
			BootVolumeId: lo.ToPtr(*request.InstanceId + "-boot"),
			TimeCreated:  &common.SDKTime{Time: time.Now()},
		}}}, nil
	}
	computeClient.OnListInstances = func(_ context.Context, _ ocicore.ListInstancesRequest) (
		ocicore.ListInstancesResponse, error) {
		listed := *baseInstance
		listed.Id = lo.ToPtr("instance-2")
		return ocicore.ListInstancesResponse{Items: []ocicore.Instance{listed}}, nil
	}

	vcnClient := &fakes.FakeVirtualNetwork{
		OnGetVnic: func(_ context.Context, request ocicore.GetVnicRequest) (ocicore.GetVnicResponse, error) {
			return ocicore.GetVnicResponse{Vnic: ocicore.Vnic{
				Id: request.VnicId, SubnetId: lo.ToPtr(subnetID), IsPrimary: lo.ToPtr(true),
			}}, nil
		},
	}
	driftCaches := instance.NewDriftCaches()
	networkProvider, err := network.NewProvider(ctx, compartment, false, []network.IpFamily{network.IPv4},
		vcnClient, driftCaches.VnicCache())
	require.NoError(t, err)
	blockClient := &fakes.FakeBlockstorage{
		OnGetBootVolume: func(_ context.Context,
			request ocicore.GetBootVolumeRequest) (ocicore.GetBootVolumeResponse, error) {
			return ocicore.GetBootVolumeResponse{BootVolume: ocicore.BootVolume{Id: request.BootVolumeId}}, nil
		},
	}
	blockProvider, err := blockstorage.NewProvider(ctx, blockClient, driftCaches.BootVolumeCache())
	require.NoError(t, err)
	instanceProvider, err := instance.NewProvider(ctx, computeClient, nil, compartment, nil, driftCaches,
		time.Minute, time.Minute, false, time.Millisecond, ocicache.NewUnavailableOfferings(0), false)
	require.NoError(t, err)

	cp := newUnitTestCloudProvider(unitTestCloudProviderOptions{
		kubeClient:           newFakeKubeClient(nodeClass),
		instanceTypes:        []*instancetype.OciInstanceType{testInstanceType("shape-a", 10)},
		instanceProvider:     instanceProvider,
		networkProvider:      networkProvider,
		blockStorageProvider: blockProvider,
	})

	for i := 0; i < 2; i++ {
		reason, driftErr := cp.IsDrifted(ctx, nodeClaim)
		require.NoError(t, driftErr)
		assert.Empty(t, reason)
	}
	assertOCIReadCounts(t, computeClient, vcnClient, blockClient, 1)

	loadAllDriftCaches(t, ctx, instanceProvider, networkProvider, blockProvider, "instance-2")
	assertOCIReadCounts(t, computeClient, vcnClient, blockClient, 2)
	_, err = instanceProvider.ListInstances(ctx, []string{compartment})
	require.NoError(t, err)
	assert.Equal(t, 1, computeClient.ListInstancesCount.Get())
	assertOCIReadCounts(t, computeClient, vcnClient, blockClient, 2)

	loadAllDriftCaches(t, ctx, instanceProvider, networkProvider, blockProvider, "instance-2")
	assertOCIReadCounts(t, computeClient, vcnClient, blockClient, 2)
	loadAllDriftCaches(t, ctx, instanceProvider, networkProvider, blockProvider, instanceID)
	assertOCIReadCounts(t, computeClient, vcnClient, blockClient, 3)
}

func loadAllDriftCaches(t *testing.T, ctx context.Context, instanceProvider instance.Provider,
	networkProvider network.Provider, blockProvider blockstorage.Provider, instanceID string) {
	t.Helper()
	_, err := instanceProvider.GetInstanceCached(ctx, instanceID)
	require.NoError(t, err)
	_, err = instanceProvider.ListInstanceVnicAttachmentsCached(ctx, "compartment-1", instanceID)
	require.NoError(t, err)
	_, err = networkProvider.GetVnicCached(ctx, instanceID+"-vnic")
	require.NoError(t, err)
	_, err = instanceProvider.ListInstanceBootVolumeAttachmentsCached(ctx, "compartment-1", instanceID, "PHX-AD-1")
	require.NoError(t, err)
	_, err = blockProvider.GetBootVolumeCached(ctx, instanceID+"-boot")
	require.NoError(t, err)
}

func assertOCIReadCounts(t *testing.T, computeClient *fakes.FakeCompute,
	vcnClient *fakes.FakeVirtualNetwork, blockClient *fakes.FakeBlockstorage, expected int) {
	t.Helper()
	assert.Equal(t, expected, computeClient.GetCount.Get())
	assert.Equal(t, expected, computeClient.ListVnicCount.Get())
	assert.Equal(t, expected, computeClient.ListBootCount.Get())
	assert.Equal(t, expected, vcnClient.GetVnicCount)
	assert.Equal(t, expected, blockClient.GetCount.Get())
}
