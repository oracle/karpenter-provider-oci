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
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/oracle/karpenter-provider-oci/pkg/oci"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/capacityreservation"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/npn"
	"github.com/oracle/karpenter-provider-oci/pkg/utils"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/awslabs/operatorpkg/status"
	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/blockstorage"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/image"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instance"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancetype"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/kms"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/placement"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	apierros "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	coreapis "sigs.k8s.io/karpenter/pkg/apis"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/utils/resources"
)

type CloudProvider struct {
	kubeClient                  client.Client
	instanceTypeProvider        instancetype.Provider
	imageProvider               image.Provider
	networkProvider             network.Provider
	kmsKeyProvider              kms.Provider
	instanceProvider            instance.Provider
	placementProvider           placement.Provider
	capacityReservationProvider capacityreservation.Provider
	blockStorageProvider        blockstorage.Provider
	npnProvider                 npn.Provider
	repairPolicies              []cloudprovider.RepairPolicy
	initialized                 bool
}

func New(ctx context.Context, client client.Client, itp instancetype.Provider, imgp image.Provider,
	nwp network.Provider, kkp kms.Provider, ip instance.Provider, pp placement.Provider,
	cp capacityreservation.Provider, bsp blockstorage.Provider, npn npn.Provider,
	repairPolicies []cloudprovider.RepairPolicy, startAsync <-chan struct{}) (*CloudProvider, error) {
	c := &CloudProvider{
		kubeClient:                  client,
		instanceTypeProvider:        itp,
		imageProvider:               imgp,
		networkProvider:             nwp,
		kmsKeyProvider:              kkp,
		instanceProvider:            ip,
		placementProvider:           pp,
		capacityReservationProvider: cp,
		blockStorageProvider:        bsp,
		npnProvider:                 npn,
	}

	if len(repairPolicies) > 0 {
		c.repairPolicies = append(c.repairPolicies, repairPolicies...)
	}

	go func() {
		// wait until elected
		select {
		case <-ctx.Done():
			return
		case <-startAsync:
			break
		}

		// run until we warm up placement cache
		for {
			_, err := c.List(ctx)
			if err != nil {
				log.FromContext(ctx).Error(err, "failed to warm up placement cache")
			} else {
				c.initialized = true
				log.FromContext(ctx).Info("placement cache warmed up")
				return
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second * 5):
			}
		}
	}()

	return c, nil
}

