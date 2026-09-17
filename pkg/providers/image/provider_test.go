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
	"testing"
	"time"

	"github.com/coreos/go-semver/semver"
	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/cache"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	"github.com/oracle/karpenter-provider-oci/pkg/utils"
	"github.com/oracle/oci-go-sdk/v65/common"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/set"
)

func TestNewProvider(t *testing.T) {
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh) // Close immediately to prevent refresh goroutine

	provider, err := NewProvider(context.Background(), nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	assert.NoError(t, err)
	assert.NotNil(t, provider)
	assert.Equal(t, fakeClient, provider.computeClient)
	assert.Equal(t, "prebaked-comp", provider.preBakedImageCompartmentId)
	assert.Equal(t, "cio-comp", provider.cioHardenedImageCompartmentId)
	assert.NotNil(t, provider.imageOcidCache)
	assert.NotNil(t, provider.imageFilterCache)
	assert.NotNil(t, provider.imageShapeCache)
}

func TestResolveImages(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		config      *v1beta1.ImageConfig
		setupFake   func(*fakes.FakeCompute)
		expectError bool
		validate    func(*testing.T, *ImageResolveResult, *fakes.FakeCompute)
	}{
		{
			name:        "nil config",
			config:      nil,
			expectError: true,
		},
		{
			name: "config with both OCID and filter",
			config: &v1beta1.ImageConfig{
				ImageId:     lo.ToPtr("ocid1.image.123"),
				ImageFilter: &v1beta1.ImageSelectorTerm{},
			},
			expectError: true,
		},
		{
			name: "resolve by OCID",
			config: &v1beta1.ImageConfig{
				ImageId:   lo.ToPtr("ocid1.image.123"),
				ImageType: v1beta1.Custom,
			},
			setupFake: func(f *fakes.FakeCompute) {
				f.GetImageResp = ocicore.GetImageResponse{
					Image: ocicore.Image{
						Id:                     lo.ToPtr("ocid1.image.123"),
						DisplayName:            lo.ToPtr("test-image"),
						OperatingSystem:        lo.ToPtr("Oracle Linux"),
						OperatingSystemVersion: lo.ToPtr("8"),
						CompartmentId:          lo.ToPtr("custom-comp"),
						TimeCreated:            &common.SDKTime{Time: time.Now()},
					},
				}
			},
			expectError: false,
			validate: func(t *testing.T, result *ImageResolveResult, f *fakes.FakeCompute) {
				assert.NotNil(t, result)
				assert.Len(t, result.Images, 1)
				assert.Equal(t, "ocid1.image.123", *result.Images[0].Id)
				assert.Equal(t, v1beta1.Custom, result.ImageType)
				assert.Equal(t, "Oracle Linux", *result.Os)
				assert.Equal(t, "8", *result.OsVersion)
				assert.Equal(t, 1, f.GetImageCount.Get())
			},
		},
		{
			name: "resolve by filter - single match",
			config: &v1beta1.ImageConfig{
				ImageType: v1beta1.Platform,
				ImageFilter: &v1beta1.ImageSelectorTerm{
					OsFilter:        "Oracle Linux",
					OsVersionFilter: "8",
				},
			},
			setupFake: func(f *fakes.FakeCompute) {
				f.ListImagesResp = ocicore.ListImagesResponse{
					Items: []ocicore.Image{
						{
							Id:                     lo.ToPtr("ocid1.image.456"),
							DisplayName:            lo.ToPtr("oracle-linux-8"),
							OperatingSystem:        lo.ToPtr("Oracle Linux"),
							OperatingSystemVersion: lo.ToPtr("8"),
							CompartmentId:          nil, // Platform images have nil compartment
							TimeCreated:            &common.SDKTime{Time: time.Now()},
						},
					},
				}
			},
			expectError: false,
			validate: func(t *testing.T, result *ImageResolveResult, f *fakes.FakeCompute) {
				assert.NotNil(t, result)
				assert.Len(t, result.Images, 1)
				assert.Equal(t, "ocid1.image.456", *result.Images[0].Id)
				assert.Equal(t, v1beta1.Platform, result.ImageType)
				assert.Equal(t, 1, f.ListImagesCount.Get())
			},
		},
		{
			name: "resolve by filter - no matches",
			config: &v1beta1.ImageConfig{
				ImageFilter: &v1beta1.ImageSelectorTerm{
					OsFilter: "NonExistent",
				},
			},
			setupFake: func(f *fakes.FakeCompute) {
				f.ListImagesResp = ocicore.ListImagesResponse{
					Items: []ocicore.Image{},
				}
			},
			expectError: true,
		},
		{
			name: "OCID not found",
			config: &v1beta1.ImageConfig{
				ImageId: lo.ToPtr("ocid1.image.123"),
			},
			setupFake: func(f *fakes.FakeCompute) {
				f.GetImageErr = errors.New("image not found")
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := &fakes.FakeCompute{}
			if tt.setupFake != nil {
				tt.setupFake(fakeClient)
			}

			startCh := make(chan struct{})
			close(startCh)
			provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

			result, err := provider.ResolveImages(ctx, tt.config)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validate != nil {
					tt.validate(t, result, fakeClient)
				}
			}
		})
	}
}

func TestListShapesForImage(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	// Setup shape compatibility response
	fakeClient.ListImageShapeCompatibilityEntriesResp = ocicore.ListImageShapeCompatibilityEntriesResponse{
		Items: []ocicore.ImageShapeCompatibilitySummary{
			{Shape: lo.ToPtr("VM.Standard2.1")},
			{Shape: lo.ToPtr("VM.Standard2.2")},
		},
	}

	shapes, err := provider.listShapesForImage(ctx, "ocid1.image.123", false)

	assert.NoError(t, err)
	assert.Equal(t, set.New("VM.Standard2.1", "VM.Standard2.2"), shapes)
	assert.Equal(t, 1, fakeClient.ListImageShapeCompatibilityEntriesCount.Get())
}

