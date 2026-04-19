/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package oci

import (
	"context"

	ocicpg "github.com/oracle/oci-go-sdk/v65/clusterplacementgroups"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	ociidentity "github.com/oracle/oci-go-sdk/v65/identity"
	ocikms "github.com/oracle/oci-go-sdk/v65/keymanagement"
	ociwr "github.com/oracle/oci-go-sdk/v65/workrequests"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	readMode  = "read"
	writeMode = "write"
)

type rateLimitedComputeClient struct {
	inner   *ocicore.ComputeClient
	limiter RateLimiter
}

type rateLimitedBlockStorageClient struct {
	inner   *ocicore.BlockstorageClient
	limiter RateLimiter
}

type rateLimitedVirtualNetworkClient struct {
	inner   *ocicore.VirtualNetworkClient
	limiter RateLimiter
}

type rateLimitedIdentityClient struct {
	inner   *ociidentity.IdentityClient
	limiter RateLimiter
}

type rateLimitedWorkRequestClient struct {
	inner   *ociwr.WorkRequestClient
	limiter RateLimiter
}

type rateLimitedClusterPlacementGroupClient struct {
	inner   *ocicpg.ClusterPlacementGroupsCPClient
	limiter RateLimiter
}

type kmsDecoratedClient struct {
	inner   *ocikms.KmsManagementClient
	limiter RateLimiter
}

func waitForToken(ctx context.Context, limiter flowcontrol.RateLimiter, operation, mode string) error {
	if err := limiter.Wait(ctx); err != nil {
		log.FromContext(ctx).Error(err, "OCI rate limiter wait failed", "operation", operation, "mode", mode)
		return err
	}
	return nil
}

func callWithReadLimit[T any, R any](ctx context.Context, limiter RateLimiter,
	operation string, _ R, fn func() (T, error)) (T, error) {
	var zero T
	if err := waitForToken(ctx, limiter.Reader, operation, readMode); err != nil {
		return zero, err
	}
	return fn()
}

func callWithWriteLimit[T any, R any](ctx context.Context, limiter RateLimiter,
	operation string, _ R, fn func() (T, error)) (T, error) {
	var zero T
	if err := waitForToken(ctx, limiter.Writer, operation, writeMode); err != nil {
		return zero, err
	}
	return fn()
}

func callWithDecoratedReadLimit[T any, R any](ctx context.Context, limiter RateLimiter,
	operation string, request R, fn func() (T, error)) (T, error) {
	var zero T
	if err := waitForToken(ctx, limiter.Reader, operation, readMode); err != nil {
		return zero, err
	}
	return decorate(ctx, operation, request, fn)
}

func (c *rateLimitedComputeClient) LaunchInstance(ctx context.Context,
	req ocicore.LaunchInstanceRequest) (ocicore.LaunchInstanceResponse, error) {
	return callWithWriteLimit(ctx, c.limiter, "LaunchInstance", req, func() (ocicore.LaunchInstanceResponse, error) {
		return c.inner.LaunchInstance(ctx, req)
	})
}

func (c *rateLimitedComputeClient) GetInstance(ctx context.Context,
	req ocicore.GetInstanceRequest) (ocicore.GetInstanceResponse, error) {
	return callWithReadLimit(ctx, c.limiter, "GetInstance", req, func() (ocicore.GetInstanceResponse, error) {
		return c.inner.GetInstance(ctx, req)
	})
}

func (c *rateLimitedComputeClient) ListInstances(ctx context.Context,
	req ocicore.ListInstancesRequest) (ocicore.ListInstancesResponse, error) {
	return callWithReadLimit(ctx, c.limiter, "ListInstances", req, func() (ocicore.ListInstancesResponse, error) {
		return c.inner.ListInstances(ctx, req)
	})
}

func (c *rateLimitedComputeClient) ListVnicAttachments(ctx context.Context,
	req ocicore.ListVnicAttachmentsRequest) (ocicore.ListVnicAttachmentsResponse, error) {
	return callWithReadLimit(ctx, c.limiter, "ListVnicAttachments", req,
		func() (ocicore.ListVnicAttachmentsResponse, error) {
			return c.inner.ListVnicAttachments(ctx, req)
		})
}

func (c *rateLimitedComputeClient) ListBootVolumeAttachments(ctx context.Context,
	req ocicore.ListBootVolumeAttachmentsRequest) (ocicore.ListBootVolumeAttachmentsResponse, error) {
	return callWithReadLimit(ctx, c.limiter, "ListBootVolumeAttachments", req,
		func() (ocicore.ListBootVolumeAttachmentsResponse, error) {
			return c.inner.ListBootVolumeAttachments(ctx, req)
		})
}

