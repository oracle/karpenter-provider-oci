/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package v1beta1

import (
	"time"

	"github.com/awslabs/operatorpkg/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// +kubebuilder:rbac:groups=oci.oraclecould.com,resources=ocinodeclasses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=oci.oraclecould.com,resources=ocinodeclasses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=oci.oraclecould.com,resources=ocinodeclasses/finalizers,verbs=update

// OCINodeClassSpec defines the desired state of OCINodeClass.
// +kubebuilder:validation:XValidation:rule="((has(self.capacityReservationConfigs) && self.capacityReservationConfigs.size() > 0 ? 1 : 0) + (has(self.clusterPlacementGroupConfigs) && self.clusterPlacementGroupConfigs.size() > 0 ? 1 : 0) + (has(self.computeClusterConfig) && self.computeClusterConfig != null ? 1 : 0)) <= 1",message="At most one of capacityReservationConfigs, clusterPlacementGroupConfigs, or computeClusterConfig may be set"
// +kubebuilder:validation:XValidation:rule="!(has(oldSelf.computeClusterConfig) && oldSelf.computeClusterConfig != null) || (has(self.computeClusterConfig) && self.computeClusterConfig != null && self.computeClusterConfig == oldSelf.computeClusterConfig)",message="computeClusterConfig is immutable once set"
type OCINodeClassSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// ShapeConfigs is additional shape config applies to flexible and burstable config
	// without specifying it, flexible and burstable shapes are excluded from scheduling consideration.
	// Different from other cloud providers, our flexible/burstable options are not offered as
	// individual shapes, thus we cannot expose one instance type with dynamic pricing and
	// allocatable resources.
	// +optional
	ShapeConfigs []*ShapeConfig `json:"shapeConfigs,omitempty"`

	// NodeCompartmentId is optional to place instance in a different compartment from the cluster
	// +optional
	NodeCompartmentId *string `json:"nodeCompartmentId,omitempty"`

	// VolumeConfig contains required configuration for the boot volume (and optional additional volumes).
	// +required
	VolumeConfig *VolumeConfig `json:"volumeConfig"`

	// NetworkConfig defines vnic subnet and optional network security groups for compute instance
	// +required
	NetworkConfig *NetworkConfig `json:"networkConfig"`

	// CapacityReservationConfigs contains an array of capacity reservations
	// +optional
	CapacityReservationConfigs []*CapacityReservationConfig `json:"capacityReservationConfigs,omitempty"`

	// ClusterPlacementGroupConfigs contains an array of cluster placement group.
	// +optional
	ClusterPlacementGroupConfigs []*ClusterPlacementGroupConfig `json:"clusterPlacementGroupConfigs,omitempty"`

	// ComputeClusterConfig refers to a compute cluster. It is fully immutable once set and cannot be
	// modified or deleted after creation, as changing the compute cluster has severe consequences.
	// +optional
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="computeClusterConfig is immutable once set"
	ComputeClusterConfig *ComputeClusterConfig `json:"computeClusterConfig,omitempty"`

	// Metadata is user_data passed into the compute instance.
	// +optional
	Metadata map[string]string `json:"metadata,omitempty"`

	// FreeformTags is customer-owned key/value labels passed into the compute instance.
	// +optional
	FreeformTags map[string]string `json:"freeformTags,omitempty"`

	// DefinedTags is customer-owned namespace key/value labels passed into the compute instance.
	DefinedTags map[string]map[string]string `json:"definedTags,omitempty"`

	// KubeletConfig gives customers finer control over parameters passed to the kubelet process.
	// +optional
	KubeletConfig *KubeletConfiguration `json:"kubeletConfig,omitempty"`

	// PreBootstrapInitScript is an optional base64 encoded script to run before OKE bootstrapping
	// +optional
	PreBootstrapInitScript *string `json:"preBootstrapInitScript,omitempty"`

	// PostBootstrapInitScript is an optional base64 encoded script to run after OKE bootstrapping
	// +optional
	PostBootstrapInitScript *string `json:"postBootstrapInitScript,omitempty"`

	// SshAuthorizedKeys is an array of authorized SSH public keys passed into the compute instance.
	// +optional
	SshAuthorizedKeys []string `json:"sshAuthorizedKeys,omitempty"`

	// LaunchOptions gives advanced control of volume, network, firmware and etc. of compute instance
	// +optional
	LaunchOptions *LaunchOptions `json:"launchOptions,omitempty"`

	// AgentList is a list of Oracle Cloud Agent plugins to enable on the launched instance.
	// Each entry must be the exact plugin name as listed by the OCI ListInstanceagentAvailablePlugins
	// API (for example, "Bastion", "Block Volume Management", "Compute Instance Monitoring",
	// "OS Management Service Agent"). Each listed plugin is set to ENABLED at launch time;
	// any plugin not listed is left at its image default. Unknown plugin names are accepted by
	// this provider but ignored by the OCI control plane.
	// +optional
	AgentList []string `json:"agentList,omitempty"`
}

type VolumeType string

