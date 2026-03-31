/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package nodeclasses

import (
	"github.com/awslabs/operatorpkg/status"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
)

var hashTestNodeClass v1beta1.OCINodeClass
var hashController *Controller

var _ = Describe("Hash Reconciler", func() {
	BeforeEach(func() {
		hashTestNodeClass = fakes.CreateBasicOciNodeClass()

		hashController = &Controller{
			Client:   k8sClient,
			recorder: &fakes.FakeEventRecorder{},
			reconcilers: []nodeClassReconciler{
				&Hash{},
			},
		}
	})

	It("should create hash", func() {
		hashTestNodeClass.Status = v1beta1.OCINodeClassStatus{
			Conditions: []status.Condition{
				{
					Status:             metav1.ConditionTrue,
					Type:               status.ConditionReady,
					Reason:             status.ConditionReady,
					LastTransitionTime: metav1.Now(),
				},
				{
					Status:             metav1.ConditionTrue,
					Type:               v1beta1.ConditionTypeImageReady,
					Reason:             v1beta1.ConditionTypeImageReady,
					LastTransitionTime: metav1.Now(),
				},
				{
					Status:             metav1.ConditionTrue,
					Type:               v1beta1.ConditionTypeImageReady,
					Reason:             v1beta1.ConditionTypeImageReady,
					LastTransitionTime: metav1.Now(),
				},
				{
					Status:             metav1.ConditionTrue,
					Type:               v1beta1.ConditionTypeNetworkReady,
					Reason:             v1beta1.ConditionTypeNetworkReady,
					LastTransitionTime: metav1.Now(),
				},
				{
					Status:             metav1.ConditionTrue,
					Type:               v1beta1.ConditionTypeNodeCompartment,
					Reason:             v1beta1.ConditionTypeNodeCompartment,
					LastTransitionTime: metav1.Now(),
				}},
		}

		nodeClassPtr := &hashTestNodeClass
		ExpectApplied(ctx, k8sClient, nodeClassPtr)
		ExpectObjectReconciled(ctx, k8sClient, hashController, nodeClassPtr)
		nodeClassPtr = ExpectExists(ctx, k8sClient, nodeClassPtr)

		Expect(nodeClassPtr).NotTo(BeNil())
		Expect(nodeClassPtr.Annotations).NotTo(BeNil())
		Expect(nodeClassPtr.Annotations[v1beta1.NodeClassHash]).NotTo(BeNil())
		Expect(nodeClassPtr.Annotations[v1beta1.NodeClassHashVersion]).To(Equal(v1beta1.OCINodeClassHashVersion))
	})

	It("should not create hash if nodeClass not ready", func() {

		nodeClassPtr := &hashTestNodeClass
		ExpectApplied(ctx, k8sClient, nodeClassPtr)
		ExpectObjectReconciled(ctx, k8sClient, hashController, nodeClassPtr)
		nodeClassPtr = ExpectExists(ctx, k8sClient, nodeClassPtr)

		Expect(nodeClassPtr).NotTo(BeNil())
		Expect(nodeClassPtr.Annotations).To(BeNil())
	})
})
