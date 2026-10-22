/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package instancetype

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/coreos/go-semver/semver"
	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/cache"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	"github.com/oracle/karpenter-provider-oci/pkg/metrics"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/capacityreservation"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/identity"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	ociidentity "github.com/oracle/oci-go-sdk/v65/identity"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

var platformBooleanFields = []struct{ field, option string }{
	{"secureBootEnabled", "secureBootOptions"},
	{"trustedPlatformModuleEnabled", "trustedPlatformModuleOptions"},
	{"measuredBootEnabled", "measuredBootOptions"},
	{"memoryEncryptionEnabled", "memoryEncryptionOptions"},
	{"symmetricMultiThreadingEnabled", "symmetricMultiThreadingOptions"},
	{"accessControlServiceEnabled", "accessControlServiceOptions"},
	{"virtualInstructionsEnabled", "virtualInstructionsOptions"},
	{"inputOutputMemoryManagementUnitEnabled", "inputOutputMemoryManagementUnitOptions"},
}

func platformShapeFixture(t *testing.T, platformType, name, extras string) *ocicore.Shape {
	t.Helper()
	var options map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(`{
  "secureBootOptions":{"allowedValues":[false,true]},
  "trustedPlatformModuleOptions":{"allowedValues":[false,true]},
  "measuredBootOptions":{"allowedValues":[false,true]},
  "memoryEncryptionOptions":{"allowedValues":[false,true]},
  "symmetricMultiThreadingOptions":{"allowedValues":[false,true]}
}`), &options))
	var extra map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(extras), &extra))
	for field, value := range extra {
		options[field] = value
	}
	typeJSON, err := json.Marshal(platformType)
	require.NoError(t, err)
	options["type"] = typeJSON
	encoded, err := json.Marshal(options)
	require.NoError(t, err)
	shape := &ocicore.Shape{Shape: lo.ToPtr(name), PlatformConfigOptions: &ocicore.ShapePlatformConfigOptions{}}
	require.NoError(t, json.Unmarshal(encoded, shape.PlatformConfigOptions))
	return shape
}

func platformRequestFixture(t *testing.T, requestJSON string) *v1beta1.PlatformConfig {
	t.Helper()
	var request *v1beta1.PlatformConfig
	require.NoError(t, json.Unmarshal([]byte(requestJSON), &request))
	return request
}

func TestEvaluatePlatformConfig_EmptyAndMissingMetadata(t *testing.T) {
	// These cases catch accidental filtering of equivalent empty requests, including
	// nil metadata and a nil shape before callers have resolved a shape.
	for _, requestJSON := range []string{"null", "{}", `{"secureBootEnabled":null,"percentageOfCoresEnabled":null}`} {
		t.Run("empty/"+requestJSON, func(t *testing.T) {
			request := platformRequestFixture(t, requestJSON)
			for _, shape := range []*ocicore.Shape{nil, {}, {PlatformConfigOptions: &ocicore.ShapePlatformConfigOptions{}}} {
				got := evaluatePlatformConfig(shape, request)
				require.True(t, got.compatible)
				require.Empty(t, got.reason)
				require.Empty(t, got.field)
			}
		})
	}
	for _, shape := range []*ocicore.Shape{nil, {}, {PlatformConfigOptions: &ocicore.ShapePlatformConfigOptions{}}} {
		got := evaluatePlatformConfig(shape, &v1beta1.PlatformConfig{SecureBootEnabled: lo.ToPtr(false)})
		require.False(t, got.compatible)
		require.Equal(t, "missing_platform_metadata", got.reason)
		require.Empty(t, got.field)
	}

}

func TestEvaluatePlatformConfig_BooleanCapabilities(t *testing.T) {
	for _, field := range platformBooleanFields {
		for _, tc := range []struct {
			name, metadata    string
			value, compatible bool
			reason            string
		}{
			{"missing", "null", false, false, "field_not_supported"},
			{"empty", `{"allowedValues":[]}`, false, false, "field_not_supported"},
			{"unset_list", `{}`, false, false, "field_not_supported"},
			{"default_without_list", `{"isDefaultEnabled":false}`, false, false, "field_not_supported"},
			{"true_overrides_false_default", `{"allowedValues":[false,true],"isDefaultEnabled":false}`, true, true, ""},
			{"false_only_accepts_false", `{"allowedValues":[false],"isDefaultEnabled":true}`, false, true, ""},
			{
				"false_only_rejects_true",
				`{"allowedValues":[false],"isDefaultEnabled":true}`,
				true,
				false,
				"unsupported_value",
			},
			{
				"true_only_rejects_false",
				`{"allowedValues":[true],"isDefaultEnabled":false}`,
				false,
				false,
				"unsupported_value",
			},
		} {
			t.Run(field.field+"/"+tc.name, func(t *testing.T) {
				shape := platformShapeFixture(t,
					"HYPOTHETICAL_FUTURE_VM",
					"VM.Future",
					fmt.Sprintf(`{%q:%s}`,
						field.option,
						tc.metadata))
				request := platformRequestFixture(t, fmt.Sprintf(`{%q:%t}`, field.field, tc.value))
				requestBefore := request.DeepCopy()
				metadataBefore, err := json.Marshal(shape.PlatformConfigOptions)
				require.NoError(t, err)
				got := evaluatePlatformConfig(shape, request)
				require.Equal(t, requestBefore, request)
				metadataAfter, err := json.Marshal(shape.PlatformConfigOptions)
				require.NoError(t, err)
				require.JSONEq(t, string(metadataBefore), string(metadataAfter))
				require.Equal(t, tc.compatible, got.compatible)
				require.Equal(t, tc.reason, got.reason)
				if tc.compatible {
					require.Empty(t, got.field)
				} else {
					require.Equal(t, field.field, got.field)
				}
			})
		}
	}

}

func TestEvaluatePlatformConfig_NumaCapabilities(t *testing.T) {
	for _, tc := range []struct{ name, metadata, value, reason string }{
		{"missing", `null`, `"NPS1"`, "field_not_supported"},
		{"empty", `{"allowedValues":[]}`, `"NPS1"`, "field_not_supported"},
		{"unset_list", `{}`, `"NPS1"`, "field_not_supported"},
		{"unsupported", `{"allowedValues":["NPS1"]}`, `"NPS2"`, "unsupported_value"},
		{"exact_case", `{"allowedValues":["NPS1"]}`, `"nps1"`, "unsupported_value"},
		{"NPS6", `{"allowedValues":["NPS0","NPS1","NPS2","NPS4","NPS6"]}`, `"NPS6"`, ""},
	} {
		t.Run("numa/"+tc.name, func(t *testing.T) {
			shape := platformShapeFixture(t,
				"HYPOTHETICAL_FUTURE_VM",
				"VM.Future",
				fmt.Sprintf(`{"numaNodesPerSocketPlatformOptions":%s}`,
					tc.metadata))
			got := evaluatePlatformConfig(shape, platformRequestFixture(t, fmt.Sprintf(`{"numaNodesPerSocket":%s}`, tc.value)))
			require.Equal(t, tc.reason == "", got.compatible)
			require.Equal(t, tc.reason, got.reason)
			if tc.reason != "" {
				require.Equal(t, "numaNodesPerSocket", got.field)
			}
		})
	}
}

func TestEvaluatePlatformConfig_PercentageCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name, metadata string
		value          int
		reason         string
	}{
		{"missing", `null`, 50, "field_not_supported"},
		{"no_bounds", `{}`, 50, "invalid_field_metadata"},
		{"no_min", `{"max":100}`, 50, "invalid_field_metadata"},
		{"no_max", `{"min":25}`, 50, "invalid_field_metadata"},
		{"reversed", `{"min":100,"max":25}`, 50, "invalid_field_metadata"},
		{"lower_endpoint", `{"min":25,"max":100}`, 25, ""},
		{"upper_endpoint", `{"min":25,"max":100}`, 100, ""},
		{"same_bounds", `{"min":50,"max":50}`, 50, ""},
		{"below_shape_range", `{"min":50,"max":75}`, 25, "unsupported_value"},
		{"above_shape_range", `{"min":50,"max":75}`, 100, "unsupported_value"},
	} {
		t.Run("percentage/"+tc.name, func(t *testing.T) {
			shape := platformShapeFixture(t,
				"HYPOTHETICAL_FUTURE_VM",
				"VM.Future",
				fmt.Sprintf(`{"percentageOfCoresEnabledOptions":%s}`,
					tc.metadata))
			got := evaluatePlatformConfig(shape, &v1beta1.PlatformConfig{PercentageOfCoresEnabled: lo.ToPtr(tc.value)})
			require.Equal(t, tc.reason == "", got.compatible)
			require.Equal(t, tc.reason, got.reason)
			if tc.reason != "" {
				require.Equal(t, "percentageOfCoresEnabled", got.field)
			}
		})
	}

}

