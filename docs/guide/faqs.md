# FAQs

### Do special taints added by OKE bootstrapping in specific scenarios apply to Karpenter managed nodes?

Yes—there are a few special scenarios:

#### Spot / preemptible nodes

- Spot instances have the `oci.oraclecloud.com/oke-is-preemptible` taint applied.
- Customers must **explicitly add this taint to the Karpenter NodePool**. If this taint is not configured on the NodePool, Karpenter will **not** offer `spot` compute shapes.
- Workloads intended to run on these nodes must include the corresponding toleration(s) so they can be scheduled.

Example taint to add to the NodePool:

```yaml
taints:
  - effect: NoSchedule
    key: oci.oraclecloud.com/oke-is-preemptible
    value: present
```
#### GPU shapes

- GPU shapes have taints based on the GPU type:
  - NVIDIA: `nvidia.com/gpu`
  - AMD: `amd.com/gpu`
- Add the relevant taint(s) to your Karpenter `NodePool` and add matching tolerations to workloads that should run on GPU nodes.

Example NVIDIA taint to add to the NodePool:

```yaml
taints:
  - effect: NoSchedule
    key: nvidia.com/gpu
    value: present
```

Example AMD taint to add to the NodePool:

```yaml
taints:
  - effect: NoSchedule
    key: amd.com/gpu
    value: present
```
      
### How to configure Secondary VNICs?
OciVcnIpNative CNI versions before 3.2.0 each secondary VNIC supports a maximum of 16 IP addresses. OciVcnIpNative CNI version 3.2.0 or after this limit has been increased to 256 IP addresses per secondary VNIC. It is highly recommended to use add-on version **3.2.0** or later. If an earlier add-on version (prior to **3.2.0**) must be used, it is required that the secondary VNIC `ipCount` is explicitly configured to be no greater than 16.

In addition to the maximum number of supported IP addresses, secondary VNICs are subject to the following restrictions:
- The number of assigned IP addresses must be a power of two.
- For IPv6-only (single stack) secondary VNICs, only 1, 16, or 256 assigned IP addresses are supported.
- The aggregate total of all assigned IP addresses across all secondary VNICs within a node must not exceed 256.
- If secondary VNIC IP count is not set, it defaults to 32 IPs for IPv4 or IPv6 dual stack cluster, and 256 IPs for IPv6 single stack cluster.

In the current implementation of Karpenter, there are specific limitations regarding the subnet CIDR configuration for secondary VNICs. The following guidelines apply:
- It is recommended to configure two CIDR blocks for the pod subnet used by secondary VNICs.
- If configuring a subnet with a single CIDR block, ensure that the subnet has a sufficient number of contiguous IP addresses to accommodate the required number of IP assignments.

Care should be taken to plan the subnet configuration to avoid address exhaustion and ensure reliable secondary VNIC operation. Enhancements to relax certain CIDR-related restrictions are planned for future releases.

### How can I ensure deployments are scheduled on a specific karpenter node pool?