const (
	VolumeTypeIscsi VolumeType = "ISCSI"
	VolumeTypeScsi  VolumeType = "SCSI"
	VolumeTypeIde   VolumeType = "IDE"
	VolumeTypeVfio  VolumeType = "VFIO"
	Paravirtualized VolumeType = "PARAVIRTUALIZED"
)

type Firmware string

const (
	FirmwareBios   Firmware = "BIOS"
	FirmwareUefi64 Firmware = "UEFI_64"
)

type NetworkType string

const (
	NetworkTypeVfio            NetworkType = "VFIO"
	NetworkTypeE1000           NetworkType = "E1000"
	NetworkTypeParavirtualized NetworkType = "PARAVIRTUALIZED"
)

// LaunchOptions gives advanced control of volume, network, firmware and etc. of compute instance
type LaunchOptions struct {
	// BootVolumeType defines type of boot volume, accepts any of ISCSI|SCSI|IDE|VFIO|PARAVIRTUALIZED
	// +optional
	BootVolumeType *VolumeType `json:"bootVolumeType,omitempty"`
	// RemoteDataVolumeType defines type of remote data volume, accepts any of ISCSI|SCSI|IDE|VFIO|PARAVIRTUALIZED
	// +optional
	RemoteDataVolumeType *VolumeType `json:"remoteDataVolumeType,omitempty"`
	// Firmware defines kind of firmware, accepts any of BIOS|UEFI_64
	// +optional
	Firmware *Firmware `json:"firmware,omitempty"`
	// NetworkType defines emulation type of physical network interface card, accepts any of VFIO|E1000|PARAVIRTUALIZED
	// +optional
	NetworkType *NetworkType `json:"networkType,omitempty"`
	// Whether to enable consistent volume naming feature. Defaults to false.
	// +optional
	ConsistentVolumeNamingEnabled *bool `json:"consistentVolumeNamingEnabled"`
}

// VolumeConfig contains required configuration for boot volume of a worker node, and optional configurations for
// additional volumes to be attached during worker node instance launch
type VolumeConfig struct {
	// BootVolumeConfig configures the boot volume used for the worker node instance.
	// +required
	BootVolumeConfig *BootVolumeConfig `json:"bootVolumeConfig"`

	// AdditionalVolumeConfigs is a list of additional volume to be attached during worker node instance launch
	// +optional
	// TODO: additional volume config support will be provided later
	// AdditionalVolumeConfigs []*LaunchAttachVolumeConfig `json:"additionalVolumeConfigs,omitempty"`
}

type IscsiEncryptInTransitType string

const (
	IscsiEncryptInTransitTypeNone         IscsiEncryptInTransitType = "NONE"
	IscsiEncryptInTransitTypeBmEncryption IscsiEncryptInTransitType = "BM_ENCRYPTION_IN_TRANSIT"

	EncryptionInTransitTypeBmEncryptionInTransit
)

// LaunchAttachVolumeConfig defines a volume to be attached along with worker node instance launch.
// +kubebuilder:validation:XValidation:message="must specify exactly one of ['iscsiVolumeConfig', 'paravirtualizedVolumeConfig']",rule="has(self.iscsiVolumeConfig) && !has(self.paravirtualizedVolumeConfig) || !has(self.iscsiVolumeConfig) && has(self.paravirtualizedVolumeConfig)"
type LaunchAttachVolumeConfig struct {
	// IscsiVolumeConfig configures attaching an iSCSI volume at instance launch.
	// +optional
	IscsiVolumeConfig *LaunchAttachIscsiVolumeConfig `json:"iscsiVolumeConfig,omitempty"`

	// ParavirtualizedVolumeConfig configures attaching a paravirtualized volume at instance launch.
	// +optional
	ParavirtualizedVolumeConfig *LaunchAttachParavirtualizedVolumeConfig `json:"paravirtualizedVolumeConfig,omitempty"`
}

// LaunchAttachIscsiVolumeConfig defines details of attaching an iscsi volume along with worker node instance launch
type LaunchAttachIscsiVolumeConfig struct {
	// AttachVolumeDetails selects a volume by ID, filter, or createVolumeDetails.
	AttachVolumeDetails `json:",inline"`
	// Whether to enable Oracle Cloud Agent to perform the iSCSI login and logout commands after the volume attach or detach operations for non multipath-enabled iSCSI attachments.
	// +optional
	AgentAutoIscsiLoginEnabled *bool `json:"agentAutoIscsiLoginEnabled,omitempty"`

	// EncryptionInTransitType configure encryption in transition type, default is NONE
	// +optional
	EncryptionInTransitType *IscsiEncryptInTransitType `json:"encryptionInTransitType,omitempty"`
}

// LaunchAttachParavirtualizedVolumeConfig defines details of attaching a paravirtualized
// volume along with worker node instance launch
type LaunchAttachParavirtualizedVolumeConfig struct {
	// AttachVolumeDetails selects a volume by ID, filter, or createVolumeDetails.
	AttachVolumeDetails `json:",inline"`

	// PvEncryptionInTransit configures the "isPvEncryptionInTransitEnabled" bool flag
	// +optional
	PvEncryptionInTransit *bool `json:"pvEncryptionInTransit,omitempty"`
}