func TestEvaluatePlatformConfig_EffectiveValues(t *testing.T) {
	for _, tc := range []struct {
		name, options, request string
		wantVCPU               float32
	}{
		{
			"defaults_without_allowed_lists",
			`{
  "memoryEncryptionOptions":{"isDefaultEnabled":true},
  "symmetricMultiThreadingOptions":{"isDefaultEnabled":false},
  "percentageOfCoresEnabledOptions":{"defaultValue":37}
}`,
			`{"secureBootEnabled":false}`,
			3,
		},
		{
			"explicit_false_overrides_defaults",
			`{
  "memoryEncryptionOptions":{"allowedValues":[false,true],"isDefaultEnabled":true},
  "symmetricMultiThreadingOptions":{"allowedValues":[false,true],"isDefaultEnabled":true},
  "percentageOfCoresEnabledOptions":{"min":25,"max":100,"defaultValue":25}
}`,
			`{
  "memoryEncryptionEnabled":false,
  "symmetricMultiThreadingEnabled":false,
  "percentageOfCoresEnabled":50
}`,
			4,
		},
		{
			"nil_options",
			`{
  "memoryEncryptionOptions":null,
  "symmetricMultiThreadingOptions":null,
  "percentageOfCoresEnabledOptions":null
}`,
			`{"secureBootEnabled":false}`,
			16,
		},
		{
			"nil_default_pointers",
			`{
  "memoryEncryptionOptions":{"isDefaultEnabled":null},
  "symmetricMultiThreadingOptions":{"isDefaultEnabled":null},
  "percentageOfCoresEnabledOptions":{"defaultValue":null}
}`,
			`{"secureBootEnabled":false}`,
			16,
		},
		{
			"singleton_values_not_defaults",
			`{
  "memoryEncryptionOptions":{"allowedValues":[true]},
  "symmetricMultiThreadingOptions":{"allowedValues":[false]},
  "percentageOfCoresEnabledOptions":{"min":50,"max":50}
}`,
			`{"secureBootEnabled":false}`,
			16,
		},
		{
			"percentage_lower_endpoint",
			`{"percentageOfCoresEnabledOptions":{"min":25,"max":50,"defaultValue":1}}`,
			`{"secureBootEnabled":false}`,
			2,
		},
		{
			"nil_bypass",
			`{"symmetricMultiThreadingOptions":{"isDefaultEnabled":false}}`,
			`null`,
			16,
		},
		{
			"empty_bypass",
			`{
  "memoryEncryptionOptions":{"isDefaultEnabled":true},
  "symmetricMultiThreadingOptions":{"isDefaultEnabled":false},
  "percentageOfCoresEnabledOptions":{"defaultValue":50}
}`,
			`{}`,
			16,
		},
	} {
		t.Run("effective_values/"+tc.name, func(t *testing.T) {
			shape := platformShapeFixture(t, "AMD_VM", "VM.Future", tc.options)
			request := platformRequestFixture(t, tc.request)
			before := request.DeepCopy()
			got := evaluatePlatformConfig(shape, request)
			require.True(t, got.compatible)
			p, class, sa := platformCatalogueFixture(t)
			class.Spec.PlatformConfig = request
			class.Spec.ShapeConfigs = class.Spec.ShapeConfigs[1:]
			sa.Shape.PlatformConfigOptions = shape.PlatformConfigOptions
			candidates := platformCatalogue(t, context.Background(), p, class)
			require.Len(t, candidates, 1)
			require.Equal(t, int64(tc.wantVCPU*1000), candidates[0].Capacity.Cpu().MilliValue())
			require.Equal(t, before, request)
		})
	}

}

func TestEvaluatePlatformConfig_FirstFailureInAPIOrder(t *testing.T) {
	// Make every requested value unsupported, then permit each preceding field.
	// Any reordered check returns a later diagnostic instead of the next field.
	t.Run("first_failure_in_api_order", func(t *testing.T) {
		options := map[string]any{}
		request := map[string]any{}
		fields := append([]struct{ field, option string }{}, platformBooleanFields...)
		fields = append(fields,
			struct {
				field,
				option string
			}{"numaNodesPerSocket",
				"numaNodesPerSocketPlatformOptions"},
			struct {
				field,
				option string
			}{"percentageOfCoresEnabled",
				"percentageOfCoresEnabledOptions"})
		for _, field := range platformBooleanFields {
			options[field.option] = map[string]any{"allowedValues": []bool{true}}
			request[field.field] = false
		}
		options["numaNodesPerSocketPlatformOptions"] = map[string]any{"allowedValues": []string{"NPS2"}}
		options["percentageOfCoresEnabledOptions"] = map[string]any{"min": 75, "max": 100}
		request["numaNodesPerSocket"], request["percentageOfCoresEnabled"] = "NPS1", 50
		for _, field := range fields {
			optionJSON, err := json.Marshal(options)
			require.NoError(t, err)
			requestJSON, err := json.Marshal(request)
			require.NoError(t, err)
			shape := platformShapeFixture(t, "HYPOTHETICAL_FUTURE_VM", "VM.Future", string(optionJSON))
			got := evaluatePlatformConfig(shape, platformRequestFixture(t, string(requestJSON)))
			require.False(t, got.compatible)
			require.Equal(t, "unsupported_value", got.reason)
			require.Equal(t, field.field, got.field)
			switch field.field {
			case "numaNodesPerSocket":
				options[field.option] = map[string]any{"allowedValues": []string{"NPS1"}}
			case "percentageOfCoresEnabled":
				options[field.option] = map[string]any{"min": 25, "max": 100}
			default:
				options[field.option] = map[string]any{"allowedValues": []bool{false}}
			}
		}
	})
}

