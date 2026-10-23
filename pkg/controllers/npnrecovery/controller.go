/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package npnrecovery

import (
	"context"
	"fmt"
	"time"

	"github.com/awslabs/operatorpkg/reasonable"
	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	npnv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/npn/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instance"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/npn"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	nodeclaimutils "sigs.k8s.io/karpenter/pkg/utils/nodeclaim"
)

// gracePeriod is how long after launch a missing NPN is left alone. CloudProvider.Create writes the NPN
// before the NodeClaim is marked launched, so this only absorbs ordering and clock skew.
const gracePeriod = 1 * time.Minute

// recoveryTimeout is how long after launch the NPN may stay missing before the NodeClaim is deleted.
const recoveryTimeout = 15 * time.Minute

// Controller recreates the NativePodNetwork of a launched NodeClaim when CloudProvider.Create failed to
// create it. Without it the node registers but never initializes, and core karpenter reclaims neither.
// If the NPN still cannot be created within recoveryTimeout, the NodeClaim is deleted so karpenter
// terminates the instance and provisions a replacement.
//
// All state is read from the cluster on every reconcile, so recovery carries on across restarts.
type Controller struct {
	clk              clock.Clock
	kubeClient       client.Client
	apiReader        client.Reader
	cloudProvider    cloudprovider.CloudProvider
	instanceProvider instance.Provider
	networkProvider  network.Provider
	npnProvider      npn.Provider
}

// NewController takes an apiReader for NPN lookups so the controller needs no list/watch permission on
// NativePodNetworks and never acts on a stale cache.
func NewController(clk clock.Clock, kubeClient client.Client, apiReader client.Reader,
	cloudProvider cloudprovider.CloudProvider, instanceProvider instance.Provider,
	networkProvider network.Provider, npnProvider npn.Provider) *Controller {
	return &Controller{
		clk:              clk,
		kubeClient:       kubeClient,
		apiReader:        apiReader,
		cloudProvider:    cloudProvider,
		instanceProvider: instanceProvider,
		networkProvider:  networkProvider,
		npnProvider:      npnProvider,
	}
}

func (c *Controller) Name() string {
	return "npnrecovery"
}

func (c *Controller) Reconcile(ctx context.Context, nodeClaim *corev1.NodeClaim) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, c.Name())

	launched := nodeClaim.StatusConditions().Get(corev1.ConditionTypeLaunched)
	if !nodeClaim.DeletionTimestamp.IsZero() || nodeClaim.Status.ProviderID == "" || !launched.IsTrue() ||
		nodeClaim.StatusConditions().Get(corev1.ConditionTypeInitialized).IsTrue() {
		return reconcile.Result{}, nil
	}

	npnName := npn.NameForInstance(nodeClaim.Status.ProviderID)
	err := c.apiReader.Get(ctx, types.NamespacedName{Name: npnName}, &npnv1beta1.NativePodNetwork{})
	if err == nil {
		return reconcile.Result{}, nil
	}
	if !apierrors.IsNotFound(err) {
		return reconcile.Result{}, fmt.Errorf("getting NPN %s, %w", npnName, err)
	}

	sinceLaunch := c.clk.Since(launched.LastTransitionTime.Time)
	if sinceLaunch < gracePeriod {
		return reconcile.Result{RequeueAfter: gracePeriod - sinceLaunch}, nil
	}

	lg := log.FromContext(ctx).WithValues("npn", npnName, "providerID", nodeClaim.Status.ProviderID)
	if sinceLaunch >= recoveryTimeout {
		lg.Info("NPN still missing after recovery timeout, deleting nodeclaim", "timeout", recoveryTimeout)
		return reconcile.Result{}, client.IgnoreNotFound(c.kubeClient.Delete(ctx, nodeClaim))
	}

	lg.Info("NPN missing for launched nodeclaim, recreating it")
	if err := c.createNpn(ctx, nodeClaim); err != nil {
		// returning the error retries with the workqueue's exponential backoff, capped at a minute, so the
		// timeout above is still reached promptly.
		return reconcile.Result{}, fmt.Errorf("recreating NPN %s, %w", npnName, err)
	}
	return reconcile.Result{}, nil
}

func (c *Controller) createNpn(ctx context.Context, nodeClaim *corev1.NodeClaim) error {
	nodeClass := &ociv1beta1.OCINodeClass{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaim.Spec.NodeClassRef.Name},
		nodeClass); err != nil {
		return fmt.Errorf("getting nodeclass, %w", err)
	}

	inst, err := c.instanceProvider.GetInstanceCached(ctx, nodeClaim.Status.ProviderID)
	if err != nil {
		return fmt.Errorf("getting instance, %w", err)
	}

	networkResolveResult, err := c.networkProvider.ResolveNetworkConfig(ctx, nodeClass.Spec.NetworkConfig)
	if err != nil {
		return fmt.Errorf("resolving network config, %w", err)
	}

	npnObject, err := c.npnProvider.CreateNpnCustomObject(ctx, inst.Instance, nodeClass, networkResolveResult)
	if err != nil {
		return fmt.Errorf("constructing NPN, %w", err)
	}

	return client.IgnoreAlreadyExists(c.kubeClient.Create(ctx, npnObject))
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named(c.Name()).
		For(&corev1.NodeClaim{}, builder.WithPredicates(nodeclaimutils.IsManagedPredicateFuncs(c.cloudProvider))).
		WithOptions(controller.Options{
			RateLimiter:             reasonable.RateLimiter(),
			MaxConcurrentReconciles: 1,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}
