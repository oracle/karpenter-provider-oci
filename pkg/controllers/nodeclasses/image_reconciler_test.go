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
	"github.com/oracle/karpenter-provider-oci/pkg/providers/image"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
)

var imageTestNodeClass v1beta1.OCINodeClass
var imageController *Controller

var _ = Describe("Image Reconciler", func() {
	BeforeEach(func() {
		imageTestNodeClass = fakes.CreateBasicOciNodeClass()

		fakeComputeClient := fakes.NewFakeComputeClient("testClusterCompartmentId")
		imageProvider := lo.Must(image.NewProvider(ctx, nil, fakeComputeClient,
			"testPreBakedCompartmentId", "", fakes.NewDummyChannel(), false, 0))

		imageController = &Controller{
			Client:   k8sClient,
			recorder: &fakes.FakeEventRecorder{},
			reconcilers: []nodeClassReconciler{
				&InitStatus{},
				&ImageReconciler{
					imageProvider: imageProvider,
				},
			},
		}
	})

	It("should set Image condition to true if image found", func() {
		imageTestNodeClass.Spec.VolumeConfig.BootVolumeConfig.ImageConfig.ImageId = lo.ToPtr("ocid1.image.123")

		nodeClassPtr := &imageTestNodeClass
		ExpectApplied(ctx, k8sClient, nodeClassPtr)
		ExpectObjectReconciled(ctx, k8sClient, imageController, nodeClassPtr)
		nodeClassPtr = ExpectExists(ctx, k8sClient, nodeClassPtr)

		Expect(nodeClassPtr.GetConditions()).ToNot(BeEmpty())
		_, found := lo.Find(nodeClassPtr.GetConditions(), func(item status.Condition) bool {
			return item.Type == v1beta1.ConditionTypeImageReady && item.Status == metav1.ConditionTrue
		})
		Expect(found).To(BeTrue())

		_, imageFound := lo.Find(nodeClassPtr.Status.Volume.ImageCandidates, func(item *v1beta1.Image) bool {
			return item.ImageId == "ocid1.image.123"
		})
		Expect(imageFound).To(BeTrue())
	})

	It("should set Image condition to false if image not found", func() {
		imageTestNodeClass.Spec.VolumeConfig.BootVolumeConfig.ImageConfig.ImageId = lo.ToPtr("dummyImage")

		nodeClassPtr := &imageTestNodeClass
		ExpectApplied(ctx, k8sClient, nodeClassPtr)
		ExpectObjectReconciled(ctx, k8sClient, imageController, nodeClassPtr)
		nodeClassPtr = ExpectExists(ctx, k8sClient, nodeClassPtr)

		Expect(nodeClassPtr.GetConditions()).ToNot(BeEmpty())
		_, found := lo.Find(nodeClassPtr.GetConditions(), func(item status.Condition) bool {
			return item.Type == v1beta1.ConditionTypeImageReady && item.Status == metav1.ConditionFalse
		})
		Expect(found).To(BeTrue())
	})
})