// Create launches a NodeClaim with the given resource requests and requirements and returns a hydrated
// NodeClaim back with resolved NodeClaim labels for the launched NodeClaim
func (c *CloudProvider) Create(ctx context.Context, nodeClaim *corev1.NodeClaim) (*corev1.NodeClaim, error) {
	if !c.initialized {
		return nil, errors.New("cloud-provider is not ready for use")
	}

	lg := log.FromContext(ctx, "nodeClaim", nodeClaim.Name)

	lg.Info("launching oci instance for node claim ...")

	// check node class
	nodeClass, err := c.ensureNodeClassFromNodeClaim(ctx, nodeClaim)
	if err != nil {
		if apierros.IsNotFound(err) {
			return nil, cloudprovider.NewCreateError(err, "OciNodeClassNotFound",
				"Unable to find OciNodeClass "+nodeClaim.Spec.NodeClassRef.Name)
		}
		return nil, err
	}

	// filter instance type with requirement
	instanceTypes, err := c.resolveInstanceTypes(ctx, nodeClaim, nodeClass)
	if err != nil {
		return nil, cloudprovider.NewCreateError(fmt.Errorf("resolving instance types, %w", err),
			"InstanceTypeResolutionFailed", "Error resolving instance types")
	}

	instanceTypes = orderInstanceTypesByPrice(instanceTypes,
		scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...))

	if len(instanceTypes) == 0 {
		lg.Error(errors.New("no instance type available"), "")
		return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("no instance type available"))
	}

	lg.Info("ordered instance type", "orderedInstanceType", strings.Join(
		lo.Map(instanceTypes, func(item *instancetype.OciInstanceType, _ int) string {
			return item.Print()
		}), ","))

	networkResolveResult, err := c.networkProvider.ResolveNetworkConfig(ctx, nodeClass.Spec.NetworkConfig)
	if err != nil {
		lg.Error(err, "cannot resolve network")
		return nil, err
	}

	kmsKeyResolveResult, err := c.kmsKeyProvider.ResolveKmsKeyConfig(ctx,
		nodeClass.Spec.VolumeConfig.BootVolumeConfig.KmsKeyConfig)
	if err != nil {
		lg.Error(err, "cannot resolve kms key")
		return nil, err
	}

	var inst *instance.InstanceInfo
	var selectedInstanceType *instancetype.OciInstanceType

	// try launch until all instance types are consumed.
	for _, instanceType := range instanceTypes {
		imageResolveResult, resErr := c.imageProvider.ResolveImageForShape(ctx,
			nodeClass.Spec.VolumeConfig.BootVolumeConfig.ImageConfig, instanceType.Shape)

		if resErr != nil {
			lg.Error(resErr, "cannot resolve image")
			continue
		}

		err = c.placementProvider.PlaceInstance(ctx, nodeClaim, nodeClass, instanceType,
			func(proposal *placement.Proposal) error {
				// in func directly access point to instance to break the cycle dep
				// between placement & instance providers.
				i, launchErr := c.instanceProvider.LaunchInstance(ctx, nodeClaim, nodeClass,
					instanceType, imageResolveResult, networkResolveResult, kmsKeyResolveResult, proposal)
				if launchErr != nil {
					return launchErr
				}

				inst = i
				return nil
			})

		if err != nil {
			lg.Error(err, "cannot launch instance")
			// TODO in capacity reservation case, what error will we get if there is no capacity?
			if instance.IsNoCapacityError(err) {
				continue
			}
			return nil, cloudprovider.NewCreateError(fmt.Errorf("launch instance failure, %w", err),
				"LaunchInstanceFailed", "Error Launching Instance")
		}

		lg.WithValues("instanceId", inst.Instance.Id).Info("oci instance launched")
		selectedInstanceType = instanceType
		break
	}

	if inst == nil {
		errMsg := "cannot create node after trying all instance types"
		finalErr := errors.New(errMsg)
		lg.Error(finalErr, "")
		return nil, cloudprovider.NewCreateError(finalErr, "LaunchInstanceFailed", errMsg)
	}

	c.placementProvider.InstanceFound(nodeClaim.Labels[corev1.NodePoolLabelKey], inst.Instance)
	if inst.Instance.CapacityReservationId != nil {
		c.capacityReservationProvider.MarkCapacityReservationUsed(inst.Instance)
	}

	if c.npnProvider.NpnCluster() {
		lg.Info("Creating NPN Object")
		npnApplyError := c.createAndApplyNpnObject(ctx, inst, nodeClass, networkResolveResult)
		if npnApplyError != nil {
			errMsg := fmt.Sprintf("Failed to apply NPN object: %s",
				npnApplyError.Error())
			lg.Error(npnApplyError, errMsg)
			// We shouldn't return errors from here because if only NPN apply failed it will keep creating new compute instances
			// If NPN apply failed the node claims will end up not able to join the cluster and will be terminated eventually
			// return nil, cloudprovider.NewCreateError(err, "LaunchInstanceFailed", errMsg)
		}
	} else {
		lg.Info("Skipping NPN Object creation")
	}

	nodeClaim = createNodeClaimFromInstanceAndInstanceType(inst.Instance, selectedInstanceType)
	if nodeClaim.Annotations == nil {
		nodeClaim.Annotations = make(map[string]string)
	}

	nodeClaim.Annotations[ociv1beta1.NodeClassHash] = utils.HashNodeClassSpec(nodeClass)
	nodeClaim.Annotations[ociv1beta1.NodeClassHashVersion] = ociv1beta1.OCINodeClassHashVersion
	return nodeClaim, nil
}

func (c *CloudProvider) createAndApplyNpnObject(ctx context.Context, instance *instance.InstanceInfo,
	nodeClass *ociv1beta1.OCINodeClass, networkResolveResult *network.NetworkResolveResult) error {
	npnCustomerObject, constructErr := c.npnProvider.CreateNpnCustomObject(ctx, instance.Instance,
		nodeClass, networkResolveResult)
	if constructErr != nil {
		return constructErr
	}

	applyErr := c.kubeClient.Create(ctx, npnCustomerObject)
	if applyErr != nil {
		return applyErr
	}

	return nil
}

