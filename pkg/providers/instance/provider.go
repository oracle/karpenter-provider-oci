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
	"strings"
	"time"

	"github.com/awslabs/operatorpkg/object"
	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/cache"
	"github.com/oracle/karpenter-provider-oci/pkg/oci"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/image"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancemeta"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancetype"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/kms"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/npn"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/placement"
	"github.com/oracle/karpenter-provider-oci/pkg/utils"
	"github.com/oracle/oci-go-sdk/v65/common"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	ociwr "github.com/oracle/oci-go-sdk/v65/workrequests"
	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

const (
	NodePoolOciFreeFormTagKey      = "KarpenterNodePool"
	NodePoolUIDOciFreeFormTagKey   = "KarpenterNodePoolUID"
	NodeClassOciFreeFormTagKey     = "KarpenterNodeClass"
	NodeClassHashOciFreeFormTagKey = "Karpenter_NodeClass_Hash"
)

var errDriftCachesRequired = errors.New("drift caches are required")

type Provider interface {
	LaunchInstance(ctx context.Context,
		nodeClaim *corev1.NodeClaim,
		nodeClass *v1beta1.OCINodeClass,
		instanceType *instancetype.OciInstanceType,
		imageResolveResult *image.ImageResolveResult,
		networkResolveResult *network.NetworkResolveResult,
		kmsKeyResolveResult *kms.KmsKeyResolveResult,
		placementProposal *placement.Proposal) (*InstanceInfo, error)

	DeleteInstance(ctx context.Context, instanceOcid string) error

	GetInstance(ctx context.Context, instanceOcid string) (*InstanceInfo, error)

	GetInstanceCached(ctx context.Context, instanceOcid string) (*InstanceInfo, error)

	GetInstanceCompartment(nodeClass *v1beta1.OCINodeClass) string

	ListInstances(ctx context.Context, compartmentIDs []string) ([]*ocicore.Instance, error)

	ListInstanceBootVolumeAttachments(ctx context.Context,
		compartmentOcid string, instanceOcid string, ad string) ([]*ocicore.BootVolumeAttachment, error)

	ListInstanceBootVolumeAttachmentsCached(ctx context.Context,
		compartmentOcid, instanceOcid, ad string) ([]*ocicore.BootVolumeAttachment, error)

	ListInstanceVnicAttachments(ctx context.Context,
		compartmentOcid string, instanceOcid string) ([]*ocicore.VnicAttachment, error)

	ListInstanceVnicAttachmentsCached(ctx context.Context,
		compartmentOcid, instanceOcid string) ([]*ocicore.VnicAttachment, error)
}

type DefaultProvider struct {
	computeClient                                    oci.ComputeClient
	workRequestClient                                oci.WorkRequestClient
	clusterCompartmentId                             string
	instanceMetaProvider                             instancemeta.Provider
	instanceCache                                    *cache.GetOrLoadCache[*InstanceInfo]
	vnicAttachCache                                  *cache.GetOrLoadCache[[]*ocicore.VnicAttachment]
	bootVolAttachCache                               *cache.GetOrLoadCache[[]*ocicore.BootVolumeAttachment]
	launchTimeoutVM                                  time.Duration
	launchTimeoutBM                                  time.Duration
	launchTimeOutFailOver                            bool
	pollInterval                                     time.Duration
	unavailableOfferings                             *cache.UnavailableOfferings
	enableUnavailableOfferingsOnServiceLimitExceeded bool
}

func NewProvider(ctx context.Context, computeClient oci.ComputeClient,
	workRequestClient oci.WorkRequestClient,
	clusterCompartmentId string,
	instanceMetaProvider instancemeta.Provider,
	driftCaches *DriftCaches,
	launchTimeoutVM, launchTimeoutBM time.Duration,
	launchTimeOutFailOver bool,
	pollInterval time.Duration,
	unavailableOfferings *cache.UnavailableOfferings,
	enableUnavailableOfferingsOnServiceLimitExceeded bool) (*DefaultProvider, error) {
	if driftCaches == nil {
		return nil, errDriftCachesRequired
	}
	p := &DefaultProvider{
		computeClient:         computeClient,
		workRequestClient:     workRequestClient,
		clusterCompartmentId:  clusterCompartmentId,
		instanceMetaProvider:  instanceMetaProvider,
		instanceCache:         driftCaches.instances,
		vnicAttachCache:       driftCaches.vnicAttachments,
		bootVolAttachCache:    driftCaches.bootVolumeAttachments,
		launchTimeoutVM:       launchTimeoutVM,
		launchTimeoutBM:       launchTimeoutBM,
		launchTimeOutFailOver: launchTimeOutFailOver,
		pollInterval:          pollInterval,
		unavailableOfferings:  unavailableOfferings,
		enableUnavailableOfferingsOnServiceLimitExceeded: enableUnavailableOfferingsOnServiceLimitExceeded,
	}

	return p, nil
}

