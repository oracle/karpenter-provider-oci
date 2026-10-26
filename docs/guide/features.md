# Features

- Implements the standard Karpenter cloud-provider interface (aligned with upstream **Karpenter v1.15.1**), enabling dynamic node scaling to meet workload scheduling demands and optimization requirements.
- Supports drift detection to keep nodes launched by KPO up-to-date.
- Provides opt-in [legacy node repair](https://karpenter.sh/v1.15/concepts/disruption/#legacy-node-repair) with configurable condition-based replacement policies. The `NodeRepair` feature gate is disabled by default; `settings.legacyNodeRepair: true` selects the legacy controller without enabling repair.
- Supports opt-in pod deletion-cost management through `settings.featureGates.podDeletionCostManagement`, disabled by default.
- Supports upstream scheduler configuration through `settings.schedulerConfig`; see [Mirror cluster scheduler defaults](faqs.md#mirror-cluster-scheduler-defaults).
- Provides an optional, configurable client-side [OCI API rate limiter](rate-limiter.md) with separate controls for read and write requests.
- Supports upstream `NodeOverlay` when `settings.featureGates.nodeOverlay=true`, allowing operators to add scheduling-time capacity overlays or price adjustments for selected instance types and node pools.
- Supports the upstream `CapacityBuffer` API when `settings.featureGates.capacityBuffer=true`, enabling proactive provisioning and replenishment of spare node capacity.
- Supports upstream static node pools when `settings.featureGates.staticCapacity=true`, enabling fixed-replica `NodePool.spec.replicas` workflows in addition to demand-driven provisioning.
- Supports a wide range of OCI cloud-specific features through `OCINodeClass`, including:
    - Generic support of OCI compute VM and BM shapes and corresponding special scheduling labels
    - Multiple shape configs based on a flexible shape
    - Burstable instances
    - Preemptible instances
    - OKE prebaked images (Oracle Linux and Ubuntu)
    - Boot volume customizations including KMS key, size, VPU, `pvEncryptionInTransit`
    - IPv4 & IPv6 support on worker node VNICs
    - Network security group support on worker node VNICs
    - Secondary VNIC configurations (OciIpNativeCNI cluster only)
    - Capacity reservation
    - Cluster placement group
    - Compute cluster
    - Instance configurations such as metadata, freeform & defined tags, launch options, SSH authorized keys, etc.
    - Kubelet configuration customizations
    - Ability to auto-detect latest compatible OKE image with image filter

## Unsupported upstream alpha features

KPO does not support the following [upstream disruption features](https://karpenter.sh/v1.15/concepts/disruption/):

- Voluntary node repair (node repair through the disruption controller).
- Terminate-first drift.
- Terminate-first repair.
- In-place reboot and reboot-based repair (`RebootNode`).

Keep `settings.legacyNodeRepair: true`, the Helm default. KPO rejects the non-legacy repair mode and enabled `TerminateFirstDrift` or `TerminateFirstRepair` gates at startup.
