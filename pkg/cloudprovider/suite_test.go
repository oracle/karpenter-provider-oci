/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package cloudprovider

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ocioraclecouldcomv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	npnv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/npn/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/operator/options"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/blockstorage"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/capacityreservation"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/image"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instance"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancetype"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/kms"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/npn"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/placement"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	v1 "k8s.io/api/core/v1"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	ctx       context.Context
	cancel    context.CancelFunc
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
)

func TestCloudProvider(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "CloudProvider Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())
	ctx = options.ToContext(ctx, &options.Options{})

	var err error
	err = ocioraclecouldcomv1beta1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "apis", "crds")},
		ErrorIfCRDPathMissing: true,
	}

	// Retrieve the first found binary directory to allow running tests from IDEs
	if getFirstFoundEnvTestBinaryDir() != "" {
		testEnv.BinaryAssetsDirectory = getFirstFoundEnvTestBinaryDir()
	}

	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

// getFirstFoundEnvTestBinaryDir locates the first binary in the specified path.
// ENVTEST-based tests depend on specific binaries, usually located in paths set by
// controller-runtime. When running tests directly (e.g., via an IDE) without using
// Makefile targets, the 'BinaryAssetsDirectory' must be explicitly configured.
//
// This function streamlines the process by finding the required binaries, similar to
// setting the 'KUBEBUILDER_ASSETS' environment variable. To ensure the binaries are
// properly set up, run 'make setup-envtest' beforehand.
func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		logf.Log.Error(err, "Failed to read directory", "path", basePath)
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}

// Mock for InstanceProvider
type FakeInstanceProvider struct {
	TestInstance     *instance.InstanceInfo
	LaunchInstanceFn func(context.Context, *corev1.NodeClaim, *ocioraclecouldcomv1beta1.OCINodeClass,
		*instancetype.OciInstanceType, *image.ImageResolveResult, *network.NetworkResolveResult,
		*kms.KmsKeyResolveResult, *placement.Proposal) (*instance.InstanceInfo, error)
	DeleteInstanceFn                    func(context.Context, string) error
	GetInstanceFn                       func(context.Context, string) (*instance.InstanceInfo, error)
	GetInstanceCachedFn                 func(context.Context, string) (*instance.InstanceInfo, error)
	GetInstanceCompartmentFn            func(*ocioraclecouldcomv1beta1.OCINodeClass) string
	ListInstancesFn                     func(context.Context, string) ([]*ocicore.Instance, error)
	ListInstanceBootVolumeAttachmentsFn func(context.Context, string, string, string) ([]*ocicore.BootVolumeAttachment,
		error)
	ListInstanceBootVolumeAttachmentsCFn func(context.Context, string, string, string) ([]*ocicore.BootVolumeAttachment,
		error)
	ListInstanceVnicAttachmentsFn       func(context.Context, string, string) ([]*ocicore.VnicAttachment, error)
	ListInstanceVnicAttachmentsCachedFn func(context.Context, string, string) ([]*ocicore.VnicAttachment, error)
}

func NewFakeInstanceProvider(inputInstance *instance.InstanceInfo) *FakeInstanceProvider {
	return &FakeInstanceProvider{
		TestInstance: inputInstance,
	}
}

func (ip *FakeInstanceProvider) LaunchInstance(ctx context.Context,
	nodeClaim *corev1.NodeClaim,
	nodeClass *ocioraclecouldcomv1beta1.OCINodeClass,
	instanceType *instancetype.OciInstanceType,
	imageResolveResult *image.ImageResolveResult,
	networkResolveResult *network.NetworkResolveResult,
	kmsKeyResolveResult *kms.KmsKeyResolveResult,
	placementProposal *placement.Proposal) (*instance.InstanceInfo, error) {
	if ip.LaunchInstanceFn != nil {
		return ip.LaunchInstanceFn(ctx, nodeClaim, nodeClass, instanceType, imageResolveResult,
			networkResolveResult, kmsKeyResolveResult, placementProposal)
	}
	return ip.TestInstance, nil
}

func (ip *FakeInstanceProvider) DeleteInstance(ctx context.Context, instanceOcid string) error {
	if ip.DeleteInstanceFn != nil {
		return ip.DeleteInstanceFn(ctx, instanceOcid)
	}
	return nil
}

