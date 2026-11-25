/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package placement

import (
	"context"
	"errors"
	"sync"

	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/oci"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/capacityreservation"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/clusterplacementgroup"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/computecluster"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/identity"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancetype"
	"github.com/oracle/karpenter-provider-oci/pkg/utils"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

type Provider interface {
	PlaceInstance(ctx context.Context,
		claim *corev1.NodeClaim, nodeClass *ociv1beta1.OCINodeClass,
		instanceType *instancetype.OciInstanceType,
		placeFunc func(*Proposal) error) error
	InstanceFound(nodePool string, instance *ocicore.Instance)
	InstanceForget(nodePool string, instanceId string)
}

type DefaultProvider struct {
	mutex                         sync.Mutex
	instancesByNodePool           map[string]*adFdSummary
	capacityReservationProvider   capacityreservation.Provider
	clusterPlacementGroupProvider clusterplacementgroup.Provider
	computeClusterProvider        computecluster.Provider
	identityProvider              identity.Provider
}

func NewProvider(ctx context.Context,
	capacityReservationProvider capacityreservation.Provider,
	computeClusterProvider computecluster.Provider,
	clusterPlacementGroupProvider clusterplacementgroup.Provider,
	identityProvider identity.Provider) (*DefaultProvider, error) {
	return &DefaultProvider{
		instancesByNodePool:           make(map[string]*adFdSummary),
		capacityReservationProvider:   capacityReservationProvider,
		computeClusterProvider:        computeClusterProvider,
		clusterPlacementGroupProvider: clusterPlacementGroupProvider,
		identityProvider:              identityProvider,
	}, nil
}

func (p *DefaultProvider) InstanceFound(nodePool string, instance *ocicore.Instance) {
	p.findAdSummaryForNodePool(nodePool).update(instance)
}

func (p *DefaultProvider) InstanceForget(nodePool string, instanceId string) {
	p.findAdSummaryForNodePool(nodePool).forget(instanceId)
}

func (p *DefaultProvider) findAdSummaryForNodePool(nodePool string) *adFdSummary {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	v, ok := p.instancesByNodePool[nodePool]
	if !ok {
		v = newAdFdSummary()
		p.instancesByNodePool[nodePool] = v
	}

	return v
}

func (p *DefaultProvider) PlaceInstance(ctx context.Context,
	claim *corev1.NodeClaim, nodeClass *ociv1beta1.OCINodeClass,
	instanceType *instancetype.OciInstanceType,
	placeFunc func(*Proposal) error) error {
	eligibleOfferings := instanceType.Offerings.Available().Compatible(
		scheduling.NewNodeSelectorRequirementsWithMinValues(claim.Spec.Requirements...))

	if len(eligibleOfferings) == 0 {
		return errors.New("no compatible offering")
	}

	placementDecorateFunc, err := p.placementDecorateFunc(ctx, nodeClass, instanceType)
	if err != nil {
		return err
	}

	nodeClaimFdRequirement, hasFdPlacementFromNodeClaim := lo.Find(claim.Spec.Requirements,
		func(item corev1.NodeSelectorRequirementWithMinValues) bool {
			return item.Key == ociv1beta1.OciFaultDomain
		})

	proposals := lo.FlatMap(eligibleOfferings, func(item *cloudprovider.Offering, _ int) []Proposal {
		ad := utils.ZoneLabelValueToAd(item.Zone(), p.identityProvider.GetLogicalAdPrefix())
		proposal := Proposal{
			tempIdentifier: claim.Name,
			Ad:             ad,
		}

		if item.Requirements.Has(ociv1beta1.ReservationIDLabel) &&
			item.Requirements.Get(ociv1beta1.ReservationIDLabel).Operator() == v1.NodeSelectorOpIn {
			capResId := item.ReservationID()
			proposal.CapacityReservationId = &capResId

			// an offering can have fd placement requirement if it is from fd capacity reservation.
			if item.Requirements.Has(ociv1beta1.OciFaultDomain) {
				fd := item.Requirements.Get(ociv1beta1.OciFaultDomain).Any()
				proposal.Fd = &fd
			}
		}

		// this should only happen when pod need be placed in a specific fd from topology spread/affinity/node selector
		// perspective, in this case fd should pass thru to compute in proposal
		if hasFdPlacementFromNodeClaim {
			proposal.Fd = &nodeClaimFdRequirement.Values[0]
		}

		return placementDecorateFunc(proposal)
	})

	nodePool := claim.Labels[corev1.NodePoolLabelKey]

	// this is a loop to dynamically sort proposals everytime before placing instance.
	// if instance is placed, then update placement summary and return instance;
	// otherwise continue to try next proposals until run out of proposals then last error is returned.
	s := p.findAdSummaryForNodePool(nodePool)
	for {
		pr := s.Propose(proposals, instanceType.Shape, instanceType.Ocpu, instanceType.MemoryInGbs)

		func() {
			defer func() {
				// forget the temp proposal immediately, there is a slight race condition window
				// that an instance is placed but not tracked in placement view (instanceFound)
				s.forget(pr.tempIdentifier)
			}()

			err = placeFunc(pr)
		}()

		if err != nil {
			// A 429 throttling error is scoped to the tenancy, not to this
			// proposal's AD, so trying the remaining proposals cannot
			// succeed; it only adds more calls against the same throttle.
			// Return at once and let Karpenter core requeue the NodeClaim
			// with backoff.
			if oci.IsTooManyRequests(err) {
				return err
			}
			if len(proposals) > 1 {
				// drop last proposal and try next
				proposals = proposals[1:]
				continue
			} else {
				return err
			}
		} else {
			return nil
		}
	}
}

func (p *DefaultProvider) placementDecorateFunc(ctx context.Context,
	nodeClass *ociv1beta1.OCINodeClass,
	instanceType *instancetype.OciInstanceType) (func(Proposal) []Proposal, error) {
	var computeCluster *computecluster.ResolveResult
	var clusterPlacementGroupMap map[string][]clusterplacementgroup.ResolveResult
	var err error

	if nodeClass.Spec.ComputeClusterConfig != nil {
		computeCluster, err = p.computeClusterProvider.ResolveComputeCluster(ctx,
			nodeClass.Spec.ComputeClusterConfig)
		if err != nil {
			return nil, err
		}
	}

	if len(nodeClass.Spec.ClusterPlacementGroupConfigs) > 0 {
		var cpgs []clusterplacementgroup.ResolveResult
		cpgs, err = p.clusterPlacementGroupProvider.ResolveClusterPlacementGroups(ctx,
			nodeClass.Spec.ClusterPlacementGroupConfigs)
		if err != nil {
			return nil, err
		}

		clusterPlacementGroupMap = lo.GroupBy(cpgs, func(item clusterplacementgroup.ResolveResult) string {
			return item.Ad
		})
	}

	// placement decorate is exclusive, only accept one of capacity reservation, compute cluster
	// or cluster placement group, considerations are:
	// 1.If capacity reservation is required within a cpg, then the CPG should be specified in the capacity reservation.
	// 2.ComputeCluster maps to an HPC island which is ad-local, so that rules out any out of ad offering. Since customer
	// already get dedicated hpc island, then capacity reservation or cpg does not make sense.
	// 3.CPG is also an ad-local resource, so it rules out any out of ad offering.
	// 4.we still pass around FD in computeCluster case, which ideally customer should be recommended to not care about
	// FD placement in public doc.
	return func(pr Proposal) []Proposal {
		// no special placement requested
		if computeCluster == nil && clusterPlacementGroupMap == nil && pr.CapacityReservationId == nil {
			return []Proposal{pr}
		}

		// propose against a capacity reservation, cap res must be evaluated dynamically
		if pr.CapacityReservationId != nil {
			if nodeClass.Spec.CapacityReservationConfigs != nil {
				var capRes []capacityreservation.ResolveResult
				capRes, err = p.capacityReservationProvider.ResolveCapacityReservations(ctx,
					nodeClass.Spec.CapacityReservationConfigs)

				if rr, ok := lo.Find(capRes, func(item capacityreservation.ResolveResult) bool {
					return capacityreservation.OcidToLabelValue(item.Ocid) == *pr.CapacityReservationId
				}); ok {
					fdAvailMap := rr.AvailabilityForShape(instanceType.Shape,
						instanceType.Ocpu, instanceType.MemoryInGbs, instanceType.SupportShapeConfig)

					// find fd availability, be noticed pr.Fd can be nil, which represent AD level
					if fdAvail, fdOk := fdAvailMap[lo.FromPtr(pr.Fd)]; fdOk {
						if fdAvail.Total > fdAvail.Used {
							// replace it with real capacity reservation id
							pr.CapacityReservationId = &rr.Ocid
							return []Proposal{pr}
						}
					}
				}
			}

			return nil
		}

		// has computeCluster, disable out of ad proposal
		if computeCluster != nil {
			if pr.Ad != computeCluster.Ad {
				return nil
			}

			pr.ComputeClusterId = &computeCluster.Ocid
			return []Proposal{pr}
		} else if clusterPlacementGroupMap != nil { // has cpgs, disable out of ad proposal
			cpgsInAd, ok := clusterPlacementGroupMap[pr.Ad]
			if !ok {
				return nil
			}

			return lo.Map(cpgsInAd, func(item clusterplacementgroup.ResolveResult, _ int) Proposal {
				return Proposal{
					Ad:                      pr.Ad,
					Fd:                      pr.Fd,
					ClusterPlacementGroupId: &item.Ocid,
					tempIdentifier:          pr.tempIdentifier,
				}
			})
		}

		return nil
	}, nil
}