func TestEvaluatePlatformConfig_ShieldedCombinations(t *testing.T) {
	for _, tc := range []struct{ name, shape, platformType, request, reason string }{
		{"vm_tpm_true_measured_false", "VM.Standard.E4.Flex", "AMD_VM", `{
  "trustedPlatformModuleEnabled":true,
  "measuredBootEnabled":false
}`, "unsupported_shielded_combination"},
		{"vm_tpm_false_measured_true", "VM.Standard3.Flex", "INTEL_VM", `{
  "trustedPlatformModuleEnabled":false,
  "measuredBootEnabled":true
}`, "unsupported_shielded_combination"},
		{"vm_tpm_only_unknown_default", "VM.Standard.E4.Flex", "AMD_VM", `{"trustedPlatformModuleEnabled":true}`, ""},
		{"vm_measured_only_unknown_default", "VM.Standard3.Flex", "INTEL_VM", `{"measuredBootEnabled":true}`, ""},
		{"vm_secure_only", "VM.Standard.E4.Flex", "AMD_VM", `{"secureBootEnabled":true}`, ""},
		{"vm_tpm_and_measured", "VM.Standard.E4.Flex", "AMD_VM", `{
  "trustedPlatformModuleEnabled":true,
  "measuredBootEnabled":true
}`, ""},
		{"vm_both_disabled", "VM.Standard.E4.Flex", "AMD_VM", `{
  "trustedPlatformModuleEnabled":false,
  "measuredBootEnabled":false
}`, ""},
		{"bm_tpm_only", "BM.Standard.E4.128", "AMD_MILAN_BM", `{"trustedPlatformModuleEnabled":true}`, ""},
		{"bm_secure_and_tpm", "BM.Standard.E4.128", "AMD_MILAN_BM", `{
  "secureBootEnabled":true,
  "trustedPlatformModuleEnabled":true,
  "measuredBootEnabled":false
}`, ""},
		{"bm_measured_only", "BM.Standard2.52", "INTEL_SKYLAKE_BM", `{
  "measuredBootEnabled":true
}`, "unsupported_shielded_combination"},
		{"shape_classification_over_type", "BM.Future", "HYPOTHETICAL_FUTURE_VM", `{
  "measuredBootEnabled":true
}`, "unsupported_shielded_combination"},
		{"vm_classification_over_type", "VM.Future", "GENERIC_BM", `{"measuredBootEnabled":true}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shape := platformShapeFixture(t, tc.platformType, tc.shape, `{}`)
			request := platformRequestFixture(t, tc.request)
			before := request.DeepCopy()
			got := evaluatePlatformConfig(shape, request)
			require.Equal(t, tc.reason == "", got.compatible)
			require.Equal(t, tc.reason, got.reason)
			if tc.reason != "" {
				require.Equal(t, "measuredBootEnabled", got.field)
			} else {
				require.Empty(t, got.field)
			}
			require.Equal(t, before, request)
		})
	}
	// Defaults can prove a conflict, but missing defaults and singleton allowed
	// lists cannot. Each request must stay untouched when a default is consumed.
	for _, tc := range []struct{ name, shape, options, request, reason string }{
		{
			"vm_omitted_measured",
			"VM.Future",
			`{"measuredBootOptions":{"isDefaultEnabled":false}}`,
			`{"trustedPlatformModuleEnabled":true}`,
			"unsupported_shielded_combination",
		},
		{
			"vm_omitted_tpm",
			"VM.Future",
			`{"trustedPlatformModuleOptions":{"isDefaultEnabled":false}}`,
			`{"measuredBootEnabled":true}`,
			"unsupported_shielded_combination",
		},
		{
			"vm_both_default_mismatch",
			"VM.Future",
			`{
  "trustedPlatformModuleOptions":{"isDefaultEnabled":true},
  "measuredBootOptions":{"isDefaultEnabled":false}
}`,
			`{"secureBootEnabled":false}`,
			"unsupported_shielded_combination",
		},
		{
			"vm_equal_true_defaults",
			"VM.Future",
			`{
  "trustedPlatformModuleOptions":{"isDefaultEnabled":true},
  "measuredBootOptions":{"isDefaultEnabled":true}
}`,
			`{"secureBootEnabled":false}`,
			"",
		},
		{
			"vm_default_mismatch_secure_true",
			"VM.Future",
			`{
  "trustedPlatformModuleOptions":{"isDefaultEnabled":true},
  "measuredBootOptions":{"isDefaultEnabled":false}
}`,
			`{"secureBootEnabled":true}`,
			"unsupported_shielded_combination",
		},
		{
			"vm_equal_false_defaults_secure_true",
			"VM.Future",
			`{
  "trustedPlatformModuleOptions":{"isDefaultEnabled":false},
  "measuredBootOptions":{"isDefaultEnabled":false}
}`,
			`{"secureBootEnabled":true}`,
			"",
		},
		{
			"vm_explicit_false_overrides_tpm_default",
			"VM.Future",
			`{
  "trustedPlatformModuleOptions":{"allowedValues":[false,true],"isDefaultEnabled":true},
  "measuredBootOptions":{"isDefaultEnabled":false}
}`,
			`{"trustedPlatformModuleEnabled":false}`,
			"",
		},
		{
			"vm_explicit_false_overrides_measured_default",
			"VM.Future",
			`{
  "measuredBootOptions":{"allowedValues":[false,true],"isDefaultEnabled":true},
  "trustedPlatformModuleOptions":{"isDefaultEnabled":false}
}`,
			`{"measuredBootEnabled":false}`,
			"",
		},
		{
			"vm_nil_options_unknown",
			"VM.Future",
			`{"measuredBootOptions":null}`,
			`{"trustedPlatformModuleEnabled":true}`,
			"",
		},
		{
			"vm_nil_default_unknown",
			"VM.Future",
			`{"measuredBootOptions":{"isDefaultEnabled":null}}`,
			`{"trustedPlatformModuleEnabled":true}`,
			"",
		},
		{
			"vm_singleton_not_default",
			"VM.Future",
			`{"measuredBootOptions":{"allowedValues":[false]}}`,
			`{"trustedPlatformModuleEnabled":true}`,
			"",
		},
		{
			"vm_tpm_singleton_not_default",
			"VM.Future",
			`{"trustedPlatformModuleOptions":{"allowedValues":[false]}}`,
			`{"measuredBootEnabled":true}`,
			"",
		},
		{
			"bm_measured_default_true",
			"BM.Future",
			`{"measuredBootOptions":{"isDefaultEnabled":true}}`,
			`{"secureBootEnabled":false}`,
			"unsupported_shielded_combination",
		},
		{
			"bm_measured_default_false",
			"BM.Future",
			`{
  "measuredBootOptions":{"isDefaultEnabled":false},
  "trustedPlatformModuleOptions":{"isDefaultEnabled":true}
}`,
			`{"secureBootEnabled":true}`,
			"",
		},
		{
			"bm_measured_nil_options",
			"BM.Future",
			`{"measuredBootOptions":null}`,
			`{"secureBootEnabled":true,"trustedPlatformModuleEnabled":true}`,
			"",
		},
		{
			"bm_measured_false_overrides_default",
			"BM.Future",
			`{"measuredBootOptions":{"allowedValues":[false,true],"isDefaultEnabled":true}}`,
			`{"measuredBootEnabled":false}`,
			"",
		},
		{
			"confidential_secure_explicit",
			"VM.Future",
			`{}`,
			`{"memoryEncryptionEnabled":true,"secureBootEnabled":true}`,
			"confidential_shielded_conflict",
		},
		{
			"confidential_tpm_explicit_unknown_measured",
			"VM.Future",
			`{}`,
			`{"memoryEncryptionEnabled":true,"trustedPlatformModuleEnabled":true}`,
			"confidential_shielded_conflict",
		},
		{
			"confidential_measured_explicit_unknown_tpm",
			"VM.Future",
			`{}`,
			`{"memoryEncryptionEnabled":true,"measuredBootEnabled":true}`,
			"confidential_shielded_conflict",
		},
		{
			"confidential_secure_default_unknown_pair",
			"VM.Future",
			`{"secureBootOptions":{"isDefaultEnabled":true}}`,
			`{"memoryEncryptionEnabled":true}`,
			"confidential_shielded_conflict",
		},
		{
			"confidential_tpm_default_unknown_measured",
			"VM.Future",
			`{"trustedPlatformModuleOptions":{"isDefaultEnabled":true}}`,
			`{"memoryEncryptionEnabled":true}`,
			"confidential_shielded_conflict",
		},
		{
			"confidential_measured_default_unknown_tpm",
			"VM.Future",
			`{"measuredBootOptions":{"isDefaultEnabled":true}}`,
			`{"memoryEncryptionEnabled":true}`,
			"confidential_shielded_conflict",
		},
		{
			"confidential_default_secure_explicit",
			"VM.Future",
			`{"memoryEncryptionOptions":{"isDefaultEnabled":true}}`,
			`{"secureBootEnabled":true}`,
			"confidential_shielded_conflict",
		},
		{
			"confidential_and_secure_both_defaults",
			"VM.Future",
			`{
  "memoryEncryptionOptions":{"isDefaultEnabled":true},
  "secureBootOptions":{"isDefaultEnabled":true}
}`,
			`{"symmetricMultiThreadingEnabled":false}`,
			"confidential_shielded_conflict",
		},
		{
			"confidential_false_overrides_default",
			"VM.Future",
			`{"memoryEncryptionOptions":{"allowedValues":[false,true],"isDefaultEnabled":true}}`,
			`{"memoryEncryptionEnabled":false,"secureBootEnabled":true}`,
			"",
		},
		{
			"secure_false_overrides_default",
			"VM.Future",
			`{"secureBootOptions":{"allowedValues":[false,true],"isDefaultEnabled":true}}`,
			`{"memoryEncryptionEnabled":true,"secureBootEnabled":false}`,
			"",
		},
		{"unknown_shielded_does_not_reject", "VM.Future", `{}`, `{"memoryEncryptionEnabled":true}`, ""},
		{
			"unknown_encryption_does_not_reject",
			"VM.Future",
			`{"memoryEncryptionOptions":null}`,
			`{"secureBootEnabled":true}`,
			"",
		},
		{
			"singleton_secure_not_default",
			"VM.Future",
			`{"secureBootOptions":{"allowedValues":[true]}}`,
			`{"memoryEncryptionEnabled":true}`,
			"",
		},
		{
			"singleton_encryption_not_default",
			"VM.Future",
			`{"memoryEncryptionOptions":{"allowedValues":[true]}}`,
			`{"secureBootEnabled":true}`,
			"",
		},
		{
			"shielded_precedes_confidential",
			"VM.Future",
			`{"memoryEncryptionOptions":{"isDefaultEnabled":true}}`,
			`{"trustedPlatformModuleEnabled":true,"measuredBootEnabled":false}`,
			"unsupported_shielded_combination",
		},
		{
			"capability_precedes_default_conflict",
			"VM.Future",
			`{
  "memoryEncryptionOptions":{"isDefaultEnabled":true},
  "symmetricMultiThreadingOptions":{"allowedValues":[true]}
}`,
			`{"secureBootEnabled":true,"symmetricMultiThreadingEnabled":false}`,
			"unsupported_value",
		},
		{
			"empty_bypasses_conflicting_defaults",
			"VM.Future",
			`{
  "memoryEncryptionOptions":{"isDefaultEnabled":true},
  "secureBootOptions":{"isDefaultEnabled":true},
  "trustedPlatformModuleOptions":{"isDefaultEnabled":true},
  "measuredBootOptions":{"isDefaultEnabled":false}
}`,
			`{}`,
			"",
		},
		{"null_bypasses_bm_default", "BM.Future", `{"measuredBootOptions":{"isDefaultEnabled":true}}`, `null`, ""},
		{
			"unused_options_absent",
			"VM.Future",
			`{
  "accessControlServiceOptions":null,
  "virtualInstructionsOptions":null,
  "inputOutputMemoryManagementUnitOptions":null,
  "numaNodesPerSocketPlatformOptions":null
}`,
			`{"secureBootEnabled":false}`,
			"",
		},
		{
			"unused_options_defaults_no_rules",
			"VM.Future",
			`{
  "accessControlServiceOptions":{"isDefaultEnabled":true},
  "virtualInstructionsOptions":{"isDefaultEnabled":true},
  "inputOutputMemoryManagementUnitOptions":{"isDefaultEnabled":true},
  "numaNodesPerSocketPlatformOptions":{"defaultValue":"NPS_INVALID"}
}`,
			`{"memoryEncryptionEnabled":true}`,
			"",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shape := platformShapeFixture(t, "HYPOTHETICAL_FUTURE_VM", tc.shape, tc.options)
			request := platformRequestFixture(t, tc.request)
			before := request.DeepCopy()
			got := evaluatePlatformConfig(shape, request)
			require.Equal(t, tc.reason == "", got.compatible)
			require.Equal(t, tc.reason, got.reason)
			if tc.reason != "" {
				field := "measuredBootEnabled"
				if tc.reason == "confidential_shielded_conflict" {
					field = "memoryEncryptionEnabled"
				}
				if tc.reason == "unsupported_value" {
					field = "symmetricMultiThreadingEnabled"
				}
				require.Equal(t, field, got.field)
			}
			require.Equal(t, before, request)
		})
	}

	for _, shapeName := range []string{"VM.Future", "BM.Future"} {
		t.Run("capability_before_combination/"+shapeName, func(t *testing.T) {
			shape := platformShapeFixture(t, "HYPOTHETICAL_FUTURE_VM", shapeName, `{
  "symmetricMultiThreadingOptions":{"allowedValues":[false]}
}`)
			got := evaluatePlatformConfig(shape,
				&v1beta1.PlatformConfig{TrustedPlatformModuleEnabled: lo.ToPtr(false),
					MeasuredBootEnabled:            lo.ToPtr(true),
					SymmetricMultiThreadingEnabled: lo.ToPtr(true)})
			require.False(t, got.compatible)
			require.Equal(t, "unsupported_value", got.reason)
			require.Equal(t, "symmetricMultiThreadingEnabled", got.field)
		})
	}
}

const platformCatalogueShapeName = "VM.Standard.E4.Flex"

func platformCatalogueFixture(t *testing.T) (*DefaultProvider, *v1beta1.OCINodeClass, *ShapeAndAd) {
	t.Helper()
	shape := platformShapeFixture(t, "HYPOTHETICAL_FUTURE_VM", platformCatalogueShapeName,
		`{"percentageOfCoresEnabledOptions":{"min":25,"max":100}}`)
	shape.IsFlexible = lo.ToPtr(true)
	shape.Ocpus, shape.MemoryInGBs = lo.ToPtr(float32(8)), lo.ToPtr(float32(32))
	shape.OcpuOptions = &ocicore.ShapeOcpuOptions{Min: lo.ToPtr(float32(1)), Max: lo.ToPtr(float32(64))}
	shape.MemoryOptions = &ocicore.ShapeMemoryOptions{
		MinInGBs: lo.ToPtr(float32(1)), MaxInGBs: lo.ToPtr(float32(512)), DefaultPerOcpuInGBs: lo.ToPtr(float32(4)),
	}
	sa := &ShapeAndAd{Shape: shape, Ads: []string{"test:AD-1", "test:AD-2"}}
	p := newProvider(func(p *DefaultProvider) {
		p.shapeAdMap[platformCatalogueShapeName] = sa
		p.shapeToPrice["VM.STANDARD.E4.FLEX"] = &ShapePriceInfo{OcpuUnitPrice: 0.125, MemoryUnitPrice: 0.03125}
		p.preemptibleShapes["VM.STANDARD.E4"] = platformCatalogueShapeName
		p.k8sVersion = semver.New("1.31.0")
		p.ipFamilies = ipV4SingleStack
		p.vmMemoryOverhead = defaultVMMemoryOverhead
	})
	class := &v1beta1.OCINodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-test"},
		Spec: v1beta1.OCINodeClassSpec{
			NetworkConfig: &v1beta1.NetworkConfig{},
			VolumeConfig: &v1beta1.VolumeConfig{BootVolumeConfig: &v1beta1.BootVolumeConfig{
				VolumeAttribute: v1beta1.VolumeAttribute{SizeInGBs: lo.ToPtr(int64(100))},
			}},
			ShapeConfigs: []*v1beta1.ShapeConfig{
				{Ocpus: lo.ToPtr(float32(4)), MemoryInGbs: lo.ToPtr(float32(16))},
				{Ocpus: lo.ToPtr(float32(8)), MemoryInGbs: lo.ToPtr(float32(32))},
			},
			KubeletConfig: &v1beta1.KubeletConfiguration{PodsPerCore: lo.ToPtr(int32(10))},
		},
	}
	return p, class, sa
}

func cloneOfferingRequirements(offerings cloudprovider.Offerings) []scheduling.Requirements {
	result := make([]scheduling.Requirements, len(offerings))
	for i, offering := range offerings {
		entries := offering.Requirements.NodeSelectorRequirements()
		copied := make([]corev1.NodeSelectorRequirementWithMinValues, len(entries))
		for j := range entries {
			copied[j] = *entries[j].DeepCopy()
		}
		result[i] = scheduling.NewNodeSelectorRequirementsWithMinValues(copied...)
	}
	return result
}

func platformCatalogue(t *testing.T, ctx context.Context, p *DefaultProvider,
	class *v1beta1.OCINodeClass) []*OciInstanceType {
	t.Helper()
	types, err := p.ListInstanceTypes(ctx, class, []v1.Taint{preemptibleTaintNoSchedule})
	require.NoError(t, err)
	require.NotEmpty(t, types)
	return types
}

func TestPlatformConfigCatalogue(t *testing.T) {
	t.Run("security_request_uses_smt_default_resources", func(t *testing.T) {
		p, class, sa := platformCatalogueFixture(t)
		baseline := platformCatalogue(t, context.Background(), p, class)
		sa.Shape.PlatformConfigOptions.SymmetricMultiThreadingOptions.IsDefaultEnabled = lo.ToPtr(false)
		class.Spec.PlatformConfig = &v1beta1.PlatformConfig{SecureBootEnabled: lo.ToPtr(false)}
		before := class.DeepCopy()
		advisor := newFakeAdvisor()
		measured := resource.MustParse("12345Mi")
		for _, candidate := range baseline {
			advisor.measure(candidate.Name, measured)
		}
		p.capacityAdvisor = advisor
		current := platformCatalogue(t, context.Background(), p, class)
		require.Len(t, current, len(baseline))
		for i, candidate := range current {
			require.Equal(t, baseline[i].Ocpu, candidate.Ocpu)
			require.Equal(t, baseline[i].MemoryInGbs, candidate.MemoryInGbs)
			require.Equal(t, baseline[i].Offerings, candidate.Offerings)
			require.Equal(t, measured.Value(), candidate.Capacity.Memory().Value())
			require.Equal(t, baseline[i].Capacity.Cpu().MilliValue()/2, candidate.Capacity.Cpu().MilliValue())
			if *candidate.Ocpu == 8 {
				require.Equal(t, int64(8), candidate.Capacity.Cpu().Value())
				require.Equal(t, int64(80), candidate.Capacity.Pods().Value())
				require.Equal(t, int64(97), candidate.Overhead.KubeReserved.Cpu().MilliValue())
				allocatable := candidate.Allocatable()
				require.Equal(t, int64(7803), allocatable.Cpu().MilliValue())
			}
		}
		require.Equal(t, before, class)
	})
	t.Run("combined_cpu_settings_preserve_measured_memory_and_price", func(t *testing.T) {
		p, class, _ := platformCatalogueFixture(t)
		class.Spec.ShapeConfigs = class.Spec.ShapeConfigs[1:]
		baseline := platformCatalogue(t, context.Background(), p, class)
		require.Len(t, baseline, 1)
		advisor := newFakeAdvisor()
		measured := resource.MustParse("12345Mi")
		advisor.measure(baseline[0].Name, measured)
		p.capacityAdvisor = advisor
		class.Spec.PlatformConfig = &v1beta1.PlatformConfig{
			SymmetricMultiThreadingEnabled: lo.ToPtr(false), PercentageOfCoresEnabled: lo.ToPtr(50),
		}
		current := platformCatalogue(t, context.Background(), p, class)
		require.Len(t, current, 1)
		candidate := current[0]
		require.Equal(t, int64(4), candidate.Capacity.Cpu().Value())
		require.Equal(t, int64(40), candidate.Capacity.Pods().Value())
		require.Equal(t, int64(85), candidate.Overhead.KubeReserved.Cpu().MilliValue())
		allocatable := candidate.Allocatable()
		require.Equal(t, int64(3815), allocatable.Cpu().MilliValue())
		require.Equal(t, measured.Value(), candidate.Capacity.Memory().Value())
		require.Equal(t, baseline[0].Offerings, candidate.Offerings)
		require.Equal(t, []string{platformCatalogueShapeName}, advisor.asked)
	})

	// Fixed and flexible candidates use the requested configuration for resources
	// and remain in the catalogue when their offerings are incompatible.
	for _, flexible := range []bool{false, true} {
		t.Run(lo.Ternary(flexible, "flexible", "fixed"), func(t *testing.T) {
			p, class, sa := platformCatalogueFixture(t)
			sa.Shape.IsFlexible = lo.ToPtr(flexible)
			baseline := platformCatalogue(t, context.Background(), p, class)
			class.Spec.PlatformConfig = &v1beta1.PlatformConfig{SymmetricMultiThreadingEnabled: lo.ToPtr(false)}
			original := class.DeepCopy()
			compatible := platformCatalogue(t, context.Background(), p, class)
			require.Len(t, compatible, len(baseline))
			for i, candidate := range compatible {
				require.Equal(t, "HYPOTHETICAL_FUTURE_VM", string(candidate.PlatformType))
				require.Equal(t, baseline[i].Capacity.Cpu().Value()/2, candidate.Capacity.Cpu().Value())
				require.Equal(t, candidate.Capacity.Cpu().Value()*10, candidate.Capacity.Pods().Value())
				require.Less(t, candidate.Overhead.KubeReserved.Cpu().MilliValue(),
					baseline[i].Overhead.KubeReserved.Cpu().MilliValue())
				require.Equal(t, baseline[i].Ocpu, candidate.Ocpu)
				require.Equal(t, baseline[i].MemoryInGbs, candidate.MemoryInGbs)
				require.Equal(t, baseline[i].Offerings, candidate.Offerings)
			}
			require.Equal(t, original, class)

			// Unsupported SMT retains the baseline resources and per-offering requirements,
			// including spot entries, while aggregate requirements reflect unavailable capacity.
			sa.Shape.PlatformConfigOptions.SymmetricMultiThreadingOptions.AllowedValues = []bool{true}
			incompatible := platformCatalogue(t, context.Background(), p, class)
			require.Len(t, incompatible, len(baseline))
			for i, candidate := range incompatible {
				require.Empty(t, candidate.Offerings.Available())
				for _, offering := range candidate.Offerings {
					metric, found := fakes.FindMetricWithLabelValues(t,
						"instance_type_offering_available", map[string]string{
							metrics.InstanceTypeLabel: candidate.Name,
							metrics.CapacityTypeLabel: offering.CapacityType(), metrics.ZoneLabel: offering.Zone(),
						})
					require.True(t, found)
					require.Equal(t, float64(0), metric.GetGauge().GetValue())
				}
				require.Equal(t, baseline[i].Capacity, candidate.Capacity)
				require.Equal(t, baseline[i].Overhead, candidate.Overhead)
				expected := scheduling.NewRequirements(baseline[i].Requirements.Values()...)
				expected[corev1.CapacityTypeLabelKey] = scheduling.NewRequirement(corev1.CapacityTypeLabelKey, v1.NodeSelectorOpIn)
				require.Equal(t, expected, candidate.Requirements)
				require.Equal(t, cloneOfferingRequirements(baseline[i].Offerings), cloneOfferingRequirements(candidate.Offerings))
			}
		})
	}
	for _, tc := range []struct{ name, options, request string }{
		{
			"shielded_default",
			`{
  "measuredBootOptions":{"isDefaultEnabled":false},
  "symmetricMultiThreadingOptions":{"isDefaultEnabled":false}
}`,
			`{"trustedPlatformModuleEnabled":true}`,
		},
		{
			"confidential_default",
			`{
  "memoryEncryptionOptions":{"isDefaultEnabled":true},
  "percentageOfCoresEnabledOptions":{"defaultValue":50}
}`,
			`{"secureBootEnabled":true}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, class, sa := platformCatalogueFixture(t)
			baseline := platformCatalogue(t, context.Background(), p, class)
			sa.Shape.PlatformConfigOptions = platformShapeFixture(t,
				"HYPOTHETICAL_FUTURE_VM", platformCatalogueShapeName, tc.options).PlatformConfigOptions
			class.Spec.PlatformConfig = platformRequestFixture(t, tc.request)
			before := class.DeepCopy()
			current := platformCatalogue(t, context.Background(), p, class)
			require.Len(t, current, len(baseline))
			for i, candidate := range current {
				require.Empty(t, candidate.Offerings.Available())
				require.Equal(t, baseline[i].Capacity, candidate.Capacity)
				require.Equal(t, baseline[i].Overhead, candidate.Overhead)
				expected := scheduling.NewRequirements(baseline[i].Requirements.Values()...)
				expected[corev1.CapacityTypeLabelKey] = scheduling.NewRequirement(corev1.CapacityTypeLabelKey, v1.NodeSelectorOpIn)
				require.Equal(t, expected, candidate.Requirements)
				require.Equal(t, cloneOfferingRequirements(baseline[i].Offerings),
					cloneOfferingRequirements(candidate.Offerings))
			}
			require.Equal(t, before, class)
		})
	}
}

