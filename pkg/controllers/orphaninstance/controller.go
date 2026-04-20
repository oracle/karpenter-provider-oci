/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package orphaninstance

import (
	"context"
	"fmt"
	"time"

	"github.com/awslabs/operatorpkg/reconciler"
	"github.com/awslabs/operatorpkg/singleton"
	"github.com/samber/lo"
	"go.uber.org/multierr"
	v1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/workqueue"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	nodeutils "sigs.k8s.io/karpenter/pkg/utils/node"
	nodeclaimutils "sigs.k8s.io/karpenter/pkg/utils/nodeclaim"
	podutil "sigs.k8s.io/karpenter/pkg/utils/pod"
)

var slowRequeueAfter = 60 * time.Minute
var fastRequeueAfter = 5 * time.Minute

var creationTimeThreshold = 1 * time.Hour

type Controller struct {
	kubeClient          client.Client
	kubernetesInterface kubernetes.Interface
	cloudProvider       cloudprovider.CloudProvider
}

func NewController(context context.Context, kubeClient client.Client, clientSet kubernetes.Interface,
	cloudProvider cloudprovider.CloudProvider) *Controller {
	return &Controller{
		kubeClient:          kubeClient,
		kubernetesInterface: clientSet,
		cloudProvider:       cloudProvider,
	}
}

func (c *Controller) Reconcile(ctx context.Context) (reconciler.Result, error) {
	ctx = injection.WithControllerName(ctx, "orphaninstance")

	nodeClaims, err := nodeclaimutils.ListManaged(ctx, c.kubeClient, c.cloudProvider)
	if err != nil {
		return reconciler.Result{}, err
	}

	nameToNodeClaims := lo.SliceToMap(nodeClaims, func(item *corev1.NodeClaim) (string, *corev1.NodeClaim) {
		return item.Name, item
	})

	providerIdToClaims := lo.SliceToMap(
		lo.Filter(nodeClaims, func(item *corev1.NodeClaim, _ int) bool {
			return item.Status.ProviderID != ""
		}),
		func(item *corev1.NodeClaim) (string, *corev1.NodeClaim) {
			return item.Status.ProviderID, item
		})

	cloudProviderNodeClaims, err := c.cloudProvider.List(ctx)
	if err != nil {
		return reconciler.Result{}, err
	}

	cloudProviderNodeClaims = lo.Filter(cloudProviderNodeClaims, func(item *corev1.NodeClaim, _ int) bool {
		return item.Status.ProviderID != ""
	})

	var errs []error
	var fastRequeue bool
	workqueue.ParallelizeUntil(ctx, 5, len(cloudProviderNodeClaims), func(i int) {
		cpNodeClaim := cloudProviderNodeClaims[i]

		reclaim := false

		// a few cases can cause resource leakage:
		// 1) someone manually remove the finalizer of a node claim then delete it
		// 2) cloudProvider launched an instance but not associated with a node claim, e.g restart in the middle of
		// launch call.
		// check provider ID match first, if failed then check by name
		if _, ok := providerIdToClaims[cpNodeClaim.Status.ProviderID]; !ok {
			sameNameNodeClaim, exist := nameToNodeClaims[cpNodeClaim.Name]

			// case 1: there is no node claim with the same name
			// 1a: a stale node claim attempted to launch instance but never gone thru, eventually the node claim
			// deleted by karpenter
			// 1b: nodepools with same name but from different clusters that run instances in the same compartment.
			// case 2: the matched node claim is launched with a different providerId
			// 2a: reattempt to launch the same node claim
			// case 3: the node claim has been in creating state longer than a reasonable window
			// 3a: reattempt to launch the same node claim
			if !exist || sameNameNodeClaim.Status.ProviderID != "" ||
				time.Since(cpNodeClaim.CreationTimestamp.Time) > creationTimeThreshold {
				reclaim = true
			}
		}

		if reclaim {
			finished, reclaimErr := c.reclaim(ctx, cpNodeClaim)
			if reclaimErr != nil {
				errs = append(errs, reclaimErr)
			}

			fastRequeue = fastRequeue || !finished
		}
	})

	if err = multierr.Combine(errs...); err != nil {
		return reconciler.Result{}, err
	} else {
		requeueAfter := slowRequeueAfter
		if fastRequeue {
			requeueAfter = fastRequeueAfter
		}

		return reconciler.Result{RequeueAfter: requeueAfter}, nil
	}
}

func (c *Controller) Register(context context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("orphan.instance").
		WatchesRawSource(singleton.Source()).
		Complete(singleton.AsReconciler(c))
}

func (c *Controller) reclaim(ctx context.Context, i *corev1.NodeClaim) (bool, error) {
	node, err := nodeclaimutils.NodeForNodeClaim(ctx, c.kubeClient, i)

	if nodeclaimutils.IgnoreNodeNotFoundError(err) != nil {
		return false, err
	}

	if node != nil {
		// cordon + drain the node
		err = c.cordonNode(ctx, node)
		if err != nil {
			return false, err
		}

		drainFinished, drainErr := c.drainNode(ctx, node)
		if drainErr != nil {
			return false, drainErr
		}

		if !drainFinished {
			// return false to requeue reconciliation, hopefully by then drain is done
			return false, nil
		}
	}

	log.FromContext(ctx).Info("deleting orphaned instance", "nodeClaim", i.Name,
		"instanceOcid", i.Status.ProviderID)

	err = c.cloudProvider.Delete(ctx, i)
	return true, err
}

func (c *Controller) cordonNode(ctx context.Context, node *v1.Node) error {
	if !node.Spec.Unschedulable {
		stored := node.DeepCopy()
		node.Spec.Unschedulable = true
		return c.kubeClient.Patch(ctx, node, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{}))
	}

	return nil
}

func (c *Controller) drainNode(ctx context.Context, node *v1.Node) (bool, error) {
	pods, err := nodeutils.GetPods(ctx, c.kubeClient, node)
	if err != nil {
		return false, fmt.Errorf("listing pods on node, %w", err)
	}

	drainablePods := lo.Filter(pods, func(p *v1.Pod, _ int) bool {
		return !podutil.IsTerminal(p) && !podutil.IsOwnedByDaemonSet(p) && !IsMirrorPod(p)
	})

	var podsPendingForEviction int
	for _, pod := range drainablePods {
		if pod.DeletionTimestamp.IsZero() {
			eviction := &policyv1.Eviction{
				ObjectMeta: metav1.ObjectMeta{
					Name:      pod.Name,
					Namespace: pod.Namespace,
				},
			}

			err = c.kubernetesInterface.CoreV1().Pods(pod.Namespace).EvictV1(ctx, eviction)
			if err != nil {
				return false, err
			}
		}

		podsPendingForEviction++
	}

	if podsPendingForEviction > 0 {
		log.FromContext(ctx).Info("node still has pods pending for eviction", "node", node.Name,
			"instanceOcid", node.Spec.ProviderID, "podsPendingForEviction", podsPendingForEviction)
		return false, nil
	}

	return true, nil
}

func IsMirrorPod(p *v1.Pod) bool {
	if p.Annotations != nil {
		_, ok := p.Annotations["kubernetes.io/config.mirror"]
		return ok
	}

	return false
}
