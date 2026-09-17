/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package metrics

import (
	"strconv"
	"time"

	opmetrics "github.com/awslabs/operatorpkg/metrics"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/karpenter/pkg/metrics"
)

const (
	CloudProviderSubsystem  = "cloudprovider"
	NodeClassLabel          = "nodeclass"
	NodeNameLabel           = "node_name"
	InstanceTypeLabel       = "instance_type"
	CapacityTypeLabel       = "capacity_type"
	ZoneLabel               = "zone"
	ShapeLabel              = "shape"
	AvailabilityDomainLabel = "availability_domain"
	ResultLabel             = "result"
	FaultDomainLabel        = "fault_domain"
	ApiNameLabel            = "api_name"
	ApiStatusCodeLabel      = "status_code"
	OperationLabel          = "operation"
	StatusLabel             = "status"

	ResultSuccess = "success"
	ResultFailure = "failure"

	// OutcomeLabel carries why capacity discovery did or did not have advice to give. Its values
	// are a small closed set, so the series stays bounded however many shapes are listed.
	OutcomeLabel = "outcome"

	// OutcomeApplied means a measurement was found and used in place of the estimate.
	OutcomeApplied = "applied"
	// OutcomeNoMeasurement means nothing of this kind has registered yet, or what did has expired.
	// This is the ordinary state before a combination's first node comes up.
	OutcomeNoMeasurement = "no_measurement"
	// OutcomeNoImage means the NodeClass carries no image configuration to resolve from at all, so
	// there is nothing to key a measurement on.
	OutcomeNoImage = "no_image"
	// OutcomeNotCached means the image this shape would launch with could not be established from
	// what image resolution has already cached, and discovery will not call OCI to find out. It is
	// a catch-all on purpose: nothing resolved recently, nothing cached covering this shape, or a
	// configuration that yields no image are all the same from here, and all end in the estimate.
	OutcomeNotCached = "not_cached"
	// OutcomeResolutionFailed means the image provider answered with something unusable. This is
	// the one outcome that indicates something is wrong.
	OutcomeResolutionFailed = "resolution_failed"
	// OutcomeDisabled means capacity discovery is switched off.
	OutcomeDisabled = "disabled"
)

var (
	NodeClassReady = opmetrics.NewPrometheusGauge(
		crmetrics.Registry,
		prometheus.GaugeOpts{
			Namespace: metrics.Namespace,
			Subsystem: CloudProviderSubsystem,
			Name:      "nodeclass_ready",
			Help:      "Whether an OCINodeClass is ready and has resolved the required OCI dependencies for provisioning.",
		},
		[]string{
			NodeClassLabel,
		},
	)

	InstanceTypeOfferingAvailable = opmetrics.NewPrometheusGauge(
		crmetrics.Registry,
		prometheus.GaugeOpts{
			Namespace: metrics.Namespace,
			Subsystem: CloudProviderSubsystem,
			Name:      "instance_type_offering_available",
			Help:      "Instance type offering availability, based on instance type, capacity type, and zone",
		},
		[]string{
			InstanceTypeLabel,
			CapacityTypeLabel,
			ZoneLabel,
		},
	)

	InstanceTypeOfferingPriceEstimate = opmetrics.NewPrometheusGauge(
		crmetrics.Registry,
		prometheus.GaugeOpts{
			Namespace: metrics.Namespace,
			Subsystem: CloudProviderSubsystem,
			Name:      "instance_type_offering_price_estimate",
			Help: "Instance type offering estimated hourly price used when making informed decisions on node " +
				"cost calculation, based on instance type, capacity type, and zone.",
		},
		[]string{
			InstanceTypeLabel,
			CapacityTypeLabel,
			ZoneLabel,
		},
	)

	// CapacityAdviceCounter separates the expected reasons for having no measured capacity to
	// offer - nothing observed yet, no image for the shape, discovery switched off - from a
	// genuine failure of image resolution. All of them leave the modelled estimate in place, so
	// only OutcomeResolutionFailed indicates a problem; the rest are ordinary.
	CapacityAdviceCounter = opmetrics.NewPrometheusCounter(
		crmetrics.Registry,
		prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Subsystem: CloudProviderSubsystem,
			Name:      "capacity_discovery_advice_count",
			Help: "Number of times capacity discovery was consulted while modelling an instance " +
				"type, by outcome",
		},
		[]string{
			OutcomeLabel,
		},
	)

	ExternalApiCallDuration = opmetrics.NewPrometheusHistogram(
		crmetrics.Registry,
		prometheus.HistogramOpts{
			Namespace: metrics.Namespace,
			Subsystem: CloudProviderSubsystem,
			Name:      "external_api_call_seconds",
			Help:      "Duration of the external API call",
			Buckets:   metrics.DurationBuckets(),
		},
		[]string{
			ApiNameLabel,
		},
	)

	ExternalApiCallStatusCounter = opmetrics.NewPrometheusCounter(
		crmetrics.Registry,
		prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Subsystem: CloudProviderSubsystem,
			Name:      "external_api_call_status_count",
			Help:      "Number of external API call status",
		},
		[]string{
			ApiNameLabel,
			ApiStatusCodeLabel,
		},
	)

	InstanceLaunchesCounter = opmetrics.NewPrometheusCounter(
		crmetrics.Registry,
		prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Subsystem: CloudProviderSubsystem,
			Name:      "instance_launches_total",
			Help:      "Number of OCI instance launch attempts initiated by Karpenter Provider OCI.",
		},
		[]string{
			CapacityTypeLabel,
			ShapeLabel,
			AvailabilityDomainLabel,
			ResultLabel,
		},
	)

	CapacityErrorsCounter = opmetrics.NewPrometheusCounter(
		crmetrics.Registry,
		prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Subsystem: CloudProviderSubsystem,
			Name:      "capacity_errors_total",
			Help:      "Number of OCI capacity-related provisioning failures.",
		},
		[]string{
			CapacityTypeLabel,
			ShapeLabel,
			AvailabilityDomainLabel,
			FaultDomainLabel,
		},
	)

	WorkRequestsCounter = opmetrics.NewPrometheusCounter(
		crmetrics.Registry,
		prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Subsystem: CloudProviderSubsystem,
			Name:      "work_requests_total",
			Help:      "Number of OCI work request outcomes.",
		},
		[]string{
			OperationLabel,
			StatusLabel,
		},
	)

	WorkRequestProcessDuration = opmetrics.NewPrometheusHistogram(
		crmetrics.Registry,
		prometheus.HistogramOpts{
			Namespace: metrics.Namespace,
			Subsystem: CloudProviderSubsystem,
			Name:      "work_request_process_time_seconds",
			Help:      "Duration of workrequest process time in seconds",
			Buckets:   metrics.DurationBuckets(),
		},
		[]string{
			OperationLabel,
			StatusLabel,
		},
	)

	VcnIPNativeEnabled = opmetrics.NewPrometheusGauge(
		crmetrics.Registry,
		prometheus.GaugeOpts{
			Namespace: metrics.Namespace,
			Subsystem: CloudProviderSubsystem,
			Name:      "vcn_ip_native_enabled",
			Help:      "Whether Karpenter Provider OCI is configured for an OCI VCN IP native cluster.",
		},
		[]string{},
	)

	LeaderNodeInfo = opmetrics.NewPrometheusGauge(
		crmetrics.Registry,
		prometheus.GaugeOpts{
			Namespace: metrics.Namespace,
			Subsystem: CloudProviderSubsystem,
			Name:      "leader_node_info",
			Help: "Information about the Kubernetes node hosting the elected Karpenter Provider OCI controller. " +
				"The value is always 1.",
		},
		[]string{
			NodeNameLabel,
		},
	)
)

