/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package options

import (
	"context"
	"flag"
	"math"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancetype"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/operator/options"
)

func TestOperatorOptions(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Operator Suite")
}

var _ = Describe("Test Operator Options", func() {
	It("should validate missing required fields properly", func() {
		testCases := []struct {
			testOptions *Options
			want        string
		}{
			{&Options{}, "cluster-compartment-id is missing"},
			{
				&Options{
					ClusterCompartmentId: "testClusterCompartmentId",
				},
				"vcn-compartment-id is missing",
			},
			{
				&Options{
					ClusterCompartmentId: "testClusterCompartmentId",
					VcnCompartmentId:     "testVcnCompartmentId",
				},
				"pre-baked-image-compartment-id is missing",
			},
			{
				&Options{
					ClusterCompartmentId:       "testClusterCompartmentId",
					VcnCompartmentId:           "testVcnCompartmentId",
					PreBakedImageCompartmentId: "testPreBakedImageCompartmentId",
				},
				"apiserver-endpoint is missing",
			},
			{
				&Options{
					ClusterCompartmentId:       "testClusterCompartmentId",
					VcnCompartmentId:           "testVcnCompartmentId",
					PreBakedImageCompartmentId: "testPreBakedImageCompartmentId",
					ApiserverEndpoint:          "1.0.10.1",
				},
				"shape-meta-refresh-interval-hours must be a positive integer",
			},
			{
				&Options{
					ClusterCompartmentId:          "testClusterCompartmentId",
					VcnCompartmentId:              "testVcnCompartmentId",
					PreBakedImageCompartmentId:    "testPreBakedImageCompartmentId",
					ApiserverEndpoint:             "1.0.10.1",
					ShapeMetaRefreshIntervalHours: 1,
				},
				"instance-launch-timeout-vm-mins must be a positive integer",
			},
			{
				&Options{
					ClusterCompartmentId:          "testClusterCompartmentId",
					VcnCompartmentId:              "testVcnCompartmentId",
					PreBakedImageCompartmentId:    "testPreBakedImageCompartmentId",
					ApiserverEndpoint:             "1.0.10.1",
					ShapeMetaRefreshIntervalHours: 1,
					InstanceLaunchTimeoutVMMins:   1,
				},
				"delete-instance-timeout-bm-mins must be a positive integer",
			},
			{
				&Options{
					ClusterCompartmentId:           "testClusterCompartmentId",
					VcnCompartmentId:               "testVcnCompartmentId",
					PreBakedImageCompartmentId:     "testPreBakedImageCompartmentId",
					ApiserverEndpoint:              "1.0.10.1",
					ShapeMetaRefreshIntervalHours:  1,
					InstanceLaunchTimeoutVMMins:    1,
					InstanceLaunchTimeoutBMMins:    1,
					UnavailableOfferingsTTLSeconds: -1,
				},
				"unavailable-offerings-ttl-seconds must be zero (to disable) or a positive integer",
			},
			{
				// A ttl of 0 is valid and disables the unavailable-offerings cache.
				&Options{
					ClusterCompartmentId:           "testClusterCompartmentId",
					VcnCompartmentId:               "testVcnCompartmentId",
					PreBakedImageCompartmentId:     "testPreBakedImageCompartmentId",
					ApiserverEndpoint:              "1.0.10.1",
					ShapeMetaRefreshIntervalHours:  1,
					InstanceLaunchTimeoutVMMins:    1,
					InstanceLaunchTimeoutBMMins:    1,
					UnavailableOfferingsTTLSeconds: 0,
				},
				"",
			},
			{
				&Options{
					GlobalShapeConfigs: []ociv1beta1.ShapeConfig{{}},
				},
				"global-shape-configs[0] ocpus must be >= 1",
			},
			{
				&Options{
					GlobalShapeConfigs: []ociv1beta1.ShapeConfig{
						{Ocpus: lo.ToPtr(float32(8))},
						{Ocpus: lo.ToPtr(float32(0))},
					},
				},
				"global-shape-configs[1] ocpus must be >= 1",
			},
			{
				&Options{
					GlobalShapeConfigs: []ociv1beta1.ShapeConfig{
						{Ocpus: lo.ToPtr(float32(8))},
						{Ocpus: lo.ToPtr(float32(2))},
					},
					ClusterCompartmentId:           "testClusterCompartmentId",
					VcnCompartmentId:               "testVcnCompartmentId",
					PreBakedImageCompartmentId:     "testPreBakedImageCompartmentId",
					ApiserverEndpoint:              "1.0.10.1",
					ShapeMetaRefreshIntervalHours:  1,
					InstanceLaunchTimeoutVMMins:    1,
					InstanceLaunchTimeoutBMMins:    1,
					UnavailableOfferingsTTLSeconds: 1,
				},
				"",
			},
			{
				&Options{
					ClusterCompartmentId:           "testClusterCompartmentId",
					VcnCompartmentId:               "testVcnCompartmentId",
					PreBakedImageCompartmentId:     "testPreBakedImageCompartmentId",
					ApiserverEndpoint:              "1.0.10.1",
					ShapeMetaRefreshIntervalHours:  1,
					InstanceLaunchTimeoutVMMins:    1,
					InstanceLaunchTimeoutBMMins:    1,
					UnavailableOfferingsTTLSeconds: 1,
					RateLimitQPSRead:               -1,
				},
				"rate-limit-qps-read must be greater than or equal to 0",
			},
		}

		for _, tc := range testCases {
			o := tc.testOptions
			err := o.Validate()
			if tc.want != "" {
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal(tc.want))
			} else {
				Expect(err).ToNot(HaveOccurred())
			}
		}

	})

	It("should AddFlags properly", func() {
		o := &Options{}
		testflagSet := flag.NewFlagSet("testFlagSet", flag.PanicOnError)

		o.AddFlags(&options.FlagSet{
			FlagSet: testflagSet,
		})

		expectedFlagNames := []string{
			"cluster-compartment-id",
			"vcn-compartment-id",
			"apiserver-endpoint",
			"oci-vcn-ip-native",
			"ip-families",
			"oci-auth-method",
			"oci-profile-name",
			"flexible-shape-configs",
			"shape-meta-refresh-interval-hours",
			"instance-launch-timeout-vm-mins",
			"instance-launch-timeout-bm-mins",
			"instance-operation-poll-interval-seconds",
			"instance-launch-timeout-failover",
			"unavailable-offerings-ttl-seconds",
			"disable-rate-limiter",
			"rate-limit-qps-read",
			"rate-limit-burst-read",
			"rate-limit-qps-write",
			"rate-limit-burst-write",
			"repair-policies",
			"pre-baked-image-compartment-id",
			"shape-meta-file",
		}

		for _, expectedFlagName := range expectedFlagNames {
			Expect(testflagSet.Lookup(expectedFlagName)).NotTo(BeNil())
		}
	})

	It("should parse options properly", func() {
		o := &Options{}
		o.IpFamiliesFlag = new(network.IpFamilyValue)
		fs := &options.FlagSet{
			FlagSet: flag.NewFlagSet("testFlagSet", flag.ContinueOnError),
		}
		o.AddFlags(fs)

		arguments := []string{
			"--cluster-compartment-id",
			"testCompartmentId",
			"--vcn-compartment-id",
			"testVcnCompartmentId",
			"--apiserver-endpoint",
			"1.0.10.1",
			"--oci-vcn-ip-native", // "true",
			"--ip-families",
			"IPv4,IPv6",
			"--oci-auth-method",
			"SESSION",
			"--oci-profile-name",
			"testProfile",
			"--flexible-shape-configs",
			"[{\"ocpus\": 2, \"memoryInGbs\": 16}, " +
				"{\"ocpus\": 4, \"memoryInGbs\": 32, \"baselineOcpuUtilization\":\"BASELINE_1_2\"}]",
			"--shape-meta-refresh-interval-hours",
			"10",
			"--instance-launch-timeout-vm-mins",
			"20",
			"--instance-launch-timeout-bm-mins",
			"30",
			"--instance-operation-poll-interval-seconds",
			"5",
			"--instance-launch-timeout-failover", // "true"
			"--unavailable-offerings-ttl-seconds",
			"90",
			"--enable-unavailable-offerings-on-service-limit-exceeded",
			"--disable-rate-limiter",
			"--rate-limit-qps-read",
			"21",
			"--rate-limit-burst-read",
			"6",
			"--rate-limit-qps-write",
			"11",
			"--rate-limit-burst-write",
			"3",
			"--repair-policies",
			"[{\"ConditionType\": \"Ready\",\"ConditionStatus\": \"False\",\"TolerationDuration\": 600000000000}]",
			"--pre-baked-image-compartment-id",
			"testImageCompartmentId",
			"--shape-meta-file",
			"testLocation",
		}

		err := o.Parse(fs, arguments...)

		Expect(err).ToNot(HaveOccurred())
		Expect(o.ClusterCompartmentId).To(Equal("testCompartmentId"))
		Expect(o.VcnCompartmentId).To(Equal("testVcnCompartmentId"))
		Expect(o.ApiserverEndpoint).To(Equal("1.0.10.1"))
		Expect(o.OciVcnIpNative).To(BeTrue())
		Expect(o.IpFamiliesFlag.IpFamilies).To(ContainElements(network.IPv4, network.IPv6))
		Expect(o.OciAuthMethods).To(Equal(AuthBySession))
		Expect(o.OciProfileName).To(Equal("testProfile"))
		Expect(o.GlobalShapeConfigs).To(ContainElements(
			ociv1beta1.ShapeConfig{
				Ocpus:       lo.ToPtr(float32(2)),
				MemoryInGbs: lo.ToPtr(float32(16)),
			},
			ociv1beta1.ShapeConfig{
				Ocpus:                   lo.ToPtr(float32(4)),
				MemoryInGbs:             lo.ToPtr(float32(32)),
				BaselineOcpuUtilization: lo.ToPtr(ociv1beta1.BASELINE_1_2),
			},
		))
		Expect(o.ShapeMetaRefreshIntervalHours).To(Equal(10))
		Expect(o.InstanceLaunchTimeoutVMMins).To(Equal(20))
		Expect(o.InstanceLaunchTimeoutBMMins).To(Equal(30))
		Expect(o.InstanceOperationPollIntervalInSeconds).To(Equal(5))
		Expect(o.InstanceLaunchTimeOutFailOver).To(BeTrue())
		Expect(o.UnavailableOfferingsTTLSeconds).To(Equal(90))
		Expect(o.EnableUnavailableOfferingsOnServiceLimitExceeded).To(BeTrue())
		Expect(o.DisableRateLimiter).To(BeTrue())
		Expect(o.RateLimitQPSRead).To(Equal(float64(21)))
		Expect(o.RateLimitBurstRead).To(Equal(6))
		Expect(o.RateLimitQPSWrite).To(Equal(float64(11)))
		Expect(o.RateLimitBurstWrite).To(Equal(3))
		Expect(o.RepairPolicies).To(ContainElements(cloudprovider.RepairPolicy{
			ConditionType:      corev1.NodeReady,
			ConditionStatus:    corev1.ConditionFalse,
			TolerationDuration: 10 * time.Minute,
		}))
		Expect(o.PreBakedImageCompartmentId).To(Equal("testImageCompartmentId"))
		Expect(o.ShapeMetaFile).To(Equal("testLocation"))
	})

	It("should fail to parse options if args not in right format", func() {
		o := &Options{}
		o.IpFamiliesFlag = new(network.IpFamilyValue)
		fs := &options.FlagSet{
			FlagSet: flag.NewFlagSet("testFlagSet", flag.PanicOnError),
		}
		o.AddFlags(fs)

		arguments := []string{
			"--cluster-compartment-id",
			"--vcn-compartment-id",
			"testVcnCompartmentId",
			"--apiserver-endpoint",
			"1.0.10.1",
		}

		err := o.Parse(fs, arguments...)
		Expect(err).To(HaveOccurred())
	})

	It("should be able to add to and get option from context", func() {
		expected := &Options{
			ClusterCompartmentId:          "testClusterCompartmentId",
			VcnCompartmentId:              "testVcnCompartmentId",
			PreBakedImageCompartmentId:    "testPreBakedImageCompartmentId",
			ApiserverEndpoint:             "1.0.10.1",
			ShapeMetaRefreshIntervalHours: 1,
			InstanceLaunchTimeoutVMMins:   1,
		}

		ctx := expected.ToContext(context.Background())
		result := FromContext(ctx)

		Expect(result).To(Equal(expected))
	})
})

