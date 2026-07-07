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
	"sync"
	"time"

	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/cache"
	"github.com/oracle/karpenter-provider-oci/pkg/oci"
	"github.com/oracle/karpenter-provider-oci/pkg/utils"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
)

var (
	InvalidCapacityReservationConfigError = errors.New("define either ocid or capacityReservation filter")
)

type Provider interface {
	ResolveCapacityReservations(ctx context.Context,
		capacityReservationConfigs []*v1beta1.CapacityReservationConfig) ([]ResolveResult, error)
	MarkCapacityReservationUsed(instance *ocicore.Instance)
	MarkCapacityReservationReleased(instance *ocicore.Instance)
	SyncCapacityReservation(ctx context.Context, capacityReservationId string) error
}

type DefaultProvider struct {
	computeClient        oci.ComputeClient
	clusterCompartmentId string
	capResCache          *cache.GetOrLoadCache[*CapResWithLoadTime]
	capResSelectorCache  *cache.GetOrLoadCache[[]*CapResWithLoadTime]
	usageMap             map[string]*Usage
	mutex                sync.Mutex
}

func NewProvider(ctx context.Context, computeClient oci.ComputeClient, clusterCompartmentId string) *DefaultProvider {
	return &DefaultProvider{
		computeClient:        computeClient,
		clusterCompartmentId: clusterCompartmentId,
		capResCache:          cache.NewDefaultGetOrLoadCache[*CapResWithLoadTime](),
		capResSelectorCache:  cache.NewDefaultGetOrLoadCache[[]*CapResWithLoadTime](),
		usageMap:             make(map[string]*Usage),
	}
}

func (p *DefaultProvider) ResolveCapacityReservations(ctx context.Context,
	capacityReservationConfigs []*v1beta1.CapacityReservationConfig) ([]ResolveResult, error) {
	if len(capacityReservationConfigs) == 0 {
		return nil, errors.New("no capacityReservationConfig specified")
	}

	capRes := make([]ResolveResult, 0)
	for _, cfg := range capacityReservationConfigs {
		if cfg.CapacityReservationId != nil && cfg.CapacityReservationFilter != nil {
			return nil, InvalidCapacityReservationConfigError
		}

		if cfg.CapacityReservationId != nil {
			c, err := p.getCapacityReservation(ctx, *cfg.CapacityReservationId)
			if err != nil {
				return capRes, err
			}

			capRes = append(capRes, toCapacityResolveResult(c))
		} else {
			cs, err := p.filterCapacityReservations(ctx, cfg.CapacityReservationFilter)
			if err != nil {
				return capRes, err
			}

			capRes = append(capRes, utils.MapNoIndex(cs, toCapacityResolveResult)...)
		}
	}

	// reconcile usage so we don't over-shoot.
	return lo.Map(capRes, func(capRes ResolveResult, _ int) ResolveResult {
		usage := p.getUsage(capRes.Ocid)

		// as capRes is loaded before usage, adjust it accordingly, this might be
		// conservative as capRes can be used and released not known by us, should be
		// reconciled when cache expire.
		if usage != nil && capRes.time.Before(usage.lastCommit) {
			return capRes.adjustCapacityAgainstUsage(usage)
		}

		return capRes
	}), nil
}

func (p *DefaultProvider) MarkCapacityReservationUsed(instance *ocicore.Instance) {
	if instance.CapacityReservationId == nil {
		return
	}

	p.getOrCreateUsage(*instance.CapacityReservationId).IncreaseUsage(instance)
}

func (p *DefaultProvider) MarkCapacityReservationReleased(instance *ocicore.Instance) {
	if instance.CapacityReservationId == nil {
		return
	}

	p.getOrCreateUsage(*instance.CapacityReservationId).DecreaseUsage(instance)
}

func (p *DefaultProvider) getOrCreateUsage(capacityReservationId string) *Usage {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	capEntry, ok := p.usageMap[capacityReservationId]
	if !ok {
		capEntry = newUsage()
		p.usageMap[capacityReservationId] = capEntry
	}

	return capEntry
}

