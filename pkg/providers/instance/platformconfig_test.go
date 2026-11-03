/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package instance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancemeta"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancetype"
	"github.com/oracle/oci-go-sdk/v65/common"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	ociwr "github.com/oracle/oci-go-sdk/v65/workrequests"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

func launchRequestBody(t *testing.T, request ocicore.LaunchInstanceRequest) map[string]json.RawMessage {
	t.Helper()
	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(launchRequestBytes(t, request), &body))
	return body
}

func launchRequestBytes(t *testing.T, request ocicore.LaunchInstanceRequest) []byte {
	t.Helper()
	httpRequest, err := request.HTTPRequest(http.MethodPost, "/20160918/instances", nil, nil)
	require.NoError(t, err)
	body, err := io.ReadAll(httpRequest.Body)
	require.NoError(t, err)
	require.NoError(t, httpRequest.Body.Close())
	return body
}

func completePlatformRequest() *v1beta1.PlatformConfig {
	return &v1beta1.PlatformConfig{
		SecureBootEnabled: lo.ToPtr(false), TrustedPlatformModuleEnabled: lo.ToPtr(false),
		MeasuredBootEnabled: lo.ToPtr(false), MemoryEncryptionEnabled: lo.ToPtr(true),
		SymmetricMultiThreadingEnabled: lo.ToPtr(false), AccessControlServiceEnabled: lo.ToPtr(true),
		VirtualInstructionsEnabled: lo.ToPtr(false), InputOutputMemoryManagementUnitEnabled: lo.ToPtr(true),
		NumaNodesPerSocket: lo.ToPtr("NPS4"), PercentageOfCoresEnabled: lo.ToPtr(50),
	}
}

const completePlatformJSON = `{
  "type":"HYPOTHETICAL_FUTURE_VM",
  "isSecureBootEnabled":false,
  "isTrustedPlatformModuleEnabled":false,
  "isMeasuredBootEnabled":false,
  "isMemoryEncryptionEnabled":true,
  "isSymmetricMultiThreadingEnabled":false,
  "isAccessControlServiceEnabled":true,
  "areVirtualInstructionsEnabled":false,
  "isInputOutputMemoryManagementUnitEnabled":true,
  "numaNodesPerSocket":"NPS4",
  "percentageOfCoresEnabled":50
}`