func (c *CloudProvider) createNodeClaimFromInstance(ctx context.Context,
	i *ocicore.Instance) (*corev1.NodeClaim, error) {
	it, err := c.resolveInstanceTypeFromInstance(ctx, i)
	if err != nil {
		return nil, fmt.Errorf("resolving instance type, %w", err)
	}

	return createNodeClaimFromInstanceAndInstanceType(i, it), nil
}

func createNodeClaimFromInstanceAndInstanceType(i *ocicore.Instance,
	it *instancetype.OciInstanceType) *corev1.NodeClaim {
	// karpenter explicitly requires return a new node claim, not the input. The node claim is only partially populated
	nodeClaim := &corev1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Labels: make(map[string]string),
		},
	}

	instancetype.DecorateNodeClaimByInstanceType(nodeClaim, it)
	instance.DecorateNodeClaimByInstance(nodeClaim, i)
	return nodeClaim
}

func (c *CloudProvider) resolveInstanceTypes(ctx context.Context,
	nodeClaim *corev1.NodeClaim, nodeClass *ociv1beta1.OCINodeClass) ([]*instancetype.OciInstanceType, error) {
	instanceTypes, err := c.instanceTypeProvider.ListInstanceTypes(ctx, nodeClass, nodeClaim.Spec.Taints)
	if err != nil {
		return nil, err
	}

	/*
		 how schedule requirement works:
		 #1 before creating node claim, karpenter will make sure instance types compatible with node claim
		(list instance types for a node pool)
		 #2 reqs.Compatible verify customized labels match
		 #3 offering compatible make sure there is an offering match reqs
		 #4 resource fit
	*/
	reqs := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...)
	return lo.Filter(instanceTypes, func(i *instancetype.OciInstanceType, _ int) bool {
		err := reqs.Compatible(i.Requirements, scheduling.AllowUndefinedWellKnownLabels)
		if err != nil {
			log.FromContext(ctx).V(1).Info("requirements not compatible",
				"instanceType", i.Name, "error", err)
			return false
		}
		return len(i.Offerings.Compatible(reqs).Available()) > 0 &&
			resources.Fits(nodeClaim.Spec.Resources.Requests, i.Allocatable())
	}), nil
}

func orderInstanceTypesByPrice(instanceTypes []*instancetype.OciInstanceType,
	requirements scheduling.Requirements) []*instancetype.OciInstanceType {

	instanceTypePrices := make([]*instancetype.OciInstanceType, 0)
	for _, instanceType := range instanceTypes {
		compatibleOfferings := instanceType.Offerings.Available().Compatible(requirements)

		offeringByPrice := lo.GroupBy(compatibleOfferings, func(item *cloudprovider.Offering) float64 {
			return item.Price
		})

		for _, offerings := range offeringByPrice {
			instanceTypePrices = append(instanceTypePrices,
				instanceType.CopyAndUpdateOfferings(offerings))
		}

	}

	// sort by price, cheaper prices goes first
	sort.SliceStable(instanceTypePrices, func(i, j int) bool {
		iPrice := math.MaxFloat64
		jPrice := math.MaxFloat64

		if len(instanceTypePrices[i].Offerings.Available().Compatible(requirements)) > 0 {
			iPrice = instanceTypePrices[i].Offerings.Available().Compatible(requirements).Cheapest().Price
		}

		if len(instanceTypePrices[j].Offerings.Available().Compatible(requirements)) > 0 {
			jPrice = instanceTypePrices[j].Offerings.Available().Compatible(requirements).Cheapest().Price
		}

		if iPrice != jPrice {
			return iPrice < jPrice
		}

		return strings.Compare(instanceTypePrices[i].Name, instanceTypePrices[j].Name) < 0
	})

	return instanceTypePrices
}

func (c *CloudProvider) ensureNodeClassFromNodeClaim(ctx context.Context,
	nodeClaim *corev1.NodeClaim) (*ociv1beta1.OCINodeClass, error) {
	nodeClass := &ociv1beta1.OCINodeClass{}
	if err := c.kubeClient.Get(ctx,
		types.NamespacedName{Name: nodeClaim.Spec.NodeClassRef.Name}, nodeClass); err != nil {
		return nil, err
	}

	// For the purposes of NodeClass CloudProvider resolution, we treat deleting NodeClasses as NotFound
	if !nodeClass.DeletionTimestamp.IsZero() {
		return nil, newTerminatingNodeClassError(nodeClass.Name)
	}

	if err := ensureNodeClassReady(nodeClass); err != nil {
		return nil, err
	}

	return nodeClass, nil
}