func (p *DefaultProvider) LaunchInstance(ctx context.Context,
	nodeClaim *corev1.NodeClaim,
	nodeClass *v1beta1.OCINodeClass,
	instanceType *instancetype.OciInstanceType,
	imageResolveResult *image.ImageResolveResult,
	networkResolveResult *network.NetworkResolveResult,
	kmsKeyResolveResult *kms.KmsKeyResolveResult,
	placementProposal *placement.Proposal) (result *InstanceInfo, err error) {
	imageSource := ocicore.InstanceSourceViaImageDetails{
		ImageId:  imageResolveResult.Images[0].Id,
		KmsKeyId: nil,
	}

	if kmsKeyResolveResult != nil {
		imageSource.KmsKeyId = &kmsKeyResolveResult.Ocid
	}

	if nodeClass.Spec.VolumeConfig.BootVolumeConfig.SizeInGBs != nil {
		imageSource.BootVolumeSizeInGBs = nodeClass.Spec.VolumeConfig.BootVolumeConfig.SizeInGBs
	}

	if nodeClass.Spec.VolumeConfig.BootVolumeConfig.VpusPerGB != nil {
		imageSource.BootVolumeVpusPerGB = nodeClass.Spec.VolumeConfig.BootVolumeConfig.VpusPerGB
	}

	capacityType := decideCapacityType(ctx, nodeClaim, instanceType)
	var preemptibleInstanceConfig *ocicore.PreemptibleInstanceConfigDetails
	isPreemptible := false
	if capacityType == corev1.CapacityTypeSpot {
		preemptibleInstanceConfig = &ocicore.PreemptibleInstanceConfigDetails{
			PreemptionAction: &ocicore.TerminatePreemptionAction{},
		}
		isPreemptible = true
	}

	// When a launch attempt fails because of a skippable capacity exhaustion, record the
	// (shape, ocpu/memory, AD/zone, capacity-type) offering as unavailable so it is excluded from
	// offering availability until the cache entry expires, enabling spot->on-demand and
	// cross-NodePool fallback. Scoping by the flexible-shape CPU/memory configuration keeps a
	// failure for one config from suppressing the shape's other configs.
	if p.unavailableOfferings != nil {
		defer func() {
			if !IsSkippableLaunchError(err, p.enableUnavailableOfferingsOnServiceLimitExceeded) {
				return
			}

			// The unavailable-offerings cache key does not include placement scope
			// (capacity reservation, compute cluster, cluster placement group, or fault domain).
			// A failure scoped to one of those is narrower than the generic
			// (shape, config, zone, capacity-type) key, so caching it would wrongly suppress
			// otherwise-valid generic capacity for other NodeClaims. Two proposals can only share
			// this key if they differ by one of these fields, so skipping them here also prevents a
			// single failed proposal from suppressing an offering another proposal could still
			// launch. This deliberately gives up cache-based fallback for placement-scoped
			// failures; a future solution could include the placement scope in the cache key.
			if placementProposal.CapacityReservationId != nil ||
				placementProposal.ComputeClusterId != nil ||
				placementProposal.ClusterPlacementGroupId != nil ||
				placementProposal.Fd != nil {
				return
			}

			// QuotaExceeded is an administrator-defined quota failure for a specific compartment
			// and resource, so scope the cache entry to the target compartment; otherwise a quota
			// failure in one node compartment would wrongly suppress the same offering for
			// NodeClasses launching into other compartments. Host-capacity exhaustion and
			// tenancy-scoped LimitExceeded service limits apply regardless of compartment, so they
			// use an empty (tenancy-wide) compartment scope.
			compartment := ""
			if oci.IsQuotaExceeded(err) {
				compartment = p.GetInstanceCompartment(nodeClass)
			}

			p.unavailableOfferings.MarkUnavailable(ctx, instanceType.Shape,
				instanceType.Ocpu, instanceType.MemoryInGbs,
				utils.AdToZoneLabelValue(placementProposal.Ad), capacityType, compartment)
		}()
	}

	metadata, err := p.instanceMetaProvider.BuildInstanceMetadata(ctx, nodeClaim, nodeClass,
		imageResolveResult, networkResolveResult, isPreemptible)
	if err != nil {
		return nil, err
	}

	imdsV1Disabled := true

	IsPvEncryptionInTransitEnabled := false
	if nodeClass.Spec.VolumeConfig.BootVolumeConfig.PvEncryptionInTransit != nil {
		IsPvEncryptionInTransitEnabled = *nodeClass.Spec.VolumeConfig.BootVolumeConfig.PvEncryptionInTransit
	}

	instanceCompartment := p.GetInstanceCompartment(nodeClass)

	launchOptions := BuildLaunchOptions(nodeClass.Spec.LaunchOptions)

	agentConfig := buildAgentConfig(nodeClass.Spec.AgentPlugins)

	freeFormTags, err := buildFreeFormTags(nodeClass, nodeClaim)
	if err != nil {
		return nil, err
	}

	// construct launch instance request.
	launchRequest := ocicore.LaunchInstanceRequest{
		LaunchInstanceDetails: ocicore.LaunchInstanceDetails{
			AvailabilityDomain:      &placementProposal.Ad,
			CompartmentId:           &instanceCompartment,
			FaultDomain:             placementProposal.Fd,
			ComputeClusterId:        placementProposal.ComputeClusterId,
			CapacityReservationId:   placementProposal.CapacityReservationId,
			ClusterPlacementGroupId: placementProposal.ClusterPlacementGroupId,
			CreateVnicDetails: buildCreateVnicDetails(networkResolveResult,
				nodeClass.Spec.NetworkConfig.PrimaryVnicConfig),
			DefinedTags:               buildDefinedTags(nodeClass.Spec.DefinedTags),
			DisplayName:               &nodeClaim.Name,
			FreeformTags:              freeFormTags,
			Metadata:                  metadata,
			PreemptibleInstanceConfig: preemptibleInstanceConfig, // preemptible support
			Shape:                     &instanceType.Shape,
			ShapeConfig:               buildShapeConfigFromInstanceType(instanceType), // flexible + burstable support
			SourceDetails:             imageSource,
			InstanceOptions: &ocicore.InstanceOptions{
				AreLegacyImdsEndpointsDisabled: &imdsV1Disabled, // disable imdsv1
			},
			IsPvEncryptionInTransitEnabled: &IsPvEncryptionInTransitEnabled,
			LaunchOptions:                  launchOptions,
			AgentConfig:                    agentConfig,
		},
	}

	resp, err := p.computeClient.LaunchInstance(ctx, launchRequest)
	if err != nil {
		if oci.IsOutOfHostCapacity(err) {
			return nil, NoCapacityError{}
		}
		return nil, err
	}

	wrID := resp.RawResponse.Header.Get("opc-work-request-id")
	if wrID == "" {
		return nil, errors.New("work request ID not found in LaunchInstance response headers")
	}

	instance := &InstanceInfo{
		&resp.Instance,
		*resp.Etag,
	}

	// Select timeout based on instance shape (BM vs VM)
	timeout := p.launchTimeoutVM
	if instancetype.IsBmShape(instanceType.Shape) {
		timeout = p.launchTimeoutBM
	}

	// Wait for work request to complete, inline from waitForWorkRequest
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, errors.New("context cancelled")
		case <-timer.C:
			result, resultErr := p.instanceInProvisioningOrPlacementTimeOut(ctx, instance)
			p.cacheInstance(result)
			return result, resultErr
		case <-time.After(p.pollInterval):
			wrResp, getWrErr := p.workRequestClient.GetWorkRequest(ctx, ociwr.GetWorkRequestRequest{
				WorkRequestId: &wrID,
			})
			if getWrErr != nil {
				log.FromContext(ctx).Error(getWrErr, "Unable to get work-request")
				continue
			}
			switch wrResp.WorkRequest.Status {
			case ociwr.WorkRequestStatusSucceeded:
				oci.LogWorkRequestDuration(ctx, "LaunchInstance", wrResp.WorkRequest)
				p.cacheInstance(instance)
				return instance, nil
			case ociwr.WorkRequestStatusFailed, ociwr.WorkRequestStatusCanceled, ociwr.WorkRequestStatusCanceling:
				oci.LogWorkRequestDuration(ctx, "LaunchInstance", wrResp.WorkRequest)
				errResp, listWrErr := p.workRequestClient.ListWorkRequestErrors(ctx, ociwr.ListWorkRequestErrorsRequest{
					WorkRequestId: &wrID,
				})
				if listWrErr != nil {
					return nil, errors.New("work-request failed")
				}
				if len(errResp.Items) > 0 && errResp.Items[0].Message != nil {
					err = errors.New(*errResp.Items[0].Message)
					if oci.IsOutOfHostCapacity(err) {
						return nil, NoCapacityError{}
					}
					return nil, err
				}
				return nil, fmt.Errorf("work-request failed %s", wrID)
			}
		}
	}
}

