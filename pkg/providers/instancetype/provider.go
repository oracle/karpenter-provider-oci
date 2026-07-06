/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package instancetype

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-semver/semver"
	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/metrics"
	"github.com/oracle/karpenter-provider-oci/pkg/oci"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/capacityreservation"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/clusterplacementgroup"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/computecluster"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/identity"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	"github.com/oracle/karpenter-provider-oci/pkg/utils"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/utils/resources"
)

const (
	minK8sMinorForMultiFlex = 31

	MemoryAvailable = "memory.available"
	NodeFSAvailable = "nodefs.available"
	trueStr         = "true"

	PreemptibleTaintKey = "oci.oraclecloud.com/oke-is-preemptible"
	NvidiaGpuTaintKey   = "nvidia.com/gpu"
	AmdGpuTaintKey      = "amd.com/gpu"

	NvidiaGpuResourceName = v1.ResourceName("nvidia.com/gpu")
	AmdGpuResourceName    = v1.ResourceName("amd.com/gpu")
)

type Provider interface {
	ListInstanceTypes(ctx context.Context,
		nodeClass *ociv1beta1.OCINodeClass, taints []v1.Taint) ([]*OciInstanceType, error)
}

// refresh every day - compute shapes doesn't change often.
var refreshInterval = time.Hour * 24

type DefaultProvider struct {
	region                        string
	identityProvider              identity.Provider
	computeClient                 oci.ComputeClient
	shapeAdMap                    map[string]*ShapeAndAd
	clusterCompartmentId          string
	capacityReservationProvider   capacityreservation.Provider
	computeClusterProvider        computecluster.Provider
	clusterPlacementGroupProvider clusterplacementgroup.Provider
	shapeToPrice                  map[string]*ShapePriceInfo
	preemptibleShapes             PreemptibleShapes
	computeClusterShapes          []string
	shapeMetaFile                 string
	client                        client.Reader
	GlobalShapeConfigs            []*ociv1beta1.ShapeConfig
	kubernetesInterface           kubernetes.Interface
	k8sVersion                    *semver.Version
	ipFamilies                    []network.IpFamily

	lock sync.RWMutex
}

func New(ctx context.Context,
	region string,
	clusterCompartmentOcid string,
	computeClient oci.ComputeClient,
	identityProvider identity.Provider,
	kubernetesInterface kubernetes.Interface,
	directClient client.Reader,
	capResProvider capacityreservation.Provider,
	computeClusterProvider computecluster.Provider,
	clusterPlacementGroupProvider clusterplacementgroup.Provider,
	shapeMetaFile string,
	metaRefreshInterval time.Duration,
	globalShapeConfigs []ociv1beta1.ShapeConfig,
	ipFamilies []network.IpFamily,
	startAsync <-chan struct{}) (*DefaultProvider, error) {
	p := &DefaultProvider{
		region:                        region,
		identityProvider:              identityProvider,
		computeClient:                 computeClient,
		clusterCompartmentId:          clusterCompartmentOcid,
		capacityReservationProvider:   capResProvider,
		computeClusterProvider:        computeClusterProvider,
		clusterPlacementGroupProvider: clusterPlacementGroupProvider,
		shapeToPrice:                  make(map[string]*ShapePriceInfo),
		preemptibleShapes:             make(PreemptibleShapes),
		shapeMetaFile:                 shapeMetaFile,
		client:                        directClient,
		ipFamilies:                    ipFamilies,
		kubernetesInterface:           kubernetesInterface,
	}

	p.GlobalShapeConfigs = lo.Map(globalShapeConfigs, func(item ociv1beta1.ShapeConfig, _ int) *ociv1beta1.ShapeConfig {
		return lo.ToPtr(item)
	})

	// Refresh OCI shapes (AD listings)
	go utils.RefreshAtInterval(ctx, true, startAsync, refreshInterval, p.refreshShapes)()
	// Refresh pricing and preemptible shape metadata
	go utils.RefreshAtInterval(ctx, true, startAsync, metaRefreshInterval, p.reloadConfigFile)()
	// Refresh cluster version
	go utils.RefreshAtInterval(ctx, true, startAsync, refreshInterval, p.refreshClusterVersion)()

	return p, nil
}

