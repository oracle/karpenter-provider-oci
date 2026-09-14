/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package image

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/coreos/go-semver/semver"
	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/cache"
	"github.com/oracle/karpenter-provider-oci/pkg/oci"
	"github.com/oracle/karpenter-provider-oci/pkg/utils"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/set"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Provider interface {
	ResolveImages(ctx context.Context, imageCfg *v1beta1.ImageConfig) (*ImageResolveResult, error)

	ResolveImageForShape(ctx context.Context,
		imageCfg *v1beta1.ImageConfig, shape string) (*ImageResolveResult, error)
}

// DefaultProvider - provide image, cache get result based on ocid or filter, with assumption
// ocid based images are prone to change, verse filter based change less often.
// this provider does image & shape compatibility validation using direct information, not from base image.
type DefaultProvider struct {
	computeClient oci.ComputeClient

	kubernetesInterface kubernetes.Interface

	// use a ttl cache to cache customer input but also expire in need
	imageOcidCache *cache.GetOrLoadCache[*ocicore.Image]
	// this is kept as a list, other than a single image to provide possible shape selection
	imageFilterCache *cache.GetOrLoadCache[[]*ocicore.Image]
	// image shape cache
	imageShapeCache *cache.GetOrLoadCache[set.Set[string]]

	preBakedImageCompartmentId    string
	cioHardenedImageCompartmentId string

	k8sVersion *semver.Version
}

func (p *DefaultProvider) getImage(ctx context.Context, imageOcid string) (*ocicore.Image, error) {
	return p.imageOcidCache.GetOrLoad(ctx, imageOcid, func(ctx2 context.Context, key string) (*ocicore.Image, error) {
		resp, err := p.computeClient.GetImage(ctx2, ocicore.GetImageRequest{
			ImageId: &key,
		})

		if err != nil {
			return nil, err
		}

		return &resp.Image, nil
	})
}

func (p *DefaultProvider) ResolveImages(ctx context.Context,
	imageCfg *v1beta1.ImageConfig) (*ImageResolveResult, error) {
	if imageCfg != nil {
		if imageCfg.ImageId != nil && imageCfg.ImageFilter != nil {
			return nil, errors.New("cannot define image ocid and image filter together")
		}

		var images []*ocicore.Image
		if imageCfg.ImageId != nil {
			image, err := p.getImage(ctx, *imageCfg.ImageId)
			if err != nil {
				return nil, err
			}

			images = append(images, image)
		} else if imageCfg.ImageFilter != nil {
			is, err := p.filterImage(ctx, imageCfg.ImageType, *imageCfg.ImageFilter)

			if err != nil {
				return nil, err
			}

			images = append(images, is...)
		}

		images, err := p.filterAndSortImages(ctx, images, imageCfg)
		if err != nil {
			return nil, err
		}

		if len(images) == 0 {
			return nil, errors.New("no image match")
		}

		log.FromContext(ctx).V(1).Info("image resolving result", "imageNames",
			strings.Join(lo.Map(images, func(image *ocicore.Image, _ int) string {
				return *image.DisplayName
			}), ","))

		return p.toImageResolveResult(images, imageCfg), nil
	}

	return nil, errors.New("either image ocid or image filter is required")
}

func (p *DefaultProvider) ResolveImageForShape(ctx context.Context,
	imageCfg *v1beta1.ImageConfig, shape string) (*ImageResolveResult, error) {
	images, err := p.ResolveImages(ctx, imageCfg)
	if err != nil {
		return nil, err
	}

	// often times an OKE image support shapes within same architecture, so we filter and sort image first
	// and then check shape compatibility, with hope to hit the shape in the first few iterations, if not
	// the first iteration
	var firstImage *ocicore.Image
	for _, item := range images.Images {
		var shapes set.Set[string]
		shapes, err = p.listShapesForImage(ctx, *item.Id)
		if err != nil {
			return nil, err
		}

		if shapes.Has(shape) {
			firstImage = item
			break
		}
	}

	if firstImage == nil {
		return nil, fmt.Errorf("no image suitable for shape %s", shape)
	}

	log.FromContext(ctx).V(1).Info("image resolving result for shape", "imageName",
		firstImage.DisplayName, "shape", shape)

	// intentionally return the first image only
	return p.toImageResolveResult([]*ocicore.Image{firstImage}, imageCfg), nil
}

