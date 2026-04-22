/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package cloudprovider

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/controllers/nodeclasses"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	npnv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/npn/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/blockstorage"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/capacityreservation"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/clusterplacementgroup"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/computecluster"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/identity"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/image"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instance"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancetype"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/kms"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/npn"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/placement"
	"github.com/oracle/karpenter-provider-oci/pkg/utils"
	"github.com/oracle/oci-go-sdk/v65/common"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
)

const (
	nodeClassClusterCompartmentID = "ocid1.compartment.oc1..cluster123"
	testImageID                   = "ocid1.image.oc1..test"
)

var (
	ociTestNodeClass       v1beta1.OCINodeClass
	cloudProvider          *CloudProvider
	ociNodeClassController *nodeclasses.Controller
	testCreatedAt          = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	uniqueNameCounter      uint64
)

var _ = Describe("CloudProvider Integration Tests", func() {
	testShape := "VM.Standard.E4.Flex"

	BeforeEach(func() {
		ipV4Family := []network.IpFamily{network.IPv4}

		ociTestNodeClass = fakes.CreateOciNodeClassWithMinimumReconcilableSetting(nodeClassClusterCompartmentID)
		imageProvider := lo.Must(image.NewProvider(ctx, nil, fakes.NewFakeComputeClient(nodeClassClusterCompartmentID),
			"testPreBakedCompartmentId", "", fakes.NewDummyChannel()))
		kmsProvider := lo.Must(kms.NewProvider(ctx, nodeClassClusterCompartmentID, fakes.NewDummyConfigurationProvider()))
		kmsProvider.SetKmsClient("https://testvalut-management.kms.us-ashburn-1.oraclecloud.com", fakes.NewFakeKmsClient())
		networkProvider := lo.Must(network.NewProvider(ctx, nodeClassClusterCompartmentID,
			false, ipV4Family, fakes.NewFakeVirtualNetworkClient()))
		crProvider := capacityreservation.NewProvider(ctx,
			fakes.NewFakeCapacityReservationClient(nodeClassClusterCompartmentID), nodeClassClusterCompartmentID)
		computeClusterProvider := computecluster.NewProvider(ctx,
			fakes.NewFakeComputeClient(nodeClassClusterCompartmentID), nodeClassClusterCompartmentID)
		identityProvider := lo.Must(identity.NewProvider(ctx, nodeClassClusterCompartmentID, fakes.NewFakeIdentityClient()))
		cpgProvider := clusterplacementgroup.NewProvider(ctx, fakes.NewFakeClusterPlacementGroupClient(
			nodeClassClusterCompartmentID), nodeClassClusterCompartmentID)
		placementProvider := lo.Must(placement.NewProvider(ctx, crProvider, computeClusterProvider,
			cpgProvider, identityProvider))
		npnProvider := lo.Must(npn.NewProvider(ctx, false, ipV4Family))
		instancetypeProvider := NewFakeInstanceTypeProvider([]*instancetype.OciInstanceType{
			testInstanceType(testShape, 1),
		})
		instanceProvider := NewFakeInstanceProvider(&instance.InstanceInfo{
			Instance: &ocicore.Instance{
				Id:                 lo.ToPtr("test-instance-ocid"),
				DisplayName:        lo.ToPtr("test-instance"),
				AvailabilityDomain: lo.ToPtr("aumf:PHX-AD-1"),
				FaultDomain:        lo.ToPtr("fd1"),
				Shape:              lo.ToPtr(testShape),
				TimeCreated:        &common.SDKTime{Time: testCreatedAt},
				SourceDetails: ocicore.InstanceSourceViaImageDetails{
					ImageId: lo.ToPtr("ocid1.image.123"),
				},
			},
		})
		bsProvider := lo.Must(blockstorage.NewProvider(ctx, &fakes.FakeBlockstorage{}))
		ociNodeClassController = lo.Must(nodeclasses.NewController(ctx, k8sClient,
			&fakes.FakeEventRecorder{},
			imageProvider,
			kmsProvider,
			networkProvider,
			crProvider,
			computeClusterProvider,
			identityProvider,
			cpgProvider))
		cloudProvider = lo.Must(New(ctx, k8sClient, instancetypeProvider, imageProvider,
			networkProvider, kmsProvider, instanceProvider, placementProvider,
			crProvider, bsProvider, npnProvider, nil, fakes.NewDummyChannel()))
	})

	It("should create nodeclaim with nodeclass hash", func() {

		nodeClassPtr := &ociTestNodeClass
		ExpectApplied(ctx, k8sClient, nodeClassPtr)
		ExpectObjectReconciled(ctx, k8sClient, ociNodeClassController, nodeClassPtr)
		nodeClaimPtr := &v1.NodeClaim{
			Spec: v1.NodeClaimSpec{
				NodeClassRef: &v1.NodeClassReference{
					Kind:  nodeClassPtr.Kind,
					Group: v1beta1.Group,
					Name:  nodeClassPtr.Name,
				},
				Requirements: []v1.NodeSelectorRequirementWithMinValues{{
					NodeSelectorRequirement: corev1.NodeSelectorRequirement{
						Key:      corev1.LabelInstanceTypeStable,
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{testShape},
					},
				}},
			},
		}

		resultNodeClaim, err := cloudProvider.Create(ctx, nodeClaimPtr)
		nodeClassPtr = ExpectExists(ctx, k8sClient, nodeClassPtr)

		Expect(err).ToNot(HaveOccurred())
		Expect(resultNodeClaim.Annotations[v1beta1.NodeClassHash]).To(Equal(utils.HashNodeClassSpec(nodeClassPtr)))
	})

	It("should priority spot shape first according to price", func() {
		shapeA := "VM.Standard.E3.Flex"
		shapeB := "VM.Standard.E4.Flex"
		shapeC := "VM.Standard.E5.Flex"

		instanceTypes := []*instancetype.OciInstanceType{
			createTestSpotAndOnDemandOfferings(shapeA, 10.0),
			createTestSpotAndOnDemandOfferings(shapeB, 14.0),
			createTestSpotAndOnDemandOfferings(shapeC, 28.0),
		}

		sorted := orderInstanceTypesByPrice(instanceTypes, scheduling.NewRequirements())

		Expect(sorted).To(HaveLen(6))
		Expect(sorted[0].InstanceType.Name).To(Equal(shapeA))
		Expect(sorted[0].Offerings[0].Requirements.Get(v1.CapacityTypeLabelKey).Any()).To(Equal(v1.CapacityTypeSpot))
		Expect(sorted[0].Offerings[0].Price).To(Equal(5.0))

		Expect(sorted[1].InstanceType.Name).To(Equal(shapeB))
		Expect(sorted[1].Offerings[0].Requirements.Get(v1.CapacityTypeLabelKey).Any()).To(Equal(v1.CapacityTypeSpot))
		Expect(sorted[1].Offerings[0].Price).To(Equal(7.0))

		Expect(sorted[2].InstanceType.Name).To(Equal(shapeA))
		Expect(sorted[2].Offerings[0].Requirements.Get(v1.CapacityTypeLabelKey).Any()).To(Equal(v1.CapacityTypeOnDemand))
		Expect(sorted[2].Offerings[0].Price).To(Equal(10.0))

		Expect(sorted[3].InstanceType.Name).To(Equal(shapeB))
		Expect(sorted[3].Offerings[0].Requirements.Get(v1.CapacityTypeLabelKey).Any()).To(Equal(v1.CapacityTypeOnDemand))
		Expect(sorted[3].Offerings[0].Price).To(Equal(14.0))

		Expect(sorted[4].InstanceType.Name).To(Equal(shapeC))
		Expect(sorted[4].Offerings[0].Requirements.Get(v1.CapacityTypeLabelKey).Any()).To(Equal(v1.CapacityTypeSpot))
		Expect(sorted[4].Offerings[0].Price).To(Equal(14.0))

		Expect(sorted[5].InstanceType.Name).To(Equal(shapeC))
		Expect(sorted[5].Offerings[0].Requirements.Get(v1.CapacityTypeLabelKey).Any()).To(Equal(v1.CapacityTypeOnDemand))
		Expect(sorted[5].Offerings[0].Price).To(Equal(28.0))
	})
})

