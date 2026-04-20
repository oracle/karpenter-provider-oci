# Usage

Here are a few common use cases. For solutions addressing additional scenarios, refer to the [Advanced Use Cases](advanced-use-cases.md) or check the [OCINodeClass](ocinodeclass.md) specification.

---

## Use KPO to manage nodes with OCI flexible shapes and an OKE image

The YAML below creates a Karpenter `NodePool` that can launch nodes with one of `VM.Standard.E3.Flex`, `VM.Standard.E4.Flex`, `VM.Standard.E5.Flex` shapes. The `OCINodeClass` provides two `shapeConfigs` (`2 ocpus / 8 GiB` and `4 ocpus / 16 GiB`) and selects an OKE pre-baked image by OCID.

```yaml
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: my-nodepool
spec:
  template:
    spec:
      expireAfter: Never
      nodeClassRef:
        group: oci.oraclecloud.com
        kind: OCINodeClass
        name: my-ocinodeclass
      requirements:
        - key: karpenter.sh/capacity-type
          operator: In
          values:
            - on-demand
        - key: oci.oraclecloud.com/instance-shape # extend this list as needed
          operator: In
          values:
            - VM.Standard.E3.Flex
            - VM.Standard.E4.Flex
            - VM.Standard.E5.Flex
      terminationGracePeriod: 120m
  disruption:
    budgets:
      - nodes: 5%
    consolidateAfter: 60m
    consolidationPolicy: WhenEmpty
  limits:
    cpu: 64
    memory: 256Gi
---
apiVersion: oci.oraclecloud.com/v1beta1
kind: OCINodeClass
metadata:
  name: my-ocinodeclass
spec:
  shapeConfigs:
    - ocpus: 2
      memoryInGbs: 8
    - ocpus: 4
      memoryInGbs: 16
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

## Ensure worker nodes using an OKE image are always updated to the latest image

The sample OCINodeClass below specifies an image filter to select OKE images. The resolved image depends on the cluster's Kubernetes version and the available OKE images. When the cluster control plane is upgraded or new OKE images are released, the desired worker node image will also change—nodes launched with an outdated image will be considered as "Drifted". To minimize unexpected disruption during these events, it is recommended to configure an appropriate disruption budget in the Karpenter node pool, specifying reasons, disruption percentage, and schedule.

```yaml
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: my-nodepool
spec:
  template:
    spec:
      expireAfter: Never
      nodeClassRef:
        group: oci.oraclecloud.com
        kind: OCINodeClass
        name: my-ocinodeclass
      requirements:
        - key: karpenter.sh/capacity-type
          operator: In
          values:
            - on-demand
        - key: oci.oraclecloud.com/instance-shape # extend this list as needed
          operator: In
          values:
            - VM.Standard.E3.Flex
            - VM.Standard.E4.Flex
            - VM.Standard.E5.Flex
      terminationGracePeriod: 120m
  disruption:
    budgets:
      - nodes: 5%
        reasons:
          - Drifted
        schedule: "@daily" # customize for your needs (see https://karpenter.sh/docs/concepts/disruption/)
        duration: 10m
    consolidateAfter: 60m
    consolidationPolicy: WhenEmpty
  limits:
    cpu: 64
    memory: 256Gi
---
apiVersion: oci.oraclecloud.com/v1beta1
kind: OCINodeClass
metadata:
  name: my-ocinodeclass
spec:
  shapeConfigs:
    - ocpus: 2
      memoryInGbs: 8
    - ocpus: 4
      memoryInGbs: 16
  volumeConfig:
    bootVolumeConfig:
      imageConfig:
        imageType: OKEImage
        imageFilter:
          osFilter: "Oracle Linux"
          osVersionFilter: "8"  # see OCINodeClass docs for imageFilter behavior
  networkConfig:
    primaryVnicConfig:
      subnetConfig:
        subnetId: <subnet-ocid>
```

## Maintain a fixed number of worker nodes with static capacity

KPO supports upstream static node pools when `settings.featureGates.staticCapacity=true`. With this feature enabled, a `NodePool` can set `spec.replicas` so Karpenter keeps a fixed number of nodes available even when there are no pending pods. For the upstream behavior and API details, see the [Karpenter NodePools documentation](https://karpenter.sh/docs/concepts/nodepools/#specreplicas).

Enable the feature gate through Helm values:

```yaml
settings:
  featureGates:
    staticCapacity: true
```

The example below creates a static `NodePool` that keeps three on-demand worker nodes available at all times:

```yaml
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: static-workers
spec:
  replicas: 3
  template:
    spec:
      nodeClassRef:
        group: oci.oraclecloud.com
        kind: OCINodeClass
        name: static-workers-class
      requirements:
        - key: kubernetes.io/arch
          operator: In
          values:
            - arm64
        - key: kubernetes.io/os
          operator: In
          values:
            - linux
        - key: karpenter.sh/capacity-type
          operator: In
          values:
            - on-demand
        - key: oci.oraclecloud.com/instance-shape
          operator: In
          values:
            - VM.Standard.A1.Flex
