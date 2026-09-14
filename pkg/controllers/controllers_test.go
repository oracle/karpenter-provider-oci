/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package controllers

import (
	"context"
	"testing"

	"github.com/awslabs/operatorpkg/controller"
	"github.com/oracle/karpenter-provider-oci/pkg/controllers/capacitydiscovery"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	"github.com/oracle/karpenter-provider-oci/pkg/operator/options"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/capacityreservation"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/clusterplacementgroup"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/computecluster"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/identity"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/image"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instance"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/kms"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	"github.com/samber/lo"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controllers Suite")
}

var _ = Describe("OCINodeClass Reconciler", func() {
	It("should create new controllers successfully", func() {
		ctx := options.ToContext(context.TODO(), &options.Options{})
		driftCaches := instance.NewDriftCaches()

		nodeClassClusterCompartmentId := "ocid1.compartment.oc1..cluster123"

		imageProvider := lo.Must(image.NewProvider(ctx, nil, fakes.NewFakeComputeClient(nodeClassClusterCompartmentId),
			"testPreBakedCompartmentId", "", fakes.NewDummyChannel()))

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

		newControllers := func(discoveryEnabled bool) []controller.Controller {
			return NewControllers(ctx, nil, nil, nil, fake.NewClientset(),
				&fakes.FakeEventRecorder{}, imageProvider, kmsProvider, networkProvider, crProvider,
				computeClusterProvider, identityProvider, cpgProvider, &fakes.FakeCloudProvider{},
				&fakeCapacityProvider{enabled: discoveryEnabled},
			)
		}

		hasCapacityController := func(cs []controller.Controller) bool {
			return lo.ContainsBy(cs, func(c controller.Controller) bool {
				_, ok := c.(*capacitydiscovery.Controller)
				return ok
			})
		}

		Expect(newControllers(true)).To(HaveLen(3))
		Expect(hasCapacityController(newControllers(true))).To(BeTrue())

		// Disabling capacity discovery must remove its controller, not merely neuter it: left
		// registered it would keep watching nodes and reading NodeClaims and NodeClasses to
		// produce measurements nothing would store. Assert which controller went, so that
		// dropping a different one would not pass.
		Expect(newControllers(false)).To(HaveLen(2))
		Expect(hasCapacityController(newControllers(false))).To(BeFalse())
	})
})

// fakeCapacityProvider stands in for the instance type provider's capacity discovery, which this
// test does not exercise; it only needs NewControllers to wire something in.
type fakeCapacityProvider struct{ enabled bool }

func (f *fakeCapacityProvider) DiscoveryEnabled() bool { return f.enabled }

func (f *fakeCapacityProvider) UpdateInstanceTypeCapacityFromNode(_ context.Context, _ *corev1.Node,
	_ *karpv1.NodeClaim) error {
	return nil
}