func (ip *FakeInstanceProvider) GetInstance(ctx context.Context,
	instanceOcid string) (*instance.InstanceInfo, error) {
	if ip.GetInstanceFn != nil {
		return ip.GetInstanceFn(ctx, instanceOcid)
	}
	return &instance.InstanceInfo{}, nil
}

func (ip *FakeInstanceProvider) GetInstanceCached(ctx context.Context,
	instanceOcid string) (*instance.InstanceInfo, error) {
	if ip.GetInstanceCachedFn != nil {
		return ip.GetInstanceCachedFn(ctx, instanceOcid)
	}
	return ip.GetInstance(ctx, instanceOcid)
}

func (ip *FakeInstanceProvider) GetInstanceCompartment(nodeClass *ocioraclecouldcomv1beta1.OCINodeClass) string {
	if ip.GetInstanceCompartmentFn != nil {
		return ip.GetInstanceCompartmentFn(nodeClass)
	}
	return ""
}

func (ip *FakeInstanceProvider) ListInstances(ctx context.Context,
	compartmentId string) ([]*ocicore.Instance, error) {
	if ip.ListInstancesFn != nil {
		return ip.ListInstancesFn(ctx, compartmentId)
	}
	return []*ocicore.Instance{}, nil
}

func (ip *FakeInstanceProvider) ListInstanceBootVolumeAttachments(ctx context.Context,
	compartmentOcid string, instanceOcid string, ad string) ([]*ocicore.BootVolumeAttachment, error) {
	if ip.ListInstanceBootVolumeAttachmentsFn != nil {
		return ip.ListInstanceBootVolumeAttachmentsFn(ctx, compartmentOcid, instanceOcid, ad)
	}
	return []*ocicore.BootVolumeAttachment{}, nil
}

func (ip *FakeInstanceProvider) ListInstanceBootVolumeAttachmentsCached(ctx context.Context,
	compartmentOcid, instanceOcid, ad string) ([]*ocicore.BootVolumeAttachment, error) {
	if ip.ListInstanceBootVolumeAttachmentsCFn != nil {
		return ip.ListInstanceBootVolumeAttachmentsCFn(ctx, compartmentOcid, instanceOcid, ad)
	}
	return ip.ListInstanceBootVolumeAttachments(ctx, compartmentOcid, instanceOcid, ad)
}

func (ip *FakeInstanceProvider) ListInstanceVnicAttachments(ctx context.Context,
	compartmentOcid string, instanceOcid string) ([]*ocicore.VnicAttachment, error) {
	if ip.ListInstanceVnicAttachmentsFn != nil {
		return ip.ListInstanceVnicAttachmentsFn(ctx, compartmentOcid, instanceOcid)
	}
	return []*ocicore.VnicAttachment{}, nil
}

func (ip *FakeInstanceProvider) ListInstanceVnicAttachmentsCached(ctx context.Context,
	compartmentOcid, instanceOcid string) ([]*ocicore.VnicAttachment, error) {
	if ip.ListInstanceVnicAttachmentsCachedFn != nil {
		return ip.ListInstanceVnicAttachmentsCachedFn(ctx, compartmentOcid, instanceOcid)
	}
	return ip.ListInstanceVnicAttachments(ctx, compartmentOcid, instanceOcid)
}

// Mock for InstanceTypeProvider
type FakeInstanceTypeProvider struct {
	InstancesTypes []*instancetype.OciInstanceType
	Err            error
	ListFn         func(context.Context, *ocioraclecouldcomv1beta1.OCINodeClass,
		[]v1.Taint) ([]*instancetype.OciInstanceType, error)
}

func NewFakeInstanceTypeProvider(testInstanceTypes []*instancetype.OciInstanceType) *FakeInstanceTypeProvider {
	return &FakeInstanceTypeProvider{
		InstancesTypes: testInstanceTypes,
	}
}

func (itp *FakeInstanceTypeProvider) ListInstanceTypes(ctx context.Context,
	nodeClass *ocioraclecouldcomv1beta1.OCINodeClass, taints []v1.Taint) ([]*instancetype.OciInstanceType, error) {
	if itp.ListFn != nil {
		return itp.ListFn(ctx, nodeClass, taints)
	}
	if itp.Err != nil {
		return nil, itp.Err
	}
	return itp.InstancesTypes, nil
}