// +kubebuilder:validation:XValidation:message="must specify exactly one of ['volumeId', 'volumeFilter', 'createVolumeDetails']",rule="([self.volumeId, self.volumeFilter, self.createVolumeDetails].filter(x, x != null).size() == 1)"
type AttachVolumeDetails struct {
	// Whether the attachment was created in read-only mode.
	// +optional
	IsReadonly bool `json:"isReadonly,omitempty"`

	// Whether the attachment should be created in shareable mode.
	// +optional
	IsSharable bool `json:"isSharable,omitempty"`

	// The OCID of the volume.
	// +optional
	VolumeId string `json:"volumeId,omitempty"`

	// A filter to filter volume.
	// +optional
	VolumeFilter OciResourceSelectorTerm `json:"volumeFilter,omitempty"`

	// Create a volume with the details.
	// +optional
	CreateVolumeDetails VolumeAttribute `json:"createVolumeDetails,omitempty"`
}

// VolumeAttribute define attributes that will be created and attached or attached to an instance on creation.
type VolumeAttribute struct {
	// KmsKeyConfig optionally references a KMS key used to encrypt the volume.
	// +optional
	KmsKeyConfig *KmsKeyConfig `json:"kmsKeyConfig,omitempty"`

	// SizeInGBs is the size of the volume in GB.
	// +kubebuilder:validation:Minimum=1
	// +optional
	SizeInGBs *int64 `json:"sizeInGBs,omitempty"`

	// VpusPerGB configures number of volume performance units (VPUs) that will be applied to this volume per GB
	// +kubebuilder:validation:XValidation:rule="self == 10 || self == 20 || (self >= 30 && self <= 120)",message="VpusPerGB must be 10, 20, or between 30 and 120"
	// +optional
	VpusPerGB *int64 `json:"vpusPerGB,omitempty"`
}

// KmsKeyConfig defines reference to a kms key via an OCID.
type KmsKeyConfig struct {
	// KmsKeyId refers to a KmsKey by OCID to encrypt volume
	KmsKeyId *string `json:"kmsKeyId,omitempty"`
}

// BootVolumeConfig contains all configurations to configure boot volume of an oci compute instance
type BootVolumeConfig struct {
	// ImageConfig selects the image for the boot volume via imageId or imageFilter.
	// +required
	ImageConfig *ImageConfig `json:"imageConfig"`

	// VolumeAttribute defines KMS, size, and performance attributes for the boot volume.
	VolumeAttribute `json:",inline"`

	// PvEncryptionInTransit configures the "isPvEncryptionInTransitEnabled" bool flag
	// +optional
	PvEncryptionInTransit *bool `json:"pvEncryptionInTransit,omitempty"`
}

// ImageConfig defines reference to image(s) via an OCID or an image filter.
// +kubebuilder:validation:XValidation:message="must specify exactly one of ['imageFilter', 'imageId']",rule="has(self.imageId) && !has(self.imageFilter) || !has(self.imageId) && has(self.imageFilter)"
type ImageConfig struct {
	// ImageType declares how OKE bootstrapping should be performed for the selected image.
	// +required
	ImageType ImageType `json:"imageType"`

	// ImageFilter select a set of images
	// +optional
	ImageFilter *ImageSelectorTerm `json:"imageFilter,omitempty"`

	// ImageId select a specific image by OCID
	// +optional
	ImageId *string `json:"imageId,omitempty"`
}

// NetworkConfig defines vnic's subnet and optional network security groups for a compute instance
type NetworkConfig struct {
	// PrimaryVnicConfig is required to configure the primary vnic's subnet and optional network security groups
	// +required
	PrimaryVnicConfig *SimpleVnicConfig `json:"primaryVnicConfig"`

	// SecondaryVnicConfig secondaryVnicConfigs
	// +kubebuilder:validation:MinItems=0
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:Optional
	// +optional
	SecondaryVnicConfigs []*SecondaryVnicConfig `json:"secondaryVnicConfigs"`
}

// CapacityReservationConfig define reference to a capacity reservation
// +kubebuilder:validation:XValidation:message="must specify exactly one of ['capacityReservationFilter', 'capacityReservationId']",rule="has(self.capacityReservationId) && !has(self.capacityReservationFilter) || !has(self.capacityReservationId) && has(self.capacityReservationFilter)"
type CapacityReservationConfig struct {
	// CapacityReservation ocid
	// +optional
	CapacityReservationId *string `json:"capacityReservationId,omitempty"`

	// CapacityReservation filter, must match exactly one capacityReservation
	// +kubebuilder:validation:XValidation:message="expected at least one, got none, ['displayName', 'freeformTags', 'definedTags']",rule="has(self.displayName) || has(self.freeformTags) || has(self.definedTags)"
	// +optional
	CapacityReservationFilter *OciResourceSelectorTerm `json:"capacityReservationFilter,omitempty"`
}

