/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package capacityreservation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/cache"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	"github.com/oracle/karpenter-provider-oci/pkg/oci"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var compartmentID = "test-compartment"

func fakeProviderWithCompute(fc oci.ComputeClient) *DefaultProvider {
	return &DefaultProvider{
		computeClient:        fc,
		clusterCompartmentId: compartmentID,
		capResCache:          cache.NewDefaultGetOrLoadCache[*CapResWithLoadTime](),
		capResSelectorCache:  cache.NewDefaultGetOrLoadCache[[]*CapResWithLoadTime](),
		usageMap:             make(map[string]*Usage),
	}
}

func TestProvider_ResolveCapacityReservations(t *testing.T) {
	ctx := context.Background()
	t.Run("resolve by id, happy", func(t *testing.T) {
		id := "ocid1.res.oc1..x"
		fc := &fakes.FakeCompute{}
		// Provide canned get response for GetComputeCapacityReservation
		fc.OnGetComputeCapacityReservation = func(ctx context.Context,
			req ocicore.GetComputeCapacityReservationRequest) (ocicore.GetComputeCapacityReservationResponse, error) {
			return ocicore.GetComputeCapacityReservationResponse{
				ComputeCapacityReservation: ocicore.ComputeCapacityReservation{
					Id:                 &id,
					DisplayName:        lo.ToPtr("cname"),
					AvailabilityDomain: lo.ToPtr("ad1"),
					CompartmentId:      lo.ToPtr("compid"),
				}}, nil
		}
		provider := fakeProviderWithCompute(fc)
		cfgs := []*v1beta1.CapacityReservationConfig{{CapacityReservationId: &id}}
		res, err := provider.ResolveCapacityReservations(ctx, cfgs)
		require.NoError(t, err)
		assert.Len(t, res, 1)
		assert.Equal(t, id, res[0].Ocid)
	})

	t.Run("error if both id & selector are set", func(t *testing.T) {
		id := "foo"
		selector := &v1beta1.OciResourceSelectorTerm{}
		provider := fakeProviderWithCompute(nil)
		cfgs := []*v1beta1.CapacityReservationConfig{{CapacityReservationId: &id, CapacityReservationFilter: selector}}
		_, err := provider.ResolveCapacityReservations(ctx, cfgs)
		assert.Equal(t, InvalidCapacityReservationConfigError, err)
	})

	t.Run("error from getCapacityReservation", func(t *testing.T) {
		id := "bar"
		fc := &fakes.FakeCompute{}
		fc.OnGetComputeCapacityReservation = func(ctx context.Context,
			req ocicore.GetComputeCapacityReservationRequest) (ocicore.GetComputeCapacityReservationResponse, error) {
			return ocicore.GetComputeCapacityReservationResponse{}, errors.New("fail get")
		}
		provider := fakeProviderWithCompute(fc)
		cfgs := []*v1beta1.CapacityReservationConfig{{CapacityReservationId: &id}}
		_, err := provider.ResolveCapacityReservations(ctx, cfgs)
		assert.ErrorContains(t, err, "fail get")
	})

	t.Run("usage adjustment and reconciliation branch", func(t *testing.T) {
		id := "ocid1.res.oc1..y"
		name := "c1"
		fc := &fakes.FakeCompute{}
		fc.OnGetComputeCapacityReservation = func(ctx context.Context,
			req ocicore.GetComputeCapacityReservationRequest) (ocicore.GetComputeCapacityReservationResponse, error) {
			return ocicore.GetComputeCapacityReservationResponse{
				ComputeCapacityReservation: ocicore.ComputeCapacityReservation{
					Id: &id, DisplayName: &name, AvailabilityDomain: lo.ToPtr("ad1"),
					CompartmentId: lo.ToPtr("compid"),
				},
			}, nil
		}
		provider := fakeProviderWithCompute(fc)
		cfgs := []*v1beta1.CapacityReservationConfig{{CapacityReservationId: &id}}
		provider.usageMap[id] = &Usage{lastCommit: time.Now().Add(10 * time.Second)}
		_, _ = provider.ResolveCapacityReservations(ctx, cfgs)
	})
}

