/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package nodeclasses

import (
	"context"
	"fmt"
	"time"

	"github.com/awslabs/operatorpkg/object"
	"github.com/awslabs/operatorpkg/reasonable"
	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/operator/options"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/capacityreservation"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/clusterplacementgroup"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/computecluster"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/identity"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/image"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/kms"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	"github.com/samber/lo"
	"go.uber.org/multierr"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/events"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	"sigs.k8s.io/karpenter/pkg/utils/result"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// OCINodeClassReconciler reconciles a OCINodeClass object
type Controller struct {
	client.Client
	recorder events.Recorder
	Scheme   *runtime.Scheme

	reconcilers []nodeClassReconciler
}

func NewController(ctx context.Context,
	kubeClient client.Client,
	recorder events.Recorder,
	imgProvider image.Provider,
	kmsKeyProvider kms.Provider,
	networkProvider network.Provider,
	capacityReservationProvider capacityreservation.Provider,
	computeClusterProvider computecluster.Provider,
	identityProvider identity.Provider,
	clusterPlacementGroupProvider clusterplacementgroup.Provider) (*Controller, error) {

	clusterCompartmentId := options.FromContext(ctx).ClusterCompartmentId
	nodeCompartmentReconciler := NodeCompartmentReconciler{identityProvider: identityProvider,
		clusterCompartmentId: clusterCompartmentId}

	c := &Controller{
		Client:   kubeClient,
		recorder: recorder,
		reconcilers: []nodeClassReconciler{
			&InitStatus{},
			// keep compartment at the top to resolve node compartment, this is dependency for some other optional
			// placement impacted configs such as capacity reservation and compute clusters.
			&nodeCompartmentReconciler,
			&ImageReconciler{imageProvider: imgProvider},
			&KmsKeyReconciler{kmsKeyProvider: kmsKeyProvider},
			&NetworkReconciler{networkProvider: networkProvider},
			&CapacityReservationReconciler{capacityReservationProvider: capacityReservationProvider},
			&ClusterPlacementGroupReconciler{clusterPlacementGroupProvider: clusterPlacementGroupProvider},
			&ComputeClusterReconciler{computeClusterProvider: computeClusterProvider},
			&Hash{}, // keep this guy as last one
		},
	}

	return c, nil
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the OCINodeClass object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/reconcile
func (c *Controller) Reconcile(ctx context.Context, nodeClass *v1beta1.OCINodeClass) (ctrl.Result, error) {
	ctx = injection.WithControllerName(ctx, "OCINodeClassReconciler")

	log := log.FromContext(ctx)

	log.Info("reconciling ocinodeclass")

	if !nodeClass.GetDeletionTimestamp().IsZero() {
		return c.finalize(ctx, nodeClass)
	}

	if !controllerutil.ContainsFinalizer(nodeClass, corev1.TerminationFinalizer) {
		stored := nodeClass.DeepCopy()
		controllerutil.AddFinalizer(nodeClass, corev1.TerminationFinalizer)

		if err := c.Client.Patch(ctx, nodeClass,
			client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})); err != nil {
			if errors.IsConflict(err) {
				return reconcile.Result{Requeue: true}, nil
			}

			return reconcile.Result{}, err
		}
	}

	stored := nodeClass.DeepCopy()

	// be noticed "Ready" condition is re-compute after each condition set call.
	results := make([]reconcile.Result, 0)
	var errs error
	for _, reconciler := range c.reconcilers {
		res, err := reconciler.Reconcile(ctx, nodeClass)
		errs = multierr.Append(errs, err)
		results = append(results, res)
	}

	log.Info(fmt.Sprintf("post reconcile: %v", nodeClass))

	if !equality.Semantic.DeepEqual(stored, nodeClass) {
		newHash, foundNewHash := nodeClass.Annotations[v1beta1.NodeClassHash]
		existingHash, foundExistingHash := stored.Annotations[v1beta1.NodeClassHash]

		if (!foundExistingHash && foundNewHash) || (foundExistingHash && foundNewHash && newHash != existingHash) {
			annotationPatchOnly := nodeClass.DeepCopy()
			if err := c.Client.Patch(ctx, annotationPatchOnly,
				client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})); err != nil {
				if errors.IsConflict(err) {
					return reconcile.Result{Requeue: true}, nil
				}
				errs = multierr.Append(errs, client.IgnoreNotFound(err))
			}

			nodeClass.ResourceVersion = annotationPatchOnly.ResourceVersion
			stored = annotationPatchOnly
		}

		// We use client.MergeFromWithOptimisticLock because patching a list with a JSON merge patch
		// can cause races due to the fact that it fully replaces the list on a change
		// Here, we are updating the status condition list
		if err := c.Client.Status().Patch(ctx, nodeClass,
			client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})); err != nil {
			if errors.IsConflict(err) {
				return reconcile.Result{Requeue: true}, nil
			}
			errs = multierr.Append(errs, client.IgnoreNotFound(err))
		}
	} else {
		log.Info("no change to resource")
	}

	if errs != nil {
		return reconcile.Result{}, errs
	}
	return result.Min(results...), nil
}