func (c *rateLimitedComputeClient) TerminateInstance(ctx context.Context,
	req ocicore.TerminateInstanceRequest) (ocicore.TerminateInstanceResponse, error) {
	return callWithWriteLimit(ctx, c.limiter, "TerminateInstance", req, func() (ocicore.TerminateInstanceResponse, error) {
		return c.inner.TerminateInstance(ctx, req)
	})
}

func (c *rateLimitedComputeClient) ListShapes(ctx context.Context,
	req ocicore.ListShapesRequest) (ocicore.ListShapesResponse, error) {
	return callWithReadLimit(ctx, c.limiter, "ListShapes", req, func() (ocicore.ListShapesResponse, error) {
		return c.inner.ListShapes(ctx, req)
	})
}

func (c *rateLimitedComputeClient) GetComputeCapacityReservation(ctx context.Context,
	req ocicore.GetComputeCapacityReservationRequest) (ocicore.GetComputeCapacityReservationResponse, error) {
	return callWithReadLimit(ctx, c.limiter, "GetComputeCapacityReservation", req,
		func() (ocicore.GetComputeCapacityReservationResponse, error) {
			return c.inner.GetComputeCapacityReservation(ctx, req)
		})
}

func (c *rateLimitedComputeClient) ListComputeCapacityReservations(ctx context.Context,
	req ocicore.ListComputeCapacityReservationsRequest) (ocicore.ListComputeCapacityReservationsResponse, error) {
	return callWithReadLimit(ctx, c.limiter, "ListComputeCapacityReservations", req,
		func() (ocicore.ListComputeCapacityReservationsResponse, error) {
			return c.inner.ListComputeCapacityReservations(ctx, req)
		})
}

func (c *rateLimitedComputeClient) GetComputeCluster(ctx context.Context,
	req ocicore.GetComputeClusterRequest) (ocicore.GetComputeClusterResponse, error) {
	return callWithReadLimit(ctx, c.limiter, "GetComputeCluster", req, func() (ocicore.GetComputeClusterResponse, error) {
		return c.inner.GetComputeCluster(ctx, req)
	})
}

func (c *rateLimitedComputeClient) ListComputeClusters(ctx context.Context,
	req ocicore.ListComputeClustersRequest) (ocicore.ListComputeClustersResponse, error) {
	return callWithReadLimit(ctx, c.limiter, "ListComputeClusters", req, func() (ocicore.ListComputeClustersResponse, error) {
		return c.inner.ListComputeClusters(ctx, req)
	})
}

func (c *rateLimitedComputeClient) GetImage(ctx context.Context,
	req ocicore.GetImageRequest) (ocicore.GetImageResponse, error) {
	return callWithReadLimit(ctx, c.limiter, "GetImage", req, func() (ocicore.GetImageResponse, error) {
		return c.inner.GetImage(ctx, req)
	})
}

func (c *rateLimitedComputeClient) ListImages(ctx context.Context,
	req ocicore.ListImagesRequest) (ocicore.ListImagesResponse, error) {
	return callWithReadLimit(ctx, c.limiter, "ListImages", req, func() (ocicore.ListImagesResponse, error) {
		return c.inner.ListImages(ctx, req)
	})
}

func (c *rateLimitedComputeClient) ListImageShapeCompatibilityEntries(ctx context.Context,
	req ocicore.ListImageShapeCompatibilityEntriesRequest) (ocicore.ListImageShapeCompatibilityEntriesResponse, error) {
	return callWithReadLimit(ctx, c.limiter, "ListImageShapeCompatibilityEntries", req,
		func() (ocicore.ListImageShapeCompatibilityEntriesResponse, error) {
			return c.inner.ListImageShapeCompatibilityEntries(ctx, req)
		})
}

func (b *rateLimitedBlockStorageClient) GetBootVolume(ctx context.Context,
	req ocicore.GetBootVolumeRequest) (ocicore.GetBootVolumeResponse, error) {
	return callWithReadLimit(ctx, b.limiter, "GetBootVolume", req, func() (ocicore.GetBootVolumeResponse, error) {
		return b.inner.GetBootVolume(ctx, req)
	})
}

func (v *rateLimitedVirtualNetworkClient) GetNetworkSecurityGroup(ctx context.Context,
	req ocicore.GetNetworkSecurityGroupRequest) (ocicore.GetNetworkSecurityGroupResponse, error) {
	return callWithReadLimit(ctx, v.limiter, "GetNetworkSecurityGroup", req,
		func() (ocicore.GetNetworkSecurityGroupResponse, error) {
			return v.inner.GetNetworkSecurityGroup(ctx, req)
		})
}

