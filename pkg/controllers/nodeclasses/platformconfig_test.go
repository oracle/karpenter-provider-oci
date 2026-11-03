/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package nodeclasses

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	"github.com/samber/lo"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	. "sigs.k8s.io/karpenter/pkg/test/expectations"
)

var platformBooleanFields = []string{
	"secureBootEnabled", "trustedPlatformModuleEnabled", "measuredBootEnabled", "memoryEncryptionEnabled",
	"symmetricMultiThreadingEnabled", "accessControlServiceEnabled", "virtualInstructionsEnabled",
	"inputOutputMemoryManagementUnitEnabled",
}

var _ = Describe("PlatformConfig API", func() {
	// Create raw objects so missing CRD fields cannot be hidden by typed client serialization.
	create := func(request any, omitted bool) (*unstructured.Unstructured, error) {
		nc := fakes.CreateBasicOciNodeClass()
		raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&nc)
		Expect(err).NotTo(HaveOccurred())
		obj := &unstructured.Unstructured{Object: raw}
		if !omitted {
			Expect(unstructured.SetNestedField(obj.Object, request, "spec", "platformConfig")).To(Succeed())
		}
		err = k8sClient.Create(ctx, obj)
		if err == nil {
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, obj))).To(Succeed()) })
		}
		return obj, err
	}
	get := func(obj *unstructured.Unstructured) *v1beta1.OCINodeClass {
		stored := &unstructured.Unstructured{}
		stored.SetGroupVersionKind(obj.GroupVersionKind())
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), stored)).To(Succeed())
		nc := &v1beta1.OCINodeClass{}
		Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(stored.Object, nc)).To(Succeed())
		return nc
	}
	It("maps all ten JSON fields to their typed API fields", func() {
		obj, err := create(map[string]any{
			"secureBootEnabled": true, "trustedPlatformModuleEnabled": false, "measuredBootEnabled": true,
			"memoryEncryptionEnabled": false, "symmetricMultiThreadingEnabled": true, "accessControlServiceEnabled": false,
			"virtualInstructionsEnabled": true, "inputOutputMemoryManagementUnitEnabled": false,
			"numaNodesPerSocket": "NPS6", "percentageOfCoresEnabled": int64(50),
		}, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(get(obj).Spec.PlatformConfig).To(Equal(&v1beta1.PlatformConfig{
			SecureBootEnabled:              lo.ToPtr(true),
			TrustedPlatformModuleEnabled:   lo.ToPtr(false),
			MeasuredBootEnabled:            lo.ToPtr(true),
			MemoryEncryptionEnabled:        lo.ToPtr(false),
			SymmetricMultiThreadingEnabled: lo.ToPtr(true),
			AccessControlServiceEnabled:    lo.ToPtr(false),
			VirtualInstructionsEnabled:     lo.ToPtr(true), InputOutputMemoryManagementUnitEnabled: lo.ToPtr(false),
			NumaNodesPerSocket: lo.ToPtr("NPS6"), PercentageOfCoresEnabled: lo.ToPtr(50),
		}))
	})

	DescribeTable("normalizes empty forms without defaults", func(request any, omitted bool) {
		obj, err := create(request, omitted)
		Expect(err).NotTo(HaveOccurred())
		Expect(get(obj).Spec.PlatformConfig.IsEmpty()).To(BeTrue())
	},
		Entry("omitted", nil, true),
		Entry("null", nil, false),
		Entry("empty", map[string]any{}, false),
		Entry("all null", map[string]any{
			"secureBootEnabled": nil, "trustedPlatformModuleEnabled": nil, "measuredBootEnabled": nil,
			"memoryEncryptionEnabled": nil, "symmetricMultiThreadingEnabled": nil, "accessControlServiceEnabled": nil,
			"virtualInstructionsEnabled": nil, "inputOutputMemoryManagementUnitEnabled": nil,
			"numaNodesPerSocket": nil, "percentageOfCoresEnabled": nil,
		}, false),
	)

	for _, field := range platformBooleanFields {
		for _, value := range []bool{false, true} {
			It(fmt.Sprintf("preserves %s=%t", field, value), func() {
				obj, err := create(map[string]any{field: value}, false)
				Expect(err).NotTo(HaveOccurred())
				stored := get(obj)
				// Independent typed expectations catch incorrect JSON-to-Go field mappings.
				want := map[string]*v1beta1.PlatformConfig{
					"secureBootEnabled":                      {SecureBootEnabled: lo.ToPtr(value)},
					"trustedPlatformModuleEnabled":           {TrustedPlatformModuleEnabled: lo.ToPtr(value)},
					"measuredBootEnabled":                    {MeasuredBootEnabled: lo.ToPtr(value)},
					"memoryEncryptionEnabled":                {MemoryEncryptionEnabled: lo.ToPtr(value)},
					"symmetricMultiThreadingEnabled":         {SymmetricMultiThreadingEnabled: lo.ToPtr(value)},
					"accessControlServiceEnabled":            {AccessControlServiceEnabled: lo.ToPtr(value)},
					"virtualInstructionsEnabled":             {VirtualInstructionsEnabled: lo.ToPtr(value)},
					"inputOutputMemoryManagementUnitEnabled": {InputOutputMemoryManagementUnitEnabled: lo.ToPtr(value)},
				}
				Expect(stored.Spec.PlatformConfig).To(Equal(want[field]))
				Expect(stored.Spec.PlatformConfig.IsEmpty()).To(BeFalse())
			})
		}
		It("rejects a nonboolean "+field, func() {
			_, err := create(map[string]any{field: "false"}, false)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%v", err)
		})
	}

	DescribeTable("preserves supported NUMA values", func(value string) {
		obj, err := create(map[string]any{"numaNodesPerSocket": value}, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(get(obj).Spec.PlatformConfig.NumaNodesPerSocket).To(Equal(lo.ToPtr(value)))
	}, Entry("NPS0", "NPS0"), Entry("NPS1", "NPS1"), Entry("NPS2", "NPS2"), Entry("NPS4", "NPS4"), Entry("NPS6", "NPS6"))

	DescribeTable("preserves supported core percentages", func(value int64) {
		obj, err := create(map[string]any{"percentageOfCoresEnabled": value}, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(get(obj).Spec.PlatformConfig.PercentageOfCoresEnabled).To(Equal(lo.ToPtr(int(value))))
	}, Entry("25", int64(25)), Entry("50", int64(50)), Entry("75", int64(75)), Entry("100", int64(100)))

	DescribeTable("rejects invalid values and types", func(request any) {
		_, err := create(request, false)
		Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%v", err)
	},
		Entry("object type", "invalid"), Entry("NUMA type", map[string]any{"numaNodesPerSocket": true}),
		Entry("NPS3", map[string]any{"numaNodesPerSocket": "NPS3"}),
		Entry("percentage type", map[string]any{"percentageOfCoresEnabled": "25"}),
		Entry("fraction", map[string]any{"percentageOfCoresEnabled": float64(25.5)}),
		Entry("0", map[string]any{"percentageOfCoresEnabled": int64(0)}),
		Entry("24", map[string]any{"percentageOfCoresEnabled": int64(24)}),
		Entry("26", map[string]any{"percentageOfCoresEnabled": int64(26)}),
		Entry("101", map[string]any{"percentageOfCoresEnabled": int64(101)}),
	)

	for mask := 1; mask < 8; mask++ {
		It(fmt.Sprintf("rejects encryption with Shielded combination %d", mask), func() {
			request := map[string]any{"memoryEncryptionEnabled": true}
			for i, field := range platformBooleanFields[:3] {
				request[field] = mask&(1<<i) != 0
			}
			_, err := create(request, false)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%v", err)
		})
	}
	DescribeTable("accepts explicitly disabled encryption conflict partners", func(request map[string]any) {
		obj, err := create(request, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(get(obj).Spec.PlatformConfig.IsEmpty()).To(BeFalse())
	},
		Entry("all Shielded disabled",
			map[string]any{"memoryEncryptionEnabled": true,
				"secureBootEnabled":            false,
				"trustedPlatformModuleEnabled": false,
				"measuredBootEnabled":          false}),
		Entry("encryption disabled",
			map[string]any{"memoryEncryptionEnabled": false,
				"secureBootEnabled":            true,
				"trustedPlatformModuleEnabled": true,
				"measuredBootEnabled":          true}),
	)
})

var _ = Describe("PlatformConfig DeepCopy", func() {
	It("copies the nested platform configuration without sharing pointers", func() {
		nc := fakes.CreateBasicOciNodeClass()
		nc.Spec.PlatformConfig = &v1beta1.PlatformConfig{SecureBootEnabled: lo.ToPtr(false)}
		copied := nc.DeepCopy()
		Expect(copied.Spec.PlatformConfig).To(Equal(nc.Spec.PlatformConfig))
		Expect(copied.Spec.PlatformConfig).NotTo(BeIdenticalTo(nc.Spec.PlatformConfig))
		*nc.Spec.PlatformConfig.SecureBootEnabled = true
		Expect(*copied.Spec.PlatformConfig.SecureBootEnabled).To(BeFalse())
	})
})

var _ = Describe("Hash Reconciler PlatformConfig", func() {
	It("updates a Ready NodeClass hash after an edit and restores it on removal", func() {
		nc := fakes.CreateBasicOciNodeClass()
		ExpectApplied(ctx, k8sClient, &nc)
		DeferCleanup(func() {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&nc), &nc)).To(Succeed())
			original := nc.DeepCopy()
			nc.Finalizers = nil
			nc.Spec.PlatformConfig = nil
			Expect(k8sClient.Patch(ctx, &nc, client.MergeFrom(original))).To(Succeed())
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &nc))).To(Succeed())
		})
		// Readiness is derived from dependent conditions; setting only Ready is reset to Unknown.
		for _, condition := range []string{v1beta1.ConditionTypeImageReady,
			v1beta1.ConditionTypeNetworkReady,
			v1beta1.ConditionTypeNodeCompartment} {
			nc.StatusConditions().SetTrue(condition)
		}
		Expect(nc.StatusConditions().Root().IsTrue()).To(BeTrue())
		Expect(k8sClient.Status().Update(ctx, &nc)).To(Succeed())
		controller := &Controller{Client: k8sClient,
			recorder:    &fakes.FakeEventRecorder{},
			reconcilers: []nodeClassReconciler{&Hash{}}}
		ExpectObjectReconciled(ctx, k8sClient, controller, &nc)
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&nc), &nc)).To(Succeed())
		legacyHash := nc.Annotations[v1beta1.NodeClassHash]
		Expect(legacyHash).NotTo(BeEmpty())
		original := nc.DeepCopy()
		nc.Spec.PlatformConfig = &v1beta1.PlatformConfig{SecureBootEnabled: lo.ToPtr(false)}
		Expect(k8sClient.Patch(ctx, &nc, client.MergeFrom(original))).To(Succeed())
		ExpectObjectReconciled(ctx, k8sClient, controller, &nc)
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&nc), &nc)).To(Succeed())
		Expect(nc.Annotations[v1beta1.NodeClassHash]).NotTo(Equal(legacyHash))
		Expect(nc.Annotations[v1beta1.NodeClassHashVersion]).To(Equal("v1"))
		original = nc.DeepCopy()
		nc.Spec.PlatformConfig = nil
		Expect(k8sClient.Patch(ctx, &nc, client.MergeFrom(original))).To(Succeed())
		ExpectObjectReconciled(ctx, k8sClient, controller, &nc)
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&nc), &nc)).To(Succeed())
		Expect(nc.Annotations[v1beta1.NodeClassHash]).To(Equal(legacyHash))
	})
})