func (p *DefaultProvider) getUsage(capacityReservationId string) *Usage {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	return p.usageMap[capacityReservationId]
}

// SyncCapacityReservation offer ability to evict capacity reservation in case it is out of sync
func (p *DefaultProvider) SyncCapacityReservation(ctx context.Context, capacityReservationId string) error {
	p.capResCache.Evict(capacityReservationId)
	_, err := p.getCapacityReservation(ctx, capacityReservationId)
	if err != nil {
		return err
	}

	return nil
}

//nolint:dupl
func (p *DefaultProvider) filterCapacityReservations(ctx context.Context,
	selector *v1beta1.OciResourceSelectorTerm) ([]*CapResWithLoadTime, error) {
	key, err := utils.HashFor(selector)
	if err != nil {
		return nil, err
	}

	compartmentId := p.clusterCompartmentId
	if selector.CompartmentId != nil {
		compartmentId = *selector.CompartmentId
	}

	var displayName *string
	if selector.DisplayName != nil {
		displayName = selector.DisplayName
	}

	listReq := ocicore.ListComputeCapacityReservationsRequest{
		CompartmentId: &compartmentId,
		DisplayName:   displayName,
	}

	tagFilterFunc := utils.ToTagFilterFunc(selector,
		func(s *ocicore.ComputeCapacityReservationSummary) map[string]string {
			return s.FreeformTags
		},
		func(s *ocicore.ComputeCapacityReservationSummary) map[string]map[string]interface{} {
			return s.DefinedTags
		},
	)

	return p.capResSelectorCache.GetOrLoad(ctx, key,
		func(ctx context.Context, s string) ([]*CapResWithLoadTime, error) {
			return p.listAndFilterCapacityReservations(ctx, listReq, tagFilterFunc)
		})
}

//nolint:dupl
func (p *DefaultProvider) getCapacityReservation(ctx context.Context, ocid string) (*CapResWithLoadTime, error) {
	c, err := p.capResCache.GetOrLoad(ctx, ocid,
		func(ctx context.Context, s string) (*CapResWithLoadTime, error) {
			resp, err := p.computeClient.GetComputeCapacityReservation(ctx,
				ocicore.GetComputeCapacityReservationRequest{
					CapacityReservationId: &ocid,
				})

			if err != nil {
				return nil, err
			}

			return &CapResWithLoadTime{
				resp.ComputeCapacityReservation,
				time.Now(),
			}, nil
		})

	if err == nil {
		p.mutex.Lock()
		defer p.mutex.Unlock()

		// reset usage to respect capacity reservation response.
		delete(p.usageMap, ocid)
	}

	return c, err
}

//nolint:dupl
func (p *DefaultProvider) listAndFilterCapacityReservations(ctx context.Context,
	request ocicore.ListComputeCapacityReservationsRequest,
	extraFilterFunc func(i *ocicore.ComputeCapacityReservationSummary) bool) ([]*CapResWithLoadTime,
	error) {
	var capacityReservations []*CapResWithLoadTime
	for {
		resp, err := p.computeClient.ListComputeCapacityReservations(ctx, request)

		if err != nil {
			return nil, err
		}

		for _, item := range resp.Items {
			if item.LifecycleState == ocicore.ComputeCapacityReservationLifecycleStateDeleted {
				continue
			}

			// Compute API has a bug that filtering by displayName doesn't work
			if request.DisplayName != nil {
				if *item.DisplayName != *request.DisplayName {
					continue
				}
			}

			if extraFilterFunc == nil || extraFilterFunc(&item) {
				// get again here as list does not return InstanceReservationConfigs
				c, inerr := p.getCapacityReservation(ctx, *item.Id)
				if inerr != nil {
					return capacityReservations, inerr
				}

				capacityReservations = append(capacityReservations, c)
			}
		}

		request.Page = resp.OpcNextPage
		if request.Page == nil {
			break
		}
	}

	return capacityReservations, nil
}