func TestPlatformConfigOfferingIsolation(t *testing.T) {
	// Encryption must mask final offerings, retain reserved/spot requirements,
	// preserve other availability gates, and never mutate earlier snapshots.
	for _, reservations := range []bool{false, true} {
		for _, tc := range []struct {
			name, request     string
			defaultEncryption *bool
			encrypted         bool
		}{
			{"security_only", `{"secureBootEnabled":true}`, nil, false},
			{"explicit_true", `{"memoryEncryptionEnabled":true}`, nil, true},
			{"default_true", `{"secureBootEnabled":false}`, lo.ToPtr(true), true},
			{"default_false", `{"secureBootEnabled":false}`, lo.ToPtr(false), false},
			{"false_overrides_true_default", `{"memoryEncryptionEnabled":false}`, lo.ToPtr(true), false},
			{"nil_bypasses_true_default", "null", lo.ToPtr(true), false},
			{"empty_bypasses_true_default", "{}", lo.ToPtr(true), false},
		} {
			if reservations && tc.name != "explicit_true" && tc.name != "default_true" &&
				tc.name != "false_overrides_true_default" {
				continue
			}
			t.Run(lo.Ternary(reservations, "reserved/", "spot/")+tc.name, func(t *testing.T) {
				p, class, sa := platformCatalogueFixture(t)
				sa.Shape.PlatformConfigOptions.MemoryEncryptionOptions.IsDefaultEnabled = tc.defaultEncryption
				if reservations {
					class.Spec.CapacityReservationConfigs = []*v1beta1.CapacityReservationConfig{
						{CapacityReservationId: lo.ToPtr("test-reservation")},
					}
					p.capacityReservationProvider = &fakeCapacityReservationProvider{results: []capacityreservation.ResolveResult{{
						Ocid: "test-reservation", Ad: sa.Ads[0], ShapeAvailabilities: []capacityreservation.ShapeAvailability{
							{Ad: sa.Ads[0], Shape: platformCatalogueShapeName, Total: 1,
								Ocpu: lo.ToPtr(float32(4)), MemoryInGbs: lo.ToPtr(float32(16))},
							{Ad: sa.Ads[0], Shape: platformCatalogueShapeName, Total: 1,
								Ocpu: lo.ToPtr(float32(8)), MemoryInGbs: lo.ToPtr(float32(32))},
						},
					}}}
				}
				p.unavailableOfferings = cache.NewUnavailableOfferings(cache.UnavailableOfferingsTTL)
				for _, config := range class.Spec.ShapeConfigs {
					p.unavailableOfferings.MarkUnavailable(context.Background(), platformCatalogueShapeName,
						config.Ocpus, config.MemoryInGbs, "AD-2", corev1.CapacityTypeOnDemand, "")
				}
				baseline := platformCatalogue(t, context.Background(), p, class)
				for _, candidate := range baseline {
					capacityTypes := []string{}
					for _, offering := range candidate.Offerings {
						capacityTypes = append(capacityTypes, offering.CapacityType())
						if offering.CapacityType() == corev1.CapacityTypeOnDemand && offering.Zone() == "AD-2" {
							require.False(t, offering.Available, "the earlier cache restriction must be active")
						}
					}
					require.Contains(t, capacityTypes, corev1.CapacityTypeOnDemand)
					require.Contains(t, capacityTypes, lo.Ternary(reservations, corev1.CapacityTypeReserved, corev1.CapacityTypeSpot))
				}
				beforeRequirements := cloneOfferingRequirements(baseline[0].Offerings)
				beforeAvailability := lo.Map(baseline[0].Offerings, func(o *cloudprovider.Offering, _ int) bool {
					return o.Available
				})
				class.Spec.PlatformConfig = platformRequestFixture(t, tc.request)
				originalClass := class.DeepCopy()
				current := platformCatalogue(t, context.Background(), p, class)
				for i, candidate := range current {
					expected := scheduling.NewRequirements(baseline[i].Requirements.Values()...)
					if tc.encrypted {
						expected[corev1.CapacityTypeLabelKey] = scheduling.NewRequirement(
							corev1.CapacityTypeLabelKey, v1.NodeSelectorOpIn, corev1.CapacityTypeOnDemand)
						expected[cloudprovider.ReservationIDLabel] = scheduling.NewRequirement(
							cloudprovider.ReservationIDLabel, v1.NodeSelectorOpDoesNotExist)
					}
					require.Equal(t, expected, candidate.Requirements)
					require.Equal(t, cloneOfferingRequirements(baseline[i].Offerings), cloneOfferingRequirements(candidate.Offerings))
					for j, offering := range candidate.Offerings {
						want := baseline[i].Offerings[j].Available
						if tc.encrypted && offering.CapacityType() != corev1.CapacityTypeOnDemand {
							want = false
						}
						require.Equal(t, want, offering.Available, "capacity=%s zone=%s", offering.CapacityType(), offering.Zone())
						require.Equal(t, baseline[i].Offerings[j].Price, offering.Price)
						metric, found := fakes.FindMetricWithLabelValues(t, "instance_type_offering_available", map[string]string{
							metrics.InstanceTypeLabel: candidate.Name,
							metrics.CapacityTypeLabel: offering.CapacityType(), metrics.ZoneLabel: offering.Zone(),
						})
						require.True(t, found)
						require.Equal(t, lo.Ternary(want, float64(1), float64(0)), metric.GetGauge().GetValue())
					}
				}
				require.Equal(t, originalClass, class)
				require.Equal(t, beforeRequirements, cloneOfferingRequirements(baseline[0].Offerings))
				require.Equal(t, beforeAvailability, lo.Map(baseline[0].Offerings, func(o *cloudprovider.Offering, _ int) bool {
					return o.Available
				}))
				// A later NodeClass with no restrictions must keep its baseline availability.
				other := class.DeepCopy()
				other.Name, other.Spec.PlatformConfig = "other-class", nil
				require.Equal(t, baseline, platformCatalogue(t, context.Background(), p, other))
			})
		}
	}

}

