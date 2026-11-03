/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package instancetype

import (
	"math"
	"slices"

	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
)

const (
	platformPercentageOfCoresEnabledField = "percentageOfCoresEnabled"
	platformMeasuredBootEnabledField      = "measuredBootEnabled"
	platformNumaNodesPerSocketField       = "numaNodesPerSocket"
	platformUnsupportedValueReason        = "unsupported_value"
	platformFieldNotSupportedReason       = "field_not_supported"
)

type platformConfigEvaluation struct {
	compatible bool
	reason     string
	field      string
}

// resolvePlatformBoolean returns the requested value or advertised default, without changing either.
func resolvePlatformBoolean(request, defaultValue *bool) *bool {
	if request != nil {
		return request
	}
	return defaultValue
}

// evaluatePlatformConfig checks exact requested values against the cached shape
// metadata, then uses advertised defaults for security rules and resource inputs.
// Diagnostics follow API field order, before Shielded and confidential rules.
func evaluatePlatformConfig(shape *ocicore.Shape, request *v1beta1.PlatformConfig) platformConfigEvaluation {
	if request.IsEmpty() {
		return platformConfigEvaluation{compatible: true}
	}
	if shape == nil || shape.PlatformConfigOptions == nil || shape.PlatformConfigOptions.Type == "" {
		return platformConfigEvaluation{reason: "missing_platform_metadata"}
	}
	options := shape.PlatformConfigOptions
	result := platformConfigEvaluation{}
	fail := func(reason, field string) platformConfigEvaluation {
		result.reason, result.field = reason, field
		return result
	}
	if reason, field := evaluatePlatformConfigCapabilities(options, request); reason != "" {
		return fail(reason, field)
	}

	// An explicit request wins over the advertised default. Allowed values alone
	// do not establish a default, and unresolved values remain unknown.
	secureBoot := resolvePlatformBoolean(request.SecureBootEnabled, lo.FromPtr(options.SecureBootOptions).IsDefaultEnabled)
	tpm := resolvePlatformBoolean(request.TrustedPlatformModuleEnabled,
		lo.FromPtr(options.TrustedPlatformModuleOptions).IsDefaultEnabled)
	measuredBoot := resolvePlatformBoolean(request.MeasuredBootEnabled,
		lo.FromPtr(options.MeasuredBootOptions).IsDefaultEnabled)
	encrypted := resolvePlatformBoolean(request.MemoryEncryptionEnabled,
		lo.FromPtr(options.MemoryEncryptionOptions).IsDefaultEnabled)

	if !platformShieldedCompatible(shape, tpm, measuredBoot) {
		return fail("unsupported_shielded_combination", platformMeasuredBootEnabledField)
	}
	if lo.FromPtr(encrypted) &&
		(lo.FromPtr(secureBoot) || lo.FromPtr(tpm) || lo.FromPtr(measuredBoot)) {
		return fail("confidential_shielded_conflict", "memoryEncryptionEnabled")
	}
	result.compatible = true
	return result
}

