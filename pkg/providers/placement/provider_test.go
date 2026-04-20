/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package placement

import (
	"context"
	"testing"

	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/capacityreservation"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/identity"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancetype"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

func TestProvider_InstanceFound(t *testing.T) {
	provider := &DefaultProvider{
		instancesByNodePool: make(map[string]*adFdSummary),
	}

	instance := &ocicore.Instance{
		Id:                 lo.ToPtr("instance1"),
		AvailabilityDomain: lo.ToPtr("AD-1"),
		FaultDomain:        lo.ToPtr("FD-1"),
		Shape:              lo.ToPtr("VM.Standard2.1"),
	}

	provider.InstanceFound("nodepool1", instance)

	summary := provider.instancesByNodePool["nodepool1"]
	assert.NotNil(t, summary)

	actual, exists := summary.instanceMap["instance1"]
	assert.True(t, exists)
	assert.Equal(t, "AD-1", actual.ad)
	assert.Equal(t, "FD-1", actual.fd)
	assert.Equal(t, "VM.Standard2.1", actual.shape)
}

func TestProvider_InstanceForget(t *testing.T) {
	provider := &DefaultProvider{
		instancesByNodePool: make(map[string]*adFdSummary),
	}

	// Add instance first
	summary := newAdFdSummary()
	summary.updateBy("instance1", "AD-1", "FD-1", "VM.Standard2.1", nil, nil, nil)
	provider.instancesByNodePool["nodepool1"] = summary

	provider.InstanceForget("nodepool1", "instance1")

	assert.Len(t, summary.instanceMap, 0)
}

func TestProvider_findAdSummaryForNodePool(t *testing.T) {
	provider := &DefaultProvider{
		instancesByNodePool: make(map[string]*adFdSummary),
	}

	// Test creating new summary for nodepool1
	result := provider.findAdSummaryForNodePool("nodepool1")
	assert.NotNil(t, result)
	assert.Contains(t, provider.instancesByNodePool, "nodepool1")
	assert.Equal(t, result, provider.instancesByNodePool["nodepool1"])

	// Test creating new summary for nodepool2
	result2 := provider.findAdSummaryForNodePool("nodepool2")
	assert.NotNil(t, result2)
	assert.Contains(t, provider.instancesByNodePool, "nodepool2")
	assert.Equal(t, result2, provider.instancesByNodePool["nodepool2"])
	// Note: Not checking NotEqual due to testify issue with mutex-containing structs
}

func TestNewProvider(t *testing.T) {
	ctx := context.Background()

	provider, err := NewProvider(ctx, nil, nil, nil, nil)

	assert.NoError(t, err)
	assert.NotNil(t, provider)
	assert.NotNil(t, provider.instancesByNodePool)
	assert.Nil(t, provider.capacityReservationProvider)
	assert.Nil(t, provider.computeClusterProvider)
	assert.Nil(t, provider.clusterPlacementGroupProvider)
	assert.Nil(t, provider.identityProvider)
}

func TestProvider_PlaceInstance_PassesNodeClaimFaultDomainRequirement(t *testing.T) {
	ctx := context.Background()
	fakeCompute := &fakes.FakeCompute{}
	fakeIdentity := fakes.NewFakeIdentity()
	capResProvider := capacityreservation.NewProvider(ctx, fakeCompute, "ocid1.compartment.oc1..cluster123")
	identityProvider, err := identity.NewProvider(ctx, "ocid1.compartment.oc1..cluster123", fakeIdentity)
	assert.NoError(t, err)

	provider := &DefaultProvider{
		instancesByNodePool:         make(map[string]*adFdSummary),
		capacityReservationProvider: capResProvider,
		identityProvider:            identityProvider,
	}

	offering := &cloudprovider.Offering{
		Requirements: scheduling.NewRequirements(
			scheduling.NewRequirement(corev1.CapacityTypeLabelKey, v1.NodeSelectorOpIn, corev1.CapacityTypeOnDemand),
			scheduling.NewRequirement(v1.LabelTopologyZone, v1.NodeSelectorOpIn, "ad-1"),
			scheduling.NewRequirement(cloudprovider.ReservationIDLabel, v1.NodeSelectorOpDoesNotExist),
		),
		Price:     0.1,
		Available: true,
	}
	instanceType := &instancetype.OciInstanceType{
		Shape: "VM.Standard2.1",
		InstanceType: cloudprovider.InstanceType{
			Offerings: []*cloudprovider.Offering{offering},
		},
	}
	claim := &corev1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-claim",
			Labels: map[string]string{
				corev1.NodePoolLabelKey: "nodepool1",
			},
		},
		Spec: corev1.NodeClaimSpec{
			Requirements: []corev1.NodeSelectorRequirementWithMinValues{
				{
					Key:      ociv1beta1.OciFaultDomain,
					Operator: v1.NodeSelectorOpIn,
					Values:   []string{"FAULT-DOMAIN-2"},
				},
			},
		},
	}

	placeFuncCalled := false
	err = provider.PlaceInstance(ctx, claim, &ociv1beta1.OCINodeClass{}, instanceType, func(proposal *Proposal) error {
		placeFuncCalled = true
		if assert.NotNil(t, proposal.Fd) {
			assert.Equal(t, "FAULT-DOMAIN-2", *proposal.Fd)
		}
		assert.Equal(t, "PHX:ad-1", proposal.Ad)
		return nil
	})

	assert.NoError(t, err)
	assert.True(t, placeFuncCalled)
}