func TestProvider_MarkCapacityReservationUsedAndReleased(t *testing.T) {
	t.Run("with capacity reservation id", func(t *testing.T) {
		id := "crid"
		instance := &ocicore.Instance{CapacityReservationId: &id, FaultDomain: lo.ToPtr("fd1"),
			Shape: lo.ToPtr("VM.Standard1.1")}
		provider := fakeProviderWithCompute(&fakes.FakeCompute{})
		provider.MarkCapacityReservationUsed(instance)
		provider.MarkCapacityReservationReleased(instance)
		assert.NotNil(t, provider.usageMap[id])
		assert.True(t, provider.usageMap[id].lastCommit.Before(time.Now().Add(1*time.Second)))
	})

	t.Run("nil capacity reservation id", func(t *testing.T) {
		instance := &ocicore.Instance{CapacityReservationId: nil, FaultDomain: lo.ToPtr("fd1"),
			Shape: lo.ToPtr("VM.Standard1.1")}
		provider := fakeProviderWithCompute(&fakes.FakeCompute{})
		provider.MarkCapacityReservationUsed(instance)
		provider.MarkCapacityReservationReleased(instance)
		// Should not create usage map entry
		assert.Len(t, provider.usageMap, 0)
	})
}

func TestProvider_getOrCreateUsage_and_getUsage(t *testing.T) {
	provider := fakeProviderWithCompute(nil)
	id := "crid2"
	usage := provider.getOrCreateUsage(id)
	assert.Equal(t, usage, provider.getUsage(id))
}

func TestProvider_SyncCapacityReservation(t *testing.T) {
	id := "ocid1.res"
	fc := &fakes.FakeCompute{}
	fc.OnGetComputeCapacityReservation = func(ctx context.Context, req ocicore.GetComputeCapacityReservationRequest) (
		ocicore.GetComputeCapacityReservationResponse, error) {
		return ocicore.GetComputeCapacityReservationResponse{
			ComputeCapacityReservation: ocicore.ComputeCapacityReservation{Id: &id},
		}, nil
	}
	provider := fakeProviderWithCompute(fc)
	_ = provider.getOrCreateUsage(id)
	err := provider.SyncCapacityReservation(context.TODO(), id)
	assert.NoError(t, err)
}

func TestProvider_SyncCapacityReservation_error(t *testing.T) {
	id := "bad-id"
	fc := &fakes.FakeCompute{}
	fc.OnGetComputeCapacityReservation = func(ctx context.Context, req ocicore.GetComputeCapacityReservationRequest) (
		ocicore.GetComputeCapacityReservationResponse, error) {
		return ocicore.GetComputeCapacityReservationResponse{}, errors.New("fail sync")
	}
	provider := fakeProviderWithCompute(fc)
	err := provider.SyncCapacityReservation(context.TODO(), id)
	assert.ErrorContains(t, err, "fail sync")
}

func TestProvider_NewProvider(t *testing.T) {
	fc := &fakes.FakeCompute{}
	compartmentId := compartmentID
	provider := NewProvider(context.Background(), fc, compartmentId)
	assert.NotNil(t, provider)
	assert.Equal(t, fc, provider.computeClient)
	assert.Equal(t, compartmentId, provider.clusterCompartmentId)
	assert.NotNil(t, provider.capResCache)
	assert.NotNil(t, provider.capResSelectorCache)
	assert.NotNil(t, provider.usageMap)
}

