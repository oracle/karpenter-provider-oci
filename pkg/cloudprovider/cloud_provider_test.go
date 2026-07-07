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
	ocicache "github.com/oracle/karpenter-provider-oci/pkg/cache"
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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/controllers/provisioning"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/events"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	coretest "sigs.k8s.io/karpenter/pkg/test"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
)

const (
	nodeClassClusterCompartmentID = "ocid1.compartment.oc1..cluster123"
	sharedCompartmentID           = "ocid1.compartment.oc1..shared"
	testImageID                   = "ocid1.image.oc1..test"
	testShapeA                    = "shape-a"
	testCompartmentA              = "ocid1.compartment.oc1..a"
	testCompartmentB              = "ocid1.compartment.oc1..b"
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
		driftCaches := instance.NewDriftCaches()

		ociTestNodeClass = fakes.CreateOciNodeClassWithMinimumReconcilableSetting(nodeClassClusterCompartmentID)
		imageProvider := lo.Must(image.NewProvider(ctx, nil, fakes.NewFakeComputeClient(nodeClassClusterCompartmentID),
			"testPreBakedCompartmentId", "", fakes.NewDummyChannel()))
		kmsProvider := lo.Must(kms.NewProvider(ctx, nodeClassClusterCompartmentID, fakes.NewDummyConfigurationProvider()))
		kmsProvider.SetKmsClient("https://testvalut-management.kms.us-ashburn-1.oraclecloud.com", fakes.NewFakeKmsClient())
		networkProvider := lo.Must(network.NewProvider(ctx, nodeClassClusterCompartmentID,
			false, ipV4Family, fakes.NewFakeVirtualNetworkClient(), driftCaches.VnicCache()))
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
		bsProvider := lo.Must(blockstorage.NewProvider(ctx, &fakes.FakeBlockstorage{},
			driftCaches.BootVolumeCache()))
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
			crProvider, bsProvider, npnProvider, nil, false, fakes.NewDummyChannel()))
	})

	It("should create nodeclaim with nodeclass hash", func() {

		nodeClassPtr := &ociTestNodeClass
		ExpectApplied(ctx, k8sClient, nodeClassPtr)
		ExpectObjectReconciled(ctx, k8sClient, ociNodeClassController, nodeClassPtr)
		nodePool := testNodePool("pool-a", nodeClassPtr.Name)
		ExpectApplied(ctx, k8sClient, nodePool)
		nodeClaimPtr := &v1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{v1.NodePoolLabelKey: nodePool.Name},
			},
			Spec: v1.NodeClaimSpec{
				NodeClassRef: &v1.NodeClassReference{
					Kind:  nodeClassPtr.Kind,
					Group: v1beta1.Group,
					Name:  nodeClassPtr.Name,
				},
				Requirements: []v1.NodeSelectorRequirementWithMinValues{
					{
						Key:      corev1.LabelInstanceTypeStable,
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{testShape},
					},
				},
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
				instanceTypes: []*instancetype.OciInstanceType{testInstanceType(testShapeA, 10)},
				repairPolicies: []cloudprovider.RepairPolicy{
					{ConditionType: corev1.NodeReady},
				},
			})

			resolvedPool, err := cp.resolveNodePoolFromInstance(context.Background(),
				testInstanceWithShape(testShapeA, nodePool.Name))
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
			Expect(instanceTypes[0].Name).To(Equal(testShapeA))

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
				instanceTypes: []*instancetype.OciInstanceType{testInstanceType(testShapeA, 10)},
			})
		})

		It("should hydrate nodeclaims from active instances", func() {
			cp.instanceProvider = &FakeInstanceProvider{
				GetInstanceFn: func(context.Context, string) (*instance.InstanceInfo, error) {
					return &instance.InstanceInfo{Instance: testInstanceWithShape(testShapeA, nodePool.Name)}, nil
				},
			}

			nodeClaim, err := cp.Get(context.Background(), "ocid1.instance.oc1..get")
			Expect(err).ToNot(HaveOccurred())
			Expect(nodeClaim.Labels[v1.NodePoolLabelKey]).To(Equal(nodePool.Name))
			Expect(nodeClaim.Labels[corev1.LabelInstanceTypeStable]).To(Equal(testShapeA))
		})

		It("should treat terminated instances as not found", func() {
			cp.instanceProvider = &FakeInstanceProvider{
				GetInstanceFn: func(context.Context, string) (*instance.InstanceInfo, error) {
					i := testInstanceWithShape(testShapeA, nodePool.Name)
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
					i := testInstanceWithShape(testShapeA, "pool-a")
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
					i := testInstanceWithShape(testShapeA, "pool-a")
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
					i := testInstanceWithShape(testShapeA, "pool-a")
					i.Id = lo.ToPtr(nodeClaim.Status.ProviderID)
					i.CapacityReservationId = lo.ToPtr("ocid1.capacityreservation.oc1..abc")
					return &instance.InstanceInfo{Instance: i}, nil
				},
				DeleteInstanceFn: func(context.Context, string) error { return nil },
			}

			Expect(cp.Delete(context.Background(), nodeClaim)).To(Succeed())
			Expect(released).To(ContainElement("ocid1.capacityreservation.oc1..abc"))
		})

		It("should trust nodeclaim provider ID when deleting tagged instances", func() {
			deleteCalled := false
			cp.instanceProvider = &FakeInstanceProvider{
				GetInstanceFn: func(context.Context, string) (*instance.InstanceInfo, error) {
					i := testInstanceWithShape(testShapeA, "pool-a")
					i.Id = lo.ToPtr(nodeClaim.Status.ProviderID)
					i.FreeformTags[instance.NodePoolUIDOciFreeFormTagKey] = "other-nodepool-uid"
					return &instance.InstanceInfo{Instance: i}, nil
				},
				DeleteInstanceFn: func(context.Context, string) error {
					deleteCalled = true
					return nil
				},
			}

			Expect(cp.Delete(context.Background(), nodeClaim)).To(Succeed())
			Expect(deleteCalled).To(BeTrue())
		})
	})

	Context("List", func() {
		It("should list instances only for in-use nodeclasses and filter unknown nodepools", func() {
			compartmentA := testCompartmentA
			usedNodeClassA := testReadyNodeClass(uniqueName("used-a"))
			usedNodeClassA.Spec.NodeCompartmentId = &compartmentA
			compartmentB := testCompartmentB
			usedNodeClassB := testReadyNodeClass(uniqueName("used-b"))
			usedNodeClassB.Spec.NodeCompartmentId = &compartmentB
			unusedNodeClass := testReadyNodeClass(uniqueName("unused"))
			unusedCompartment := sharedCompartmentID
			unusedNodeClass.Spec.NodeCompartmentId = &unusedCompartment

			nodePoolA := testNodePool(uniqueName("nodepool-a"), usedNodeClassA.Name)
			nodePoolB := testNodePool(uniqueName("nodepool-b"), usedNodeClassB.Name)
			var listedCompartments []string
			listCalls := 0
			cp := newUnitTestCloudProvider(unitTestCloudProviderOptions{
				kubeClient: newFakeKubeClient(
					usedNodeClassA, usedNodeClassB, unusedNodeClass, nodePoolA, nodePoolB),
				instanceTypes: []*instancetype.OciInstanceType{testInstanceType(testShapeA, 10)},
				instanceProvider: &FakeInstanceProvider{
					ListInstancesFn: func(_ context.Context, compartmentIDs []string) ([]*ocicore.Instance, error) {
						listCalls++
						listedCompartments = append(listedCompartments, compartmentIDs...)
						return []*ocicore.Instance{
							testInstanceWithShape(testShapeA, nodePoolA.Name),
							testInstanceWithShape(testShapeA, "unknown-pool"),
						}, nil
					},
				},
			})

			nodeClaims, err := cp.List(context.Background())
			Expect(err).ToNot(HaveOccurred())
			Expect(listCalls).To(Equal(1))
			Expect(listedCompartments).To(ConsistOf(compartmentA, compartmentB))
			Expect(listedCompartments).ToNot(ContainElement(unusedCompartment))
			Expect(nodeClaims).To(HaveLen(1))
			Expect(nodeClaims[0].Labels[v1.NodePoolLabelKey]).To(Equal(nodePoolA.Name))
		})

		It("should include instances when nodepool name and UID match", func() {
			nodeClass := testReadyNodeClass(uniqueName("used"))
			compartment := sharedCompartmentID
			nodeClass.Spec.NodeCompartmentId = &compartment
			nodePool := testNodePoolWithUID(uniqueName("nodepool"), nodeClass.Name)
			localInstance := testInstanceWithShape(testShapeA, nodePool.Name)
			localInstance.FreeformTags[instance.NodePoolUIDOciFreeFormTagKey] = string(nodePool.UID)

			cp := newUnitTestCloudProvider(unitTestCloudProviderOptions{
				kubeClient:    newFakeKubeClient(nodeClass, nodePool),
				instanceTypes: []*instancetype.OciInstanceType{testInstanceType(testShapeA, 10)},
				instanceProvider: &FakeInstanceProvider{
					ListInstancesFn: func(_ context.Context, _ []string) ([]*ocicore.Instance, error) {
						return []*ocicore.Instance{localInstance}, nil
					},
				},
			})

			nodeClaims, err := cp.List(context.Background())
			Expect(err).ToNot(HaveOccurred())
			Expect(nodeClaims).To(HaveLen(1))
			Expect(nodeClaims[0].Labels[v1.NodePoolLabelKey]).To(Equal(nodePool.Name))
		})

		It("should exclude instances owned by another nodepool UID even when nodepool names match", func() {
			nodeClass := testReadyNodeClass(uniqueName("used"))
			compartment := sharedCompartmentID
			nodeClass.Spec.NodeCompartmentId = &compartment
			nodePool := testNodePoolWithUID(uniqueName("nodepool"), nodeClass.Name)
			foreignInstance := testInstanceWithShape(testShapeA, nodePool.Name)
			foreignInstance.FreeformTags[instance.NodePoolUIDOciFreeFormTagKey] = "other-nodepool-uid"

			cp := newUnitTestCloudProvider(unitTestCloudProviderOptions{
				kubeClient:    newFakeKubeClient(nodeClass, nodePool),
				instanceTypes: []*instancetype.OciInstanceType{testInstanceType(testShapeA, 10)},
				instanceProvider: &FakeInstanceProvider{
					ListInstancesFn: func(_ context.Context, _ []string) ([]*ocicore.Instance, error) {
						return []*ocicore.Instance{foreignInstance}, nil
					},
				},
			})

			nodeClaims, err := cp.List(context.Background())
			Expect(err).ToNot(HaveOccurred())
			Expect(nodeClaims).To(BeEmpty())
		})

		It("should include legacy untagged instances when nodepool name matches", func() {
			nodeClass := testReadyNodeClass(uniqueName("used"))
			compartment := sharedCompartmentID
			nodeClass.Spec.NodeCompartmentId = &compartment
			nodePool := testNodePool("pool-a", nodeClass.Name)

			ownedLegacy := testInstanceWithShape(testShapeA, nodePool.Name)
			ownedLegacy.Id = lo.ToPtr("ocid1.instance.oc1..legacy-owned")
			delete(ownedLegacy.FreeformTags, instance.NodePoolUIDOciFreeFormTagKey)

			unownedLegacy := testInstanceWithShape(testShapeA, nodePool.Name)
			unownedLegacy.Id = lo.ToPtr("ocid1.instance.oc1..legacy-unowned")
			delete(unownedLegacy.FreeformTags, instance.NodePoolUIDOciFreeFormTagKey)

			cp := newUnitTestCloudProvider(unitTestCloudProviderOptions{
				kubeClient:    newFakeKubeClient(nodeClass, nodePool),
				instanceTypes: []*instancetype.OciInstanceType{testInstanceType(testShapeA, 10)},
				instanceProvider: &FakeInstanceProvider{
					ListInstancesFn: func(_ context.Context, _ []string) ([]*ocicore.Instance, error) {
						return []*ocicore.Instance{ownedLegacy, unownedLegacy}, nil
					},
				},
			})

			nodeClaims, err := cp.List(context.Background())
			Expect(err).ToNot(HaveOccurred())
			Expect(nodeClaims).To(HaveLen(2))
		})

		It("should surface an aggregate compartment listing failure", func() {
			compartmentA := testCompartmentA
			compartmentB := testCompartmentB
			nodeClassA := testReadyNodeClass(uniqueName("used-a"))
			nodeClassA.Spec.NodeCompartmentId = &compartmentA
			nodeClassB := testReadyNodeClass(uniqueName("used-b"))
			nodeClassB.Spec.NodeCompartmentId = &compartmentB
			nodePoolA := testNodePool(uniqueName("nodepool-a"), nodeClassA.Name)
			nodePoolB := testNodePool(uniqueName("nodepool-b"), nodeClassB.Name)

			cp := newUnitTestCloudProvider(unitTestCloudProviderOptions{
				kubeClient: newFakeKubeClient(nodeClassA, nodeClassB, nodePoolA, nodePoolB),
				instanceProvider: &FakeInstanceProvider{
					ListInstancesFn: func(_ context.Context, compartmentIDs []string) ([]*ocicore.Instance, error) {
						Expect(compartmentIDs).To(ConsistOf(compartmentA, compartmentB))
						return nil, errors.New("list failed")
					},
				},
			})

			_, err := cp.List(context.Background())
			Expect(err).To(MatchError("list failed"))
		})

	})

	Context("Create", func() {
		var (
			nodeClass   *v1beta1.OCINodeClass
			nodePool    *v1.NodePool
			kubeClient  client.Client
			nodeClaim   *v1.NodeClaim
			instanceA   *instancetype.OciInstanceType
			instanceB   *instancetype.OciInstanceType
			imageResult *image.ImageResolveResult
		)

		BeforeEach(func() {
			nodeClass = testReadyNodeClass(uniqueName("create"))
			nodePool = testNodePoolWithUID("pool-a", nodeClass.Name)
			kubeClient = newFakeKubeClient(nodeClass, nodePool)
			nodeClaim = testNodeClaim(nodeClass.Name)
			instanceA = testInstanceType(testShapeA, 10)
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
						if shape == testShapeA {
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

		It("should try the next shape config when the first is out of host capacity "+
			"(single shape, multiple shape configs)", func() {
			// A NodePool pinned to a single shape paired with a NodeClass that declares multiple
			// shape configs resolves to multiple instance types that share the same Shape but differ
			// by OCPU/memory. When the first (cheaper) shape config is out of host capacity, the
			// launch loop must move on and attempt the next shape config rather than failing.
			const shape = "VM.Standard.E5.Flex"
			smallConfig := testInstanceType(shape, 10)
			smallConfig.Ocpu = lo.ToPtr(float32(2))
			largeConfig := testInstanceType(shape, 20)
			largeConfig.Ocpu = lo.ToPtr(float32(4))

			launchCount := 0
			cp := newUnitTestCloudProvider(unitTestCloudProviderOptions{
				kubeClient:    kubeClient,
				instanceTypes: []*instancetype.OciInstanceType{smallConfig, largeConfig},
				imageProvider: &FakeImageProvider{
					ResolveImageForShapeFn: func(_ context.Context, _ *v1beta1.ImageConfig,
						_ string) (*image.ImageResolveResult, error) {
						return imageResult, nil
					},
				},
				instanceProvider: &FakeInstanceProvider{
					LaunchInstanceFn: func(_ context.Context, nc *v1.NodeClaim, _ *v1beta1.OCINodeClass,
						it *instancetype.OciInstanceType, _ *image.ImageResolveResult, _ *network.NetworkResolveResult,
						_ *kms.KmsKeyResolveResult, _ *placement.Proposal) (*instance.InstanceInfo, error) {
						launchCount++
						// Only the smaller shape config is out of host capacity.
						if it.Ocpu != nil && *it.Ocpu == float32(2) {
							return nil, instance.NoCapacityError{}
						}
						return &instance.InstanceInfo{
							Instance: testInstanceWithShape(it.Shape, nc.Labels[v1.NodePoolLabelKey]),
						}, nil
					},
				},
			})

			created, err := cp.Create(context.Background(), nodeClaim)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).ToNot(BeNil())
			// Both shape configs were attempted: the first failed with OOHC, the second succeeded.
			Expect(launchCount).To(Equal(2))
			Expect(created.Labels[corev1.LabelInstanceTypeStable]).To(Equal(shape))
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

		It("should return insufficient capacity when launch exhausts out of host capacity errors", func() {
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
						_ *instancetype.OciInstanceType, _ *image.ImageResolveResult, _ *network.NetworkResolveResult,
						_ *kms.KmsKeyResolveResult, _ *placement.Proposal) (*instance.InstanceInfo, error) {
						launchCount++
						return nil, errors.New("Out of host capacity in selected AD")
					},
				},
			})

			_, err := cp.Create(context.Background(), nodeClaim)
			Expect(cloudprovider.IsInsufficientCapacityError(err)).To(BeTrue())
			var createErr *cloudprovider.CreateError
			Expect(errors.As(err, &createErr)).To(BeFalse())
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

		It("should return insufficient capacity when all instance types are out of host capacity", func() {
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
						_ *instancetype.OciInstanceType, _ *image.ImageResolveResult, _ *network.NetworkResolveResult,
						_ *kms.KmsKeyResolveResult, _ *placement.Proposal) (*instance.InstanceInfo, error) {
						launchCount++
						return nil, instance.NoCapacityError{}
					},
				},
			})

			_, err := cp.Create(context.Background(), nodeClaim)
			Expect(cloudprovider.IsInsufficientCapacityError(err)).To(BeTrue())
			Expect(launchCount).To(Equal(2))
		})

		It("should return insufficient capacity when all instance types hit a service limit", func() {
			launchCount := 0
			cp := newUnitTestCloudProvider(unitTestCloudProviderOptions{
				kubeClient:                 kubeClient,
				instanceTypes:              []*instancetype.OciInstanceType{instanceA, instanceB},
				enableServiceLimitFallback: true,
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
						launchCount++
						return nil, fakeServiceError{statusCode: 400, code: "LimitExceeded", message: "service limit exceeded"}
					},
				},
			})

			_, err := cp.Create(context.Background(), nodeClaim)
			Expect(cloudprovider.IsInsufficientCapacityError(err)).To(BeTrue())
			Expect(launchCount).To(Equal(2))
		})

		It("should return a create error when service limit fallback is disabled", func() {
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
						_ *instancetype.OciInstanceType, _ *image.ImageResolveResult, _ *network.NetworkResolveResult,
						_ *kms.KmsKeyResolveResult, _ *placement.Proposal) (*instance.InstanceInfo, error) {
						launchCount++
						return nil, fakeServiceError{statusCode: 400, code: "LimitExceeded", message: "service limit exceeded"}
					},
				},
			})

			_, err := cp.Create(context.Background(), nodeClaim)
			var createErr *cloudprovider.CreateError
			Expect(errors.As(err, &createErr)).To(BeTrue())
			Expect(cloudprovider.IsInsufficientCapacityError(err)).To(BeFalse())
			Expect(launchCount).To(Equal(1))
		})

		It("should fall back to the next shape when one shape hits a service limit", func() {
			launchCount := 0
			cp := newUnitTestCloudProvider(unitTestCloudProviderOptions{
				kubeClient:                 kubeClient,
				instanceTypes:              []*instancetype.OciInstanceType{instanceA, instanceB},
				enableServiceLimitFallback: true,
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
							return nil, fakeServiceError{statusCode: 400, code: "LimitExceeded", message: "service limit exceeded"}
						}
						return &instance.InstanceInfo{Instance: testInstanceWithShape(it.Shape, "pool-a")}, nil
					},
				},
			})

			created, err := cp.Create(context.Background(), nodeClaim)
			Expect(err).ToNot(HaveOccurred())
			Expect(created.Labels[corev1.LabelInstanceTypeStable]).To(Equal("shape-b"))
			Expect(launchCount).To(Equal(2))
		})
	})

	Context("Drift", func() {
		It("should report instance drift for image mismatches", func() {
			nodeClass := testReadyNodeClass(uniqueName("drift"))
			desiredImage := &ocicore.Image{Id: lo.ToPtr("ocid1.image.oc1..desired")}
			instanceType := testInstanceType(testShapeA, 10)
			instanceInfo := testInstanceWithShape(testShapeA, "pool-a")
			instanceInfo.CompartmentId = lo.ToPtr(testCompartmentA)
			instanceInfo.SourceDetails = ocicore.InstanceSourceViaImageDetails{
				ImageId: lo.ToPtr("ocid1.image.oc1..actual"),
			}

			reason, err := IsInstanceDrifted(&InstanceDesiredState{
				InstanceType:    instanceType,
				CompartmentOcid: testCompartmentA,
				Image:           desiredImage,
				NodeClass:       nodeClass,
			}, instanceInfo)
			Expect(err).ToNot(HaveOccurred())
			Expect(reason).To(Equal(cloudprovider.DriftReason("imageMismatch")))
		})

		It("should return capacity reservation mismatch during drift checks when reservation config is removed", func() {
			nodeClass := testReadyNodeClass(uniqueName("drift-nodeclass"))
			nodeClaim := testNodeClaim(nodeClass.Name)
			nodeClaim.Labels[corev1.LabelInstanceTypeStable] = testShapeA
			nodeClaim.Spec.Requirements = append(nodeClaim.Spec.Requirements, v1.NodeSelectorRequirementWithMinValues{
				Key:      v1beta1.ReservationIDLabel,
				Operator: corev1.NodeSelectorOpIn,
				Values:   []string{"reservation-a"},
			})

			cp := newUnitTestCloudProvider(unitTestCloudProviderOptions{
				kubeClient:    newFakeKubeClient(nodeClass),
				instanceTypes: []*instancetype.OciInstanceType{testInstanceType(testShapeA, 10)},
				imageProvider: &FakeImageProvider{
					ResolveImageForShapeFn: func(_ context.Context, _ *v1beta1.ImageConfig,
						_ string) (*image.ImageResolveResult, error) {
						return testImageResolveResult("ocid1.image.oc1..shape-a"), nil
					},
				},
				instanceProvider: &FakeInstanceProvider{
					GetInstanceCompartmentFn: func(*v1beta1.OCINodeClass) string {
						return testCompartmentA
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
	blockStorageProvider        blockstorage.Provider
	npnProvider                 npn.Provider
	repairPolicies              []cloudprovider.RepairPolicy
	enableServiceLimitFallback  bool
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
		blockStorageProvider:        &FakeBlockStorageProvider{},
		npnProvider:                 &FakeNpnProvider{},
		repairPolicies:              opts.repairPolicies,
		enableServiceLimitFallback:  opts.enableServiceLimitFallback,
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
	if opts.blockStorageProvider != nil {
		cp.blockStorageProvider = opts.blockStorageProvider
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
				Key:      v1.CapacityTypeLabelKey,
				Operator: corev1.NodeSelectorOpIn,
				Values:   []string{v1.CapacityTypeOnDemand},
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
						Key:      corev1.LabelInstanceTypeStable,
						Operator: corev1.NodeSelectorOpExists,
					}},
				},
			},
		},
	}
}

func testNodePoolWithUID(name, nodeClassName string) *v1.NodePool {
	nodePool := testNodePool(name, nodeClassName)
	nodePool.UID = types.UID(name + "-uid")
	return nodePool
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

// testFlexConfigInstanceType builds a flexible-shape instance type for a specific CPU/memory
// configuration. All configs of a shape share the same Shape but differ by Name (the instance-type
// label) and by their Ocpu/MemoryInGbs, mirroring how listInstanceTypesForFlexShape enumerates flex
// configs in production.
func testFlexConfigInstanceType(shape, name string, ocpu, memory float32, price float64) *instancetype.OciInstanceType {
	o := ocpu
	m := memory
	return &instancetype.OciInstanceType{
		InstanceType: cloudprovider.InstanceType{
			Name: name,
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, name),
			),
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(int64(ocpu*1000), resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(int64(memory)*1024*1024*1024, resource.BinarySI),
				corev1.ResourcePods:   *resource.NewQuantity(110, resource.DecimalSI),
			},
			Overhead: &cloudprovider.InstanceTypeOverhead{},
			Offerings: []*cloudprovider.Offering{{
				Requirements: scheduling.NewRequirements(
					scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, name),
					scheduling.NewRequirement(v1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, v1.CapacityTypeOnDemand),
					scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "PHX-AD-1"),
				),
				Price:               price,
				Available:           true,
				ReservationCapacity: 1,
			}},
		},
		Shape:              shape,
		SupportShapeConfig: true,
		Ocpu:               &o,
		MemoryInGbs:        &m,
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

// These tests drive Karpenter core's real scheduler/provisioner against the OCI CloudProvider to
// verify cross-NodePool fallback when one NodePool's only offering goes out of host capacity.
//
// The unavailable-offerings cache feeds offering availability into the instance-type provider
// (production wiring is unit-tested in pkg/providers/instancetype). Here we exercise the end-to-end
// behaviour: once an offering is marked unavailable, GetInstanceTypes reports it as unavailable and
// core's scheduler routes new capacity to the other NodePool.
var _ = Describe("CloudProvider NodePool Fallback", func() {
	const zone = "PHX-AD-1"

	var (
		preferredShape     string
		fallbackShape      string
		preferredClass     string
		fallbackClass      string
		unavailable        *ocicache.UnavailableOfferings
		instanceProvider   *FakeInstanceProvider
		cp                 *CloudProvider
		cluster            *state.Cluster
		prov               *provisioning.Provisioner
		provCtx            context.Context
		preferredNodeClass *v1beta1.OCINodeClass
		fallbackNodeClass  *v1beta1.OCINodeClass
		preferredPool      *v1.NodePool
		fallbackPool       *v1.NodePool
		smallPodResources  corev1.ResourceRequirements
	)

	BeforeEach(func() {
		preferredShape = uniqueName("shape-preferred")
		fallbackShape = uniqueName("shape-fallback")
		unavailable = ocicache.NewUnavailableOfferings(ocicache.UnavailableOfferingsTTL)

		preferredNodeClass = testReadyNodeClass(uniqueName("preferred"))
		fallbackNodeClass = testReadyNodeClass(uniqueName("fallback"))
		preferredClass = preferredNodeClass.Name
		fallbackClass = fallbackNodeClass.Name

		// Instance types are resolved per-NodeClass and have their on-demand offering gated by the
		// shared unavailable-offerings cache, mirroring the production setOfferings behaviour.
		instanceTypeProvider := &FakeInstanceTypeProvider{
			ListFn: func(_ context.Context, nodeClass *v1beta1.OCINodeClass,
				_ []corev1.Taint) ([]*instancetype.OciInstanceType, error) {
				var shape string
				switch nodeClass.Name {
				case preferredClass:
					shape = preferredShape
				case fallbackClass:
					shape = fallbackShape
				default:
					return nil, nil
				}
				it := fallbackTestInstanceType(shape, 10)
				for _, o := range it.Offerings {
					if unavailable.IsUnavailable(shape, it.Ocpu, it.MemoryInGbs, o.Zone(), o.CapacityType(), "") {
						o.Available = false
					}
				}
				return []*instancetype.OciInstanceType{it}, nil
			},
		}

		instanceProvider = &FakeInstanceProvider{
			LaunchInstanceFn: func(_ context.Context, nc *v1.NodeClaim, _ *v1beta1.OCINodeClass,
				it *instancetype.OciInstanceType, _ *image.ImageResolveResult, _ *network.NetworkResolveResult,
				_ *kms.KmsKeyResolveResult, _ *placement.Proposal) (*instance.InstanceInfo, error) {
				return &instance.InstanceInfo{
					Instance: testInstanceWithShape(it.Shape, nc.Labels[v1.NodePoolLabelKey]),
				}, nil
			},
		}

		cp = newUnitTestCloudProvider(unitTestCloudProviderOptions{
			kubeClient:           k8sClient,
			instanceTypeProvider: instanceTypeProvider,
			instanceProvider:     instanceProvider,
		})

		clk := clock.RealClock{}
		recorder := events.NewRecorder(&record.FakeRecorder{})
		cluster = state.NewCluster(clk, k8sClient, cp)
		prov = provisioning.NewProvisioner(k8sClient, recorder, cp, cluster, clk)
		provCtx = coreoptions.ToContext(ctx, coretest.Options())

		// Preferred pool has the higher weight, so the scheduler always tries it first.
		preferredPool = fallbackTestNodePool(uniqueName("preferred-pool"), preferredClass, 100)
		fallbackPool = fallbackTestNodePool(uniqueName("fallback-pool"), fallbackClass, 10)

		ExpectApplied(provCtx, k8sClient, preferredNodeClass, fallbackNodeClass, preferredPool, fallbackPool)

		// Sized so that only one such pod fits per node, forcing a fresh node per pod.
		smallPodResources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1500m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
		}
	})

	AfterEach(func() {
		// ExpectCleanedUp references a TestNodeClass CRD that is not registered in this envtest, so we
		// delete the objects this suite created directly. Unique names keep specs isolated.
		ExpectDeleted(provCtx, k8sClient, preferredPool, fallbackPool, preferredNodeClass, fallbackNodeClass)
	})

	It("schedules to the preferred NodePool while its offering is available", func() {
		pod := coretest.UnschedulablePod(coretest.PodOptions{ResourceRequirements: smallPodResources})
		bindings := ExpectProvisioned(provCtx, k8sClient, cluster, cp, prov, pod)

		binding := bindings.Get(pod)
		Expect(binding).ToNot(BeNil())
		Expect(binding.NodeClaim).ToNot(BeNil())
		Expect(binding.NodeClaim.Labels[v1.NodePoolLabelKey]).To(Equal(preferredPool.Name))
		Expect(binding.NodeClaim.Labels[corev1.LabelInstanceTypeStable]).To(Equal(preferredShape))
	})

	It("falls back to another NodePool once the preferred offering is out of capacity", func() {
		// First pod lands on the preferred pool.
		pod1 := coretest.UnschedulablePod(coretest.PodOptions{ResourceRequirements: smallPodResources})
		bindings := ExpectProvisioned(provCtx, k8sClient, cluster, cp, prov, pod1)
		binding1 := bindings.Get(pod1)
		Expect(binding1).ToNot(BeNil())
		Expect(binding1.NodeClaim).ToNot(BeNil())
		Expect(binding1.NodeClaim.Labels[v1.NodePoolLabelKey]).To(Equal(preferredPool.Name))

		// Preferred pool's on-demand offering (for its resolved CPU/memory config) goes out of host
		// capacity.
		preferredIT := fallbackTestInstanceType(preferredShape, 10)
		unavailable.MarkUnavailable(provCtx, preferredShape, preferredIT.Ocpu, preferredIT.MemoryInGbs,
			zone, v1.CapacityTypeOnDemand, "")

		// Next pod cannot reuse the preferred node and the preferred offering is unavailable, so the
		// scheduler must fall back to the other NodePool.
		pod2 := coretest.UnschedulablePod(coretest.PodOptions{ResourceRequirements: smallPodResources})
		bindings = ExpectProvisioned(provCtx, k8sClient, cluster, cp, prov, pod2)
		binding2 := bindings.Get(pod2)
		Expect(binding2).ToNot(BeNil())
		Expect(binding2.NodeClaim).ToNot(BeNil())
		Expect(binding2.NodeClaim.Labels[v1.NodePoolLabelKey]).To(Equal(fallbackPool.Name))
		Expect(binding2.NodeClaim.Labels[corev1.LabelInstanceTypeStable]).To(Equal(fallbackShape))
	})

	It("falls back to another NodePool when the preferred pool's launch hits a service limit", func() {
		cp.enableServiceLimitFallback = true

		// The preferred shape's launch fails with a service-limit (LimitExceeded) error. We emulate
		// the production LaunchInstance defer by marking the offering unavailable on that error; the
		// FakeInstanceProvider does not run the real defer. CloudProvider.Create classifies this as an
		// InsufficientCapacityError, so no node is created this round.
		instanceProvider.LaunchInstanceFn = func(_ context.Context, nc *v1.NodeClaim, _ *v1beta1.OCINodeClass,
			it *instancetype.OciInstanceType, _ *image.ImageResolveResult, _ *network.NetworkResolveResult,
			_ *kms.KmsKeyResolveResult, _ *placement.Proposal) (*instance.InstanceInfo, error) {
			if it.Shape == preferredShape {
				unavailable.MarkUnavailable(provCtx, preferredShape, it.Ocpu, it.MemoryInGbs,
					zone, v1.CapacityTypeOnDemand, "")
				return nil, fakeServiceError{statusCode: 400, code: "LimitExceeded", message: "service limit exceeded"}
			}
			return &instance.InstanceInfo{
				Instance: testInstanceWithShape(it.Shape, nc.Labels[v1.NodePoolLabelKey]),
			}, nil
		}

		// Round 1: the pod schedules onto the higher-weighted preferred pool (its offering is still
		// available at schedule time), but the launch hits the service limit, so no binding is made.
		pod := coretest.UnschedulablePod(coretest.PodOptions{ResourceRequirements: smallPodResources})
		bindings := ExpectProvisioned(provCtx, k8sClient, cluster, cp, prov, pod)
		Expect(bindings.Get(pod)).To(BeNil())

		// Core's NodeClaim lifecycle deletes the NodeClaim on InsufficientCapacityError; emulate that
		// so the failed NodeClaim is not counted as in-flight capacity on the next scheduling round.
		for _, nc := range ExpectNodeClaims(provCtx, k8sClient) {
			ExpectDeleted(provCtx, k8sClient, nc)
			cluster.DeleteNodeClaim(nc.Name)
		}

		// Round 2: the preferred offering is now cached as unavailable, so the scheduler falls back to
		// the other NodePool.
		bindings = ExpectProvisioned(provCtx, k8sClient, cluster, cp, prov, pod)
		binding := bindings.Get(pod)
		Expect(binding).ToNot(BeNil())
		Expect(binding.NodeClaim).ToNot(BeNil())
		Expect(binding.NodeClaim.Labels[v1.NodePoolLabelKey]).To(Equal(fallbackPool.Name))
		Expect(binding.NodeClaim.Labels[corev1.LabelInstanceTypeStable]).To(Equal(fallbackShape))
	})
})

