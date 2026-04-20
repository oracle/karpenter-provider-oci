/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package main

import (
	"log"

	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/cloudprovider"
	"github.com/oracle/karpenter-provider-oci/pkg/controllers"
	npnv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/npn/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/operator"
	"github.com/oracle/karpenter-provider-oci/pkg/operator/options"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/metrics"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/overlay"
	corecontrollers "sigs.k8s.io/karpenter/pkg/controllers"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	coreoperator "sigs.k8s.io/karpenter/pkg/operator"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(clientgoscheme.Scheme))
	utilruntime.Must(ociv1beta1.AddToScheme(clientgoscheme.Scheme))
	utilruntime.Must(npnv1beta1.AddToScheme(clientgoscheme.Scheme))
}

// nolint:gocyclo
func main() {
	ctx, op := operator.NewOperator(coreoperator.NewOperator())

	ociOptions := options.FromContext(ctx)
	if len(ociOptions.RepairPolicies) > 0 && !coreoptions.FromContext(ctx).FeatureGates.NodeRepair {
		log.Fatal("nodeRepair featureGate must be enabled when repair policies are specified")
	}

	ociCloudProvider, err := cloudprovider.New(
		ctx,
		op.GetClient(),
		op.InstanceTypeProvider,
		op.ImageProvider,
		op.NetworkProvider,
		op.KmsKeyProvider,
		op.InstanceProvider,
		op.PlacementProvider,
		op.CapacityReservationProvider,
		op.BlockStorageProvider,
		op.NpnProvider,
		ociOptions.RepairPolicies,
		op.Elected(),
	)

	if err != nil {
		log.Fatal(err)
	}

	overlayUndecoratedCloudProvider := metrics.Decorate(ociCloudProvider)
	cloudProvider := overlay.Decorate(overlayUndecoratedCloudProvider, op.GetClient(), op.InstanceTypeStore)
	clusterState := state.NewCluster(op.Clock, op.GetClient(), cloudProvider)

	op.WithControllers(ctx, corecontrollers.NewControllers(
		ctx,
		op.Manager,
		op.Clock,
		op.GetClient(),
		op.EventRecorder,
		cloudProvider,
		overlayUndecoratedCloudProvider,
		clusterState,
		op.InstanceTypeStore,
	)...).
		WithControllers(ctx, controllers.NewControllers(
			ctx,
			op.Manager,
			op.Clock,
			op.GetClient(),
			op.ClientSet,
			op.EventRecorder,
			op.ImageProvider,
			op.KmsKeyProvider,
			op.NetworkProvider,
			op.CapacityReservationProvider,
			op.ComputeClusterProvider,
			op.IdentityProvider,
			op.ClusterPlacementGroupProvider,
			cloudProvider,
		)...).
		Start(ctx)
}