var _ = Describe("CloudProvider Unit Tests", func() {
	Context("NodeClass readiness", func() {
		It("should validate nodeclass readiness states", func() {
			ready := testReadyNodeClass(uniqueName("ready"))
			Expect(ensureNodeClassReady(ready)).To(Succeed())

			notReady := testReadyNodeClass(uniqueName("not-ready"))
			notReady.StatusConditions().SetFalse(v1beta1.ConditionTypeImageReady, "NotReady", "not ready")
			Expect(cloudprovider.IsNodeClassNotReadyError(ensureNodeClassReady(notReady))).To(BeTrue())

			stale := testReadyNodeClass(uniqueName("stale"))
			for i := range stale.Status.Conditions {
				stale.Status.Conditions[i].ObservedGeneration = stale.Generation - 1
			}
			Expect(cloudprovider.IsNodeClassNotReadyError(ensureNodeClassReady(stale))).To(BeTrue())
		})
	})

	Context("Metadata helpers", func() {
		It("should resolve nodepools, nodeclasses and provider metadata helpers", func() {
			nodeClass := testReadyNodeClass(uniqueName("nodeclass"))
			nodePool := testNodePool(uniqueName("nodepool"), nodeClass.Name)
			cp := newUnitTestCloudProvider(unitTestCloudProviderOptions{
				kubeClient:    newFakeKubeClient(nodeClass, nodePool),
				instanceTypes: []*instancetype.OciInstanceType{testInstanceType("shape-a", 10)},
				repairPolicies: []cloudprovider.RepairPolicy{
					{ConditionType: corev1.NodeReady},
				},
			})

			resolvedPool, err := cp.resolveNodePoolFromInstance(context.Background(),
				testInstanceWithShape("shape-a", nodePool.Name))
			Expect(err).ToNot(HaveOccurred())
			Expect(resolvedPool.Name).To(Equal(nodePool.Name))

			_, err = cp.resolveNodePoolFromInstance(context.Background(), &ocicore.Instance{})
			Expect(err).To(HaveOccurred())

			resolvedClass, err := cp.resolveNodeClassFromNodePool(context.Background(), nodePool)
			Expect(err).ToNot(HaveOccurred())
			Expect(resolvedClass.Name).To(Equal(nodeClass.Name))

			instanceTypes, err := cp.GetInstanceTypes(context.Background(), nodePool)
			Expect(err).ToNot(HaveOccurred())
			Expect(instanceTypes).To(HaveLen(1))
			Expect(instanceTypes[0].Name).To(Equal("shape-a"))

			Expect(cp.RepairPolicies()).To(HaveLen(1))
			Expect(cp.Name()).To(Equal("oci"))
			Expect(cp.GetSupportedNodeClasses()).To(HaveLen(1))
			Expect(newTerminatingNodeClassError("terminating").Status().Code).To(Equal(int32(404)))
		})
	})

	Context("Get", func() {
		var (
			nodeClass *v1beta1.OCINodeClass
			nodePool  *v1.NodePool
			cp        *CloudProvider
		)

		BeforeEach(func() {
			nodeClass = testReadyNodeClass(uniqueName("nodeclass"))
			nodePool = testNodePool(uniqueName("nodepool"), nodeClass.Name)
			cp = newUnitTestCloudProvider(unitTestCloudProviderOptions{
				kubeClient:    newFakeKubeClient(nodeClass, nodePool),
				instanceTypes: []*instancetype.OciInstanceType{testInstanceType("shape-a", 10)},
			})
		})

		It("should hydrate nodeclaims from active instances", func() {
			cp.instanceProvider = &FakeInstanceProvider{
				GetInstanceFn: func(context.Context, string) (*instance.InstanceInfo, error) {
					return &instance.InstanceInfo{Instance: testInstanceWithShape("shape-a", nodePool.Name)}, nil
				},
			}

			nodeClaim, err := cp.Get(context.Background(), "ocid1.instance.oc1..get")
			Expect(err).ToNot(HaveOccurred())
			Expect(nodeClaim.Labels[v1.NodePoolLabelKey]).To(Equal(nodePool.Name))
			Expect(nodeClaim.Labels[corev1.LabelInstanceTypeStable]).To(Equal("shape-a"))
		})

		It("should treat terminated instances as not found", func() {
			cp.instanceProvider = &FakeInstanceProvider{
				GetInstanceFn: func(context.Context, string) (*instance.InstanceInfo, error) {
					i := testInstanceWithShape("shape-a", nodePool.Name)
					i.LifecycleState = ocicore.InstanceLifecycleStateTerminated
					return &instance.InstanceInfo{Instance: i}, nil
				},
			}

			_, err := cp.Get(context.Background(), "ocid1.instance.oc1..terminated")
			Expect(cloudprovider.IsNodeClaimNotFoundError(err)).To(BeTrue())
		})

		It("should treat missing instances as not found", func() {
			cp.instanceProvider = &FakeInstanceProvider{
				GetInstanceFn: func(context.Context, string) (*instance.InstanceInfo, error) {
					return nil, fakeServiceError{statusCode: 404, code: "NotAuthorizedOrNotFound", message: "missing"}
				},
			}

			_, err := cp.Get(context.Background(), "ocid1.instance.oc1..missing")
			Expect(cloudprovider.IsNodeClaimNotFoundError(err)).To(BeTrue())
		})
	})

	Context("Delete", func() {
		var (
			forgotten []string
			released  []string
			nodeClaim *v1.NodeClaim
			cp        *CloudProvider
		)

		BeforeEach(func() {
			nodeClaim = &v1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "claim-a",
					Labels: map[string]string{v1.NodePoolLabelKey: "pool-a"},
				},
				Status: v1.NodeClaimStatus{ProviderID: "ocid1.instance.oc1..delete"},
			}
			cp = newUnitTestCloudProvider(unitTestCloudProviderOptions{
				placementProvider: &FakePlacementProvider{
					InstanceForgetFn: func(nodePool string, instanceID string) {
						forgotten = append(forgotten, fmt.Sprintf("%s/%s", nodePool, instanceID))
					},
				},
				capacityReservationProvider: &FakeCapacityReservationProvider{
					MarkReleasedFn: func(instance *ocicore.Instance) {
						released = append(released, lo.FromPtr(instance.CapacityReservationId))
					},
				},
			})
		})

		It("should forget missing instances", func() {
			cp.instanceProvider = &FakeInstanceProvider{
				GetInstanceFn: func(context.Context, string) (*instance.InstanceInfo, error) {
					return nil, fakeServiceError{statusCode: 404, code: "NotAuthorizedOrNotFound", message: "missing"}
				},
			}

			err := cp.Delete(context.Background(), nodeClaim)
			Expect(cloudprovider.IsNodeClaimNotFoundError(err)).To(BeTrue())
			Expect(forgotten).To(ContainElement("pool-a/ocid1.instance.oc1..delete"))
		})

		It("should forget terminated instances", func() {
			cp.instanceProvider = &FakeInstanceProvider{
				GetInstanceFn: func(context.Context, string) (*instance.InstanceInfo, error) {
					i := testInstanceWithShape("shape-a", "pool-a")
					i.LifecycleState = ocicore.InstanceLifecycleStateTerminated
					i.Id = lo.ToPtr(nodeClaim.Status.ProviderID)
					return &instance.InstanceInfo{Instance: i}, nil
				},
			}

			err := cp.Delete(context.Background(), nodeClaim)
			Expect(cloudprovider.IsNodeClaimNotFoundError(err)).To(BeTrue())
			Expect(forgotten).To(ContainElement("pool-a/ocid1.instance.oc1..delete"))
		})

		It("should surface delete not found as already deleted", func() {
			cp.instanceProvider = &FakeInstanceProvider{
				GetInstanceFn: func(context.Context, string) (*instance.InstanceInfo, error) {
					i := testInstanceWithShape("shape-a", "pool-a")
					i.Id = lo.ToPtr(nodeClaim.Status.ProviderID)
					return &instance.InstanceInfo{Instance: i}, nil
				},
				DeleteInstanceFn: func(context.Context, string) error {
					return fakeServiceError{statusCode: 404, code: "NotAuthorizedOrNotFound", message: "missing"}
				},
			}

			err := cp.Delete(context.Background(), nodeClaim)
			Expect(cloudprovider.IsNodeClaimNotFoundError(err)).To(BeTrue())
		})

		It("should release capacity reservations after a successful delete", func() {
			cp.instanceProvider = &FakeInstanceProvider{
				GetInstanceFn: func(context.Context, string) (*instance.InstanceInfo, error) {
					i := testInstanceWithShape("shape-a", "pool-a")
					i.Id = lo.ToPtr(nodeClaim.Status.ProviderID)
					i.CapacityReservationId = lo.ToPtr("ocid1.capacityreservation.oc1..abc")
					return &instance.InstanceInfo{Instance: i}, nil
				},
				DeleteInstanceFn: func(context.Context, string) error { return nil },
			}

			Expect(cp.Delete(context.Background(), nodeClaim)).To(Succeed())
			Expect(released).To(ContainElement("ocid1.capacityreservation.oc1..abc"))
		})
	})

	Context("List", func() {
		It("should list instances only for in-use nodeclasses and filter unknown nodepools", func() {
			usedNodeClass := testReadyNodeClass(uniqueName("used"))
			compartmentA := "ocid1.compartment.oc1..a"
			usedNodeClass.Spec.NodeCompartmentId = &compartmentA
			unusedNodeClass := testReadyNodeClass(uniqueName("unused"))
			compartmentB := "ocid1.compartment.oc1..b"
			unusedNodeClass.Spec.NodeCompartmentId = &compartmentB

			nodePool := testNodePool(uniqueName("nodepool"), usedNodeClass.Name)
			var listedCompartments []string
			cp := newUnitTestCloudProvider(unitTestCloudProviderOptions{
				kubeClient:    newFakeKubeClient(usedNodeClass, unusedNodeClass, nodePool),
				instanceTypes: []*instancetype.OciInstanceType{testInstanceType("shape-a", 10)},
				instanceProvider: &FakeInstanceProvider{
					ListInstancesFn: func(_ context.Context, compartmentID string) ([]*ocicore.Instance, error) {
						listedCompartments = append(listedCompartments, compartmentID)
						return []*ocicore.Instance{
							testInstanceWithShape("shape-a", nodePool.Name),
							testInstanceWithShape("shape-a", "unknown-pool"),
						}, nil
					},
				},
			})

			nodeClaims, err := cp.List(context.Background())
			Expect(err).ToNot(HaveOccurred())
			Expect(listedCompartments).To(Equal([]string{compartmentA}))
			Expect(nodeClaims).To(HaveLen(1))
			Expect(nodeClaims[0].Labels[v1.NodePoolLabelKey]).To(Equal(nodePool.Name))
		})
	})

	Context("Create", func() {
		var (
			nodeClass   *v1beta1.OCINodeClass
			kubeClient  client.Client
			nodeClaim   *v1.NodeClaim
			instanceA   *instancetype.OciInstanceType
			instanceB   *instancetype.OciInstanceType
			imageResult *image.ImageResolveResult
		)

		BeforeEach(func() {
			nodeClass = testReadyNodeClass(uniqueName("create"))
			kubeClient = newFakeKubeClient(nodeClass)
			nodeClaim = testNodeClaim(nodeClass.Name)
			instanceA = testInstanceType("shape-a", 10)
			instanceB = testInstanceType("shape-b", 20)
			imageResult = testImageResolveResult(testImageID)
		})

		It("should reject creates before the cloud provider is initialized", func() {
			_, err := (&CloudProvider{}).Create(context.Background(), nodeClaim)
			Expect(err).To(MatchError(ContainSubstring("cloud-provider is not ready for use")))
		})

		It("should return a create error when the nodeclass cannot be resolved", func() {
			cp := newUnitTestCloudProvider(unitTestCloudProviderOptions{
				kubeClient:    newFakeKubeClient(),
				instanceTypes: []*instancetype.OciInstanceType{instanceA},
			})

			_, err := cp.Create(context.Background(), nodeClaim)
			var createErr *cloudprovider.CreateError
			Expect(errors.As(err, &createErr)).To(BeTrue())
			Expect(createErr.ConditionReason).To(Equal("OciNodeClassNotFound"))
		})

		It("should return insufficient capacity when no instance types match", func() {
			cp := newUnitTestCloudProvider(unitTestCloudProviderOptions{
				kubeClient:    kubeClient,
				instanceTypes: nil,
			})

			_, err := cp.Create(context.Background(), nodeClaim)
			Expect(cloudprovider.IsInsufficientCapacityError(err)).To(BeTrue())
		})

		It("should fall back to the next instance type when image resolution fails", func() {
			cp := newUnitTestCloudProvider(unitTestCloudProviderOptions{
				kubeClient:    kubeClient,
				instanceTypes: []*instancetype.OciInstanceType{instanceA, instanceB},
				imageProvider: &FakeImageProvider{
					ResolveImageForShapeFn: func(_ context.Context, _ *v1beta1.ImageConfig,
						shape string) (*image.ImageResolveResult, error) {
						if shape == "shape-a" {
							return nil, errors.New("shape-a image unavailable")
						}
						return imageResult, nil
					},
				},
				instanceProvider: &FakeInstanceProvider{
					LaunchInstanceFn: func(_ context.Context, _ *v1.NodeClaim, _ *v1beta1.OCINodeClass,
						it *instancetype.OciInstanceType, _ *image.ImageResolveResult, _ *network.NetworkResolveResult,
						_ *kms.KmsKeyResolveResult, _ *placement.Proposal) (*instance.InstanceInfo, error) {
						return &instance.InstanceInfo{Instance: testInstanceWithShape(it.Shape, "pool-a")}, nil
					},
				},
			})

			created, err := cp.Create(context.Background(), nodeClaim)
			Expect(err).ToNot(HaveOccurred())
			Expect(created.Labels[corev1.LabelInstanceTypeStable]).To(Equal("shape-b"))
		})

		It("should retry on no capacity and ignore NPN apply failures", func() {
			launchCount := 0
			cp := newUnitTestCloudProvider(unitTestCloudProviderOptions{
				kubeClient:    kubeClient,
				instanceTypes: []*instancetype.OciInstanceType{instanceA, instanceB},
				imageProvider: &FakeImageProvider{
					ResolveImageForShapeFn: func(_ context.Context, _ *v1beta1.ImageConfig,
						_ string) (*image.ImageResolveResult, error) {
						return imageResult, nil
					},
				},
				instanceProvider: &FakeInstanceProvider{
					LaunchInstanceFn: func(_ context.Context, _ *v1.NodeClaim, _ *v1beta1.OCINodeClass,
						it *instancetype.OciInstanceType, _ *image.ImageResolveResult, _ *network.NetworkResolveResult,
						_ *kms.KmsKeyResolveResult, _ *placement.Proposal) (*instance.InstanceInfo, error) {
						launchCount++
						if launchCount == 1 {
							return nil, instance.NoCapacityError{}
						}
						return &instance.InstanceInfo{Instance: testInstanceWithShape(it.Shape, "pool-a")}, nil
					},
				},
				npnProvider: &FakeNpnProvider{
					NpnClusterFn: func() bool { return true },
					CreateCustomObjFn: func(_ context.Context, _ *ocicore.Instance, _ *v1beta1.OCINodeClass,
						_ *network.NetworkResolveResult) (*npnv1beta1.NativePodNetwork, error) {
						return nil, errors.New("npn create failed")
					},
				},
			})

			created, err := cp.Create(context.Background(), nodeClaim)
			Expect(err).ToNot(HaveOccurred())
			Expect(created.Labels[corev1.LabelInstanceTypeStable]).To(Equal("shape-b"))
			Expect(launchCount).To(Equal(2))
		})

		It("should return a create error when launch fails with a non-capacity error", func() {
			cp := newUnitTestCloudProvider(unitTestCloudProviderOptions{
				kubeClient:    kubeClient,
				instanceTypes: []*instancetype.OciInstanceType{instanceA},
				imageProvider: &FakeImageProvider{
					ResolveImageForShapeFn: func(_ context.Context, _ *v1beta1.ImageConfig,
						_ string) (*image.ImageResolveResult, error) {
						return imageResult, nil
					},
				},
				instanceProvider: &FakeInstanceProvider{
					LaunchInstanceFn: func(_ context.Context, _ *v1.NodeClaim, _ *v1beta1.OCINodeClass,
						_ *instancetype.OciInstanceType, _ *image.ImageResolveResult, _ *network.NetworkResolveResult,
						_ *kms.KmsKeyResolveResult, _ *placement.Proposal) (*instance.InstanceInfo, error) {
						return nil, errors.New("launch failed")
					},
				},
			})

			_, err := cp.Create(context.Background(), nodeClaim)
			var createErr *cloudprovider.CreateError
			Expect(errors.As(err, &createErr)).To(BeTrue())
			Expect(createErr.ConditionReason).To(Equal("LaunchInstanceFailed"))
		})
	})

	Context("Drift", func() {
		It("should report instance drift for image mismatches", func() {
			nodeClass := testReadyNodeClass(uniqueName("drift"))
			desiredImage := &ocicore.Image{Id: lo.ToPtr("ocid1.image.oc1..desired")}
			instanceType := testInstanceType("shape-a", 10)
			instanceInfo := testInstanceWithShape("shape-a", "pool-a")
			instanceInfo.CompartmentId = lo.ToPtr("ocid1.compartment.oc1..a")
			instanceInfo.SourceDetails = ocicore.InstanceSourceViaImageDetails{
				ImageId: lo.ToPtr("ocid1.image.oc1..actual"),
			}

			reason, err := IsInstanceDrifted(&InstanceDesiredState{
				InstanceType:    instanceType,
				CompartmentOcid: "ocid1.compartment.oc1..a",
				Image:           desiredImage,
				NodeClass:       nodeClass,
			}, instanceInfo)
			Expect(err).ToNot(HaveOccurred())
			Expect(reason).To(Equal(cloudprovider.DriftReason("imageMismatch")))
		})

		It("should return capacity reservation mismatch during drift checks when reservation config is removed", func() {
			nodeClass := testReadyNodeClass(uniqueName("drift-nodeclass"))
			nodeClaim := testNodeClaim(nodeClass.Name)
			nodeClaim.Labels[corev1.LabelInstanceTypeStable] = "shape-a"
			nodeClaim.Spec.Requirements = append(nodeClaim.Spec.Requirements, v1.NodeSelectorRequirementWithMinValues{
				NodeSelectorRequirement: corev1.NodeSelectorRequirement{
					Key:      v1beta1.ReservationIDLabel,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{"reservation-a"},
				},
			})

			cp := newUnitTestCloudProvider(unitTestCloudProviderOptions{
				kubeClient:    newFakeKubeClient(nodeClass),
				instanceTypes: []*instancetype.OciInstanceType{testInstanceType("shape-a", 10)},
				imageProvider: &FakeImageProvider{
					ResolveImageForShapeFn: func(_ context.Context, _ *v1beta1.ImageConfig,
						_ string) (*image.ImageResolveResult, error) {
						return testImageResolveResult("ocid1.image.oc1..shape-a"), nil
					},
				},
				instanceProvider: &FakeInstanceProvider{
					GetInstanceCompartmentFn: func(*v1beta1.OCINodeClass) string {
						return "ocid1.compartment.oc1..a"
					},
				},
			})

			reason, err := cp.IsDrifted(context.Background(), nodeClaim)
			Expect(err).ToNot(HaveOccurred())
			Expect(reason).To(Equal(cloudprovider.DriftReason(CapacityReservationMismatch)))
		})
	})
})