---
apiVersion: oci.oraclecloud.com/v1beta1
kind: OCINodeClass
metadata:
  name: static-workers-class
spec:
  shapeConfigs:
    - ocpus: 2
      memoryInGbs: 12
  volumeConfig:
    bootVolumeConfig:
      imageConfig:
        imageType: OKEImage
        imageFilter:
          osFilter: "Oracle Linux"
          osVersionFilter: "8"
  networkConfig:
    primaryVnicConfig:
      subnetConfig:
        subnetId: <subnet-ocid>
```

When `spec.replicas` is set, keep these constraints in mind:

- Karpenter maintains the requested replica count instead of reacting only to pending pods.
- Once `spec.replicas` is set, the `NodePool` cannot be converted back to a dynamic node pool by removing it.
- `disruption.consolidationPolicy` and `disruption.consolidateAfter` are ignored.
- Only `limits.nodes` is supported; CPU or memory limits must not be set.
- `NodePool.spec.weight` is not supported on static node pools.
- Nodes in a static node pool are not considered for consolidation.
- Scaling operations bypass node disruption budgets, but still respect PodDisruptionBudgets.

## Influence scheduling decisions with `NodeOverlay`

KPO supports upstream `NodeOverlay` when `settings.featureGates.nodeOverlay=true`. A `NodeOverlay` lets you influence scheduling simulations for selected node pools or instance shapes by applying a price override, a price adjustment, or additional extended-resource capacity. For the upstream behavior and API details, see the [Karpenter NodeOverlays documentation](https://karpenter.sh/docs/concepts/nodeoverlays/).

Enable the feature gate through Helm values:

```yaml
settings:
  featureGates:
    nodeOverlay: true
```

The example below biases Karpenter toward `VM.Standard.E5.Flex` for one node pool by making that shape look 15% cheaper during scheduling:

```yaml
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: general-purpose
spec:
  template:
    spec:
      nodeClassRef:
        group: oci.oraclecloud.com
        kind: OCINodeClass
        name: general-purpose-class
      requirements:
        - key: karpenter.sh/capacity-type
          operator: In
          values:
            - on-demand
        - key: oci.oraclecloud.com/instance-shape
          operator: In
          values:
            - VM.Standard.E4.Flex
            - VM.Standard.E5.Flex
---
apiVersion: karpenter.sh/v1alpha1
kind: NodeOverlay
metadata:
  name: prefer-e5-flex
spec:
  requirements:
    - key: karpenter.sh/nodepool
      operator: In
      values:
        - general-purpose
    - key: oci.oraclecloud.com/instance-shape
      operator: In
      values:
        - VM.Standard.E5.Flex
  priceAdjustment: "-15%"
  weight: 100
---
apiVersion: oci.oraclecloud.com/v1beta1
kind: OCINodeClass
metadata:
  name: general-purpose-class
spec:
  shapeConfigs:
    - ocpus: 2
      memoryInGbs: 16
  volumeConfig:
    bootVolumeConfig:
      imageConfig:
        imageType: OKEImage
        imageFilter:
          osFilter: "Oracle Linux"
          osVersionFilter: "8"
  networkConfig:
    primaryVnicConfig:
      subnetConfig:
        subnetId: <subnet-ocid>
```

Notes for `NodeOverlay` usage:

- `spec.requirements` controls where the overlay applies. Matching can use labels such as `karpenter.sh/nodepool`, well-known Kubernetes labels, or custom labels added through `NodePool.spec.template.metadata.labels`.
- Use `price` to set an absolute simulated price, or `priceAdjustment` to apply a relative change. These fields are mutually exclusive.
- `spec.capacity` can add extended resources only; it cannot override standard resources such as CPU, memory, ephemeral storage, or pods.
- If multiple overlays match, higher `weight` wins. Overlays with the same weight are merged in alphabetical order.

## Launch worker nodes for an OciIpNativeCNI cluster

The sample `OCINodeClass` below includes a secondary VNIC configuration. In clusters using the OciIpNativeCNI add-on, worker nodes provisioned by Karpenter will attach a secondary VNIC. All pods will receive a VCN-routable IP address from the secondary VNIC’s subnet, and you can configure the number of allocated IP addresses as needed.
```yaml
---
apiVersion: oci.oraclecloud.com/v1beta1
kind: OCINodeClass
metadata:
  name: my-ocinodeclass
spec:
  shapeConfigs:
    - ocpus: 2  
      memoryInGbs: 8
    - ocpus: 4
      memoryInGbs: 16
  volumeConfig:
    bootVolumeConfig:
      imageConfig:
        imageType: OKEImage
        imageFilter: 
          osFilter: "Oracle Linux"
          osVersionFilter: "8"  # see OCINodeClass docs for imageFilter behavior
  networkConfig:
    primaryVnicConfig:
      subnetConfig:
        subnetId: <subnet-ocid>
    secondaryVnicConfigs:
      - subnetConfig:
          subnetId: <subnet-ocid>  # pod subnet
        ipCount: 16
```

For more examples, see [Advanced Use Cases](advanced-use-cases.md).