func (p *DefaultProvider) instanceInProvisioningOrPlacementTimeOut(ctx context.Context,
	instance *InstanceInfo) (*InstanceInfo, error) {
	if !p.launchTimeOutFailOver {
		return instance, nil
	}

	if instancetype.IsBmShape(*instance.Shape) {
		return instance, nil // return bm shapes, hopefully it can reach running state eventually
	}

	bootVolumes, err := p.ListInstanceBootVolumeAttachments(ctx, *instance.CompartmentId,
		*instance.Id, *instance.AvailabilityDomain)
	if err != nil || len(bootVolumes) > 0 {
		// failed to check boot volumes or boot volume exists, return the launched instance,
		// and defer it to nodeClaim register time out flow to decide
		return instance, nil
	}

	// got a VM didn't get placed in timeout window, try to delete the instance and return noCapacityError.
	// must clean up existing instance to avoid launching multiple instances against a single nodeClaim
	err = p.DeleteInstance(ctx, *instance.Id)
	if err != nil {
		// failed to delete the launched instance, return it and defer to nodeClaim register time out flow
		return instance, nil
	}

	return nil, NoCapacityError{} // make up NoCapacityError so cloudProvider can move to next instance type
}

func buildDefinedTags(tags map[string]map[string]string) map[string]map[string]interface{} {
	out := make(map[string]map[string]interface{})
	for k, v := range tags {
		m := make(map[string]interface{})
		for ik, iv := range v {
			m[ik] = iv
		}

		out[k] = m
	}

	return out
}

