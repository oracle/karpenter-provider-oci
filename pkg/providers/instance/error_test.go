/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package instance

import (
	"errors"
	"net/http"
	"testing"

	pkgerrors "github.com/pkg/errors"
	"github.com/stretchr/testify/assert"

	"github.com/oracle/karpenter-provider-oci/pkg/oci"
)

// fakeServiceError implements the OCI SDK common.ServiceError interface so tests
// can exercise the error classifiers without a live API.
type fakeServiceError struct {
	httpStatus int
	code       string
	message    string
}

func (e fakeServiceError) Error() string           { return e.message }
func (e fakeServiceError) GetHTTPStatusCode() int  { return e.httpStatus }
func (e fakeServiceError) GetMessage() string      { return e.message }
func (e fakeServiceError) GetCode() string         { return e.code }
func (e fakeServiceError) GetOpcRequestID() string { return "" }

func limitExceededError() error {
	return fakeServiceError{
		httpStatus: http.StatusBadRequest,
		code:       oci.LimitExceeded,
		message:    "service limit exceeded",
	}
}

func outOfHostCapacityError() error {
	return fakeServiceError{
		httpStatus: http.StatusInternalServerError,
		code:       "InternalError",
		message:    "Out of host capacity.",
	}
}

func quotaExceededError() error {
	return fakeServiceError{
		httpStatus: http.StatusBadRequest,
		code:       oci.QuotaExceeded,
		message:    "compartment quota exceeded",
	}
}

func TestIsSkippableLaunchError(t *testing.T) {
	testCases := []struct {
		name                       string
		err                        error
		enableServiceLimitFallback bool
		want                       bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "no capacity error",
			err:  NoCapacityError{},
			want: true,
		},
		{
			name: "out of host capacity",
			err:  outOfHostCapacityError(),
			want: true,
		},
		{
			name: "service limit exceeded gate disabled",
			err:  limitExceededError(),
			want: false,
		},
		{
			name:                       "service limit exceeded gate enabled",
			err:                        limitExceededError(),
			enableServiceLimitFallback: true,
			want:                       true,
		},
		{
			name: "quota exceeded gate disabled",
			err: fakeServiceError{
				httpStatus: http.StatusBadRequest,
				code:       oci.QuotaExceeded,
				message:    "compartment quota exceeded",
			},
			want: false,
		},
		{
			name: "quota exceeded gate enabled",
			err: fakeServiceError{
				httpStatus: http.StatusBadRequest,
				code:       oci.QuotaExceeded,
				message:    "compartment quota exceeded",
			},
			enableServiceLimitFallback: true,
			want:                       true,
		},
		{
			name:                       "wrapped service limit exceeded gate enabled",
			err:                        pkgerrors.Wrap(limitExceededError(), "wrapped"),
			enableServiceLimitFallback: true,
			want:                       true,
		},
		{
			name: "unrelated service error",
			err: fakeServiceError{
				httpStatus: http.StatusBadRequest,
				code:       "SomeOtherCode",
				message:    "bad request",
			},
			want: false,
		},
		{
			name: "generic error",
			err:  errors.New("boom"),
			want: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsSkippableLaunchError(tc.err, tc.enableServiceLimitFallback))
		})
	}
}
