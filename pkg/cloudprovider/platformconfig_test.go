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
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	ocicache "github.com/oracle/karpenter-provider-oci/pkg/cache"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/capacityreservation"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/identity"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/image"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instance"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancetype"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/kms"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/placement"
	"github.com/oracle/karpenter-provider-oci/pkg/utils"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	ociidentity "github.com/oracle/oci-go-sdk/v65/identity"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	discoveryfake "k8s.io/client-go/discovery/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/controllers/disruption"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/events"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/utils/pdb"
)

var _ = Describe("PlatformConfig Create and Lifecycle", func() {
	const shape = "VM.Standard.E4.8"
	const platformType ocicore.ShapePlatformConfigOptionsTypeEnum = "HYPOTHETICAL_FUTURE_VM"
	var (
		cp                          *CloudProvider
		nodeClass                   *v1beta1.OCINodeClass
		pool                        *v1.NodePool
		claim                       *v1.NodeClaim
		unavailable                 *ocicache.UnavailableOfferings
		launchCalls, placementCalls int
	)

	BeforeEach(func() {
		nodeClass = testReadyNodeClass(uniqueName("platform"))
		nodeClass.Spec.PlatformConfig = &v1beta1.PlatformConfig{SymmetricMultiThreadingEnabled: lo.ToPtr(true)}
		pool = testNodePool("pool-a", nodeClass.Name)
		pool.Spec.Template.Spec.Taints = []corev1.Taint{{Key: instancetype.PreemptibleTaintKey,
			Effect: corev1.TaintEffectNoSchedule}}
		claim = testNodeClaim(nodeClass.Name)
		claim.Spec.Taints = pool.Spec.Template.Spec.Taints
		claim.Spec.Requirements = append(claim.Spec.Requirements, v1.NodeSelectorRequirementWithMinValues{
			Key: corev1.LabelInstanceTypeStable, Operator: corev1.NodeSelectorOpIn, Values: []string{shape},
		})
		launchCalls, placementCalls = 0, 0
		unavailable = ocicache.NewUnavailableOfferings(ocicache.UnavailableOfferingsTTL)
		kubeClient := clientfake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(nodeClass, pool).
			WithIndex(&corev1.Pod{},
				"spec.nodeName",
				func(obj client.Object) []string { return []string{obj.(*corev1.Pod).Spec.NodeName} }).Build()
		provider := newPlatformConfigCatalogue(kubeClient, nodeClass, unavailable, nil)
		cp = newUnitTestCloudProvider(unitTestCloudProviderOptions{
			kubeClient: kubeClient, instanceTypeProvider: provider,
			placementProvider: &FakePlacementProvider{PlaceInstanceFn: func(_ context.Context, _ *v1.NodeClaim,
				_ *v1beta1.OCINodeClass, _ *instancetype.OciInstanceType, launch func(*placement.Proposal) error) error {
				placementCalls++
				return launch(&placement.Proposal{})
			}},
			instanceProvider: &FakeInstanceProvider{LaunchInstanceFn: func(_ context.Context, nc *v1.NodeClaim,
				_ *v1beta1.OCINodeClass, it *instancetype.OciInstanceType, _ *image.ImageResolveResult,
				_ *network.NetworkResolveResult,
				_ *kms.KmsKeyResolveResult,
				_ *placement.Proposal) (*instance.InstanceInfo,
				error) {
				launchCalls++
				return &instance.InstanceInfo{Instance: testInstanceWithShape(it.Shape, nc.Labels[v1.NodePoolLabelKey])}, nil
			}},
		})
	})

	It("selects default-encrypted on-demand capacity with a reservation configured", func() {
		claim.Spec.Requirements[0].Values = []string{v1.CapacityTypeOnDemand, v1.CapacityTypeSpot, v1.CapacityTypeReserved}
		patchPlatformNodeClass(cp.kubeClient, nodeClass.Name, func(nc *v1beta1.OCINodeClass) {
			nc.Spec.CapacityReservationConfigs = []*v1beta1.CapacityReservationConfig{
				{CapacityReservationId: lo.ToPtr("test-reservation")},
			}
			nc.StatusConditions().SetTrue(v1beta1.ConditionTypeCapacityReservation)
		})
		baseline, err := cp.GetInstanceTypes(ctx, pool)
		Expect(err).NotTo(HaveOccurred())
		Expect(baseline).To(HaveLen(1))
		Expect(baseline[0].Offerings.Available()).To(HaveLen(2))
		capacityTypes := func(offerings cloudprovider.Offerings) []string {
			var types []string
			for _, offering := range offerings {
				types = append(types, offering.Requirements.Get(v1.CapacityTypeLabelKey).Any())
			}
			return types
		}
		Expect(capacityTypes(baseline[0].Offerings)).To(ConsistOf(v1.CapacityTypeOnDemand, v1.CapacityTypeReserved))
		current := &v1beta1.OCINodeClass{}
		Expect(cp.kubeClient.Get(ctx, client.ObjectKeyFromObject(nodeClass), current)).To(Succeed())
		cp.instanceTypeProvider = newPlatformConfigCatalogue(cp.kubeClient, current, unavailable, func(shape *ocicore.Shape) {
			shape.PlatformConfigOptions.MemoryEncryptionOptions.IsDefaultEnabled = lo.ToPtr(true)
		})
		cp.placementProvider = &FakePlacementProvider{PlaceInstanceFn: func(_ context.Context, _ *v1.NodeClaim,
			_ *v1beta1.OCINodeClass, it *instancetype.OciInstanceType, launch func(*placement.Proposal) error) error {
			placementCalls++
			Expect(capacityTypes(it.Offerings)).To(ConsistOf(v1.CapacityTypeOnDemand))
			return launch(&placement.Proposal{})
		}}
		cp.instanceProvider = &FakeInstanceProvider{LaunchInstanceFn: func(_ context.Context, nc *v1.NodeClaim,
			_ *v1beta1.OCINodeClass, it *instancetype.OciInstanceType, _ *image.ImageResolveResult,
			_ *network.NetworkResolveResult, _ *kms.KmsKeyResolveResult,
			_ *placement.Proposal) (*instance.InstanceInfo, error) {
			launchCalls++
			Expect(capacityTypes(it.Offerings)).To(ConsistOf(v1.CapacityTypeOnDemand))
			return &instance.InstanceInfo{Instance: testInstanceWithShape(it.Shape, nc.Labels[v1.NodePoolLabelKey])}, nil
		}}
		_, err = cp.Create(ctx, claim)
		Expect(err).NotTo(HaveOccurred())
		Expect(placementCalls).To(Equal(1))
		Expect(launchCalls).To(Equal(1))
	})

	It("skips a cheaper shape without platform metadata before placement", func() {
		const cheaperShape = "VM.Standard.E4.4"
		claim.Spec.Requirements[len(claim.Spec.Requirements)-1].Values = []string{shape, cheaperShape}
		cp.instanceTypeProvider = newPlatformConfigCatalogue(cp.kubeClient, nodeClass, unavailable, nil, cheaperShape)
		baseline, err := cp.GetInstanceTypes(ctx, pool)
		Expect(err).NotTo(HaveOccurred())
		Expect(baseline).To(HaveLen(2))
		prices := map[string]float64{}
		for _, it := range baseline {
			Expect(it.Offerings.Available()).NotTo(BeEmpty())
			prices[it.Name] = it.Offerings.Available()[0].Price
		}
		Expect(prices[cheaperShape]).To(BeNumerically("<", prices[shape]))
		cp.instanceTypeProvider = newPlatformConfigCatalogue(cp.kubeClient, nodeClass, unavailable, func(it *ocicore.Shape) {
			if *it.Shape == cheaperShape {
				it.PlatformConfigOptions.Type = ""
			}
		}, cheaperShape)
		candidates, err := cp.GetInstanceTypes(ctx, pool)
		Expect(err).NotTo(HaveOccurred())
		Expect(candidates).To(HaveLen(2)) // retain lifecycle catalogue entries
		for _, it := range candidates {
			if it.Name == cheaperShape {
				Expect(it.Offerings.Available()).To(BeEmpty())
			}
		}
		cp.placementProvider = &FakePlacementProvider{PlaceInstanceFn: func(_ context.Context, _ *v1.NodeClaim,
			_ *v1beta1.OCINodeClass, it *instancetype.OciInstanceType, launch func(*placement.Proposal) error) error {
			placementCalls++
			Expect(it.Shape).To(Equal(shape))
			Expect(it.PlatformType).To(Equal(platformType))
			return launch(&placement.Proposal{})
		}}
		cp.instanceProvider = &FakeInstanceProvider{LaunchInstanceFn: func(_ context.Context, nc *v1.NodeClaim,
			_ *v1beta1.OCINodeClass, it *instancetype.OciInstanceType, _ *image.ImageResolveResult,
			_ *network.NetworkResolveResult, _ *kms.KmsKeyResolveResult, _ *placement.Proposal) (*instance.InstanceInfo, error) {
			launchCalls++
			Expect(it.Shape).To(Equal(shape))
			Expect(it.PlatformType).To(Equal(platformType))
			return &instance.InstanceInfo{Instance: testInstanceWithShape(it.Shape, nc.Labels[v1.NodePoolLabelKey])}, nil
		}}
		_, err = cp.Create(ctx, claim)
		Expect(err).NotTo(HaveOccurred())
		Expect(placementCalls).To(Equal(1))
		Expect(launchCalls).To(Equal(1))
	})

	// Removing the shared filter, resource fit, or offering recheck would allow a
	// stale scheduling choice to reach placement and Compute in these cases.
	DescribeTable("rechecks scheduling candidates before placement", func(change string) {
		if change == "confidential reserved" {
			patchPlatformNodeClass(cp.kubeClient, nodeClass.Name, func(nc *v1beta1.OCINodeClass) {
				nc.Spec.CapacityReservationConfigs = []*v1beta1.CapacityReservationConfig{
					{CapacityReservationId: lo.ToPtr("test-reservation")},
				}
				nc.StatusConditions().SetTrue(v1beta1.ConditionTypeCapacityReservation)
			})
			claim.Spec.Requirements[0].Values = []string{v1.CapacityTypeReserved}
		}
		if change == "reduced CPU" {
			claim.Spec.Resources.Requests = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("12")}
		}
		scheduled, err := cp.GetInstanceTypes(ctx, pool)
		Expect(err).NotTo(HaveOccurred())
		Expect(scheduled).To(HaveLen(1))
		reqs := scheduling.NewNodeSelectorRequirementsWithMinValues(claim.Spec.Requirements...)
		Expect(scheduled[0].Offerings.Compatible(reqs).Available()).NotTo(BeEmpty())
		allocatable := scheduled[0].Allocatable()
		Expect(allocatable.Cpu().MilliValue()).To(BeNumerically(">", 12000))
		patchPlatformNodeClass(cp.kubeClient, nodeClass.Name, func(nc *v1beta1.OCINodeClass) {
			switch change {
			case "lost compatibility":
				nc.Spec.PlatformConfig.AccessControlServiceEnabled = lo.ToPtr(true)
			case "reduced CPU":
				nc.Spec.PlatformConfig.SymmetricMultiThreadingEnabled = lo.ToPtr(false)
			case "confidential reserved":
				nc.Spec.PlatformConfig.MemoryEncryptionEnabled = lo.ToPtr(true)
			}
		})
		_, err = cp.Create(ctx, claim)
		Expect(err).To(HaveOccurred())
		Expect(cloudprovider.IsInsufficientCapacityError(err)).To(BeTrue())
		Expect(launchCalls).To(Equal(0))
		Expect(placementCalls).To(Equal(0))
		// Previously returned candidates remain snapshots of scheduling-time state.
		Expect(scheduled[0].Offerings.Compatible(reqs).Available()).NotTo(BeEmpty())
	},
		Entry("lost compatibility", "lost compatibility"),
		Entry("reduced allocatable CPU", "reduced CPU"),
		Entry("confidential reservation requirement", "confidential reserved"),
	)

	It("leaves unknown defaults to Compute and preserves ordinary launch errors", func() {
		patchPlatformNodeClass(cp.kubeClient, nodeClass.Name, func(nc *v1beta1.OCINodeClass) {
			nc.Spec.PlatformConfig = &v1beta1.PlatformConfig{MeasuredBootEnabled: lo.ToPtr(true)}
		})
		cp.instanceProvider = &FakeInstanceProvider{LaunchInstanceFn: func(_ context.Context, _ *v1.NodeClaim,
			nc *v1beta1.OCINodeClass, it *instancetype.OciInstanceType, _ *image.ImageResolveResult,
			_ *network.NetworkResolveResult, _ *kms.KmsKeyResolveResult, _ *placement.Proposal) (*instance.InstanceInfo, error) {
			launchCalls++
			Expect(nc.Spec.PlatformConfig).To(Equal(&v1beta1.PlatformConfig{MeasuredBootEnabled: lo.ToPtr(true)}))
			Expect(it.Capacity.Cpu().Value()).To(Equal(int64(16)))
			return nil, fakeServiceError{statusCode: 400, code: "InvalidParameter", message: "platform combination rejected"}
		}}
		_, err := cp.Create(ctx, claim)
		var createErr *cloudprovider.CreateError
		Expect(errors.As(err, &createErr)).To(BeTrue())
		Expect(createErr.ConditionReason).To(Equal("LaunchInstanceFailed"))
		Expect(createErr.ConditionMessage).To(Equal("Error Launching Instance"))
		Expect(placementCalls).To(Equal(1))
		Expect(launchCalls).To(Equal(1))
	})

	It("keeps the fetched request through price groups and observes API edits on a later attempt", func() {
		// A placement-time API edit must not change the request already fetched for
		// this attempt. The next Create reads the updated object through the client.
		var rebuilt *v1beta1.OCINodeClass
		var requestUsedDuringRebuild *v1beta1.PlatformConfig
		var typeSelectedDuringRebuild ocicore.ShapePlatformConfigOptionsTypeEnum
		realProvider := newPlatformConfigCatalogue(cp.kubeClient, nodeClass, unavailable, func(shape *ocicore.Shape) {
			shape.PlatformConfigOptions.MemoryEncryptionOptions.IsDefaultEnabled = lo.ToPtr(false)
		})
		laterProvider := newPlatformConfigCatalogue(cp.kubeClient, nodeClass, unavailable, func(shape *ocicore.Shape) {
			shape.PlatformConfigOptions.MemoryEncryptionOptions.IsDefaultEnabled = lo.ToPtr(true)
		})
		cp.instanceTypeProvider = &FakeInstanceTypeProvider{ListFn: func(c context.Context,
			nc *v1beta1.OCINodeClass,
			taints []corev1.Taint) ([]*instancetype.OciInstanceType,
			error) {
			rebuilt, requestUsedDuringRebuild = nc, nc.Spec.PlatformConfig.DeepCopy()
			candidates, err := realProvider.ListInstanceTypes(c, nc, taints)
			if err == nil {
				typeSelectedDuringRebuild = candidates[0].PlatformType
			}
			return candidates, err
		}}
		claim.Spec.Requirements[0].Values = []string{v1.CapacityTypeSpot, v1.CapacityTypeOnDemand}
		attempt := 0
		cp.placementProvider = &FakePlacementProvider{PlaceInstanceFn: func(_ context.Context, _ *v1.NodeClaim,
			nc *v1beta1.OCINodeClass, it *instancetype.OciInstanceType, launch func(*placement.Proposal) error) error {
			placementCalls++
			Expect(nc).To(BeIdenticalTo(rebuilt))
			Expect(it.PlatformType).To(Equal(typeSelectedDuringRebuild))
			Expect(it.Offerings).To(HaveLen(1)) // price groups split spot and on-demand
			Expect(it.Offerings[0].Available).To(BeTrue())
			if attempt != 0 {
				Expect(it.Offerings[0].CapacityType()).To(Equal(v1.CapacityTypeOnDemand))
			}
			patchPlatformNodeClass(cp.kubeClient, nodeClass.Name, func(current *v1beta1.OCINodeClass) {
				current.Spec.PlatformConfig.SymmetricMultiThreadingEnabled = lo.ToPtr(attempt != 0)
			})
			unavailable.MarkUnavailable(ctx, shape, nil, nil, "PHX-AD-1", v1.CapacityTypeSpot, "")
			return launch(&placement.Proposal{})
		}}
		cp.instanceProvider = &FakeInstanceProvider{LaunchInstanceFn: func(_ context.Context, nc *v1.NodeClaim,
			launchedNodeClass *v1beta1.OCINodeClass,
			launchedCandidate *instancetype.OciInstanceType,
			_ *image.ImageResolveResult,
			_ *network.NetworkResolveResult, _ *kms.KmsKeyResolveResult, _ *placement.Proposal) (*instance.InstanceInfo, error) {
			launchCalls++
			Expect(launchedNodeClass).To(BeIdenticalTo(rebuilt))
			Expect(launchedNodeClass.Spec.PlatformConfig).To(Equal(requestUsedDuringRebuild))
			Expect(launchedCandidate.PlatformType).To(Equal(typeSelectedDuringRebuild))
			if attempt != 0 {
				Expect(launchedCandidate.Offerings[0].CapacityType()).To(Equal(v1.CapacityTypeOnDemand))
			}
			Expect(launchedCandidate.Capacity.Cpu().Value()).To(Equal(lo.Ternary(attempt == 0, int64(16), int64(8))))
			if attempt == 0 {
				return nil, fakeServiceError{statusCode: 500, code: "InternalError", message: "Out of host capacity."}
			}
			return &instance.InstanceInfo{Instance: testInstanceWithShape(launchedCandidate.Shape,
					nc.Labels[v1.NodePoolLabelKey])},
				nil
		}}
		for attempt = 0; attempt < 2; attempt++ {
			if attempt == 1 {
				current := &v1beta1.OCINodeClass{}
				Expect(cp.kubeClient.Get(ctx, client.ObjectKeyFromObject(nodeClass), current)).To(Succeed())
				hash := utils.HashNodeClassSpec(current)
				realProvider = laterProvider
				candidates, err := laterProvider.ListInstanceTypes(ctx, current, claim.Spec.Taints)
				Expect(err).NotTo(HaveOccurred())
				for _, candidate := range candidates {
					Expect(candidate.Offerings.Available()).NotTo(BeEmpty())
					for _, offering := range candidate.Offerings.Available() {
						Expect(offering.CapacityType()).To(Equal(v1.CapacityTypeOnDemand))
					}
				}
				Expect(utils.HashNodeClassSpec(current)).To(Equal(hash))
			}
			created, err := cp.Create(ctx, claim)
			Expect(*requestUsedDuringRebuild.SymmetricMultiThreadingEnabled).To(Equal(attempt == 0))
			current := &v1beta1.OCINodeClass{}
			Expect(cp.kubeClient.Get(ctx, client.ObjectKeyFromObject(nodeClass), current)).To(Succeed())
			Expect(*current.Spec.PlatformConfig.SymmetricMultiThreadingEnabled).To(Equal(attempt != 0))
			if attempt == 0 {
				Expect(cloudprovider.IsInsufficientCapacityError(err)).To(BeTrue())
			} else {
				Expect(err).NotTo(HaveOccurred())
				Expect(created.Annotations[v1beta1.NodeClassHash]).To(Equal(utils.HashNodeClassSpec(rebuilt)))
				Expect(created.Annotations[v1beta1.NodeClassHash]).NotTo(Equal(utils.HashNodeClassSpec(current)))
			}
		}
		Expect(launchCalls).To(Equal(3))
		Expect(placementCalls).To(Equal(3))
	})

	It("retains reserved-node lookup, drift and disruption price when all new offerings are incompatible", func() {
		claim.Spec.Requirements[0].Values = []string{v1.CapacityTypeReserved}
		claim.Spec.Requirements = append(claim.Spec.Requirements, v1.NodeSelectorRequirementWithMinValues{
			Key: cloudprovider.ReservationIDLabel, Operator: corev1.NodeSelectorOpIn, Values: []string{"test-reservation"},
		})
		cp.capacityReservationProvider = &FakeCapacityReservationProvider{ResolveFn: func(context.Context,
			[]*v1beta1.CapacityReservationConfig) ([]capacityreservation.ResolveResult, error) {
			return []capacityreservation.ResolveResult{{Ocid: "test-reservation"}}, nil
		}}
		patchPlatformNodeClass(cp.kubeClient, nodeClass.Name, func(nc *v1beta1.OCINodeClass) {
			nc.Spec.CapacityReservationConfigs = []*v1beta1.CapacityReservationConfig{
				{CapacityReservationId: lo.ToPtr("test-reservation")},
			}
			nc.StatusConditions().SetTrue(v1beta1.ConditionTypeCapacityReservation)
		})
		Expect(cp.kubeClient.Get(ctx, client.ObjectKeyFromObject(nodeClass), nodeClass)).To(Succeed())
		scheduled, err := cp.GetInstanceTypes(ctx, pool)
		Expect(err).NotTo(HaveOccurred())
		price, found := scheduled[0].OfferingPrice("PHX-AD-1", v1.CapacityTypeReserved)
		Expect(found).To(BeTrue())
		Expect(price).To(Equal(float64(2)))
		Expect(scheduled[0].Requirements.Get(cloudprovider.ReservationIDLabel).Values()).To(ConsistOf("test-reservation"))
		i := testInstanceWithShape(shape, pool.Name)
		i.CompartmentId = lo.ToPtr("existing-compartment")
		i.CapacityReservationId = lo.ToPtr("test-reservation")
		cp.instanceProvider = &FakeInstanceProvider{
			GetInstanceFn: func(context.Context, string) (*instance.InstanceInfo, error) {
				return &instance.InstanceInfo{Instance: i}, nil
			},
			ListInstancesFn: func(context.Context, []string) ([]*ocicore.Instance, error) { return []*ocicore.Instance{i}, nil },
		}
		claim.Labels[corev1.LabelInstanceTypeStable] = shape
		claim.Status.ProviderID = *i.Id
		claim.Annotations = platformHashAnnotations(nodeClass)
		patchPlatformNodeClass(cp.kubeClient, nodeClass.Name, func(nc *v1beta1.OCINodeClass) {
			nc.Spec.PlatformConfig.AccessControlServiceEnabled = lo.ToPtr(true)
			nc.Annotations = platformHashAnnotations(nc)
		})
		got, err := cp.Get(ctx, *i.Id)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Labels[corev1.LabelInstanceTypeStable]).To(Equal(shape))
		Expect(got.Labels[v1.CapacityTypeLabelKey]).To(Equal(v1.CapacityTypeReserved))
		listed, err := cp.List(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(HaveLen(1))
		Expect(listed[0].Labels[corev1.LabelInstanceTypeStable]).To(Equal(shape))
		Expect(listed[0].Labels[v1.CapacityTypeLabelKey]).To(Equal(v1.CapacityTypeReserved))
		reason, err := cp.IsDrifted(ctx, claim)
		Expect(err).NotTo(HaveOccurred())
		Expect(reason).To(Equal(cloudprovider.DriftReason("NodeClassStaticFieldDrift")))
		// With matching hashes, the unrelated instance-compartment check still runs.
		current := &v1beta1.OCINodeClass{}
		Expect(cp.kubeClient.Get(ctx, client.ObjectKeyFromObject(nodeClass), current)).To(Succeed())
		claim.Annotations = platformHashAnnotations(current)
		reason, err = cp.IsDrifted(ctx, claim)
		Expect(err).NotTo(HaveOccurred())
		Expect(reason).To(Equal(cloudprovider.DriftReason("instanceCompartmentMismatch")))

		poolMap, typeMap, err := disruption.BuildNodePoolMap(ctx, cp.kubeClient, cp)
		Expect(err).NotTo(HaveOccurred())
		Expect(typeMap[pool.Name]).To(HaveKey(shape))
		Expect(typeMap[pool.Name][shape].Offerings.Available()).To(BeEmpty())
		claim.Labels[v1.CapacityTypeLabelKey] = v1.CapacityTypeReserved
		claim.Labels[cloudprovider.ReservationIDLabel] = "test-reservation"
		claim.Labels[corev1.LabelTopologyZone] = "PHX-AD-1"
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "existing-node", Labels: lo.Assign(claim.Labels,
			map[string]string{v1.NodeInitializedLabelKey: "true"})}, Spec: corev1.NodeSpec{ProviderID: *i.Id}}
		recorder := events.NewRecorder(&record.FakeRecorder{})
		clk := clock.RealClock{}
		cluster := state.NewCluster(clk, cp.kubeClient, cp)
		queue := disruption.NewQueue(cp.kubeClient, recorder, cluster, clk, nil)
		pdbLimits, err := pdb.NewLimits(ctx, cp.kubeClient)
		Expect(err).NotTo(HaveOccurred())
		candidate, err := disruption.NewCandidate(ctx, cp.kubeClient, recorder, clk,
			&state.StateNode{Node: node,
				NodeClaim: claim},
			pdbLimits,
			poolMap,
			typeMap,
			queue,
			disruption.GracefulDisruptionClass)
		Expect(err).NotTo(HaveOccurred())
		Expect(candidate.Price).To(Equal(float64(2)))
		_, err = cp.Create(ctx, claim)
		Expect(cloudprovider.IsInsufficientCapacityError(err)).To(BeTrue())
		Expect(launchCalls).To(Equal(0))
		Expect(placementCalls).To(Equal(0))
	})

})

