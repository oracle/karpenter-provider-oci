/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package operator

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	"github.com/oracle/karpenter-provider-oci/pkg/oci"
	"github.com/oracle/karpenter-provider-oci/pkg/operator/options"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/kms"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	k8sclientfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/conversion"
	"sigs.k8s.io/karpenter/pkg/operator"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
)

func TestOperator(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Operator Suite")
}

var _ = Describe("Test Operator", func() {
	It("test valueOrDefault method", func() {
		tests := []struct {
			name string
			val  string
			def  string
			want string
		}{
			{"non-empty val", "present", "default", "present"},
			{"empty val", "", "default", "default"},
			{"empty val, empty default", "", "", ""},
			{"non-empty val, empty default", "nonempty", "", "nonempty"},
		}
		for _, tt := range tests {
			got := valueOrDefault(tt.val, tt.def)
			GinkgoWriter.Print("Testing", tt.name)
			Expect(got).To(Equal(tt.want))
		}
	})

	It("should create operator properly", func() {
		testCases := []struct {
			authMethod options.AuthMethod
			want       common.AuthenticationType
		}{
			{options.AuthBySession, common.UnknownAuthenticationType},
			{options.AuthByInstancePrincipal, common.InstancePrincipal},
			{options.AuthByWORKLOAD, common.UnknownAuthenticationType},
		}

		nodeClassClusterCompartmentId := "ocid1.compartment.oc1..cluster123"

		mockOciClient := &oci.Client{
			Compute:  fakes.NewFakeComputeClient(nodeClassClusterCompartmentId),
			Identity: fakes.NewFakeIdentityClient(),
		}
		mockCoreOp := &operator.Operator{
			Manager: &MockManager{},
		}
		mockClientset := k8sclientfake.NewClientset()
		fakeVersion := &version.Info{
			Major:      "1",
			Minor:      "30",
			GitVersion: "v1.30.0",
		}

		mockClientset.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = fakeVersion
		restConfig := &rest.Config{
			TLSClientConfig: rest.TLSClientConfig{
				CAData: []byte("DUMMY_CA"),
			},
		}

		for _, tc := range testCases {
			ociOptions := &options.Options{
				OciVcnIpNative: true,
				IpFamiliesFlag: &network.IpFamilyValue{
					IpFamilies: []network.IpFamily{network.IPv4},
				},
				OciAuthMethods: tc.authMethod,
			}

			_, operator := createOperator(context.TODO(), mockCoreOp,
				ociOptions, "us-phoenix-1",
				mockOciClient, mockClientset, restConfig, &MockAuthConfigProvider{})

			Expect(operator).NotTo(BeNil())
			authType, err := operator.KmsKeyProvider.(*kms.DefaultProvider).GetConfigProvider().AuthType()
			Expect(err).ToNot(HaveOccurred())
			Expect(authType.AuthType).To(Equal(tc.want))
		}
	})
})

// Mock AuthConfigProvider
type MockAuthConfigProvider struct{}

func (p *MockAuthConfigProvider) GetCustomProfileSessionTokenConfigProvider(
	profileName string) common.ConfigurationProvider {
	return &MockConfigurationProvider{
		AuthenticationType: common.UnknownAuthenticationType,
	}
}

func (p *MockAuthConfigProvider) GetInstancePrincipalConfigurationProvider() (common.ConfigurationProvider, error) {
	return &MockConfigurationProvider{
		AuthenticationType: common.InstancePrincipal,
	}, nil
}

func (p *MockAuthConfigProvider) GetWorkloadIdentityConfigurationProvider(
	region string) (common.ConfigurationProvider, error) {
	return &MockConfigurationProvider{
		AuthenticationType: common.UnknownAuthenticationType,
	}, nil
}

// Mock ConfigurationProvider
type MockConfigurationProvider struct {
	AuthenticationType common.AuthenticationType
}

func (cp *MockConfigurationProvider) PrivateRSAKey() (*rsa.PrivateKey, error) {
	return &rsa.PrivateKey{}, nil
}
func (cp *MockConfigurationProvider) KeyID() (string, error) {
	return "testKeyID", nil
}

func (cp *MockConfigurationProvider) TenancyOCID() (string, error) {
	return "testTenancyOCID", nil
}

func (cp *MockConfigurationProvider) UserOCID() (string, error) {
	return "testUserOCID", nil
}

func (cp *MockConfigurationProvider) KeyFingerprint() (string, error) {
	return "testKeyFingerprint", nil
}

func (cp *MockConfigurationProvider) Region() (string, error) {
	return "us-phoenix-1", nil
}

func (cp *MockConfigurationProvider) AuthType() (common.AuthConfig, error) {
	return common.AuthConfig{
		AuthType: cp.AuthenticationType,
	}, nil
}

// Mock
type MockManager struct{}