func TestProvider_filterCapacityReservations(t *testing.T) {
	t.Run("basic selector", func(t *testing.T) {
		fc := &fakes.FakeCompute{}
		provider := fakeProviderWithCompute(fc)

		id := "test-id"
		name := "test-name"
		ad := "test-ad"
		comp := compartmentID

		fc.OnListComputeCapacityReservations = func(ctx context.Context,
			req ocicore.ListComputeCapacityReservationsRequest) (
			ocicore.ListComputeCapacityReservationsResponse, error) {
			return ocicore.ListComputeCapacityReservationsResponse{
				Items: []ocicore.ComputeCapacityReservationSummary{
					{
						Id: &id, DisplayName: &name, AvailabilityDomain: &ad, CompartmentId: &comp,
						LifecycleState: ocicore.ComputeCapacityReservationLifecycleStateActive,
					},
				},
			}, nil
		}
		fc.OnGetComputeCapacityReservation = func(ctx context.Context,
			req ocicore.GetComputeCapacityReservationRequest) (ocicore.GetComputeCapacityReservationResponse, error) {
			return ocicore.GetComputeCapacityReservationResponse{
				ComputeCapacityReservation: ocicore.ComputeCapacityReservation{
					Id: &id, DisplayName: &name, AvailabilityDomain: &ad, CompartmentId: &comp,
				},
			}, nil
		}

		selector := &v1beta1.OciResourceSelectorTerm{CompartmentId: &comp}
		results, err := provider.filterCapacityReservations(context.Background(), selector)
		assert.NoError(t, err)
		assert.Len(t, results, 1)
		assert.Equal(t, id, *results[0].Id)
	})

	t.Run("selector with display name", func(t *testing.T) {
		fc := &fakes.FakeCompute{}
		provider := fakeProviderWithCompute(fc)

		id := "test-id2"
		name := "test-name"
		ad := "test-ad"
		comp := "test-comp"

		fc.OnListComputeCapacityReservations = func(ctx context.Context,
			req ocicore.ListComputeCapacityReservationsRequest) (
			ocicore.ListComputeCapacityReservationsResponse, error) {
			// Verify that DisplayName is set in the request
			if req.DisplayName != nil && *req.DisplayName == name {
				return ocicore.ListComputeCapacityReservationsResponse{
					Items: []ocicore.ComputeCapacityReservationSummary{
						{
							Id: &id, DisplayName: &name, AvailabilityDomain: &ad, CompartmentId: &comp,
							LifecycleState: ocicore.ComputeCapacityReservationLifecycleStateActive,
						},
					},
				}, nil
			}
			return ocicore.ListComputeCapacityReservationsResponse{}, nil
		}
		fc.OnGetComputeCapacityReservation = func(ctx context.Context,
			req ocicore.GetComputeCapacityReservationRequest) (ocicore.GetComputeCapacityReservationResponse, error) {
			return ocicore.GetComputeCapacityReservationResponse{
				ComputeCapacityReservation: ocicore.ComputeCapacityReservation{
					Id: &id, DisplayName: &name, AvailabilityDomain: &ad, CompartmentId: &comp,
				},
			}, nil
		}

		selector := &v1beta1.OciResourceSelectorTerm{CompartmentId: &comp, DisplayName: &name}
		results, err := provider.filterCapacityReservations(context.Background(), selector)
		assert.NoError(t, err)
		assert.Len(t, results, 1)
		assert.Equal(t, id, *results[0].Id)
	})
}