// listInstanceTypesForFlexShape generates all instance type permutations for a flexible shape
// based on nodeClass.Spec.ShapeConfigs (or global shape configs if nil).
// Each returned OciInstanceType represents a unique (ocpus, memory, baseline) combinatorial config.
// Naming convention: ShapeName.<X>o.<Y>g.<Z>b where X is ocpu Y is MemoryInGB and Z is baseline cpu usage
func (p *DefaultProvider) listInstanceTypesForFlexShape(ctx context.Context, shape *ocicore.Shape,
	nodeClass *ociv1beta1.OCINodeClass) []*OciInstanceType {

	results := make([]*OciInstanceType, 0)

	if nodeClass == nil {
		return results
	}
	// Get shape configs to enumerate
	var shapeConfigs []*ociv1beta1.ShapeConfig
	if len(nodeClass.Spec.ShapeConfigs) > 0 {
		shapeConfigs = nodeClass.Spec.ShapeConfigs
	} else if len(p.GlobalShapeConfigs) > 0 {
		log.FromContext(ctx).V(1).Info("using global shape config",
			"operation", "list_flex_shape",
			"reason", "nodeclass_shape_configs_absent")
		shapeConfigs = p.GlobalShapeConfigs
	} else {
		log.FromContext(ctx).V(1).Info("flexible shapes not allowed",
			"operation", "list_flex_shape",
			"reason", "no_shape_configs_in_nodeclass_or_global")
		// No configs means no valid offerings for flex shapes
		return results
	}

	if len(shapeConfigs) > 1 {
		// On Kubernetes versions < 1.31, limit to a single ShapeConfig as flex shape support isn't there in CCM
		if p.k8sVersion == nil {
			log.FromContext(ctx).Info("multiple shapeConfigs only supported on Kubernetes v1.31+; using first entry",
				"operation", "list_flex_shape")
			shapeConfigs = shapeConfigs[:1]
		} else if p.k8sVersion.Minor < minK8sMinorForMultiFlex {
			log.FromContext(ctx).Info("multiple shapeConfigs only supported on Kubernetes v1.31+; using first entry",
				"operation", "list_flex_shape",
				"clusterVersion", p.k8sVersion.String())
			shapeConfigs = shapeConfigs[:1]
		}
	}

	for _, cfg := range shapeConfigs {
		// Validation: need at least ocpus configured
		if cfg.Ocpus == nil {
			log.FromContext(ctx).Info("shapeConfig contains nil ocpus, ignoring",
				"operation", "list_flex_shape")
			continue
		}

		ocpu := *cfg.Ocpus
		var memory float32

		if *cfg.Ocpus > *shape.OcpuOptions.Max || *cfg.Ocpus < *shape.OcpuOptions.Min {
			log.FromContext(ctx).Info("shapeConfig ocpus are not within allowed shape range",
				"operation", "list_flex_shape",
				"shape", *shape.Shape, "ocpus", *cfg.Ocpus)
			continue
		}

		cpuBaseline := ociv1beta1.BASELINE_1_1
		if cfg.BaselineOcpuUtilization != nil {
			cpuBaseline = *cfg.BaselineOcpuUtilization
		}
		// Baseline_X not supported in all shapes
		if cpuBaseline != ociv1beta1.BASELINE_1_1 {
			supported := false
			if len(shape.BaselineOcpuUtilizations) != 0 {
				for _, supportedBaseline := range shape.BaselineOcpuUtilizations {
					if string(cpuBaseline) == string(supportedBaseline) {
						supported = true
						break
					}
				}
			}
			if !supported {
				log.FromContext(ctx).V(1).Info("shapeConfig baselineOcpuUtilization is not within allowed shape range",
					"operation", "list_flex_shape",
					"shape", *shape.Shape, "baselineOcpuUtilization", cpuBaseline)
				continue
			}
		}

		baselineFactor := 1
		baselineSuffix := "1_1b"

		switch cpuBaseline {
		case ociv1beta1.BASELINE_1_2:
			baselineFactor = 2
			baselineSuffix = "1_2b"
		case ociv1beta1.BASELINE_1_8:
			baselineFactor = 8
			baselineSuffix = "1_8b"
		}

		if cfg.MemoryInGbs != nil {
			if *cfg.MemoryInGbs < *shape.MemoryOptions.MinInGBs || *cfg.MemoryInGbs > *shape.MemoryOptions.MaxInGBs {
				log.FromContext(ctx).Info("shapeConfig memoryInGbs are not within allowed shape range",
					"operation", "list_flex_shape",
					"shape", *shape.Shape, "memoryInGbs", *cfg.MemoryInGbs)
				continue
			}
			memory = *cfg.MemoryInGbs
		} else {
			// This is replication of compute logic
			memory = ocpu * (*shape.MemoryOptions.DefaultPerOcpuInGBs) / float32(baselineFactor)
		}

		// Generate name like: ShapeName.4o.32g.1_8b
		components := []string{*shape.Shape, fmt.Sprintf("%.0fo", ocpu), fmt.Sprintf("%.0fg", memory),
			baselineSuffix}
		name := strings.Join(components, ".")

		it := &OciInstanceType{
			InstanceType: cloudprovider.InstanceType{Name: name},
			Shape:        *shape.Shape,
		}
		it.Ocpu = &ocpu
		it.MemoryInGbs = &memory
		it.BaselineOcpuUtilization = &cpuBaseline
		it.SupportShapeConfig = true // By definition, if this block runs

		results = append(results, it)
	}
	return results
}

// decorateInstanceType populates resource, additional requirements and cost information.
// Besides populating metadata like cost/preemptible/availability, it also handles flexible & burstable shape.
// nolint:lll
func (p *DefaultProvider) decorateInstanceType(ctx context.Context, it *OciInstanceType,
	nodeClass *ociv1beta1.OCINodeClass, shapeAndAd *ShapeAndAd, taints []v1.Taint) error {
	if it == nil || nodeClass == nil || shapeAndAd == nil || shapeAndAd.Shape == nil {
		return nil
	}

	/*
		Capacity: Cpu/Memory are set from calculated result, disk and maxPods are set from node class,
		GPU/NVME/RDMA/VNIC attachments TBD. Overhead: https://docs.oracle.com/en-us/iaas/Content/ContEng/Tasks/contengbestpractices_topic-Cluster-Management-best-practices.htm#contengbestpractices_topic-Cluster-Management-best-practices__ManagingOKEClusters-Reserveresourcesforkubernetesandossystemdaemons
		it can be also controlled from nodeClass when specified.
		Offering: Price is based on cpu, memory, utilization baseline and other settings,
		available is calculated based on shape, flexible and burstable config. Rules as follows:
		1# if current shape is flexible but there is no flexible config, the current shape is calculated at cost of minCpu &
		minMemory plus other resource, available is set to false.
		2# if there is burstable config but current shape doesn't support that burstable config, the current shape is calculated
		at cost of BASELINE_1_1 plus other resource, available is set to false
		3# free tier billing type resource is always unavailable
	*/

	shape := shapeAndAd.Shape
	// Attributes (ocpu, memory, baseline) have already been validated and set by listInstanceTypesForFlexShape for flex shapes

	var ocpu, memoryInGbs float32
	var cpuBaseline ociv1beta1.BaselineOcpuUtilization
	shapeAvailable := true
	if shape.BillingType == ocicore.ShapeBillingTypeAlwaysFree {
		shapeAvailable = false // make free billing type not available
	}
	if it.Ocpu != nil {
		ocpu = *it.Ocpu
	} else {
		ocpu = *shape.Ocpus
	}
	if it.MemoryInGbs != nil {
		memoryInGbs = *it.MemoryInGbs
	} else {
		memoryInGbs = *shape.MemoryInGBs
	}
	if it.BaselineOcpuUtilization != nil {
		cpuBaseline = *it.BaselineOcpuUtilization
	} else {
		cpuBaseline = ociv1beta1.BASELINE_1_1
	}

	// Set capacity & overhead
	setCapacity(it, shape, ocpu, memoryInGbs, nodeClass, p.ipFamilies)
	setOverhead(it, shape, ocpu, memoryInGbs, nodeClass)

	basePrice, priceAvailable := p.calculatePrices(shape, ocpu, memoryInGbs, cpuBaseline)

	vnicAvailable := true
	secondVnicsNum := -1
	if shape.MaxVnicAttachments != nil {
		secondVnicsNum = p.getMaxVnicAttachmentsForShape(ctx, shape, ocpu) - 1
	}

	if nodeClass.Spec.NetworkConfig.SecondaryVnicConfigs != nil &&
		secondVnicsNum < len(nodeClass.Spec.NetworkConfig.SecondaryVnicConfigs) {
		vnicAvailable = false
		log.FromContext(ctx).V(1).Info("Fail secondVnicsNum check", "shape", *shape.Shape,
			"secondVnicsNum", secondVnicsNum)
	}

	err := p.setOfferings(ctx, it, nodeClass, shapeAndAd,
		shapeAvailable && priceAvailable && vnicAvailable,
		basePrice, taints)

	if err != nil {
		log.FromContext(ctx).Error(err, "failed to create offering")
	}

	return err
}