func TestResolveImageForShape(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	// Setup base image resolution
	imageConfig := &v1beta1.ImageConfig{
		ImageId: lo.ToPtr("ocid1.image.123"),
	}
	fakeClient.GetImageResp = ocicore.GetImageResponse{
		Image: ocicore.Image{
			Id:              lo.ToPtr("ocid1.image.123"),
			DisplayName:     lo.ToPtr("test-image"),
			TimeCreated:     &common.SDKTime{Time: time.Now()},
			OperatingSystem: lo.ToPtr("Oracle Linux"),
		},
	}

	// Setup shape compatibility
	fakeClient.OnListImageShapeCompatibilityEntries = func(ctx context.Context,
		request ocicore.ListImageShapeCompatibilityEntriesRequest) (ocicore.ListImageShapeCompatibilityEntriesResponse,
		error) {
		var summary []ocicore.ImageShapeCompatibilitySummary
		if *request.ImageId == "ocid1.image.123" {
			summary = []ocicore.ImageShapeCompatibilitySummary{
				{Shape: lo.ToPtr("VM.Standard2.1")},
			}
		} else {
			summary = []ocicore.ImageShapeCompatibilitySummary{
				{Shape: lo.ToPtr("VM.Standard2.2")},
			}
		}

		return ocicore.ListImageShapeCompatibilityEntriesResponse{
			Items: summary,
		}, nil
	}

	result, err := provider.ResolveImageForShape(ctx, imageConfig, "VM.Standard2.1")

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Len(t, result.Images, 1)
	assert.Equal(t, "ocid1.image.123", *result.Images[0].Id)
	assert.Equal(t, 1, fakeClient.GetImageCount.Get())
	assert.Equal(t, 1, fakeClient.ListImageShapeCompatibilityEntriesCount.Get())

	imageConfig = &v1beta1.ImageConfig{
		ImageFilter: &v1beta1.ImageSelectorTerm{
			OsFilter: "Oracle Linux",
		},
	}

	imageCreationTime := time.Now()
	fakeClient.ListImagesResp = ocicore.ListImagesResponse{
		Items: []ocicore.Image{
			{
				Id:              lo.ToPtr("ocid1.image.123"),
				DisplayName:     lo.ToPtr("test-image1"),
				TimeCreated:     &common.SDKTime{Time: imageCreationTime},
				OperatingSystem: lo.ToPtr("Oracle Linux"),
			},
			{
				Id:              lo.ToPtr("ocid1.image.456"),
				DisplayName:     lo.ToPtr("test-image2"),
				TimeCreated:     &common.SDKTime{Time: imageCreationTime},
				OperatingSystem: lo.ToPtr("Oracle Linux"),
			},
		},
	}

	result, err = provider.ResolveImageForShape(ctx, imageConfig, "VM.Standard2.1")

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Len(t, result.Images, 1)
	assert.Equal(t, "ocid1.image.123", *result.Images[0].Id)
	assert.Equal(t, 1, fakeClient.ListImagesCount.Get())
	assert.Equal(t, 1, fakeClient.ListImageShapeCompatibilityEntriesCount.Get())

	result, err = provider.ResolveImageForShape(ctx, imageConfig, "VM.Standard2.2")

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Len(t, result.Images, 1)
	assert.Equal(t, "ocid1.image.456", *result.Images[0].Id)
	assert.Equal(t, 1, fakeClient.ListImagesCount.Get())
	assert.Equal(t, 2, fakeClient.ListImageShapeCompatibilityEntriesCount.Get())
}

func TestFilterImage(t *testing.T) {
	tests := []struct {
		name          string
		imageType     v1beta1.ImageType
		filter        v1beta1.ImageSelectorTerm
		setupFake     func(*fakes.FakeCompute)
		expectError   bool
		expectedCount int
	}{
		{
			name:      "platform image - OS filter",
			imageType: v1beta1.Platform,
			filter: v1beta1.ImageSelectorTerm{
				OsFilter: "Oracle Linux",
			},
			setupFake: func(f *fakes.FakeCompute) {
				f.ListImagesResp = ocicore.ListImagesResponse{
					Items: []ocicore.Image{
						{
							Id:              lo.ToPtr("ocid1.image.123"),
							OperatingSystem: lo.ToPtr("Oracle Linux"),
							TimeCreated:     &common.SDKTime{Time: time.Now()},
						},
						{
							Id:              lo.ToPtr("ocid1.image.456"),
							OperatingSystem: lo.ToPtr("Ubuntu"),
							TimeCreated:     &common.SDKTime{Time: time.Now()},
						},
					},
				}
			},
			expectError:   false,
			expectedCount: 1,
		},
		{
			name:      "OKE image - compartment filter",
			imageType: v1beta1.OKEImage,
			filter: v1beta1.ImageSelectorTerm{
				OsFilter: "Oracle Linux",
			},
			setupFake: func(f *fakes.FakeCompute) {
				f.ListImagesResp = ocicore.ListImagesResponse{
					Items: []ocicore.Image{
						{
							Id:              lo.ToPtr("ocid1.image.123"),
							OperatingSystem: lo.ToPtr("Oracle Linux"),
							CompartmentId:   lo.ToPtr("prebaked-comp"),
							TimeCreated:     &common.SDKTime{Time: time.Now()},
						},
					},
				}
			},
			expectError:   false,
			expectedCount: 1,
		},
		{
			name:      "custom image - custom compartment",
			imageType: v1beta1.Custom,
			filter: v1beta1.ImageSelectorTerm{
				OsFilter:      "Oracle Linux",
				CompartmentId: lo.ToPtr("custom-comp"),
			},
			setupFake: func(f *fakes.FakeCompute) {
				f.ListImagesResp = ocicore.ListImagesResponse{
					Items: []ocicore.Image{
						{
							Id:              lo.ToPtr("ocid1.image.123"),
							OperatingSystem: lo.ToPtr("Oracle Linux"),
							CompartmentId:   lo.ToPtr("custom-comp"),
							TimeCreated:     &common.SDKTime{Time: time.Now()},
						},
					},
				}
			},
			expectError:   false,
			expectedCount: 1,
		},
		{
			name:      "tag filtering - freeform tags",
			imageType: v1beta1.Platform,
			filter: v1beta1.ImageSelectorTerm{
				FreeformTags: map[string]string{
					"env": "test",
				},
			},
			setupFake: func(f *fakes.FakeCompute) {
				f.ListImagesResp = ocicore.ListImagesResponse{
					Items: []ocicore.Image{
						{
							Id:          lo.ToPtr("ocid1.image.123"),
							TimeCreated: &common.SDKTime{Time: time.Now()},
							FreeformTags: map[string]string{
								"env": "test",
							},
						},
						{
							Id:          lo.ToPtr("ocid1.image.456"),
							TimeCreated: &common.SDKTime{Time: time.Now()},
							FreeformTags: map[string]string{
								"env": "prod",
							},
						},
					},
				}
			},
			expectError:   false,
			expectedCount: 1,
		},
		{
			name:      "tag filtering - defined tags",
			imageType: v1beta1.Platform,
			filter: v1beta1.ImageSelectorTerm{
				DefinedTags: map[string]map[string]string{
					"Oracle-Tags": {
						"CreatedBy": "test-user",
					},
				},
			},
			setupFake: func(f *fakes.FakeCompute) {
				f.ListImagesResp = ocicore.ListImagesResponse{
					Items: []ocicore.Image{
						{
							Id:          lo.ToPtr("ocid1.image.123"),
							TimeCreated: &common.SDKTime{Time: time.Now()},
							DefinedTags: map[string]map[string]interface{}{
								"Oracle-Tags": {
									"CreatedBy": "test-user",
								},
							},
						},
						{
							Id:          lo.ToPtr("ocid1.image.456"),
							TimeCreated: &common.SDKTime{Time: time.Now()},
							DefinedTags: map[string]map[string]interface{}{
								"Oracle-Tags": {
									"CreatedBy": "other-user",
								},
							},
						},
					},
				}
			},
			expectError:   false,
			expectedCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			fakeClient := &fakes.FakeCompute{}
			if tt.setupFake != nil {
				tt.setupFake(fakeClient)
			}

			startCh := make(chan struct{})
			close(startCh)
			provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

			images, err := provider.filterImage(ctx, tt.imageType, tt.filter, false)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Len(t, images, tt.expectedCount)
				assert.Equal(t, 1, fakeClient.ListImagesCount.Get())
			}
		})
	}
}

func TestListAndFilterImages(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	// Setup mock response
	fakeClient.ListImagesResp = ocicore.ListImagesResponse{
		Items: []ocicore.Image{
			{
				Id:              lo.ToPtr("ocid1.image.123"),
				OperatingSystem: lo.ToPtr("Oracle Linux"),
				TimeCreated:     &common.SDKTime{Time: time.Now()},
			},
			{
				Id:              lo.ToPtr("ocid1.image.456"),
				OperatingSystem: lo.ToPtr("Ubuntu"),
				TimeCreated:     &common.SDKTime{Time: time.Now()},
			},
		},
	}

	request := ocicore.ListImagesRequest{
		CompartmentId: lo.ToPtr("test-comp"),
	}

	filterFunc := func(image *ocicore.Image) bool {
		return *image.OperatingSystem == "Oracle Linux"
	}

	images, err := provider.listAndFilterImages(ctx, request, filterFunc)

	assert.NoError(t, err)
	assert.Len(t, images, 1)
	assert.Equal(t, "ocid1.image.123", *images[0].Id)
	assert.Equal(t, 1, fakeClient.ListImagesCount.Get())
}