type FakeImageProvider struct {
	ResolveImagesFn        func(context.Context, *ocioraclecouldcomv1beta1.ImageConfig) (*image.ImageResolveResult, error)
	ResolveImageForShapeFn func(context.Context,
		*ocioraclecouldcomv1beta1.ImageConfig, string) (*image.ImageResolveResult, error)
}

func (f *FakeImageProvider) ResolveImages(ctx context.Context,
	imageCfg *ocioraclecouldcomv1beta1.ImageConfig) (*image.ImageResolveResult, error) {
	if f.ResolveImagesFn != nil {
		return f.ResolveImagesFn(ctx, imageCfg)
	}
	return nil, nil
}

func (f *FakeImageProvider) ResolveImageForShape(ctx context.Context,
	imageCfg *ocioraclecouldcomv1beta1.ImageConfig, shape string) (*image.ImageResolveResult, error) {
	if f.ResolveImageForShapeFn != nil {
		return f.ResolveImageForShapeFn(ctx, imageCfg, shape)
	}
	return nil, nil
}

type FakeNetworkProvider struct {
	ResolveNetworkConfigFn func(context.Context,
		*ocioraclecouldcomv1beta1.NetworkConfig) (*network.NetworkResolveResult, error)
	GetVnicFn       func(context.Context, string) (*ocicore.Vnic, error)
	GetVnicCachedFn func(context.Context, string) (*ocicore.Vnic, error)
}

func (f *FakeNetworkProvider) ResolveNetworkConfig(ctx context.Context,
	cfg *ocioraclecouldcomv1beta1.NetworkConfig) (*network.NetworkResolveResult, error) {
	if f.ResolveNetworkConfigFn != nil {
		return f.ResolveNetworkConfigFn(ctx, cfg)
	}
	return &network.NetworkResolveResult{}, nil
}

func (f *FakeNetworkProvider) GetVnic(ctx context.Context, vnicOcid string) (*ocicore.Vnic, error) {
	if f.GetVnicFn != nil {
		return f.GetVnicFn(ctx, vnicOcid)
	}
	return &ocicore.Vnic{}, nil
}

func (f *FakeNetworkProvider) GetVnicCached(ctx context.Context, vnicOcid string) (*ocicore.Vnic, error) {
	if f.GetVnicCachedFn != nil {
		return f.GetVnicCachedFn(ctx, vnicOcid)
	}
	return f.GetVnic(ctx, vnicOcid)
}

type FakeKmsProvider struct {
	ResolveKmsKeyConfigFn func(context.Context, *ocioraclecouldcomv1beta1.KmsKeyConfig) (*kms.KmsKeyResolveResult, error)
}

func (f *FakeKmsProvider) ResolveKmsKeyConfig(ctx context.Context,
	cfg *ocioraclecouldcomv1beta1.KmsKeyConfig) (*kms.KmsKeyResolveResult, error) {
	if f.ResolveKmsKeyConfigFn != nil {
		return f.ResolveKmsKeyConfigFn(ctx, cfg)
	}
	return nil, nil
}

type FakePlacementProvider struct {
	PlaceInstanceFn func(context.Context, *corev1.NodeClaim, *ocioraclecouldcomv1beta1.OCINodeClass,
		*instancetype.OciInstanceType, func(*placement.Proposal) error) error
	InstanceFoundFn  func(string, *ocicore.Instance)
	InstanceForgetFn func(string, string)
}

func (f *FakePlacementProvider) PlaceInstance(ctx context.Context, claim *corev1.NodeClaim,
	nodeClass *ocioraclecouldcomv1beta1.OCINodeClass, instanceType *instancetype.OciInstanceType,
	placeFunc func(*placement.Proposal) error) error {
	if f.PlaceInstanceFn != nil {
		return f.PlaceInstanceFn(ctx, claim, nodeClass, instanceType, placeFunc)
	}
	return placeFunc(&placement.Proposal{})
}

func (f *FakePlacementProvider) InstanceFound(nodePool string, instance *ocicore.Instance) {
	if f.InstanceFoundFn != nil {
		f.InstanceFoundFn(nodePool, instance)
	}
}

