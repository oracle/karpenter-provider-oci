# Platform Configuration

`OCINodeClass.spec.platformConfig` sets the OCI Compute platform configuration for instances launched from the NodeClass.
It supports Shielded Instances, Confidential Computing, and processor settings.
**The selected shape must support every supplied value.**
See [Creating an Instance](https://docs.oracle.com/en-us/iaas/Content/Compute/Tasks/launchinginstance.htm) for OCI configuration options.

For example, add this fragment to an existing [OCINodeClass](ocinodeclass.md#minimal-example) to disable symmetric multithreading:

```yaml
spec:
  platformConfig:
    symmetricMultiThreadingEnabled: false
```

## Configuration fields

| Field | Values | Purpose |
| --- | --- | --- |
| `secureBootEnabled` | Boolean | Enable Secure Boot. |
| `trustedPlatformModuleEnabled` | Boolean | Enable the Trusted Platform Module (TPM). |
| `measuredBootEnabled` | Boolean | Enable Measured Boot. |
| `memoryEncryptionEnabled` | Boolean | Enable Confidential Computing. |
| `symmetricMultiThreadingEnabled` | Boolean | Enable symmetric multithreading (SMT). |
| `accessControlServiceEnabled` | Boolean | Enable Access Control Service. |
| `virtualInstructionsEnabled` | Boolean | Enable virtualization instructions. |
| `inputOutputMemoryManagementUnitEnabled` | Boolean | Enable the input-output memory management unit (IOMMU). |
| `numaNodesPerSocket` | `NPS0`, `NPS1`, `NPS2`, `NPS4`, `NPS6` | Set the number of NUMA nodes per socket. |
| `percentageOfCoresEnabled` | `25`, `50`, `75`, `100` | Set the percentage of cores enabled. |

For processor options, see [BIOS Settings for Bare Metal Instances](https://docs.oracle.com/en-us/iaas/Content/Compute/References/bios-settings.htm).

If you omit `platformConfig`, OCI Compute uses the shape's defaults.
Setting a boolean field to `false` disables that feature; leaving it unset uses the shape's default.

When any field is set, KPO uses your values and known shape defaults to check compatibility, capacity types, and CPU estimates.
Missing defaults remain unknown. Only supplied values are sent to OCI.

## Prerequisites and compatibility

Use a compatible image that meets the [OKE self-managed node prerequisites](https://docs.oracle.com/en-us/iaas/Content/ContEng/Tasks/contengprereqsforselfmanagednodes.htm).

The Kubernetes API server enforces KPO's CRD rules for field types, NUMA and core-percentage values, and explicitly conflicting Confidential Computing and Shielded settings.
KPO checks each shape against OCI's supported values before scheduling and launch:

| Condition | Result |
| --- | --- |
| A boolean field is explicitly set, including `false` | OCI must list the value as supported; a default alone is insufficient. |
| A supplied field or value is unsupported, or required shape information is missing | KPO excludes the shape. It does not drop or replace requested settings. |
| `numaNodesPerSocket` is supplied | The shape must support that NUMA value. |
| `percentageOfCoresEnabled` is supplied | The value must fall within the shape's supported range. |
| Several shapes share a NodeClass | Each selected shape must support every requested platform config setting. Use separate NodeClasses for different configurations. |

OCI Compute validates image compatibility and other launch requirements.

## Shielded instance

See [OCI Shielded Instances](https://docs.oracle.com/en-us/iaas/Content/Compute/References/shielded-instances.htm) for supported feature combinations, shapes, and images.

This example enables Secure Boot, TPM, and Measured Boot on a supported Linux virtual machine (VM):

```yaml
spec:
  platformConfig:
    secureBootEnabled: true
    trustedPlatformModuleEnabled: true
    measuredBootEnabled: true
```

Verify that the selected OKE image boots and its networking and storage work with these settings.

## Confidential Computing

Set `memoryEncryptionEnabled: true` to enable [OCI Confidential Computing](https://docs.oracle.com/en-us/iaas/Content/Compute/References/confidential_compute.htm).
Refer to OCI's support matrix for compatible regions, shapes, and images.

When any platform field is set and memory encryption is enabled, KPO:

- Excludes `spot` and `reserved` capacity. The NodePool must allow `on-demand`, even if the NodeClass lists capacity reservations.
- Excludes shapes with Secure Boot, TPM, or Measured Boot enabled.

For example, add this fragment to an OCINodeClass named `confidential-workers`:

```yaml
spec:
  platformConfig:
    memoryEncryptionEnabled: true
```

Configure its NodePool to use on-demand capacity:

```yaml
spec:
  template:
    spec:
      nodeClassRef:
        group: oci.oraclecloud.com
        kind: OCINodeClass
        name: confidential-workers
      requirements:
        - key: karpenter.sh/capacity-type
          operator: In
          values: [on-demand]
```

## CPU and pod estimates

KPO uses `symmetricMultiThreadingEnabled` and `percentageOfCoresEnabled` to estimate CPU capacity.
Known shape defaults can affect this estimate even when you set only a security field.
Omitting `platformConfig` preserves the existing CPU calculation.
See [OCI multithreading](https://docs.oracle.com/en-us/iaas/Content/Compute/Tasks/disablesmt.htm) for the effect of enabling or disabling multithreading.

The CPU estimate determines default CPU reservations, CPU available to pods, and pod limits based on `podsPerCore`.
`maxPods`, networking limits, and explicit CPU reservations still apply.

For example, with Flannel on `VM.Standard.E4.Flex`, 8 OCPUs, `podsPerCore: 10`, and `maxPods: 110`:

| Symmetric multithreading | CPU capacity | Pod capacity |
| --- | --- | --- |
| Disabled | 8 | 80 |
| Enabled | 16 | 110 |

Allocated OCPUs, memory, and price do not change. NUMA settings do not add topology-aware scheduling.

## Changes and replacement

Adding, changing, or removing platform settings triggers drift replacement, subject to NodePool disruption controls and replacement capacity.

## Troubleshooting

If KPO excludes a shape from launch because of its platform configuration, enable debug logging and look for `shape unavailable for requested platform configuration`.
The message identifies the shape, platform type, first failed check, and field.

| Reason | Check |
| --- | --- |
| `missing_platform_metadata` | OCI must provide platform options and a platform type for the shape. |
| `field_not_supported` | OCI must report configurable values for the requested field. |
| `invalid_field_metadata` | The minimum and maximum core percentages must be present and consistent. |
| `unsupported_value` | The requested value must be in the shape's supported list or range. |
| `unsupported_shielded_combination` | Check the supported combinations in the linked Shielded Instances guide. |
| `confidential_shielded_conflict` | Memory encryption and a Shielded Instance feature are both enabled. |

For pending pods, check NodePool requirements, compatible shapes, and available capacity.
For launch failures, inspect NodeClaim conditions and the OCI Compute error.
KPO retains your requested settings across retries.