// ClusterPlacementGroupConfig define reference to a cluster placement group
// +kubebuilder:validation:XValidation:message="must specify exactly one of ['clusterPlacementGroupId', 'clusterPlacementGroupFilter']",rule="has(self.clusterPlacementGroupId) && !has(self.clusterPlacementGroupFilter) || !has(self.clusterPlacementGroupId) && has(self.clusterPlacementGroupFilter)"
type ClusterPlacementGroupConfig struct {
	// ClusterPlacementGroupId ocid
	// +optional
	ClusterPlacementGroupId *string `json:"clusterPlacementGroupId,omitempty"`

	// ClusterPlacementGroup filter, must match exactly one clusterPlacementGroup
	// +optional
	ClusterPlacementGroupFilter *OciResourceSelectorTerm `json:"clusterPlacementGroupFilter,omitempty"`
}

// ComputeClusterConfig define references to compute cluster
// +kubebuilder:validation:XValidation:message="must specify exactly one of ['computeClusterFilter', 'computeClusterId']",rule="has(self.computeClusterId) && !has(self.computeClusterFilter) || !has(self.computeClusterId) && has(self.computeClusterFilter)"
type ComputeClusterConfig struct {
	// ComputeCluster ocid
	// +optional
	ComputeClusterId *string `json:"computeClusterId,omitempty"`

	// ComputeCluster filter, must match exactly one computeCluster
	// +kubebuilder:validation:XValidation:message="expected at least one, got none, ['displayName', 'freeformTags', 'definedTags']",rule="has(self.displayName) || has(self.freeformTags) || has(self.definedTags)"
	// +optional
	ComputeClusterFilter *OciResourceSelectorTerm `json:"computeClusterFilter,omitempty"`
}

type VnicConfig struct {
	// SimpleVnicConfig defines attributes shared by both primary and secondary VNICs.
	SimpleVnicConfig
	// DisplayName is the display name assigned to the VNIC.
	DisplayName *string
	// AllocateIPV6 controls whether to allocate an IPv6 address for the VNIC.
	AllocateIPV6 *bool
	// AssignPrivateDnsRecord controls whether to create a private DNS record for the VNIC.
	AssignPrivateDnsRecord *bool
	// SkipSourceDestCheck controls whether source/destination checks are disabled on the VNIC.
	SkipSourceDestCheck *bool
	// Ipv6AddressIpv6SubnetCidrPairDetails is the list of IPv6 subnet CIDR/address pairs.
	Ipv6AddressIpv6SubnetCidrPairDetails []Ipv6AddressIpv6SubnetCidrPairDetails
	// FreeformTags is an optional set of free-form tags applied to the VNIC.
	FreeformTags map[string]string
	// DefinedTags is an optional set of defined tags applied to the VNIC.
	DefinedTags map[string]map[string]string
}

// OciResourceSelectorTerm defines a filter to match oci resource
type OciResourceSelectorTerm struct {
	// CompartmentId restrict resource owning compartment
	// +optional
	CompartmentId *string `json:"compartmentId"`
	// DisplayName is optional to exactly match OCI resources by name
	DisplayName *string `json:"displayName,omitempty"`
	// +optional
	// FreeformTags is optional to match OCI resources by free-form tags
	FreeformTags map[string]string `json:"freeformTags,omitempty"`
	// +optional
	// DefinedTags is optional to match OCI resources by defined tags
	// +optional
	DefinedTags map[string]map[string]string `json:"definedTags,omitempty"`
}

// SimpleVnicConfig defines attributes shared by both primary and secondary vnics
type SimpleVnicConfig struct {
	// SubnetAndNsgConfig defines subnet and optional network security groups
	*SubnetAndNsgConfig `json:",inline"`

	// AssignIpV6Ip flag for whether assign IpV6 IPs or not
	// +optional
	AssignIpV6Ip *bool `json:"assignIpV6Ip,omitempty"`

	// AssignPublicIp flag for whether assign public IPs or not. If it is true, the subnet must be a public subnet
	// +optional
	AssignPublicIp *bool `json:"assignPublicIp,omitempty"`

	// VnicDisplayname display name for the vnic
	// +kubebuilder:validation:MinLength:=1
	// +kubebuilder:validation:MaxLength:=255
	// +optional
	VnicDisplayName *string `json:"vnicDisplayname,omitempty"`

	// Ipv6AddressIpv6SubnetCidrPairDetails list of IpV6 subnet cider and address pairs
	// +optional
	Ipv6AddressIpv6SubnetCidrPairDetails []*Ipv6AddressIpv6SubnetCidrPairDetails `json:"ipv6AddressIpv6SubnetCidrPairDetails,omitempty"`

	// SkipSourceDestCheck flag for whether skip source dest check
	// +optional
	SkipSourceDestCheck *bool `json:"skipSourceDestCheck,omitempty"`

	// SecurityAttributes map of security attributes
	// +optional
	SecurityAttributes map[string]map[string]string `json:"securityAttributes,omitempty"`
}