/*
If shape is a Flexible shape and selected Ocpu number larger than Min Ocpu options
Then work out the maxSecondaryVnicAttachements from MaxVnicAttachmentOptions

	minVnic + (ocpu - minOcpu) * defaultVnicPerOcpu

Otherwise return shape.maxVnicAttahments
*/
func (p *DefaultProvider) getMaxVnicAttachmentsForShape(ctx context.Context, shape *ocicore.Shape, ocpu float32) int {

	if shape.IsFlexible != nil && *shape.IsFlexible &&
		isValidMaxVnicAttachmentOptions(shape.MaxVnicAttachmentOptions) &&
		isValidOcpuOptions(shape.OcpuOptions) {
		minOcpu := *shape.OcpuOptions.Min
		if ocpu > minOcpu {
			vnicMin := float32(*shape.MaxVnicAttachmentOptions.Min)
			vnicMax := *shape.MaxVnicAttachmentOptions.Max
			vnicPerOcpu := float32(1)
			if shape.MaxVnicAttachmentOptions.DefaultPerOcpu != nil {
				vnicPerOcpu = *shape.MaxVnicAttachmentOptions.DefaultPerOcpu
			}

			maxPossibleVnics := vnicMin + (ocpu-minOcpu)*vnicPerOcpu
			if maxPossibleVnics > vnicMax {
				maxPossibleVnics = vnicMax
			}

			log.FromContext(ctx).V(1).Info("MaxVnicAttachmentDetails",
				"vnicMin", vnicMin, "vnicMax", vnicMax, "vnicPerOcpu",
				vnicPerOcpu, "maxPossibleVnics", maxPossibleVnics)
			return int(maxPossibleVnics)
		} else {
			return *shape.MaxVnicAttachmentOptions.Min // when ocpu<=minOcpu using *shape.MaxVnicAttachmentOptions.Min
		}
	}

	return *shape.MaxVnicAttachments
}

func isValidMaxVnicAttachmentOptions(maxVnicAttachmentOptions *ocicore.ShapeMaxVnicAttachmentOptions) bool {
	return maxVnicAttachmentOptions != nil &&
		maxVnicAttachmentOptions.Min != nil &&
		maxVnicAttachmentOptions.Max != nil
}

func isValidOcpuOptions(ocpuOptions *ocicore.ShapeOcpuOptions) bool {
	return ocpuOptions != nil && ocpuOptions.Min != nil
}

func makeOnDemandOffering(shapeName string, ads []string, price float64, available bool,
	computeClusterRestrictFunc func(string, string) bool) []*cloudprovider.Offering {
	return lo.Map(ads, func(ad string, _ int) *cloudprovider.Offering {
		return makeOffering(ad, price, corev1.CapacityTypeOnDemand,
			available && computeClusterRestrictFunc(shapeName, ad))
	})
}

func makeSpotOffering(ads []string, base float64) []*cloudprovider.Offering {
	return lo.Map(ads, func(ad string, _ int) *cloudprovider.Offering {
		return makeOffering(ad, base*0.5, corev1.CapacityTypeSpot, true)
	})
}

// makeReservedOffering can produce many offering, based on combination of capReservation, ad or individual fd or mix.
func makeReservedOffering(price float64,
	capResAdMap map[capacityreservation.CapacityReserveIdAndAd]map[string]capacityreservation.ShapeAvailability,
) []*cloudprovider.Offering {
	offerings := make([]*cloudprovider.Offering, 0)

	// for each capRes, generate capResId, ad w/ fd, a
	for capResIdAndAd, adAvailMap := range capResAdMap {
		for _, shapeAvail := range adAvailMap {
			avail := int(shapeAvail.Total - shapeAvail.Used)
			if avail < 0 {
				avail = 0
			}

			offering := makeOffering(capResIdAndAd.Ad, price*0.85, corev1.CapacityTypeReserved, avail != 0)
			offering.ReservationCapacity = avail

			// extend scheduling requirement
			offering.Requirements.Add(reservedOfferingSchedulingRequirement(
				capacityreservation.OcidToLabelValue(capResIdAndAd.Ocid), shapeAvail)...)
			offerings = append(offerings, offering)
		}
	}

	return offerings
}

func reservedOfferingSchedulingRequirement(capResId string,
	info capacityreservation.ShapeAvailability) []*scheduling.Requirement {
	reqs := []*scheduling.Requirement{
		scheduling.NewRequirement(ociv1beta1.ReservationIDLabel, v1.NodeSelectorOpIn, capResId),
	}

	if info.FaultDomain != nil {
		reqs = append(reqs, scheduling.NewRequirement(ociv1beta1.OciFaultDomain, v1.NodeSelectorOpIn, *info.FaultDomain))
	}

	return reqs
}