func TestPlatformConfigDiagnostics(t *testing.T) {
	// One shape expands to two allocations. Every diagnostic must contain only
	// the expected fields, with no requested values or objects.
	for _, tc := range []struct{ name, options, request, message, reason, field string }{
		{
			"explicit_rejection",
			`{"percentageOfCoresEnabledOptions":{"min":25,"max":50}}`,
			`{"percentageOfCoresEnabled":75}`,
			"shape unavailable for requested platform configuration",
			"unsupported_value",
			"percentageOfCoresEnabled",
		},
		{
			"valid_non_enum_default_quiet",
			`{"percentageOfCoresEnabledOptions":{"defaultValue":37}}`,
			`{"secureBootEnabled":false}`,
			"",
			"",
			"",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, class, sa := platformCatalogueFixture(t)
			class.Spec.PlatformConfig = platformRequestFixture(t, tc.request)
			sa.Shape.PlatformConfigOptions = platformShapeFixture(t,
				"HYPOTHETICAL_FUTURE_VM", platformCatalogueShapeName, tc.options).PlatformConfigOptions
			var logs bytes.Buffer
			ctx := log.IntoContext(context.Background(),
				zap.New(zap.WriteTo(&logs), zap.UseDevMode(true), zap.JSONEncoder()))
			for build := 1; build <= 2; build++ {
				logs.Reset()
				candidates := platformCatalogue(t, ctx, p, class)
				require.Len(t, candidates, 2)
				var entries []map[string]interface{}
				scanner := bufio.NewScanner(bytes.NewReader(logs.Bytes()))
				for scanner.Scan() {
					var entry map[string]interface{}
					require.NoError(t, json.Unmarshal(scanner.Bytes(), &entry))
					require.NotEqual(t, "info", entry["level"])
					if entry["msg"] == "shape unavailable for requested platform configuration" {
						entries = append(entries, entry)
					}
				}
				require.NoError(t, scanner.Err())
				if tc.message == "" {
					require.Empty(t, entries)
					continue
				}
				require.NotEmpty(t, entries)
				for _, entry := range entries {
					delete(entry, "ts")
					require.Equal(t, map[string]interface{}{
						"level": "debug", "msg": tc.message, "nodeclass": "platform-test",
						"shape":        platformCatalogueShapeName,
						"platformType": "HYPOTHETICAL_FUTURE_VM", "reason": tc.reason, "field": tc.field,
					}, entry)
				}
			}
		})
	}
}

