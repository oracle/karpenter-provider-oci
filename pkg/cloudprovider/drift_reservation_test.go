/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package cloudprovider

import (
	"context"
	"testing"

	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/capacityreservation"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instance"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancetype"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// Exercise the complete provider drift path: reservation requirement values are
// label suffixes, while OCI reports full reservation OCIDs.
func TestCapacityReservationDrift(t *testing.T) {
	require.NoError(t, v1beta1.AddToScheme(scheme.Scheme))
	const (
		a = "ocid1.capacityreservation.oc1.phx.reservation-a"
		b = "ocid1.capacityreservation.oc1.phx.reservation-b"
		c = "ocid1.capacityreservation.oc1.phx.reservation-c"
	)
	tests := []struct {
		name       string
		allowed    []string
		configured []string
		actual     *string
		drifted    bool
	}{
		{"first allowed reservation", []string{a, b, c}, []string{a, b, c}, lo.ToPtr(a), false},
		{"second allowed reservation", []string{a, b, c}, []string{a, b, c}, lo.ToPtr(b), false},
		{"third allowed reservation", []string{a, b, c}, []string{a, b, c}, lo.ToPtr(c), false},
		{"configured reservation outside requirement", []string{a, b}, []string{a, b, c}, lo.ToPtr(c), true},
		{"actual reservation removed from configuration", []string{a, b, c}, []string{a, b}, lo.ToPtr(c), true},
		{"other allowed reservation removed from configuration", []string{a, b, c}, []string{b, c}, lo.ToPtr(c), false},
		{"all reservation configuration removed", []string{a, b, c}, nil, lo.ToPtr(c), true},
		{"no configured reservation satisfies requirement", []string{a}, []string{b}, lo.ToPtr(b), true},
		{"missing actual reservation", []string{a, b, c}, []string{a, b, c}, nil, true},
		{"single matching reservation", []string{b}, []string{b}, lo.ToPtr(b), false},
		{"unreserved instance", nil, nil, nil, false},
		{"unexpected reserved instance", nil, nil, lo.ToPtr(b), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nodeClass := testReadyNodeClass(uniqueName("reservation-drift"))
			for _, id := range tc.configured {
				nodeClass.Spec.CapacityReservationConfigs = append(nodeClass.Spec.CapacityReservationConfigs,
					&v1beta1.CapacityReservationConfig{CapacityReservationId: lo.ToPtr(id)})
			}
			if len(tc.configured) > 0 {
				nodeClass.StatusConditions().SetTrue(v1beta1.ConditionTypeCapacityReservation)
			}
			nodeClass.Spec.VolumeConfig.BootVolumeConfig = &v1beta1.BootVolumeConfig{}
			nodeClass.Status.Network = &v1beta1.Network{
				PrimaryVnic: &v1beta1.Vnic{Subnet: v1beta1.Subnet{SubnetId: "subnet-1"}},
			}
			nodeClaim := testNodeClaim(nodeClass.Name)
			nodeClaim.Labels[corev1.LabelInstanceTypeStable] = testShapeA
			if len(tc.allowed) > 0 {
				values := lo.Map(tc.allowed, func(id string, _ int) string {
					return capacityreservation.OcidToLabelValue(id)
				})
				nodeClaim.Spec.Requirements = append(nodeClaim.Spec.Requirements, v1.NodeSelectorRequirementWithMinValues{
					Key: v1beta1.ReservationIDLabel, Operator: corev1.NodeSelectorOpIn, Values: values,
				})
			}
			actual := testInstanceWithShape(testShapeA, "pool-a")
			actual.CompartmentId = lo.ToPtr(testCompartmentA)
			actual.CapacityReservationId = tc.actual
			cp := newUnitTestCloudProvider(unitTestCloudProviderOptions{
				kubeClient:    newFakeKubeClient(nodeClass),
				instanceTypes: []*instancetype.OciInstanceType{testInstanceType(testShapeA, 10)},
				capacityReservationProvider: &FakeCapacityReservationProvider{
					ResolveFn: func(_ context.Context, configs []*v1beta1.CapacityReservationConfig) (
						[]capacityreservation.ResolveResult, error) {
						return lo.Map(configs, func(cfg *v1beta1.CapacityReservationConfig, _ int) capacityreservation.ResolveResult {
							return capacityreservation.ResolveResult{Ocid: *cfg.CapacityReservationId}
						}), nil
					},
				},
				instanceProvider: &FakeInstanceProvider{
					GetInstanceCompartmentFn: func(*v1beta1.OCINodeClass) string { return testCompartmentA },
					GetInstanceFn: func(context.Context, string) (*instance.InstanceInfo, error) {
						return &instance.InstanceInfo{Instance: actual}, nil
					},
					ListInstanceVnicAttachmentsFn: func(context.Context, string, string) ([]*ocicore.VnicAttachment, error) {
						return []*ocicore.VnicAttachment{{VnicId: lo.ToPtr("vnic-1"), SubnetId: lo.ToPtr("subnet-1"),
							LifecycleState: ocicore.VnicAttachmentLifecycleStateAttached}}, nil
					},
					ListInstanceBootVolumeAttachmentsFn: func(context.Context, string, string, string) (
						[]*ocicore.BootVolumeAttachment, error) {
						return []*ocicore.BootVolumeAttachment{{BootVolumeId: lo.ToPtr("boot-1"), TimeCreated: actual.TimeCreated}}, nil
					},
				},
				networkProvider: &FakeNetworkProvider{
					GetVnicFn: func(context.Context, string) (*ocicore.Vnic, error) {
						return &ocicore.Vnic{IsPrimary: lo.ToPtr(true)}, nil
					},
				},
			})
			reason, err := cp.IsDrifted(context.Background(), nodeClaim)
			require.NoError(t, err)
			if tc.drifted {
				require.Equal(t, cloudprovider.DriftReason(CapacityReservationMismatch), reason)
			} else {
				require.Empty(t, reason)
			}
		})
	}
}