// SubnetAndNsg defines subnet and optional network security groups
type SubnetAndNsgConfig struct {
	// SubnetConfig selects the subnet for the VNIC by subnetId or subnetFilter.
	// +required
	SubnetConfig *SubnetConfig `json:"subnetConfig"`

	// NetworkSecurityGroupConfigs selects an array of network security groups
	// +optional
	NetworkSecurityGroupConfigs []*NetworkSecurityGroupConfig `json:"networkSecurityGroupConfigs,omitempty"`
}

// SubnetConfig selects a subnet by subnetId or subnetFilter
// +kubebuilder:validation:XValidation:message="must specify exactly one of ['subnetFilter', 'subnetId']",rule="has(self.subnetId) && !has(self.subnetFilter) || !has(self.subnetId) && has(self.subnetFilter)"
type SubnetConfig struct {
	// Subnet ocid
	// +optional
	SubnetId *string `json:"subnetId,omitempty"`

	// Subnet filter, must match exactly one subnet
	// +kubebuilder:validation:XValidation:message="expected at least one, got none, ['displayName', 'freeformTags', 'definedTags']",rule="has(self.displayName) || has(self.freeformTags) || has(self.definedTags)"
	// +optional
	SubnetFilter *OciResourceSelectorTerm `json:"subnetFilter,omitempty"`
}

// NetworkSecurityGroupConfig selects an array of network security group resources
// +kubebuilder:validation:XValidation:message="must specify exactly one of ['networkSecurityGroupId', 'networkSecurityGroupFilter']",rule="has(self.networkSecurityGroupId) && !has(self.networkSecurityGroupFilter) || !has(self.networkSecurityGroupId) && has(self.networkSecurityGroupFilter)"
type NetworkSecurityGroupConfig struct {
	// NetworkSecurityGroupIds defines an array of network security group ids
	// +optional
	NetworkSecurityGroupId *string `json:"networkSecurityGroupId,omitempty"`

	// NetworkSecurityGroupFilter defines a network security group filter
	// +optional
	NetworkSecurityGroupFilter *OciResourceSelectorTerm `json:"networkSecurityGroupFilter,omitempty"`
}

// Ipv6AddressIpv6SubnetCidrPairDetails IpV6 subnet cider and address pairs
type Ipv6AddressIpv6SubnetCidrPairDetails struct {
	// SubnetCidr ipv6SubnetCidr string
	SubnetCidr string `json:"ipv6SubnetCidr,omitempty"`
}

// SecondaryVnicConfig defines subnet and optional network security groups
type SecondaryVnicConfig struct {
	// SimpleVnicConfig defines attributes shared by both primary and secondary VNICs.
	SimpleVnicConfig `json:",inline"`

	// ApplicationResource optional application identifier assigned to a vnic
	// +optional
	ApplicationResource *string `json:"applicationResource,omitempty"`

	// IpCount defines max number of IPs can be placed on a single vnic, the number should be to the power of 2.
	// For a OciIpNative CNI cluster, a pod will have its own unique IP. OCI compute shapes can have multiple vnics,
	// and there is a primary vnic reserved for node <--> api server communication. Seconds vnics can be used for pods,
	// and each vnic can allocate 256 IPs. When it is configured, those compute shapes that do not have enough vnics are filtered out.
	// +kubebuilder:validation:Minimum:=1
	// +kubebuilder:validation:Maximum:=256
	// +optional
	IpCount *int `json:"ipCount,omitempty"`

	// NicIndex 0 | 1 #vnic provision slot for bm hosts that have multiple cavium cards
	// +optional
	NicIndex *int `json:"nicIndex,omitempty"`
}

// ImageType is mandatory to configure and initialize bootstrap process correctly on a worker node, it accepts "OKEImage" only
type ImageType string

const (
	Platform ImageType = "Platform"
	OKEImage ImageType = "OKEImage"
	Custom   ImageType = "Custom"

	OracleLinux string = "Oracle Linux"
)

type ImageSelectorTerm struct {
	// OsFilter is the operating system name to match (for example, "Oracle Linux").
	OsFilter string `json:"osFilter"`
	// OsVersionFilter is the operating system version to match (for example, "8").
	OsVersionFilter string `json:"osVersionFilter"`

	// CompartmentId restricts the image search to the specified compartment OCID.
	CompartmentId *string `json:"compartmentId,omitempty"`
	// FreeformTags optionally filters images by free-form tags (key/value).
	FreeformTags map[string]string `json:"freeformTags,omitempty"`
	// DefinedTags optionally filters images by defined tags (namespace/key/value).
	DefinedTags map[string]map[string]string `json:"definedTags,omitempty"`
}