Use [node affinity](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#node-affinity) or [nodeSelector](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#nodeselector) targeting the node pool label (for Karpenter, typically karpenter.sh/nodepool).
- **Using Node Affinity**:
  ```yaml
  affinity:
    nodeAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
        nodeSelectorTerms:
        - matchExpressions:
          - key: karpenter.sh/nodepool
            operator: In
            values:
              - <NODEPOOL-NAME> 
  ```
- **Using Node Selectors:**
  ```yaml
   nodeSelector:
     karpenter.sh/nodepool: <NODEPOOL-NAME>
  ```

### How can I ensure workloads are evenly scheduled across ADs and FDs with Karpenter Provider OCI?

Karpenter Provider OCI respects pod-level `topologySpreadConstraints` when the `topologyKey` matches labels that KPO supports during scheduling. The most common OCI-related topology keys are:

- `topology.kubernetes.io/zone` to spread across availability domains (ADs)
- `oci.oraclecloud.com/fault-domain` to spread across fault domains (FDs) within an AD

For Karpenter-managed capacity, the workload should still target the intended node pool using `karpenter.sh/nodepool`, and the selected `NodePool` must allow the ADs or FDs that you want the scheduler to use for spreading. When `whenUnsatisfiable: DoNotSchedule` is used, unschedulable pods remain pending, which gives Karpenter an opportunity to provision nodes that satisfy the spread requirement.

Recommendations:

- Use `replicas` equal to or greater than the number of topology domains you want to spread across. 
- Use AD spread when the workload should remain available across multiple availability domains.
- Use FD spread in a single-AD region.
- Make sure the `NodePool` requirements include all topology values you expect `topologySpreadConstraints` to use.

Example: configure a `NodePool` so Karpenter can launch nodes in three ADs:

```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: example-ad-nodepool
spec:
  template:
    spec:
      requirements:
      - key: topology.kubernetes.io/zone
        operator: In
        values:
        - <AVAILABILITY_DOMAIN_1>
        - <AVAILABILITY_DOMAIN_2>
        - <AVAILABILITY_DOMAIN_3>
```

Then spread a `Deployment` across those three ADs:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: example-ad-spread
spec:
  replicas: 3
  selector:
    matchLabels:
      app: example-ad-spread
  template:
    metadata:
      labels:
        app: example-ad-spread
    spec:
      affinity:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
            - matchExpressions:
              - key: karpenter.sh/nodepool
                operator: In
                values:
                - example-ad-nodepool
      topologySpreadConstraints:
      - maxSkew: 1
        minDomains: 3
        topologyKey: topology.kubernetes.io/zone
        whenUnsatisfiable: DoNotSchedule
        labelSelector:
          matchLabels:
            app: example-ad-spread
        matchLabelKeys:
        - pod-template-hash
        nodeAffinityPolicy: Honor
        nodeTaintsPolicy: Honor
      containers:
      - name: app
        image: registry.k8s.io/pause:3.9
        imagePullPolicy: IfNotPresent
        resources:
          requests:
            cpu: "1"
```

Example: configure a `NodePool` so Karpenter can launch nodes across three fault domains in a single-AD region:

```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: example-fd-nodepool
spec:
  template:
    spec:
      requirements:
      - key: topology.kubernetes.io/zone
        operator: In
        values:
        - <AVAILABILITY_DOMAIN>
      - key: oci.oraclecloud.com/fault-domain
        operator: In
        values:
        - FAULT-DOMAIN-1
        - FAULT-DOMAIN-2
        - FAULT-DOMAIN-3
```

Then spread replicas evenly across those three fault domains:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: example-fd-spread
spec:
  replicas: 3
  selector:
    matchLabels:
      app: example-fd-spread
  template:
    metadata:
      labels:
        app: example-fd-spread
    spec:
      affinity:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
            - matchExpressions:
              - key: karpenter.sh/nodepool
                operator: In
                values:
                - example-fd-nodepool
      topologySpreadConstraints:
      - maxSkew: 1
        minDomains: 3
        topologyKey: oci.oraclecloud.com/fault-domain
        whenUnsatisfiable: DoNotSchedule
        labelSelector:
          matchLabels:
            app: example-fd-spread
        matchLabelKeys:
        - pod-template-hash
        nodeAffinityPolicy: Honor
        nodeTaintsPolicy: Honor
      containers:
      - name: app
        image: registry.k8s.io/pause:3.9
        imagePullPolicy: IfNotPresent
        resources:
          requests:
            cpu: "1"
```

Notes:

- `DoNotSchedule` is recommended when you want Karpenter to provision capacity that satisfies the spread rule.
- Set `replicas` high enough for the number of ADs or FDs you expect the workload to use.
- The `labelSelector` must match the pod labels, or the scheduler will not calculate skew against the intended pod set.
- Supported scheduling labels are documented in [Scheduling Labels](scheduling-labels.md).

### How can Karpenter fall back when an OCI service limit or compartment quota is exceeded?

By default, an OCI `LimitExceeded` or `QuotaExceeded` launch failure is returned immediately. To have Karpenter temporarily treat the failed offering as unavailable and try another compatible offering or NodePool, enable the Helm setting:

```yaml
settings:
  enableUnavailableOfferingsOnServiceLimitExceeded: true
```

The unavailable-offerings cache is enabled by default for 180 seconds. Keep `settings.unavailableOfferingsTTLSeconds` greater than zero for cache-based fallback; adjust it if you want failed offerings retried sooner or later. This setting does not increase OCI limits or quotas—review and raise the applicable limit or quota when necessary.
  
### How can I list OKE images compatible with my OKE cluster?

Use OCI CLI to get node pool options and filter images:

```shell
REGION="<region>"                    # e.g. us-phoenix-1
CLUSTER_OCID="<oke-cluster-ocid>"
OKE_VERSION="1.31"                   # match minor (e.g., 1.31, 1.32, 1.33 ...)
OS_MAJOR="8"                         # optional
EXCLUDE_PATTERN="aarch64|arm64|GPU"  # optional


oci ce node-pool-options get --region "${REGION}" --node-pool-option-id "${CLUSTER_OCID}" --output json | jq -r --arg ver "${OKE_VERSION:-}" --arg os "${OS_MAJOR:-}" --arg ex "${EXCLUDE_PATTERN:-}" '.data.sources[] | . as $src | ($src["source-name"] // "") as $name | select( ($ver == "" or ($name | test($ver))) and ($os == "" or ($name | test($os; "i"))) and ($ex == "" or ($name | test($ex; "i") | not)) ) | {id: $src["image-id"], source_name: $name}'
```

### Flexible shape node pool isn’t provisioning. Error: “skipping, nodepool requirements filtered out all instance types.” How do I fix this?
Set a valid `shapeConfigs` in your `OCINodeClass` for flexible shapes (and/or define global defaults via Helm values).
- **OCINodeClass**
    ```yaml
    apiVersion: oci.oraclecloud.com/v1beta1
    kind: OCINodeClass
    metadata:
      name: example-nodeclass
    spec:
      shapeConfigs:
        - ocpus: 2
          memoryInGbs: 16
          baselineOcpuUtilization: BASELINE_1_2
    ```
  
- **Helm values**

  If you prefer to set default `shapeConfigs` values globally, you can define them in the Helm chart values under `settings.flexibleShapeConfigs` (list). You can override the defaults by specifying `shapeConfigs` in `OCINodeClass`.
  ```yaml
     settings:
        flexibleShapeConfigs:
           - ocpus: 2
             memoryInGbs: 16
  ```

If you’re using a Capacity Reservation and facing this issue, confirm your `shapeConfigs` (or `flexibleShapeConfigs` for global config) matches the reservation exactly—`ocpus`, `memoryInGbs`, and `baselineOcpuUtilization`.

### How to run the OCI Karpenter controller with debug logging?
When deploying the OCI Karpenter provider using Helm, specify the logLevel parameter in your values.yaml file or directly in the command line. For example:
```shell
logLevel: debug
```
If you want to run OCI GO SDK in debug mode (Karpenter uses OCI GO SDK to interact with OCI) please edit Karpenter deployment and set `OCI_GO_SDK_DEBUG=1` as an environment variable to the deployment.

### Suggestions regarding KubeletConfig maxPods and podsPerCore
The value assigned to "podsPerCore" must not exceed the "maxPods" value. Additionally, for environments in which the customer is utilizing an OciVcnIpNative cluster, the "maxPods" value should be less than the aggregate sum of "IpCount" from the secondary VNICs.
