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
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instance"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
)

var networkTestNodeClass v1beta1.OCINodeClass
var networkController *Controller

var _ = Describe("Network Reconciler", func() {
	BeforeEach(func() {
		driftCaches := instance.NewDriftCaches()
		networkTestNodeClass = fakes.CreateBasicOciNodeClass()
		networkTestNodeClass.Spec.NetworkConfig.SecondaryVnicConfigs = []*v1beta1.SecondaryVnicConfig{
			{
				SimpleVnicConfig: v1beta1.SimpleVnicConfig{
					SubnetAndNsgConfig: &v1beta1.SubnetAndNsgConfig{
						SubnetConfig: &v1beta1.SubnetConfig{
							SubnetId: lo.ToPtr("testSubnetId"),
						},
					},
					VnicDisplayName: lo.ToPtr("testPrimaryVnic"),
				},
			},
		}

		vcnClient := fakes.NewFakeVirtualNetworkClient()
		networkProvider := lo.Must(network.NewProvider(ctx, "clusterVcnCompartmentId",
			true, []network.IpFamily{network.IPv4}, vcnClient, driftCaches.VnicCache()))

		networkController = &Controller{
			Client:   k8sClient,
			recorder: &fakes.FakeEventRecorder{},
			reconcilers: []nodeClassReconciler{
				&InitStatus{},
				&NetworkReconciler{
					networkProvider: networkProvider,
				},
			},
		}
	})

	It("should set true if vnic and nsg resolve successfully", func() {
		networkTestNodeClass.Spec.NetworkConfig.PrimaryVnicConfig.
			SubnetAndNsgConfig.SubnetConfig.SubnetId = lo.ToPtr("subnet-ipv4")
		networkTestNodeClass.Spec.NetworkConfig.PrimaryVnicConfig.
			SubnetAndNsgConfig.NetworkSecurityGroupConfigs = []*v1beta1.NetworkSecurityGroupConfig{
			{NetworkSecurityGroupId: lo.ToPtr("nsg-vcn1-a")},
		}
		networkTestNodeClass.Spec.NetworkConfig.SecondaryVnicConfigs[0].
			SubnetAndNsgConfig.SubnetConfig.SubnetId = lo.ToPtr("subnet-private")
		networkTestNodeClass.Spec.NetworkConfig.SecondaryVnicConfigs[0].
			SubnetAndNsgConfig.NetworkSecurityGroupConfigs = []*v1beta1.NetworkSecurityGroupConfig{
			{NetworkSecurityGroupId: lo.ToPtr("nsg-vcn1-a")},
		}

		nodeClassPtr := &networkTestNodeClass
		ExpectApplied(ctx, k8sClient, nodeClassPtr)
		ExpectObjectReconciled(ctx, k8sClient, networkController, nodeClassPtr)
		nodeClassPtr = ExpectExists(ctx, k8sClient, nodeClassPtr)

		Expect(nodeClassPtr.GetConditions()).ToNot(BeEmpty())
		_, found := lo.Find(nodeClassPtr.GetConditions(), func(item status.Condition) bool {
			return item.Type == v1beta1.ConditionTypeNetworkReady && item.Status == metav1.ConditionTrue
		})
		Expect(found).To(BeTrue())
	})

	It("should set false if primary vnic not found", func() {
		networkTestNodeClass.Spec.NetworkConfig.PrimaryVnicConfig.
			SubnetAndNsgConfig.SubnetConfig.SubnetId = lo.ToPtr("dummyVncId")
		networkTestNodeClass.Spec.NetworkConfig.SecondaryVnicConfigs[0].
			SubnetAndNsgConfig.SubnetConfig.SubnetId = lo.ToPtr("subnet-private")

		reconcileAndVerifyFailure(networkTestNodeClass, "primaryVnic: subnet not found")
	})

	It("should set false if secondary vnic not found", func() {
		networkTestNodeClass.Spec.NetworkConfig.PrimaryVnicConfig.
			SubnetAndNsgConfig.SubnetConfig.SubnetId = lo.ToPtr("subnet-ipv4")
		networkTestNodeClass.Spec.NetworkConfig.SecondaryVnicConfigs[0].
			SubnetAndNsgConfig.SubnetConfig.SubnetId = lo.ToPtr("dummyVncId")

		reconcileAndVerifyFailure(networkTestNodeClass, "second vnic 0: subnet not found")
	})

	It("should set false if primary vnic nsg notfound", func() {
		networkTestNodeClass.Spec.NetworkConfig.PrimaryVnicConfig.
			SubnetAndNsgConfig.SubnetConfig.SubnetId = lo.ToPtr("subnet-ipv4")
		networkTestNodeClass.Spec.NetworkConfig.PrimaryVnicConfig.
			SubnetAndNsgConfig.NetworkSecurityGroupConfigs = []*v1beta1.NetworkSecurityGroupConfig{
			{NetworkSecurityGroupId: lo.ToPtr("dummyNsgId")},
		}
		networkTestNodeClass.Spec.NetworkConfig.SecondaryVnicConfigs[0].
			SubnetAndNsgConfig.SubnetConfig.SubnetId = lo.ToPtr("subnet-private")
		networkTestNodeClass.Spec.NetworkConfig.SecondaryVnicConfigs[0].
			SubnetAndNsgConfig.NetworkSecurityGroupConfigs = []*v1beta1.NetworkSecurityGroupConfig{
			{NetworkSecurityGroupId: lo.ToPtr("nsg-vcn1-a")},
		}

		reconcileAndVerifyFailure(networkTestNodeClass, "primaryVnic: NSG not found")
	})

	It("should set false if secondary vnic nsg notfound", func() {
		networkTestNodeClass.Spec.NetworkConfig.PrimaryVnicConfig.
			SubnetAndNsgConfig.SubnetConfig.SubnetId = lo.ToPtr("subnet-ipv4")
		networkTestNodeClass.Spec.NetworkConfig.PrimaryVnicConfig.
			SubnetAndNsgConfig.NetworkSecurityGroupConfigs = []*v1beta1.NetworkSecurityGroupConfig{
			{NetworkSecurityGroupId: lo.ToPtr("nsg-vcn1-a")},
		}
		networkTestNodeClass.Spec.NetworkConfig.SecondaryVnicConfigs[0].
			SubnetAndNsgConfig.SubnetConfig.SubnetId = lo.ToPtr("subnet-private")
		networkTestNodeClass.Spec.NetworkConfig.SecondaryVnicConfigs[0].
			SubnetAndNsgConfig.NetworkSecurityGroupConfigs = []*v1beta1.NetworkSecurityGroupConfig{
			{NetworkSecurityGroupId: lo.ToPtr("dummyNsgId")},
		}

		reconcileAndVerifyFailure(networkTestNodeClass, "second vnic 0: NSG not found")
	})
})

func reconcileAndVerifyFailure(nodeClass v1beta1.OCINodeClass, errorMsg string) {
	nodeClassPtr := &nodeClass
	ExpectApplied(ctx, k8sClient, nodeClassPtr)
	ExpectObjectReconciled(ctx, k8sClient, networkController, nodeClassPtr)
	nodeClassPtr = ExpectExists(ctx, k8sClient, nodeClassPtr)

	Expect(nodeClassPtr.GetConditions()).ToNot(BeEmpty())
	condition, found := lo.Find(nodeClassPtr.GetConditions(), func(item status.Condition) bool {
		return item.Type == v1beta1.ConditionTypeNetworkReady && item.Status == metav1.ConditionFalse
	})
	Expect(found).To(BeTrue())
	Expect(condition.Reason).To(Equal(v1beta1.ConditionNetworkNotReadyReason))
	Expect(condition.Message).To(Equal(errorMsg))
}
