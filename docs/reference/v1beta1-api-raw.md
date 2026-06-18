v1beta1
=======

| Metadata             | Value                                                     |
|----------------------|-----------------------------------------------------------|
| Group                | oci.oraclecloud.com                                       |
| Version              |                                                           |
| Module               | github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1 |
| Property Optionality |                                                           |

<a id="AttachVolumeDetails"></a>AttachVolumeDetails
---------------------------------------------------

| Property            | Description                                                 | Type                                                |
|---------------------|-------------------------------------------------------------|-----------------------------------------------------|
| createVolumeDetails | Create a volume with the details.                           | [VolumeAttribute](#VolumeAttribute)                 |
| isReadonly          | Whether the attachment was created in read-only mode.       | bool                                                |
| isSharable          | Whether the attachment should be created in shareable mode. | bool                                                |
| volumeFilter        | A filter to filter volume.                                  | [OciResourceSelectorTerm](#OciResourceSelectorTerm) |
| volumeId            | The OCID of the volume.                                     | string                                              |

<a id="IscsiEncryptInTransitType"></a>IscsiEncryptInTransitType
---------------------------------------------------------------

Used by: [LaunchAttachIscsiVolumeConfig.encryptionInTransitType](#LaunchAttachIscsiVolumeConfig).

<a id="KmsKeyConfig"></a>KmsKeyConfig
-------------------------------------

KmsKeyConfig defines reference to a kms key via an OCID.

Used by: [VolumeAttribute.kmsKeyConfig](#VolumeAttribute).

| Property | Description                                           | Type   |
|----------|-------------------------------------------------------|--------|
| kmsKeyId | KmsKeyId refers to a KmsKey by OCID to encrypt volume | string |

<a id="LaunchAttachIscsiVolumeConfig"></a>LaunchAttachIscsiVolumeConfig
-----------------------------------------------------------------------

LaunchAttachIscsiVolumeConfig defines details of attaching an iscsi volume along with worker node instance launch

Used by: [LaunchAttachVolumeConfig.iscsiVolumeConfig](#LaunchAttachVolumeConfig).

| Property                                    | Description                                                                                                                                                                   | Type                                                    |
|---------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|---------------------------------------------------------|
| [AttachVolumeDetails](#AttachVolumeDetails) | AttachVolumeDetails selects a volume by ID, filter, or createVolumeDetails.                                                                                                   |                                                         |
| agentAutoIscsiLoginEnabled                  | Whether to enable Oracle Cloud Agent to perform the iSCSI login and logout commands after the volume attach or detach operations for non multipath-enabled iSCSI attachments. | bool                                                    |
| encryptionInTransitType                     | EncryptionInTransitType configure encryption in transition type, default is NONE                                                                                              | [IscsiEncryptInTransitType](#IscsiEncryptInTransitType) |

<a id="LaunchAttachParavirtualizedVolumeConfig"></a>LaunchAttachParavirtualizedVolumeConfig
-------------------------------------------------------------------------------------------

LaunchAttachParavirtualizedVolumeConfig defines details of attaching a paravirtualized volume along with worker node instance launch

Used by: [LaunchAttachVolumeConfig.paravirtualizedVolumeConfig](#LaunchAttachVolumeConfig).

| Property                                    | Description                                                                     | Type |
|---------------------------------------------|---------------------------------------------------------------------------------|------|
| [AttachVolumeDetails](#AttachVolumeDetails) | AttachVolumeDetails selects a volume by ID, filter, or createVolumeDetails.     |      |
| pvEncryptionInTransit                       | PvEncryptionInTransit configures the "isPvEncryptionInTransitEnabled" bool flag | bool |

<a id="LaunchAttachVolumeConfig"></a>LaunchAttachVolumeConfig
-------------------------------------------------------------

LaunchAttachVolumeConfig defines a volume to be attached along with worker node instance launch.

| Property                    | Description                                                                                   | Type                                                                                |
|-----------------------------|-----------------------------------------------------------------------------------------------|-------------------------------------------------------------------------------------|
| iscsiVolumeConfig           | IscsiVolumeConfig configures attaching an iSCSI volume at instance launch.                    | [LaunchAttachIscsiVolumeConfig](#LaunchAttachIscsiVolumeConfig)                     |
| paravirtualizedVolumeConfig | ParavirtualizedVolumeConfig configures attaching a paravirtualized volume at instance launch. | [LaunchAttachParavirtualizedVolumeConfig](#LaunchAttachParavirtualizedVolumeConfig) |

<a id="NetworkSecurityGroupConfig"></a>NetworkSecurityGroupConfig
-----------------------------------------------------------------

NetworkSecurityGroupConfig selects an array of network security group resources

Used by: [SubnetAndNsgConfig.networkSecurityGroupConfigs](#SubnetAndNsgConfig).

| Property                   | Description                                                            | Type                                                |
|----------------------------|------------------------------------------------------------------------|-----------------------------------------------------|
| networkSecurityGroupFilter | NetworkSecurityGroupFilter defines a network security group filter     | [OciResourceSelectorTerm](#OciResourceSelectorTerm) |
| networkSecurityGroupId     | NetworkSecurityGroupIds defines an array of network security group ids | string                                              |

<a id="OCINodeClass"></a>OCINodeClass
-------------------------------------

OCINodeClass is the Schema for the ocinodeclasses API.

Used by: [OCINodeClassList.items](#OCINodeClassList).

| Property          | Description                                                                     | Type                                      |
|-------------------|---------------------------------------------------------------------------------|-------------------------------------------|
| metav1.TypeMeta   | TypeMeta contains the API version and kind for this object.                     |                                           |
| metav1.ObjectMeta | ObjectMeta contains standard object metadata (name, labels, annotations, etc.). |                                           |
| spec              | Spec defines the desired state of the node class.                               | [OCINodeClassSpec](#OCINodeClassSpec)     |
| status            | Status defines the observed state of the node class.                            | [OCINodeClassStatus](#OCINodeClassStatus) |

### <a id="OCINodeClassSpec"></a>OCINodeClassSpec

| Property                     | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | Type                                                                      |
|------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------|
| agentList                    | AgentList is a list of Oracle Cloud Agent plugins to enable on the launched instance. Each entry must be the exact plugin name as listed by the OCI ListInstanceagentAvailablePlugins API (for example, "Bastion", "Block Volume Management", "Compute Instance Monitoring", "OS Management Service Agent"). Each listed plugin is set to ENABLED at launch time; any plugin not listed is left at its image default. Unknown plugin names are accepted by this provider but ignored by the OCI control plane. | string[]                                                                  |
| capacityReservationConfigs   | CapacityReservationConfigs contains an array of capacity reservations                                                                                                                                                                                                                                                                                                                                                                                                                                          | [CapacityReservationConfig[]](#CapacityReservationConfig)                 |
| clusterPlacementGroupConfigs | ClusterPlacementGroupConfigs contains an array of cluster placement group.                                                                                                                                                                                                                                                                                                                                                                                                                                     | [ClusterPlacementGroupConfig[]](#ClusterPlacementGroupConfig)             |
| computeClusterConfig         | ComputeClusterConfig refers to a compute cluster. It is fully immutable once set and cannot be modified or deleted after creation, as changing the compute cluster has severe consequences.                                                                                                                                                                                                                                                                                                                    | [ComputeClusterConfig](#ComputeClusterConfig)<br/><small>Optional</small> |
| definedTags                  | DefinedTags is customer-owned namespace key/value labels passed into the compute instance.                                                                                                                                                                                                                                                                                                                                                                                                                     | map[string]map[string]string                                              |
| freeformTags                 | FreeformTags is customer-owned key/value labels passed into the compute instance.                                                                                                                                                                                                                                                                                                                                                                                                                              | map[string]string                                                         |
| kubeletConfig                | KubeletConfig gives customers finer control over parameters passed to the kubelet process.                                                                                                                                                                                                                                                                                                                                                                                                                     | [KubeletConfiguration](#KubeletConfiguration)                             |
| launchOptions                | LaunchOptions gives advanced control of volume, network, firmware and etc. of compute instance                                                                                                                                                                                                                                                                                                                                                                                                                 | [LaunchOptions](#LaunchOptions)                                           |
| metadata                     | Metadata is user_data passed into the compute instance.                                                                                                                                                                                                                                                                                                                                                                                                                                                        | map[string]string                                                         |
| networkConfig                | NetworkConfig defines vnic subnet and optional network security groups for compute instance                                                                                                                                                                                                                                                                                                                                                                                                                    | [NetworkConfig](#NetworkConfig)                                           |
| nodeCompartmentId            | NodeCompartmentId is optional to place instance in a different compartment from the cluster                                                                                                                                                                                                                                                                                                                                                                                                                    | string                                                                    |
| postBootstrapInitScript      | PostBootstrapInitScript is an optional base64 encoded script to run after OKE bootstrapping                                                                                                                                                                                                                                                                                                                                                                                                                    | string                                                                    |
| preBootstrapInitScript       | PreBootstrapInitScript is an optional base64 encoded script to run before OKE bootstrapping                                                                                                                                                                                                                                                                                                                                                                                                                    | string                                                                    |
| shapeConfigs                 | ShapeConfigs is additional shape config applies to flexible and burstable config without specifying it, flexible and burstable shapes are excluded from scheduling consideration. Different from other cloud providers, our flexible/burstable options are not offered as individual shapes, thus we cannot expose one instance type with dynamic pricing and allocatable resources.                                                                                                                           | [ShapeConfig[]](#ShapeConfig)                                             |
| sshAuthorizedKeys            | SshAuthorizedKeys is an array of authorized SSH public keys passed into the compute instance.                                                                                                                                                                                                                                                                                                                                                                                                                  | string[]                                                                  |
| volumeConfig                 | VolumeConfig contains required configuration for the boot volume (and optional additional volumes).                                                                                                                                                                                                                                                                                                                                                                                                            | [VolumeConfig](#VolumeConfig)                                             |

### <a id="OCINodeClassStatus"></a>OCINodeClassStatus

| Property               | Description                                                               | Type                                              |
|------------------------|---------------------------------------------------------------------------|---------------------------------------------------|
| capacityReservations   | CapacityReservations contains resolved capacity reservation details.      | [CapacityReservation[]](#CapacityReservation)     |
| clusterPlacementGroups | ClusterPlacementGroups contains resolved cluster placement group details. | [ClusterPlacementGroup[]](#ClusterPlacementGroup) |
| computeCluster         | ComputeCluster contains resolved compute cluster details.                 | [ComputeCluster](#ComputeCluster)                 |
| conditions             | Conditions represents the reconciliation state of the OCINodeClass.       | status.Condition[]                                |
| network                | Network contains resolved VNIC, subnet, and NSG details.                  | [Network](#Network)                               |
| volume                 | Volume contains resolved boot volume and KMS key details.                 | [Volume](#Volume)                                 |

<a id="OCINodeClassList"></a>OCINodeClassList
---------------------------------------------

<br/>OCINodeClassList contains a list of OCINodeClass.

| Property        | Description                                               | Type                            |
|-----------------|-----------------------------------------------------------|---------------------------------|
| metav1.TypeMeta | TypeMeta contains the API version and kind for this list. |                                 |
| metav1.ListMeta | ListMeta contains standard list metadata.                 |                                 |
| items           | Items is the list of OCINodeClass objects.                | [OCINodeClass[]](#OCINodeClass) |

<a id="SubnetAndNsgConfig"></a>SubnetAndNsgConfig
-------------------------------------------------

SubnetAndNsg defines subnet and optional network security groups

| Property                    | Description                                                               | Type                                                        |
|-----------------------------|---------------------------------------------------------------------------|-------------------------------------------------------------|
| networkSecurityGroupConfigs | NetworkSecurityGroupConfigs selects an array of network security groups   | [NetworkSecurityGroupConfig[]](#NetworkSecurityGroupConfig) |
| subnetConfig                | SubnetConfig selects the subnet for the VNIC by subnetId or subnetFilter. | [SubnetConfig](#SubnetConfig)                               |

<a id="SubnetConfig"></a>SubnetConfig
-------------------------------------

SubnetConfig selects a subnet by subnetId or subnetFilter

Used by: [SubnetAndNsgConfig.subnetConfig](#SubnetAndNsgConfig).

| Property     | Description                                  | Type                                                |
|--------------|----------------------------------------------|-----------------------------------------------------|
| subnetFilter | Subnet filter, must match exactly one subnet | [OciResourceSelectorTerm](#OciResourceSelectorTerm) |
| subnetId     | Subnet ocid                                  | string                                              |

<a id="VnicConfig"></a>VnicConfig
---------------------------------

| Property                              | Description                                                                              | Type                                                                            |
|---------------------------------------|------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------|
| [SimpleVnicConfig](#SimpleVnicConfig) | SimpleVnicConfig defines attributes shared by both primary and secondary VNICs.          |                                                                                 |
| AllocateIPV6                          | AllocateIPV6 controls whether to allocate an IPv6 address for the VNIC.                  | bool                                                                            |
| AssignPrivateDnsRecord                | AssignPrivateDnsRecord controls whether to create a private DNS record for the VNIC.     | bool                                                                            |
| DefinedTags                           | DefinedTags is an optional set of defined tags applied to the VNIC.                      | map[string]map[string]string                                                    |
| DisplayName                           | DisplayName is the display name assigned to the VNIC.                                    | string                                                                          |
| FreeformTags                          | FreeformTags is an optional set of free-form tags applied to the VNIC.                   | map[string]string                                                               |
| Ipv6AddressIpv6SubnetCidrPairDetails  | Ipv6AddressIpv6SubnetCidrPairDetails is the list of IPv6 subnet CIDR/address pairs.      | [Ipv6AddressIpv6SubnetCidrPairDetails[]](#Ipv6AddressIpv6SubnetCidrPairDetails) |
| SkipSourceDestCheck                   | SkipSourceDestCheck controls whether source/destination checks are disabled on the VNIC. | bool                                                                            |

<a id="VolumeAttribute"></a>VolumeAttribute
-------------------------------------------

VolumeAttribute define attributes that will be created and attached or attached to an instance on creation.

Used by: [AttachVolumeDetails.createVolumeDetails](#AttachVolumeDetails).

| Property     | Description                                                                                               | Type                          |
|--------------|-----------------------------------------------------------------------------------------------------------|-------------------------------|
| kmsKeyConfig | KmsKeyConfig optionally references a KMS key used to encrypt the volume.                                  | [KmsKeyConfig](#KmsKeyConfig) |
| sizeInGBs    | SizeInGBs is the size of the volume in GB.                                                                | int64                         |
| vpusPerGB    | VpusPerGB configures number of volume performance units (VPUs) that will be applied to this volume per GB | int64                         |

<a id="OCINodeClassSpec"></a>OCINodeClassSpec
---------------------------------------------

EDIT THIS FILE! THIS IS SCAFFOLDING FOR YOU TO OWN! NOTE: json tags are required. Any new fields you add must have json tags for the fields to be serialized. <br/><br/>OCINodeClassSpec defines the desired state of OCINodeClass.

Used by: [OCINodeClass.spec](#OCINodeClass).

| Property                     | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | Type                                                                      |
|------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------|
| agentList                    | AgentList is a list of Oracle Cloud Agent plugins to enable on the launched instance. Each entry must be the exact plugin name as listed by the OCI ListInstanceagentAvailablePlugins API (for example, "Bastion", "Block Volume Management", "Compute Instance Monitoring", "OS Management Service Agent"). Each listed plugin is set to ENABLED at launch time; any plugin not listed is left at its image default. Unknown plugin names are accepted by this provider but ignored by the OCI control plane. | string[]                                                                  |
| capacityReservationConfigs   | CapacityReservationConfigs contains an array of capacity reservations                                                                                                                                                                                                                                                                                                                                                                                                                                          | [CapacityReservationConfig[]](#CapacityReservationConfig)                 |
| clusterPlacementGroupConfigs | ClusterPlacementGroupConfigs contains an array of cluster placement group.                                                                                                                                                                                                                                                                                                                                                                                                                                     | [ClusterPlacementGroupConfig[]](#ClusterPlacementGroupConfig)             |
| computeClusterConfig         | ComputeClusterConfig refers to a compute cluster. It is fully immutable once set and cannot be modified or deleted after creation, as changing the compute cluster has severe consequences.                                                                                                                                                                                                                                                                                                                    | [ComputeClusterConfig](#ComputeClusterConfig)<br/><small>Optional</small> |
| definedTags                  | DefinedTags is customer-owned namespace key/value labels passed into the compute instance.                                                                                                                                                                                                                                                                                                                                                                                                                     | map[string]map[string]string                                              |
| freeformTags                 | FreeformTags is customer-owned key/value labels passed into the compute instance.                                                                                                                                                                                                                                                                                                                                                                                                                              | map[string]string                                                         |
| kubeletConfig                | KubeletConfig gives customers finer control over parameters passed to the kubelet process.                                                                                                                                                                                                                                                                                                                                                                                                                     | [KubeletConfiguration](#KubeletConfiguration)                             |
| launchOptions                | LaunchOptions gives advanced control of volume, network, firmware and etc. of compute instance                                                                                                                                                                                                                                                                                                                                                                                                                 | [LaunchOptions](#LaunchOptions)                                           |
| metadata                     | Metadata is user_data passed into the compute instance.                                                                                                                                                                                                                                                                                                                                                                                                                                                        | map[string]string                                                         |
| networkConfig                | NetworkConfig defines vnic subnet and optional network security groups for compute instance                                                                                                                                                                                                                                                                                                                                                                                                                    | [NetworkConfig](#NetworkConfig)                                           |
| nodeCompartmentId            | NodeCompartmentId is optional to place instance in a different compartment from the cluster                                                                                                                                                                                                                                                                                                                                                                                                                    | string                                                                    |
| postBootstrapInitScript      | PostBootstrapInitScript is an optional base64 encoded script to run after OKE bootstrapping                                                                                                                                                                                                                                                                                                                                                                                                                    | string                                                                    |
| preBootstrapInitScript       | PreBootstrapInitScript is an optional base64 encoded script to run before OKE bootstrapping                                                                                                                                                                                                                                                                                                                                                                                                                    | string                                                                    |
| shapeConfigs                 | ShapeConfigs is additional shape config applies to flexible and burstable config without specifying it, flexible and burstable shapes are excluded from scheduling consideration. Different from other cloud providers, our flexible/burstable options are not offered as individual shapes, thus we cannot expose one instance type with dynamic pricing and allocatable resources.                                                                                                                           | [ShapeConfig[]](#ShapeConfig)                                             |
| sshAuthorizedKeys            | SshAuthorizedKeys is an array of authorized SSH public keys passed into the compute instance.                                                                                                                                                                                                                                                                                                                                                                                                                  | string[]                                                                  |
| volumeConfig                 | VolumeConfig contains required configuration for the boot volume (and optional additional volumes).                                                                                                                                                                                                                                                                                                                                                                                                            | [VolumeConfig](#VolumeConfig)                                             |

<a id="OCINodeClassStatus"></a>OCINodeClassStatus
-------------------------------------------------

OCINodeClassStatus defines the observed state of OCINodeClass.

Used by: [OCINodeClass.status](#OCINodeClass).

| Property               | Description                                                               | Type                                              |
|------------------------|---------------------------------------------------------------------------|---------------------------------------------------|
| capacityReservations   | CapacityReservations contains resolved capacity reservation details.      | [CapacityReservation[]](#CapacityReservation)     |
| clusterPlacementGroups | ClusterPlacementGroups contains resolved cluster placement group details. | [ClusterPlacementGroup[]](#ClusterPlacementGroup) |
| computeCluster         | ComputeCluster contains resolved compute cluster details.                 | [ComputeCluster](#ComputeCluster)                 |
| conditions             | Conditions represents the reconciliation state of the OCINodeClass.       | status.Condition[]                                |
| network                | Network contains resolved VNIC, subnet, and NSG details.                  | [Network](#Network)                               |
| volume                 | Volume contains resolved boot volume and KMS key details.                 | [Volume](#Volume)                                 |

<a id="CapacityReservation"></a>CapacityReservation
---------------------------------------------------

Used by: [OCINodeClassStatus.capacityReservations](#OCINodeClassStatus).

| Property              | Description                                                                | Type   |
|-----------------------|----------------------------------------------------------------------------|--------|
| availabilityDomain    | AvailabilityDomain is the availability domain of the capacity reservation. | string |
| capacityReservationId | CapacityReservationId is the OCID of the capacity reservation.             | string |
| displayName           | DisplayName is the display name of the capacity reservation.               | string |

<a id="CapacityReservationConfig"></a>CapacityReservationConfig
---------------------------------------------------------------

CapacityReservationConfig define reference to a capacity reservation

Used by: [OCINodeClassSpec.capacityReservationConfigs](#OCINodeClassSpec).

| Property                  | Description                                                            | Type                                                |
|---------------------------|------------------------------------------------------------------------|-----------------------------------------------------|
| capacityReservationFilter | CapacityReservation filter, must match exactly one capacityReservation | [OciResourceSelectorTerm](#OciResourceSelectorTerm) |
| capacityReservationId     | CapacityReservation ocid                                               | string                                              |

<a id="ClusterPlacementGroup"></a>ClusterPlacementGroup
-------------------------------------------------------

Used by: [OCINodeClassStatus.clusterPlacementGroups](#OCINodeClassStatus).

| Property                | Description                                                                   | Type   |
|-------------------------|-------------------------------------------------------------------------------|--------|
| availabilityDomain      | AvailabilityDomain is the availability domain of the cluster placement group. | string |
| clusterPlacementGroupId | ClusterPlacementGroupId is the OCID of the cluster placement group.           | string |
| displayName             | DisplayName is the display name of the cluster placement group.               | string |

<a id="ClusterPlacementGroupConfig"></a>ClusterPlacementGroupConfig
-------------------------------------------------------------------

ClusterPlacementGroupConfig define reference to a cluster placement group

Used by: [OCINodeClassSpec.clusterPlacementGroupConfigs](#OCINodeClassSpec).

| Property                    | Description                                                                | Type                                                |
|-----------------------------|----------------------------------------------------------------------------|-----------------------------------------------------|
| clusterPlacementGroupFilter | ClusterPlacementGroup filter, must match exactly one clusterPlacementGroup | [OciResourceSelectorTerm](#OciResourceSelectorTerm) |
| clusterPlacementGroupId     | ClusterPlacementGroupId ocid                                               | string                                              |

<a id="ComputeCluster"></a>ComputeCluster
-----------------------------------------

Used by: [OCINodeClassStatus.computeCluster](#OCINodeClassStatus).

| Property           | Description                                                           | Type   |
|--------------------|-----------------------------------------------------------------------|--------|
| availabilityDomain | AvailabilityDomain is the availability domain of the compute cluster. | string |
| ComputeClusterId   | ComputeClusterId is the OCID of the compute cluster.                  | string |
| displayName        | DisplayName is the display name of the compute cluster.               | string |

<a id="ComputeClusterConfig"></a>ComputeClusterConfig
-----------------------------------------------------

ComputeClusterConfig define references to compute cluster

Used by: [OCINodeClassSpec.computeClusterConfig](#OCINodeClassSpec).

| Property             | Description                                                  | Type                                                |
|----------------------|--------------------------------------------------------------|-----------------------------------------------------|
| computeClusterFilter | ComputeCluster filter, must match exactly one computeCluster | [OciResourceSelectorTerm](#OciResourceSelectorTerm) |
| computeClusterId     | ComputeCluster ocid                                          | string                                              |

<a id="KubeletConfiguration"></a>KubeletConfiguration
-----------------------------------------------------

PodsPerCore value cannot exceed MaxPods

Used by: [OCINodeClassSpec.kubeletConfig](#OCINodeClassSpec).

| Property                    | Description                                                                                                                                                                                                                                                                                                                                          | Type                       |
|-----------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|----------------------------|
| clusterDNS                  | ClusterDNS is a list of IP addresses for the cluster DNS server. Note that not all providers may use all addresses.                                                                                                                                                                                                                                  | string[]                   |
| evictionHard                | EvictionHard is the map of signal names to quantities that define hard eviction thresholds                                                                                                                                                                                                                                                           | map[string]string          |
| evictionMaxPodGracePeriod   | EvictionMaxPodGracePeriod is the maximum allowed grace period (in seconds) to use when terminating pods in response to soft eviction thresholds being met.                                                                                                                                                                                           | int32                      |
| evictionSoft                | EvictionSoft is the map of signal names to quantities that define soft eviction thresholds                                                                                                                                                                                                                                                           | map[string]string          |
| evictionSoftGracePeriod     | EvictionSoftGracePeriod is the map of signal names to quantities that define grace periods for each eviction signal                                                                                                                                                                                                                                  | map[string]metav1.Duration |
| extraArgs                   | ExtraArgs is a placeholder for other attributes that not listed here.                                                                                                                                                                                                                                                                                | string                     |
| imageGCHighThresholdPercent | ImageGCHighThresholdPercent is the percent of disk usage after which image garbage collection is always run. The percent is calculated by dividing this field value by 100, so this field must be between 0 and 100, inclusive. When specified, the value must be greater than ImageGCLowThresholdPercent.                                           | int32                      |
| imageGCLowThresholdPercent  | ImageGCLowThresholdPercent is the percent of disk usage before which image garbage collection is never run. Lowest disk usage to garbage collect to. The percent is calculated by dividing this field value by 100, so the field value must be between 0 and 100, inclusive. When specified, the value must be less than imageGCHighThresholdPercent | int32                      |
| kubeReserved                | KubeReserved contains resources reserved for Kubernetes system components.                                                                                                                                                                                                                                                                           | map[string]string          |
| maxPods                     | MaxPods is an override for the maximum number of pods that can run on a worker node instance.                                                                                                                                                                                                                                                        | int32                      |
| nodeLabels                  | NodeLabels is extra labels to pass into kubelet as initial node labels.                                                                                                                                                                                                                                                                              | map[string]string          |
| podsPerCore                 | PodsPerCore is an override for the number of pods that can run on a worker node instance based on the number of cpu cores. This value cannot exceed MaxPods, so, if MaxPods is a lower value, that value will be used.                                                                                                                               | int32                      |
| systemReserved              | SystemReserved contains resources reserved for OS system daemons and kernel memory.                                                                                                                                                                                                                                                                  | map[string]string          |

<a id="LaunchOptions"></a>LaunchOptions
---------------------------------------

LaunchOptions gives advanced control of volume, network, firmware and etc. of compute instance

Used by: [OCINodeClassSpec.launchOptions](#OCINodeClassSpec).

| Property                      | Description                                                                                | Type    |
|-------------------------------|--------------------------------------------------------------------------------------------|---------|
| bootVolumeType                | BootVolumeType defines type of boot volume, accepts any of ISCSI                           | SCSI    |
| consistentVolumeNamingEnabled | Whether to enable consistent volume naming feature. Defaults to false.                     | bool    |
| firmware                      | Firmware defines kind of firmware, accepts any of BIOS                                     | UEFI_64 |
| networkType                   | NetworkType defines emulation type of physical network interface card, accepts any of VFIO | E1000   |
| remoteDataVolumeType          | RemoteDataVolumeType defines type of remote data volume, accepts any of ISCSI              | SCSI    |

<a id="Network"></a>Network
---------------------------

Used by: [OCINodeClassStatus.network](#OCINodeClassStatus).

| Property       | Description                                                   | Type            |
|----------------|---------------------------------------------------------------|-----------------|
| primaryVnic    | PrimaryVnic is the resolved primary VNIC configuration.       | [Vnic](#Vnic)   |
| secondaryVnics | SecondaryVnics is the resolved secondary VNIC configurations. | [Vnic[]](#Vnic) |

<a id="NetworkConfig"></a>NetworkConfig
---------------------------------------

NetworkConfig defines vnic's subnet and optional network security groups for a compute instance

Used by: [OCINodeClassSpec.networkConfig](#OCINodeClassSpec).

| Property             | Description                                                                                               | Type                                                                      |
|----------------------|-----------------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------|
| primaryVnicConfig    | PrimaryVnicConfig is required to configure the primary vnic's subnet and optional network security groups | [SimpleVnicConfig](#SimpleVnicConfig)                                     |
| secondaryVnicConfigs | SecondaryVnicConfig secondaryVnicConfigs                                                                  | [SecondaryVnicConfig[]](#SecondaryVnicConfig)<br/><small>Optional</small> |

<a id="ShapeConfig"></a>ShapeConfig
-----------------------------------

Used by: [OCINodeClassSpec.shapeConfigs](#OCINodeClassSpec).

| Property                | Description                                                                                                                                                                                                                                               | Type                                                |
|-------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|-----------------------------------------------------|
| baselineOcpuUtilization | BaselineOcpuUtilization control utilization ratio on burstable shapes accepted values is [BASELINE_1_8, BASELINE_1_2, BASELINE_1_1], only specify it in need.                                                                                             | [BaselineOcpuUtilization](#BaselineOcpuUtilization) |
| memoryInGbs             | MemoryInGbs control number of memory in GBs on flexible shapes when neither Ocpu nor MemoryInGbs is specified, flexible shapes are not considered as available offering minimum value is 1, and need be an integer, where maximum value varies by shapes. | float32                                             |
| ocpus                   | Ocpus control number of opcu on flexible shapes when neither Ocpu nor MemoryInGbs is specified, flexible shapes are not considered as available offering minimum value is 1, and need be an integer, where maximum value varies by shapes.                | float32                                             |

<a id="Volume"></a>Volume
-------------------------

Used by: [OCINodeClassStatus.volume](#OCINodeClassStatus).

| Property | Description                                                                         | Type                |
|----------|-------------------------------------------------------------------------------------|---------------------|
| images   | ImageCandidates is the resolved list of candidate images for the ImageFilter.       | [Image[]](#Image)   |
| kmsKey   | KmsKeys is the resolved list of candidate KMS keys for the configured KMS selector. | [KmsKey[]](#KmsKey) |

<a id="VolumeConfig"></a>VolumeConfig
-------------------------------------

VolumeConfig contains required configuration for boot volume of a worker node, and optional configurations for additional volumes to be attached during worker node instance launch

Used by: [OCINodeClassSpec.volumeConfig](#OCINodeClassSpec).

| Property         | Description                                                                    | Type                                  |
|------------------|--------------------------------------------------------------------------------|---------------------------------------|
| bootVolumeConfig | BootVolumeConfig configures the boot volume used for the worker node instance. | [BootVolumeConfig](#BootVolumeConfig) |

<a id="BaselineOcpuUtilization"></a>BaselineOcpuUtilization
-----------------------------------------------------------

Used by: [ShapeConfig.baselineOcpuUtilization](#ShapeConfig).

<a id="BootVolumeConfig"></a>BootVolumeConfig
---------------------------------------------

BootVolumeConfig contains all configurations to configure boot volume of an oci compute instance

Used by: [VolumeConfig.bootVolumeConfig](#VolumeConfig).

| Property                            | Description                                                                        | Type                        |
|-------------------------------------|------------------------------------------------------------------------------------|-----------------------------|
| [VolumeAttribute](#VolumeAttribute) | VolumeAttribute defines KMS, size, and performance attributes for the boot volume. |                             |
| imageConfig                         | ImageConfig selects the image for the boot volume via imageId or imageFilter.      | [ImageConfig](#ImageConfig) |
| pvEncryptionInTransit               | PvEncryptionInTransit configures the "isPvEncryptionInTransitEnabled" bool flag    | bool                        |

<a id="Firmware"></a>Firmware
-----------------------------

Used by: [LaunchOptions.firmware](#LaunchOptions).

<a id="Image"></a>Image
-----------------------

Used by: [Volume.images](#Volume).

| Property    | Description                                   | Type   |
|-------------|-----------------------------------------------|--------|
| displayName | DisplayName is the display name of the image. | string |
| imageId     | ImageId is the OCID of the image.             | string |

<a id="KmsKey"></a>KmsKey
-------------------------

Used by: [Volume.kmsKey](#Volume).

| Property    | Description                                     | Type   |
|-------------|-------------------------------------------------|--------|
| displayName | DisplayName is the display name of the KMS key. | string |
| kmsKeyId    | KmsKeyId is the OCID of the KMS key.            | string |

<a id="NetworkType"></a>NetworkType
-----------------------------------

Used by: [LaunchOptions.networkType](#LaunchOptions).

<a id="OciResourceSelectorTerm"></a>OciResourceSelectorTerm
-----------------------------------------------------------

OciResourceSelectorTerm defines a filter to match oci resource

Used by: [AttachVolumeDetails.volumeFilter](#AttachVolumeDetails), [CapacityReservationConfig.capacityReservationFilter](#CapacityReservationConfig), [ClusterPlacementGroupConfig.clusterPlacementGroupFilter](#ClusterPlacementGroupConfig), [ComputeClusterConfig.computeClusterFilter](#ComputeClusterConfig), [NetworkSecurityGroupConfig.networkSecurityGroupFilter](#NetworkSecurityGroupConfig), and [SubnetConfig.subnetFilter](#SubnetConfig).

| Property      | Description                                                       | Type                         |
|---------------|-------------------------------------------------------------------|------------------------------|
| compartmentId | CompartmentId restrict resource owning compartment                | string                       |
| definedTags   | DefinedTags is optional to match OCI resources by defined tags    | map[string]map[string]string |
| displayName   | DisplayName is optional to exactly match OCI resources by name    | string                       |
| freeformTags  | FreeformTags is optional to match OCI resources by free-form tags | map[string]string            |

<a id="SecondaryVnicConfig"></a>SecondaryVnicConfig
---------------------------------------------------

SecondaryVnicConfig defines subnet and optional network security groups

Used by: [NetworkConfig.secondaryVnicConfigs](#NetworkConfig).

| Property                              | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                              | Type                                                                |
|---------------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|---------------------------------------------------------------------|
| [SimpleVnicConfig](#SimpleVnicConfig) | SimpleVnicConfig defines attributes shared by both primary and secondary VNICs.                                                                                                                                                                                                                                                                                                                                                                                          |                                                                     |
| applicationResource                   | ApplicationResource optional application identifier assigned to a vnic                                                                                                                                                                                                                                                                                                                                                                                                   | string                                                              |
| ipCount                               | IpCount defines max number of IPs can be placed on a single vnic, the number should be to the power of 2. For a OciIpNative CNI cluster, a pod will have its own unique IP. OCI compute shapes can have multiple vnics, and there is a primary vnic reserved for node <--> api server communication. Seconds vnics can be used for pods, and each vnic can allocate 256 IPs. When it is configured, those compute shapes that do not have enough vnics are filtered out. | int                                                                 |
| nicIndex                              | NicIndex 0                                                                                                                                                                                                                                                                                                                                                                                                                                                               | 1 #vnic provision slot for bm hosts that have multiple cavium cards |

<a id="SimpleVnicConfig"></a>SimpleVnicConfig
---------------------------------------------

SimpleVnicConfig defines attributes shared by both primary and secondary vnics

Used by: [NetworkConfig.primaryVnicConfig](#NetworkConfig).

| Property                                  | Description                                                                                                 | Type                                                                            |
|-------------------------------------------|-------------------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------|
| [SubnetAndNsgConfig](#SubnetAndNsgConfig) | SubnetAndNsgConfig defines subnet and optional network security groups                                      |                                                                                 |
| assignIpV6Ip                              | AssignIpV6Ip flag for whether assign IpV6 IPs or not                                                        | bool                                                                            |
| assignPublicIp                            | AssignPublicIp flag for whether assign public IPs or not. If it is true, the subnet must be a public subnet | bool                                                                            |
| ipv6AddressIpv6SubnetCidrPairDetails      | Ipv6AddressIpv6SubnetCidrPairDetails list of IpV6 subnet cider and address pairs                            | [Ipv6AddressIpv6SubnetCidrPairDetails[]](#Ipv6AddressIpv6SubnetCidrPairDetails) |
| securityAttributes                        | SecurityAttributes map of security attributes                                                               | map[string]map[string]string                                                    |
| skipSourceDestCheck                       | SkipSourceDestCheck flag for whether skip source dest check                                                 | bool                                                                            |
| vnicDisplayname                           | VnicDisplayname display name for the vnic                                                                   | string                                                                          |

<a id="Vnic"></a>Vnic
---------------------

Used by: [Network.primaryVnic](#Network), and [Network.secondaryVnics](#Network).

| Property              | Description                                                  | Type                                            |
|-----------------------|--------------------------------------------------------------|-------------------------------------------------|
| networkSecurityGroups | NetworkSecurityGroups is the resolved NSG list for the VNIC. | [NetworkSecurityGroup[]](#NetworkSecurityGroup) |
| subnet                | Subnet is the resolved subnet for the VNIC.                  | [Subnet](#Subnet)                               |

<a id="VolumeType"></a>VolumeType
---------------------------------

Used by: [LaunchOptions.bootVolumeType](#LaunchOptions), and [LaunchOptions.remoteDataVolumeType](#LaunchOptions).

<a id="ImageConfig"></a>ImageConfig
-----------------------------------

ImageConfig defines reference to image(s) via an OCID or an image filter.

Used by: [BootVolumeConfig.imageConfig](#BootVolumeConfig).

| Property    | Description                                                                          | Type                                    |
|-------------|--------------------------------------------------------------------------------------|-----------------------------------------|
| imageFilter | ImageFilter select a set of images                                                   | [ImageSelectorTerm](#ImageSelectorTerm) |
| imageId     | ImageId select a specific image by OCID                                              | string                                  |
| imageType   | ImageType declares how OKE bootstrapping should be performed for the selected image. | [ImageType](#ImageType)                 |

<a id="Ipv6AddressIpv6SubnetCidrPairDetails"></a>Ipv6AddressIpv6SubnetCidrPairDetails
-------------------------------------------------------------------------------------

Ipv6AddressIpv6SubnetCidrPairDetails IpV6 subnet cider and address pairs

Used by: [SimpleVnicConfig.ipv6AddressIpv6SubnetCidrPairDetails](#SimpleVnicConfig), and [VnicConfig.Ipv6AddressIpv6SubnetCidrPairDetails](#VnicConfig).

| Property       | Description                      | Type   |
|----------------|----------------------------------|--------|
| ipv6SubnetCidr | SubnetCidr ipv6SubnetCidr string | string |

<a id="NetworkSecurityGroup"></a>NetworkSecurityGroup
-----------------------------------------------------

Used by: [Vnic.networkSecurityGroups](#Vnic).

| Property               | Description                                                       | Type   |
|------------------------|-------------------------------------------------------------------|--------|
| displayName            | DisplayName is the display name of the network security group.    | string |
| networkSecurityGroupId | NetworkSecurityGroupId is the OCID of the network security group. | string |

<a id="Subnet"></a>Subnet
-------------------------

Used by: [Vnic.subnet](#Vnic).

| Property    | Description                                    | Type   |
|-------------|------------------------------------------------|--------|
| displayName | DisplayName is the display name of the subnet. | string |
| subnetId    | SubnetId is the OCID of the subnet.            | string |

<a id="ImageSelectorTerm"></a>ImageSelectorTerm
-----------------------------------------------

Used by: [ImageConfig.imageFilter](#ImageConfig).

| Property        | Description                                                                   | Type                         |
|-----------------|-------------------------------------------------------------------------------|------------------------------|
| compartmentId   | CompartmentId restricts the image search to the specified compartment OCID.   | string                       |
| definedTags     | DefinedTags optionally filters images by defined tags (namespace/key/value).  | map[string]map[string]string |
| freeformTags    | FreeformTags optionally filters images by free-form tags (key/value).         | map[string]string            |
| osFilter        | OsFilter is the operating system name to match (for example, "Oracle Linux"). | string                       |
| osVersionFilter | OsVersionFilter is the operating system version to match (for example, "8").  | string                       |

<a id="ImageType"></a>ImageType
-------------------------------

ImageType is mandatory to configure and initialize bootstrap process correctly on a worker node, it accepts "OKEImage" only

Used by: [ImageConfig.imageType](#ImageConfig).