func (p *DefaultProvider) listShapesForImage(ctx context.Context, imageOcid string) (set.Set[string], error) {
	return p.imageShapeCache.GetOrLoad(ctx, imageOcid, func(ctx2 context.Context, k string) (set.Set[string], error) {
		var shapes []string
		request := ocicore.ListImageShapeCompatibilityEntriesRequest{
			ImageId: &imageOcid,
		}

		for {
			resp, err := p.computeClient.ListImageShapeCompatibilityEntries(ctx, request)

			if err != nil {
				return nil, err
			}

			shapes = append(shapes, lo.Map(resp.Items, func(item ocicore.ImageShapeCompatibilitySummary, _ int) string {
				return *item.Shape
			})...)

			request.Page = resp.OpcNextPage
			if request.Page == nil {
				break
			}
		}

		return set.New(shapes...), nil
	})
}

func (p *DefaultProvider) filterImage(ctx context.Context, imageType v1beta1.ImageType,
	filter v1beta1.ImageSelectorTerm) ([]*ocicore.Image, error) {
	k, err := utils.HashFor(filter)
	if err != nil {
		return nil, err
	}

	tagFilterFunc := func(image *ocicore.Image) bool {
		if filter.FreeformTags != nil {
			for k, v := range filter.FreeformTags {
				iv, ok := image.FreeformTags[k]
				if !ok || iv != v {
					return false
				}
			}
		}

		if filter.DefinedTags != nil {
			// n -> namespace, m -> map, f -> filter, i -> image, k -> key, v -> value
			for fn, fm := range filter.DefinedTags {
				im, ok := image.DefinedTags[fn]
				if !ok {
					return false
				}

				for fk, fv := range fm {
					if iv, ok := im[fk]; !ok || iv != fv {
						return false
					}
				}
			}
		}

		return true
	}

	return p.imageFilterCache.GetOrLoad(ctx, k, func(ctx2 context.Context, _ string) ([]*ocicore.Image, error) {
		var compartmentId *string
		switch imageType {
		case v1beta1.OKEImage:
			compartmentId = filter.CompartmentId
			if compartmentId == nil {
				compartmentId = &p.preBakedImageCompartmentId
			}
		case v1beta1.Platform:
			compartmentId = nil
		case v1beta1.Custom:
			compartmentId = filter.CompartmentId
		}

		return p.listAndFilterImages(ctx, ocicore.ListImagesRequest{
			CompartmentId:          compartmentId,
			OperatingSystem:        &filter.OsFilter,
			OperatingSystemVersion: &filter.OsVersionFilter,
		}, tagFilterFunc)
	})
}

func (p *DefaultProvider) listAndFilterImages(ctx context.Context, request ocicore.ListImagesRequest,
	extraFilterFunc func(image *ocicore.Image) bool) ([]*ocicore.Image, error) {
	var images []*ocicore.Image
	for {
		resp, err := p.computeClient.ListImages(ctx, request)

		if err != nil {
			return nil, err
		}

		images = append(images, lo.ToSlicePtr(lo.Filter(resp.Items, func(item ocicore.Image, _ int) bool {
			if extraFilterFunc != nil {
				return extraFilterFunc(&item)
			}

			return true
		}))...)

		request.Page = resp.OpcNextPage
		if request.Page == nil {
			break
		}
	}

	return images, nil
}

func (p *DefaultProvider) toImageResolveResult(images []*ocicore.Image,
	imageCfg *v1beta1.ImageConfig) *ImageResolveResult {
	if len(images) == 0 {
		return nil
	}

	imageType := imageCfg.ImageType

	return &ImageResolveResult{
		Images:    images,
		ImageType: imageType,
		Os:        images[0].OperatingSystem,
		OsVersion: images[0].OperatingSystemVersion,
	}
}

func (p *DefaultProvider) filterAndSortImages(ctx context.Context, images []*ocicore.Image,
	imageCfg *v1beta1.ImageConfig) ([]*ocicore.Image, error) {
	if len(images) == 0 {
		return nil, errors.New("no image available")
	}

	// sort by time created by default
	sort.SliceStable(images, func(i, j int) bool {
		return images[i].TimeCreated.After(images[j].TimeCreated.Time)
	})

	// further filtering based on image type.
	preBakedImage := false
	if imageCfg.ImageId != nil {
		if images[0].CompartmentId != nil && *images[0].CompartmentId == p.preBakedImageCompartmentId {
			preBakedImage = true
		}
	} else if imageCfg.ImageType == v1beta1.OKEImage {
		preBakedImage = true
	}

	if !preBakedImage {
		return images, nil
	}

	if p.k8sVersion == nil {
		return nil, errors.New("cannot detect cluster version")
	}

	imgScore := make(map[string]int)
	out := make([]*ocicore.Image, 0)
	for _, i := range images {
		imgKletVersion, err := p.extractKubeletVersionFromPreBakedImage(ctx, i)
		if err != nil {
			// TBD: this is not necessarily an issue, or this is platform image as compute list image
			// always return platform images
			log.FromContext(ctx).V(1).Info(err.Error(), "image", i.Id,
				"imageName", i.DisplayName)
			continue
		}

		score := kubeletVersionCompatibleScore(p.k8sVersion, imgKletVersion)
		if score < 0 {
			log.FromContext(ctx).V(1).Info("skip image due to incompatible",
				"image", i.Id,
				"imageName", i.DisplayName,
				"imageKubeletVersion", imgKletVersion,
				"clusterVersion", p.k8sVersion)
			continue
		}

		imgScore[*i.Id] = score
		out = append(out, i)
	}

	sort.SliceStable(out, func(i, j int) bool {
		si := imgScore[*out[i].Id]
		sj := imgScore[*out[j].Id]

		// return minimal version skew
		return si < sj
	})

	return out, nil
}

