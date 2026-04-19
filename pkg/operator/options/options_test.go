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
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
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
					ClusterCompartmentId:          "testClusterCompartmentId",
					VcnCompartmentId:              "testVcnCompartmentId",
					PreBakedImageCompartmentId:    "testPreBakedImageCompartmentId",
					ApiserverEndpoint:             "1.0.10.1",
					ShapeMetaRefreshIntervalHours: 1,
					InstanceLaunchTimeoutVMMins:   1,
					InstanceLaunchTimeoutBMMins:   1,
				},
				"",
			},
			{
				&Options{
					ClusterCompartmentId:          "testClusterCompartmentId",
					VcnCompartmentId:              "testVcnCompartmentId",
					PreBakedImageCompartmentId:    "testPreBakedImageCompartmentId",
					ApiserverEndpoint:             "1.0.10.1",
					ShapeMetaRefreshIntervalHours: 1,
					InstanceLaunchTimeoutVMMins:   1,
					InstanceLaunchTimeoutBMMins:   1,
					RateLimitQPSRead:              -1,
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
