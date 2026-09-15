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
	CloudProviderSubsystem = "cloudprovider"
	InstanceTypeLabel      = "instance_type"
	CapacityTypeLabel      = "capacity_type"
	ZoneLabel              = "zone"
	ApiNameLabel           = "api_name"
	ApiStatusCodeLabel     = "status_code"
	OperationLabel         = "operation"
	StatusLabel            = "status"

	// OutcomeLabel carries why capacity discovery did or did not have advice to give. Its values
	// are a small closed set, so the series stays bounded however many shapes are listed.
	OutcomeLabel = "outcome"

	// OutcomeApplied means a measurement was found and used in place of the estimate.
	OutcomeApplied = "applied"
	// OutcomeNoMeasurement means nothing of this kind has registered yet, or what did has expired.
	// This is the ordinary state before a combination's first node comes up.
	OutcomeNoMeasurement = "no_measurement"
	// OutcomeNoImage means the NodeClass configuration yields no image for this shape, so there is
	// nothing to key a measurement on. Expected for a NodeClass whose images cover part of the
	// catalogue.
	OutcomeNoImage = "no_image"
	// OutcomeSuppressed means a recent outcome for this shape and image configuration is still
	// standing, so no attempt was made.
	OutcomeSuppressed = "suppressed"
	// OutcomeResolutionFailed means the image service could not answer, or answered unusably. This
	// is the one outcome that indicates something is wrong.
	OutcomeResolutionFailed = "resolution_failed"
	// OutcomeDisabled means capacity discovery is switched off.
	OutcomeDisabled = "disabled"
)

var (
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
)

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