// Wrong JSON names, discarded explicit false, extra fields, or a type allowlist break the HTTP contract.
func TestBuildPlatformConfig_HTTPRequest(t *testing.T) {
	platformType := ocicore.ShapePlatformConfigOptionsTypeEnum("HYPOTHETICAL_FUTURE_VM")
	for _, tt := range []struct {
		name    string
		request *v1beta1.PlatformConfig
		want    string
	}{
		{"secure boot", &v1beta1.PlatformConfig{SecureBootEnabled: lo.ToPtr(false)},
			`{"type":"HYPOTHETICAL_FUTURE_VM","isSecureBootEnabled":false}`},
		{"TPM", &v1beta1.PlatformConfig{TrustedPlatformModuleEnabled: lo.ToPtr(false)},
			`{"type":"HYPOTHETICAL_FUTURE_VM","isTrustedPlatformModuleEnabled":false}`},
		{"measured boot", &v1beta1.PlatformConfig{MeasuredBootEnabled: lo.ToPtr(false)},
			`{"type":"HYPOTHETICAL_FUTURE_VM","isMeasuredBootEnabled":false}`},
		{"memory encryption", &v1beta1.PlatformConfig{MemoryEncryptionEnabled: lo.ToPtr(false)},
			`{"type":"HYPOTHETICAL_FUTURE_VM","isMemoryEncryptionEnabled":false}`},
		{"explicit false and omitted fields", &v1beta1.PlatformConfig{
			SymmetricMultiThreadingEnabled: lo.ToPtr(false),
		}, `{"type":"HYPOTHETICAL_FUTURE_VM","isSymmetricMultiThreadingEnabled":false}`},
		{"ACS", &v1beta1.PlatformConfig{AccessControlServiceEnabled: lo.ToPtr(false)},
			`{"type":"HYPOTHETICAL_FUTURE_VM","isAccessControlServiceEnabled":false}`},
		{"virtualization instructions", &v1beta1.PlatformConfig{VirtualInstructionsEnabled: lo.ToPtr(false)},
			`{"type":"HYPOTHETICAL_FUTURE_VM","areVirtualInstructionsEnabled":false}`},
		{"IOMMU", &v1beta1.PlatformConfig{InputOutputMemoryManagementUnitEnabled: lo.ToPtr(false)},
			`{"type":"HYPOTHETICAL_FUTURE_VM","isInputOutputMemoryManagementUnitEnabled":false}`},
		{"NUMA", &v1beta1.PlatformConfig{NumaNodesPerSocket: lo.ToPtr("NPS2")},
			`{"type":"HYPOTHETICAL_FUTURE_VM","numaNodesPerSocket":"NPS2"}`},
		{"enabled core percentage", &v1beta1.PlatformConfig{PercentageOfCoresEnabled: lo.ToPtr(25)},
			`{"type":"HYPOTHETICAL_FUTURE_VM","percentageOfCoresEnabled":25}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			payload := buildPlatformConfig(tt.request, platformType)
			body := launchRequestBody(t, ocicore.LaunchInstanceRequest{
				LaunchInstanceDetails: ocicore.LaunchInstanceDetails{PlatformConfig: payload},
			})
			require.JSONEq(t, tt.want, string(body["platformConfig"]))
		})
	}
}

// The generic builder must produce the same known-type wire payload as the pinned SDK.
func TestBuildPlatformConfig_KnownTypeParity(t *testing.T) {
	request := &v1beta1.PlatformConfig{
		SecureBootEnabled: lo.ToPtr(true), TrustedPlatformModuleEnabled: lo.ToPtr(true),
		MeasuredBootEnabled: lo.ToPtr(true), MemoryEncryptionEnabled: lo.ToPtr(false),
		SymmetricMultiThreadingEnabled: lo.ToPtr(false),
	}
	payload := buildPlatformConfig(request, ocicore.ShapePlatformConfigOptionsTypeAmdVm)
	got := launchRequestBody(t, ocicore.LaunchInstanceRequest{
		LaunchInstanceDetails: ocicore.LaunchInstanceDetails{PlatformConfig: payload},
	})
	sdk := launchRequestBody(t, ocicore.LaunchInstanceRequest{
		LaunchInstanceDetails: ocicore.LaunchInstanceDetails{PlatformConfig: ocicore.AmdVmLaunchInstancePlatformConfig{
			IsSecureBootEnabled: lo.ToPtr(true), IsTrustedPlatformModuleEnabled: lo.ToPtr(true),
			IsMeasuredBootEnabled: lo.ToPtr(true), IsMemoryEncryptionEnabled: lo.ToPtr(false),
			IsSymmetricMultiThreadingEnabled: lo.ToPtr(false),
		}},
	})

	require.JSONEq(t, string(sdk["platformConfig"]), string(got["platformConfig"]))
}

// Empty forms must omit the whole object.
func TestBuildPlatformConfig_Empty(t *testing.T) {
	for _, raw := range []string{"{}", `{
  "platformConfig":null
}`, `{
  "platformConfig":{}
}`, `{
  "platformConfig":{"secureBootEnabled":null}
}`} {
		t.Run(raw, func(t *testing.T) {
			var spec v1beta1.OCINodeClassSpec
			require.NoError(t, json.Unmarshal([]byte(raw), &spec))
			payload := buildPlatformConfig(spec.PlatformConfig, "")
			require.Nil(t, payload)
			body := launchRequestBody(t, ocicore.LaunchInstanceRequest{
				LaunchInstanceDetails: ocicore.LaunchInstanceDetails{PlatformConfig: payload},
			})
			require.NotContains(t, body, "platformConfig")
		})
	}
}

// Changes to any API pointer after construction must not change an already-built launch.
func TestBuildPlatformConfig_DoesNotAliasRequest(t *testing.T) {
	request := completePlatformRequest()
	payload := buildPlatformConfig(request, "HYPOTHETICAL_FUTURE_VM")
	*request.SecureBootEnabled = true
	*request.TrustedPlatformModuleEnabled = true
	*request.MeasuredBootEnabled = true
	*request.MemoryEncryptionEnabled = false
	*request.SymmetricMultiThreadingEnabled = true
	*request.AccessControlServiceEnabled = false
	*request.VirtualInstructionsEnabled = true
	*request.InputOutputMemoryManagementUnitEnabled = false
	*request.NumaNodesPerSocket = "NPS1"
	*request.PercentageOfCoresEnabled = 100
	body := launchRequestBody(t, ocicore.LaunchInstanceRequest{
		LaunchInstanceDetails: ocicore.LaunchInstanceDetails{PlatformConfig: payload},
	})
	require.JSONEq(t, completePlatformJSON, string(body["platformConfig"]))
}

// Capture serialized bytes inside Compute, since LastLaunchReq holds pointer aliases.
func platformLaunchProvider(t *testing.T) (*DefaultProvider, *fakes.FakeCompute, *[][]byte) {
	t.Helper()
	var snapshots [][]byte
	fc := &fakes.FakeCompute{}
	fc.OnLaunch = func(
		_ context.Context, request ocicore.LaunchInstanceRequest,
	) (ocicore.LaunchInstanceResponse, error) {
		snapshots = append(snapshots, launchRequestBytes(t, request))
		return ocicore.LaunchInstanceResponse{
			Instance: ocicore.Instance{Id: lo.ToPtr("ocid1.instance.oc1..platform-test")},
			Etag:     lo.ToPtr("etag-platform"),
			RawResponse: &http.Response{StatusCode: http.StatusOK,
				Header: http.Header{"Opc-Work-Request-Id": []string{"platform-wr"}}},
		}, nil
	}
	fwr := &fakes.FakeWorkRequest{}
	fwr.OnGet = func(_ context.Context, _ ociwr.GetWorkRequestRequest) (ociwr.GetWorkRequestResponse, error) {
		return ociwr.GetWorkRequestResponse{WorkRequest: ociwr.WorkRequest{
			Status:      ociwr.WorkRequestStatusSucceeded,
			TimeStarted: &common.SDKTime{Time: time.Now()}, TimeFinished: &common.SDKTime{Time: time.Now()},
		}}, nil
	}
	metadata, err := instancemeta.NewProvider(context.Background(), "10.0.0.1", []byte("CA"), ipV4SingleStack)
	require.NoError(t, err)
	return &DefaultProvider{
		computeClient: fc, workRequestClient: fwr, instanceMetaProvider: metadata,
		clusterCompartmentId: "ocid1.compartment.oc1..platform-test",
		launchTimeoutVM:      time.Minute, launchTimeoutBM: time.Minute, pollInterval: time.Millisecond,
	}, fc, &snapshots
}

func platformLaunchInstanceType(capacityType string) *instancetype.OciInstanceType {
	return &instancetype.OciInstanceType{
		Shape: testShapeE4Flex, PlatformType: "HYPOTHETICAL_FUTURE_VM",
		InstanceType: cloudprovider.InstanceType{Offerings: cloudprovider.Offerings{makeOfferingWithCapType(capacityType)}},
	}
}

func platformSnapshot(t *testing.T, snapshots *[][]byte, index int) map[string]json.RawMessage {
	t.Helper()
	require.Greater(t, len(*snapshots), index)
	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal((*snapshots)[index], &body))
	return body
}

func platformInputBytes(t *testing.T, input interface{}) []byte {
	t.Helper()
	body, err := json.Marshal(input)
	require.NoError(t, err)
	return body
}

// A Compute error and subsequent retry must preserve every requested setting and the complete wire body.
func TestProvider_LaunchInstance_PlatformConfigFailurePreservesRequest(t *testing.T) {
	p, fc, snapshots := platformLaunchProvider(t)
	computeErr := errors.New("injected Compute failure")
	fc.OnLaunch = func(
		_ context.Context, request ocicore.LaunchInstanceRequest,
	) (ocicore.LaunchInstanceResponse, error) {
		*snapshots = append(*snapshots, launchRequestBytes(t, request))
		return ocicore.LaunchInstanceResponse{}, computeErr
	}
	nodeClass := minimalNodeClass()
	nodeClass.Spec.PlatformConfig = completePlatformRequest()
	claim := minimalNodeClaim()
	proposal := minimalPlacement()
	instanceType := platformLaunchInstanceType(corev1.CapacityTypeOnDemand)
	classBefore := platformInputBytes(t, nodeClass)
	proposalBefore := platformInputBytes(t, proposal)
	claimBefore := platformInputBytes(t, claim)
	for attempt := 0; attempt < 2; attempt++ {
		_, err := p.LaunchInstance(context.Background(), claim, nodeClass, instanceType,
			minimalImageResolve(), minimalNetworkResolve(), nil, proposal)
		require.ErrorIs(t, err, computeErr)
		body := platformSnapshot(t, snapshots, attempt)
		require.JSONEq(t, completePlatformJSON, string(body["platformConfig"]))
		require.NotContains(t, body, "capacityReservationId")
		require.NotContains(t, body, "preemptibleInstanceConfig")
		require.Equal(t, classBefore, platformInputBytes(t, nodeClass))
		require.Equal(t, proposalBefore, platformInputBytes(t, proposal))
		require.Equal(t, claimBefore, platformInputBytes(t, claim))
	}
	require.EqualValues(t, 2, fc.LaunchCount.Get())
	require.Equal(t, (*snapshots)[0], (*snapshots)[1])
}