// PodsPerCore  value cannot exceed MaxPods
// +kubebuilder:validation:XValidation:message="podsPerCore value cannot exceed maxPods",rule="(has(self.maxPods) && has(self.podsPerCore) && (self.maxPods >= self.podsPerCore)) || (has(self.maxPods) && !has(self.podsPerCore)) || (!has(self.maxPods) && has(self.podsPerCore)) || (!has(self.maxPods) && !has(self.podsPerCore))"
type KubeletConfiguration struct {
	// ClusterDNS is a list of IP addresses for the cluster DNS server.
	// Note that not all providers may use all addresses.
	// +optional
	ClusterDNS []string `json:"clusterDNS,omitempty"`

	// ExtraArgs is a placeholder for other attributes that not listed here.
	// +optional
	ExtraArgs *string `json:"extraArgs,omitempty"`

	// NodeLabels is extra labels to pass into kubelet as initial node labels.
	// +optional
	NodeLabels map[string]string `json:"nodeLabels,omitempty"`

	// MaxPods is an override for the maximum number of pods that can run on
	// a worker node instance.
	// +kubebuilder:validation:Minimum:=1
	// +optional
	MaxPods *int32 `json:"maxPods,omitempty"`
	// PodsPerCore is an override for the number of pods that can run on a worker node
	// instance based on the number of cpu cores. This value cannot exceed MaxPods, so, if
	// MaxPods is a lower value, that value will be used.
	// +kubebuilder:validation:Minimum:=0
	// +optional
	PodsPerCore *int32 `json:"podsPerCore,omitempty"`
	// SystemReserved contains resources reserved for OS system daemons and kernel memory.
	// +kubebuilder:validation:XValidation:message="valid keys for systemReserved are ['cpu','memory','ephemeral-storage','pid']",rule="self.all(x, x=='cpu' || x=='memory' || x=='ephemeral-storage' || x=='pid')"
	// +kubebuilder:validation:XValidation:message="systemReserved value cannot be a negative resource quantity",rule="self.all(x, !self[x].startsWith('-'))"
	// +optional
	SystemReserved map[string]string `json:"systemReserved,omitempty"`
	// KubeReserved contains resources reserved for Kubernetes system components.
	// +kubebuilder:validation:XValidation:message="valid keys for kubeReserved are ['cpu','memory','ephemeral-storage','pid']",rule="self.all(x, x=='cpu' || x=='memory' || x=='ephemeral-storage' || x=='pid')"
	// +kubebuilder:validation:XValidation:message="kubeReserved value cannot be a negative resource quantity",rule="self.all(x, !self[x].startsWith('-'))"
	// +optional
	KubeReserved map[string]string `json:"kubeReserved,omitempty"`
	// EvictionHard is the map of signal names to quantities that define hard eviction thresholds
	// +kubebuilder:validation:XValidation:message="valid keys for evictionHard are ['memory.available','nodefs.available','nodefs.inodesFree','imagefs.available','imagefs.inodesFree','pid.available']",rule="self.all(x, x in ['memory.available','nodefs.available','nodefs.inodesFree','imagefs.available','imagefs.inodesFree','pid.available'])"
	// +optional
	EvictionHard map[string]string `json:"evictionHard,omitempty"`
	// EvictionSoft is the map of signal names to quantities that define soft eviction thresholds
	// +kubebuilder:validation:XValidation:message="valid keys for evictionSoft are ['memory.available','nodefs.available','nodefs.inodesFree','imagefs.available','imagefs.inodesFree','pid.available']",rule="self.all(x, x in ['memory.available','nodefs.available','nodefs.inodesFree','imagefs.available','imagefs.inodesFree','pid.available'])"
	// +optional
	EvictionSoft map[string]string `json:"evictionSoft,omitempty"`
	// EvictionSoftGracePeriod is the map of signal names to quantities that define grace periods for each eviction signal
	// +kubebuilder:validation:XValidation:message="valid keys for evictionSoftGracePeriod are ['memory.available','nodefs.available','nodefs.inodesFree','imagefs.available','imagefs.inodesFree','pid.available']",rule="self.all(x, x in ['memory.available','nodefs.available','nodefs.inodesFree','imagefs.available','imagefs.inodesFree','pid.available'])"
	// +optional
	EvictionSoftGracePeriod map[string]metav1.Duration `json:"evictionSoftGracePeriod,omitempty"`
	// EvictionMaxPodGracePeriod is the maximum allowed grace period (in seconds) to use when terminating pods in
	// response to soft eviction thresholds being met.
	// +optional
	EvictionMaxPodGracePeriod *int32 `json:"evictionMaxPodGracePeriod,omitempty"`
	// ImageGCHighThresholdPercent is the percent of disk usage after which image
	// garbage collection is always run. The percent is calculated by dividing this
	// field value by 100, so this field must be between 0 and 100, inclusive.
	// When specified, the value must be greater than ImageGCLowThresholdPercent.
	// +kubebuilder:validation:Minimum:=0
	// +kubebuilder:validation:Maximum:=100
	// +optional
	ImageGCHighThresholdPercent *int32 `json:"imageGCHighThresholdPercent,omitempty"`
	// ImageGCLowThresholdPercent is the percent of disk usage before which image
	// garbage collection is never run. Lowest disk usage to garbage collect to.
	// The percent is calculated by dividing this field value by 100,
	// so the field value must be between 0 and 100, inclusive.
	// When specified, the value must be less than imageGCHighThresholdPercent
	// +kubebuilder:validation:Minimum:=0
	// +kubebuilder:validation:Maximum:=100
	// +optional
	ImageGCLowThresholdPercent *int32 `json:"imageGCLowThresholdPercent,omitempty"`
}

