# OCI API Rate Limiter

Karpenter Provider OCI (KPO) includes an optional client-side rate limiter for OCI API requests. The limiter smooths bursts of requests from KPO before they reach OCI services, helping reduce server-side throttling during controller reconciliation, node launches, and node terminations.

The rate limiter is available in KPO v1.2.0 and later. It is disabled by default so that upgrading KPO does not change request throughput until an operator explicitly enables it.

## How it works

When enabled, KPO creates two token buckets:

- A **read** bucket for OCI get and list operations.
- A **write** bucket for OCI operations that change resources.

Each request waits for a token from its bucket before KPO calls the OCI SDK. A bucket can issue requests at its configured queries-per-second (QPS) rate and can temporarily exceed that steady rate up to its burst capacity. For example, a QPS of `10` with a burst of `3` permits up to three immediately available requests, after which tokens are replenished at ten per second.

The limiter is local to each KPO process. If more than one KPO replica is actively making OCI requests, each replica has its own read and write buckets. Choose values with the aggregate traffic from the deployment and any other clients using the same OCI service limits in mind.

Client-side rate limiting does not replace OCI service limits or the OCI SDK retry policy. OCI can still return `429 TooManyRequests` or other throttling responses because limits can vary by service, operation, tenancy, or region.

## Default behavior

The Helm chart defaults to:

```yaml
settings:
  rateLimiter:
    disable: true
    qpsRead: 0.0
    burstRead: 0
    qpsWrite: 0.0
    burstWrite: 0
```

`disable: true` bypasses client-side limiting. When the limiter is enabled, a rate or burst value of `0` selects the built-in default:

| Traffic class | Built-in QPS | Built-in burst |
| --- | ---: | ---: |
| Read | 20 | 5 |
| Write | 20 | 5 |

All rate and burst values must be greater than or equal to zero. QPS values may be fractional; burst values must be integers.

## Enable the rate limiter

To enable the limiter with the built-in rates, add the following settings to the values file used for the KPO release:

```yaml
settings:
  rateLimiter:
    disable: false
    qpsRead: 0.0
    burstRead: 0
    qpsWrite: 0.0
    burstWrite: 0
```

Apply the updated configuration:

```shell
helm upgrade karpenter karpenter-provider-oci/karpenter \
  --version <chart-version> \
  --namespace <karpenter-namespace> \
  --values <path-to-values.yaml>
```

You can also enable the built-in defaults with a Helm command-line override:

```shell
helm upgrade karpenter karpenter-provider-oci/karpenter \
  --version <chart-version> \
  --namespace <karpenter-namespace> \
  --reuse-values \
  --set settings.rateLimiter.disable=false
```

After the KPO pods restart, each pod logs the effective configuration in a message similar to:

```text
OCI rate limiter is enabled readQPS=20 readBurst=5 writeQPS=20 writeBurst=5
```

## Configure custom limits

Set read and write values independently when the workload has different traffic profiles. The following example allows more read traffic than write traffic:

```yaml
settings:
  rateLimiter:
    disable: false
    qpsRead: 10
    burstRead: 5
    qpsWrite: 5
    burstWrite: 2
```

The Helm values map to KPO flags and environment variables as follows:

| Helm value | KPO flag | Environment variable | Description |
| --- | --- | --- | --- |
| `settings.rateLimiter.disable` | `--disable-rate-limiter` | `DISABLE_RATE_LIMITER` | Set to `false` to enable the limiter. |
| `settings.rateLimiter.qpsRead` | `--rate-limit-qps-read` | `RATE_LIMIT_QPS_READ` | Steady read request rate. `0` uses 20 QPS. |
| `settings.rateLimiter.burstRead` | `--rate-limit-burst-read` | `RATE_LIMIT_BURST_READ` | Maximum read burst. `0` uses 5. |
| `settings.rateLimiter.qpsWrite` | `--rate-limit-qps-write` | `RATE_LIMIT_QPS_WRITE` | Steady write request rate. `0` uses 20 QPS. |
| `settings.rateLimiter.burstWrite` | `--rate-limit-burst-write` | `RATE_LIMIT_BURST_WRITE` | Maximum write burst. `0` uses 5. |

When running KPO without the Helm chart, configure either flags or their corresponding environment variables. An explicitly provided flag takes precedence over its environment variable.

## Requests covered by the limiter

The two buckets are shared across the supported OCI clients within a KPO process; they are not separate per OCI service.

Read-limited operations include:

- Compute queries for instances, shapes, images, image compatibility, VNIC attachments, boot volume attachments, capacity reservations, compute clusters, and related list operations.
- Block Volume `GetBootVolume`.
- Virtual Cloud Network queries for subnets, VNICs, and network security groups.
- Identity queries for compartments and availability domains.
- Work Request queries for status and errors.
- Cluster Placement Group get and list operations.
- Key Management `GetKey`.

Write-limited operations include:

- Compute `LaunchInstance`.
- Compute `TerminateInstance`.

## Tune the limits

Start with the built-in values and adjust gradually using observed controller behavior and OCI service responses.

- Lower QPS when KPO or other automation frequently receives OCI throttling responses.
- Keep the burst small when short request spikes trigger throttling.
- Increase read limits when discovery or reconciliation is delayed and OCI is not throttling the requests.
- Increase write limits cautiously when node launches or terminations queue behind the client-side limiter.
- Account for every active KPO replica because the limits apply per process, not globally across the deployment.

Avoid setting a very low burst relative to concurrent controller activity. A small bucket can serialize requests and increase provisioning or termination latency even when the QPS value appears sufficient.

## Verify the configuration

Check the rate-limiter environment variables rendered into the deployment:

```shell
kubectl -n <karpenter-namespace> get deployment karpenter \
  -o jsonpath='{range .spec.template.spec.containers[0].env[*]}{.name}={.value}{"\n"}{end}' \
  | grep -E '^(DISABLE_RATE_LIMITER|RATE_LIMIT_)'
```

Check the startup log for the effective values:

```shell
kubectl -n <karpenter-namespace> logs deployment/karpenter \
  | grep 'OCI rate limiter is'
```

The log reports either `OCI rate limiter is disabled` or the enabled read and write settings. When a request cannot obtain a token because its context is canceled or expires, KPO logs `OCI rate limiter wait failed` with the OCI operation and traffic mode.

## Disable the rate limiter

Set `disable` back to `true` and upgrade the release:

```yaml
settings:
  rateLimiter:
    disable: true
```

Disabling the limiter makes both buckets always allow requests. The OCI SDK retry policy and OCI service-side throttling still apply.

## Troubleshooting

### The startup log says the limiter is disabled

Confirm that the effective Helm values contain `settings.rateLimiter.disable: false` and that the deployment has `DISABLE_RATE_LIMITER=false`. A stale values file or `--reuse-values` can retain the previous setting.

### KPO still receives `429 TooManyRequests`

The configured rate may be above an OCI service limit, other clients may be consuming the same limit, or multiple KPO processes may be contributing traffic. Reduce the relevant QPS and burst values, then compare the frequency of throttling responses and controller latency.

### Provisioning or termination became slower

The write bucket covers both launch and terminate requests. If OCI is not returning throttling responses, increase `qpsWrite` or `burstWrite` incrementally. Also check whether multiple node operations are waiting concurrently.

### Discovery or reconciliation became slower

Most discovery calls use the shared read bucket. If OCI is not returning throttling responses, increase `qpsRead` or `burstRead` incrementally.

For the complete Helm value reference, see [Helm Chart Reference](../reference/helm-chart.md).
