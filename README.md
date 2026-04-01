# karpenter-provider-oci

Karpenter Provider OCI brings [Karpenter](https://karpenter.sh/) - powered node provisioning to Oracle Kubernetes Engine (OKE) clusters. It automatically adds and removes worker nodes to match real-time workload demand.
Karpenter helps improve utilization and control cost in Kubernetes by:

- **Detecting** pods that can’t be scheduled due to insufficient capacity
- **Interpreting** pod placement requirements such as CPU/memory requests, node selectors, affinities, tolerations, and topology spread constraints
- **Launching** new nodes that satisfy those requirements so workloads can start quickly
- **Deprovisioning** nodes once they’re no longer needed
- **Optimizing** the node fleet over time by consolidating workloads onto fewer or more cost-effective nodes with better utilization

[![License](https://img.shields.io/badge/license-UPL%201.0-blue.svg)](https://oss.oracle.com/licenses/upl/)
[![Release](https://img.shields.io/github/v/release/oracle/karpenter-provider-oci)](https://img.shields.io/github/v/release/oracle/karpenter-provider-oci)
[![Coverage Status](https://coveralls.io/repos/github/oracle/karpenter-provider-oci/badge.svg?branch=main)](https://coveralls.io/github/oracle/karpenter-provider-oci?branch=main)
[![Go Report Card](https://goreportcard.com/badge/github.com/oracle/karpenter-provider-oci)](https://goreportcard.com/report/github.com/oracle/karpenter-provider-oci)

## Installation

See [Installation](docs/guide/installation.md).

## Documentation

- [Introduction](docs/guide/introduction.md)
  - [Architecture View](docs/guide/architecture-view.md)
  - [Features](docs/guide/features.md)
- [Installation](docs/guide/installation.md)
  - [Configure IAM Policies for KPO to manage OCI resources](docs/guide/configure-iam-policies.md)
  - [Install KPO Helm Chart](docs/guide/installation-using-helm.md)
- [Helm Chart Reference](docs/reference/helm-chart.md)
- [OCI v1beta1 API](docs/reference/v1beta1-api-raw.md)
- [Scheduling Labels](docs/guide/scheduling-labels.md)
- [Usage](docs/guide/usage.md)
  - [Use KPO to manage nodes with OCI flexible shapes and an OKE image](docs/guide/usage.md#use-kpo-to-manage-nodes-with-oci-flexible-shapes-and-an-oke-image)
  - [Ensure worker nodes using an OKE image are always updated to the latest image](docs/guide/usage.md#ensure-worker-nodes-using-an-oke-image-are-always-updated-to-the-latest-image)
  - [Launch worker nodes for an OciIpNativeCNI cluster](docs/guide/usage.md#launch-worker-nodes-for-an-ociipnativecni-cluster)
- [Advanced Use Cases](docs/guide/advanced-use-cases.md)
  - [Spot capacity (OCI preemptible)](docs/guide/advanced-use-cases.md#spot-capacity-oci-preemptible)
  - [Reserved capacity (OCI capacity reservations)](docs/guide/advanced-use-cases.md#reserved-capacity-oci-capacity-reservations)
  - [Burstable instances (flex shapes)](docs/guide/advanced-use-cases.md#burstable-instances-flex-shapes)
  - [Cluster placement groups](docs/guide/advanced-use-cases.md#cluster-placement-groups)
  - [Compute clusters](docs/guide/advanced-use-cases.md#compute-clusters)
  - [Customize kubelet](docs/guide/advanced-use-cases.md#customize-kubelet)
  - [Customize compute instances](docs/guide/advanced-use-cases.md#customize-compute-instances)
  - [Customize node bootstrap (cloud-init)](docs/guide/advanced-use-cases.md#customize-node-bootstrap-cloud-init)
  - [Ubuntu images](docs/guide/advanced-use-cases.md#ubuntu-images)
  - [Override the OKE image compartment used by `imageFilter`](docs/guide/advanced-use-cases.md#override-the-oke-image-compartment-used-by-imagefilter)
- [Development](docs/guide/development.md)
- [OCI Resource Discovery and Reclaiming](docs/guide/oci-resource-discovery-and-reclaiming.md)
- [FAQs](docs/guide/faqs.md)
  - [Do special taints added by OKE bootstrapping in specific scenarios apply to Karpenter managed nodes?](docs/guide/faqs.md#do-special-taints-added-by-oke-bootstrapping-in-specific-scenarios-apply-to-karpenter-managed-nodes)
  - [How to configure Secondary VNICs?](docs/guide/faqs.md#how-to-configure-secondary-vnics)
  - [How can I ensure deployments are scheduled on a specific karpenter node pool?](docs/guide/faqs.md#how-can-i-ensure-deployments-are-scheduled-on-a-specific-karpenter-node-pool)
  - [How can I ensure workloads are evenly scheduled across ADs and FDs with Karpenter Provider OCI?](docs/guide/faqs.md#how-can-i-ensure-workloads-are-evenly-scheduled-across-ads-and-fds-with-karpenter-provider-oci)
  - [How can I list OKE images compatible with my OKE cluster?](docs/guide/faqs.md#how-can-i-list-oke-images-compatible-with-my-oke-cluster)
  - [Flexible shape node pool isn’t provisioning. Error: “skipping, nodepool requirements filtered out all instance types.” How do I fix this?](docs/guide/faqs.md#flexible-shape-node-pool-isnt-provisioning-error-skipping-nodepool-requirements-filtered-out-all-instance-types-how-do-i-fix-this)
  - [How to run the OCI Karpenter controller with debug logging?](docs/guide/faqs.md#how-to-run-the-oci-karpenter-controller-with-debug-logging)
  - [Suggestions regarding KubeletConfig maxPods and podsPerCore](docs/guide/faqs.md#suggestions-regarding-kubeletconfig-maxpods-and-podspercore)

## Examples

See curated [examples](docs/guide/usage.md) for different use cases.

## Help

* Open a [GitHub issue](https://github.com/oracle/karpenter-provider-oci/issues) for bug reports, questions, or requests for enhancements.
* Report a security vulnerability according to the [Reporting Vulnerabilities guide](https://www.oracle.com/corporate/security-practices/assurance/vulnerability/reporting.html).

## Contributing

This project welcomes contributions from the community. Before submitting a pull request, please [review our contribution guide](./CONTRIBUTING.md)

## Security

Please consult the [security guide](./SECURITY.md) for our responsible security vulnerability disclosure process

## License

Copyright (c) 2026 Oracle and/or its affiliates.

Released under the Universal Permissive License v1.0 as shown at
<https://oss.oracle.com/licenses/upl/>.