// The VM memory overhead defaults are proven safe against real measurements by
// TestMemoryCapacity_NeverOverEstimates, but only for the constants in pkg/providers/instancetype.
// These two tests stop the flag defaults and the Helm chart defaults from drifting away from those
// constants, which would ship an unproven — possibly over-estimating — value.

func TestVMMemoryOverheadDefaults_MatchProviderConstants(t *testing.T) {
	opts := &Options{IpFamiliesFlag: new(network.IpFamilyValue)}
	fs := &options.FlagSet{FlagSet: flag.NewFlagSet("test", flag.ContinueOnError)}
	opts.AddFlags(fs)

	assert.Equal(t, float64(instancetype.DefaultVMMemoryOverheadBaseMiB), opts.VMMemoryOverheadBaseMiB)
	assert.Equal(t, float64(instancetype.DefaultVMMemoryOverheadPerGBMiB), opts.VMMemoryOverheadPerGBMiB)
	assert.Equal(t, float64(instancetype.DefaultVMMemoryOverheadPercent), opts.VMMemoryOverheadPercent)
}

func TestVMMemoryOverheadDefaults_MatchHelmChart(t *testing.T) {
	raw, err := os.ReadFile("../../../chart/values.yaml")
	require.NoError(t, err)

	var values struct {
		Settings struct {
			VMMemoryOverhead struct {
				BaseMiB  *float64 `json:"baseMiB"`
				PerGBMiB *float64 `json:"perGBMiB"`
				Percent  *float64 `json:"percent"`
			} `json:"vmMemoryOverhead"`
		} `json:"settings"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &values))

	chart := values.Settings.VMMemoryOverhead
	require.NotNil(t, chart.BaseMiB, "chart/values.yaml is missing settings.vmMemoryOverhead.baseMiB")
	require.NotNil(t, chart.PerGBMiB, "chart/values.yaml is missing settings.vmMemoryOverhead.perGBMiB")
	require.NotNil(t, chart.Percent, "chart/values.yaml is missing settings.vmMemoryOverhead.percent")

	assert.Equal(t, float64(instancetype.DefaultVMMemoryOverheadBaseMiB), *chart.BaseMiB)
	assert.Equal(t, float64(instancetype.DefaultVMMemoryOverheadPerGBMiB), *chart.PerGBMiB)
	assert.Equal(t, float64(instancetype.DefaultVMMemoryOverheadPercent), *chart.Percent)
}

// The overhead settings are the one place an operator can reintroduce the over-estimation this
// change exists to prevent, so validation must reject values the arithmetic cannot handle.
func TestValidateVMMemoryOverhead(t *testing.T) {
	base := func() *Options {
		return &Options{
			ClusterCompartmentId:          "ocid1.compartment.oc1..a",
			VcnCompartmentId:              "ocid1.compartment.oc1..b",
			PreBakedImageCompartmentId:    "ocid1.compartment.oc1..c",
			ApiserverEndpoint:             "10.0.0.1:6443",
			ShapeMetaRefreshIntervalHours: 24,
			InstanceLaunchTimeoutVMMins:   5,
			InstanceLaunchTimeoutBMMins:   60,
			VMMemoryOverheadBaseMiB:       instancetype.DefaultVMMemoryOverheadBaseMiB,
			VMMemoryOverheadPerGBMiB:      instancetype.DefaultVMMemoryOverheadPerGBMiB,
			VMMemoryOverheadPercent:       instancetype.DefaultVMMemoryOverheadPercent,
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Options)
		wantErr string
	}{
		{"shipped defaults are valid", func(*Options) {}, ""},
		{"percent form is accepted", func(o *Options) { o.VMMemoryOverheadPercent = 0.075 }, ""},

		{"negative base", func(o *Options) { o.VMMemoryOverheadBaseMiB = -1 }, "vm-memory-overhead-base-mib"},
		{"negative per-gb", func(o *Options) { o.VMMemoryOverheadPerGBMiB = -1 }, "vm-memory-overhead-per-gb-mib"},
		{"negative percent", func(o *Options) { o.VMMemoryOverheadPercent = -0.1 }, "vm-memory-overhead-percent"},

		// NaN compares false against every bound, so a plain `< 0` check would let it through and
		// the int64 conversion downstream would be implementation-defined.
		{"NaN base", func(o *Options) { o.VMMemoryOverheadBaseMiB = math.NaN() }, "vm-memory-overhead-base-mib"},
		{"NaN per-gb", func(o *Options) { o.VMMemoryOverheadPerGBMiB = math.NaN() }, "vm-memory-overhead-per-gb-mib"},
		{"NaN percent", func(o *Options) { o.VMMemoryOverheadPercent = math.NaN() }, "vm-memory-overhead-percent"},

		{"infinite base", func(o *Options) { o.VMMemoryOverheadBaseMiB = math.Inf(1) }, "vm-memory-overhead-base-mib"},
		{"infinite per-gb", func(o *Options) { o.VMMemoryOverheadPerGBMiB = math.Inf(1) }, "vm-memory-overhead-per-gb-mib"},

		// 100% would model every node as having no memory at all.
		{"percent of one", func(o *Options) { o.VMMemoryOverheadPercent = 1 }, "vm-memory-overhead-percent"},
		{"percent above one", func(o *Options) { o.VMMemoryOverheadPercent = 2 }, "vm-memory-overhead-percent"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := base()
			tt.mutate(o)

			err := o.Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// Guards the mapping from flags to the provider's config. Transposing two same-typed fields here
// would compile and silently model every node's memory wrongly.
func TestOptions_VMMemoryOverhead(t *testing.T) {
	o := &Options{
		VMMemoryOverheadBaseMiB:  601,
		VMMemoryOverheadPerGBMiB: 17,
		VMMemoryOverheadPercent:  0.075,
	}

	// Distinct values, so a transposition cannot pass.
	assert.Equal(t, instancetype.VMMemoryOverheadConfig{
		BaseMiB:  601,
		PerGBMiB: 17,
		Percent:  0.075,
	}, o.VMMemoryOverhead())
}

// The chart reaches the provider through environment variables whose names KPO derives from the
// flag names. A typo on either side is silent: the flag keeps its built-in default and the chart
// value is ignored. Assert the deployment template carries exactly the names the flags imply.
func TestVMMemoryOverheadEnvVarsMatchFlagNames(t *testing.T) {
	opts := &Options{IpFamiliesFlag: new(network.IpFamilyValue)}
	fs := &options.FlagSet{FlagSet: flag.NewFlagSet("test", flag.ContinueOnError)}
	opts.AddFlags(fs)

	raw, err := os.ReadFile("../../../chart/templates/deployment.yaml")
	require.NoError(t, err)
	deployment := string(raw)

	for _, flagName := range []string{
		"vm-memory-overhead-base-mib",
		"vm-memory-overhead-per-gb-mib",
		"vm-memory-overhead-percent",
	} {
		require.NotNil(t, fs.Lookup(flagName), "flag %s must exist", flagName)

		// Parse() derives the env name from the flag name this way.
		envName := strings.ReplaceAll(strings.ToUpper(flagName), "-", "_")
		// Match the whole line: a bare substring check would also accept a suffixed typo such as
		// VM_MEMORY_OVERHEAD_BASE_MIB_TYPO, which is exactly the mistake this guards against.
		assert.Regexp(t, regexp.MustCompile(`(?m)^\s*- name: `+regexp.QuoteMeta(envName)+`\s*$`), deployment,
			"chart/templates/deployment.yaml must set %s exactly, or the chart value is silently ignored", envName)
	}

	// Go templates treat 0 as falsy, so guarding a numeric field with `with` would silently drop
	// an explicitly configured zero and fall back to the built-in default - a live bug in the AWS
	// and Azure charts. Guarding the enclosing map with `with` is fine; a non-empty map is truthy.
	for _, field := range []string{".baseMiB", ".perGBMiB", ".percent"} {
		assert.Contains(t, deployment, `if not (kindIs "invalid" `+field+")",
			"%s must be guarded by kindIs \"invalid\" so an explicit 0 is not discarded", field)
		assert.NotContains(t, deployment, "with "+field+" }}",
			"%s must not be guarded by `with`: Go templates treat 0 as falsy", field)
	}
}