func (f *FakePlacementProvider) InstanceForget(nodePool string, instanceID string) {
	if f.InstanceForgetFn != nil {
		f.InstanceForgetFn(nodePool, instanceID)
	}
}

type FakeCapacityReservationProvider struct {
	ResolveFn func(context.Context,
		[]*ocioraclecouldcomv1beta1.CapacityReservationConfig) ([]capacityreservation.ResolveResult, error)
	MarkUsedFn     func(*ocicore.Instance)
	MarkReleasedFn func(*ocicore.Instance)
	SyncFn         func(context.Context, string) error
}

func (f *FakeCapacityReservationProvider) ResolveCapacityReservations(ctx context.Context,
	cfgs []*ocioraclecouldcomv1beta1.CapacityReservationConfig) ([]capacityreservation.ResolveResult, error) {
	if f.ResolveFn != nil {
		return f.ResolveFn(ctx, cfgs)
	}
	return nil, nil
}

func (f *FakeCapacityReservationProvider) MarkCapacityReservationUsed(instance *ocicore.Instance) {
	if f.MarkUsedFn != nil {
		f.MarkUsedFn(instance)
	}
}

func (f *FakeCapacityReservationProvider) MarkCapacityReservationReleased(instance *ocicore.Instance) {
	if f.MarkReleasedFn != nil {
		f.MarkReleasedFn(instance)
	}
}

func (f *FakeCapacityReservationProvider) SyncCapacityReservation(ctx context.Context, id string) error {
	if f.SyncFn != nil {
		return f.SyncFn(ctx, id)
	}
	return nil
}

type FakeBlockStorageProvider struct {
	GetBootVolumeFn       func(context.Context, string) (*ocicore.BootVolume, error)
	GetBootVolumeCachedFn func(context.Context, string) (*ocicore.BootVolume, error)
}

func (f *FakeBlockStorageProvider) GetBootVolume(ctx context.Context, ocid string) (*ocicore.BootVolume, error) {
	if f.GetBootVolumeFn != nil {
		return f.GetBootVolumeFn(ctx, ocid)
	}
	return &ocicore.BootVolume{}, nil
}

func (f *FakeBlockStorageProvider) GetBootVolumeCached(ctx context.Context, ocid string) (*ocicore.BootVolume, error) {
	if f.GetBootVolumeCachedFn != nil {
		return f.GetBootVolumeCachedFn(ctx, ocid)
	}
	return f.GetBootVolume(ctx, ocid)
}

type FakeNpnProvider struct {
	NpnClusterFn      func() bool
	CreateCustomObjFn func(context.Context, *ocicore.Instance, *ocioraclecouldcomv1beta1.OCINodeClass,
		*network.NetworkResolveResult) (*npnv1beta1.NativePodNetwork, error)
}

func (f *FakeNpnProvider) CreateNpnCustomObject(ctx context.Context, inst *ocicore.Instance,
	nodeClass *ocioraclecouldcomv1beta1.OCINodeClass,
	resolveResult *network.NetworkResolveResult) (*npnv1beta1.NativePodNetwork, error) {
	if f.CreateCustomObjFn != nil {
		return f.CreateCustomObjFn(ctx, inst, nodeClass, resolveResult)
	}
	return &npnv1beta1.NativePodNetwork{}, nil
}

func (f *FakeNpnProvider) NpnCluster() bool {
	if f.NpnClusterFn != nil {
		return f.NpnClusterFn()
	}
	return false
}

var (
	_ instance.Provider            = (*FakeInstanceProvider)(nil)
	_ instancetype.Provider        = (*FakeInstanceTypeProvider)(nil)
	_ image.Provider               = (*FakeImageProvider)(nil)
	_ network.Provider             = (*FakeNetworkProvider)(nil)
	_ kms.Provider                 = (*FakeKmsProvider)(nil)
	_ placement.Provider           = (*FakePlacementProvider)(nil)
	_ capacityreservation.Provider = (*FakeCapacityReservationProvider)(nil)
	_ blockstorage.Provider        = (*FakeBlockStorageProvider)(nil)
	_ npn.Provider                 = (*FakeNpnProvider)(nil)
)