func ensureNodeClassReady(nodeClass *ociv1beta1.OCINodeClass) error {
	nodeClassReady := nodeClass.StatusConditions().Get(status.ConditionReady)
	switch {
	case nodeClassReady.IsFalse(), nodeClassReady.IsUnknown():
		return cloudprovider.NewNodeClassNotReadyError(errors.New(nodeClassReady.Message))
	case nodeClassReady != nil && nodeClassReady.ObservedGeneration != nodeClass.Generation:
		return cloudprovider.NewNodeClassNotReadyError(
			fmt.Errorf("nodeclass status has not been reconciled against the latest spec"))
	default:
		return nil
	}
}

func (c *CloudProvider) Get(ctx context.Context, id string) (*corev1.NodeClaim, error) {
	log.FromContext(ctx).V(1).WithValues("nodeClaimName", id).Info("checking oci instance ...")
	i, err := c.instanceProvider.GetInstance(ctx, id)
	if err != nil {
		if oci.IsNotFound(err) {
			return nil, cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("no nodeclaim exists with id %s", id))
		}
		return nil, err
	}

	if instance.IsInstanceTerminated(i) {
		return nil, cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("instance %s terminated", id))
	}

	return c.createNodeClaimFromInstance(ctx, i.Instance)
}

func (c *CloudProvider) resolveInstanceTypeFromInstance(ctx context.Context,
	i *ocicore.Instance) (*instancetype.OciInstanceType, error) {
	nodePool, err := c.resolveNodePoolFromInstance(ctx, i)
	if err != nil {
		// If we can't resolve the NodePool, we fall back to not getting instance type info
		return nil, client.IgnoreNotFound(fmt.Errorf("resolving nodepool, %w", err))
	}

	instanceTypes, err := c.getOciInstanceTypes(ctx, nodePool)
	if err != nil {
		// If we can't resolve the NodePool, we fall back to not getting instance type info
		return nil, client.IgnoreNotFound(fmt.Errorf("resolving nodeclass, %w", err))
	}

	instanceType, _ := lo.Find(instanceTypes, func(it *instancetype.OciInstanceType) bool {
		return it.Name == *i.Shape
	})

	return instanceType, nil
}

func (c *CloudProvider) resolveNodePoolFromInstance(ctx context.Context,
	i *ocicore.Instance) (*corev1.NodePool, error) {
	nodePoolName, ok := instance.GetNodePoolNameFromInstance(i)
	if ok {
		nodePool := &corev1.NodePool{}
		if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: nodePoolName}, nodePool); err != nil {
			return nil, err
		}

		return nodePool, nil
	}

	return nil, apierros.NewNotFound(schema.GroupResource{Group: coreapis.Group, Resource: "nodepools"}, "")
}

func (c *CloudProvider) List(ctx context.Context) ([]*corev1.NodeClaim, error) {
	log.FromContext(ctx).Info("listing all instances...")
	/*
			retrieve all node pools, and then cross filter nodeClass. then for each in use nodeclass, list all instances in
		the compartment (instance provider is smart enough to list instance in cluster compartment if a nodeclass does not
		specify one)
	*/
	nodePoolList := &corev1.NodePoolList{}
	err := c.kubeClient.List(ctx, nodePoolList)
	if err != nil {
		return nil, err
	}

	inUseNodeClassNameSet := sets.New(lo.Map(nodePoolList.Items, func(item corev1.NodePool, _ int) string {
		return item.Spec.Template.Spec.NodeClassRef.Name
	})...)

	nodeClassList := &ociv1beta1.OCINodeClassList{}
	err = c.kubeClient.List(ctx, nodeClassList)
	if err != nil {
		return nil, err
	}

	inUseNodeClasses := lo.Filter(nodeClassList.Items, func(item ociv1beta1.OCINodeClass, _ int) bool {
		return inUseNodeClassNameSet.Has(item.Name)
	})

	nodeCompartments := lo.Uniq(lo.Map(inUseNodeClasses, func(nodeClass ociv1beta1.OCINodeClass, _ int) string {
		if nodeClass.Spec.NodeCompartmentId != nil {
			return *nodeClass.Spec.NodeCompartmentId
		}

		return ""
	}))

	var nodeClaims []*corev1.NodeClaim
	var ins []*ocicore.Instance

	nodePoolNameSet := sets.New(lo.Map(nodePoolList.Items, func(item corev1.NodePool, _ int) string {
		return item.Name
	})...)

	for _, compartmentId := range nodeCompartments {
		ins, err = c.instanceProvider.ListInstances(ctx, compartmentId)
		if err != nil {
			return nil, err
		}

		for _, i := range ins {
			nodeClaim, inerr := c.createNodeClaimFromInstance(ctx, i)
			if inerr != nil {
				return nil, inerr
			}

			// filter out instances that are unknown.
			if nodePoolNameSet.Has(nodeClaim.Labels[corev1.NodePoolLabelKey]) {
				c.placementProvider.InstanceFound(nodeClaim.Labels[corev1.NodePoolLabelKey], i)
				nodeClaims = append(nodeClaims, nodeClaim)
			}
		}
	}

	return nodeClaims, nil
}

