# karpenter

![Version: 1.0.0](https://img.shields.io/badge/Version-1.0.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: v1.0.0](https://img.shields.io/badge/AppVersion-v1.0.0-informational?style=flat-square)

A Helm chart for Karpenter provider OCI

**Homepage:** <https://github.com/oracle/karpenter-provider-oci/>

## Source Code

* <https://github.com/oracle/karpenter-provider-oci/>

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| additionalAnnotations | object | `{}` | Additional annotations to add into metadata. |
| additionalClusterRoleRules | list | `[]` | Specifies additional rules for the core ClusterRole. |
| additionalLabels | object | `{}` | Additional labels to add into metadata. |
| affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0] | object | `{"key":"karpenter.sh/nodepool","operator":"DoesNotExist"}` | Default node affinity key used to avoid scheduling the controller onto Karpenter-managed nodes. |
| affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].operator | string | `"DoesNotExist"` | Node affinity operator for the default anti-self-scheduling rule. |
| affinity.podAntiAffinity.requiredDuringSchedulingIgnoredDuringExecution[0] | object | `{"topologyKey":"kubernetes.io/hostname"}` | Topology key used to spread controller pods across nodes. |
| controller.env | list | `[{"name":"OCI_RESOURCE_PRINCIPAL_VERSION","value":"2.2"},{"name":"OCI_REGION","value":""}]` | Additional environment variables for the controller pod. |
| controller.envFrom | list | `[]` | Additional environment sources for the controller pod, such as ConfigMaps or Secrets. |
| controller.healthProbe.port | int | `8081` | The container port to use for http health probe. |
| controller.metrics.port | int | `8000` | The container port to use for metrics. |
| controller.resources | object | `{"limits":{"cpu":"1","ephemeral-storage":"1Gi","memory":"1Gi"},"requests":{"cpu":"1","ephemeral-storage":"1Gi","memory":"1Gi"}}` | Resources for the controller pod. |
| controller.startupProbe.failureThreshold | int | `30` |  |
| controller.startupProbe.httpGet.path | string | `"/readyz"` |  |
| controller.startupProbe.httpGet.port | string | `"http"` |  |
| controller.startupProbe.periodSeconds | int | `5` |  |
| dnsConfig | object | `{}` | Configure DNS settings for the pod. |
| dnsPolicy | string | `"Default"` | Configure the DNS Policy for the pod |
| fullnameOverride | string | `""` | Overrides the chart's computed fullname. |
| hostNetwork | bool | `false` | Bind the pod to the host network. This is required when using a custom CNI. |
| image.pullPolicy | string | `"IfNotPresent"` | Container image pull policy. |
| image.registry | string | `"ghcr.io"` | Container image registry. |
| image.repositoryName | string | `"oracle/karpenter-provider-oci"` | Container image repository name. |
| imagePullPolicy | string | `"IfNotPresent"` | Image pull policy for Docker images. |
| imagePullSecrets | list | `[]` | Image pull secrets for Docker images. |
| livenessProbe.httpGet.path | string | `"/"` | HTTP path used by the liveness probe. |
| livenessProbe.httpGet.port | string | `"http"` | Named container port used by the liveness probe. |
| logErrorOutputPaths | list | `["stderr"]` | Log errorOutputPaths - defaults to stderr only |
| logLevel | string | `"info"` | Log level - defaults is INFO |
| logOutputPaths | list | `["stdout"]` | Log outputPaths - defaults to stdout only |
| nameOverride | string | `""` | Overrides the chart's name. |
| nodeSelector | object | `{"kubernetes.io/os":"linux"}` | Node selectors to schedule the pod to nodes with labels. |
| podAnnotations | object | `{}` | Additional annotations for the pod. |
| podDisruptionBudget.maxUnavailable | string | `"33%"` | Maximum percentage of controller pods that can be unavailable during a voluntary disruption. |
| podDisruptionBudget.name | string | `"karpenter"` | PodDisruptionBudget name. |
| podDisruptionBudget.unhealthyPodEvictionPolicy | string | `"AlwaysAllow"` | Controls whether unhealthy pods are considered for eviction during voluntary disruptions. |
| podLabels | object | `{}` | Additional labels for the pod. |
| podSecurityContext | object | `{}` | Pod-level security context. |
| priorityClassName | string | `"system-cluster-critical"` | PriorityClass name for the pod. |
| readinessProbe.httpGet.path | string | `"/"` | HTTP path used by the readiness probe. |
| readinessProbe.httpGet.port | string | `"http"` | Named container port used by the readiness probe. |
| replicaCount | int | `2` | Number of controller replicas. |
| resources | object | `{}` | Pod resource requests and limits. |
| revisionHistoryLimit | int | `10` | The number of old ReplicaSets to retain to allow rollback. |
| securityContext.allowPrivilegeEscalation | bool | `false` | Allow/Disallow privilege escalation for the controller container. |
| securityContext.capabilities.drop[0] | string | `"ALL"` | Linux capabilities to drop from the controller container. |
| securityContext.readOnlyRootFilesystem | bool | `true` | Mount the root filesystem as read-only. |
| securityContext.runAsNonRoot | bool | `true` | Run the container as a non-root user. |
| securityContext.runAsUser | int | `1000` | User ID to run the controller container as. |
| service.annotations | object | `{}` | Additional annotations for the Service. |
| serviceAccount.annotations | object | `{}` | Annotations to add to the service account. |
| serviceAccount.automount | bool | `true` | Specifies whether to automatically mount the service account token. |
| serviceAccount.create | bool | `true` | Specifies whether a service account should be created. |
| serviceAccount.name | string | `""` | The name of the service account to use. If empty and `create` is true, a name is generated from the fullname template. |
| serviceMonitor.additionalLabels | object | `{}` | Additional labels for the ServiceMonitor. |
| serviceMonitor.enabled | bool | `false` | Specifies whether a ServiceMonitor should be created. |
| serviceMonitor.endpointConfig | object | `{}` | Configuration on `http-metrics` endpoint for the ServiceMonitor. Not to be used to add additional endpoints. See the Prometheus operator documentation for configurable fields https://github.com/prometheus-operator/prometheus-operator/blob/main/Documentation/api.md#endpoint |
| settings.apiserverEndpoint | string | `""` | [required] API server endpoint(privateIP) for worker nodes to communicate with the Kubernetes API server. |
| settings.batchIdleDuration | string | `"1s"` | The maximum amount of time with no new ending pods that if exceeded ends the current batching window. If pods arrive faster than this time, the batching window will be extended up to the maxDuration. If they arrive slower, the pods will be batched separately. |
| settings.batchMaxDuration | string | `"10s"` | The maximum length of a batch window. The longer this is, the more pods we can consider for provisioning at one time which usually results in fewer but larger nodes. |
| settings.clusterCompartmentId | string | `""` | [required] Cluster compartment OCID. |
| settings.featureGates | object | `{"nodeOverlay":false,"nodeRepair":false,"spotToSpotConsolidation":false,"staticCapacity":false}` | Feature Gate configuration values. Feature Gates will follow the same graduation process and requirements as feature gates in Kubernetes. More information here https://kubernetes.io/docs/reference/command-line-tools-reference/feature-gates/#feature-gates-for-alpha-or-beta-features |
| settings.featureGates.nodeOverlay | bool | `false` | nodeOverlay is ALPHA and is disabled by default. Setting this to true will enable nodeOverlay. |
| settings.featureGates.nodeRepair | bool | `false` | nodeRepair is ALPHA and is disabled by default. Setting this to true will enable node repair. |
| settings.featureGates.spotToSpotConsolidation | bool | `false` | spotToSpotConsolidation is ALPHA and is disabled by default. Setting this to true will enable spot replacement consolidation for both single and multi-node consolidation. |
| settings.featureGates.staticCapacity | bool | `false` | staticCapacity is ALPHA and is disabled by default. Setting this to true will enable staticCapacity. |
| settings.instanceTypeMetaConfigMapName | string | `"oci-instance-type-meta"` | ConfigMap that stores oci-compute price and shape information |
| settings.ipFamilies | list | `["IPv4"]` | by default only IPv4, add IPv6 in needed |
| settings.ociVcnIpNative | bool | `false` | set this to true for a cluster run with OciVcnIpNative |
| settings.preBakedImageCompartmentId | string | `"ocid1.compartment.oc1..aaaaaaaab4u67dhgtj5gpdpp3z42xqqsdnufxkatoild46u3hb67vzojfmzq"` | Compartment OCID under which OKE pre-baked images are published |
| settings.vcnCompartmentId | string | `""` | [required] Cluster's VCN compartment OCID. |
| strategy | object | `{"rollingUpdate":{"maxUnavailable":1}}` | Strategy for updating the pod. |
| terminationGracePeriodSeconds | string | `nil` | Override the default termination grace period for the pod. |
| tolerations | list | `[{"key":"CriticalAddonsOnly","operator":"Exists"}]` | Tolerations to allow the pod to be scheduled to nodes with taints. |
| topologySpreadConstraints | list | `[{"maxSkew":1,"topologyKey":"topology.kubernetes.io/zone","whenUnsatisfiable":"DoNotSchedule"}]` | Topology spread constraints to increase the controller resilience by distributing pods across the cluster zones. If an explicit label selector is not provided one will be created from the pod selector labels. |
| volumeMounts | list | `[]` | Additional volume mounts on the controller container. |
| volumes | list | `[]` | Additional volumes on the controller Deployment. |

