/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package operator

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/user"
	"time"

	"github.com/oracle/karpenter-provider-oci/pkg/oci"
	"github.com/oracle/karpenter-provider-oci/pkg/operator/options"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/blockstorage"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/capacityreservation"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/clusterplacementgroup"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/computecluster"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/identity"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/image"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instance"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancemeta"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancetype"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/kms"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/npn"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/placement"
	"github.com/oracle/karpenter-provider-oci/pkg/utils"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/common/auth"
	"github.com/samber/lo"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/karpenter/pkg/operator"
)

// Version is the karpenter app version injected during compilation
// when using the Makefile
var Version = "unspecified"

const (
	DefaultShapeMetafileCluster = "/etc/karpenter/meta/ociShapeMeta"
	DefaultShapeMetafileLocal   = "chart/config/oci-shape-meta.json"
)

type Operator struct {
	*operator.Operator

	InstanceProvider              instance.Provider
	InstanceTypeProvider          instancetype.Provider
	PlacementProvider             placement.Provider
	NetworkProvider               network.Provider
	CapacityReservationProvider   capacityreservation.Provider
	ClusterPlacementGroupProvider clusterplacementgroup.Provider
	ComputeClusterProvider        computecluster.Provider
	ImageProvider                 image.Provider
	KmsKeyProvider                kms.Provider
	IdentityProvider              identity.Provider
	BlockStorageProvider          blockstorage.Provider
	NpnProvider                   npn.Provider

	// expose a clientSet for subresource operation
	ClientSet kubernetes.Interface
}

func NewOperator(ctx context.Context, coreOp *operator.Operator) (context.Context, *Operator) {
	ociOptions := options.FromContext(ctx)
	log.Printf("option address: %p, ctx address: %p", ociOptions, ctx)
	log.Printf("option in newOperator: %p, %v", ociOptions, ociOptions)
	lo.Must0(ociOptions.Validate())

	restConfig := config.GetConfigOrDie()
	clientSet := kubernetes.NewForConfigOrDie(restConfig)

	region := lo.Must(utils.GetRegion())

	// enable SDK to look up the IMDS endpoint to figure out the right realmDomain
	common.EnableInstanceMetadataServiceLookup()

	authConfigProvider := &DefaultAuthConfigProvider{}
	return createOperator(ctx, coreOp, ociOptions, region, nil, clientSet, restConfig, authConfigProvider)
}

