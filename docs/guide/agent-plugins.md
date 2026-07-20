# Oracle Cloud Agent Plugins

`OCINodeClass.spec.agentPlugins` enables Oracle Cloud Agent plugins when Karpenter Provider OCI (KPO) launches an instance. Each item is the exact plugin name returned by OCI `ListInstanceagentAvailablePlugins` for the selected image operating system and version.

```yaml
apiVersion: oci.oraclecloud.com/v1beta1
kind: OCINodeClass
metadata:
  name: monitored-workers
spec:
  agentPlugins:
    - Bastion
    - Compute Instance Monitoring
    - Block Volume Management
    - OS Management Hub Agent
```

KPO sends every configured name to OCI with `desiredState: ENABLED`. An omitted or empty list leaves `AgentConfig` unspecified, so OCI and the selected image retain their defaults for every plugin.

## Known use cases

| Plugin | Primary use case | Key prerequisites | OCI documentation |
| --- | --- | --- | --- |
| `Bastion` | Managed SSH sessions to worker nodes. | Linux platform image, OpenSSH, Oracle Cloud Agent, Bastion resource, IAM, a VCN gateway and route, and network rules allowing Bastion access to the node. | [Bastion overview](https://docs.oracle.com/en-us/iaas/Content/Bastion/Concepts/bastionoverview.htm) |
| `Block Volume Management` | Multipath Ultra High Performance iSCSI attachments and agent-managed iSCSI login. | Supported image and Oracle Cloud Agent; automatic iSCSI connection requires OCA 1.23.0+ on Oracle Linux, 1.24.0+ on Windows, or 1.35.0+ on Ubuntu; public IP or service gateway; instance dynamic group with `instances` and `volume-attachments` permissions. | [Block Volume Management](https://docs.oracle.com/en-us/iaas/Content/Block/Tasks/enablingblockvolumemanagementplugin.htm) |
| `Cloud Guard Workload Protection` | Instance Security agent management for Cloud Guard. | Cloud Guard onboarding and agent-installation setup; custom images without Oracle Cloud Agent require manual agent management. | [Instance Security guidance](https://docs.oracle.com/en-us/iaas/Content/cloud-guard/using/cgis-update-agent.htm) |
| `Compute Instance Monitoring` | Publish compute health, capacity, and performance metrics. | Supported current platform or compatible custom image with Oracle Cloud Agent; public IP or service-gateway access to OCI Monitoring. | [Compute monitoring](https://docs.oracle.com/en-us/iaas/Content/Compute/Tasks/enablingmonitoring.htm) |
| `Compute Instance Run Command` | Run remote management scripts. | Supported Oracle Linux, Autonomous Linux, CentOS, or Windows image; IAM and instance dynamic-group permissions; `ocarun` administrator privileges for elevated commands. | [Run Command requirements](https://docs.oracle.com/en-us/iaas/Content/Compute/Tasks/runningcommands.htm) |
| `Compute HPC RDMA Authentication` | Configure and maintain RDMA/RoCE network-interface authentication. | OCA 1.35.0+ on a compatible HPC image; NVIDIA GPU and Mellanox OFED drivers; for migration, remove existing OCI HPC packages before enabling the plugin. | [HPC plugin guidance](https://docs.oracle.com/en-us/iaas/Content/Compute/Tasks/transitioning-to-hpc-plugin.htm) |
| `Compute HPC RDMA Auto-Configuration` | Configure RDMA interface addresses, Mellanox firmware and PCIe settings, and legacy MPI DAPL configuration. | OCA 1.35.0+ on a compatible HPC image; NVIDIA GPU and Mellanox OFED drivers; for migration, remove existing OCI HPC packages before enabling the plugin. | [HPC plugin guidance](https://docs.oracle.com/en-us/iaas/Content/Compute/Tasks/transitioning-to-hpc-plugin.htm) |
| `Compute RDMA GPU Monitoring` | Publish GPU and RDMA metrics from HPC and GPU nodes. | OCA 1.35.0+, NVIDIA GPU and Mellanox OFED drivers, public internet path or service gateway, instance dynamic group, and Monitoring metric permissions; custom metric namespaces are billable. | [GPU and RDMA monitoring](https://docs.oracle.com/en-us/iaas/Content/Compute/Tasks/transitioning-to-hpc-plugin.htm) |
| `Custom Logs Monitoring` | Send application and system logs to OCI Logging. | Log and agent configuration; outbound HTTPS access to OCI authentication and Logging ingestion endpoints. | [Custom Logs requirements](https://docs.oracle.com/en-us/iaas/Content/Logging/Concepts/custom_logs.htm) |
| `Fleet Application Management` | Discover and patch supported products. | Service onboarding, IAM and dynamic-group setup, OS Management Hub when required, and managed-product prerequisites. | [Fleet Application Management requirements](https://docs.oracle.com/en-us/iaas/Content/fleet-management/requirements-access.htm) |
| `High Performance Computing` | Configure and authenticate HPC bare metal network workloads. | Supported HPC shape and image; OCI recommends or enables the plugin from OCA 1.37.0 for listed shapes. | [HPC requirements](https://docs.oracle.com/en-us/iaas/Content/Compute/References/high-performance-compute.htm) |
| `Management Agent` | Collect management data from Linux compute instances. | Management Agent service OS, network, disk, Java, IAM, and installation prerequisites. | [Management Agent deployment](https://docs.oracle.com/en-us/iaas/management-agents/doc/management-agents-oracle-cloud-agent.html) |
| `Oracle Java Management Service` | Monitor Java deployments. | OCA JMS plugin is currently available only on Oracle Linux; other supported operating systems use Oracle Management Agent; 300 MiB disk and 500 MiB memory. | [JMS system requirements](https://docs.oracle.com/en-us/iaas/jms/doc/system-requirements.html) |
| `OS Management Hub Agent` | Register nodes with OS Management Hub. | Supported operating system, OCA 1.40+, service IAM policies, public IP or service gateway, and compatible registration profile. | [OS Management Hub setup](https://docs.oracle.com/en-us/iaas/osmh/doc/getstarted.htm) |
| `Vulnerability Scanning` | Run agent-based host vulnerability scans. | Supported platform image, scan recipes and targets, Vulnerability Scanning IAM permissions, and service gateway for private-subnet nodes. | [Vulnerability Scanning requirements](https://docs.oracle.com/en-us/iaas/Content/scanning/using/overview.htm) |

## Prerequisites and operation

Plugin availability depends on the selected image and Oracle Cloud Agent version. Before enabling a plugin, query OCI for the available plugins for the image operating system and version, and configure any plugin-specific IAM, dynamic-group, network, or service prerequisites. KPO does not validate image compatibility or report whether a plugin is healthy after launch.

`agentPlugins` is launch-time configuration. Updating the list changes the OCINodeClass static hash, so Karpenter replaces affected nodes through the normal disruption workflow; it does not update agent plugins on running instances. Ordering has no intended semantic meaning.

For OCI plugin management and troubleshooting, see [Managing Plugins with Oracle Cloud Agent](https://docs.oracle.com/en-us/iaas/Content/Compute/Tasks/manage-plugins.htm).