func (m *MockManager) GetHTTPClient() *http.Client {
	return &http.Client{}
}

func (m *MockManager) GetConfig() *rest.Config {
	return &rest.Config{}
}

func (m *MockManager) GetCache() cache.Cache {
	return nil
}

func (m *MockManager) GetScheme() *runtime.Scheme {
	return &runtime.Scheme{}
}

func (m *MockManager) GetClient() client.Client {
	return nil
}

func (m *MockManager) GetFieldIndexer() client.FieldIndexer {
	return nil
}

func (m *MockManager) GetEventRecorderFor(name string) record.EventRecorder {
	return nil
}

func (m *MockManager) GetEventRecorder(name string) events.EventRecorder {
	return nil
}

func (m *MockManager) GetRESTMapper() meta.RESTMapper {
	return nil
}

func (m *MockManager) GetAPIReader() client.Reader {
	return nil
}

func (m *MockManager) Start(ctx context.Context) error {
	return nil
}

func (m *MockManager) Add(manager.Runnable) error {
	return nil
}

func (m *MockManager) Elected() <-chan struct{} {
	return fakes.NewDummyChannel()
}
func (m *MockManager) AddMetricsServerExtraHandler(path string, handler http.Handler) error {
	return nil
}

func (m *MockManager) AddHealthzCheck(name string, check healthz.Checker) error {
	return nil
}

func (m *MockManager) AddReadyzCheck(name string, check healthz.Checker) error {
	return nil
}

func (m *MockManager) GetWebhookServer() webhook.Server {
	return nil
}

func (m *MockManager) GetLogger() logr.Logger {
	return logr.Logger{}
}

func (m *MockManager) GetControllerOptions() config.Controller {
	return config.Controller{}
}

func (m *MockManager) GetConverterRegistry() conversion.Registry {
	return conversion.NewRegistry()
}

type MockRestInterface struct{}

func (ki *MockRestInterface) GetRateLimiter() flowcontrol.RateLimiter {
	return nil
}

func (ki *MockRestInterface) Verb(verb string) *rest.Request {
	return &rest.Request{}
}

func (ki *MockRestInterface) Post() *rest.Request {
	return &rest.Request{}
}

func (ki *MockRestInterface) Put() *rest.Request {
	return &rest.Request{}
}

func (ki *MockRestInterface) Patch(pt types.PatchType) *rest.Request {
	return &rest.Request{}
}

func (ki *MockRestInterface) Get() *rest.Request {
	return &rest.Request{}
}

func (ki *MockRestInterface) Delete() *rest.Request {
	return &rest.Request{}
}

func (ki *MockRestInterface) APIVersion() schema.GroupVersion {
	return schema.GroupVersion{}
}

