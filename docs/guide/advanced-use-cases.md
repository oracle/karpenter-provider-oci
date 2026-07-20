# Advanced Use Cases

### Oracle Cloud Agent Plugins

For Oracle Cloud Agent plugin use cases, prerequisites, and configuration examples, see [Oracle Cloud Agent plugins](agent-plugins.md).

### Spot capacity (OCI preemptible)

Configure the `NodePool` capacity type to `spot`:

```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: my-nodepool
spec:
  template:
    spec:
      requirements:
        - key: karpenter.sh/capacity-type
          operator: In
          values: ["spot"]
      taints:
        - effect: NoSchedule
          key: oci.oraclecloud.com/oke-is-preemptible
          value: present
```

KPO maps the Karpenter `spot` capacity type to OCI **preemptible** instances. For details, see [OCI Preemptible Instances](https://docs.oracle.com/en-us/iaas/Content/Compute/Concepts/preemptible.htm).

### Reserved capacity (OCI capacity reservations)

1. Configure the `NodePool` capacity type to `reserved`:

```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: my-nodepool
spec:
  template:
    spec:
      requirements:
        - key: karpenter.sh/capacity-type
          operator: In
          values: ["reserved"]
```

2. Configure `capacityReservationConfigs` in the `OCINodeClass`:

```yaml
apiVersion: oci.oraclecloud.com/v1beta1
kind: OCINodeClass
metadata:
  name: my-ocinodeclass
spec:
  capacityReservationConfigs:
    - capacityReservationId: <capacity-reservation-ocid-1>
    - capacityReservationId: <capacity-reservation-ocid-2>
```

KPO maps the Karpenter `reserved` capacity type to OCI **capacity reservations**. For details, see [OCI Capacity Reservations](https://docs.oracle.com/en-us/iaas/Content/Compute/Tasks/reserve-capacity.htm).

### Burstable instances (flex shapes)

Configure `baselineOcpuUtilization` in `shapeConfigs`:

```yaml
apiVersion: oci.oraclecloud.com/v1beta1
kind: OCINodeClass
metadata:
  name: my-ocinodeclass
spec:
  shapeConfigs:
    - ocpus: 2
      memoryInGbs: 16
      baselineOcpuUtilization: BASELINE_1_2
```

For details, see [OCI Burstable Instances](https://docs.oracle.com/en-us/iaas/Content/Compute/References/burstable-instances.htm).

### Cluster placement groups

Configure `clusterPlacementGroupConfigs` in the `OCINodeClass`:

```yaml
apiVersion: oci.oraclecloud.com/v1beta1
kind: OCINodeClass
metadata:
  name: my-ocinodeclass
spec:
  clusterPlacementGroupConfigs:
    - clusterPlacementGroupId: <cluster-placement-group-ocid-1>
    - clusterPlacementGroupId: <cluster-placement-group-ocid-2>
```

For details, see [OCI Cluster Placement Groups](https://docs.oracle.com/en-us/iaas/Content/cluster-placement-groups/overview.htm).

### Compute clusters

Configure `computeClusterConfig` in the `OCINodeClass` (immutable after creation):

```yaml
apiVersion: oci.oraclecloud.com/v1beta1
kind: OCINodeClass
metadata:
  name: my-ocinodeclass
spec:
  computeClusterConfig:
    computeClusterId: <compute-cluster-ocid>
```

For details, see [OCI Compute Clusters](https://docs.oracle.com/en-us/iaas/Content/Compute/Tasks/compute-clusters.htm).

### Customize kubelet

Configure `kubeletConfig` in `OCINodeClass.spec`. If a kubelet field is not available (or you want to pass a raw flag), use `extraArgs`.

See the generated reference: [OCI v1beta1 API](../reference/v1beta1-api-raw.md).

### Customize compute instances

Use standard OCI instance settings via `OCINodeClass.spec` (examples): `nodeCompartmentId`, `metadata`, `freeformTags`, `definedTags`, `sshAuthorizedKeys`, `launchOptions`.

For details, see [OCI Launching Instances](https://docs.oracle.com/en-us/iaas/Content/Compute/Tasks/launchinginstance.htm).

### Customize node bootstrap (cloud-init)

You can inject custom cloud-init behavior through `OCINodeClass` during node launch. This is useful for installing packages, configuring the OS, or adding pre/post bootstrap hooks.

#### Option 1 — `preBootstrapInitScript` / `postBootstrapInitScript` (recommended)

Use base64-encoded scripts that run before/after OKE bootstrap:

1. Write the pre/post scripts you want to run.
2. Base64-encode them.
3. Set them in `OCINodeClass.spec`.

```yaml
apiVersion: oci.oraclecloud.com/v1beta1
kind: OCINodeClass
metadata:
  name: my-ocinodeclass
spec:
  preBootstrapInitScript: "IyEvYmluL2Jhc2gKZWNobyAiSSBhbSBhIHByZSBib290c3RyYXAgc2NyaXB0Ig=="
  postBootstrapInitScript: "IyEvYmluL2Jhc2gKZWNobyAiSSBhbSBhIHBvc3QgYm9vdHN0cmFwIHNjcmlwdCI="
```

#### Option 2 — `metadata.user_data` (advanced)

Provide a full cloud-init payload (base64 encoded) via `metadata.user_data`. This gives full control but you must ensure the script still runs `oke bootstrap` (and sets any required environment variables for bootstrap).

Example:

```yaml
apiVersion: oci.oraclecloud.com/v1beta1
kind: OCINodeClass
metadata:
  name: my-ocinodeclass
spec:
  metadata:
    user_data: "IyEvdXNyL2Jpbi9lbnYgYmFzaAoKc2V0IC1vIGVycmV4aXQKc2V0IC1vIG5vdW5zZXQKc2V0IC1vIHBpcGVmYWlsCg=="
```

For a reference script, see `samples/custom_cloud_init.sh`. When using that script, only edit the sections between:
- `# BEGIN OF CUSTOM SCRIPT BOOTSTRAP SCRIPT ...` and `# END OF CUSTOM SCRIPT BOOTSTRAP SCRIPT`
- `# BEGIN OF POST BOOTSTRAP SCRIPT, IF NEEDED` and `# END OF POST BOOTSTRAP SCRIPT`

Important considerations:
- Ensure scripts are base64-encoded when set in `preBootstrapInitScript`, `postBootstrapInitScript`, or `metadata.user_data`.
- If you provide your own `user_data`, ensure it remains compatible with OKE bootstrap (including calling `oke bootstrap`).
- When using `metadata.user_data`, integrate custom logic carefully so it doesn’t break the default cloud-init/OKE bootstrap flow.
- Test custom scripts in a non-production environment before rollout.
- The parameters needed to bootstrap nodes are available via the Instance Metadata Service (IMDS). In custom scripts, ensure you set values such as `CLUSTER_DNS`, `KUBELET_EXTRA_ARGS`, `APISERVER_ENDPOINT`, and `KUBELET_CA_CERT` (as shown in the reference script).

### Ubuntu images

Ubuntu image support in KPO is currently limited to OKE pre-baked Ubuntu images. Ubuntu nodes also require a cloud-init snippet that runs `oke bootstrap`.

Ubuntu image support in KPO is currently LA because OKE’s Ubuntu image support is also LA. Since KPO relies on OKE for node bootstrapping, Ubuntu support in KPO will become GA once OKE Ubuntu images are GA. At this time, KPO supports only OKE pre-baked Ubuntu images.

Prerequisites:
- You may need an additional IAM policy in your tenancy to allow KPO worker nodes to read Ubuntu pre-baked images. Adapt this to your environment and workload identity constraints.
- Ubuntu nodes require cloud-init that includes `oke bootstrap`.

Example IAM policy (replace the tenancy OCID and review against your environment):

```shell
Define tenancy oke as ocid1.tenancy.oc1..aaaaaaaa5vrtu4bjcqpjvbworiwffgccrgrbkum64mtn33yrccjrqpzuyara
Endorse any-user to read instance-images in tenancy oke
```

Ubuntu nodes currently require custom cloud-init to complete bootstrap automatically:

```yaml
#cloud-config
runcmd:
- oke bootstrap
```

1. Find an Ubuntu image OCID (compartment depends on where your OKE Ubuntu images are published):

```shell
UBUNTU_IMAGE_COMPARTMENT_OCID="<ubuntu-image-compartment-ocid>"
oci compute image list --compartment-id "${UBUNTU_IMAGE_COMPARTMENT_OCID}" --all \
  --query 'data[].{id:id,displayName:"display-name",os:"operating-system"}'
```

Example command/output:

```shell
oci compute image list --compartment-id ocid1.compartment.oc1..aaaaaaaawapv5zqax243hxuvi5xs6ekpsntos2ylg2xyx6qnncctcab53hya --all --query 'data[?"operating-system"==`Custom`].{id:id,displayName:"display-name"}'
[
  {
    "displayName": "ubuntu-arm64-minimal-24.04-noble-v20250604.1-OKE-1.32.1",
    "id": "ocid1.image.oc1.phx.aaaaaaaazm2rgqjojayx6am3ppvd7bm7jymxoldxxibl23pn77yjk576sqoa"
  },
  {
    "displayName": "ubuntu-arm64-minimal-24.04-noble-v20250604.1-OKE-1.31.1",
    "id": "ocid1.image.oc1.phx.aaaaaaaaoc33obmcchh53csxzakysygsvkpkdfindjuitog6d4y37ufwqe3a"
  },
  {
    "displayName": "ubuntu-arm64-minimal-22.04-jammy-v20250604.1-OKE-1.32.1",
    "id": "ocid1.image.oc1.phx.aaaaaaaa2vbvzj77dqj23ps2awezvccgyhfftyljhdlcdjrxbea7ttp3aiba"
  },
  {
    "displayName": "ubuntu-arm64-minimal-22.04-jammy-v20250604.1-OKE-1.31.1",
    "id": "ocid1.image.oc1.phx.aaaaaaaa3wh4wdn22lkwqgi2uw73ol2v3xde7xplcjofbnkgmkefrti56mra"
  },
  {
    "displayName": "ubuntu-amd64-minimal-24.04-noble-v20250604.1-OKE-1.32.1",
    "id": "ocid1.image.oc1.phx.aaaaaaaasugkwcjkxljmp4jzhah4yvehz7fjl3m26o4dftmnlb7rcabvvf6a"
  },
  {
    "displayName": "ubuntu-amd64-minimal-24.04-noble-v20250604.1-OKE-1.31.1",
    "id": "ocid1.image.oc1.phx.aaaaaaaaeicyaibcoemlii7a2pc3cki5xzw27s7rm43c557jsjuskbzlhczq"
  },
  {
    "displayName": "ubuntu-amd64-minimal-22.04-jammy-v20250604.1-OKE-1.32.1",
    "id": "ocid1.image.oc1.phx.aaaaaaaauyns5pryycvwhqdge7p6hdhpiphvu4z3io5tzdzwe2jzj4qmon2q"
  },
  {
    "displayName": "ubuntu-amd64-minimal-22.04-jammy-v20250604.1-OKE-1.31.1",
    "id": "ocid1.image.oc1.phx.aaaaaaaajqzb5jcbcvh5obyg2l2hzzw5qewwhrknvlyb3zaglduivigvo4sq"
  }
]
```

2. Create an `OCINodeClass` that uses the Ubuntu image OCID and sets `metadata.user_data` to a base64-encoded cloud-init script containing `oke bootstrap`:

```yaml
apiVersion: oci.oraclecloud.com/v1beta1
kind: OCINodeClass
metadata:
  name: ubuntu-ocinodeclass
spec:
  volumeConfig:
    bootVolumeConfig:
      imageConfig:
        imageType: OKEImage
        imageId: <ubuntu-image-ocid>
  networkConfig:
    primaryVnicConfig:
      subnetConfig:
        subnetId: <subnet-ocid>
  metadata:
    # base64("#cloud-config\nruncmd:\n- oke bootstrap\n")
    user_data: "I2Nsb3VkLWNvbmZpZwpydW5jbWQ6Ci0gb2tlIGJvb3RzdHJhcA=="
```

`metadata.user_data` is required for Ubuntu nodes and must be base64-encoded.

If you need extra steps before/after bootstrap, add them before and/or after `oke bootstrap`, then base64-encode the updated cloud-init and set it as `metadata.user_data`.

Example:

```yaml
#cloud-config
runcmd:
  - echo "pre bootstrap"
  - oke bootstrap
  - echo "post bootstrap"
```

Known limitation: `imageFilter` does not currently work for Ubuntu images; reference Ubuntu images by OCID (`imageId`).

For more advanced cloud-init customization, refer to:
- https://documentation.ubuntu.com/oracle/oracle-how-to/deploy-ubuntu-oke-nodes-using-cli/
- https://docs.cloud-init.io/en/latest/reference/examples.html

## Override the OKE image compartment used by `imageFilter`

When you set `imageType: OKEImage` with `imageFilter`, KPO searches a configured compartment for OKE pre-baked images. If you publish custom images based on OKE images to a different compartment and want `imageFilter` to discover them, you can configure where KPO searches. You can ignore this if you only use default OKE pre-baked images or if you reference images directly by OCID using `imageId`.

### Configure via Helm (global default)

Set the following Helm value:

```yaml
settings:
  preBakedImageCompartmentId: "<compartment-ocid-containing-oke-derived-images>"
```

This value is used as the default pre-baked image compartment for all `OCINodeClass` resources (unless overridden per resource).

If you set this in Helm values, it is used as `preBakedImageCompartmentId` for all `OCINodeClass` resources.

### Configure via `OCINodeClass` (per-resource override)

Override for a specific node class by setting `spec.volumeConfig.bootVolumeConfig.imageConfig.imageFilter.compartmentId`:

```yaml
volumeConfig:
  bootVolumeConfig:
    imageConfig:
      imageType: OKEImage
      imageFilter:
        compartmentId: "<compartment-ocid-containing-oke-derived-images>"
        osFilter: "Oracle Linux"
        osVersionFilter: "8"
```

Prerequisites:
- Know the compartment OCID that hosts your custom OKE-derived images.
- Ensure the KPO controller has permission to read images in that compartment (in addition to existing KPO IAM permissions). Example policy shape:

```shell
Allow any-user to read instance-images in compartment <compartment-name> where all {request.principal.type = 'workload', request.principal.service_account = 'karpenter'}
```

### `k8s_version` requirements for OKE-derived custom images

Custom images based on OKE images must have either:
- a `k8s_version` freeform tag, or
- a `BaseImageId` that points (directly or indirectly) to an ancestor OKE image that has the `k8s_version` tag.

If neither is present, KPO cannot determine the image’s Kubernetes/kubelet version and image selection may fail with `missing k8s_version tag`.