func (v *rateLimitedVirtualNetworkClient) GetSubnet(ctx context.Context,
	req ocicore.GetSubnetRequest) (ocicore.GetSubnetResponse, error) {
	return callWithReadLimit(ctx, v.limiter, "GetSubnet", req, func() (ocicore.GetSubnetResponse, error) {
		return v.inner.GetSubnet(ctx, req)
	})
}

func (v *rateLimitedVirtualNetworkClient) ListSubnets(ctx context.Context,
	req ocicore.ListSubnetsRequest) (ocicore.ListSubnetsResponse, error) {
	return callWithReadLimit(ctx, v.limiter, "ListSubnets", req, func() (ocicore.ListSubnetsResponse, error) {
		return v.inner.ListSubnets(ctx, req)
	})
}

func (v *rateLimitedVirtualNetworkClient) ListNetworkSecurityGroups(ctx context.Context,
	req ocicore.ListNetworkSecurityGroupsRequest) (ocicore.ListNetworkSecurityGroupsResponse, error) {
	return callWithReadLimit(ctx, v.limiter, "ListNetworkSecurityGroups", req,
		func() (ocicore.ListNetworkSecurityGroupsResponse, error) {
			return v.inner.ListNetworkSecurityGroups(ctx, req)
		})
}

func (v *rateLimitedVirtualNetworkClient) GetVnic(ctx context.Context,
	req ocicore.GetVnicRequest) (ocicore.GetVnicResponse, error) {
	return callWithReadLimit(ctx, v.limiter, "GetVnic", req, func() (ocicore.GetVnicResponse, error) {
		return v.inner.GetVnic(ctx, req)
	})
}

func (i *rateLimitedIdentityClient) GetCompartment(ctx context.Context,
	req ociidentity.GetCompartmentRequest) (ociidentity.GetCompartmentResponse, error) {
	return callWithReadLimit(ctx, i.limiter, "GetCompartment", req, func() (ociidentity.GetCompartmentResponse, error) {
		return i.inner.GetCompartment(ctx, req)
	})
}

func (i *rateLimitedIdentityClient) ListAvailabilityDomains(ctx context.Context,
	req ociidentity.ListAvailabilityDomainsRequest) (ociidentity.ListAvailabilityDomainsResponse, error) {
	return callWithReadLimit(ctx, i.limiter, "ListAvailabilityDomains", req,
		func() (ociidentity.ListAvailabilityDomainsResponse, error) {
			return i.inner.ListAvailabilityDomains(ctx, req)
		})
}

func (w *rateLimitedWorkRequestClient) GetWorkRequest(ctx context.Context,
	req ociwr.GetWorkRequestRequest) (ociwr.GetWorkRequestResponse, error) {
	return callWithReadLimit(ctx, w.limiter, "GetWorkRequest", req, func() (ociwr.GetWorkRequestResponse, error) {
		return w.inner.GetWorkRequest(ctx, req)
	})
}

func (w *rateLimitedWorkRequestClient) ListWorkRequestErrors(ctx context.Context,
	req ociwr.ListWorkRequestErrorsRequest) (ociwr.ListWorkRequestErrorsResponse, error) {
	return callWithReadLimit(ctx, w.limiter, "ListWorkRequestErrors", req,
		func() (ociwr.ListWorkRequestErrorsResponse, error) {
			return w.inner.ListWorkRequestErrors(ctx, req)
		})
}

func (c *rateLimitedClusterPlacementGroupClient) GetClusterPlacementGroup(ctx context.Context,
	req ocicpg.GetClusterPlacementGroupRequest) (ocicpg.GetClusterPlacementGroupResponse, error) {
	return callWithReadLimit(ctx, c.limiter, "GetClusterPlacementGroup", req,
		func() (ocicpg.GetClusterPlacementGroupResponse, error) {
			return c.inner.GetClusterPlacementGroup(ctx, req)
		})
}

func (c *rateLimitedClusterPlacementGroupClient) ListClusterPlacementGroups(ctx context.Context,
	req ocicpg.ListClusterPlacementGroupsRequest) (ocicpg.ListClusterPlacementGroupsResponse, error) {
	return callWithReadLimit(ctx, c.limiter, "ListClusterPlacementGroups", req,
		func() (ocicpg.ListClusterPlacementGroupsResponse, error) {
			return c.inner.ListClusterPlacementGroups(ctx, req)
		})
}

func (k *kmsDecoratedClient) GetKey(ctx context.Context,
	req ocikms.GetKeyRequest) (ocikms.GetKeyResponse, error) {
	return callWithDecoratedReadLimit(ctx, k.limiter, "GetKey", req, func() (ocikms.GetKeyResponse, error) {
		return k.inner.GetKey(ctx, req)
	})
}