func TestToImageResolveResult(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	tests := []struct {
		name     string
		images   []*ocicore.Image
		imageCfg *v1beta1.ImageConfig
		expected *ImageResolveResult
	}{
		{
			name:     "empty images",
			images:   []*ocicore.Image{},
			imageCfg: nil,
			expected: nil,
		},
		{
			name: "platform image",
			images: []*ocicore.Image{
				{
					Id:                     lo.ToPtr("ocid1.image.123"),
					OperatingSystem:        lo.ToPtr("Oracle Linux"),
					OperatingSystemVersion: lo.ToPtr("8"),
				},
			},
			imageCfg: &v1beta1.ImageConfig{ImageType: v1beta1.Platform},
			expected: &ImageResolveResult{
				Images: []*ocicore.Image{{Id: lo.ToPtr("ocid1.image.123"),
					OperatingSystem: lo.ToPtr("Oracle Linux"), OperatingSystemVersion: lo.ToPtr("8")}},
				ImageType: v1beta1.Platform,
				Os:        lo.ToPtr("Oracle Linux"),
				OsVersion: lo.ToPtr("8"),
			},
		},
		{
			name: "OKE image",
			images: []*ocicore.Image{
				{
					Id:                     lo.ToPtr("ocid1.image.123"),
					CompartmentId:          lo.ToPtr("prebaked-comp"),
					OperatingSystem:        lo.ToPtr("Oracle Linux"),
					OperatingSystemVersion: lo.ToPtr("8"),
				},
			},
			imageCfg: &v1beta1.ImageConfig{ImageType: v1beta1.OKEImage},
			expected: &ImageResolveResult{
				Images: []*ocicore.Image{{Id: lo.ToPtr("ocid1.image.123"),
					CompartmentId: lo.ToPtr("prebaked-comp"), OperatingSystem: lo.ToPtr("Oracle Linux"),
					OperatingSystemVersion: lo.ToPtr("8")}},
				ImageType: v1beta1.OKEImage,
				Os:        lo.ToPtr("Oracle Linux"),
				OsVersion: lo.ToPtr("8"),
			},
		},
		{
			name: "CIO hardened image",
			images: []*ocicore.Image{
				{
					Id:                     lo.ToPtr("ocid1.image.123"),
					CompartmentId:          lo.ToPtr("cio-comp"),
					OperatingSystem:        lo.ToPtr("Oracle Linux"),
					OperatingSystemVersion: lo.ToPtr("8"),
				},
			},
			imageCfg: &v1beta1.ImageConfig{ImageType: v1beta1.Custom},
			expected: &ImageResolveResult{
				Images: []*ocicore.Image{{Id: lo.ToPtr("ocid1.image.123"),
					CompartmentId: lo.ToPtr("cio-comp"), OperatingSystem: lo.ToPtr("Oracle Linux"),
					OperatingSystemVersion: lo.ToPtr("8")}},
				ImageType: v1beta1.Custom,
				Os:        lo.ToPtr("Oracle Linux"),
				OsVersion: lo.ToPtr("8"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := provider.toImageResolveResult(tt.images, tt.imageCfg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFilterAndSortImages(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	// Set up a mock k8s version
	provider.k8sVersion = &semver.Version{Major: 1, Minor: 27}

	now := time.Now()
	past := now.Add(-time.Hour)

	images := []*ocicore.Image{
		{
			Id:          lo.ToPtr("ocid1.image.newer"),
			TimeCreated: &common.SDKTime{Time: now},
			FreeformTags: map[string]string{
				"k8s_version": "v1.27.1",
			},
		},
		{
			Id:          lo.ToPtr("ocid1.image.older"),
			TimeCreated: &common.SDKTime{Time: past},
			FreeformTags: map[string]string{
				"k8s_version": "v1.26.1",
			},
		},
	}

	imageCfg := &v1beta1.ImageConfig{
		ImageType: v1beta1.OKEImage,
	}

	filtered, err := provider.filterAndSortImages(ctx, images, imageCfg, false)

	assert.NoError(t, err)
	assert.Len(t, filtered, 2)
	// Should be sorted by time created (newest first)
	assert.Equal(t, "ocid1.image.newer", *filtered[0].Id)
	assert.Equal(t, "ocid1.image.older", *filtered[1].Id)
}

func TestExtractKubeletVersionFromPreBakedImage(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		image    *ocicore.Image
		expected *semver.Version
		hasError bool
	}{
		{
			name: "valid k8s version",
			image: &ocicore.Image{
				FreeformTags: map[string]string{
					"k8s_version": "v1.27.1",
				},
			},
			expected: &semver.Version{Major: 1, Minor: 27, Patch: 1},
			hasError: false,
		},
		{
			name: "missing k8s_version tag",
			image: &ocicore.Image{
				FreeformTags: map[string]string{},
			},
			expected: nil,
			hasError: true,
		},
		{
			name: "invalid version format",
			image: &ocicore.Image{
				FreeformTags: map[string]string{
					"k8s_version": "invalid",
				},
			},
			expected: nil,
			hasError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := &fakes.FakeCompute{}
			startCh := make(chan struct{})
			close(startCh)
			provider, err := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)
			assert.NoError(t, err)

			result, err := provider.extractKubeletVersionFromPreBakedImage(ctx, tt.image, false)

			if tt.hasError {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestKubeletVersionCompatibleScore(t *testing.T) {
	clusterVersion := &semver.Version{Major: 1, Minor: 27}

	tests := []struct {
		name     string
		kletVer  *semver.Version
		expected int
	}{
		{
			name:     "exact match",
			kletVer:  &semver.Version{Major: 1, Minor: 27},
			expected: 0,
		},
		{
			name:     "one minor behind",
			kletVer:  &semver.Version{Major: 1, Minor: 26},
			expected: 1,
		},
		{
			name:     "two minors behind",
			kletVer:  &semver.Version{Major: 1, Minor: 25},
			expected: 2,
		},
		{
			name:     "too old (k8s 1.25 special case)",
			kletVer:  &semver.Version{Major: 1, Minor: 24},
			expected: -1,
		},
		{
			name:     "future version",
			kletVer:  &semver.Version{Major: 1, Minor: 28},
			expected: -1,
		},
		{
			name:     "different major",
			kletVer:  &semver.Version{Major: 2, Minor: 0},
			expected: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := kubeletVersionCompatibleScore(clusterVersion, tt.kletVer)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractKubeletVersionFromPreBakedImage_BaseImageLookup(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, err := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)
	assert.NoError(t, err)

	child := &ocicore.Image{
		Id:           lo.ToPtr("ocid1.image.child"),
		BaseImageId:  lo.ToPtr("ocid1.image.base"),
		FreeformTags: map[string]string{},
	}

	fakeClient.OnGetImage = func(ctx context.Context, req ocicore.GetImageRequest) (ocicore.GetImageResponse, error) {
		if req.ImageId != nil && *req.ImageId == "ocid1.image.base" {
			return ocicore.GetImageResponse{
				Image: ocicore.Image{
					Id: lo.ToPtr("ocid1.image.base"),
					FreeformTags: map[string]string{
						"k8s_version": "v1.26.3",
					},
				},
			}, nil
		}
		return ocicore.GetImageResponse{}, errors.New("unexpected image id")
	}

	got, err := provider.extractKubeletVersionFromPreBakedImage(ctx, child, false)
	assert.NoError(t, err)
	assert.Equal(t, &semver.Version{Major: 1, Minor: 26, Patch: 3}, got)
}

func TestExtractKubeletVersionFromPreBakedImage_DeepChain(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, err := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)
	assert.NoError(t, err)

	child := &ocicore.Image{
		Id:           lo.ToPtr("ocid1.image.child"),
		BaseImageId:  lo.ToPtr("ocid1.image.base"),
		FreeformTags: map[string]string{},
	}

	imageDB := map[string]ocicore.Image{
		"ocid1.image.base": {
			Id:           lo.ToPtr("ocid1.image.base"),
			BaseImageId:  lo.ToPtr("ocid1.image.grand"),
			FreeformTags: map[string]string{},
		},
		"ocid1.image.grand": {
			Id: lo.ToPtr("ocid1.image.grand"),
			FreeformTags: map[string]string{
				"k8s_version": "v1.27.4",
			},
		},
	}

	fakeClient.OnGetImage = func(ctx context.Context, req ocicore.GetImageRequest) (ocicore.GetImageResponse, error) {
		if req.ImageId == nil {
			return ocicore.GetImageResponse{}, errors.New("nil image id")
		}
		img, ok := imageDB[*req.ImageId]
		if !ok {
			return ocicore.GetImageResponse{}, errors.New("unexpected image id")
		}
		return ocicore.GetImageResponse{Image: img}, nil
	}

	got, err := provider.extractKubeletVersionFromPreBakedImage(ctx, child, false)
	assert.NoError(t, err)
	assert.Equal(t, &semver.Version{Major: 1, Minor: 27, Patch: 4}, got)
}

func TestExtractKubeletVersionFromPreBakedImage_NotFoundInChain(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, err := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)
	assert.NoError(t, err)

	child := &ocicore.Image{
		Id:           lo.ToPtr("ocid1.image.child"),
		BaseImageId:  lo.ToPtr("ocid1.image.base"),
		FreeformTags: map[string]string{},
	}

	imageDB := map[string]ocicore.Image{
		"ocid1.image.base": {
			Id:           lo.ToPtr("ocid1.image.base"),
			BaseImageId:  lo.ToPtr("ocid1.image.grand"),
			FreeformTags: map[string]string{},
		},
		"ocid1.image.grand": {
			Id:           lo.ToPtr("ocid1.image.grand"),
			BaseImageId:  nil,
			FreeformTags: map[string]string{},
		},
	}

	fakeClient.OnGetImage = func(ctx context.Context, req ocicore.GetImageRequest) (ocicore.GetImageResponse, error) {
		if req.ImageId == nil {
			return ocicore.GetImageResponse{}, errors.New("nil image id")
		}
		img, ok := imageDB[*req.ImageId]
		if !ok {
			return ocicore.GetImageResponse{}, errors.New("unexpected image id")
		}
		return ocicore.GetImageResponse{Image: img}, nil
	}

	got, err := provider.extractKubeletVersionFromPreBakedImage(ctx, child, false)
	assert.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "missing k8s_version tag")
}

func TestGetImageCaching(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	fakeClient.GetImageResp = ocicore.GetImageResponse{
		Image: ocicore.Image{
			Id:                     lo.ToPtr("ocid1.image.123"),
			DisplayName:            lo.ToPtr("test-image"),
			OperatingSystem:        lo.ToPtr("Oracle Linux"),
			OperatingSystemVersion: lo.ToPtr("8"),
			TimeCreated:            &common.SDKTime{Time: time.Now()},
		},
	}

	// First call
	image1, err := provider.getImage(ctx, "ocid1.image.123", false)
	assert.NoError(t, err)
	assert.Equal(t, "ocid1.image.123", *image1.Id)
	assert.Equal(t, 1, fakeClient.GetImageCount.Get())

	// Second call with same OCID should hit cache
	image2, err := provider.getImage(ctx, "ocid1.image.123", false)
	assert.NoError(t, err)
	assert.Equal(t, "ocid1.image.123", *image2.Id)
	assert.Equal(t, 1, fakeClient.GetImageCount.Get()) // Should still be 1
}

func TestListShapesForImageCaching(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	fakeClient.ListImageShapeCompatibilityEntriesResp = ocicore.ListImageShapeCompatibilityEntriesResponse{
		Items: []ocicore.ImageShapeCompatibilitySummary{
			{Shape: lo.ToPtr("VM.Standard2.1")},
		},
	}

	// First call
	shapes1, err := provider.listShapesForImage(ctx, "ocid1.image.123", false)
	assert.NoError(t, err)
	assert.True(t, shapes1.Has("VM.Standard2.1"))
	assert.Equal(t, 1, fakeClient.ListImageShapeCompatibilityEntriesCount.Get())

	// Second call with same OCID should hit cache
	shapes2, err := provider.listShapesForImage(ctx, "ocid1.image.123", false)
	assert.NoError(t, err)
	assert.True(t, shapes2.Has("VM.Standard2.1"))
	assert.Equal(t, 1, fakeClient.ListImageShapeCompatibilityEntriesCount.Get()) // Should still be 1
}

func TestFilterImageCaching(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	filter := v1beta1.ImageSelectorTerm{
		OsFilter: "Oracle Linux",
	}

	fakeClient.ListImagesResp = ocicore.ListImagesResponse{
		Items: []ocicore.Image{
			{
				Id:              lo.ToPtr("ocid1.image.123"),
				OperatingSystem: lo.ToPtr("Oracle Linux"),
				TimeCreated:     &common.SDKTime{Time: time.Now()},
			},
		},
	}

	// First call
	images1, err := provider.filterImage(ctx, v1beta1.Platform, filter, false)
	assert.NoError(t, err)
	assert.Len(t, images1, 1)
	assert.Equal(t, 1, fakeClient.ListImagesCount.Get())

	// Second call with identical filter should hit cache
	images2, err := provider.filterImage(ctx, v1beta1.Platform, filter, false)
	assert.NoError(t, err)
	assert.Len(t, images2, 1)
	assert.Equal(t, 1, fakeClient.ListImagesCount.Get()) // Should still be 1
}

func TestFilterAndSortImages_K8sVersionMissing(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	// Do not set k8sVersion
	// provider.k8sVersion is nil

	images := []*ocicore.Image{
		{
			Id:          lo.ToPtr("ocid1.image.123"),
			TimeCreated: &common.SDKTime{Time: time.Now()},
			FreeformTags: map[string]string{
				"k8s_version": "v1.27.1",
			},
			CompartmentId: lo.ToPtr("prebaked-comp"), // This makes it pre-baked
		},
	}

	imageCfg := &v1beta1.ImageConfig{
		ImageType: v1beta1.OKEImage, // This makes it pre-baked
	}

	// Should return error when k8sVersion is nil but images are pre-baked
	_, err := provider.filterAndSortImages(ctx, images, imageCfg, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot detect cluster version")
}

func TestToImageResolveResult_CioHardened(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	images := []*ocicore.Image{
		{
			Id:                     lo.ToPtr("ocid1.image.123"),
			CompartmentId:          lo.ToPtr("cio-comp"), // CIO hardened compartment
			OperatingSystem:        lo.ToPtr("Oracle Linux"),
			OperatingSystemVersion: lo.ToPtr("8"),
		},
	}

	imageCfg := &v1beta1.ImageConfig{ImageType: v1beta1.Custom}
	result := provider.toImageResolveResult(images, imageCfg)
	assert.NotNil(t, result)
	assert.Equal(t, v1beta1.Custom, result.ImageType) // Custom, not Platform or OKE
}

func TestFilterAndSortImages_ExtractVersionError(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	// Set up a mock k8s version
	version, _ := semver.NewVersion("1.27.0")
	provider.k8sVersion = version

	images := []*ocicore.Image{
		{
			Id:           lo.ToPtr("ocid1.image.123"),
			TimeCreated:  &common.SDKTime{Time: time.Now()},
			FreeformTags: map[string]string{
				// Missing "k8s_version" tag - this will cause extractKubeletVersionFromPreBakedImage to error
			},
			CompartmentId: lo.ToPtr("prebaked-comp"),
		},
	}

	imageCfg := &v1beta1.ImageConfig{
		ImageType: v1beta1.OKEImage,
	}

	// Should filter out the image with missing k8s_version tag and return empty list
	filtered, err := provider.filterAndSortImages(ctx, images, imageCfg, false)
	assert.NoError(t, err)
	assert.Len(t, filtered, 0) // Image should be filtered out due to missing k8s_version
}

func TestListAndFilterImages_Pagination(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	// Set up pagination - first response has items and next page
	fakeClient.OnListImages = func(ctx context.Context, req ocicore.ListImagesRequest) (
		ocicore.ListImagesResponse, error) {
		if req.Page == nil {
			// First page
			return ocicore.ListImagesResponse{
				Items: []ocicore.Image{
					{
						Id:              lo.ToPtr("ocid1.image.123"),
						OperatingSystem: lo.ToPtr("Oracle Linux"),
						TimeCreated:     &common.SDKTime{Time: time.Now()},
					},
				},
				OpcNextPage: lo.ToPtr("page2"),
			}, nil
		} else if *req.Page == "page2" {
			// Second page
			return ocicore.ListImagesResponse{
				Items: []ocicore.Image{
					{
						Id:              lo.ToPtr("ocid1.image.456"),
						OperatingSystem: lo.ToPtr("Oracle Linux"),
						TimeCreated:     &common.SDKTime{Time: time.Now()},
					},
				},
				// No more pages
			}, nil
		}
		return ocicore.ListImagesResponse{}, nil
	}

	request := ocicore.ListImagesRequest{
		CompartmentId: lo.ToPtr("test-comp"),
	}

	filterFunc := func(image *ocicore.Image) bool {
		return *image.OperatingSystem == "Oracle Linux"
	}

	images, err := provider.listAndFilterImages(ctx, request, filterFunc)

	assert.NoError(t, err)
	assert.Len(t, images, 2) // Should get images from both pages
	assert.Equal(t, "ocid1.image.123", *images[0].Id)
	assert.Equal(t, "ocid1.image.456", *images[1].Id)
}

// A shape that none of the configured images covers must say so, and name the shape - this is
// what ends up in the NodeClass's ImageReady condition when a user's images do not cover what
// they asked to launch.
func TestResolveImageForShape_NoCompatibleImageIsReported(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	fakeClient.GetImageResp = ocicore.GetImageResponse{
		Image: ocicore.Image{
			Id:              lo.ToPtr("ocid1.image.arm"),
			DisplayName:     lo.ToPtr("arm-image"),
			TimeCreated:     &common.SDKTime{Time: time.Now()},
			OperatingSystem: lo.ToPtr("Oracle Linux"),
		},
	}
	// The one candidate image supports an ARM shape only.
	fakeClient.OnListImageShapeCompatibilityEntries = func(context.Context,
		ocicore.ListImageShapeCompatibilityEntriesRequest) (ocicore.ListImageShapeCompatibilityEntriesResponse,
		error) {
		return ocicore.ListImageShapeCompatibilityEntriesResponse{
			Items: []ocicore.ImageShapeCompatibilitySummary{{Shape: lo.ToPtr("VM.Standard.A1.Flex")}},
		}, nil
	}

	_, err := provider.ResolveImageForShape(ctx,
		&v1beta1.ImageConfig{ImageId: lo.ToPtr("ocid1.image.arm")}, "VM.Standard.E5.Flex")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no image suitable for shape")
	assert.Contains(t, err.Error(), "VM.Standard.E5.Flex", "the error should name the shape asked for")
}

// A failing service must surface its own error rather than being reported as though no image
// covered the shape - the two need different things from whoever reads the condition.
func TestResolveImageForShape_ServiceFailureSurfacesItsOwnError(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	fakeClient.GetImageResp = ocicore.GetImageResponse{
		Image: ocicore.Image{
			Id:              lo.ToPtr("ocid1.image.789"),
			DisplayName:     lo.ToPtr("test-image"),
			TimeCreated:     &common.SDKTime{Time: time.Now()},
			OperatingSystem: lo.ToPtr("Oracle Linux"),
		},
	}
	fakeClient.OnListImageShapeCompatibilityEntries = func(context.Context,
		ocicore.ListImageShapeCompatibilityEntriesRequest) (ocicore.ListImageShapeCompatibilityEntriesResponse,
		error) {
		return ocicore.ListImageShapeCompatibilityEntriesResponse{}, errors.New("service unavailable")
	}

	_, err := provider.ResolveImageForShape(ctx,
		&v1beta1.ImageConfig{ImageId: lo.ToPtr("ocid1.image.789")}, "VM.Standard.E5.Flex")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "service unavailable")
	assert.NotContains(t, err.Error(), "no image suitable for shape")
}

// A configuration that cannot yield an image must fail with a message saying which way it is
// wrong, since that message is what the user sees on the NodeClass.
func TestResolveImages_ConfigurationErrorsAreDescriptive(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	for name, tt := range map[string]struct {
		cfg  *v1beta1.ImageConfig
		want string
	}{
		// A present-but-empty config selects nothing, which is caught further in than a nil one.
		"neither id nor filter": {&v1beta1.ImageConfig{}, "no image available"},
		"both id and filter": {&v1beta1.ImageConfig{
			ImageId:     lo.ToPtr("ocid1.image.123"),
			ImageFilter: &v1beta1.ImageSelectorTerm{OsFilter: "Oracle Linux"},
		}, "cannot define image ocid and image filter together"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := provider.ResolveImages(ctx, tt.cfg)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}

	// A nil configuration is the "neither" case by another route.
	_, err := provider.ResolveImages(ctx, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "either image ocid or image filter is required")

	// A filter that matches nothing empties the candidate list before any filtering happens.
	fakeClient.ListImagesResp = ocicore.ListImagesResponse{}
	_, err = provider.ResolveImages(ctx, &v1beta1.ImageConfig{
		ImageFilter: &v1beta1.ImageSelectorTerm{OsFilter: "No Such OS"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no image available")
}

// The selection can also empty out later, when candidate images are dropped for being
// incompatible with the cluster version. That must fail rather than return nothing.
func TestResolveImages_FilteredToNothingIsAnError(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)
	provider.k8sVersion = semver.New("1.30.0")

	// OKE images are matched, but none carries a kubelet version, so all are dropped.
	fakeClient.ListImagesResp = ocicore.ListImagesResponse{
		Items: []ocicore.Image{
			{
				Id:              lo.ToPtr("ocid1.image.untagged"),
				DisplayName:     lo.ToPtr("untagged-image"),
				TimeCreated:     &common.SDKTime{Time: time.Now()},
				OperatingSystem: lo.ToPtr("Oracle Linux"),
			},
		},
	}

	_, err := provider.ResolveImages(ctx, &v1beta1.ImageConfig{
		ImageType:   v1beta1.OKEImage,
		ImageFilter: &v1beta1.ImageSelectorTerm{OsFilter: "Oracle Linux"},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no image match")
}

// The cached-only path must make no API call at all, whatever the cache state.
func TestResolveImageForShapeCached_NeverCallsOCI(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	cfg := &v1beta1.ImageConfig{ImageId: lo.ToPtr("ocid1.image.123")}

	_, ok := provider.ResolveImageForShapeCached(ctx, cfg, "VM.Standard2.1")

	assert.False(t, ok, "a cold cache has no answer")
	assert.Equal(t, 0, fakeClient.GetImageCount.Get(), "no image lookup may be issued")
	assert.Equal(t, 0, fakeClient.ListImageShapeCompatibilityEntriesCount.Get(),
		"no shape-compatibility lookup may be issued")
}

// And once a launch has warmed the cache, the cached path must name the same image the launch
// would - it runs the same selection, so a node is modelled against the image it will actually
// boot. Diverging here would file measurements under a key no node ever used.
func TestResolveImageForShapeCached_AgreesWithLaunch(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	fakeClient.GetImageResp = ocicore.GetImageResponse{
		Image: ocicore.Image{
			Id:              lo.ToPtr("ocid1.image.123"),
			DisplayName:     lo.ToPtr("test-image"),
			TimeCreated:     &common.SDKTime{Time: time.Now()},
			OperatingSystem: lo.ToPtr("Oracle Linux"),
		},
	}
	fakeClient.OnListImageShapeCompatibilityEntries = func(context.Context,
		ocicore.ListImageShapeCompatibilityEntriesRequest) (ocicore.ListImageShapeCompatibilityEntriesResponse,
		error) {
		return ocicore.ListImageShapeCompatibilityEntriesResponse{
			Items: []ocicore.ImageShapeCompatibilitySummary{{Shape: lo.ToPtr("VM.Standard2.1")}},
		}, nil
	}

	cfg := &v1beta1.ImageConfig{ImageId: lo.ToPtr("ocid1.image.123")}

	// Cold: declines.
	_, ok := provider.ResolveImageForShapeCached(ctx, cfg, "VM.Standard2.1")
	require.False(t, ok)

	// A launch resolves it for real, warming both caches.
	launched, err := provider.ResolveImageForShape(ctx, cfg, "VM.Standard2.1")
	require.NoError(t, err)
	require.Len(t, launched.Images, 1)

	// Warm: same answer, and still no further API calls.
	getsAfterLaunch := fakeClient.GetImageCount.Get()
	shapesAfterLaunch := fakeClient.ListImageShapeCompatibilityEntriesCount.Get()

	cached, ok := provider.ResolveImageForShapeCached(ctx, cfg, "VM.Standard2.1")

	require.True(t, ok, "what a launch resolved must be readable from cache afterwards")
	require.Len(t, cached.Images, 1)
	assert.Equal(t, *launched.Images[0].Id, *cached.Images[0].Id,
		"the cached path must select exactly what a launch selects")
	assert.Equal(t, getsAfterLaunch, fakeClient.GetImageCount.Get())
	assert.Equal(t, shapesAfterLaunch, fakeClient.ListImageShapeCompatibilityEntriesCount.Get())
}

// A shape no cached image covers is a settled answer, not a miss to go and check.
func TestResolveImageForShapeCached_WarmButNoCompatibleImage(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	fakeClient.GetImageResp = ocicore.GetImageResponse{
		Image: ocicore.Image{
			Id:              lo.ToPtr("ocid1.image.arm"),
			DisplayName:     lo.ToPtr("arm-image"),
			TimeCreated:     &common.SDKTime{Time: time.Now()},
			OperatingSystem: lo.ToPtr("Oracle Linux"),
		},
	}
	fakeClient.OnListImageShapeCompatibilityEntries = func(context.Context,
		ocicore.ListImageShapeCompatibilityEntriesRequest) (ocicore.ListImageShapeCompatibilityEntriesResponse,
		error) {
		return ocicore.ListImageShapeCompatibilityEntriesResponse{
			Items: []ocicore.ImageShapeCompatibilitySummary{{Shape: lo.ToPtr("VM.Standard.A1.Flex")}},
		}, nil
	}

	cfg := &v1beta1.ImageConfig{ImageId: lo.ToPtr("ocid1.image.arm")}

	// Warm both caches for this configuration via a real resolution of the ARM shape.
	_, err := provider.ResolveImageForShape(ctx, cfg, "VM.Standard.A1.Flex")
	require.NoError(t, err)

	before := fakeClient.ListImageShapeCompatibilityEntriesCount.Get()

	_, ok := provider.ResolveImageForShapeCached(ctx, cfg, "VM.Standard.E5.Flex")

	assert.False(t, ok, "cached and incompatible is still no answer for the caller")
	assert.Equal(t, before, fakeClient.ListImageShapeCompatibilityEntriesCount.Get(),
		"and it must not go asking")
}

// Selection drops images whose kubelet version cannot be read, and reading it can mean walking the
// base-image chain. If part of that chain is uncached, the cached-only pass would see a shorter
// candidate list than a launch does and could pick a different image. It must abandon instead:
// no answer is safe, a different answer is not.
func TestResolveImageForShapeCached_PartialChainDeclinesRatherThanReselects(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)
	provider.k8sVersion = semver.New("1.30.0")

	filter := v1beta1.ImageSelectorTerm{OsFilter: "Oracle Linux"}
	cfg := &v1beta1.ImageConfig{ImageType: v1beta1.OKEImage, ImageFilter: &filter}

	// Newest image carries no kubelet tag, so its version can only come from its base image.
	untagged := &ocicore.Image{
		Id: lo.ToPtr("ocid1.image.untagged"), DisplayName: lo.ToPtr("untagged"),
		TimeCreated:     &common.SDKTime{Time: time.Now()},
		OperatingSystem: lo.ToPtr("Oracle Linux"), BaseImageId: lo.ToPtr("ocid1.image.base"),
	}
	tagged := &ocicore.Image{
		Id: lo.ToPtr("ocid1.image.tagged"), DisplayName: lo.ToPtr("tagged"),
		TimeCreated:     &common.SDKTime{Time: time.Now().Add(-time.Hour)},
		OperatingSystem: lo.ToPtr("Oracle Linux"),
		FreeformTags:    map[string]string{"k8s_version": "1.30.0"},
	}

	// The filter result is cached, but the base image it depends on is not.
	key, err := utils.HashFor(filter)
	require.NoError(t, err)
	provider.imageFilterCache.Set(key, []*ocicore.Image{untagged, tagged})

	// The older image is fully cached and compatible, so if the pass were to skip the one it
	// cannot read it would happily return this instead - a different image than a launch picks.
	provider.imageShapeCache.Set("ocid1.image.tagged", set.New("VM.Standard2.1"))

	resolved, ok := provider.ResolveImageForShapeCached(ctx, cfg, "VM.Standard2.1")

	assert.False(t, ok, "an incomplete chain must yield no answer, not a re-selected one")
	assert.Nil(t, resolved)
	assert.Equal(t, 0, fakeClient.GetImageCount.Get(), "and it must not fetch the missing link")
}

// The realistic partial state: the NodeClass reconciler refreshes image lookups every few minutes,
// but shape compatibility is only cached by an actual launch. So the image is very often cached
// while its shape compatibility is not, and that must still produce no answer and no API call.
func TestResolveImageForShapeCached_ImageCachedButShapeNot(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	provider.imageOcidCache.Set("ocid1.image.123", &ocicore.Image{
		Id: lo.ToPtr("ocid1.image.123"), DisplayName: lo.ToPtr("test-image"),
		TimeCreated:     &common.SDKTime{Time: time.Now()},
		OperatingSystem: lo.ToPtr("Oracle Linux"),
	})

	cfg := &v1beta1.ImageConfig{ImageId: lo.ToPtr("ocid1.image.123")}

	_, ok := provider.ResolveImageForShapeCached(ctx, cfg, "VM.Standard2.1")

	assert.False(t, ok, "the image is known but its shape compatibility is not")
	assert.Equal(t, 0, fakeClient.ListImageShapeCompatibilityEntriesCount.Get(),
		"and compatibility must not be fetched to find out")
	assert.Equal(t, 0, fakeClient.GetImageCount.Get())
}

// With both halves warm, the answer comes back with no API call at all.
func TestResolveImageForShapeCached_BothHalvesWarm(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	provider.imageOcidCache.Set("ocid1.image.123", &ocicore.Image{
		Id: lo.ToPtr("ocid1.image.123"), DisplayName: lo.ToPtr("test-image"),
		TimeCreated:     &common.SDKTime{Time: time.Now()},
		OperatingSystem: lo.ToPtr("Oracle Linux"),
	})
	provider.imageShapeCache.Set("ocid1.image.123", set.New("VM.Standard2.1"))

	resolved, ok := provider.ResolveImageForShapeCached(ctx,
		&v1beta1.ImageConfig{ImageId: lo.ToPtr("ocid1.image.123")}, "VM.Standard2.1")

	require.True(t, ok)
	require.Len(t, resolved.Images, 1)
	assert.Equal(t, "ocid1.image.123", *resolved.Images[0].Id)
	assert.Equal(t, 0, fakeClient.GetImageCount.Get())
	assert.Equal(t, 0, fakeClient.ListImageShapeCompatibilityEntriesCount.Get())
}

// The filter half of the configuration needs the same treatment as the ocid half: an uncached
// filter must decline rather than go and list images.
func TestResolveImageForShapeCached_UncachedFilterDeclines(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	cfg := &v1beta1.ImageConfig{
		ImageFilter: &v1beta1.ImageSelectorTerm{OsFilter: "Oracle Linux"},
	}

	_, ok := provider.ResolveImageForShapeCached(ctx, cfg, "VM.Standard2.1")

	assert.False(t, ok)
	assert.Equal(t, 0, fakeClient.ListImagesCount.Get(), "no image listing may be issued")
}

// The equivalence that matters is on the full selection path, not the trivial one: a filter that
// returns several images, kubelet-version scoring against the cluster, and a base-image chain
// walked more than one level deep. Whatever a launch picks out of that, the cached pass must pick
// the same - it is the key a measurement gets filed under.
func TestResolveImageForShapeCached_AgreesWithLaunchOnTheFullPath(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)
	provider.k8sVersion = semver.New("1.30.0")

	now := time.Now()
	// Two levels from the image that carries the version tag, and deliberately NOT the newest:
	// selection must be driven by version skew, so recency alone would pick the wrong one.
	grandchild := ocicore.Image{
		Id: lo.ToPtr("ocid1.image.grandchild"), DisplayName: lo.ToPtr("grandchild"),
		TimeCreated:     &common.SDKTime{Time: now.Add(-time.Minute)},
		OperatingSystem: lo.ToPtr("Oracle Linux"), CompartmentId: lo.ToPtr("prebaked-comp"),
		BaseImageId: lo.ToPtr("ocid1.image.child"),
	}
	child := ocicore.Image{
		Id: lo.ToPtr("ocid1.image.child"), DisplayName: lo.ToPtr("child"),
		TimeCreated:     &common.SDKTime{Time: now.Add(-time.Hour)},
		OperatingSystem: lo.ToPtr("Oracle Linux"), CompartmentId: lo.ToPtr("prebaked-comp"),
		BaseImageId: lo.ToPtr("ocid1.image.root"),
	}
	root := ocicore.Image{
		Id: lo.ToPtr("ocid1.image.root"), DisplayName: lo.ToPtr("root"),
		TimeCreated:     &common.SDKTime{Time: now.Add(-2 * time.Hour)},
		OperatingSystem: lo.ToPtr("Oracle Linux"), CompartmentId: lo.ToPtr("prebaked-comp"),
		FreeformTags: map[string]string{"k8s_version": "1.30.0"},
	}
	// Newer, directly tagged, but three minor versions behind the cluster - so it scores worse and
	// must lose, even though it sorts first by creation time.
	newerButWorseScoring := ocicore.Image{
		Id: lo.ToPtr("ocid1.image.newer"), DisplayName: lo.ToPtr("newer"),
		TimeCreated:     &common.SDKTime{Time: now},
		OperatingSystem: lo.ToPtr("Oracle Linux"), CompartmentId: lo.ToPtr("prebaked-comp"),
		FreeformTags: map[string]string{"k8s_version": "1.27.0"},
	}

	fakeClient.ListImagesResp = ocicore.ListImagesResponse{
		Items: []ocicore.Image{newerButWorseScoring, grandchild},
	}
	fakeClient.OnGetImage = func(_ context.Context,
		req ocicore.GetImageRequest) (ocicore.GetImageResponse, error) {
		switch *req.ImageId {
		case "ocid1.image.child":
			return ocicore.GetImageResponse{Image: child}, nil
		case "ocid1.image.root":
			return ocicore.GetImageResponse{Image: root}, nil
		}
		return ocicore.GetImageResponse{}, errors.New("unexpected image")
	}
	fakeClient.OnListImageShapeCompatibilityEntries = func(context.Context,
		ocicore.ListImageShapeCompatibilityEntriesRequest) (ocicore.ListImageShapeCompatibilityEntriesResponse,
		error) {
		return ocicore.ListImageShapeCompatibilityEntriesResponse{
			Items: []ocicore.ImageShapeCompatibilitySummary{{Shape: lo.ToPtr("VM.Standard2.1")}},
		}, nil
	}

	cfg := &v1beta1.ImageConfig{
		ImageType:   v1beta1.OKEImage,
		ImageFilter: &v1beta1.ImageSelectorTerm{OsFilter: "Oracle Linux"},
	}

	// What a launch selects, warming every cache it touches on the way.
	launched, err := provider.ResolveImageForShape(ctx, cfg, "VM.Standard2.1")
	require.NoError(t, err)
	require.Len(t, launched.Images, 1)

	gets := fakeClient.GetImageCount.Get()
	lists := fakeClient.ListImagesCount.Get()
	shapes := fakeClient.ListImageShapeCompatibilityEntriesCount.Get()

	cached, ok := provider.ResolveImageForShapeCached(ctx, cfg, "VM.Standard2.1")

	// The launch must have chosen on skew, not recency - otherwise the equivalence below is vacuous.
	require.Equal(t, "ocid1.image.grandchild", *launched.Images[0].Id,
		"the chain-resolved image scores best, so it wins despite not being newest")

	require.True(t, ok, "everything a launch touched is now cached")
	require.Len(t, cached.Images, 1)
	assert.Equal(t, "ocid1.image.grandchild", *cached.Images[0].Id,
		"filter ordering, kubelet scoring and the base-image chain must all reach the same image")
	assert.Equal(t, gets, fakeClient.GetImageCount.Get(), "and none of it may be refetched")
	assert.Equal(t, lists, fakeClient.ListImagesCount.Get())
	assert.Equal(t, shapes, fakeClient.ListImageShapeCompatibilityEntriesCount.Get())
}

// The base-image walk recurses, so cached-only has to hold all the way down. Cache the first link
// but not the second: if the recursive call dropped the flag it would fetch the missing root,
// succeed, and hand back an image the pass had no right to resolve.
func TestResolveImageForShapeCached_DeepChainStaysCachedOnly(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)
	provider.k8sVersion = semver.New("1.30.0")

	now := time.Now()
	grandchild := &ocicore.Image{
		Id: lo.ToPtr("ocid1.image.grandchild"), DisplayName: lo.ToPtr("grandchild"),
		TimeCreated:     &common.SDKTime{Time: now},
		OperatingSystem: lo.ToPtr("Oracle Linux"), CompartmentId: lo.ToPtr("prebaked-comp"),
		BaseImageId: lo.ToPtr("ocid1.image.child"),
	}
	child := &ocicore.Image{
		Id: lo.ToPtr("ocid1.image.child"), DisplayName: lo.ToPtr("child"),
		TimeCreated:     &common.SDKTime{Time: now.Add(-time.Hour)},
		OperatingSystem: lo.ToPtr("Oracle Linux"), CompartmentId: lo.ToPtr("prebaked-comp"),
		BaseImageId: lo.ToPtr("ocid1.image.root"),
	}
	// root carries the version tag but is deliberately never cached.
	fakeClient.OnGetImage = func(_ context.Context,
		_ ocicore.GetImageRequest) (ocicore.GetImageResponse, error) {
		return ocicore.GetImageResponse{Image: ocicore.Image{
			Id: lo.ToPtr("ocid1.image.root"), DisplayName: lo.ToPtr("root"),
			TimeCreated:     &common.SDKTime{Time: now.Add(-2 * time.Hour)},
			OperatingSystem: lo.ToPtr("Oracle Linux"),
			FreeformTags:    map[string]string{"k8s_version": "1.30.0"},
		}}, nil
	}

	filter := v1beta1.ImageSelectorTerm{OsFilter: "Oracle Linux"}
	key, err := utils.HashFor(filter)
	require.NoError(t, err)
	provider.imageFilterCache.Set(key, []*ocicore.Image{grandchild})
	provider.imageOcidCache.Set("ocid1.image.child", child) // first link cached, root is not
	provider.imageShapeCache.Set("ocid1.image.grandchild", set.New("VM.Standard2.1"))

	resolved, ok := provider.ResolveImageForShapeCached(ctx,
		&v1beta1.ImageConfig{ImageType: v1beta1.OKEImage, ImageFilter: &filter}, "VM.Standard2.1")

	assert.False(t, ok, "the chain is incomplete, so there is no answer to give")
	assert.Nil(t, resolved)
	assert.Equal(t, 0, fakeClient.GetImageCount.Get(),
		"the uncached link must not be fetched to complete the walk")
}

// Compatibility is checked candidate by candidate in rank order, so an unknown answer for an
// earlier candidate is not the same as a negative one: the earlier candidate might well be the one
// a launch picks. Falling through to a later candidate would hand back an image the launch would
// not have chosen, and discovery would key measurements against it. Decline instead.
func TestResolveImageForShapeCached_UnknownCompatibilityDoesNotPromoteALaterCandidate(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)

	now := time.Now()
	// Ranked first by recency, and its shape compatibility is deliberately not cached.
	preferred := &ocicore.Image{
		Id: lo.ToPtr("ocid1.image.preferred"), DisplayName: lo.ToPtr("preferred"),
		TimeCreated: &common.SDKTime{Time: now}, OperatingSystem: lo.ToPtr("Oracle Linux"),
	}
	// Ranked second, fully cached, and compatible - the candidate a fall-through would promote.
	runnerUp := &ocicore.Image{
		Id: lo.ToPtr("ocid1.image.runner-up"), DisplayName: lo.ToPtr("runner-up"),
		TimeCreated: &common.SDKTime{Time: now.Add(-time.Hour)}, OperatingSystem: lo.ToPtr("Oracle Linux"),
	}

	filter := v1beta1.ImageSelectorTerm{OsFilter: "Oracle Linux"}
	key, err := utils.HashFor(filter)
	require.NoError(t, err)
	provider.imageFilterCache.Set(key, []*ocicore.Image{preferred, runnerUp})
	provider.imageShapeCache.Set("ocid1.image.runner-up", set.New("VM.Standard2.1"))

	resolved, ok := provider.ResolveImageForShapeCached(ctx,
		&v1beta1.ImageConfig{ImageType: v1beta1.Custom, ImageFilter: &filter}, "VM.Standard2.1")

	assert.False(t, ok,
		"compatibility for the leading candidate is unknown, so there is no answer to give")
	assert.Nil(t, resolved)
	assert.Equal(t, 0, fakeClient.ListImageShapeCompatibilityEntriesCount.Get(),
		"and it must not go asking to break the tie")
}

// "Missing" and "expired" are the same to the caller, but only expiry exercises the TTL. Expire
// each half on its own: resolution short-circuits at the first thing it cannot find, so expiring
// both together would never prove that expired compatibility declines.
func TestResolveImageForShapeCached_ExpiredInformationDeclines(t *testing.T) {
	const imageID = "ocid1.image.123"

	newProvider := func(t *testing.T) (*DefaultProvider, *fakes.FakeCompute) {
		t.Helper()

		fakeClient := &fakes.FakeCompute{}
		startCh := make(chan struct{})
		close(startCh)
		provider, _ := NewProvider(context.Background(), nil, fakeClient,
			"prebaked-comp", "cio-comp", startCh)

		return provider, fakeClient
	}
	warmImage := func(p *DefaultProvider) {
		p.imageOcidCache.Set(imageID, &ocicore.Image{
			Id: lo.ToPtr(imageID), DisplayName: lo.ToPtr("test-image"),
			TimeCreated: &common.SDKTime{Time: time.Now()}, OperatingSystem: lo.ToPtr("Oracle Linux"),
		})
	}
	cfg := &v1beta1.ImageConfig{ImageId: lo.ToPtr(imageID)}

	t.Run("the image lookup expires", func(t *testing.T) {
		provider, fakeClient := newProvider(t)
		provider.imageOcidCache = cache.NewGetOrLoadCache[*ocicore.Image](20*time.Millisecond, time.Minute)
		warmImage(provider)
		// Compatibility stays live, so only the image half can be what declines.
		provider.imageShapeCache.Set(imageID, set.New("VM.Standard2.1"))

		_, ok := provider.ResolveImageForShapeCached(context.Background(), cfg, "VM.Standard2.1")
		require.True(t, ok, "warm to begin with")

		time.Sleep(40 * time.Millisecond)

		_, ok = provider.ResolveImageForShapeCached(context.Background(), cfg, "VM.Standard2.1")

		assert.False(t, ok, "an expired image lookup is as good as absent")
		assert.Equal(t, 0, fakeClient.GetImageCount.Get(), "and must not be refreshed from here")
	})

	t.Run("the compatibility lookup expires", func(t *testing.T) {
		provider, fakeClient := newProvider(t)
		provider.imageShapeCache = cache.NewGetOrLoadCache[set.Set[string]](20*time.Millisecond, time.Minute)
		// The image stays live, so only compatibility can be what declines.
		warmImage(provider)
		provider.imageShapeCache.Set(imageID, set.New("VM.Standard2.1"))

		_, ok := provider.ResolveImageForShapeCached(context.Background(), cfg, "VM.Standard2.1")
		require.True(t, ok, "warm to begin with")

		time.Sleep(40 * time.Millisecond)

		_, ok = provider.ResolveImageForShapeCached(context.Background(), cfg, "VM.Standard2.1")

		assert.False(t, ok, "expired compatibility is as good as absent")
		assert.Equal(t, 0, fakeClient.ListImageShapeCompatibilityEntriesCount.Get(),
			"and must not be refreshed from here")
	})
}
