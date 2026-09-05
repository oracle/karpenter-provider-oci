/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package oci

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	"github.com/oracle/karpenter-provider-oci/pkg/metrics"
	"github.com/oracle/oci-go-sdk/v65/common"
	ociwr "github.com/oracle/oci-go-sdk/v65/workrequests"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
)

func TestDecorateMetrics(t *testing.T) {
	ctx := context.Background()
	_, err := decorate(ctx, "LaunchInstance", "testRequest",
		func() (fakes.TestResponse, error) {
			return fakes.TestResponse{
				StatusCode: 500,
				Count:      0,
			}, nil
		})

	assert.NoError(t, err)

	_, ok1 := fakes.FindMetricWithLabelValues(t,
		"external_api_call_seconds",
		map[string]string{
			metrics.ApiNameLabel: "LaunchInstance",
		})

	counterMetrics, ok2 := fakes.FindMetricWithLabelValues(t,
		"external_api_call_status_count",
		map[string]string{
			metrics.ApiNameLabel:       "LaunchInstance",
			metrics.ApiStatusCodeLabel: strconv.Itoa(500),
		})

	assert.True(t, ok1 && ok2)
	assert.Equal(t, float64(1), counterMetrics.GetCounter().GetValue())
}

func TestLogWorkRequestDurationMetrics(t *testing.T) {
	ctx := context.Background()

	now := time.Now()
	wr := ociwr.WorkRequest{
		Id:     lo.ToPtr("testWRId"),
		Status: ociwr.WorkRequestStatusFailed,
		TimeStarted: &common.SDKTime{
			Time: now,
		},
		TimeFinished: &common.SDKTime{
			Time: now.Add(5 * time.Second),
		},
	}
	LogWorkRequestDuration(ctx, "testOperation", wr)

	testMetric, ok := fakes.FindMetricWithLabelValues(t,
		"work_request_process_time_seconds",
		map[string]string{
			metrics.OperationLabel: "testOperation",
			metrics.StatusLabel:    string(wr.Status),
		})

	assert.True(t, ok)

	for _, bucket := range testMetric.GetHistogram().GetBucket() {
		if bucket.GetUpperBound() >= float64(5) {
			assert.Equal(t, uint64(1), bucket.GetCumulativeCount(), "UpperBound", bucket.GetUpperBound())
		} else {
			assert.Equal(t, uint64(0), bucket.GetCumulativeCount(), "UpperBound", bucket.GetUpperBound())
		}
	}
}
