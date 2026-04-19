/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package oci

import (
	"context"

	"github.com/oracle/karpenter-provider-oci/pkg/metrics"
	ocicpg "github.com/oracle/oci-go-sdk/v65/clusterplacementgroups"
	"github.com/oracle/oci-go-sdk/v65/common"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	ociidentity "github.com/oracle/oci-go-sdk/v65/identity"
	ocikms "github.com/oracle/oci-go-sdk/v65/keymanagement"
	ociwr "github.com/oracle/oci-go-sdk/v65/workrequests"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// nolint:lll
type ComputeClient interface {
	LaunchInstance(ctx context.Context, request ocicore.LaunchInstanceRequest) (response ocicore.LaunchInstanceResponse, err error)
	GetInstance(ctx context.Context, request ocicore.GetInstanceRequest) (response ocicore.GetInstanceResponse, err error)
	ListInstances(ctx context.Context, request ocicore.ListInstancesRequest) (response ocicore.ListInstancesResponse, err error)
	ListVnicAttachments(ctx context.Context, request ocicore.ListVnicAttachmentsRequest) (response ocicore.ListVnicAttachmentsResponse, err error)
	ListBootVolumeAttachments(ctx context.Context, request ocicore.ListBootVolumeAttachmentsRequest) (response ocicore.ListBootVolumeAttachmentsResponse, err error)
	TerminateInstance(ctx context.Context, request ocicore.TerminateInstanceRequest) (response ocicore.TerminateInstanceResponse, err error)
	ListShapes(ctx context.Context, request ocicore.ListShapesRequest) (response ocicore.ListShapesResponse, err error)
	GetComputeCapacityReservation(ctx context.Context, request ocicore.GetComputeCapacityReservationRequest) (response ocicore.GetComputeCapacityReservationResponse, err error)
	ListComputeCapacityReservations(ctx context.Context, request ocicore.ListComputeCapacityReservationsRequest) (response ocicore.ListComputeCapacityReservationsResponse, err error)
	GetComputeCluster(ctx context.Context, request ocicore.GetComputeClusterRequest) (response ocicore.GetComputeClusterResponse, err error)
	ListComputeClusters(ctx context.Context, request ocicore.ListComputeClustersRequest) (response ocicore.ListComputeClustersResponse, err error)
	GetImage(ctx context.Context, request ocicore.GetImageRequest) (response ocicore.GetImageResponse, err error)
	ListImages(ctx context.Context, request ocicore.ListImagesRequest) (response ocicore.ListImagesResponse, err error)
	ListImageShapeCompatibilityEntries(ctx context.Context, request ocicore.ListImageShapeCompatibilityEntriesRequest) (response ocicore.ListImageShapeCompatibilityEntriesResponse, err error)
}

type KmsClient interface {
	GetKey(ctx context.Context, request ocikms.GetKeyRequest) (response ocikms.GetKeyResponse, err error)
}

// nolint:lll
type BlockStorageClient interface {
	GetBootVolume(ctx context.Context, request ocicore.GetBootVolumeRequest) (response ocicore.GetBootVolumeResponse, err error)
}

// nolint:lll
type VirtualNetworkClient interface {
	GetNetworkSecurityGroup(ctx context.Context, request ocicore.GetNetworkSecurityGroupRequest) (response ocicore.GetNetworkSecurityGroupResponse, err error)
	GetSubnet(ctx context.Context, request ocicore.GetSubnetRequest) (response ocicore.GetSubnetResponse, err error)
	ListSubnets(ctx context.Context, request ocicore.ListSubnetsRequest) (response ocicore.ListSubnetsResponse, err error)
	ListNetworkSecurityGroups(ctx context.Context, request ocicore.ListNetworkSecurityGroupsRequest) (response ocicore.ListNetworkSecurityGroupsResponse, err error)
	GetVnic(ctx context.Context, request ocicore.GetVnicRequest) (response ocicore.GetVnicResponse, err error)
}

// nolint:lll
type IdentityClient interface {
	GetCompartment(ctx context.Context, request ociidentity.GetCompartmentRequest) (response ociidentity.GetCompartmentResponse, err error)
	ListAvailabilityDomains(ctx context.Context, request ociidentity.ListAvailabilityDomainsRequest) (response ociidentity.ListAvailabilityDomainsResponse, err error)
}

// nolint:lll
type WorkRequestClient interface {
	GetWorkRequest(ctx context.Context, request ociwr.GetWorkRequestRequest) (ociwr.GetWorkRequestResponse, error)
	ListWorkRequestErrors(ctx context.Context, request ociwr.ListWorkRequestErrorsRequest) (ociwr.ListWorkRequestErrorsResponse, error)
}

// nolint:lll
type ClusterPlacementGroupClient interface {
	GetClusterPlacementGroup(ctx context.Context, request ocicpg.GetClusterPlacementGroupRequest) (response ocicpg.GetClusterPlacementGroupResponse, err error)
	ListClusterPlacementGroups(ctx context.Context, request ocicpg.ListClusterPlacementGroupsRequest) (response ocicpg.ListClusterPlacementGroupsResponse, err error)
}

// newComputeClient creates a ComputeClient with the default retry policy applied
func newComputeClient(config common.ConfigurationProvider) (*ocicore.ComputeClient, error) {
	c, err := ocicore.NewComputeClientWithConfigurationProvider(config)
	if err != nil {
		return nil, err
	}

	// Apply default retry policy to all requests from this client
	c.Configuration.RetryPolicy = newRetryPolicy()
	return &c, nil
}

func NewKmsClient(config common.ConfigurationProvider, endpoint string, rateLimiter *RateLimiter) (KmsClient, error) {
	raw, err := ocikms.NewKmsManagementClientWithConfigurationProvider(config, endpoint)
	if err != nil {
		return nil, err
	}
	raw.Configuration.RetryPolicy = newRetryPolicy()
	return &kmsDecoratedClient{inner: &raw, limiter: normalizeRateLimiter(rateLimiter)}, nil
}

// newBlockStorageClient creates a BlockStorageClient with the default retry policy applied
func newBlockStorageClient(config common.ConfigurationProvider) (*ocicore.BlockstorageClient, error) {
	b, err := ocicore.NewBlockstorageClientWithConfigurationProvider(config)
	if err != nil {
		return nil, err
	}

	// Apply default retry policy to all requests from this client
	b.Configuration.RetryPolicy = newRetryPolicy()
	return &b, nil
}

// newVirtualNetworkClient creates a VirtualNetworkClient with the default retry policy applied
func newVirtualNetworkClient(config common.ConfigurationProvider) (*ocicore.VirtualNetworkClient, error) {
	v, err := ocicore.NewVirtualNetworkClientWithConfigurationProvider(config)
	if err != nil {
		return nil, err
	}

	// Apply default retry policy to all requests from this client
	v.Configuration.RetryPolicy = newRetryPolicy()
	return &v, nil
}

// newWorkRequestClient creates a WorkRequestClient with the default retry policy applied
func newWorkRequestClient(config common.ConfigurationProvider) (*ociwr.WorkRequestClient, error) {
	w, err := ociwr.NewWorkRequestClientWithConfigurationProvider(config)
	if err != nil {
		return nil, err
	}

	// Apply default retry policy to all requests from this client
	w.Configuration.RetryPolicy = newRetryPolicy()
	return &w, nil
}

// newIdentityClient creates an IdentityClient with the default retry policy applied
func newIdentityClient(config common.ConfigurationProvider) (*ociidentity.IdentityClient, error) {
	i, err := ociidentity.NewIdentityClientWithConfigurationProvider(config)
	if err != nil {
		return nil, err
	}

	// Apply default retry policy to all requests from this client
	i.Configuration.RetryPolicy = newRetryPolicy()
	return &i, nil
}

// newClusterPlacementGroupClient creates a ClusterPlacementGroupClient with the default retry policy applied
func newClusterPlacementGroupClient(config common.ConfigurationProvider) (
	*ocicpg.ClusterPlacementGroupsCPClient, error) {
	c, err := ocicpg.NewClusterPlacementGroupsCPClientWithConfigurationProvider(config)
	if err != nil {
		return nil, err
	}

	// Apply default retry policy to all requests from this client
	c.Configuration.RetryPolicy = newRetryPolicy()
	return &c, nil
}

// NewClient creates Client for all OCI services with centralized logging
func NewClient(ctx context.Context, config common.ConfigurationProvider, rateLimiter *RateLimiter) (*Client, error) {
	compute, err := newComputeClient(config)
	if err != nil {
		return nil, err
	}

	blockStorage, err := newBlockStorageClient(config)
	if err != nil {
		return nil, err
	}

	virtualNetwork, err := newVirtualNetworkClient(config)
	if err != nil {
		return nil, err
	}

	identity, err := newIdentityClient(config)
	if err != nil {
		return nil, err
	}

	workRequest, err := newWorkRequestClient(config)
	if err != nil {
		return nil, err
	}

	clusterPlacementGroup, err := newClusterPlacementGroupClient(config)
	if err != nil {
		return nil, err
	}

	rl := normalizeRateLimiter(rateLimiter)
	return newClient(
		&rateLimitedComputeClient{inner: compute, limiter: rl},
		&rateLimitedBlockStorageClient{inner: blockStorage, limiter: rl},
		&rateLimitedVirtualNetworkClient{inner: virtualNetwork, limiter: rl},
		&rateLimitedIdentityClient{inner: identity, limiter: rl},
		&rateLimitedWorkRequestClient{inner: workRequest, limiter: rl},
		&rateLimitedClusterPlacementGroupClient{inner: clusterPlacementGroup, limiter: rl},
	), nil
}

// Client wraps OCI clients with centralized logging
type Client struct {
	Compute               ComputeClient
	BlockStorage          BlockStorageClient
	VirtualNetwork        VirtualNetworkClient
	Identity              IdentityClient
	WorkRequest           WorkRequestClient
	ClusterPlacementGroup ClusterPlacementGroupClient
}

// newClient creates a new Client wrapper with the provided clients
func newClient(compute ComputeClient, blockStorage BlockStorageClient,
	virtualNetwork VirtualNetworkClient, identity IdentityClient,
	workRequest WorkRequestClient, clusterPlacementGroup ClusterPlacementGroupClient) *Client {
	return &Client{
		Compute:               compute,
		BlockStorage:          blockStorage,
		VirtualNetwork:        virtualNetwork,
		Identity:              identity,
		WorkRequest:           workRequest,
		ClusterPlacementGroup: clusterPlacementGroup,
	}
}

// decorate is a generic helper for timing and logging OCI operations
func decorate[T any, R any](ctx context.Context, operation string,
	request R, fn func() (T, error)) (T, error) {
	lg := log.FromContext(ctx)
	lgInfo := lg

	var debugLogOperations = map[string]struct{}{
		"GetWorkRequest": {},
		"GetInstance":    {},
	}

	if _, ok := debugLogOperations[operation]; ok {
		lgInfo = lgInfo.V(1)
	}

	// Start log with request
	lgInfo.Info(operation+" start", "operation", operation, "Request", request)

	measureDuration := metrics.MeasureCallDuration(operation)
	out, err := fn()
	dur := measureDuration()

	metrics.CountResponseStatus(operation, &out)

	if err != nil {
		lg.Error(err, operation+" failed", "operation", operation, "duration_ms", dur.Milliseconds())
		return out, err
	}

	lgInfo.Info(operation+" success", "operation", operation, "duration_ms", dur.Milliseconds())
	return out, nil
}

// LogWorkRequestDuration logs the server-reported duration (TimeFinished – TimeStarted) for a work request.
func LogWorkRequestDuration(ctx context.Context, operation string, wr ociwr.WorkRequest) {
	logger := log.FromContext(ctx)
	if wr.TimeStarted == nil || wr.TimeFinished == nil {
		return // No server duration available
	}

	serverElapsedDuration := wr.TimeFinished.Sub(wr.TimeStarted.Time)

	values := []any{
		"operation", operation + "TotalTime",
		"status", wr.Status,
		"duration_ms", serverElapsedDuration.Milliseconds(),
	}

	wrIdStr := ""
	if wr.Id != nil {
		values = append(values, "workRequestId", *wr.Id)
		wrIdStr = *wr.Id
	}

	logger.Info("work-request complete", values...)

	metrics.RecordWorkRequestProcessTime(serverElapsedDuration.Seconds(),
		operation, string(wr.Status), wrIdStr)
}

// --- Compute methods ---

func (o *Client) LaunchInstance(ctx context.Context, request ocicore.LaunchInstanceRequest) (
	response ocicore.LaunchInstanceResponse, err error) {
	return decorate(ctx, "LaunchInstance", request, func() (ocicore.LaunchInstanceResponse, error) {
		return o.Compute.LaunchInstance(ctx, request)
	})
}

func (o *Client) GetInstance(ctx context.Context, request ocicore.GetInstanceRequest) (
	response ocicore.GetInstanceResponse, err error) {
	return decorate(ctx, "GetInstance", request, func() (ocicore.GetInstanceResponse, error) {
		return o.Compute.GetInstance(ctx, request)
	})
}

func (o *Client) ListInstances(ctx context.Context, request ocicore.ListInstancesRequest) (
	response ocicore.ListInstancesResponse, err error) {
	return decorate(ctx, "ListInstances", request, func() (ocicore.ListInstancesResponse, error) {
		return o.Compute.ListInstances(ctx, request)
	})
}

func (o *Client) ListVnicAttachments(ctx context.Context, request ocicore.ListVnicAttachmentsRequest) (
	response ocicore.ListVnicAttachmentsResponse, err error) {
	return decorate(ctx, "ListVnicAttachments", request, func() (ocicore.ListVnicAttachmentsResponse, error) {
		return o.Compute.ListVnicAttachments(ctx, request)
	})
}

func (o *Client) ListBootVolumeAttachments(ctx context.Context, request ocicore.ListBootVolumeAttachmentsRequest) (
	response ocicore.ListBootVolumeAttachmentsResponse, err error) {
	return decorate(ctx, "ListBootVolumeAttachments",
		request, func() (ocicore.ListBootVolumeAttachmentsResponse, error) {
			return o.Compute.ListBootVolumeAttachments(ctx, request)
		})
}

func (o *Client) TerminateInstance(ctx context.Context, request ocicore.TerminateInstanceRequest) (
	response ocicore.TerminateInstanceResponse, err error) {
	return decorate(ctx, "TerminateInstance", request, func() (ocicore.TerminateInstanceResponse, error) {
		return o.Compute.TerminateInstance(ctx, request)
	})
}

func (o *Client) ListShapes(ctx context.Context, request ocicore.ListShapesRequest) (
	response ocicore.ListShapesResponse, err error) {
	return decorate(ctx, "ListShapes", request, func() (ocicore.ListShapesResponse, error) {
		return o.Compute.ListShapes(ctx, request)
	})
}

func (o *Client) GetComputeCapacityReservation(ctx context.Context,
	request ocicore.GetComputeCapacityReservationRequest) (
	response ocicore.GetComputeCapacityReservationResponse, err error) {
	return decorate(ctx, "GetComputeCapacityReservation", request,
		func() (ocicore.GetComputeCapacityReservationResponse, error) {
			return o.Compute.GetComputeCapacityReservation(ctx, request)
		})
}

func (o *Client) ListComputeCapacityReservations(ctx context.Context,
	request ocicore.ListComputeCapacityReservationsRequest) (
	response ocicore.ListComputeCapacityReservationsResponse, err error) {
	return decorate(ctx, "ListComputeCapacityReservations", request, func() (
		ocicore.ListComputeCapacityReservationsResponse, error) {
		return o.Compute.ListComputeCapacityReservations(ctx, request)
	})
}

func (o *Client) GetComputeCluster(ctx context.Context, request ocicore.GetComputeClusterRequest) (
	response ocicore.GetComputeClusterResponse, err error) {
	return decorate(ctx, "GetComputeCluster", request, func() (ocicore.GetComputeClusterResponse, error) {
		return o.Compute.GetComputeCluster(ctx, request)
	})
}

func (o *Client) ListComputeClusters(ctx context.Context, request ocicore.ListComputeClustersRequest) (
	response ocicore.ListComputeClustersResponse, err error) {
	return decorate(ctx, "ListComputeClusters", request, func() (ocicore.ListComputeClustersResponse, error) {
		return o.Compute.ListComputeClusters(ctx, request)
	})
}

func (o *Client) GetImage(ctx context.Context, request ocicore.GetImageRequest) (
	response ocicore.GetImageResponse, err error) {
	return decorate(ctx, "GetImage", request, func() (ocicore.GetImageResponse, error) {
		return o.Compute.GetImage(ctx, request)
	})
}

func (o *Client) ListImages(ctx context.Context, request ocicore.ListImagesRequest) (
	response ocicore.ListImagesResponse, err error) {
	return decorate(ctx, "ListImages", request, func() (ocicore.ListImagesResponse, error) {
		return o.Compute.ListImages(ctx, request)
	})
}

func (o *Client) ListImageShapeCompatibilityEntries(ctx context.Context,
	request ocicore.ListImageShapeCompatibilityEntriesRequest) (
	response ocicore.ListImageShapeCompatibilityEntriesResponse, err error) {
	return decorate(ctx, "ListImageShapeCompatibilityEntries", request, func() (
		ocicore.ListImageShapeCompatibilityEntriesResponse, error) {
		return o.Compute.ListImageShapeCompatibilityEntries(ctx, request)
	})
}

// --- BlockStorage methods ---

func (o *Client) GetBootVolume(ctx context.Context, request ocicore.GetBootVolumeRequest) (
	response ocicore.GetBootVolumeResponse, err error) {
	return decorate(ctx, "GetBootVolume", request, func() (ocicore.GetBootVolumeResponse, error) {
		return o.BlockStorage.GetBootVolume(ctx, request)
	})
}

// --- VirtualNetwork methods ---

func (o *Client) GetNetworkSecurityGroup(ctx context.Context, request ocicore.GetNetworkSecurityGroupRequest) (
	response ocicore.GetNetworkSecurityGroupResponse, err error) {
	return decorate(ctx, "GetNetworkSecurityGroup", request, func() (ocicore.GetNetworkSecurityGroupResponse, error) {
		return o.VirtualNetwork.GetNetworkSecurityGroup(ctx, request)
	})
}

func (o *Client) GetSubnet(ctx context.Context, request ocicore.GetSubnetRequest) (
	response ocicore.GetSubnetResponse, err error) {
	return decorate(ctx, "GetSubnet", request, func() (ocicore.GetSubnetResponse, error) {
		return o.VirtualNetwork.GetSubnet(ctx, request)
	})
}

func (o *Client) ListSubnets(ctx context.Context, request ocicore.ListSubnetsRequest) (
	response ocicore.ListSubnetsResponse, err error) {
	return decorate(ctx, "ListSubnets", request, func() (ocicore.ListSubnetsResponse, error) {
		return o.VirtualNetwork.ListSubnets(ctx, request)
	})
}

func (o *Client) ListNetworkSecurityGroups(ctx context.Context, request ocicore.ListNetworkSecurityGroupsRequest) (
	response ocicore.ListNetworkSecurityGroupsResponse, err error) {
	return decorate(ctx, "ListNetworkSecurityGroups", request, func() (
		ocicore.ListNetworkSecurityGroupsResponse, error) {
		return o.VirtualNetwork.ListNetworkSecurityGroups(ctx, request)
	})
}

func (o *Client) GetVnic(ctx context.Context, request ocicore.GetVnicRequest) (
	response ocicore.GetVnicResponse, err error) {
	return decorate(ctx, "GetVnic", request, func() (ocicore.GetVnicResponse, error) {
		return o.VirtualNetwork.GetVnic(ctx, request)
	})
}

// --- Identity methods ---

func (o *Client) GetCompartment(ctx context.Context, request ociidentity.GetCompartmentRequest) (
	response ociidentity.GetCompartmentResponse, err error) {
	return decorate(ctx, "GetCompartment", request, func() (ociidentity.GetCompartmentResponse, error) {
		return o.Identity.GetCompartment(ctx, request)
	})
}

func (o *Client) ListAvailabilityDomains(ctx context.Context, request ociidentity.ListAvailabilityDomainsRequest) (
	response ociidentity.ListAvailabilityDomainsResponse, err error) {
	return decorate(ctx, "ListAvailabilityDomains", request, func() (
		ociidentity.ListAvailabilityDomainsResponse, error) {
		return o.Identity.ListAvailabilityDomains(ctx, request)
	})
}

// --- ClusterPlacementGroup methods ---

func (o *Client) GetClusterPlacementGroup(ctx context.Context, request ocicpg.GetClusterPlacementGroupRequest) (
	response ocicpg.GetClusterPlacementGroupResponse, err error) {
	return decorate(ctx, "GetClusterPlacementGroup", request, func() (
		ocicpg.GetClusterPlacementGroupResponse, error) {
		return o.ClusterPlacementGroup.GetClusterPlacementGroup(ctx, request)
	})
}

func (o *Client) ListClusterPlacementGroups(ctx context.Context, request ocicpg.ListClusterPlacementGroupsRequest) (
	response ocicpg.ListClusterPlacementGroupsResponse, err error) {
	return decorate(ctx, "ListClusterPlacementGroups", request, func() (
		ocicpg.ListClusterPlacementGroupsResponse, error) {
		return o.ClusterPlacementGroup.ListClusterPlacementGroups(ctx, request)
	})
}

// --- WorkRequest methods ---

func (o *Client) GetWorkRequest(ctx context.Context, request ociwr.GetWorkRequestRequest) (
	response ociwr.GetWorkRequestResponse, err error) {
	return decorate(ctx, "GetWorkRequest", request, func() (ociwr.GetWorkRequestResponse, error) {
		return o.WorkRequest.GetWorkRequest(ctx, request)
	})
}

func (o *Client) ListWorkRequestErrors(ctx context.Context, request ociwr.ListWorkRequestErrorsRequest) (
	response ociwr.ListWorkRequestErrorsResponse, err error) {
	return decorate(ctx, "ListWorkRequestErrors", request, func() (ociwr.ListWorkRequestErrorsResponse, error) {
		return o.WorkRequest.ListWorkRequestErrors(ctx, request)
	})
}
