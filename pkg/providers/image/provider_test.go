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
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	"github.com/oracle/oci-go-sdk/v65/common"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"k8s.io/utils/set"
)

func TestNewProvider(t *testing.T) {
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh) // Close immediately to prevent refresh goroutine

	provider, err := NewProvider(context.Background(), nil, fakeClient, "prebaked-comp", "cio-comp", startCh, false, 0)

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
			provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh, false, 0)

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
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh, false, 0)

	// Setup shape compatibility response
	fakeClient.ListImageShapeCompatibilityEntriesResp = ocicore.ListImageShapeCompatibilityEntriesResponse{
		Items: []ocicore.ImageShapeCompatibilitySummary{
			{Shape: lo.ToPtr("VM.Standard2.1")},
			{Shape: lo.ToPtr("VM.Standard2.2")},
		},
	}

	shapes, err := provider.listShapesForImage(ctx, "ocid1.image.123")

	assert.NoError(t, err)
	assert.Equal(t, set.New("VM.Standard2.1", "VM.Standard2.2"), shapes)
	assert.Equal(t, 1, fakeClient.ListImageShapeCompatibilityEntriesCount.Get())
}

func TestResolveImageForShape(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh, false, 0)

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
			provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh, false, 0)

			images, err := provider.filterImage(ctx, tt.imageType, tt.filter)

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
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh, false, 0)

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
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh, false, 0)

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
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh, false, 0)

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

	filtered, err := provider.filterAndSortImages(ctx, images, imageCfg)

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
			provider, err := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh, false, 0)
			assert.NoError(t, err)

			result, err := provider.extractKubeletVersionFromPreBakedImage(ctx, tt.image)

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
	provider, err := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh, false, 0)
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

	got, err := provider.extractKubeletVersionFromPreBakedImage(ctx, child)
	assert.NoError(t, err)
	assert.Equal(t, &semver.Version{Major: 1, Minor: 26, Patch: 3}, got)
}

func TestExtractKubeletVersionFromPreBakedImage_DeepChain(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, err := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh, false, 0)
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

	got, err := provider.extractKubeletVersionFromPreBakedImage(ctx, child)
	assert.NoError(t, err)
	assert.Equal(t, &semver.Version{Major: 1, Minor: 27, Patch: 4}, got)
}

func TestExtractKubeletVersionFromPreBakedImage_NotFoundInChain(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, err := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh, false, 0)
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

	got, err := provider.extractKubeletVersionFromPreBakedImage(ctx, child)
	assert.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "missing k8s_version tag")
}

func TestGetImageCaching(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh, false, 0)

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
	image1, err := provider.getImage(ctx, "ocid1.image.123")
	assert.NoError(t, err)
	assert.Equal(t, "ocid1.image.123", *image1.Id)
	assert.Equal(t, 1, fakeClient.GetImageCount.Get())

	// Second call with same OCID should hit cache
	image2, err := provider.getImage(ctx, "ocid1.image.123")
	assert.NoError(t, err)
	assert.Equal(t, "ocid1.image.123", *image2.Id)
	assert.Equal(t, 1, fakeClient.GetImageCount.Get()) // Should still be 1
}

func TestListShapesForImageCaching(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh, false, 0)

	fakeClient.ListImageShapeCompatibilityEntriesResp = ocicore.ListImageShapeCompatibilityEntriesResponse{
		Items: []ocicore.ImageShapeCompatibilitySummary{
			{Shape: lo.ToPtr("VM.Standard2.1")},
		},
	}

	// First call
	shapes1, err := provider.listShapesForImage(ctx, "ocid1.image.123")
	assert.NoError(t, err)
	assert.True(t, shapes1.Has("VM.Standard2.1"))
	assert.Equal(t, 1, fakeClient.ListImageShapeCompatibilityEntriesCount.Get())

	// Second call with same OCID should hit cache
	shapes2, err := provider.listShapesForImage(ctx, "ocid1.image.123")
	assert.NoError(t, err)
	assert.True(t, shapes2.Has("VM.Standard2.1"))
	assert.Equal(t, 1, fakeClient.ListImageShapeCompatibilityEntriesCount.Get()) // Should still be 1
}

func TestFilterImageCaching(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh, false, 0)

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
	images1, err := provider.filterImage(ctx, v1beta1.Platform, filter)
	assert.NoError(t, err)
	assert.Len(t, images1, 1)
	assert.Equal(t, 1, fakeClient.ListImagesCount.Get())

	// Second call with identical filter should hit cache
	images2, err := provider.filterImage(ctx, v1beta1.Platform, filter)
	assert.NoError(t, err)
	assert.Len(t, images2, 1)
	assert.Equal(t, 1, fakeClient.ListImagesCount.Get()) // Should still be 1
}

func TestFilterAndSortImages_K8sVersionMissing(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh, false, 0)

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
	_, err := provider.filterAndSortImages(ctx, images, imageCfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot detect cluster version")
}

func TestToImageResolveResult_CioHardened(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh, false, 0)

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
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh, false, 0)

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
	filtered, err := provider.filterAndSortImages(ctx, images, imageCfg)
	assert.NoError(t, err)
	assert.Len(t, filtered, 0) // Image should be filtered out due to missing k8s_version
}

func TestListAndFilterImages_Pagination(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	provider, _ := NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh, false, 0)

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