func (c *CloudProvider) getOciInstanceTypes(ctx context.Context,
	np *corev1.NodePool) ([]*instancetype.OciInstanceType, error) {
	nodeClass, err := c.resolveNodeClassFromNodePool(ctx, np)
	if err != nil {
		return nil, err
	}

	// ensure node class is ready so offering can be made with nodeClass.status
	err = ensureNodeClassReady(nodeClass)
	if err != nil {
		return nil, err
	}

	return c.instanceTypeProvider.ListInstanceTypes(ctx, nodeClass, np.Spec.Template.Spec.Taints)
}

func (c *CloudProvider) resolveNodeClassFromNodePool(ctx context.Context,
	np *corev1.NodePool) (*ociv1beta1.OCINodeClass, error) {
	nodeClass := &ociv1beta1.OCINodeClass{}
	if err := c.kubeClient.Get(ctx,
		types.NamespacedName{Name: np.Spec.Template.Spec.NodeClassRef.Name}, nodeClass); err != nil {
		return nil, err
	}

	if !nodeClass.DeletionTimestamp.IsZero() {
		// For the purposes of NodeClass CloudProvider resolution, we treat deleting NodeClasses as NotFound,
		// but we return a different error message to be clearer to users
		return nil, newTerminatingNodeClassError(nodeClass.Name)
	}

	return nodeClass, nil
}

func (c *CloudProvider) GetInstanceTypes(ctx context.Context,
	np *corev1.NodePool) ([]*cloudprovider.InstanceType, error) {
	nodeClass, err := c.resolveNodeClassFromNodePool(ctx, np)
	if err != nil {
		return nil, err
	}

	if err := ensureNodeClassReady(nodeClass); err != nil {
		return nil, err
	}

	its, err := c.instanceTypeProvider.ListInstanceTypes(ctx, nodeClass, np.Spec.Template.Spec.Taints)
	if err != nil {
		return nil, err
	}

	return lo.Map(its, func(item *instancetype.OciInstanceType, _ int) *cloudprovider.InstanceType {
		return &item.InstanceType
	}), nil
}

func (c *CloudProvider) Delete(ctx context.Context, nc *corev1.NodeClaim) error {
	log.FromContext(ctx).Info("deleting oci instance for node claim", "nodeClaim", nc.Name)

	// karpenter delete interface is wired, a nil err return is interpreted as delete in progress,
	// and it requires explicit NodeClaimNotFound error. so we return the error if we cannot find the instance or
	// instance is in terminated state
	alreadyDeletedErr := cloudprovider.NewNodeClaimNotFoundError(
		fmt.Errorf("no nodeclaim exists with provider id '%s'",
			nc.Status.ProviderID))

	i, err := c.instanceProvider.GetInstance(ctx, nc.Status.ProviderID)
	if err != nil {
		// oci does not differentiate not authorized or not found, in the case customer revoke permission
		// the handling here may cause dangling resource.
		if oci.IsNotFound(err) {
			c.placementProvider.InstanceForget(nc.Labels[corev1.NodePoolLabelKey], nc.Status.ProviderID)
			return alreadyDeletedErr
		}

		return err
	}

	if instance.IsInstanceTerminated(i) {
		c.placementProvider.InstanceForget(nc.Labels[corev1.NodePoolLabelKey], nc.Status.ProviderID)
		return alreadyDeletedErr
	}

	err = c.instanceProvider.DeleteInstance(ctx, nc.Status.ProviderID)

	if err != nil && oci.IsNotFound(err) {
		return alreadyDeletedErr
	} else if err == nil {
		// releasing capacity reservation depends on a success delete operation, this may be
		// conservative as delete may be timed out in the first try but success before next delete call,
		// thus this is unreachable block which slow down capacity reservation release, be noticed
		// it will self-heal as capacity reservation cache refresh.
		if i.Instance.CapacityReservationId != nil {
			c.capacityReservationProvider.MarkCapacityReservationReleased(i.Instance)
		}
	}

	return err
}

