/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package nodeclasses

import (
	"github.com/awslabs/operatorpkg/status"
	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/capacityreservation"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/clusterplacementgroup"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/computecluster"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/identity"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/image"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instance"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/kms"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
)

var ociTestNodeClass v1beta1.OCINodeClass
var ociNodeClassController *Controller
var nodeClassClusterCompartmentId = "ocid1.compartment.oc1..cluster123"

var _ = Describe("OCINodeClass Reconciler", func() {
	BeforeEach(func() {
		driftCaches := instance.NewDriftCaches()

		ociTestNodeClass = fakes.CreateOciNodeClassWithMinimumReconcilableSetting(nodeClassClusterCompartmentId)

		imageProvider := lo.Must(image.NewProvider(ctx, nil, fakes.NewFakeComputeClient(nodeClassClusterCompartmentId),
			"testPreBakedCompartmentId", "", fakes.NewDummyChannel(), false, 0))

		kmsProvider := lo.Must(kms.NewProvider(ctx, nodeClassClusterCompartmentId, fakes.NewDummyConfigurationProvider()))
		kmsProvider.SetKmsClient("https://testvalut-management.kms.us-ashburn-1.oraclecloud.com", fakes.NewFakeKmsClient())

		networkProvider := lo.Must(network.NewProvider(ctx, nodeClassClusterCompartmentId,
			false, []network.IpFamily{network.IPv4}, fakes.NewFakeVirtualNetworkClient(),
			driftCaches.VnicCache()))
		crProvider := capacityreservation.NewProvider(ctx,
			fakes.NewFakeCapacityReservationClient(nodeClassClusterCompartmentId), nodeClassClusterCompartmentId)
		computeClusterProvider := computecluster.NewProvider(ctx,
			fakes.NewFakeComputeClient(nodeClassClusterCompartmentId), nodeClassClusterCompartmentId)
		identityProvider := lo.Must(identity.NewProvider(ctx, nodeClassClusterCompartmentId, fakes.NewFakeIdentityClient()))
		cpgProvider := clusterplacementgroup.NewProvider(ctx, fakes.NewFakeClusterPlacementGroupClient(
			nodeClassClusterCompartmentId), nodeClassClusterCompartmentId)

		ociNodeClassController = lo.Must(NewController(ctx, k8sClient,
			&fakes.FakeEventRecorder{},
			imageProvider,
			0,
			kmsProvider,
			networkProvider,
			crProvider,
			computeClusterProvider,
			identityProvider,
			cpgProvider))
	})

	It("full ocinodeclass controller should reconcile a single nodeclass successfully ", func() {

		nodeClassPtr := &ociTestNodeClass
		ExpectApplied(ctx, k8sClient, nodeClassPtr)
		ExpectObjectReconciled(ctx, k8sClient, ociNodeClassController, nodeClassPtr)
		nodeClassPtr = ExpectExists(ctx, k8sClient, nodeClassPtr)

		Expect(nodeClassPtr.GetConditions()).ToNot(BeEmpty())
		_, found := lo.Find(nodeClassPtr.GetConditions(), func(item status.Condition) bool {
			return item.Type == v1beta1.ConditionTypeReady && item.Status == metav1.ConditionTrue
		})
		Expect(found).To(BeTrue())
	})
})
