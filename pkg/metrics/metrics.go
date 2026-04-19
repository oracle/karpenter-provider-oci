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
	WorkRequestIdLabel     = "work_request_id"
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
			WorkRequestIdLabel,
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
	operation string, status string, workRequestId string) {
	WorkRequestProcessDuration.Observe(serverElapsedSeconds,
		map[string]string{
			OperationLabel:     operation,
			StatusLabel:        status,
			WorkRequestIdLabel: workRequestId,
		})
}