func TestProvider_listAndFilterCapacityReservations(t *testing.T) {

	id := "idtest"
	comp := "comp"
	ad := "ad"
	name := "n"
	fc := &fakes.FakeCompute{}
	fc.OnListComputeCapacityReservations = func(ctx context.Context,
		req ocicore.ListComputeCapacityReservationsRequest) (ocicore.ListComputeCapacityReservationsResponse, error) {
		return ocicore.ListComputeCapacityReservationsResponse{
			Items: []ocicore.ComputeCapacityReservationSummary{
				{
					Id: &id, DisplayName: &name, AvailabilityDomain: &ad, CompartmentId: &comp,
					LifecycleState: ocicore.ComputeCapacityReservationLifecycleStateActive,
				},
			},
		}, nil
	}
	fc.OnGetComputeCapacityReservation = func(ctx context.Context, req ocicore.GetComputeCapacityReservationRequest) (
		ocicore.GetComputeCapacityReservationResponse, error) {
		return ocicore.GetComputeCapacityReservationResponse{
			ComputeCapacityReservation: ocicore.ComputeCapacityReservation{Id: &id, DisplayName: &name,
				AvailabilityDomain: &ad, CompartmentId: &comp},
		}, nil
	}
	provider := fakeProviderWithCompute(fc)
	items, err := provider.listAndFilterCapacityReservations(context.TODO(),
		ocicore.ListComputeCapacityReservationsRequest{}, nil)
	assert.NoError(t, err)
	assert.Len(t, items, 1)
	assert.Equal(t, id, *items[0].Id)

	fc2 := &fakes.FakeCompute{}
	fc2.OnListComputeCapacityReservations = func(ctx context.Context,
		req ocicore.ListComputeCapacityReservationsRequest) (ocicore.ListComputeCapacityReservationsResponse, error) {
		return ocicore.ListComputeCapacityReservationsResponse{}, errors.New("fail list")
	}
	provider2 := fakeProviderWithCompute(fc2)
	_, err = provider2.listAndFilterCapacityReservations(context.TODO(),
		ocicore.ListComputeCapacityReservationsRequest{}, nil)
	assert.ErrorContains(t, err, "fail list")
}

func TestProvider_listAndFilterCapacityReservations_pagination(t *testing.T) {
	fc := &fakes.FakeCompute{}
	provider := fakeProviderWithCompute(fc)

	// Set up two pages
	id1 := "id1"
	id2 := "id2"
	comp := "comp"
	ad := "ad"
	name := "n"

	fc.CapacityResPages = [][]ocicore.ComputeCapacityReservationSummary{
		{
			{Id: &id1, DisplayName: &name, AvailabilityDomain: &ad, CompartmentId: &comp,
				LifecycleState: ocicore.ComputeCapacityReservationLifecycleStateActive},
		},
		{
			{Id: &id2, DisplayName: &name, AvailabilityDomain: &ad, CompartmentId: &comp,
				LifecycleState: ocicore.ComputeCapacityReservationLifecycleStateActive},
		},
	}

	fc.OnGetComputeCapacityReservation = func(ctx context.Context, req ocicore.GetComputeCapacityReservationRequest) (
		ocicore.GetComputeCapacityReservationResponse, error) {
		var id string
		if req.CapacityReservationId != nil {
			id = *req.CapacityReservationId
		}
		return ocicore.GetComputeCapacityReservationResponse{
			ComputeCapacityReservation: ocicore.ComputeCapacityReservation{Id: &id, DisplayName: &name,
				AvailabilityDomain: &ad, CompartmentId: &comp},
		}, nil
	}

	items, err := provider.listAndFilterCapacityReservations(context.TODO(),
		ocicore.ListComputeCapacityReservationsRequest{}, nil)
	assert.NoError(t, err)
	assert.Len(t, items, 2)
	assert.Equal(t, id1, *items[0].Id)
	assert.Equal(t, id2, *items[1].Id)
}