// evaluatePlatformConfigCapabilities checks supplied values in API field order.
func evaluatePlatformConfigCapabilities(
	options *ocicore.ShapePlatformConfigOptions,
	request *v1beta1.PlatformConfig,
) (reason, field string) {
	if reason := evaluatePlatformBoolean(request.SecureBootEnabled,
		lo.FromPtr(options.SecureBootOptions).AllowedValues); reason != "" {
		return reason, "secureBootEnabled"
	}
	if reason := evaluatePlatformBoolean(request.TrustedPlatformModuleEnabled,
		lo.FromPtr(options.TrustedPlatformModuleOptions).AllowedValues); reason != "" {
		return reason, "trustedPlatformModuleEnabled"
	}
	if reason := evaluatePlatformBoolean(request.MeasuredBootEnabled,
		lo.FromPtr(options.MeasuredBootOptions).AllowedValues); reason != "" {
		return reason, platformMeasuredBootEnabledField
	}
	if reason := evaluatePlatformBoolean(request.MemoryEncryptionEnabled,
		lo.FromPtr(options.MemoryEncryptionOptions).AllowedValues); reason != "" {
		return reason, "memoryEncryptionEnabled"
	}
	if reason := evaluatePlatformBoolean(request.SymmetricMultiThreadingEnabled,
		lo.FromPtr(options.SymmetricMultiThreadingOptions).AllowedValues); reason != "" {
		return reason, "symmetricMultiThreadingEnabled"
	}
	if reason := evaluatePlatformBoolean(request.AccessControlServiceEnabled,
		lo.FromPtr(options.AccessControlServiceOptions).AllowedValues); reason != "" {
		return reason, "accessControlServiceEnabled"
	}
	if reason := evaluatePlatformBoolean(request.VirtualInstructionsEnabled,
		lo.FromPtr(options.VirtualInstructionsOptions).AllowedValues); reason != "" {
		return reason, "virtualInstructionsEnabled"
	}
	if reason := evaluatePlatformBoolean(request.InputOutputMemoryManagementUnitEnabled,
		lo.FromPtr(options.InputOutputMemoryManagementUnitOptions).AllowedValues); reason != "" {
		return reason, "inputOutputMemoryManagementUnitEnabled"
	}
	if request.NumaNodesPerSocket != nil {
		if options.NumaNodesPerSocketPlatformOptions == nil ||
			len(options.NumaNodesPerSocketPlatformOptions.AllowedValues) == 0 {
			return platformFieldNotSupportedReason, platformNumaNodesPerSocketField
		}
		if !slices.Contains(options.NumaNodesPerSocketPlatformOptions.AllowedValues,
			ocicore.ShapeNumaNodesPerSocketPlatformOptionsAllowedValuesEnum(*request.NumaNodesPerSocket)) {
			return platformUnsupportedValueReason, platformNumaNodesPerSocketField
		}
	}
	if request.PercentageOfCoresEnabled != nil {
		bounds := options.PercentageOfCoresEnabledOptions
		if bounds == nil {
			return platformFieldNotSupportedReason, platformPercentageOfCoresEnabledField
		}
		if bounds.Min == nil || bounds.Max == nil || *bounds.Min > *bounds.Max {
			return "invalid_field_metadata", platformPercentageOfCoresEnabledField
		}
		if *request.PercentageOfCoresEnabled < *bounds.Min || *request.PercentageOfCoresEnabled > *bounds.Max {
			return platformUnsupportedValueReason, platformPercentageOfCoresEnabledField
		}
	}

	return "", ""
}

func platformShieldedCompatible(shape *ocicore.Shape, tpm, measuredBoot *bool) bool {
	// The shape name, rather than its platform type, owns the VM/BM distinction.
	if shape.Shape != nil && IsBmShape(*shape.Shape) {
		return !(measuredBoot != nil && *measuredBoot)
	}
	return !(tpm != nil && measuredBoot != nil && *tpm != *measuredBoot)
}

// evaluatePlatformBoolean treats absent and empty allowed values as unsupported.
// An omitted request does not require field metadata.
func evaluatePlatformBoolean(request *bool, allowed []bool) string {
	if request == nil {
		return ""
	}
	if len(allowed) == 0 {
		return platformFieldNotSupportedReason
	}
	if !slices.Contains(allowed, *request) {
		return platformUnsupportedValueReason
	}
	return ""
}

// adjustVCPUForPlatformConfig applies platform settings to the baseline CPU capacity.
// Skylake and Ice Lake BM percentages follow Compute's per-socket core enablement.
func adjustVCPUForPlatformConfig(
	baseline float32,
	shape *ocicore.Shape,
	request *v1beta1.PlatformConfig,
) float32 {
	options := lo.FromPtr(shape.PlatformConfigOptions)
	smt := resolvePlatformBoolean(request.SymmetricMultiThreadingEnabled,
		lo.FromPtr(options.SymmetricMultiThreadingOptions).IsDefaultEnabled)
	percentage := request.PercentageOfCoresEnabled
	if percentage == nil {
		percentage = lo.FromPtr(options.PercentageOfCoresEnabledOptions).DefaultValue
	}
	if percentage == nil && smt == nil {
		return baseline
	}

	arm := IsArmShape(*shape)
	threadsPerCore := float64(2)
	// A1 has one single-threaded core per OCPU; A2/A4 have two.
	// The baseline conversion already accounts for their cores per OCPU.
	if arm {
		threadsPerCore = 1
	}
	physicalCores := float64(baseline) / threadsPerCore
	if percentage != nil {
		switch options.Type {
		case ocicore.ShapePlatformConfigOptionsTypeIntelSkylakeBm,
			ocicore.ShapePlatformConfigOptionsTypeIntelIcelakeBm:
			// Compute floors cores per socket on these two-socket platforms,
			// keeping at least one core enabled on each socket.
			physicalCores = 2 * math.Max(1, math.Floor((physicalCores/2)*float64(*percentage)/100))
		default:
			physicalCores = math.Ceil(physicalCores * float64(*percentage) / 100)
		}
	}
	if !arm && smt != nil && !*smt {
		threadsPerCore = 1
	}
	return float32(physicalCores * threadsPerCore)
}
