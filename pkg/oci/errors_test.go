/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package oci

import (
	"context"
	stderrs "errors"
	"net/http"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	pkgerrors "github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
)

type fakeTimeoutError struct {
	msg string
}

func (e fakeTimeoutError) Error() string   { return e.msg }
func (e fakeTimeoutError) Timeout() bool   { return true }
func (e fakeTimeoutError) Temporary() bool { return true }

type fakeServiceError struct {
	httpStatus int
	message    string
	code       string
	opcReqID   string
}

func (e fakeServiceError) Error() string          { return e.message }
func (e fakeServiceError) GetHTTPStatusCode() int { return e.httpStatus }
func (e fakeServiceError) GetMessage() string     { return e.message }
func (e fakeServiceError) GetCode() string        { return e.code }
func (e fakeServiceError) GetOpcRequestID() string {
	return e.opcReqID
}

func TestIsRetryable(t *testing.T) {
	t.Run("nil error", func(t *testing.T) {
		assert.False(t, IsRetryable(nil))
	})

	testCases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "deadline exceeded",
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "wrapped deadline exceeded",
			err:  pkgerrors.Wrap(context.DeadlineExceeded, "wrapped"),
			want: true,
		},
		{
			name: "timeout network error",
			err:  fakeTimeoutError{msg: "timeout"},
			want: true,
		},
		{
			name: "wrapped timeout network error",
			err:  pkgerrors.Wrap(fakeTimeoutError{msg: "timeout"}, "wrapped"),
			want: true,
		},
		{
			name: "retryable 409 incorrect state",
			err: fakeServiceError{
				httpStatus: http.StatusConflict,
				code:       HTTP409IncorrectStateCode,
				message:    "conflict",
			},
			want: true,
		},
		{
			name: "retryable 409 external server incorrect state",
			err: fakeServiceError{
				httpStatus: http.StatusConflict,
				code:       HTTP409ExternalServerIncorrectState,
				message:    "conflict",
			},
			want: true,
		},
		{
			name: "non-retryable 409 with unrelated code",
			err: fakeServiceError{
				httpStatus: http.StatusConflict,
				code:       "SomeOtherCode",
				message:    "conflict",
			},
			want: false,
		},
		{
			name: "retryable 500",
			err: fakeServiceError{
				httpStatus: http.StatusInternalServerError,
				code:       "InternalError",
				message:    "server error",
			},
			want: true,
		},
		{
			name: "retryable 502",
			err: fakeServiceError{
				httpStatus: http.StatusBadGateway,
				code:       "BadGateway",
				message:    "bad gateway",
			},
			want: true,
		},
		{
			name: "retryable 503",
			err: fakeServiceError{
				httpStatus: http.StatusServiceUnavailable,
				code:       "ServiceUnavailable",
				message:    "service unavailable",
			},
			want: true,
		},
		{
			name: "retryable 504",
			err: fakeServiceError{
				httpStatus: http.StatusGatewayTimeout,
				code:       "GatewayTimeout",
				message:    "gateway timeout",
			},
			want: true,
		},
		{
			name: "non-retryable 404",
			err: fakeServiceError{
				httpStatus: http.StatusNotFound,
				code:       "NotFound",
				message:    "not found",
			},
			want: false,
		},
		{
			name: "non-retryable generic error",
			err:  stderrs.New("generic"),
			want: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsRetryable(tc.err))
		})
	}
}

func TestNewRetryPolicyWithMaxAttempts(t *testing.T) {
	p := NewRetryPolicyWithMaxAttempts(5)
	assert.NotNil(t, p)
	assert.Equal(t, uint(5), p.MaximumNumberAttempts)

	retryable := p.ShouldRetryOperation(common.OCIOperationResponse{
		Error: fakeServiceError{
			httpStatus: http.StatusInternalServerError,
			code:       "InternalError",
			message:    "server error",
		},
	})
	assert.True(t, retryable)

	nonRetryable := p.ShouldRetryOperation(common.OCIOperationResponse{
		Error: fakeServiceError{
			httpStatus: http.StatusBadRequest,
			code:       "BadRequest",
			message:    "bad request",
		},
	})
	assert.False(t, nonRetryable)

	assert.Equal(t, 1*time.Second, p.NextDuration(common.OCIOperationResponse{AttemptNumber: 1}))
	assert.Equal(t, 2*time.Second, p.NextDuration(common.OCIOperationResponse{AttemptNumber: 2}))
	assert.Equal(t, 4*time.Second, p.NextDuration(common.OCIOperationResponse{AttemptNumber: 3}))
}

func TestNewRetryPolicy(t *testing.T) {
	p := newRetryPolicy()
	assert.NotNil(t, p)
	assert.Equal(t, uint(3), p.MaximumNumberAttempts)
	assert.True(t, p.ShouldRetryOperation(common.OCIOperationResponse{
		Error: fakeServiceError{
			httpStatus: http.StatusBadGateway,
			code:       "BadGateway",
			message:    "bad gateway",
		},
	}))
}

func TestIsNotFound(t *testing.T) {
	assert.False(t, IsNotFound(nil))
	assert.True(t, IsNotFound(errNotFound))
	assert.True(t, IsNotFound(pkgerrors.Wrap(errNotFound, "wrapped")))

	assert.True(t, IsNotFound(fakeServiceError{
		httpStatus: http.StatusNotFound,
		code:       "NotFound",
		message:    "not found",
	}))

	assert.False(t, IsNotFound(fakeServiceError{
		httpStatus: http.StatusUnauthorized,
		code:       "NotAuthorized",
		message:    "unauthorized",
	}))
}

func TestIsOutOfHostCapacity(t *testing.T) {
	assert.True(t, IsOutOfHostCapacity(fakeServiceError{
		httpStatus: http.StatusConflict,
		code:       "IncorrectState",
		message:    "Out of host capacity in selected AD",
	}))

	assert.False(t, IsOutOfHostCapacity(fakeServiceError{
		httpStatus: http.StatusConflict,
		code:       "IncorrectState",
		message:    "some other conflict",
	}))

	assert.False(t, IsOutOfHostCapacity(stderrs.New("generic")))
}
