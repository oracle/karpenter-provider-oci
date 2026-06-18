# OCINodeClass

`OCINodeClass` is the OCI-specific NodeClass used by Karpenter Provider OCI (KPO). It provides OCI launch configuration such as image selection, networking, boot volume settings, and optional features like capacity reservations and compute clusters.

For the full schema, see the generated reference: [OCI v1beta1 API](../reference/v1beta1-api-raw.md).

## Minimal example

```yaml
apiVersion: oci.oraclecloud.com/v1beta1
kind: OCINodeClass
metadata:
  name: my-ocinodeclass
spec:
  volumeConfig:
    bootVolumeConfig:
      imageConfig:
        imageType: OKEImage
        imageId: <oke-image-ocid>
  networkConfig:
    primaryVnicConfig:
      subnetConfig:
        subnetId: <subnet-ocid>
```

At minimum, every `OCINodeClass` must define:

- `spec.volumeConfig.bootVolumeConfig.imageConfig`
- `spec.networkConfig.primaryVnicConfig.subnetConfig`

KPO combines the `OCINodeClass` with a Karpenter `NodePool` during provisioning. The `NodePool` expresses scheduling intent, while the `OCINodeClass` supplies the OCI-specific launch details that KPO applies to the worker node.

## Common fields

- `spec.volumeConfig.bootVolumeConfig.imageConfig`
  - Use `imageId` to pin a specific image OCID.
  - Use `imageFilter` to let KPO select a compatible OKE image for the cluster (drift will occur when the desired image changes).
- `spec.networkConfig.primaryVnicConfig.subnetConfig`
  - Select the primary VNIC subnet via `subnetId` or `subnetFilter`.
- `spec.shapeConfigs`
  - Use this to enable flexible/burstable shapes and set `ocpus`/`memoryInGbs` on a per-`OCINodeClass` basis.
  - If you prefer a global default, you can also set `settings.flexibleShapeConfigs` in the Helm values and override it here when needed.
- `spec.kubeletConfig`
  - Configure kubelet limits and reservations (`maxPods`, `podsPerCore`, `systemReserved`, `kubeReserved`, etc.).
- `spec.agentList`
  - Optional list of Oracle Cloud Agent plugin names to enable on each launched instance (for example `Bastion`, `Block Volume Management`, `Compute Instance Monitoring`, `OS Management Service Agent`). Plugin names must match the values returned by the OCI `ListInstanceagentAvailablePlugins` API; each listed plugin is set to `ENABLED` at launch time, and plugins not listed retain their image default.

## Operator notes

- Flexible and burstable shapes require shape configuration. You can set it per OCINodeClass with `spec.shapeConfigs`, or define global defaults in the Helm chart with `settings.flexibleShapeConfigs`. If neither is set, flexible and burstable shapes are excluded from scheduling consideration.
- `spec.computeClusterConfig` is immutable after creation. Create a new `OCINodeClass` if you need to move a node class to a different compute cluster.
- `spec.capacityReservationConfigs`, `spec.clusterPlacementGroupConfigs`, and `spec.computeClusterConfig` are mutually exclusive. 
- Resource selector fields such as `subnetFilter`, `networkSecurityGroupFilter`, and reservation or placement-group filters must resolve to exactly one OCI resource.
- `spec.preBootstrapInitScript` and `spec.postBootstrapInitScript` must contain base64-encoded scripts.

## Status and troubleshooting

Use `status` and `conditions` to verify that KPO has resolved the OCI resources referenced by the `OCINodeClass`.

```shell
kubectl describe ocinodeclass <name>
```

Key areas to inspect in `status`:

- `status.conditions` for readiness and reconciliation errors
- `status.network` for resolved subnet, VNIC, and NSG details
- `status.volume` for resolved image, boot volume, and KMS details
- `status.capacityReservations`, `status.clusterPlacementGroups`, and `status.computeCluster` when optional placement features are configured

## Related

- Node selection and OCI labels: [Scheduling Labels](scheduling-labels.md)
- Example configs: [Usage](usage.md)