func (c *CloudProvider) IsDrifted(ctx context.Context, nodeClaim *corev1.NodeClaim) (cloudprovider.DriftReason, error) {
	log := log.FromContext(ctx, "nodeClaim", nodeClaim.Name)

	nodeClass, err := c.ensureNodeClassFromNodeClaim(ctx, nodeClaim)
	if err != nil {
		// we don't know as node class is not ready
		return "", fmt.Errorf("drifted status unknown: %v", err)
	}

	log.V(1).Info("node class resolved", "nodeClass", nodeClass.Name)

	instanceTypes, err := c.instanceTypeProvider.ListInstanceTypes(ctx, nodeClass, nodeClaim.Spec.Taints)
	if err != nil {
		return "", fmt.Errorf("drift status unknown: %v", err)
	}

	desiredInstanceType, ok := lo.Find(instanceTypes, func(item *instancetype.OciInstanceType) bool {
		return item.Name == nodeClaim.Labels[v1.LabelInstanceTypeStable]
	})

	if !ok {
		return "ShapeNotFound", nil
	}

	desiredImage, err := c.imageProvider.ResolveImageForShape(ctx,
		nodeClass.Spec.VolumeConfig.BootVolumeConfig.ImageConfig, desiredInstanceType.Shape)
	if err != nil {
		return "", fmt.Errorf("drifted status unknown: %v", err)
	}

	desiredInstanceState := &InstanceDesiredState{
		InstanceType:    desiredInstanceType,
		Image:           desiredImage.Images[0],
		CompartmentOcid: c.instanceProvider.GetInstanceCompartment(nodeClass),
		NodeClass:       nodeClass,
	}

	scheduleRequirements := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...)
	if scheduleRequirements.Has(ociv1beta1.ReservationIDLabel) {
		capResLabelValue := scheduleRequirements.Get(ociv1beta1.ReservationIDLabel).Any()

		// this can happen when nodeClaim is created with reserved capacity but then capacity reservation
		// config is removed later
		if len(nodeClass.Spec.CapacityReservationConfigs) == 0 {
			return CapacityReservationMismatch, nil
		}

		capReservations, capResErr := c.capacityReservationProvider.ResolveCapacityReservations(ctx,
			nodeClass.Spec.CapacityReservationConfigs)
		if capResErr != nil {
			return "", capResErr
		}

		capRes, found := lo.Find(capReservations, func(item capacityreservation.ResolveResult) bool {
			return capacityreservation.OcidToLabelValue(item.Ocid) == capResLabelValue
		})

		if !found {
			return CapacityReservationMismatch, nil
		}

		desiredInstanceState.CapacityReservationId = &capRes.Ocid
	}

	// ------ 0. NodeClass static field hash not matching NodeClaim hash or hash version are not matching --------
	staticFieldDriftReason := AreStaticFieldsDrifted(ctx, nodeClaim, nodeClass)
	if staticFieldDriftReason != "" {
		log.Info("static fields drift detected", "driftReason", staticFieldDriftReason)
		return staticFieldDriftReason, nil
	}

	// ---- 1. INSTANCE DRIFT (CACHED, then REAL) ----
	instanceCached, err := c.instanceProvider.GetInstanceCached(ctx, nodeClaim.Status.ProviderID)
	// TODO: handle not found
	if err != nil {
		return "", fmt.Errorf("drifted status unknown: %v", err)
	}
	reason, err := IsInstanceDrifted(desiredInstanceState, instanceCached.Instance)
	if err != nil {
		return "", err
	}
	if reason != "" {
		// Confirm with real OCI call
		instanceReal, err := c.instanceProvider.GetInstance(ctx, nodeClaim.Status.ProviderID)
		if err != nil {
			return "", fmt.Errorf("drifted status unknown: %v", err)
		}
		finalReason, err := IsInstanceDrifted(desiredInstanceState, instanceReal.Instance)
		if err != nil {
			return "", fmt.Errorf("drifted status unknown: %v", err)
		}
		if finalReason != "" {
			log.Info("instance drift detected", "driftReason", finalReason)
			return finalReason, nil
		}
	}

	// ---- 2. NETWORK/VNIC DRIFT (CACHED, then REAL) ----
	vnicCached, err := c.instanceProvider.ListInstanceVnicAttachmentsCached(ctx,
		*instanceCached.CompartmentId, *instanceCached.Id)
	if err != nil {
		return "", err
	}

	var skipSecondaryVnicsCheck bool
	if !nodeClaim.StatusConditions().Get(corev1.ConditionTypeInitialized).IsTrue() {
		skipSecondaryVnicsCheck = true
	}

	reason, err = IsInstanceNetworkDrifted(ctx, desiredInstanceState, vnicCached,
		func(ctx context.Context, vnicOcid string) (*ocicore.Vnic, error) {
			return c.networkProvider.GetVnicCached(ctx, vnicOcid)
		}, skipSecondaryVnicsCheck)
	if err != nil {
		return reason, err
	}
	if reason != "" {
		// Confirm with real OCI call
		vnicReal, err := c.instanceProvider.ListInstanceVnicAttachments(ctx,
			*instanceCached.CompartmentId, *instanceCached.Id)
		if err != nil {
			return reason, err
		}
		finalReason, err := IsInstanceNetworkDrifted(ctx, desiredInstanceState, vnicReal,
			func(ctx context.Context, vnicOcid string) (*ocicore.Vnic, error) {
				return c.networkProvider.GetVnic(ctx, vnicOcid)
			}, skipSecondaryVnicsCheck)
		if err != nil {
			return finalReason, err
		}
		if finalReason != "" {
			log.Info("instance network drift detected", "driftReason", finalReason)
			return finalReason, nil
		}
	}

	// ---- 3. BOOT VOLUME DRIFT (CACHED, then REAL) ----
	bootCached, err := c.instanceProvider.ListInstanceBootVolumeAttachmentsCached(ctx,
		*instanceCached.CompartmentId, *instanceCached.Id, *instanceCached.AvailabilityDomain)
	if err != nil {
		return "", err
	}
	reason, err = IsInstanceBootVolumeDrifted(ctx, desiredInstanceState, bootCached,
		func(ctx context.Context, bootVolumeOcid string) (*ocicore.BootVolume, error) {
			return c.blockStorageProvider.GetBootVolumeCached(ctx, bootVolumeOcid)
		})
	if err != nil {
		return reason, err
	}
	if reason != "" {
		// Confirm with real OCI call
		bootReal, err := c.instanceProvider.ListInstanceBootVolumeAttachments(ctx,
			*instanceCached.CompartmentId, *instanceCached.Id, *instanceCached.AvailabilityDomain)
		if err != nil {
			return reason, err
		}
		finalReason, err := IsInstanceBootVolumeDrifted(ctx, desiredInstanceState, bootReal,
			func(ctx context.Context, bootVolumeOcid string) (*ocicore.BootVolume, error) {
				return c.blockStorageProvider.GetBootVolume(ctx, bootVolumeOcid)
			})
		if err != nil {
			return reason, err
		}
		if finalReason != "" {
			log.Info("instance boot volume drift detected", "driftReason", finalReason)
			return finalReason, nil
		}
	}

	return "", nil // No drift detected after real checks
}

func (c *CloudProvider) RepairPolicies() []cloudprovider.RepairPolicy {
	return c.repairPolicies
}

// Name returns the CloudProvider implementation name.
func (c *CloudProvider) Name() string {
	return "oci"
}

func (c *CloudProvider) GetSupportedNodeClasses() []status.Object {
	return []status.Object{&ociv1beta1.OCINodeClass{}}
}

// newTerminatingNodeClassError returns a NotFound error for handling by
func newTerminatingNodeClassError(name string) *apierros.StatusError {
	qualifiedResource := schema.GroupResource{Group: ociv1beta1.Group, Resource: "ocinodeclasses"}
	err := apierros.NewNotFound(qualifiedResource, name)
	err.ErrStatus.Message = fmt.Sprintf("%s %q is terminating, treating as not found",
		qualifiedResource.String(), name)
	return err
}
