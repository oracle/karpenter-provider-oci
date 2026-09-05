/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package controllers

import (
	"context"

	"github.com/awslabs/operatorpkg/controller"
	"github.com/oracle/karpenter-provider-oci/pkg/controllers/nodeclasses"
	"github.com/oracle/karpenter-provider-oci/pkg/controllers/orphaninstance"
	"github.com/oracle/karpenter-provider-oci/pkg/controllers/providers/instancetype/capacity"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/capacityreservation"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/clusterplacementgroup"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/computecluster"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/identity"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/image"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/kms"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	"github.com/samber/lo"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"
)

func NewControllers(
	ctx context.Context,
	mgr manager.Manager,
	clk clock.Clock,
	kubeClient client.Client,
	clientSet kubernetes.Interface,
	recorder events.Recorder,
	imageProvider image.Provider,
	kmsKeyProvider kms.Provider,
	networkProvider network.Provider,
	capacityReservationProvider capacityreservation.Provider,
	computeClusterProvider computecluster.Provider,
	compartmentProvider identity.Provider,
	clusterPlacementGroupProvider clusterplacementgroup.Provider,
	cloudProvider cloudprovider.CloudProvider,
	capacityProvider capacity.CapacityProvider,
) []controller.Controller {
	var controllers []controller.Controller

	nodeClassController := lo.Must(nodeclasses.NewController(ctx, kubeClient, recorder, imageProvider,
		kmsKeyProvider, networkProvider, capacityReservationProvider,
		computeClusterProvider, compartmentProvider, clusterPlacementGroupProvider))

	orphanInstanceController := orphaninstance.NewController(ctx, kubeClient, clientSet, cloudProvider)

	// Feeds memory observed on registered nodes back into the instance type model, so a launch
	// that turns out too small corrects the next one instead of repeating indefinitely.
	capacityController := capacity.NewController(kubeClient, cloudProvider, capacityProvider)

	controllers = append(controllers, nodeClassController, orphanInstanceController, capacityController)

	return controllers
}