func (p *DefaultProvider) GetInstanceCompartment(nodeClass *v1beta1.OCINodeClass) string {
	instanceCompartmentId := p.clusterCompartmentId
	if nodeClass.Spec.NodeCompartmentId != nil {
		instanceCompartmentId = *nodeClass.Spec.NodeCompartmentId
	}

	return instanceCompartmentId
}

func (p *DefaultProvider) GetInstance(ctx context.Context, instanceOcid string) (*InstanceInfo, error) {
	instanceInfo, err := p.instanceCache.Refresh(ctx, instanceOcid,
		func(ctx context.Context, key string) (*InstanceInfo, error) {
			return p.getInstanceImpl(ctx, key)
		})
	p.evictInstanceIfGone(instanceOcid, instanceInfo, err)
	return instanceInfo, err
}

func (p *DefaultProvider) getInstanceImpl(ctx context.Context, instanceOcid string) (*InstanceInfo, error) {
	resp, err := p.computeClient.GetInstance(ctx, ocicore.GetInstanceRequest{
		InstanceId: &instanceOcid,
	})
	if err != nil {
		return nil, err
	}

	return &InstanceInfo{Instance: &resp.Instance, etag: *resp.Etag}, nil
}

func (p *DefaultProvider) GetInstanceCached(ctx context.Context, instanceOcid string) (*InstanceInfo, error) {
	instanceInfo, err := p.instanceCache.GetOrLoad(ctx, instanceOcid,
		func(ctx context.Context, key string) (*InstanceInfo, error) {
			return p.getInstanceImpl(ctx, key)
		})
	p.evictInstanceIfGone(instanceOcid, instanceInfo, err)
	return instanceInfo, err
}

func (p *DefaultProvider) evictInstanceIfGone(instanceOcid string, instanceInfo *InstanceInfo, err error) {
	if oci.IsNotFound(err) || (err == nil && IsInstanceTerminated(instanceInfo)) {
		p.evictInstanceCaches(instanceOcid)
	}
}

func (p *DefaultProvider) cacheInstance(instanceInfo *InstanceInfo) {
	if p.instanceCache == nil || instanceInfo == nil || instanceInfo.Instance == nil ||
		instanceInfo.Id == nil || IsInstanceTerminated(instanceInfo) {
		return
	}
	p.instanceCache.Set(*instanceInfo.Id, instanceInfo)
}

func (p *DefaultProvider) ListInstanceVnicAttachments(ctx context.Context,
	compartmentOcid string, instanceOcid string) ([]*ocicore.VnicAttachment, error) {
	attachments, err := p.listInstanceVnicAttachmentsImpl(ctx, compartmentOcid, instanceOcid)
	if err != nil {
		return nil, err
	}
	p.vnicAttachCache.Evict(instanceOcid)
	p.vnicAttachCache.Set(instanceOcid, attachments)
	return attachments, nil
}