func makeOffering(ad string, price float64, capType string, available bool) *cloudprovider.Offering {
	return &cloudprovider.Offering{
		Requirements: makeRequirement(ad, capType),
		Price:        price,
		Available:    available,
	}
}

func makeRequirement(ad string, capType string) scheduling.Requirements {
	requirements := scheduling.NewRequirements(
		scheduling.NewRequirement(corev1.CapacityTypeLabelKey, v1.NodeSelectorOpIn, capType),
		// label value does not allow colon, replace it with dash
		scheduling.NewRequirement(v1.LabelTopologyZone, v1.NodeSelectorOpIn, utils.AdToZoneLabelValue(ad)),
	)

	// explicitly mark offering not for capacity reservation
	if capType != corev1.CapacityTypeReserved {
		requirements.Add(scheduling.NewRequirement(cloudprovider.ReservationIDLabel, v1.NodeSelectorOpDoesNotExist))
	}

	return requirements
}

func setCapacity(it *OciInstanceType, shape *ocicore.Shape, ocpu float32, gbs float32,
	class *ociv1beta1.OCINodeClass, ipFamilies []network.IpFamily) {
	vcpu := vcpu(shape, ocpu)

	res := v1.ResourceList{
		v1.ResourceCPU:    *resource.NewMilliQuantity(int64(vcpu*1000), resource.DecimalSI),
		v1.ResourceMemory: *resource.NewQuantity(int64(gbs*1024*1024*1024), resource.BinarySI),
		v1.ResourcePods:   *pods(int64(vcpu), class, ipFamilies),
	}

	// TODO: what if not defined boot volume size, how much should we report?
	if class.Spec.VolumeConfig.BootVolumeConfig.SizeInGBs != nil {
		res[v1.ResourceEphemeralStorage] = *resource.NewScaledQuantity(
			*class.Spec.VolumeConfig.BootVolumeConfig.SizeInGBs, resource.Giga)
	}

	if gpuCount := gpuCount(shape); gpuCount > 0 {
		res[gpuResourceName(shape)] = *resource.NewQuantity(int64(gpuCount), resource.DecimalSI)
	}

	it.InstanceType.Capacity = res
	// TBD: add Nvme, vnic attachments
}

func gpuResourceName(shape *ocicore.Shape) v1.ResourceName {
	if shape != nil && IsAmdGpuShape(*shape) {
		return AmdGpuResourceName
	}
	return NvidiaGpuResourceName
}

func gpuCount(shape *ocicore.Shape) int {
	if shape == nil || shape.Gpus == nil {
		return 0
	}
	return *shape.Gpus
}

func vcpu(shape *ocicore.Shape, ocpu float32) float32 {
	vcpuRatio := float32(2.0)
	if IsArmShape(*shape) {
		vcpuRatio = float32(1.0)
	}
	return vcpuRatio * ocpu
}

func setOverhead(it *OciInstanceType, shape *ocicore.Shape, ocpu float32, gbs float32, class *ociv1beta1.OCINodeClass) {
	it.InstanceType.Overhead = &cloudprovider.InstanceTypeOverhead{
		KubeReserved:      kubeReservedResources(shape, vcpu(shape, ocpu), gbs, class),
		SystemReserved:    systemReservedResources(class),
		EvictionThreshold: evictionThreshold(gbs, class),
	}
}

func evictionThreshold(gbs float32, class *ociv1beta1.OCINodeClass) v1.ResourceList {
	// we don't have opinion on eviction threshold, but if customer set it, let us respect
	if class.Spec.KubeletConfig == nil {
		return nil
	}

	memory := resource.NewQuantity(int64(gbs*1024*1024), resource.BinarySI)

	diskSizeAvailable := false
	var storage *resource.Quantity
	if class.Spec.VolumeConfig.BootVolumeConfig.SizeInGBs != nil {
		diskSizeAvailable = true
		storage = resource.NewScaledQuantity(*class.Spec.VolumeConfig.BootVolumeConfig.SizeInGBs, resource.Giga)
	}

	hard := v1.ResourceList{}
	if class.Spec.KubeletConfig.EvictionHard != nil {
		if v, ok := class.Spec.KubeletConfig.EvictionHard[MemoryAvailable]; ok {
			hard[v1.ResourceMemory] = parseEvictionSignal(memory, v)
		}

		if v, ok := class.Spec.KubeletConfig.EvictionSoft[NodeFSAvailable]; diskSizeAvailable && ok {
			hard[v1.ResourceEphemeralStorage] = parseEvictionSignal(storage, v)
		}
	}

	soft := v1.ResourceList{}
	if class.Spec.KubeletConfig.EvictionHard != nil {
		if v, ok := class.Spec.KubeletConfig.EvictionHard[MemoryAvailable]; ok {
			soft[v1.ResourceMemory] = parseEvictionSignal(memory, v)
		}

		if v, ok := class.Spec.KubeletConfig.EvictionSoft[NodeFSAvailable]; diskSizeAvailable && ok {
			soft[v1.ResourceEphemeralStorage] = parseEvictionSignal(storage, v)
		}
	}

	return resources.MaxResources(hard, soft)
}

// parseEvictionSignal computes the resource quantity value for an eviction signal value, computed off the  base
// capacity value if the signal value is a percentage or as a resource quantity if the signal value isn't a percentage
func parseEvictionSignal(capacity *resource.Quantity, signalValue string) resource.Quantity {
	if strings.HasSuffix(signalValue, "%") {
		p := mustParsePercentage(signalValue)

		// Calculation is node.capacity * signalValue if percentage
		// From https://kubernetes.io/docs/concepts/scheduling-eviction/node-pressure-eviction/#eviction-signals
		return resource.MustParse(fmt.Sprint(math.Ceil(capacity.AsApproximateFloat64() / 100 * p)))
	}
	return resource.MustParse(signalValue)
}

func mustParsePercentage(v string) float64 {
	p, err := strconv.ParseFloat(strings.Trim(v, "%"), 64)
	if err != nil {
		panic(fmt.Sprintf("expected percentage value to be a float but got %s, %v", v, err))
	}
	// Setting percentage value to 100% is considered disabling the threshold according to
	// https://kubernetes.io/docs/reference/config-api/kubelet-config.v1beta1/
	if p == 100 {
		p = 0
	}
	return p
}