func TestPlatformConfigRefresh(t *testing.T) {
	// Refresh replaces metadata used by later builds while failed inventory
	// reads preserve the cache and already-built candidates.
	p, class, sa := platformCatalogueFixture(t)
	class.Spec.PlatformConfig = &v1beta1.PlatformConfig{SymmetricMultiThreadingEnabled: lo.ToPtr(false)}
	identityClient := fakes.NewFakeIdentity()
	identityClient.OnListAvailabilityDomains = func(context.Context,
		ociidentity.ListAvailabilityDomainsRequest) (ociidentity.ListAvailabilityDomainsResponse, error) {
		return ociidentity.ListAvailabilityDomainsResponse{Items: []ociidentity.AvailabilityDomain{
			{Name: lo.ToPtr("test:AD-1"), Id: lo.ToPtr("test-ad")},
		}}, nil
	}
	var err error
	p.identityProvider, err = identity.NewProvider(context.Background(), "test-compartment", identityClient)
	require.NoError(t, err)
	shapes := []ocicore.Shape{*sa.Shape}
	var refreshErr error
	p.computeClient = &fakes.FakeCompute{OnListShapes: func(context.Context,
		ocicore.ListShapesRequest) (ocicore.ListShapesResponse, error) {
		return ocicore.ListShapesResponse{Items: shapes}, refreshErr
	}}
	require.NoError(t, p.refreshShapes(context.Background()))
	first := platformCatalogue(t, context.Background(), p, class)
	firstRequirements := cloneOfferingRequirements(first[0].Offerings)
	firstCapacity := first[0].Capacity.DeepCopy()
	require.NotEmpty(t, first[0].Offerings.Available())

	require.NoError(t, p.refreshShapes(context.Background()))
	require.Equal(t, first, platformCatalogue(t, context.Background(), p, class))

	unsupported := *sa.Shape
	options := *sa.Shape.PlatformConfigOptions
	options.SymmetricMultiThreadingOptions = &ocicore.ShapeSymmetricMultiThreadingEnabledPlatformOptions{
		AllowedValues: []bool{true},
	}
	unsupported.PlatformConfigOptions = &options
	newShape := unsupported
	newShape.Shape, newShape.IsFlexible = lo.ToPtr("VM.Standard.E4.New"), lo.ToPtr(false)
	shapes = []ocicore.Shape{unsupported, newShape}
	require.NoError(t, p.refreshShapes(context.Background()))
	unsupportedCandidates := platformCatalogue(t, context.Background(), p, class)
	require.Len(t, unsupportedCandidates, 3)
	for _, candidate := range unsupportedCandidates {
		require.Empty(t, candidate.Offerings.Available())
	}

	refreshErr = errors.New("inventory unavailable")
	shapes = nil
	require.EqualError(t, p.refreshShapes(context.Background()), "inventory unavailable")
	require.ElementsMatch(t, unsupportedCandidates, platformCatalogue(t, context.Background(), p, class))

	refreshErr, shapes = nil, []ocicore.Shape{*sa.Shape}
	require.NoError(t, p.refreshShapes(context.Background()))
	require.Equal(t, first, platformCatalogue(t, context.Background(), p, class))
	require.NotEmpty(t, first[0].Offerings.Available())
	require.Equal(t, firstRequirements, cloneOfferingRequirements(first[0].Offerings))
	require.Equal(t, firstCapacity, first[0].Capacity)
	for _, candidate := range unsupportedCandidates {
		require.Empty(t, candidate.Offerings.Available())
	}

	// Default changes affect later builds only. Disabled and unknown encryption
	// leave both spot and on-demand offerings available.
	class.Spec.PlatformConfig = &v1beta1.PlatformConfig{SecureBootEnabled: lo.ToPtr(false)}
	capacityTypes := func(candidate *OciInstanceType) []string {
		return lo.Uniq(lo.Map(candidate.Offerings.Available(), func(o *cloudprovider.Offering, _ int) string {
			return o.CapacityType()
		}))
	}
	var snapshots [][]*OciInstanceType
	var snapshotCapacityTypes [][]string
	var snapshotCPUs [][]int64
	for _, tc := range []struct {
		name            string
		encryption, smt *bool
		percentage      *int
		wantCPUs        []int64
		wantCapacity    []string
	}{
		{"enabled_defaults", lo.ToPtr(true), lo.ToPtr(true), lo.ToPtr(50), []int64{4, 8},
			[]string{corev1.CapacityTypeOnDemand}},
		{"disabled_defaults", lo.ToPtr(false), lo.ToPtr(false), nil, []int64{4, 8},
			[]string{corev1.CapacityTypeOnDemand, corev1.CapacityTypeSpot}},
		{"missing_defaults", nil, nil, nil, []int64{8, 16},
			[]string{corev1.CapacityTypeOnDemand, corev1.CapacityTypeSpot}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := *sa.Shape
			options := *sa.Shape.PlatformConfigOptions
			options.MemoryEncryptionOptions = &ocicore.ShapeMemoryEncryptionOptions{IsDefaultEnabled: tc.encryption}
			options.SymmetricMultiThreadingOptions = &ocicore.ShapeSymmetricMultiThreadingEnabledPlatformOptions{
				IsDefaultEnabled: tc.smt,
			}
			options.PercentageOfCoresEnabledOptions = &ocicore.PercentageOfCoresEnabledOptions{DefaultValue: tc.percentage}
			changed.PlatformConfigOptions = &options
			refreshErr, shapes = nil, []ocicore.Shape{changed}
			require.NoError(t, p.refreshShapes(context.Background()))
			current := platformCatalogue(t, context.Background(), p, class)
			require.Len(t, current, 2)
			var cpus []int64
			for _, candidate := range current {
				require.ElementsMatch(t, tc.wantCapacity, capacityTypes(candidate))
				cpus = append(cpus, candidate.Capacity.Cpu().Value())
			}
			require.ElementsMatch(t, tc.wantCPUs, cpus)
			snapshots = append(snapshots, current)
			snapshotCapacityTypes = append(snapshotCapacityTypes, tc.wantCapacity)
			if tc.encryption != nil {
				*tc.encryption = !*tc.encryption
			}
			snapshotCPUs = append(snapshotCPUs, cpus)
		})
	}
	for i, snapshot := range snapshots {
		for j, candidate := range snapshot {
			require.ElementsMatch(t, snapshotCapacityTypes[i], capacityTypes(candidate))
			require.Equal(t, snapshotCPUs[i][j], candidate.Capacity.Cpu().Value())
		}
	}
}