// Flexible shapes expose multiple CPU/memory configs that share a Shape but differ by
// instance-type name. This suite verifies end-to-end that marking one config's offering unavailable
// only routes the scheduler away from that config, leaving the shape's other configs schedulable.
var _ = Describe("CloudProvider Flex Config Scoping", func() {
	const zone = "PHX-AD-1"

	var (
		shape             string
		smallConfig       string
		largeConfig       string
		nodeClass         *v1beta1.OCINodeClass
		unavailable       *ocicache.UnavailableOfferings
		cp                *CloudProvider
		cluster           *state.Cluster
		prov              *provisioning.Provisioner
		provCtx           context.Context
		pool              *v1.NodePool
		smallPodResources corev1.ResourceRequirements
	)

	BeforeEach(func() {
		shape = uniqueName("shape-flex")
		smallConfig = shape + ".2o.16g"
		largeConfig = shape + ".4o.16g"
		unavailable = ocicache.NewUnavailableOfferings(ocicache.UnavailableOfferingsTTL)

		nodeClass = testReadyNodeClass(uniqueName("flex"))

		// Both configs of the shape are gated on the shared unavailable-offerings cache scoped by
		// their own CPU/memory, mirroring production setOfferings.
		instanceTypeProvider := &FakeInstanceTypeProvider{
			ListFn: func(_ context.Context, nc *v1beta1.OCINodeClass,
				_ []corev1.Taint) ([]*instancetype.OciInstanceType, error) {
				if nc.Name != nodeClass.Name {
					return nil, nil
				}
				// small config is cheaper, so the scheduler prefers it while it is available.
				its := []*instancetype.OciInstanceType{
					testFlexConfigInstanceType(shape, smallConfig, 2, 16, 1),
					testFlexConfigInstanceType(shape, largeConfig, 4, 16, 2),
				}
				for _, it := range its {
					for _, o := range it.Offerings {
						if unavailable.IsUnavailable(it.Shape, it.Ocpu, it.MemoryInGbs, o.Zone(), o.CapacityType(), "") {
							o.Available = false
						}
					}
				}
				return its, nil
			},
		}

		instanceProvider := &FakeInstanceProvider{
			LaunchInstanceFn: func(_ context.Context, nc *v1.NodeClaim, _ *v1beta1.OCINodeClass,
				it *instancetype.OciInstanceType, _ *image.ImageResolveResult, _ *network.NetworkResolveResult,
				_ *kms.KmsKeyResolveResult, _ *placement.Proposal) (*instance.InstanceInfo, error) {
				return &instance.InstanceInfo{
					Instance: testInstanceWithShape(it.Shape, nc.Labels[v1.NodePoolLabelKey]),
				}, nil
			},
		}

		cp = newUnitTestCloudProvider(unitTestCloudProviderOptions{
			kubeClient:           k8sClient,
			instanceTypeProvider: instanceTypeProvider,
			instanceProvider:     instanceProvider,
		})

		clk := clock.RealClock{}
		recorder := events.NewRecorder(&record.FakeRecorder{})
		cluster = state.NewCluster(clk, k8sClient, cp)
		prov = provisioning.NewProvisioner(k8sClient, recorder, cp, cluster, clk)
		provCtx = coreoptions.ToContext(ctx, coretest.Options())

		pool = fallbackTestNodePool(uniqueName("flex-pool"), nodeClass.Name, 100)
		ExpectApplied(provCtx, k8sClient, nodeClass, pool)

		smallPodResources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1500m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
		}
	})

	AfterEach(func() {
		ExpectDeleted(provCtx, k8sClient, pool, nodeClass)
	})

	It("keeps other CPU/memory configs of a shape schedulable when one config is unavailable", func() {
		// The cheaper small config is preferred while available.
		pod1 := coretest.UnschedulablePod(coretest.PodOptions{ResourceRequirements: smallPodResources})
		bindings := ExpectProvisioned(provCtx, k8sClient, cluster, cp, prov, pod1)
		binding1 := bindings.Get(pod1)
		Expect(binding1).ToNot(BeNil())
		Expect(binding1.NodeClaim).ToNot(BeNil())
		Expect(binding1.NodeClaim.Labels[corev1.LabelInstanceTypeStable]).To(Equal(smallConfig))

		// Mark ONLY the small (2 OCPU / 16 GB) config's on-demand offering out of host capacity.
		unavailable.MarkUnavailable(provCtx, shape, lo.ToPtr(float32(2)), lo.ToPtr(float32(16)),
			zone, v1.CapacityTypeOnDemand, "")

		// The next pod cannot use the small config, but the shape's large config is still available,
		// so scheduling stays on the same shape rather than being globally suppressed.
		pod2 := coretest.UnschedulablePod(coretest.PodOptions{ResourceRequirements: smallPodResources})
		bindings = ExpectProvisioned(provCtx, k8sClient, cluster, cp, prov, pod2)
		binding2 := bindings.Get(pod2)
		Expect(binding2).ToNot(BeNil())
		Expect(binding2.NodeClaim).ToNot(BeNil())
		Expect(binding2.NodeClaim.Labels[corev1.LabelInstanceTypeStable]).To(Equal(largeConfig))
	})
})

