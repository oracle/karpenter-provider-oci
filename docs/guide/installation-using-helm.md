# Install KPO Helm Chart

KPO releases publish the Helm chart to the project Helm repository. Release tags use a leading `v`, but Helm chart versions use SemVer without the `v` prefix.

KPO’s Helm chart supports a wide range of configuration. To view all available values:

```shell
helm repo add karpenter-provider-oci https://oracle.github.io/karpenter-provider-oci/charts
helm repo update karpenter-provider-oci
helm show values karpenter-provider-oci/karpenter --version <chart-version>
```

To list available chart versions:

```shell
helm search repo karpenter-provider-oci/karpenter --versions
```

Only `settings.clusterCompartmentId`, `settings.vcnCompartmentId`, and `settings.apiserverEndpoint` must be provided by the user.

Set `settings.ociVcnIpNative` to `true` only when the OKE cluster uses OCI VCN-native pod networking.

Configure `settings.ipFamilies` to match the IP family configuration of the OKE cluster. The default value is `IPv4` only. For dual-stack clusters, include `IPv6` in the list.

The chart already provides a default OKE image compartment for `settings.preBakedImageCompartmentId`. You only need to set it if you want to override that default. Please refer to [Override the OKE image compartment used by `imageFilter`](advanced-use-cases.md#override-the-oke-image-compartment-used-by-imagefilter) for additional details.

For all the chart values, see [Helm Chart Reference](../reference/helm-chart.md).

### Install

1. Decide the namespace where KPO will run.

2. Create a `values.yaml` containing the required values.

   Minimal example:

   ```yaml
   settings:
     clusterCompartmentId: "<your-cluster-compartment-ocid>"
     vcnCompartmentId: "<your-vcn-compartment-ocid>"
     apiserverEndpoint: "<api-server-endpoint-ip>"
     ociVcnIpNative: false

   # Optional: override image location/tag (example for OCIR)
   image:
     registry: "<registry>"
     repositoryName: "<your-ocir-namespace>/<your-repo>"
     tag: "<image-tag>"
   ```

3. Add the KPO Helm repository:

   ```shell
   helm repo add karpenter-provider-oci https://oracle.github.io/karpenter-provider-oci/charts
   helm repo update karpenter-provider-oci
   ```

4. Install:

   ```shell
   helm install karpenter karpenter-provider-oci/karpenter \
     --version <chart-version> \
     --values <path-to-values.yaml> \
     --namespace <karpenter-namespace> \
     --create-namespace
   ```

5. Verify:

   ```shell
   kubectl -n <karpenter-namespace> rollout status deploy/karpenter --timeout=120s
   kubectl -n <karpenter-namespace> get pods
   ```

## Upgrade KPO Helm Chart

```shell
helm repo update karpenter-provider-oci

helm upgrade karpenter karpenter-provider-oci/karpenter \
  --version <chart-version> \
  --namespace <karpenter-namespace> \
  --values <path-to-values.yaml>
```

## Uninstall KPO Helm Chart

```shell
helm uninstall karpenter --namespace <karpenter-namespace>
```