func createOperator(ctx context.Context, coreOp *operator.Operator,
	ociOptions *options.Options, region string,
	inputOciClient *oci.Client, clientSet kubernetes.Interface, restConfig *rest.Config,
	provider AuthConfigProvider) (context.Context, *Operator) {

	var configProvider common.ConfigurationProvider
	shapeMetaFile := ociOptions.ShapeMetaFile
	authMethod := ociOptions.OciAuthMethods

	switch authMethod {
	case options.AuthBySession:
		configProvider = provider.GetCustomProfileSessionTokenConfigProvider(ociOptions.OciProfileName)
		shapeMetaFile = valueOrDefault(shapeMetaFile, DefaultShapeMetafileLocal)
	case options.AuthByInstancePrincipal:
		configProvider = lo.Must(provider.GetInstancePrincipalConfigurationProvider())
	default:
		configProvider = lo.Must(provider.GetWorkloadIdentityConfigurationProvider(region))
	}

	shapeMetaFile = valueOrDefault(shapeMetaFile, DefaultShapeMetafileCluster)
	refreshInterval := time.Duration(ociOptions.ShapeMetaRefreshIntervalHours) * time.Hour
	rateLimiter := oci.NewRateLimiter(ctx, &oci.RateLimiterConfig{
		DisableRateLimiter:  ociOptions.DisableRateLimiter,
		RateLimitQPSRead:    float32(ociOptions.RateLimitQPSRead),
		RateLimitBurstRead:  ociOptions.RateLimitBurstRead,
		RateLimitQPSWrite:   float32(ociOptions.RateLimitQPSWrite),
		RateLimitBurstWrite: ociOptions.RateLimitBurstWrite,
	})

	ociClient := inputOciClient
	if ociClient == nil {
		ociClient = lo.Must(oci.NewClient(ctx, configProvider, &rateLimiter))
	}

	capacityReservationProvider := capacityreservation.NewProvider(ctx, ociClient,
		ociOptions.ClusterCompartmentId)

	clusterPlacementGroupProvider := clusterplacementgroup.NewProvider(ctx, ociClient,
		ociOptions.ClusterCompartmentId)
	computeClusterProvider := computecluster.NewProvider(ctx, ociClient, ociOptions.ClusterCompartmentId)

	identityProvider := lo.Must(identity.NewProvider(ctx, ociOptions.ClusterCompartmentId, ociClient))

	instanceTypeProvider := lo.Must(instancetype.New(ctx, region, ociOptions.ClusterCompartmentId,
		ociClient, identityProvider, clientSet, coreOp.GetAPIReader(),
		capacityReservationProvider, computeClusterProvider, clusterPlacementGroupProvider,
		shapeMetaFile, refreshInterval, ociOptions.GlobalShapeConfigs,
		ociOptions.IpFamiliesFlag.IpFamilies,
		coreOp.Elected()))

	networkProvider := lo.Must(network.NewProvider(ctx, ociOptions.VcnCompartmentId,
		ociOptions.OciVcnIpNative, ociOptions.IpFamiliesFlag.IpFamilies, ociClient))

	instanceMetadataProvider := lo.Must(instancemeta.NewProvider(ctx, ociOptions.ApiserverEndpoint,
		restConfig.TLSClientConfig.CAData, ociOptions.IpFamiliesFlag.IpFamilies))

	placementProvider := lo.Must(placement.NewProvider(ctx, capacityReservationProvider,
		computeClusterProvider, clusterPlacementGroupProvider, identityProvider))

	vmTimeout := time.Duration(ociOptions.InstanceLaunchTimeoutVMMins) * time.Minute
	bmTimeout := time.Duration(ociOptions.InstanceLaunchTimeoutBMMins) * time.Minute
	instancePollInterval := time.Duration(ociOptions.InstanceOperationPollIntervalInSeconds) * time.Second
	instanceProvider := lo.Must(instance.NewProvider(ctx, ociClient, ociClient,
		ociOptions.ClusterCompartmentId, instanceMetadataProvider, networkProvider,
		vmTimeout, bmTimeout, ociOptions.InstanceLaunchTimeOutFailOver, instancePollInterval))

	imageProvider := lo.Must(image.NewProvider(ctx, clientSet, ociClient,
		ociOptions.PreBakedImageCompartmentId, "", coreOp.Elected()))

	kmsKeyProvider := lo.Must(kms.NewProvider(ctx, ociOptions.ClusterCompartmentId, configProvider, &rateLimiter))

	blockStorageProvider := lo.Must(blockstorage.NewProvider(ctx, ociClient))

	npnProvider := lo.Must(npn.NewProvider(ctx, ociOptions.OciVcnIpNative, ociOptions.IpFamiliesFlag.IpFamilies))
	op := &Operator{
		Operator: coreOp,

		InstanceProvider:              instanceProvider,
		PlacementProvider:             placementProvider,
		InstanceTypeProvider:          instanceTypeProvider,
		ImageProvider:                 imageProvider,
		NetworkProvider:               networkProvider,
		KmsKeyProvider:                kmsKeyProvider,
		CapacityReservationProvider:   capacityReservationProvider,
		ClusterPlacementGroupProvider: clusterPlacementGroupProvider,
		ComputeClusterProvider:        computeClusterProvider,
		IdentityProvider:              identityProvider,
		BlockStorageProvider:          blockStorageProvider,
		NpnProvider:                   npnProvider,
		ClientSet:                     clientSet,
	}

	return ctx, op
}

func valueOrDefault(val string, def string) string {
	if val != "" {
		return val
	}
	return def
}

type AuthConfigProvider interface {
	GetCustomProfileSessionTokenConfigProvider(profileName string) common.ConfigurationProvider
	GetInstancePrincipalConfigurationProvider() (common.ConfigurationProvider, error)
	GetWorkloadIdentityConfigurationProvider(region string) (common.ConfigurationProvider, error)
}

type DefaultAuthConfigProvider struct{}

func (p *DefaultAuthConfigProvider) GetCustomProfileSessionTokenConfigProvider(
	profileName string) common.ConfigurationProvider {
	u := lo.Must(user.Current())
	return common.CustomProfileSessionTokenConfigProvider(fmt.Sprintf("%s/.oci/config", u.HomeDir),
		profileName)
}

func (p *DefaultAuthConfigProvider) GetInstancePrincipalConfigurationProvider() (common.ConfigurationProvider, error) {
	return auth.InstancePrincipalConfigurationProvider()
}

func (p *DefaultAuthConfigProvider) GetWorkloadIdentityConfigurationProvider(
	region string) (common.ConfigurationProvider, error) {
	// workload identity only reads from the env
	lo.Must0(os.Setenv(auth.ResourcePrincipalRegionEnvVar, region))
	return auth.OkeWorkloadIdentityConfigurationProvider()
}