func (p *DefaultProvider) listInstanceVnicAttachmentsImpl(ctx context.Context,
	compartmentOcid string, instanceOcid string) ([]*ocicore.VnicAttachment, error) {
	var vnicAttachments []*ocicore.VnicAttachment
	var page *string
	for {
		listVnicAttachmentsResp, err := p.computeClient.ListVnicAttachments(ctx, ocicore.ListVnicAttachmentsRequest{
			InstanceId:    &instanceOcid,
			CompartmentId: &compartmentOcid,
			Page:          page,
		})

		if err != nil {
			return nil, err
		}

		vnicAttachments = append(vnicAttachments, lo.ToSlicePtr(listVnicAttachmentsResp.Items)...)
		page = listVnicAttachmentsResp.OpcNextPage
		if page == nil {
			break
		}
	}

	return vnicAttachments, nil
}

func (p *DefaultProvider) ListInstanceVnicAttachmentsCached(ctx context.Context,
	compartmentOcid, instanceOcid string) ([]*ocicore.VnicAttachment, error) {
	return p.vnicAttachCache.GetOrLoad(ctx, instanceOcid,
		func(ctx context.Context, key string) ([]*ocicore.VnicAttachment, error) {
			return p.listInstanceVnicAttachmentsImpl(ctx, compartmentOcid, instanceOcid)
		})
}

func (p *DefaultProvider) ListInstanceBootVolumeAttachments(ctx context.Context,
	compartmentOcid string, instanceOcid string, ad string) ([]*ocicore.BootVolumeAttachment, error) {
	attachments, err := p.listInstanceBootVolumeAttachmentsImpl(ctx, compartmentOcid, instanceOcid, ad)
	if err != nil {
		return nil, err
	}
	p.bootVolAttachCache.Evict(instanceOcid)
	p.bootVolAttachCache.Set(instanceOcid, attachments)
	return attachments, nil
}

func (p *DefaultProvider) listInstanceBootVolumeAttachmentsImpl(ctx context.Context,
	compartmentOcid string, instanceOcid string, ad string) ([]*ocicore.BootVolumeAttachment, error) {
	var bootVolumeAttachments []*ocicore.BootVolumeAttachment
	var page *string
	for {
		listBvaResp, err := p.computeClient.ListBootVolumeAttachments(ctx, ocicore.ListBootVolumeAttachmentsRequest{
			CompartmentId:      &compartmentOcid,
			InstanceId:         &instanceOcid,
			AvailabilityDomain: &ad,
			Page:               page,
		})

		if err != nil {
			return nil, err
		}

		bootVolumeAttachments = append(bootVolumeAttachments, lo.ToSlicePtr(listBvaResp.Items)...)
		page = listBvaResp.OpcNextPage

		if page == nil {
			break
		}
	}

	return bootVolumeAttachments, nil
}

func (p *DefaultProvider) ListInstanceBootVolumeAttachmentsCached(ctx context.Context,
	compartmentOcid, instanceOcid, ad string) ([]*ocicore.BootVolumeAttachment, error) {
	return p.bootVolAttachCache.GetOrLoad(ctx, instanceOcid,
		func(ctx context.Context, key string) ([]*ocicore.BootVolumeAttachment, error) {
			return p.listInstanceBootVolumeAttachmentsImpl(ctx, compartmentOcid, instanceOcid, ad)
		})
}

func (p *DefaultProvider) ListInstances(ctx context.Context, compartmentIDs []string) ([]*ocicore.Instance, error) {
	var instances []*ocicore.Instance
	seenInstanceIDs := map[string]struct{}{}
	listedCompartments := map[string]struct{}{}
	for _, compartmentID := range compartmentIDs {
		if compartmentID == "" {
			log.FromContext(ctx).V(1).Info("Node compartmentId is missing, falling back to cluster compartmentId")
			compartmentID = p.clusterCompartmentId
		}
		if _, listed := listedCompartments[compartmentID]; listed {
			continue
		}
		listedCompartments[compartmentID] = struct{}{}

		listed, err := p.listInstances(ctx, compartmentID)
		if err != nil {
			return nil, err
		}
		instances = append(instances, listed...)
		for _, item := range listed {
			if item.Id != nil {
				seenInstanceIDs[*item.Id] = struct{}{}
			}
		}
	}

	p.evictUnseenInstanceCaches(seenInstanceIDs)
	return instances, nil
}