func createTestSpotAndOnDemandOfferings(shape string, price float64) *instancetype.OciInstanceType {
	offerings := []*cloudprovider.Offering{
		createOffering(shape, v1.CapacityTypeSpot, "AD1", price/2),
		createOffering(shape, v1.CapacityTypeSpot, "AD2", price/2),
		createOffering(shape, v1.CapacityTypeSpot, "AD3", price/2),
		createOffering(shape, v1.CapacityTypeOnDemand, "AD1", price),
		createOffering(shape, v1.CapacityTypeOnDemand, "AD2", price),
		createOffering(shape, v1.CapacityTypeOnDemand, "AD3", price),
	}
	return &instancetype.OciInstanceType{
		InstanceType: cloudprovider.InstanceType{
			Name:      shape,
			Offerings: offerings,
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, shape),
			),
			Overhead: &cloudprovider.InstanceTypeOverhead{},
		},
		Shape: shape,
	}
}

func createOffering(shape string, capacityType string, ad string, price float64) *cloudprovider.Offering {
	return &cloudprovider.Offering{
		Requirements: scheduling.NewRequirements(
			scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, shape),
			scheduling.NewRequirement(v1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, capacityType),
			scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, ad),
		),
		Price:               price,
		Available:           true,
		ReservationCapacity: 1,
	}
}