// nolint:lll
func systemReservedResources(class *ociv1beta1.OCINodeClass) v1.ResourceList {
	/*
		follow the same logic here - https://bitbucket.oci.oraclecorp.com/projects/OKN/repos/node-ansible-bundle/browse/v4/getcpuMemory.py#18,49
		customer can override using kubelet configuration in node class spec
	*/
	res := v1.ResourceList{
		v1.ResourceCPU:    *resource.NewMilliQuantity(int64(100), resource.DecimalSI),
		v1.ResourceMemory: *resource.NewQuantity(int64(100*1024*1024), resource.BinarySI),
	}

	var override map[string]string
	if class.Spec.KubeletConfig != nil {
		override = class.Spec.KubeletConfig.SystemReserved
	}

	return lo.Assign(res, lo.MapEntries(override, func(k string, v string) (v1.ResourceName, resource.Quantity) {
		return v1.ResourceName(k), resource.MustParse(v)
	}))
}

// nolint:lll
func kubeReservedResources(shape *ocicore.Shape, ocpu float32,
	gbs float32, class *ociv1beta1.OCINodeClass) v1.ResourceList {
	var cpuRes float32
	var memoryRes float32

	/*
		follow the same logic here - https://bitbucket.oci.oraclecorp.com/projects/OKN/repos/node-ansible-bundle/browse/v4/getcpuMemory.py#18,49
		customer can override using kubelet configuration in node class spec
	*/
	vcpuRatio := float32(2.0)
	if IsArmShape(*shape) {
		vcpuRatio = float32(1.0)
	}
	totalCpu := ocpu * vcpuRatio
	switch totalCpu {
	case 1:
		cpuRes = 60
	case 2:
		cpuRes = 72
	case 3:
		cpuRes = 80
	case 4:
		cpuRes = 85
	case 5:
		cpuRes = 90
	default:
		cpuRes = 90 + (totalCpu-5)*2.5
	}

	switch {
	case gbs <= 4:
		memoryRes = 0.25 * gbs
	case gbs <= 8:
		memoryRes = 0.20*(gbs-4) + 1
	case gbs <= 16:
		memoryRes = 0.10*(gbs-8) + 1.8
	case gbs <= 128:
		memoryRes = 0.06*(gbs-16) + 2.6
	default:
		memoryRes = 0.02*(gbs-128) + 9.32
	}

	res := v1.ResourceList{
		// round-up cpu, and show memory as Mi
		v1.ResourceCPU:    *resource.NewMilliQuantity(int64(math.Ceil(float64(cpuRes))), resource.DecimalSI),
		v1.ResourceMemory: *resource.NewQuantity(int64(math.Ceil(float64(memoryRes*1024))), resource.BinarySI),
	}

	var override map[string]string
	if class.Spec.KubeletConfig != nil {
		override = class.Spec.KubeletConfig.KubeReserved
	}

	return lo.Assign(res, lo.MapEntries(override, func(k string, v string) (v1.ResourceName, resource.Quantity) {
		return v1.ResourceName(k), resource.MustParse(v)
	}))
}

func pods(vcpu int64, class *ociv1beta1.OCINodeClass, ipFamilies []network.IpFamily) *resource.Quantity {
	kc := class.Spec.KubeletConfig

	secondaryVnics := make([]*ociv1beta1.SecondaryVnicConfig, 0)
	if class.Spec.NetworkConfig != nil {
		secondaryVnics = class.Spec.NetworkConfig.SecondaryVnicConfigs
	}

	// Get kubeletMaxPods, it is the min of 110, sum of npn secondary ipCounts if it is npn,
	// or maxPods if it is set
	count := int64(utils.GetKubeletMaxPods(kc, secondaryVnics,
		network.GetDefaultSecondaryVnicIPCount(ipFamilies)))
	if kc != nil && kc.PodsPerCore != nil && *kc.PodsPerCore != 0 {
		count = lo.Min([]int64{int64(*kc.PodsPerCore) * vcpu, count})
	}

	return resources.Quantity(fmt.Sprint(count))
}

func (p *DefaultProvider) reloadConfigFile(ctx context.Context) error {
	lg := log.FromContext(ctx)
	start := time.Now()
	lg.Info("reload price config start",
		"operation", "reload_shape_meta", "outcome", "start",
		"shape_meta_file", p.shapeMetaFile)

	configBytes, err := os.ReadFile(p.shapeMetaFile)
	if err != nil {
		lg.Error(err, "unable to read ociShapeMeta file",
			"operation", "reload_shape_meta", "outcome", "error",
			"duration_ms", time.Since(start).Milliseconds(),
			"shape_meta_file", p.shapeMetaFile)
		return err
	}

	var config OciShapeMeta
	err = json.Unmarshal(configBytes, &config)
	if err != nil {
		lg.Error(err, "unable to parse ociShapeMeta JSON",
			"operation", "reload_shape_meta", "outcome", "error",
			"duration_ms", time.Since(start).Milliseconds(),
			"shape_meta_file", p.shapeMetaFile)
		return err
	}

	p.lock.Lock()
	defer p.lock.Unlock()
	p.shapeToPrice = lo.SliceToMap(config.Prices, func(item ShapePriceInfo) (string, *ShapePriceInfo) {
		return strings.ToUpper(*item.ShapeName), &item
	})
	lo.ForEach(config.PreemptibleShapes, func(s string, _ int) {
		p.preemptibleShapes[strings.ToUpper(s)] = s
	})
	p.computeClusterShapes = lo.Map(config.ComputeClusterShapes, func(s string, _ int) string {
		return strings.ToUpper(s)
	})

	lg.Info("reload price config done",
		"operation", "reload_shape_meta", "outcome", "success",
		"price_entries", len(p.shapeToPrice),
		"preemptible_shapes", len(p.preemptibleShapes),
		"compute_cluster_shapes", len(p.computeClusterShapes),
		"duration_ms", time.Since(start).Milliseconds())
	return nil
}