func (p *DefaultProvider) listInstances(ctx context.Context, compartmentID string) ([]*ocicore.Instance, error) {
	var page *string
	var instances []*ocicore.Instance
	for {
		resp, err := p.computeClient.ListInstances(ctx, ocicore.ListInstancesRequest{
			CompartmentId: &compartmentID,
			Limit:         common.Int(100),
			Page:          page,
		})

		if err != nil {
			return nil, err
		}

		for i := range resp.Items {
			item := &resp.Items[i]
			if _, ok := GetNodePoolNameFromInstance(item); !ok || item.Id == nil {
				continue
			}
			if item.LifecycleState == ocicore.InstanceLifecycleStateTerminated {
				continue
			}
			instances = append(instances, item)
			p.cacheInstance(&InstanceInfo{Instance: item})
		}

		page = resp.OpcNextPage
		if page == nil {
			break
		}
	}

	return instances, nil
}

func (p *DefaultProvider) evictInstanceCaches(instanceOcid string) {
	if p.instanceCache != nil {
		p.instanceCache.Evict(instanceOcid)
	}
	if p.vnicAttachCache != nil {
		p.vnicAttachCache.Evict(instanceOcid)
	}
	if p.bootVolAttachCache != nil {
		p.bootVolAttachCache.Evict(instanceOcid)
	}
}

// evictUnseenInstanceCaches removes all instance-scoped cache entries whose instance IDs were not
// observed during a complete ListInstances call.
func (p *DefaultProvider) evictUnseenInstanceCaches(seenInstanceIDs map[string]struct{}) {
	evictUnseen := func(instanceID string) bool {
		_, seen := seenInstanceIDs[instanceID]
		return !seen
	}
	if p.instanceCache != nil {
		p.instanceCache.EvictMatching(evictUnseen)
	}
	if p.vnicAttachCache != nil {
		p.vnicAttachCache.EvictMatching(evictUnseen)
	}
	if p.bootVolAttachCache != nil {
		p.bootVolAttachCache.EvictMatching(evictUnseen)
	}
}

func (p *DefaultProvider) DeleteInstance(ctx context.Context, instanceOcid string) error {
	_, err := p.computeClient.TerminateInstance(ctx, ocicore.TerminateInstanceRequest{
		InstanceId: &instanceOcid,
	})
	if err != nil {
		if oci.IsNotFound(err) {
			p.evictInstanceCaches(instanceOcid)
		}
		return err
	}

	deleteTimeOut := time.NewTimer(10 * time.Minute)
	defer deleteTimeOut.Stop()
	// wait for deletion finish.
	for {
		select {
		case <-ctx.Done():
			return errors.New("context cancelled")
		case <-deleteTimeOut.C:
			// in rare case, compute delete may not go thru, return error so controller would issue another delete
			return errors.New("delete timeout")
		case <-time.After(p.pollInterval):
			i, getErr := p.GetInstance(ctx, instanceOcid)
			if getErr != nil {
				return getErr
			}
			if i.Instance.LifecycleState == ocicore.InstanceLifecycleStateTerminated {
				return nil
			}
		}
	}
}

func IsInstanceTerminated(i *InstanceInfo) bool {
	return i == nil || i.LifecycleState == ocicore.InstanceLifecycleStateTerminated
}

func buildCreateVnicDetails(network *network.NetworkResolveResult,
	primaryVnicDetails *v1beta1.SimpleVnicConfig) *ocicore.CreateVnicDetails {
	c := &ocicore.CreateVnicDetails{
		SubnetId: network.PrimaryVnicSubnet.Subnet.Id,
	}

	if network.PrimaryVnicSubnet.AllocateIPv6 != nil {
		c.AssignIpv6Ip = network.PrimaryVnicSubnet.AllocateIPv6
	}

	if network.PrimaryVnicSubnet.NetworkSecurityGroups != nil {
		c.NsgIds = network.PrimaryVnicSubnet.NsgIds()
	}

	if primaryVnicDetails != nil {
		c.DisplayName = primaryVnicDetails.VnicDisplayName
		if primaryVnicDetails.AssignIpV6Ip != nil {
			c.AssignIpv6Ip = primaryVnicDetails.AssignIpV6Ip
		}
		c.AssignPublicIp = primaryVnicDetails.AssignPublicIp

		if primaryVnicDetails.SkipSourceDestCheck != nil {
			c.SkipSourceDestCheck = primaryVnicDetails.SkipSourceDestCheck
		} else {
			c.SkipSourceDestCheck = lo.ToPtr(true)
		}

		if len(primaryVnicDetails.SecurityAttributes) > 0 {
			c.SecurityAttributes = npn.MapValueStringToMapValueInterface(primaryVnicDetails.SecurityAttributes)
		}
		c.Ipv6AddressIpv6SubnetCidrPairDetails =
			npn.ToOciCoreIpvAddressCidrPair(primaryVnicDetails.Ipv6AddressIpv6SubnetCidrPairDetails)
	}

	return c
}

