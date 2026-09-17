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
	"sigs.k8s.io/controller-runtime/pkg/log"
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

// nodeClaimNotFoundRequeue is how long to wait before looking again for a registered Node's
// NodeClaim. Short, because the gap it covers is the moment between two writes.
const nodeClaimNotFoundRequeue = 5 * time.Second

// nodeClaimLookupWindow bounds that retrying, measured from the first time the NodeClaim was
// missing. Past it the absence is not a race - a NodeClaim deleted while its Node lingers, say -
// and retrying would go on forever for a Node with nothing to teach.
//
// It is deliberately measured from the miss rather than from the Node's creation: a Node may
// register long after it is created, and the race is at registration, so a creation-anchored
// window would decline to retry in exactly the case this exists for.
const nodeClaimLookupWindow = 5 * time.Minute

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

	// nodeClaimMisses bounds how long a Node whose NodeClaim has not appeared is retried.
	nodeClaimMisses *missTracker
}

func NewController(kubeClient client.Client, cloudProvider cloudprovider.CloudProvider,
	capacityProvider discovery.Recorder) *Controller {
	return &Controller{
		kubeClient:       kubeClient,
		cloudProvider:    cloudProvider,
		capacityProvider: capacityProvider,
		nodeClaimMisses:  newMissTracker(nodeClaimLookupWindow),
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
		if nodeutils.IsNodeClaimNotFoundError(err) {
			return c.requeueForMissingNodeClaim(ctx, node), nil
		}
		// Several NodeClaims for one provider ID is not something we can attribute a measurement
		// to, and retrying will not resolve it.
		return reconcile.Result{}, fmt.Errorf("getting nodeclaim for node, %w", err)
	}

	c.nodeClaimMisses.forget(node.UID)

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

// requeueForMissingNodeClaim decides what to do when a registered Node has no NodeClaim yet.
//
// The pairing is by provider ID, and the two sides are not written atomically: a Node can be
// marked registered before its NodeClaim's status.providerID is persisted, and a Node can itself
// be without spec.providerID for a moment. Landing in either gap makes a perfectly healthy pair
// look unmatched.
//
// Nothing would bring us back on its own. The registered transition has already fired, so the
// update predicate filters every later change to this Node, and NodeClaims are not watched at all
// - so giving up here loses the measurement for the life of the node. Retry instead, briefly.
func (c *Controller) requeueForMissingNodeClaim(ctx context.Context, node *v1.Node) reconcile.Result {
	now := time.Now()
	first := c.nodeClaimMisses.firstMiss(node.UID, now)

	if waited := now.Sub(first); waited >= nodeClaimLookupWindow {
		log.FromContext(ctx).V(1).Info("no nodeclaim for registered node; not recording capacity",
			"node", node.Name, "waited", waited)

		return reconcile.Result{}
	}

	return reconcile.Result{RequeueAfter: nodeClaimNotFoundRequeue}
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
		// A Node can be marked registered before its NodeClaim's providerID is persisted, and the
		// two are paired by that field. Watching NodeClaims means the moment the pairing becomes
		// visible we are asked again, rather than waiting out a timer for it.
		Watches(&corev1.NodeClaim{}, nodeutils.NodeClaimEventHandler(m.GetClient())).
		WithOptions(controller.Options{
			RateLimiter:             reasonable.RateLimiter(),
			MaxConcurrentReconciles: 1,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}