func patchPlatformNodeClass(kubeClient client.Client, name string, mutate func(*v1beta1.OCINodeClass)) {
	GinkgoHelper()
	current := &v1beta1.OCINodeClass{}
	Expect(kubeClient.Get(ctx, client.ObjectKey{Name: name}, current)).To(Succeed())
	original := current.DeepCopy()
	mutate(current)
	Expect(kubeClient.Patch(ctx, current, client.MergeFrom(original))).To(Succeed())
}

func platformHashAnnotations(nc *v1beta1.OCINodeClass) map[string]string {
	return map[string]string{v1beta1.NodeClassHash: utils.HashNodeClassSpec(nc),
		v1beta1.NodeClassHashVersion: v1beta1.OCINodeClassHashVersion}
}

func newPlatformConfigCatalogue(kubeClient client.Client,
	nc *v1beta1.OCINodeClass,
	unavailable *ocicache.UnavailableOfferings,
	configureShape func(*ocicore.Shape), extraShapeNames ...string) instancetype.Provider {
	GinkgoHelper()
	catalogueCtx, stop := context.WithCancel(ctx)
	DeferCleanup(stop)
	identityClient := fakes.NewFakeIdentityClient()
	identityClient.OnListAvailabilityDomains = func(context.Context,
		ociidentity.ListAvailabilityDomainsRequest) (ociidentity.ListAvailabilityDomainsResponse,
		error) {
		return ociidentity.ListAvailabilityDomainsResponse{
			Items: []ociidentity.AvailabilityDomain{
				{Name: lo.ToPtr("aumf:PHX-AD-1"), Id: lo.ToPtr("test-ad")},
			},
		}, nil
	}
	identityProvider, err := identity.NewProvider(catalogueCtx, nodeClassClusterCompartmentID, identityClient)
	Expect(err).NotTo(HaveOccurred())
	shapeNames := append([]string{"VM.Standard.E4.8"}, extraShapeNames...)
	shapes := make([]ocicore.Shape, 0, len(shapeNames))
	for _, name := range shapeNames {
		shape := ocicore.Shape{Shape: lo.ToPtr(name),
			Ocpus:       lo.ToPtr(float32(8)),
			MemoryInGBs: lo.ToPtr(float32(32)),
			IsFlexible:  lo.ToPtr(false), PlatformConfigOptions: &ocicore.ShapePlatformConfigOptions{
				Type: "HYPOTHETICAL_FUTURE_VM",
				SymmetricMultiThreadingOptions: &ocicore.ShapeSymmetricMultiThreadingEnabledPlatformOptions{
					AllowedValues: []bool{false, true},
				},
				MemoryEncryptionOptions:      &ocicore.ShapeMemoryEncryptionOptions{AllowedValues: []bool{false, true}},
				TrustedPlatformModuleOptions: &ocicore.ShapeTrustedPlatformModuleOptions{AllowedValues: []bool{false, true}},
				MeasuredBootOptions:          &ocicore.ShapeMeasuredBootOptions{AllowedValues: []bool{false, true}},
			}}
		if configureShape != nil {
			configureShape(&shape)
		}
		shapes = append(shapes, shape)
	}
	compute := &fakes.FakeCompute{OnListShapes: func(context.Context,
		ocicore.ListShapesRequest) (ocicore.ListShapesResponse,
		error) {
		return ocicore.ListShapesResponse{Items: shapes}, nil
	}}
	reserves := &FakeCapacityReservationProvider{ResolveFn: func(context.Context,
		[]*v1beta1.CapacityReservationConfig) ([]capacityreservation.ResolveResult,
		error) {
		return []capacityreservation.ResolveResult{{Ocid: "test-reservation",
			Ad: "aumf:PHX-AD-1",
			ShapeAvailabilities: []capacityreservation.ShapeAvailability{
				{Ad: "aumf:PHX-AD-1", Shape: "VM.Standard.E4.8", Total: 1},
			}}}, nil
	}}
	meta := filepath.Join(GinkgoT().TempDir(), "shape-meta.json")
	Expect(os.WriteFile(meta, []byte(`{
  "prices":[{"shapeName":"VM.Standard.E4.8","ocpuUnitPrice":0.125,"memoryUnitPrice":0.03125},
{"shapeName":"VM.Standard.E4.4","ocpuUnitPrice":0.0625,"memoryUnitPrice":0.015625}],
  "preemptibleShapes":["VM.Standard.E4"]
}`), 0600)).To(Succeed())
	start := make(chan struct{})
	kubernetesClient := kubernetesfake.NewClientset()
	kubernetesClient.Discovery().(*discoveryfake.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: "v1.31.0"}
	provider,
		err := instancetype.New(catalogueCtx,
		"us-phoenix-1",
		nodeClassClusterCompartmentID,
		compute,
		identityProvider,
		kubernetesClient, kubeClient, reserves, nil, nil, meta, time.Hour, nil, []network.IpFamily{network.IPv4},
		unavailable, nil, instancetype.VMMemoryOverheadConfig{}, start)
	Expect(err).NotTo(HaveOccurred())
	close(start)
	Eventually(func() bool {
		candidates, err := provider.ListInstanceTypes(catalogueCtx, nc, nil)
		if err != nil || len(candidates) != len(shapeNames) {
			return false
		}
		// Shapes and pricing load independently; candidate count alone is not readiness.
		for _, candidate := range candidates {
			if len(candidate.Offerings) == 0 || candidate.Offerings[0].Price <= 0 {
				return false
			}
		}
		return true
	}).Should(BeTrue())
	return provider
}