type unitTestCloudProviderOptions struct {
	kubeClient                  client.Client
	instanceTypeProvider        instancetype.Provider
	instanceTypes               []*instancetype.OciInstanceType
	imageProvider               image.Provider
	networkProvider             network.Provider
	kmsProvider                 kms.Provider
	instanceProvider            instance.Provider
	placementProvider           placement.Provider
	capacityReservationProvider capacityreservation.Provider
	npnProvider                 npn.Provider
	repairPolicies              []cloudprovider.RepairPolicy
}

func newUnitTestCloudProvider(opts unitTestCloudProviderOptions) *CloudProvider {
	kubeClient := opts.kubeClient
	if kubeClient == nil {
		kubeClient = newFakeKubeClient()
	}

	instanceTypeProvider := opts.instanceTypeProvider
	if instanceTypeProvider == nil {
		instanceTypeProvider = NewFakeInstanceTypeProvider(opts.instanceTypes)
	}

	cp := &CloudProvider{
		initialized:                 true,
		kubeClient:                  kubeClient,
		instanceTypeProvider:        instanceTypeProvider,
		imageProvider:               &FakeImageProvider{ResolveImageForShapeFn: defaultResolveImageForShape},
		networkProvider:             &FakeNetworkProvider{},
		kmsKeyProvider:              &FakeKmsProvider{},
		instanceProvider:            &FakeInstanceProvider{},
		placementProvider:           &FakePlacementProvider{},
		capacityReservationProvider: &FakeCapacityReservationProvider{},
		npnProvider:                 &FakeNpnProvider{},
		repairPolicies:              opts.repairPolicies,
	}

	if opts.imageProvider != nil {
		cp.imageProvider = opts.imageProvider
	}
	if opts.networkProvider != nil {
		cp.networkProvider = opts.networkProvider
	}
	if opts.kmsProvider != nil {
		cp.kmsKeyProvider = opts.kmsProvider
	}
	if opts.instanceProvider != nil {
		cp.instanceProvider = opts.instanceProvider
	}
	if opts.placementProvider != nil {
		cp.placementProvider = opts.placementProvider
	}
	if opts.capacityReservationProvider != nil {
		cp.capacityReservationProvider = opts.capacityReservationProvider
	}
	if opts.npnProvider != nil {
		cp.npnProvider = opts.npnProvider
	}

	return cp
}