// +kubebuilder:validation:Enum=BASELINE_1_8;BASELINE_1_2;BASELINE_1_1
type BaselineOcpuUtilization string

const (
	BASELINE_1_8 BaselineOcpuUtilization = "BASELINE_1_8"
	BASELINE_1_2 BaselineOcpuUtilization = "BASELINE_1_2"
	BASELINE_1_1 BaselineOcpuUtilization = "BASELINE_1_1"
)

type ShapeConfig struct {
	// BaselineOcpuUtilization control utilization ratio on burstable shapes
	// accepted values is [BASELINE_1_8, BASELINE_1_2, BASELINE_1_1], only specify it in need.
	// +optional
	BaselineOcpuUtilization *BaselineOcpuUtilization `json:"baselineOcpuUtilization,omitempty"`

	// Ocpus control number of opcu on flexible shapes
	// when neither Ocpu nor MemoryInGbs is specified, flexible shapes are not considered as available offering
	// minimum value is 1, and need be an integer, where maximum value varies by shapes.
	// +kubebuilder:validation:Minimum:=1
	Ocpus *float32 `json:"ocpus,omitempty"`

	// MemoryInGbs control number of memory in GBs on flexible shapes
	// when neither Ocpu nor MemoryInGbs is specified, flexible shapes are not considered as available offering
	// minimum value is 1, and need be an integer, where maximum value varies by shapes.
	// +kubebuilder:validation:Minimum:=1
	// +optional
	MemoryInGbs *float32 `json:"memoryInGbs,omitempty"`
}

type Volume struct {
	// ImageCandidates is the resolved list of candidate images for the ImageFilter.
	ImageCandidates []*Image `json:"images,omitempty"`
	// KmsKeys is the resolved list of candidate KMS keys for the configured KMS selector.
	KmsKeys []*KmsKey `json:"kmsKey,omitempty"`
}

type KmsKey struct {
	// KmsKeyId is the OCID of the KMS key.
	KmsKeyId string `json:"kmsKeyId,omitempty"`
	// DisplayName is the display name of the KMS key.
	DisplayName string `json:"displayName,omitempty"`
}

type Image struct {
	// ImageId is the OCID of the image.
	ImageId string `json:"imageId,omitempty"`
	// DisplayName is the display name of the image.
	DisplayName string `json:"displayName,omitempty"`
}

type Network struct {
	// PrimaryVnic is the resolved primary VNIC configuration.
	PrimaryVnic *Vnic `json:"primaryVnic,omitempty"`
	// SecondaryVnics is the resolved secondary VNIC configurations.
	SecondaryVnics []*Vnic `json:"secondaryVnics,omitempty"`
}

type CapacityReservation struct {
	// CapacityReservationId is the OCID of the capacity reservation.
	CapacityReservationId string `json:"capacityReservationId,omitempty"`
	// DisplayName is the display name of the capacity reservation.
	DisplayName string `json:"displayName,omitempty"`
	// AvailabilityDomain is the availability domain of the capacity reservation.
	AvailabilityDomain string `json:"availabilityDomain,omitempty"`
}

type ClusterPlacementGroup struct {
	// ClusterPlacementGroupId is the OCID of the cluster placement group.
	ClusterPlacementGroupId string `json:"clusterPlacementGroupId,omitempty"`
	// DisplayName is the display name of the cluster placement group.
	DisplayName string `json:"displayName,omitempty"`
	// AvailabilityDomain is the availability domain of the cluster placement group.
	AvailabilityDomain string `json:"availabilityDomain,omitempty"`
}

type ComputeCluster struct {
	// ComputeClusterId is the OCID of the compute cluster.
	ComputeClusterId string `json:"ComputeClusterId,omitempty"`
	// DisplayName is the display name of the compute cluster.
	DisplayName string `json:"displayName,omitempty"`
	// AvailabilityDomain is the availability domain of the compute cluster.
	AvailabilityDomain string `json:"availabilityDomain,omitempty"`
}

type Vnic struct {
	// Subnet is the resolved subnet for the VNIC.
	Subnet Subnet `json:"subnet,omitempty"`
	// NetworkSecurityGroups is the resolved NSG list for the VNIC.
	NetworkSecurityGroups []NetworkSecurityGroup `json:"networkSecurityGroups,omitempty"`
}

type Subnet struct {
	// SubnetId is the OCID of the subnet.
	SubnetId string `json:"subnetId,omitempty"`
	// DisplayName is the display name of the subnet.
	DisplayName string `json:"displayName,omitempty"`
}

type NetworkSecurityGroup struct {
	// NetworkSecurityGroupId is the OCID of the network security group.
	NetworkSecurityGroupId string `json:"networkSecurityGroupId,omitempty"`
	// DisplayName is the display name of the network security group.
	DisplayName string `json:"displayName,omitempty"`
}

