/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package oci

import (
	"context"
	"math"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/pkg/errors"
)

// HTTP 409 error codes handled specially by IsRetryable
const (
	HTTP409IncorrectStateCode           = "IncorrectState"
	HTTP409ExternalServerIncorrectState = "ExternalServerIncorrectState"
	OutOfHostCapacity                   = "Out of host capacity"
)

var errNotFound = errors.New("not found")

// IsRetryable returns true if the given error is retriable.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	// generic timeouts
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// transient network failures
	var ne net.Error
	if errors.As(err, &ne) && (ne.Timeout()) {
		return true
	}
	// OCI SDK network helpers (present in sdk >=v65)
	if common.IsNetworkError(err) {
		return true
	}

	err = errors.Cause(err)
	svcErr, ok := common.IsServiceError(err)
	if !ok {
		return false
	}
	switch svcErr.GetHTTPStatusCode() {
	case http.StatusConflict: // 409
		switch svcErr.GetCode() {
		case HTTP409IncorrectStateCode,
			HTTP409ExternalServerIncorrectState:
			return true
		}
	case http.StatusInternalServerError, // 500
		http.StatusBadGateway,         // 502
		http.StatusServiceUnavailable, // 503
		http.StatusGatewayTimeout:     // 504
		return true
	}
	return false
}

func newRetryPolicy() *common.RetryPolicy {
	return NewRetryPolicyWithMaxAttempts(uint(3))
}

// NewRetryPolicyWithMaxAttempts returns a RetryPolicy with the specified max retryAttempts
func NewRetryPolicyWithMaxAttempts(retryAttempts uint) *common.RetryPolicy {
	isRetryableOperation := func(r common.OCIOperationResponse) bool {
		return IsRetryable(r.Error)
	}

	nextDuration := func(r common.OCIOperationResponse) time.Duration {
		// you might want wait longer for next retry when your previous one failed
		// this function will return the duration as:
		// 1s, 2s, 4s, 8s, 16s, 32s, 64s etc...
		return time.Duration(math.Pow(float64(2), float64(r.AttemptNumber-1))) * time.Second
	}

	policy := common.NewRetryPolicy(
		retryAttempts, isRetryableOperation, nextDuration,
	)
	return &policy
}

func IsNotFound(err error) bool {
	if err == nil {
		return false
	}

	err = errors.Cause(err)
	if err == errNotFound {
		return true
	}

	serviceErr, ok := common.IsServiceError(err)
	return ok && serviceErr.GetHTTPStatusCode() == http.StatusNotFound
}

func IsOutOfHostCapacity(err error) bool {
	if err == nil {
		return false
	}

	serviceErr, ok := common.IsServiceError(err)
	if ok {
		return strings.Contains(serviceErr.GetMessage(), OutOfHostCapacity)
	}

	return strings.Contains(err.Error(), OutOfHostCapacity)
}
