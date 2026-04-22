/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package utils

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	clientgofake "k8s.io/client-go/kubernetes/fake"
	clientgotesting "k8s.io/client-go/testing"
)

func TestGetClusterVersionSuccess(t *testing.T) {
	fakeClient := clientgofake.NewClientset()
	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.30.0",
	}

	got, err := GetClusterVersion(fakeClient)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "1.30.0", got.String())
}

func TestGetClusterVersionError(t *testing.T) {
	fakeClient := clientgofake.NewClientset()
	discoveryErr := errors.New("discovery failed")

	fakeClient.Discovery().(*fakediscovery.FakeDiscovery).PrependReactor("*", "*",
		func(action clientgotesting.Action) (handled bool, ret runtime.Object, err error) {
			return true, nil, discoveryErr
		})

	got, err := GetClusterVersion(fakeClient)
	require.Error(t, err)
	require.ErrorIs(t, err, discoveryErr)
	require.Nil(t, got)
}