func (p *DefaultProvider) refreshClusterVersion() error {
	if p.kubernetesInterface == nil {
		// Skip cluster version refresh when kubernetes client is not available (e.g., in tests)
		return nil
	}
	k8sVersion, err := utils.GetClusterVersion(p.kubernetesInterface)
	if err != nil {
		return err
	}
	p.k8sVersion = k8sVersion
	return nil
}

func (p *DefaultProvider) extractKubeletVersionFromPreBakedImage(ctx context.Context,
	i *ocicore.Image) (*semver.Version, error) {

	if imageK8sVersion, ok := i.FreeformTags["k8s_version"]; ok {
		kletVersion, err := semver.NewVersion(strings.Trim(strings.ToLower(imageK8sVersion), "v"))
		if err != nil {
			return nil, err
		}

		return kletVersion, nil
	}

	if i.BaseImageId == nil {
		return nil, errors.New("missing k8s_version tag")
	}

	// Images created based on OKEImages images don't carry the `k8s_version` tag themselves.
	// In that case we recursively walk the base-image chain (BaseImageId -> GetImage -> BaseImageId ...)
	// until we find an ancestor image that has the tag, or we hit an image with no base (and fail).
	baseImage, err := p.getImage(ctx, *i.BaseImageId)
	if err != nil {
		return nil, err
	}

	return p.extractKubeletVersionFromPreBakedImage(ctx, baseImage)
}

func kubeletVersionCompatibleScore(clusterVersion, kletVersion *semver.Version) int {
	kletMajor := kletVersion.Major
	kletMinor := kletVersion.Minor

	clusterMajor := clusterVersion.Major
	clusterMinor := clusterVersion.Minor

	// https://kubernetes.io/releases/version-skew-policy/
	if kletMajor != clusterMajor {
		return -1
	}

	if kletMinor > clusterMinor {
		return -1
	}

	diff := clusterMinor - kletMinor
	if kletMajor == 1 && kletMinor < 25 && diff > 2 {
		return -1
	}

	if diff > 3 {
		return -1
	}

	return int(diff)
}

func NewProvider(ctx context.Context, kubernetesInterface kubernetes.Interface,
	computeClient oci.ComputeClient, preBakedImageCompartmentId,
	cioHardenedImageCompartmentId string, startAsync <-chan struct{},
	imageFilterCacheRefreshEnabled bool,
	imageFilterCacheRefreshInterval time.Duration) (*DefaultProvider, error) {
	p := &DefaultProvider{
		kubernetesInterface:           kubernetesInterface,
		computeClient:                 computeClient,
		preBakedImageCompartmentId:    preBakedImageCompartmentId,
		cioHardenedImageCompartmentId: cioHardenedImageCompartmentId,
		imageOcidCache:                cache.NewDefaultGetOrLoadCache[*ocicore.Image](),
		imageFilterCache:              cache.NewDefaultGetOrLoadCache[[]*ocicore.Image](),
		imageShapeCache:               cache.NewDefaultGetOrLoadCache[set.Set[string]](),
	}

	go utils.RefreshAtInterval(ctx, true, startAsync, time.Hour, func(_ context.Context) error {
		return p.refreshClusterVersion()
	})()

	if imageFilterCacheRefreshEnabled {
		go utils.RefreshAtInterval(ctx, false, startAsync, imageFilterCacheRefreshInterval,
			func(ctx2 context.Context) error {
				p.imageFilterCache.EvictMatching(func(_ string) bool { return true })
				log.FromContext(ctx2).V(1).Info("image filter cache evicted")
				return nil
			})()
	}

	return p, nil
}