func defaultResolveImageForShape(_ context.Context, _ *v1beta1.ImageConfig,
	_ string) (*image.ImageResolveResult, error) {
	return testImageResolveResult(testImageID), nil
}

func newFakeKubeClient(objs ...client.Object) client.Client {
	builder := clientfake.NewClientBuilder().WithScheme(scheme.Scheme)
	if len(objs) > 0 {
		builder = builder.WithObjects(objs...)
	}
	return builder.Build()
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, atomic.AddUint64(&uniqueNameCounter, 1))
}

func testReadyNodeClass(name string) *v1beta1.OCINodeClass {
	nodeClass := fakes.CreateOciNodeClassWithMinimumReconcilableSetting(nodeClassClusterCompartmentID)
	nodeClass.Name = name
	nodeClass.Generation = 1
	nodeClass.StatusConditions().SetTrue(v1beta1.ConditionTypeImageReady)
	nodeClass.StatusConditions().SetTrue(v1beta1.ConditionTypeNetworkReady)
	nodeClass.StatusConditions().SetTrue(v1beta1.ConditionTypeNodeCompartment)
	for i := range nodeClass.Status.Conditions {
		nodeClass.Status.Conditions[i].ObservedGeneration = nodeClass.Generation
	}
	return &nodeClass
}