func TestPlatformConfigCandidateCopies(t *testing.T) {
	// Copies preserve the platform type, resources, and selected offerings.
	p, class, sa := platformCatalogueFixture(t)
	class.Spec.PlatformConfig = &v1beta1.PlatformConfig{
		SymmetricMultiThreadingEnabled: lo.ToPtr(false),
	}
	requestBefore := class.Spec.PlatformConfig.DeepCopy()
	metadataBefore, err := json.Marshal(sa.Shape.PlatformConfigOptions)
	require.NoError(t, err)
	candidates := platformCatalogue(t, context.Background(), p, class)
	copies := make([]*OciInstanceType, 0, len(candidates))
	for _, candidate := range candidates {
		require.Equal(t, "HYPOTHETICAL_FUTURE_VM", string(candidate.PlatformType))
		originalOfferings := append(cloudprovider.Offerings{}, candidate.Offerings...)
		require.Greater(t, len(originalOfferings), 1)
		copy := candidate.CopyAndUpdateOfferings(candidate.Offerings[:1])
		copies = append(copies, copy)
		require.Equal(t, originalOfferings, candidate.Offerings)
		require.Equal(t, originalOfferings[:1], copy.Offerings)
		require.Equal(t, candidate.PlatformType, copy.PlatformType)
		require.Equal(t, candidate.Capacity, copy.Capacity)
		require.Equal(t, candidate.Overhead, copy.Overhead)
		require.Equal(t, candidate.Ocpu, copy.Ocpu)
		require.Equal(t, candidate.MemoryInGbs, copy.MemoryInGbs)
		require.Equal(t, candidate.Shape, copy.Shape)
		require.Len(t, copy.Offerings, 1)
	}
	require.Equal(t, requestBefore, class.Spec.PlatformConfig)
	metadataAfter, err := json.Marshal(sa.Shape.PlatformConfigOptions)
	require.NoError(t, err)
	require.JSONEq(t, string(metadataBefore), string(metadataAfter))

	sa.Shape.PlatformConfigOptions.Type = "CHANGED_VM"
	for _, candidate := range append(candidates, copies...) {
		require.Equal(t, ocicore.ShapePlatformConfigOptionsTypeEnum("HYPOTHETICAL_FUTURE_VM"), candidate.PlatformType)
	}
}