func (p *DefaultProvider) calculatePrices(shape *ocicore.Shape, ocpu float32, gbs float32,
	baseline ociv1beta1.BaselineOcpuUtilization) (float64, bool) {
	// The caller must hold p.lock for reading.
	shapeUp := strings.ToUpper(*shape.Shape)
	si, ok := p.shapeToPrice[shapeUp]
	if !ok {
		for k, v := range p.shapeToPrice {
			if strings.HasPrefix(shapeUp, k) {
				si = v
				break
			}
		}
	}

	if si == nil {
		return 0, false
	}

	if gpuCount := gpuCount(shape); gpuCount > 0 {
		return si.OcpuUnitPrice * float64(gpuCount), true
	}

	cpuDiscount := float64(1)
	switch baseline {
	case ociv1beta1.BASELINE_1_2:
		cpuDiscount = 0.5
	case ociv1beta1.BASELINE_1_8:
		cpuDiscount = 0.125
	}

	localDisksTotalSizeInGbs := float32(0)
	if shape.LocalDisksTotalSizeInGBs != nil {
		localDisksTotalSizeInGbs = *shape.LocalDisksTotalSizeInGBs
	}

	return si.OcpuUnitPrice*float64(ocpu)*cpuDiscount + si.MemoryUnitPrice*float64(gbs) +
		si.DiskUnitPrice*float64(localDisksTotalSizeInGbs), true
}

func (p *DefaultProvider) isPreemptibleShape(shape string) bool {
	// The caller must hold p.lock for reading.
	shapeUp := strings.ToUpper(shape)
	if _, ok := p.preemptibleShapes[shapeUp]; ok {
		return true
	}

	for k := range p.preemptibleShapes {
		if strings.HasPrefix(shapeUp, k) {
			return true
		}
	}

	return false
}

// isComputeClusterSupportedShape checks if a shape is supported for compute clusters
func (p *DefaultProvider) isComputeClusterSupportedShape(shapeName string) bool {
	// The caller must hold p.lock for reading.
	// https://docs.oracle.com/en-us/iaas/Content/Compute/Tasks/compute-clusters.htm
	return lo.Contains(p.computeClusterShapes, strings.ToUpper(shapeName))
}

func (p *DefaultProvider) setOfferings(ctx context.Context, it *OciInstanceType, nodeClass *ociv1beta1.OCINodeClass,
	shapeAndAd *ShapeAndAd, available bool, basePrice float64, taints []v1.Taint) error {
	placementRestrictFunc, err := p.getPlacementRestrictFunc(ctx, nodeClass)
	if err != nil {
		return err
	}

	// always make on-demand offering regardless it is available or not, the idea is compute return the shape
	offerings := makeOnDemandOffering(*shapeAndAd.Shape.Shape, shapeAndAd.Ads, basePrice, available,
		placementRestrictFunc)

	isPreemptible := p.isPreemptibleShape(*shapeAndAd.Shape.Shape)

	var capResAdMap map[capacityreservation.CapacityReserveIdAndAd]map[string]capacityreservation.ShapeAvailability
	if len(nodeClass.Spec.CapacityReservationConfigs) > 0 {
		capResResults, inerr := p.capacityReservationProvider.ResolveCapacityReservations(ctx,
			nodeClass.Spec.CapacityReservationConfigs)
		if inerr != nil {
			log.FromContext(ctx).Error(inerr, "failed to resolve capacity reservation")
			return inerr
		}
		capResAdMap = capacityreservation.ResolveResultSlice(capResResults).
			AvailabilityForShape(it.Shape, it.Ocpu, it.MemoryInGbs, it.SupportShapeConfig)
	}

	hasPreemptibleTaints := checkTaintExists(taints, PreemptibleTaintKey)
	// Preemptible capacity does not support burstable instances, nor capacity reservation.
	if available && isPreemptible && !IsBurstableShape(it) && len(capResAdMap) == 0 {
		if hasPreemptibleTaints {
			offerings = append(offerings, makeSpotOffering(shapeAndAd.Ads, basePrice*0.5)...)
		} else {
			log.FromContext(ctx).V(1).Info(fmt.Sprintf("Missing '%s' taints for preemptible shape '%s'",
				PreemptibleTaintKey, *shapeAndAd.Shape.Shape))
		}
	}

	if available && capResAdMap != nil {
		// be noticed capacity reservation does not support burstable && preemptible
		offerings = append(offerings, makeReservedOffering(basePrice*0.85, capResAdMap)...)
	}

	it.Offerings = append(it.Offerings, offerings...)

	if len(it.Offerings) > 0 {
		for _, offering := range it.Offerings {
			metrics.InstanceTypeOfferingAvailable.Set(float64(lo.Ternary(available, 1, 0)), map[string]string{
				metrics.InstanceTypeLabel: it.Name,
				metrics.CapacityTypeLabel: offering.CapacityType(),
				metrics.ZoneLabel:         offering.Zone(),
			})

			metrics.InstanceTypeOfferingPriceEstimate.Set(offering.Price, map[string]string{
				metrics.InstanceTypeLabel: it.Name,
				metrics.CapacityTypeLabel: offering.CapacityType(),
				metrics.ZoneLabel:         offering.Zone(),
			})
		}
	}

	return nil
}

func checkTaintExists(taints []v1.Taint, taintKey string) bool {
	hasTaints := false
	if len(taints) > 0 {
		_, hasTaints = lo.Find(taints, func(item v1.Taint) bool {
			return item.Key == taintKey && item.Effect == v1.TaintEffectNoSchedule
		})
	}
	return hasTaints
}