func testNodeClaim(nodeClassName string) *v1.NodeClaim {
	return &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   uniqueName("nodeclaim"),
			Labels: map[string]string{v1.NodePoolLabelKey: "pool-a"},
		},
		Spec: v1.NodeClaimSpec{
			NodeClassRef: &v1.NodeClassReference{
				Kind:  "OCINodeClass",
				Group: v1beta1.Group,
				Name:  nodeClassName,
			},
			Requirements: []v1.NodeSelectorRequirementWithMinValues{{
				NodeSelectorRequirement: corev1.NodeSelectorRequirement{
					Key:      v1.CapacityTypeLabelKey,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{v1.CapacityTypeOnDemand},
				},
			}},
		},
	}
}

func testNodePool(name, nodeClassName string) *v1.NodePool {
	return &v1.NodePool{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1.NodePoolSpec{
			Template: v1.NodeClaimTemplate{
				Spec: v1.NodeClaimTemplateSpec{
					NodeClassRef: &v1.NodeClassReference{
						Kind:  "OCINodeClass",
						Group: v1beta1.Group,
						Name:  nodeClassName,
					},
					Requirements: []v1.NodeSelectorRequirementWithMinValues{{
						NodeSelectorRequirement: corev1.NodeSelectorRequirement{
							Key:      corev1.LabelInstanceTypeStable,
							Operator: corev1.NodeSelectorOpExists,
						},
					}},
				},
			},
		},
	}
}

