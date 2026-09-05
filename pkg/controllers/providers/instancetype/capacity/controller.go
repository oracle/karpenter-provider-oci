/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package capacity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/awslabs/operatorpkg/reasonable"
	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancetype"
	v1 "k8s.io/api/core/v1"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	nodeutils "sigs.k8s.io/karpenter/pkg/utils/node"
)

// capacityNotReportedRequeue is how long to wait before re-reading a node that registered before
// publishing its memory capacity.
const capacityNotReportedRequeue = 15 * time.Second

// CapacityProvider records the memory a registered node actually reported.
//
// Declared here rather than reused from the instancetype package so this controller depends only
// on the one operation it performs.
type CapacityProvider interface {
	UpdateInstanceTypeCapacityFromNode(ctx context.Context, node *v1.Node,
		nodeClaim *corev1.NodeClaim, nodeClass *ociv1beta1.OCINodeClass) error
}

// Controller feeds the memory capacity of registered nodes back into the instance type model.
//
// Karpenter has to size a node before it exists, so it works from an estimate. Nothing otherwise
// compares that estimate against the node it produced, which is why an estimate that is too high
// does not cause one bad launch but an unbounded run of them: the pod never fits, stays pending,
// and the identical decision is taken again. Observing registered nodes bounds that to the
// launches made before the first measurement lands.
type Controller struct {
	kubeClient       client.Client
	cloudProvider    cloudprovider.CloudProvider
	capacityProvider CapacityProvider
}

func NewController(kubeClient client.Client, cloudProvider cloudprovider.CloudProvider,
	capacityProvider CapacityProvider) *Controller {
	return &Controller{
		kubeClient:       kubeClient,
		cloudProvider:    cloudProvider,
		capacityProvider: capacityProvider,
	}
}

func (c *Controller) Reconcile(ctx context.Context, node *v1.Node) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, "providers.instancetype.capacity")

	if !nodeutils.IsManaged(node, c.cloudProvider) {
		return reconcile.Result{}, nil
	}

	nodeClaim, err := nodeutils.NodeClaimForNode(ctx, c.kubeClient, node)
	if err != nil {
		// A node without a NodeClaim (or with several) is not something we can attribute to an
		// instance type and image, so there is nothing to learn from it.
		return reconcile.Result{}, nodeutils.IgnoreNodeClaimNotFoundError(
			fmt.Errorf("getting nodeclaim for node, %w", err))
	}

	if nodeClaim.Spec.NodeClassRef == nil {
		return reconcile.Result{}, nil
	}

	nodeClass := &ociv1beta1.OCINodeClass{}
	if err := c.kubeClient.Get(ctx, client.ObjectKey{Name: nodeClaim.Spec.NodeClassRef.Name}, nodeClass); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(fmt.Errorf("getting ocinodeclass, %w", err))
	}

	if err := c.capacityProvider.UpdateInstanceTypeCapacityFromNode(ctx, node, nodeClaim, nodeClass); err != nil {
		if errors.Is(err, instancetype.ErrCapacityNotReported) {
			// The node registered before publishing its memory. We only watch the transition into
			// the registered state, so without an explicit requeue this node would never be
			// revisited and its measurement would be lost.
			return reconcile.Result{RequeueAfter: capacityNotReportedRequeue}, nil
		}
		return reconcile.Result{}, fmt.Errorf("updating discovered capacity, %w", err)
	}

	return reconcile.Result{}, nil
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("providers.instancetype.capacity").
		For(&v1.Node{}, builder.WithPredicates(predicate.TypedFuncs[client.Object]{
			// A node only reports its real capacity once it registers, so reconciling before then
			// would read nothing and take the cache lock for no reason.
			UpdateFunc: func(e event.TypedUpdateEvent[client.Object]) bool {
				if e.ObjectOld.GetLabels()[corev1.NodeRegisteredLabelKey] != "" {
					return false
				}
				return e.ObjectNew.GetLabels()[corev1.NodeRegisteredLabelKey] == "true"
			},
			// Already-registered nodes present at startup repopulate the cache, which is otherwise
			// empty after a restart and would let the estimate govern launches again.
			CreateFunc: func(e event.TypedCreateEvent[client.Object]) bool {
				return e.Object.GetLabels()[corev1.NodeRegisteredLabelKey] == "true"
			},
			DeleteFunc:  func(_ event.TypedDeleteEvent[client.Object]) bool { return false },
			GenericFunc: func(_ event.TypedGenericEvent[client.Object]) bool { return false },
		}, nodeutils.IsManagedPredicateFuncs(c.cloudProvider))).
		WithOptions(controller.Options{
			RateLimiter:             reasonable.RateLimiter(),
			MaxConcurrentReconciles: 1,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}