// A QuotaExceeded launch failure is an administrator-defined quota for a specific compartment, so it
// must only suppress the offering for NodeClasses launching into that compartment. This suite
// verifies end-to-end that a quota failure launching into one compartment still lets a NodePool
// whose NodeClass targets a different compartment schedule the same shape (rather than being
// globally suppressed the way a tenancy-scoped LimitExceeded or host-capacity failure would be).
var _ = Describe("CloudProvider Compartment Scoping", func() {
	const (
		zone         = "PHX-AD-1"
		compartmentA = "ocid1.compartment.oc1..aaaa"
		compartmentB = "ocid1.compartment.oc1..bbbb"
	)

	var (
		shape             string
		classA            *v1beta1.OCINodeClass
		classB            *v1beta1.OCINodeClass
		unavailable       *ocicache.UnavailableOfferings
		instanceProvider  *FakeInstanceProvider
		cp                *CloudProvider
		cluster           *state.Cluster
		prov              *provisioning.Provisioner
		provCtx           context.Context
		poolA             *v1.NodePool
		poolB             *v1.NodePool
		smallPodResources corev1.ResourceRequirements
	)

	BeforeEach(func() {
		shape = uniqueName("shape")
		unavailable = ocicache.NewUnavailableOfferings(ocicache.UnavailableOfferingsTTL)

		// Both NodeClasses resolve to the SAME shape and differ only by target compartment.
		classA = testReadyNodeClass(uniqueName("class-a"))
		classA.Spec.NodeCompartmentId = lo.ToPtr(compartmentA)
		classB = testReadyNodeClass(uniqueName("class-b"))
		classB.Spec.NodeCompartmentId = lo.ToPtr(compartmentB)

		// Offering availability is gated on the shared unavailable-offerings cache using the same
		// two-scope lookup as production setOfferings: the tenancy-wide entry plus an entry scoped to
		// the NodeClass's target compartment.
		instanceTypeProvider := &FakeInstanceTypeProvider{
			ListFn: func(_ context.Context, nodeClass *v1beta1.OCINodeClass,
				_ []corev1.Taint) ([]*instancetype.OciInstanceType, error) {
				if nodeClass.Name != classA.Name && nodeClass.Name != classB.Name {
					return nil, nil
				}
				compartment := *nodeClass.Spec.NodeCompartmentId
				it := fallbackTestInstanceType(shape, 10)
				for _, o := range it.Offerings {
					if unavailable.IsUnavailable(shape, it.Ocpu, it.MemoryInGbs, o.Zone(), o.CapacityType(), "") ||
						unavailable.IsUnavailable(shape, it.Ocpu, it.MemoryInGbs, o.Zone(), o.CapacityType(),
							compartment) {
						o.Available = false
					}
				}
				return []*instancetype.OciInstanceType{it}, nil
			},
		}

		instanceProvider = &FakeInstanceProvider{
			LaunchInstanceFn: func(_ context.Context, nc *v1.NodeClaim, _ *v1beta1.OCINodeClass,
				it *instancetype.OciInstanceType, _ *image.ImageResolveResult, _ *network.NetworkResolveResult,
				_ *kms.KmsKeyResolveResult, _ *placement.Proposal) (*instance.InstanceInfo, error) {
				return &instance.InstanceInfo{
					Instance: testInstanceWithShape(it.Shape, nc.Labels[v1.NodePoolLabelKey]),
				}, nil
			},
		}

		cp = newUnitTestCloudProvider(unitTestCloudProviderOptions{
			kubeClient:           k8sClient,
			instanceTypeProvider: instanceTypeProvider,
			instanceProvider:     instanceProvider,
		})

		clk := clock.RealClock{}
		recorder := events.NewRecorder(&record.FakeRecorder{})
		cluster = state.NewCluster(clk, k8sClient, cp)
		prov = provisioning.NewProvisioner(k8sClient, recorder, cp, cluster, clk)
		provCtx = coreoptions.ToContext(ctx, coretest.Options())

		// Pool A has the higher weight so the scheduler tries it (compartment A) first.
		poolA = fallbackTestNodePool(uniqueName("pool-a"), classA.Name, 100)
		poolB = fallbackTestNodePool(uniqueName("pool-b"), classB.Name, 10)

		ExpectApplied(provCtx, k8sClient, classA, classB, poolA, poolB)

		smallPodResources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1500m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
		}
	})

	AfterEach(func() {
		ExpectDeleted(provCtx, k8sClient, poolA, poolB, classA, classB)
	})

	It("keeps a different compartment schedulable when a QuotaExceeded failure is compartment-scoped", func() {
		cp.enableServiceLimitFallback = true

		// The launch into compartment A fails with QuotaExceeded. Emulate the production
		// LaunchInstance defer by marking the offering unavailable scoped to compartment A; the
		// FakeInstanceProvider does not run the real defer. CloudProvider.Create classifies this as an
		// InsufficientCapacityError, so no node is created this round.
		instanceProvider.LaunchInstanceFn = func(_ context.Context, nc *v1.NodeClaim,
			nodeClass *v1beta1.OCINodeClass, it *instancetype.OciInstanceType, _ *image.ImageResolveResult,
			_ *network.NetworkResolveResult, _ *kms.KmsKeyResolveResult,
			_ *placement.Proposal) (*instance.InstanceInfo, error) {
			if *nodeClass.Spec.NodeCompartmentId == compartmentA {
				unavailable.MarkUnavailable(provCtx, shape, it.Ocpu, it.MemoryInGbs,
					zone, v1.CapacityTypeOnDemand, compartmentA)
				return nil, fakeServiceError{statusCode: 400, code: "QuotaExceeded",
					message: "compartment quota exceeded"}
			}
			return &instance.InstanceInfo{
				Instance: testInstanceWithShape(it.Shape, nc.Labels[v1.NodePoolLabelKey]),
			}, nil
		}

		// Round 1: the pod schedules onto the higher-weighted pool A (compartment A), but the launch
		// hits the compartment quota, so no binding is made.
		pod := coretest.UnschedulablePod(coretest.PodOptions{ResourceRequirements: smallPodResources})
		bindings := ExpectProvisioned(provCtx, k8sClient, cluster, cp, prov, pod)
		Expect(bindings.Get(pod)).To(BeNil())

		// Core's NodeClaim lifecycle deletes the NodeClaim on InsufficientCapacityError; emulate that
		// so the failed NodeClaim is not counted as in-flight capacity on the next scheduling round.
		for _, nc := range ExpectNodeClaims(provCtx, k8sClient) {
			ExpectDeleted(provCtx, k8sClient, nc)
			cluster.DeleteNodeClaim(nc.Name)
		}

		// Round 2: the offering is cached unavailable ONLY for compartment A. Pool B targets
		// compartment B and shares the same shape, so its offering is still available and the pod
		// schedules there rather than staying pending (which is what would happen if the quota
		// failure were recorded tenancy-wide).
		bindings = ExpectProvisioned(provCtx, k8sClient, cluster, cp, prov, pod)
		binding := bindings.Get(pod)
		Expect(binding).ToNot(BeNil())
		Expect(binding.NodeClaim).ToNot(BeNil())
		Expect(binding.NodeClaim.Labels[v1.NodePoolLabelKey]).To(Equal(poolB.Name))
		Expect(binding.NodeClaim.Labels[corev1.LabelInstanceTypeStable]).To(Equal(shape))
	})
})