// fakeReservationWithUsedCount returns a fake compute client serving one reservation with two
// VM.Standard.E5.Flex 16/32 slots, of which *used are taken.
func fakeReservationWithUsedCount(id string, used *int64) *fakes.FakeCompute {
	fc := &fakes.FakeCompute{}
	fc.OnGetComputeCapacityReservation = func(ctx context.Context,
		req ocicore.GetComputeCapacityReservationRequest) (ocicore.GetComputeCapacityReservationResponse, error) {
		return ocicore.GetComputeCapacityReservationResponse{
			ComputeCapacityReservation: ocicore.ComputeCapacityReservation{
				Id: &id, DisplayName: lo.ToPtr("res"), AvailabilityDomain: lo.ToPtr("ad1"),
				CompartmentId: lo.ToPtr(compartmentID),
				InstanceReservationConfigs: []ocicore.InstanceReservationConfig{{
					InstanceShape: lo.ToPtr("VM.Standard.E5.Flex"),
					InstanceShapeConfig: &ocicore.InstanceReservationShapeConfigDetails{
						Ocpus: lo.ToPtr(float32(16)), MemoryInGBs: lo.ToPtr(float32(32)),
					},
					ReservedCount: lo.ToPtr(int64(2)),
					UsedCount:     lo.ToPtr(*used),
				}},
			},
		}, nil
	}
	fc.OnListComputeCapacityReservations = func(ctx context.Context,
		req ocicore.ListComputeCapacityReservationsRequest) (ocicore.ListComputeCapacityReservationsResponse, error) {
		return ocicore.ListComputeCapacityReservationsResponse{
			Items: []ocicore.ComputeCapacityReservationSummary{{
				Id: &id, DisplayName: lo.ToPtr("res"), AvailabilityDomain: lo.ToPtr("ad1"),
				CompartmentId:  lo.ToPtr(compartmentID),
				LifecycleState: ocicore.ComputeCapacityReservationLifecycleStateActive,
			}},
		}, nil
	}
	return fc
}

func e5FlexAvailability(t *testing.T, provider *DefaultProvider,
	cfgs []*v1beta1.CapacityReservationConfig) ShapeAvailability {
	res, err := provider.ResolveCapacityReservations(context.Background(), cfgs)
	require.NoError(t, err)
	require.Len(t, res, 1)
	avail := res[0].AvailabilityForShape("VM.Standard.E5.Flex", lo.ToPtr(float32(16)), lo.ToPtr(float32(32)), true)
	return avail[""]
}

func TestProvider_ResolveCapacityReservations_keepsUsageOnCacheHit(t *testing.T) {
	id := "ocid1.capacityreservation.oc1..usage"
	used := int64(1)
	provider := fakeProviderWithCompute(fakeReservationWithUsedCount(id, &used))
	cfgs := []*v1beta1.CapacityReservationConfig{{CapacityReservationId: &id}}

	assert.Equal(t, int64(1), e5FlexAvailability(t, provider, cfgs).Used)

	// Karpenter launched an instance into the reservation; the cached reservation does not know yet.
	provider.MarkCapacityReservationUsed(&ocicore.Instance{
		CapacityReservationId: &id,
		Shape:                 lo.ToPtr("VM.Standard.E5.Flex"),
		FaultDomain:           lo.ToPtr("FAULT-DOMAIN-1"),
		ShapeConfig: &ocicore.InstanceShapeConfig{
			Ocpus: lo.ToPtr(float32(16)), MemoryInGBs: lo.ToPtr(float32(32)),
		},
	})

	assert.Equal(t, int64(2), e5FlexAvailability(t, provider, cfgs).Used)
}

func TestProvider_SyncCapacityReservation_refreshesResolvedAvailability(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  func(id string) *v1beta1.CapacityReservationConfig
	}{
		{
			name: "by id",
			cfg: func(id string) *v1beta1.CapacityReservationConfig {
				return &v1beta1.CapacityReservationConfig{CapacityReservationId: &id}
			},
		},
		{
			name: "by selector",
			cfg: func(string) *v1beta1.CapacityReservationConfig {
				return &v1beta1.CapacityReservationConfig{
					CapacityReservationFilter: &v1beta1.OciResourceSelectorTerm{DisplayName: lo.ToPtr("res")},
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "ocid1.capacityreservation.oc1..sync"
			used := int64(1)
			provider := fakeProviderWithCompute(fakeReservationWithUsedCount(id, &used))
			cfgs := []*v1beta1.CapacityReservationConfig{tc.cfg(id)}

			assert.Equal(t, int64(1), e5FlexAvailability(t, provider, cfgs).Used)

			// an instance outside the cluster takes the last slot
			used = 2
			require.NoError(t, provider.SyncCapacityReservation(context.Background(), id))

			assert.Equal(t, int64(2), e5FlexAvailability(t, provider, cfgs).Used)
		})
	}
}
