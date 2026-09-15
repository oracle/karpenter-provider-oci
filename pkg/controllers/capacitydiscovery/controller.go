/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package capacitydiscovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/awslabs/operatorpkg/reasonable"
	discovery "github.com/oracle/karpenter-provider-oci/pkg/providers/capacitydiscovery"
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
	capacityProvider discovery.Recorder
}

func NewController(kubeClient client.Client, cloudProvider cloudprovider.CloudProvider,
	capacityProvider discovery.Recorder) *Controller {
	return &Controller{
		kubeClient:       kubeClient,
		cloudProvider:    cloudProvider,
		capacityProvider: capacityProvider,
	}
}

// Name is used for both the controller-runtime registration and the logging context, so the two
// cannot drift apart.
func (c *Controller) Name() string {
	return "capacitydiscovery"
}

func (c *Controller) Reconcile(ctx context.Context, node *v1.Node) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, c.Name())

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

	// The measurement is keyed by the Node's instance-type label and the NodeClaim's ImageID, so
	// the OCINodeClass is not consulted. Not reading it saves an API call for every registered
	// node, and stops a NodeClass that has since been deleted or renamed from discarding an
	// otherwise valid observation.
	if err := c.capacityProvider.RecordNodeCapacity(ctx, node, nodeClaim); err != nil {
		if errors.Is(err, discovery.ErrCapacityNotReported) {
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
		Named(c.Name()).
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