func TestPlatformConfigVCPUAdjustment(t *testing.T) {
	p := &DefaultProvider{oneVcpuPerOcpuShapes: map[string]struct{}{
		"VM.STANDARD.A1.FLEX": {},
		"BM.STANDARD.A1.160":  {},
	}}

	// Defaults feed the existing arithmetic only for non-empty requests,
	// and 100% still applies percentage rounding.
	for _, tc := range []struct {
		name              string
		ocpu              float32
		request           *v1beta1.PlatformConfig
		smtDefault        *bool
		percentageDefault *int
		wantVCPU          float32
	}{
		{
			"explicit_smt_overrides_default",
			8,
			&v1beta1.PlatformConfig{SymmetricMultiThreadingEnabled: lo.ToPtr(true)},
			lo.ToPtr(false),
			nil,
			16,
		},
		{
			"fractional_percentage_default_100",
			2.25,
			&v1beta1.PlatformConfig{SecureBootEnabled: lo.ToPtr(false)},
			nil,
			lo.ToPtr(100),
			6,
		},
		{"percentage_default_33", 8, &v1beta1.PlatformConfig{SecureBootEnabled: lo.ToPtr(false)}, nil, lo.ToPtr(33), 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shape := &ocicore.Shape{
				Shape: lo.ToPtr("VM.Standard.E4.Flex"), PlatformConfigOptions: &ocicore.ShapePlatformConfigOptions{
					Type:              "HYPOTHETICAL_FUTURE_VM",
					SecureBootOptions: &ocicore.ShapeSecureBootOptions{AllowedValues: []bool{false, true}},
					SymmetricMultiThreadingOptions: &ocicore.ShapeSymmetricMultiThreadingEnabledPlatformOptions{
						AllowedValues: []bool{false, true}, IsDefaultEnabled: tc.smtDefault,
					},
					PercentageOfCoresEnabledOptions: &ocicore.PercentageOfCoresEnabledOptions{
						Min: lo.ToPtr(25), Max: lo.ToPtr(100), DefaultValue: tc.percentageDefault,
					},
				},
			}
			before := tc.request.DeepCopy()
			evaluation := evaluatePlatformConfig(shape, tc.request)
			require.True(t, evaluation.compatible)
			baseline := p.vcpu(shape, tc.ocpu)
			require.Equal(t, tc.wantVCPU, adjustVCPUForPlatformConfig(baseline, shape, tc.request))
			require.Equal(t, before, tc.request)
		})
	}

	// Preserve fractional CPUs without a percentage and distinguish omitted
	// symmetric multithreading from an explicit value.
	for _, tc := range []struct {
		name     string
		request  *v1beta1.PlatformConfig
		wantVCPU float32
	}{
		{"smt_omitted", &v1beta1.PlatformConfig{PercentageOfCoresEnabled: lo.ToPtr(50)}, 4},
		{"percentage_omitted_smt_on", &v1beta1.PlatformConfig{SymmetricMultiThreadingEnabled: lo.ToPtr(true)}, 4.5},
		{"percentage_omitted_smt_off", &v1beta1.PlatformConfig{SymmetricMultiThreadingEnabled: lo.ToPtr(false)}, 2.25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shape := &ocicore.Shape{Shape: lo.ToPtr("VM.Standard.E4.Flex")}
			baseline := p.vcpu(shape, 2.25)
			require.Equal(t, tc.wantVCPU, adjustVCPUForPlatformConfig(baseline, shape, tc.request))
		})
	}

	// Seven physical cores makes this percentage exercise ceiling rather
	// than truncation; omitted SMT must retain the x86 two-thread conversion.
	t.Run("x86_percentage_25", func(t *testing.T) {
		shape := &ocicore.Shape{Shape: lo.ToPtr("BM.Standard.E4.128")}
		request := &v1beta1.PlatformConfig{PercentageOfCoresEnabled: lo.ToPtr(25)}
		baseline := p.vcpu(shape, 7)
		require.Equal(t, float32(4), adjustVCPUForPlatformConfig(baseline, shape, request))
	})

	// Arm fixtures prove arithmetic only, not live shape support for SMT or
	// percentage overrides. A2/A4 physical cores come from baseline CPUs, not OCPUs.
	for _, tc := range []struct {
		shape    string
		wantVCPU float32
	}{
		{"VM.Standard.A1.Flex", 3},
		{"VM.Standard.A2.Flex", 6},
		{"VM.Standard.A4.Flex", 6},
	} {
		t.Run(tc.shape+"_smt_disabled", func(t *testing.T) {
			shape := &ocicore.Shape{Shape: lo.ToPtr(tc.shape)}
			request := &v1beta1.PlatformConfig{
				SecureBootEnabled: lo.ToPtr(false), SymmetricMultiThreadingEnabled: lo.ToPtr(false),
			}
			baseline := p.vcpu(shape, 3)
			require.Equal(t, tc.wantVCPU, adjustVCPUForPlatformConfig(baseline, shape, request))
		})
	}
	for _, smt := range []*bool{nil, lo.ToPtr(false), lo.ToPtr(true)} {
		name := fmt.Sprintf("arm_percentage_75_smt_supplied_%t_value_%t", smt != nil, lo.FromPtr(smt))
		t.Run(name, func(t *testing.T) {
			request := &v1beta1.PlatformConfig{
				PercentageOfCoresEnabled: lo.ToPtr(75), SymmetricMultiThreadingEnabled: smt,
			}
			a1 := &ocicore.Shape{Shape: lo.ToPtr("VM.Standard.A1.Flex")}
			a2 := &ocicore.Shape{Shape: lo.ToPtr("VM.Standard.A2.Flex")}
			baselineA1, baselineA2 := p.vcpu(a1, 3), p.vcpu(a2, 3)
			require.Equal(t, float32(3), adjustVCPUForPlatformConfig(baselineA1, a1, request))
			require.Equal(t, float32(5), adjustVCPUForPlatformConfig(baselineA2, a2, request))
		})
	}
}

func TestPlatformConfigIntelEnabledCores(t *testing.T) {
	p := &DefaultProvider{}
	for _, platformType := range []ocicore.ShapePlatformConfigOptionsTypeEnum{
		ocicore.ShapePlatformConfigOptionsTypeIntelSkylakeBm,
		ocicore.ShapePlatformConfigOptionsTypeIntelIcelakeBm,
	} {
		for _, tc := range []struct {
			name        string
			cores       float32
			percentage  int
			smt         bool
			useDefaults bool
			wantVCPU    float32
		}{
			{"25_percent", 52, 25, false, false, 12},
			{"50_percent", 52, 50, false, false, 26},
			{"75_percent", 52, 75, false, false, 38},
			{"100_percent", 52, 100, false, false, 52},
			{"minimum_one_core_per_socket", 2, 25, false, false, 2},
			{"defaults_with_smt", 52, 25, true, true, 24},
		} {
			t.Run(string(platformType)+"/"+tc.name, func(t *testing.T) {
				shape := &ocicore.Shape{
					Shape: lo.ToPtr("BM.Intel"),
					PlatformConfigOptions: &ocicore.ShapePlatformConfigOptions{
						Type: platformType,
						SymmetricMultiThreadingOptions: &ocicore.ShapeSymmetricMultiThreadingEnabledPlatformOptions{
							IsDefaultEnabled: lo.ToPtr(tc.smt),
						},
						PercentageOfCoresEnabledOptions: &ocicore.PercentageOfCoresEnabledOptions{
							DefaultValue: lo.ToPtr(tc.percentage),
						},
					},
				}
				request := &v1beta1.PlatformConfig{SecureBootEnabled: lo.ToPtr(false)}
				if !tc.useDefaults {
					request.PercentageOfCoresEnabled = lo.ToPtr(tc.percentage)
					request.SymmetricMultiThreadingEnabled = lo.ToPtr(tc.smt)
				}
				baseline := p.vcpu(shape, tc.cores)
				require.Equal(t, tc.wantVCPU, adjustVCPUForPlatformConfig(baseline, shape, request))
			})
		}
	}
}

func TestPlatformConfigResources(t *testing.T) {
	p := &DefaultProvider{}
	shape := platformShapeFixture(t, "HYPOTHETICAL_FUTURE_VM", "VM.Standard.E4.Flex", `{}`)
	shape.Ocpus, shape.MemoryInGBs = lo.ToPtr(float32(8)), lo.ToPtr(float32(32))
	request := &v1beta1.PlatformConfig{SecureBootEnabled: lo.ToPtr(false)}
	class := &v1beta1.OCINodeClass{Spec: v1beta1.OCINodeClassSpec{
		PlatformConfig: request,
		VolumeConfig:   &v1beta1.VolumeConfig{BootVolumeConfig: &v1beta1.BootVolumeConfig{}},
		KubeletConfig:  &v1beta1.KubeletConfiguration{PodsPerCore: lo.ToPtr(int32(10)), MaxPods: lo.ToPtr(int32(110))},
	}}
	before := class.DeepCopy()
	it := &OciInstanceType{Ocpu: lo.ToPtr(float32(2.25)), MemoryInGbs: lo.ToPtr(float32(32))}
	evaluation := evaluatePlatformConfig(shape, request)
	require.True(t, evaluation.compatible)
	totalCPU := p.vcpu(shape, *it.Ocpu)
	totalCPU = adjustVCPUForPlatformConfig(totalCPU, shape, request)
	setCapacity(it, shape, totalCPU, *it.MemoryInGbs, class, ipV4SingleStack, defaultVMMemoryOverhead)
	setOverhead(it, totalCPU, *it.MemoryInGbs, class)
	require.Equal(t, int64(4500), it.Capacity.Cpu().MilliValue())
	require.Equal(t, int64(40), it.Capacity.Pods().Value())
	require.Equal(t, int64(88), it.Overhead.KubeReserved.Cpu().MilliValue())
	allocatable := it.Allocatable()
	require.Equal(t, int64(4312), allocatable.Cpu().MilliValue())
	require.Equal(t, int64(40), allocatable.Pods().Value())
	require.Equal(t, float32(2.25), *it.Ocpu)
	require.Equal(t, before, class)
}