// This subprocess runs the real injection and KPO startup boundary. A deliberately
// malformed kubeconfig stops accepted configurations before any client or OCI access.
func TestEffectiveRepairOptionsStartup(t *testing.T) {
	if os.Getenv("KPO_TEST_OPTIONS_STARTUP") == "1" {
		logf.SetLogger(zap.New())
		os.Args = append([]string{os.Args[0]}, strings.Split(os.Getenv("KPO_TEST_OPTIONS_ARGS"), "\n")...)
		core := &coreoptions.Options{}
		oci := &options.Options{IpFamiliesFlag: new(network.IpFamilyValue)}
		ctx := injection.WithOptionsOrDie(context.Background(), core, oci)
		snapshot, err := json.Marshal(core)
		require.NoError(t, err)
		fmt.Printf("parsed core options: %s\n", snapshot)
		NewOperator(ctx, nil)
		t.Fatal("startup unexpectedly completed")
	}
	kubeconfig := filepath.Join(t.TempDir(), "invalid-kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfig, []byte("not-a-kubeconfig"), 0600))
	accepted := "error loading config file"
	type testCase struct {
		name   string
		args   []string
		env    []string
		want   string
		parsed string
	}
	cases := []testCase{
		{
			name:   "CLI legacy true",
			args:   []string{"--legacy-node-repair=true"},
			want:   accepted,
			parsed: `"LegacyNodeRepair":true`,
		},
		{name: "env legacy true", env: []string{"LEGACY_NODE_REPAIR=true"}, want: accepted},
		{
			name: "CLI true overrides env false",
			args: []string{"--legacy-node-repair=true"},
			env:  []string{"LEGACY_NODE_REPAIR=false"},
			want: accepted,
		},
		{
			name:   "CLI false overrides env true",
			args:   []string{"--legacy-node-repair=false"},
			env:    []string{"LEGACY_NODE_REPAIR=true"},
			want:   "legacy-node-repair=true",
			parsed: `"LegacyNodeRepair":false`,
		},
		{
			name: "other gates and scheduler",
			args: []string{
				"--legacy-node-repair=true",
				"--feature-gates=NodeOverlay=true,StaticCapacity=true,CapacityBuffer=true," +
					"SpotToSpotConsolidation=true,ReservedCapacity=true,PodDeletionCostManagement=true",
				`--scheduler-config={"podTopologySpread":{"defaultConstraints":[` +
					`{"maxSkew":1,"topologyKey":"topology.kubernetes.io/zone","whenUnsatisfiable":"ScheduleAnyway"}]}}`,
			},
			want:   accepted,
			parsed: `"topologyKey":"topology.kubernetes.io/zone"`,
		},
		{
			name: "raw feature gate parse error",
			args: []string{
				"--legacy-node-repair=true",
				"--feature-gates=NodeRepair=invalid",
			},
			want: "parsing feature gates",
		},
		{name: "raw legacy parse error", args: []string{"--legacy-node-repair=invalid"}, want: "not a valid value"},
		{
			name: "raw scheduler parse error",
			args: []string{
				"--legacy-node-repair=true",
				"--scheduler-config=invalid",
			},
			want: "parsing scheduler config",
		},
		{
			name: "unsupported reboot",
			args: []string{
				"--legacy-node-repair=true",
				`--repair-policies=[{"conditionType":"Ready","conditionStatus":"False","action":"RebootNode"}]`,
			},
			want: "RebootNode is not supported",
		},
	}
	for _, repair := range []string{"false", "true"} {
		for _, legacy := range []string{"unset", "false"} {
			args := []string{"--feature-gates=NodeRepair=" + repair}
			if legacy != "unset" {
				args = append(args, "--legacy-node-repair=false")
			}
			cases = append(cases,
				testCase{
					name:   "CLI legacy " + legacy + " NodeRepair=" + repair,
					args:   args,
					want:   "legacy-node-repair=true",
					parsed: `"LegacyNodeRepair":false`,
				})
		}
		cases = append(cases,
			testCase{
				name: "env legacy false NodeRepair=" + repair,
				env: []string{
					"LEGACY_NODE_REPAIR=false",
					"FEATURE_GATES=NodeRepair=" + repair,
				},
				want: "legacy-node-repair=true",
			},
			testCase{
				name: "legacy true NodeRepair=" + repair,
				args: []string{
					"--legacy-node-repair=true",
					"--feature-gates=NodeRepair=" + repair,
				},
				want: accepted,
			},
		)
		for _, gates := range []string{
			"TerminateFirstDrift=true",
			"TerminateFirstRepair=true",
			"TerminateFirstDrift=true,TerminateFirstRepair=true",
		} {
			for _, source := range []string{"CLI", "env"} {
				tc := testCase{name: source + " " + gates + " NodeRepair=" + repair, want: "terminate-first"}
				value := "NodeRepair=" + repair + "," + gates
				if source == "CLI" {
					tc.args = []string{"--legacy-node-repair=true", "--feature-gates=" + value}
				} else {
					tc.env = []string{"LEGACY_NODE_REPAIR=true", "FEATURE_GATES=" + value}
				}
				cases = append(cases, tc)
			}
		}
	}
	cases = append(cases,
		testCase{
			name: "explicit false terminate-first",
			args: []string{
				"--legacy-node-repair=true",
				"--feature-gates=TerminateFirstDrift=false,TerminateFirstRepair=false",
			},
			want: accepted,
		},
		testCase{
			name: "CLI gates override env true",
			args: []string{
				"--legacy-node-repair=true",
				"--feature-gates=TerminateFirstDrift=false,TerminateFirstRepair=false",
			},
			env:  []string{"FEATURE_GATES=TerminateFirstDrift=true,TerminateFirstRepair=true"},
			want: accepted,
		},
		testCase{
			name: "CLI gates override env false",
			args: []string{
				"--legacy-node-repair=true",
				"--feature-gates=TerminateFirstRepair=true",
			},
			env:  []string{"FEATURE_GATES=TerminateFirstRepair=false"},
			want: "terminate-first",
		},
	)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{
				"--cluster-compartment-id=test-cluster",
				"--vcn-compartment-id=test-vcn",
				"--pre-baked-image-compartment-id=test-images",
				"--apiserver-endpoint=10.0.0.1:6443",
			},
				tc.args...)
			command := exec.Command(os.Args[0], "-test.run=^TestEffectiveRepairOptionsStartup$")
			// Use a closed environment so local controller settings cannot influence parsing.
			command.Env = append([]string{
				"KPO_TEST_OPTIONS_STARTUP=1",
				"KPO_TEST_OPTIONS_ARGS=" + strings.Join(args, "\n"),
				"KUBECONFIG=" + kubeconfig,
			},
				tc.env...)
			output, err := command.CombinedOutput()
			require.Error(t, err, string(output))
			require.Contains(t, string(output), tc.want)
			if tc.parsed != "" {
				require.Contains(t, string(output), tc.parsed)
			}
		})
	}
}