func testInstanceType(shape string, price float64) *instancetype.OciInstanceType {
	ocpu := float32(2)
	memory := float32(8)
	return &instancetype.OciInstanceType{
		InstanceType: cloudprovider.InstanceType{
			Name: shape,
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, shape),
			),
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(2000, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(8*1024*1024*1024, resource.BinarySI),
			},
			Overhead: &cloudprovider.InstanceTypeOverhead{},
			Offerings: []*cloudprovider.Offering{{
				Requirements: scheduling.NewRequirements(
					scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, shape),
					scheduling.NewRequirement(v1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, v1.CapacityTypeOnDemand),
					scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "PHX-AD-1"),
				),
				Price:               price,
				Available:           true,
				ReservationCapacity: 1,
			}},
		},
		Shape:       shape,
		Ocpu:        &ocpu,
		MemoryInGbs: &memory,
	}
}

func testImageResolveResult(imageID string) *image.ImageResolveResult {
	return &image.ImageResolveResult{
		Images: []*ocicore.Image{{
			Id:          lo.ToPtr(imageID),
			DisplayName: lo.ToPtr("oke-image"),
		}},
	}
}

func testInstanceWithShape(shape, nodePoolName string) *ocicore.Instance {
	return &ocicore.Instance{
		Id:                 lo.ToPtr(fmt.Sprintf("ocid1.instance.oc1..%s", shape)),
		DisplayName:        lo.ToPtr("test-instance"),
		AvailabilityDomain: lo.ToPtr("aumf:PHX-AD-1"),
		FaultDomain:        lo.ToPtr("FAULT-DOMAIN-1"),
		Shape:              lo.ToPtr(shape),
		TimeCreated:        &common.SDKTime{Time: testCreatedAt},
		FreeformTags: map[string]string{
			instance.NodePoolOciFreeFormTagKey:      nodePoolName,
			instance.NodeClassHashOciFreeFormTagKey: "hash-a",
		},
		SourceDetails: ocicore.InstanceSourceViaImageDetails{
			ImageId: lo.ToPtr(testImageID),
		},
	}
}

type fakeServiceError struct {
	statusCode int
	code       string
	message    string
}

func (f fakeServiceError) Error() string           { return f.message }
func (f fakeServiceError) GetHTTPStatusCode() int  { return f.statusCode }
func (f fakeServiceError) GetMessage() string      { return f.message }
func (f fakeServiceError) GetCode() string         { return f.code }
func (f fakeServiceError) GetOpcRequestID() string { return "req-id" }

var _ common.ServiceError = fakeServiceError{}
var _ error = fakeServiceError{}
