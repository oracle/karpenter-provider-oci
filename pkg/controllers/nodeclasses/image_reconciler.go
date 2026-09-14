/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package nodeclasses

import (
	"context"
	"time"

	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/image"
	"github.com/oracle/karpenter-provider-oci/pkg/utils"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type ImageReconciler struct {
	imageProvider image.Provider
	// refreshInterval, when > 0, requeues the nodeclass at that interval on successful reconcile so
	// the image list is re-resolved after the image-filter cache is evicted by the provider's
	// periodic refresh.
	refreshInterval time.Duration
}

func (i *ImageReconciler) Reconcile(ctx context.Context, nodeClass *v1beta1.OCINodeClass) (reconcile.Result, error) {
	imageResolveResult, err := i.imageProvider.ResolveImages(ctx,
		nodeClass.Spec.VolumeConfig.BootVolumeConfig.ImageConfig)

	return updateImageReconcileResult(ctx, nodeClass, imageResolveResult, err, i.refreshInterval)
}

func updateImageReconcileResult(ctx context.Context, class *v1beta1.OCINodeClass,
	imageResolveResult *image.ImageResolveResult, err error, refreshInterval time.Duration) (reconcile.Result, error) {
	if err == nil {
		log.FromContext(ctx).Info("image resolved")
		class.Status.Volume.ImageCandidates = lo.Map(imageResolveResult.Images,
			func(img *ocicore.Image, _ int) *v1beta1.Image {
				return &v1beta1.Image{
					ImageId:     *img.Id,
					DisplayName: *img.DisplayName,
				}
			})

		class.StatusConditions().SetTrue(v1beta1.ConditionTypeImageReady)

		return reconcile.Result{RequeueAfter: refreshInterval}, nil
	}

	log.FromContext(ctx).Error(err, "failed to resolve image")
	class.Status.Volume.ImageCandidates = nil
	class.StatusConditions().SetFalse(v1beta1.ConditionTypeImageReady,
		v1beta1.ConditionImageNotReadyReason, utils.PrettyString(err.Error(), 1024))

	// TODO - classify error and decide whether to retry, we don't return error so as not to
	// disturb other reconciliation.
	return reconcile.Result{
		RequeueAfter: 5 * time.Minute,
	}, nil
}