func CheckTaintsAndPrintWarnings(ctx context.Context, nodeClaim *corev1.NodeClaim) {
	lg := log.FromContext(ctx)

	if !checkTaintExists(nodeClaim.Spec.Taints, NvidiaGpuTaintKey) &&
		!checkTaintExists(nodeClaim.Spec.Taints, AmdGpuTaintKey) {
		lg.Info(fmt.Sprintf("NodeCalim doesn't have taints '%s' or '%s' will not offer GPU shapes",
			NvidiaGpuTaintKey, AmdGpuTaintKey))
	}

	if !checkTaintExists(nodeClaim.Spec.Taints, PreemptibleTaintKey) {
		lg.Info(fmt.Sprintf("NodeCalim doesn't have taint '%s' will not offer preemptible shape",
			PreemptibleTaintKey))
	}
}

func (p *DefaultProvider) refreshClusterVersion(ctx context.Context) error {
	lg := log.FromContext(ctx)
	start := time.Now()
	lg.Info("refresh cluster version start",
		"operation", "refresh_cluster_version", "outcome", "start")
	k8sVersion, err := utils.GetClusterVersion(p.kubernetesInterface)
	if err != nil {
		lg.Error(err, "refresh cluster version failed",
			"operation", "refresh_cluster_version", "outcome", "error",
			"duration_ms", time.Since(start).Milliseconds())
		return err
	}
	p.k8sVersion = k8sVersion
	lg.Info("refresh cluster version done",
		"operation", "refresh_cluster_version", "outcome", "success",
		"version", k8sVersion.String(),
		"duration_ms", time.Since(start).Milliseconds())
	return nil
}

/*
ListInstanceTypes return all available instance types.
*/
func (p *DefaultProvider) ListInstanceTypes(ctx context.Context,
	nodeClass *ociv1beta1.OCINodeClass, taints []v1.Taint) ([]*OciInstanceType, error) {
	p.lock.RLock()
	defer p.lock.RUnlock()
	// Helpers in this call tree read provider state under this lock and must not reacquire it.

	// for each shape, we return offering
	instanceTypes := make([]*OciInstanceType, 0)
	for _, shapeAd := range p.shapeAdMap {
		if IsGpuShape(*shapeAd.Shape) &&
			!checkTaintExists(taints, NvidiaGpuTaintKey) &&
			!checkTaintExists(taints, AmdGpuTaintKey) {
			// If it is GPU shape and missing nvidia.com/gpu NoSchedule taint will stop offer this shape
			log.FromContext(ctx).V(1).
				Info(fmt.Sprintf("Missing '%s' or '%s' taints for gpu shape '%s'",
					NvidiaGpuTaintKey, AmdGpuTaintKey, *shapeAd.Shape.Shape))
			continue
		}

		types, err := p.makeInstanceTypes(ctx, shapeAd, nodeClass, taints)
		if err != nil {
			return nil, err
		}

		instanceTypes = append(instanceTypes, types...)
	}

	if len(instanceTypes) == 0 {
		return nil, fmt.Errorf("no suitable instance types found for node class: %s", nodeClass.Name)
	}

	return instanceTypes, nil
}

func (p *DefaultProvider) makeInstanceTypes(ctx context.Context,
	sa *ShapeAndAd, nodeClass *ociv1beta1.OCINodeClass, taints []v1.Taint) ([]*OciInstanceType, error) {

	var candidateTypes []*OciInstanceType
	if sa.Shape.IsFlexible != nil && *sa.Shape.IsFlexible {
		candidateTypes = p.listInstanceTypesForFlexShape(ctx, sa.Shape, nodeClass)
	} else {
		it := &OciInstanceType{
			InstanceType: cloudprovider.InstanceType{
				Name: *sa.Shape.Shape,
			},
			Shape: *sa.Shape.Shape,
		}
		candidateTypes = []*OciInstanceType{it}
	}

	ret := make([]*OciInstanceType, 0)
	for _, it := range candidateTypes {
		// decorate offering, capacity, overhead
		err := p.decorateInstanceType(ctx, it, nodeClass, sa, taints)
		if err != nil {
			return nil, err
		}

		p.finalizeRequirements(it, sa)
		ret = append(ret, it)
	}
	return ret, nil
}

func (p *DefaultProvider) finalizeRequirements(instanceType *OciInstanceType,
	sa *ShapeAndAd) {
	requirements := []*scheduling.Requirement{
		// Well Known Upstream
		scheduling.NewRequirement(v1.LabelInstanceTypeStable, v1.NodeSelectorOpIn, instanceType.Name),
		scheduling.NewRequirement(v1.LabelArchStable, v1.NodeSelectorOpIn, Architecture(*sa.Shape)),
		scheduling.NewRequirement(v1.LabelOSStable, v1.NodeSelectorOpIn, "linux"),
		scheduling.NewRequirement(v1.LabelTopologyZone, v1.NodeSelectorOpIn,
			lo.Map(sa.Ads, func(ad string, _ int) string {
				return strings.Split(ad, ":")[1]
			})...),
		scheduling.NewRequirement(v1.LabelTopologyRegion, v1.NodeSelectorOpIn, p.region),
		scheduling.NewRequirement(v1.LabelWindowsBuild, v1.NodeSelectorOpDoesNotExist),
		scheduling.NewRequirement(corev1.CapacityTypeLabelKey, v1.NodeSelectorOpIn,
			lo.Map(instanceType.Offerings.Available(), func(o *cloudprovider.Offering, _ int) string {
				return o.Requirements.Get(corev1.CapacityTypeLabelKey).Any()
			})...),
	}

	// add capacity type
	reservedOfferings := lo.Filter(instanceType.Offerings.Available(), func(item *cloudprovider.Offering, _ int) bool {
		return item.Requirements.Get(corev1.CapacityTypeLabelKey).Any() == corev1.CapacityTypeReserved
	})

	// follow other cloud provider's suit, always make capacity reservation explicit.
	if len(reservedOfferings) == 0 {
		requirements = append(requirements,
			scheduling.NewRequirement(cloudprovider.ReservationIDLabel, v1.NodeSelectorOpDoesNotExist))
	} else {
		requirements = append(requirements,
			scheduling.NewRequirement(cloudprovider.ReservationIDLabel, v1.NodeSelectorOpIn,
				lo.Map(reservedOfferings, func(item *cloudprovider.Offering, _ int) string {
					return item.ReservationID()
				})...))
	}

	/*
	   			add oci gpu, bm, denseio requirement. node pools that need use these special shapes
	          	need explicitly define them.
	*/

	if IsGpuShape(*sa.Shape) {
		requirements = append(requirements,
			scheduling.NewRequirement(ociv1beta1.OciGpuShape, v1.NodeSelectorOpIn, trueStr))
	} else {
		requirements = append(requirements,
			scheduling.NewRequirement(ociv1beta1.OciGpuShape, v1.NodeSelectorOpDoesNotExist))
	}

	if IsBmShape(*sa.Shape.Shape) {
		requirements = append(requirements,
			scheduling.NewRequirement(ociv1beta1.OciBmShape, v1.NodeSelectorOpIn, trueStr))
	} else {
		requirements = append(requirements,
			scheduling.NewRequirement(ociv1beta1.OciBmShape, v1.NodeSelectorOpDoesNotExist))
	}

	if IsDenseIoShape(*sa.Shape) {
		requirements = append(requirements,
			scheduling.NewRequirement(ociv1beta1.OciDenseIoShape, v1.NodeSelectorOpIn, trueStr))
	} else {
		requirements = append(requirements,
			scheduling.NewRequirement(ociv1beta1.OciDenseIoShape, v1.NodeSelectorOpDoesNotExist))
	}

	if IsFlexShape(*sa.Shape) {
		requirements = append(requirements,
			scheduling.NewRequirement(ociv1beta1.OciFlexShape, v1.NodeSelectorOpIn, trueStr))
	} else {
		requirements = append(requirements,
			scheduling.NewRequirement(ociv1beta1.OciFlexShape, v1.NodeSelectorOpDoesNotExist))
	}

	requirements = append(requirements, scheduling.NewRequirement(ociv1beta1.OciInstanceShape, v1.NodeSelectorOpIn,
		instanceType.Shape))

	instanceType.Requirements = scheduling.NewRequirements(requirements...)
}