func buildShapeConfigFromInstanceType(it *instancetype.OciInstanceType) *ocicore.LaunchInstanceShapeConfigDetails {
	if !it.SupportShapeConfig {
		return nil
	}

	l := &ocicore.LaunchInstanceShapeConfigDetails{
		Ocpus:       it.Ocpu,
		MemoryInGBs: it.MemoryInGbs,
	}

	if it.BaselineOcpuUtilization != nil {
		l.BaselineOcpuUtilization = instancetype.ToLaunchInstanceCpuBaseline(*it.BaselineOcpuUtilization)
	}

	return l
}

func buildAgentConfig(agentPlugins []string) *ocicore.LaunchInstanceAgentConfigDetails {
	if len(agentPlugins) == 0 {
		return nil
	}
	return &ocicore.LaunchInstanceAgentConfigDetails{
		PluginsConfig: lo.Map(agentPlugins, func(name string, _ int) ocicore.InstanceAgentPluginConfigDetails {
			return ocicore.InstanceAgentPluginConfigDetails{
				Name:         lo.ToPtr(name),
				DesiredState: ocicore.InstanceAgentPluginConfigDetailsDesiredStateEnabled,
			}
		}),
	}
}

func BuildLaunchOptions(slo *v1beta1.LaunchOptions) *ocicore.LaunchOptions {
	if slo == nil {
		return nil
	}

	var launchOptions ocicore.LaunchOptions
	if slo.Firmware != nil {
		launchOptions.Firmware = ocicore.LaunchOptionsFirmwareEnum(*slo.Firmware)
	}

	if slo.RemoteDataVolumeType != nil {
		launchOptions.RemoteDataVolumeType =
			ocicore.LaunchOptionsRemoteDataVolumeTypeEnum(*slo.RemoteDataVolumeType)
	}

	if slo.BootVolumeType != nil {
		launchOptions.BootVolumeType = ocicore.LaunchOptionsBootVolumeTypeEnum(*slo.BootVolumeType)
	}

	launchOptions.IsConsistentVolumeNamingEnabled = slo.ConsistentVolumeNamingEnabled

	if slo.NetworkType != nil {
		launchOptions.NetworkType = ocicore.LaunchOptionsNetworkTypeEnum(*slo.NetworkType)
	}

	return &launchOptions
}

func decideCapacityType(ctx context.Context, nodeClaim *corev1.NodeClaim,
	instanceType *instancetype.OciInstanceType) string {
	requirements := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...)
	// Preemptible capacity does not support bare metal instances, burstable instances
	// https://docs.oracle.com/en-us/iaas/Content/Compute/Concepts/preemptible.htm
	if requirements.Get(corev1.CapacityTypeLabelKey).Has(corev1.CapacityTypeSpot) &&
		!instancetype.IsBurstableShape(instanceType) {
		requirements[corev1.CapacityTypeLabelKey] = scheduling.NewRequirement(corev1.CapacityTypeLabelKey,
			v1.NodeSelectorOpIn, corev1.CapacityTypeSpot)
		offeringsChecked := 0
		for _, offering := range instanceType.Offerings.Available() {
			err := requirements.Compatible(offering.Requirements, scheduling.AllowUndefinedWellKnownLabels)
			offeringsChecked++
			if err == nil {
				log.FromContext(ctx).Info("capacity type decision",
					"operation", "decide_capacity_type", "outcome", "spot",
					"reason", "compatible_offering_found")
				return corev1.CapacityTypeSpot
			}
		}
		log.FromContext(ctx).Info("capacity type decision",
			"operation", "decide_capacity_type", "outcome", "on_demand",
			"reason", "no_compatible_spot",
			"offerings_checked", offeringsChecked,
			"burstable", instancetype.IsBurstableShape(instanceType))
	}
	return corev1.CapacityTypeOnDemand
}

func getCapacityTypeFromInstance(instance *ocicore.Instance) string {
	if instance.PreemptibleInstanceConfig != nil && instance.PreemptibleInstanceConfig.PreemptionAction != nil {
		return corev1.CapacityTypeSpot
	}

	if instance.CapacityReservationId != nil {
		return corev1.CapacityTypeReserved
	}

	return corev1.CapacityTypeOnDemand
}