// OCINodeClassStatus defines the observed state of OCINodeClass.
type OCINodeClassStatus struct {
	// Conditions represents the reconciliation state of the OCINodeClass.
	Conditions []status.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
	// Volume contains resolved boot volume and KMS key details.
	Volume *Volume `json:"volume,omitempty"`
	// Network contains resolved VNIC, subnet, and NSG details.
	Network *Network `json:"network,omitempty"`
	// CapacityReservations contains resolved capacity reservation details.
	CapacityReservations []CapacityReservation `json:"capacityReservations,omitempty"`
	// ClusterPlacementGroups contains resolved cluster placement group details.
	ClusterPlacementGroups []ClusterPlacementGroup `json:"clusterPlacementGroups,omitempty"`
	// ComputeCluster contains resolved compute cluster details.
	ComputeCluster *ComputeCluster `json:"computeCluster,omitempty"`
}

const (
	ConditionTypeReady                 = "Ready"
	ConditionStatusTrue                = "True"
	ConditionStatusFalse               = "False"
	ConditionTypeImageReady            = "Image"
	ConditionTypeNetworkReady          = "Network"
	ConditionTypeNodeCompartment       = "NodeCompartment"
	ConditionTypeKmsKeyReady           = "KmsKey"
	ConditionTypeCapacityReservation   = "CapacityReservation"
	ConditionTypeComputeCluster        = "ComputeCluster"
	ConditionTypeClusterPlacementGroup = "ClusterPlacementGroup"

	ConditionImageNotReadyReason                 = "ImageResolveFailure"
	ConditionNetworkNotReadyReason               = "NetworkResolveFailure"
	ConditionNodeCompartmentNotReadyReason       = "NodeCompartmentResolveFailure"
	ConditionKmsKeyNotReadyReason                = "KmsKeyResolveFailure"
	ConditionCapacityReservationNotReadyReason   = "CapacityReservationResolveFailure"
	ConditionClusterPlacementGroupNotReadyReason = "ClusterPlacementGroupResolveFailure"
	ConditionComputeClusterNotReadyReason        = "ComputeClusterResolveFailure"
)

// OCINodeClass is the Schema for the ocinodeclasses API.
// +kubebuilder:object:root=true
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status",description=""
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description=""
// +kubebuilder:resource:path=ocinodeclasses,scope=Cluster,categories=karpenter,shortName={ocinc,ocincs}
// +kubebuilder:subresource:status
type OCINodeClass struct {
	// TypeMeta contains the API version and kind for this object.
	metav1.TypeMeta `json:",inline"`
	// ObjectMeta contains standard object metadata (name, labels, annotations, etc.).
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of the node class.
	Spec OCINodeClassSpec `json:"spec,omitempty"`
	// Status defines the observed state of the node class.
	Status OCINodeClassStatus `json:"status,omitempty"`
}

/*
* Below methods are required by karpenter itself to be compatible with NodeClass,
* which is hideous. Stay Away from using them.
 */

func (o *OCINodeClass) GetConditions() []status.Condition {
	return o.Status.Conditions
}

func (o *OCINodeClass) SetConditions(conditions []status.Condition) {
	o.Status.Conditions = conditions
}

func (o *OCINodeClass) StatusConditions() status.ConditionSet {
	var conditions = []string{
		ConditionTypeImageReady,
		ConditionTypeNetworkReady,
		ConditionTypeNodeCompartment,
	}

	if o.Spec.VolumeConfig.BootVolumeConfig.KmsKeyConfig != nil {
		conditions = append(conditions, ConditionTypeKmsKeyReady)
	}

	if len(o.Spec.CapacityReservationConfigs) > 0 {
		conditions = append(conditions, ConditionTypeCapacityReservation)
	}

	if len(o.Spec.ClusterPlacementGroupConfigs) > 0 {
		conditions = append(conditions, ConditionTypeClusterPlacementGroup)
	}

	if o.Spec.ComputeClusterConfig != nil {
		conditions = append(conditions, ConditionTypeComputeCluster)
	}

	return status.NewReadyConditions(conditions...).For(o)
}

func (o *OCINodeClass) SetCondition(condition status.Condition) {
	found := false
	for _, exist := range o.Status.Conditions {
		if exist.Type == condition.Type {
			found = true

			// only update condition if changes, this is to avoid refresh lastTransition time unnecessarily
			if exist.Status != condition.Status || exist.Reason != condition.Reason || exist.Message != condition.Message {
				condition.DeepCopyInto(&exist)
				exist.LastTransitionTime = metav1.NewTime(time.Now())
				break
			}
		}
	}

	if !found {
		condition.LastTransitionTime = metav1.NewTime(time.Now())
		o.Status.Conditions = append(o.Status.Conditions, condition)
	}
}

// +kubebuilder:object:root=true

// OCINodeClassList contains a list of OCINodeClass.
type OCINodeClassList struct {
	// TypeMeta contains the API version and kind for this list.
	metav1.TypeMeta `json:",inline"`
	// ListMeta contains standard list metadata.
	metav1.ListMeta `json:"metadata,omitempty"`
	// Items is the list of OCINodeClass objects.
	Items []OCINodeClass `json:"items"`
}