func boolToFloat64(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

func RecordNodeClassReady(nodeClass string, ready bool) {
	NodeClassReady.Set(boolToFloat64(ready), map[string]string{
		NodeClassLabel: nodeClass,
	})
}

func DeleteNodeClassReady(nodeClass string) {
	NodeClassReady.Delete(map[string]string{
		NodeClassLabel: nodeClass,
	})
}

func RecordInstanceLaunch(capacityType string, shape string,
	availabilityDomain string, result string) {
	InstanceLaunchesCounter.Inc(map[string]string{
		CapacityTypeLabel:       capacityType,
		ShapeLabel:              shape,
		AvailabilityDomainLabel: availabilityDomain,
		ResultLabel:             result,
	})
}

func RecordCapacityError(capacityType string, shape string,
	availabilityDomain string, faultDomain string) {
	CapacityErrorsCounter.Inc(map[string]string{
		CapacityTypeLabel:       capacityType,
		ShapeLabel:              shape,
		AvailabilityDomainLabel: availabilityDomain,
		FaultDomainLabel:        faultDomain,
	})
}

func RecordWorkRequestOutcome(operation string, status string) {
	WorkRequestsCounter.Inc(map[string]string{
		OperationLabel: operation,
		StatusLabel:    status,
	})
}

func RecordVcnIPNativeEnabled(enabled bool) {
	VcnIPNativeEnabled.Set(boolToFloat64(enabled), map[string]string{})
}

func RecordLeaderNode(nodeName string) {
	LeaderNodeInfo.Set(1, map[string]string{
		NodeNameLabel: nodeName,
	})
}

func DeleteLeaderNode(nodeName string) {
	LeaderNodeInfo.Delete(map[string]string{
		NodeNameLabel: nodeName,
	})
}

func MeasureCallDuration(apiName string) func() time.Duration {
	start := time.Now()
	return func() time.Duration {
		dur := time.Since(start)
		ExternalApiCallDuration.Observe(dur.Seconds(),
			map[string]string{
				ApiNameLabel: apiName,
			})
		return dur
	}
}

func CountResponseStatus(apiName string, input interface{}) {
	if ociResponse, ok := input.(common.OCIResponse); ok {
		httpResponse := ociResponse.HTTPResponse()
		if httpResponse == nil {
			return
		}
		statusCode := httpResponse.StatusCode

		ExternalApiCallStatusCounter.Inc(
			map[string]string{
				ApiNameLabel:       apiName,
				ApiStatusCodeLabel: strconv.Itoa(statusCode),
			},
		)
	}
}

func RecordWorkRequestProcessTime(serverElapsedSeconds float64,
	operation string, status string) {
	WorkRequestProcessDuration.Observe(serverElapsedSeconds,
		map[string]string{
			OperationLabel: operation,
			StatusLabel:    status,
		})
}