func (c *Controller) Name() string {
	return "ocinodeclass"
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named(c.Name()).
		For(&v1beta1.OCINodeClass{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Watches(
			&corev1.NodeClaim{},
			handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
				nc := o.(*corev1.NodeClaim)
				if nc.Spec.NodeClassRef == nil {
					return nil
				}
				return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: nc.Spec.NodeClassRef.Name}}}
			}),
			// Watch for NodeClaim deletion events
			builder.WithPredicates(predicate.Funcs{
				CreateFunc: func(e event.CreateEvent) bool { return false },
				UpdateFunc: func(e event.UpdateEvent) bool { return false },
				DeleteFunc: func(e event.DeleteEvent) bool { return true },
			}),
		).
		WithOptions(controller.Options{
			RateLimiter:             reasonable.RateLimiter(),
			MaxConcurrentReconciles: 10,
		}).
		Complete(reconcile.AsReconciler[*v1beta1.OCINodeClass](m.GetClient(), c))
}

func (c *Controller) finalize(ctx context.Context, nodeClass *v1beta1.OCINodeClass) (ctrl.Result, error) {
	// ensure all node claims are deleted before deleting node classes.
	stored := nodeClass.DeepCopy()
	if !controllerutil.ContainsFinalizer(nodeClass, corev1.TerminationFinalizer) {
		return reconcile.Result{}, nil
	}

	nodeClaims := &corev1.NodeClaimList{}
	listOptions := client.MatchingFields{
		"spec.nodeClassRef.group": object.GVK(nodeClass).Group,
		"spec.nodeClassRef.kind":  object.GVK(nodeClass).Kind,
		"spec.nodeClassRef.name":  nodeClass.GetName(),
	}

	if err := c.Client.List(ctx, nodeClaims, listOptions); err != nil {
		return reconcile.Result{}, fmt.Errorf("listing nodeclaims that are using nodeclass, %w", err)
	}

	if len(nodeClaims.Items) > 0 {
		c.recorder.Publish(PendingNodeClaimTerminationEvent(nodeClass,
			lo.Map(nodeClaims.Items, func(nc corev1.NodeClaim, _ int) string { return nc.Name })))
		return reconcile.Result{RequeueAfter: time.Minute * 10}, nil // periodically fire the event
	}

	// TODO: do any oci related clean-up

	controllerutil.RemoveFinalizer(nodeClass, corev1.TerminationFinalizer)
	if !equality.Semantic.DeepEqual(stored, nodeClass) {
		if err := c.Client.Patch(ctx, nodeClass,
			client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})); err != nil {
			if errors.IsConflict(err) {
				return reconcile.Result{Requeue: true}, nil
			}
			return reconcile.Result{}, client.IgnoreNotFound(fmt.Errorf("removing termination finalizer, %w", err))
		}
	}
	return reconcile.Result{}, nil
}