func (p *DefaultProvider) refreshShapes(ctx context.Context) error {
	lg := log.FromContext(ctx)
	start := time.Now()
	lg.Info("list shapes start",
		"operation", "list_shapes", "outcome", "start",
		"compartment_id", p.clusterCompartmentId)

	shapeByAd := make(map[string][]ocicore.Shape)
	var nextPage *string
	for ad := range p.identityProvider.GetAdMap() {
		for {
			shapes, err := p.computeClient.ListShapes(ctx, ocicore.ListShapesRequest{
				CompartmentId:      &p.clusterCompartmentId, // compute doesn't use compartment
				AvailabilityDomain: &ad,
				Page:               nextPage,
			})

			if err != nil {
				lg.Error(err, "list shapes failed",
					"operation", "refresh_shapes", "outcome", "error",
					"duration_ms", time.Since(start).Milliseconds())
				return err
			}

			s, ok := shapeByAd[ad]
			if !ok {
				shapeByAd[ad] = shapes.Items
			} else {
				shapeByAd[ad] = append(s, shapes.Items...)
			}

			if shapes.OpcNextPage != nil {
				nextPage = shapes.OpcNextPage
			} else {
				break
			}
		}
	}

	shapeAdMap := make(map[string]*ShapeAndAd)
	for k, v := range shapeByAd {
		log.FromContext(ctx).Info("available shapes of ad", "shapes", lo.Map(v,
			func(item ocicore.Shape, _ int) string {
				return *item.Shape
			}), "ad", k)

		for _, shape := range v {
			s, ok := shapeAdMap[*shape.Shape]
			if !ok {
				ads := []string{k}
				shapeAdMap[*shape.Shape] = &ShapeAndAd{
					Shape: &shape, Ads: ads}
			} else {
				s.Ads = append(s.Ads, k)
			}
		}
	}

	p.lock.Lock()
	defer p.lock.Unlock()
	p.shapeAdMap = shapeAdMap

	lg.Info("list shapes done",
		"operation", "list_shapes", "outcome", "success",
		"shape_count", len(p.shapeAdMap),
		"duration_ms", time.Since(start).Milliseconds())
	return nil
}

func (p *DefaultProvider) getPlacementRestrictFunc(ctx context.Context,
	nodeClass *ociv1beta1.OCINodeClass) (func(shapeName, ad string) bool, error) {
	if nodeClass.Spec.ClusterPlacementGroupConfigs == nil && nodeClass.Spec.ComputeClusterConfig == nil {
		return func(shapeName, ad string) bool {
			return true
		}, nil
	}
	var err error
	var computeClusterResult *computecluster.ResolveResult
	var cpgAds map[string]bool
	if nodeClass.Spec.ComputeClusterConfig != nil {
		computeClusterResult, err = p.computeClusterProvider.
			ResolveComputeCluster(ctx, nodeClass.Spec.ComputeClusterConfig)
		if err != nil {
			log.FromContext(ctx).Error(err, "failed to resolve compute cluster")
			// return without populate offering
			return nil, err
		}
	} else if len(nodeClass.Spec.ClusterPlacementGroupConfigs) > 0 {
		var clusterPlacementGroupResults []clusterplacementgroup.ResolveResult
		clusterPlacementGroupResults, err = p.clusterPlacementGroupProvider.ResolveClusterPlacementGroups(ctx,
			nodeClass.Spec.ClusterPlacementGroupConfigs)
		if err != nil {
			log.FromContext(ctx).Error(err, "failed to resolve cluster placement groups")
			// return without populate offering
			return nil, err
		}
		cpgAds = lo.SliceToMap(clusterPlacementGroupResults,
			func(c clusterplacementgroup.ResolveResult) (string, bool) {
				return c.Ad, true
			})
	}
	placementRestrictFunc := func(shapeName, ad string) bool {
		if computeClusterResult != nil {
			// computeCluster only supports a limited set of shapes and ads.
			return p.isComputeClusterSupportedShape(shapeName) && ad == computeClusterResult.Ad
		}
		if len(cpgAds) > 0 {
			return cpgAds[ad]
		}
		return true
	}
	return placementRestrictFunc, nil
}
