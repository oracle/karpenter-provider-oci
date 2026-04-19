/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package metrics

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	"github.com/stretchr/testify/assert"
)

type nilHTTPResponse struct{}

func (nilHTTPResponse) HTTPResponse() *http.Response {
	return nil
}

func TestMeasureCallDuration(t *testing.T) {
	testCases := []int{5, 10, 12, 21}

	// Measure all duration in test cases
	for _, tc := range testCases {
		measure := MeasureCallDuration("testApi")
		time.Sleep(time.Duration(tc) * time.Second)
		measure()
	}

	// Expected values
	// upperBound <=5, count 0
	// 5 < upperBound <= 10, count 1
	// 10 < upperBound <= 12, count 2
	// 12 < upperBound <= 21, count 3
	// upperBound > 21, count 4
	previousValue := 0
	for index, tc := range testCases {
		testMetric, ok := fakes.FindMetricWithLabelValues(t,
			"external_api_call_seconds",
			map[string]string{
				ApiNameLabel: "testApi",
			})

		assert.True(t, ok)
		assert.NotEqual(t, 0, len(testMetric.GetHistogram().GetBucket()))

		for _, bucket := range testMetric.GetHistogram().GetBucket() {
			if bucket.GetUpperBound() > float64(previousValue) && bucket.GetUpperBound() <= float64(tc) {
				assert.Equal(t, uint64(index), bucket.GetCumulativeCount(),
					"testCase", tc, "UpperBound", bucket.GetUpperBound())
			}
			if index == len(testCases)-1 && bucket.GetUpperBound() > float64(tc) {
				assert.Equal(t, uint64(len(testCases)), bucket.GetCumulativeCount(),
					"testCase", tc, "UpperBound", bucket.GetUpperBound())
			}
		}

		previousValue = tc
	}
}

func TestCountResponseStatus(t *testing.T) {
	testCases := []fakes.TestResponse{
		{StatusCode: 400, Count: 3},
		{StatusCode: 404, Count: 5},
		{StatusCode: 500, Count: 2},
	}

	for _, tc := range testCases {
		for range tc.Count {
			CountResponseStatus("testApi", &tc)
		}
	}

	for _, tc := range testCases {
		testMetric, ok := fakes.FindMetricWithLabelValues(t,
			"external_api_call_status_count",
			map[string]string{
				ApiNameLabel:       "testApi",
				ApiStatusCodeLabel: strconv.Itoa(tc.StatusCode),
			})
		assert.True(t, ok)
		assert.Equal(t, float64(tc.Count), testMetric.GetCounter().GetValue())
	}
}

func TestCountResponseStatusTypeNotMatch(t *testing.T) {
	testType := struct {
		StatusCode int
	}{200}

	CountResponseStatus("testApi", &testType)

	_, ok := fakes.FindMetricWithLabelValues(t,
		"external_api_call_status_count",
		map[string]string{
			ApiNameLabel:       "testApi",
			ApiStatusCodeLabel: strconv.Itoa(200),
		})
	assert.False(t, ok)
}

func TestCountResponseStatusNilHTTPResponse(t *testing.T) {
	assert.NotPanics(t, func() {
		CountResponseStatus("testApiNilHTTPResponse", nilHTTPResponse{})
	})
}

func TestRecordWorkRequestProcessTime(t *testing.T) {
	duration := 10 * time.Second
	RecordWorkRequestProcessTime(duration.Seconds(), "testOperation", "SUCCEEDED", "testWrId")

	testMetric, ok := fakes.FindMetricWithLabelValues(t,
		"work_request_process_time_seconds",
		map[string]string{
			OperationLabel:     "testOperation",
			StatusLabel:        "SUCCEEDED",
			WorkRequestIdLabel: "testWrId",
		})

	assert.True(t, ok)

	for _, bucket := range testMetric.GetHistogram().GetBucket() {
		if bucket.GetUpperBound() >= float64(10) {
			assert.Equal(t, uint64(1), bucket.GetCumulativeCount(), "UpperBound", bucket.GetUpperBound())
		} else {
			assert.Equal(t, uint64(0), bucket.GetCumulativeCount(), "UpperBound", bucket.GetUpperBound())
		}
	}

}