// fallbackTestInstanceType builds an instance type with a single on-demand offering plus a pod
// capacity so the core scheduler treats the resulting node as schedulable.
func fallbackTestInstanceType(shape string, price float64) *instancetype.OciInstanceType {
	it := testInstanceType(shape, price)
	it.Capacity[corev1.ResourcePods] = *resource.NewQuantity(110, resource.DecimalSI)
	return it
}

func fallbackTestNodePool(name, nodeClassName string, weight int32) *v1.NodePool {
	return coretest.NodePool(v1.NodePool{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1.NodePoolSpec{
			Weight: lo.ToPtr(weight),
			Template: v1.NodeClaimTemplate{
				Spec: v1.NodeClaimTemplateSpec{
					NodeClassRef: &v1.NodeClassReference{
						Kind:  "OCINodeClass",
						Group: v1beta1.Group,
						Name:  nodeClassName,
					},
					Requirements: []v1.NodeSelectorRequirementWithMinValues{
						{Key: corev1.LabelInstanceTypeStable, Operator: corev1.NodeSelectorOpExists},
						{Key: v1.CapacityTypeLabelKey, Operator: corev1.NodeSelectorOpIn,
							Values: []string{v1.CapacityTypeOnDemand}},
					},
				},
			},
		},
	})
}