func GetNodePoolNameFromInstance(instance *ocicore.Instance) (string, bool) {
	if instance.FreeformTags != nil {
		v, ok := instance.FreeformTags[NodePoolOciFreeFormTagKey]
		if ok {
			return v, true
		}
	}

	return "", false
}

func GetNodePoolUIDFromInstance(instance *ocicore.Instance) (string, bool) {
	if instance.FreeformTags != nil {
		v, ok := instance.FreeformTags[NodePoolUIDOciFreeFormTagKey]
		if ok {
			return v, true
		}
	}

	return "", false
}

func GetNodeClassHashFromInstance(instance *ocicore.Instance) (string, bool) {
	if instance.FreeformTags != nil {
		v, ok := instance.FreeformTags[NodeClassHashOciFreeFormTagKey]
		if ok {
			return v, true
		}
	}

	return "", false
}

func buildFreeFormTags(nodeClass *v1beta1.OCINodeClass, nodeClaim *corev1.NodeClaim) (map[string]string, error) {
	freeFormTags := make(map[string]string)
	if nodeClass.Spec.FreeformTags != nil {
		freeFormTags = lo.Assign(freeFormTags, nodeClass.Spec.FreeformTags)
	}

	// always set a karpenter freeform tags -
	val, ok := nodeClaim.Labels[corev1.NodePoolLabelKey]
	if ok {
		freeFormTags[NodePoolOciFreeFormTagKey] = val
	}

	val, ok = nodeClaim.Labels[v1beta1.NodeClass]
	if ok {
		freeFormTags[NodeClassOciFreeFormTagKey] = val
	}

	// always set a node class hash
	val, ok = nodeClass.Labels[v1beta1.NodeClassHash]
	if ok {
		freeFormTags[NodeClassHashOciFreeFormTagKey] = val
	}

	nodePoolUID, ok := nodePoolUIDFromNodeClaimOwnerReference(nodeClaim)
	if !ok {
		return nil, fmt.Errorf("nodeclaim %s is missing NodePool owner reference UID", nodeClaim.Name)
	}
	if nodePoolUID != "" {
		freeFormTags[NodePoolUIDOciFreeFormTagKey] = nodePoolUID
	}

	return freeFormTags, nil
}

func nodePoolUIDFromNodeClaimOwnerReference(nodeClaim *corev1.NodeClaim) (string, bool) {
	nodePoolGVK := object.GVK(&corev1.NodePool{})
	owner, ok := lo.Find(nodeClaim.GetOwnerReferences(), func(owner metav1.OwnerReference) bool {
		if owner.APIVersion == nodePoolGVK.GroupVersion().String() &&
			owner.Kind == nodePoolGVK.Kind &&
			owner.UID != "" {
			return true
		}

		return false
	})
	return string(owner.UID), ok
}

func DecorateNodeClaimByInstance(nodeClaim *corev1.NodeClaim, i *ocicore.Instance) {
	// this is not important as karpenter doesn't consume this node claim by name so customer can change it
	nodeClaim.Name = *i.DisplayName
	nodeClaim.CreationTimestamp = metav1.NewTime(i.TimeCreated.Time)

	labels := nodeClaim.Labels

	labels[v1.LabelTopologyZone] = strings.Split(*i.AvailabilityDomain, ":")[1]
	labels[corev1.CapacityTypeLabelKey] = getCapacityTypeFromInstance(i)
	labels[v1beta1.OciFaultDomain] = *i.FaultDomain
	nodePoolName, ok := GetNodePoolNameFromInstance(i)
	if ok {
		// a missing node pool tag will lead to instance being terminated by karpenter later
		labels[corev1.NodePoolLabelKey] = nodePoolName
	}

	nodeClassHash, ok := GetNodeClassHashFromInstance(i)
	if ok {
		// a missing node class hash will lead to i drifted and then terminated
		labels[v1beta1.NodeClassHash] = nodeClassHash
	}

	nodeClaim.CreationTimestamp = metav1.Time{Time: i.TimeCreated.Time}
	// Set the deletionTimestamp to be the current time if the i is currently terminating
	if i.LifecycleState == ocicore.InstanceLifecycleStateTerminating ||
		i.LifecycleState == ocicore.InstanceLifecycleStateTerminated {
		nodeClaim.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	}

	nodeClaim.Status.ProviderID = *i.Id
	nodeClaim.Status.ImageID = *i.SourceDetails.(ocicore.InstanceSourceViaImageDetails).ImageId
}
