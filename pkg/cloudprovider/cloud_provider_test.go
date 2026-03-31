/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package cloudprovider

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/controllers/nodeclasses"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
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
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
)

var ociTestNodeClass v1beta1.OCINodeClass
var nodeClassClusterCompartmentId = "ocid1.compartment.oc1..cluster123"
var cloudProvider *CloudProvider
var ociNodeClassController *nodeclasses.Controller
var _ = Describe("CloudProvider Tests", func() {
	testShape := "VM.Standard.E4.Flex"
	BeforeEach(func() {
		ipV4Family := []network.IpFamily{network.IPv4}

		ociTestNodeClass = fakes.CreateOciNodeClassWithMinimumReconcilableSetting(nodeClassClusterCompartmentId)
		imageProvider := lo.Must(image.NewProvider(ctx, nil, fakes.NewFakeComputeClient(nodeClassClusterCompartmentId),
			"testPreBakedCompartmentId", "", fakes.NewDummyChannel()))
		kmsProvider := lo.Must(kms.NewProvider(ctx, nodeClassClusterCompartmentId, fakes.NewDummyConfigurationProvider()))
		kmsProvider.SetKmsClient("https://testvalut-management.kms.us-ashburn-1.oraclecloud.com", fakes.NewFakeKmsClient())
		networkProvider := lo.Must(network.NewProvider(ctx, nodeClassClusterCompartmentId,
			false, ipV4Family, fakes.NewFakeVirtualNetworkClient()))
		crProvider := capacityreservation.NewProvider(ctx,
			fakes.NewFakeCapacityReservationClient(nodeClassClusterCompartmentId), nodeClassClusterCompartmentId)
		computeClusterProvider := computecluster.NewProvider(ctx,
			fakes.NewFakeComputeClient(nodeClassClusterCompartmentId), nodeClassClusterCompartmentId)
		identityProvider := lo.Must(identity.NewProvider(ctx, nodeClassClusterCompartmentId, fakes.NewFakeIdentityClient()))
		cpgProvider := clusterplacementgroup.NewProvider(ctx, fakes.NewFakeClusterPlacementGroupClient(
			nodeClassClusterCompartmentId), nodeClassClusterCompartmentId)
		placementProvider := lo.Must(placement.NewProvider(ctx, crProvider, computeClusterProvider,
			cpgProvider, identityProvider))
		npnProvider := lo.Must(npn.NewProvider(ctx, false, ipV4Family))
		instancetypeProvider := NewFakeInstanceTypeProvider([]*instancetype.OciInstanceType{
			{
				InstanceType: cloudprovider.InstanceType{
					Name: testShape,
					Requirements: scheduling.NewRequirements(
						scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, testShape),
					),
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    *resource.NewMilliQuantity(2000, resource.DecimalSI),
						corev1.ResourceMemory: *resource.NewQuantity(8*1024*1024*1024, resource.BinarySI),
					},
					Overhead: &cloudprovider.InstanceTypeOverhead{},
					Offerings: []*cloudprovider.Offering{
						{
							Requirements: scheduling.NewRequirements(
								scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, testShape),
							),
							Price:               float64(1),
							Available:           true,
							ReservationCapacity: 1,
						},
					},
				},
				Shape: testShape,
			},
		})
		instanceProvider := NewFakeInstanceProvider(&instance.InstanceInfo{
			Instance: &ocicore.Instance{
				Id:                 lo.ToPtr("test-instance-ocid"),
				DisplayName:        lo.ToPtr("test-instance"),
				AvailabilityDomain: lo.ToPtr("aumf:PHX-AD-1"),
				FaultDomain:        lo.ToPtr("fd1"),
				Shape:              lo.ToPtr(testShape),
				TimeCreated: &common.SDKTime{
					Time: time.Now(),
				},
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
				Requirements: []v1.NodeSelectorRequirementWithMinValues{
					{
						NodeSelectorRequirement: corev1.NodeSelectorRequirement{
							Key:      corev1.LabelInstanceTypeStable,
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{testShape},
						},
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
			// shapeC spot offering is in same price as shapeB ondemand
			createTestSpotAndOnDemandOfferings(shapeC, 28.0),
		}
		// Use empty requirements to allow all offerings
		reqs := scheduling.NewRequirements()

		sorted := orderInstanceTypesByPrice(instanceTypes, reqs)

		Expect(sorted).To(HaveLen(6))
		// The sorted list should be ordered by price: A(spot=5), B(spot=7), A(on-demand=10), B(on-demand=14)
		Expect(sorted[0].InstanceType.Name).To(Equal(shapeA))
		Expect(sorted[0].Offerings[0].Requirements.Get(v1.CapacityTypeLabelKey).Any()).
			To(Equal(v1.CapacityTypeSpot))
		Expect(sorted[0].Offerings[0].Price).To(Equal(5.0))

		Expect(sorted[1].InstanceType.Name).To(Equal(shapeB))
		Expect(sorted[1].Offerings[0].Requirements.Get(v1.CapacityTypeLabelKey).Any()).
			To(Equal(v1.CapacityTypeSpot))
		Expect(sorted[1].Offerings[0].Price).To(Equal(7.0))

		Expect(sorted[2].InstanceType.Name).To(Equal(shapeA))
		Expect(sorted[2].Offerings[0].Requirements.Get(v1.CapacityTypeLabelKey).Any()).
			To(Equal(v1.CapacityTypeOnDemand))
		Expect(sorted[2].Offerings[0].Price).To(Equal(10.0))

		Expect(sorted[3].InstanceType.Name).To(Equal(shapeB))
		Expect(sorted[3].Offerings[0].Requirements.Get(v1.CapacityTypeLabelKey).Any()).
			To(Equal(v1.CapacityTypeOnDemand))
		Expect(sorted[3].Offerings[0].Price).To(Equal(14.0))

		Expect(sorted[4].InstanceType.Name).To(Equal(shapeC))
		Expect(sorted[4].Offerings[0].Requirements.Get(v1.CapacityTypeLabelKey).Any()).
			To(Equal(v1.CapacityTypeSpot))
		Expect(sorted[4].Offerings[0].Price).To(Equal(14.0))

		Expect(sorted[5].InstanceType.Name).To(Equal(shapeC))
		Expect(sorted[5].Offerings[0].Requirements.Get(v1.CapacityTypeLabelKey).Any()).
			To(Equal(v1.CapacityTypeOnDemand))
		Expect(sorted[5].Offerings[0].Price).To(Equal(28.0))
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
