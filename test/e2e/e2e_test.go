/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/awslabs/operatorpkg/status"
	instancepkg "github.com/oracle/karpenter-provider-oci/pkg/providers/instance"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancetype"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/common/auth"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	appsv1 "k8s.io/api/apps/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	npnv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/npn/apis/v1beta1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	crlog "sigs.k8s.io/controller-runtime/pkg/log"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpenterv1alpha1 "sigs.k8s.io/karpenter/pkg/apis/v1alpha1"
)

func init() {
	crlog.SetLogger(zap.New(zap.UseDevMode(true)))
}

const (
	TestTimeout             = 30 * time.Minute
	PollInterval            = 5 * time.Second
	NodeProvisionTimeout    = 45 * time.Minute
	PodScheduleTimeout      = 2 * time.Minute
	ResourceCreationTimeout = 2 * time.Minute
	NodePoolLabel           = "karpenter.sh/nodepool"
	ReadyConditionType      = "Ready"
	TrueConditionStatus     = "True"
	TrueStr                 = "true"
)

var (
	expectedConditions = ociv1beta1.OCINodeClassStatus{
		Conditions: []status.Condition{
			{
				Type:   "NodeCompartment",
				Status: "True",
				Reason: "NodeCompartment",
			},
			{
				Type:   "Image",
				Status: "True",
				Reason: "Image",
			},
			{
				Type:   "KmsKey",
				Status: "True",
				Reason: "KmsKey",
			},
			{
				Type:   "Network",
				Status: "True",
				Reason: "Network",
			},
			{
				Type:   "Ready",
				Status: "True",
				Reason: "Ready",
			},
		},
	}
)

// LARGE_SHAPE_TEST_ENABLED env var (default "true") gates large/expensive tests globally.
// When set to any non-empty value other than "true", large shape tests are disabled.
var largeShapesEnabled = func() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LARGE_SHAPE_TEST_ENABLED")))
	return v == "" || v == TrueStr
}()

type capacityReservationExpectedResult struct {
	capacityReservationId   string
	capacityReservationName string
	shape                   string
}

type E2ETestSuite struct {
	ctx                context.Context
	t                  *testing.T
	ctrlClient         client.Client
	testConfig         *KarpenterE2ETestConfig
	computeClient      *ocicore.ComputeClient
	networkClient      *ocicore.VirtualNetworkClient
	blockStorageClient *ocicore.BlockstorageClient
	httpClient         *http.Client
	clusterApiHost     string
}

type secondVnicDetails struct {
	vnicAttachment ocicore.VnicAttachment
	vnic           ocicore.Vnic
}

// createOCINodeClass creates the OCINodeClass
func (s *E2ETestSuite) createOCINodeClass() error {
	nodeClass := oCINodeClass(s.testConfig.OCINodeClass.Name, s.testConfig)
	s.t.Logf("Creating OCINodeClass %s", nodeClass.Name)
	return s.ctrlClient.Create(s.ctx, nodeClass)
}

// createNodePool creates the NodePool
func (s *E2ETestSuite) createNodePool() error {
	nodePool := karpenterNodePool(s.testConfig.NodePool.Name, s.testConfig.OCINodeClass.Name, s.testConfig)
	s.t.Logf("Creating NodePool %s", nodePool.Name)
	return s.ctrlClient.Create(s.ctx, nodePool)
}

func (s *E2ETestSuite) createNodeOverlay() error {
	overlay := nodeOverlay(s.testConfig)
	s.t.Logf("Creating NodeOverlay %s", overlay.Name)
	return s.ctrlClient.Create(s.ctx, overlay)
}

func (s *E2ETestSuite) deleteNodeOverlay() error {
	s.t.Logf("Deleting NodeOverlay %s", s.testConfig.NodeOverlayTest.Name)
	return s.deleteClusterScopedByName(s.testConfig.NodeOverlayTest.Name, func() client.Object {
		return &karpenterv1alpha1.NodeOverlay{}
	})
}

// deleteOCINodeClass removes the OCINodeClass.
func (s *E2ETestSuite) deleteOCINodeClass() error {
	s.t.Logf("Deleting OCINodeClass %s", s.testConfig.OCINodeClass.Name)
	return s.deleteClusterScopedByName(s.testConfig.OCINodeClass.Name, func() client.Object {
		return &ociv1beta1.OCINodeClass{}
	})
}

// deleteClusterScopedByName deletes a cluster-scoped object by name.
func (s *E2ETestSuite) deleteClusterScopedByName(name string, factory func() client.Object) error {
	obj := factory()
	if err := s.ctrlClient.Get(s.ctx, client.ObjectKey{Name: name}, obj); err != nil {
		return err
	}
	if err := s.ctrlClient.Delete(s.ctx, obj); err != nil {
		return err
	}
	require.Eventually(s.t, func() bool {
		check := factory()
		err := s.ctrlClient.Get(s.ctx, client.ObjectKey{Name: name}, check)
		return apierrors.IsNotFound(err)
	}, ResourceCreationTimeout, PollInterval, "%s should be deleted", name)
	return nil
}

// deleteNodePool removes the NodePool.
func (s *E2ETestSuite) deleteNodePool() error {
	s.t.Logf("Deleting NodePool %s", s.testConfig.NodePool.Name)
	return s.deleteClusterScopedByName(s.testConfig.NodePool.Name, func() client.Object { return &karpenterv1.NodePool{} })
}

func (s *E2ETestSuite) deleteDeployment() error {
	deployment := &appsv1.Deployment{}
	if err := s.ctrlClient.Get(s.ctx, client.ObjectKey{Name: s.testConfig.TestDeployment.Name,
		Namespace: s.testConfig.Namespace}, deployment); err != nil {
		return err
	}
	s.t.Logf("Deleting Deployment %s in namespace %s", deployment.Name, s.testConfig.Namespace)
	if err := s.ctrlClient.Delete(s.ctx, deployment); err != nil {
		return err
	}
	require.Eventually(s.t, func() bool {
		err := s.ctrlClient.Get(s.ctx, client.ObjectKey{Name: s.testConfig.TestDeployment.Name,
			Namespace: s.testConfig.Namespace}, &appsv1.Deployment{})
		return apierrors.IsNotFound(err)
	}, ResourceCreationTimeout, PollInterval, "Deployment should be deleted before proceeding: %s",
		s.testConfig.TestDeployment.Name)
	return nil
}

// Deletes in reverse, validates non-existence
func (s *E2ETestSuite) teardown() {
	if err := s.deleteDeployment(); err != nil && !apierrors.IsNotFound(err) {
		s.t.Logf("Error deleting Deployment: %v", err)
	}

	s.ensureAllNodeClaimDeleted()

	if s.testConfig.NodeOverlayTest.Name != "" {
		if err := s.deleteNodeOverlay(); err != nil && !apierrors.IsNotFound(err) {
			s.t.Logf("Error deleting NodeOverlay: %v", err)
		}
	}

	if s.testConfig.StaticCapacityTest.NodePoolName != "" {
		if err := s.deleteClusterScopedByName(s.testConfig.StaticCapacityTest.NodePoolName, func() client.Object {
			return &karpenterv1.NodePool{}
		}); err != nil && !apierrors.IsNotFound(err) {
			s.t.Logf("Error deleting static NodePool: %v", err)
		}
	}

	if err := s.deleteNodePool(); err != nil && !apierrors.IsNotFound(err) {
		s.t.Logf("Error deleting NodePool: %v", err)
	}

	if err := s.deleteOCINodeClass(); err != nil && !apierrors.IsNotFound(err) {
		s.t.Logf("Error deleting OCINodeClass: %v", err)
	}
}

func (s *E2ETestSuite) ensureAllNodeClaimDeleted() {
	labels := client.MatchingLabels(map[string]string{NodePoolLabel: s.testConfig.NodePool.Name})
	require.Eventually(s.t, func() bool {
		var nodeClaimList karpenterv1.NodeClaimList
		err := s.ctrlClient.List(s.ctx, &nodeClaimList, labels)
		return err == nil && len(nodeClaimList.Items) == 0
	}, TestTimeout, PollInterval, "All NodeClaims should be deleted")
}

func (s *E2ETestSuite) setup() {
	// Pre-clean any existing resources from previous runs (delete only if they exist)
	if err := s.deleteDeployment(); err != nil && !apierrors.IsNotFound(err) {
		s.t.Logf("Warning: failed to delete existing test Deployment: %v", err)
	}

	if s.testConfig.NodeOverlayTest.Name != "" {
		if err := s.deleteNodeOverlay(); err != nil && !apierrors.IsNotFound(err) {
			s.t.Logf("Warning: failed to delete existing NodeOverlay: %v", err)
		}
	}

	if s.testConfig.StaticCapacityTest.NodePoolName != "" {
		if err := s.deleteClusterScopedByName(s.testConfig.StaticCapacityTest.NodePoolName, func() client.Object {
			return &karpenterv1.NodePool{}
		}); err != nil && !apierrors.IsNotFound(err) {
			s.t.Logf("Warning: failed to delete existing static NodePool: %v", err)
		}
	}

	// Ensure all NodeClaims for the test NodePool are deleted before proceeding
	s.ensureAllNodeClaimDeleted()

	// Delete NodePool if it exists
	if err := s.deleteNodePool(); err != nil && !apierrors.IsNotFound(err) {
		s.t.Logf("Warning: failed to delete existing NodePool: %v", err)
	}

	// Delete OCINodeClass if it exists
	if err := s.deleteOCINodeClass(); err != nil && !apierrors.IsNotFound(err) {
		s.t.Logf("Warning: failed to delete existing OCINodeClass: %v", err)
	}

	// Create fresh resources
	require.NoError(s.t, s.createOCINodeClass(), "failed to create OCINodeClass")
	require.NoError(s.t, s.createNodePool(), "failed to create NodePool")
	deployment := testDeployment(s.testConfig)
	require.NoError(s.t, s.ctrlClient.Create(s.ctx, deployment), "failed to create test deployment")
}

// NewE2ETestSuite creates a new test suite instance for OCI
func TestKarpenterE2EFlannel(t *testing.T) {
	s, done := createE2ETest(t, "KUBECONFIG", "testdata/e2e_test_config_flannel.json")
	if done {
		return
	}
	skipCleanup := strings.ToLower(strings.TrimSpace(os.Getenv("SKIP_CLEANUP"))) == TrueStr
	if skipCleanup {
		t.Log("SKIP_CLEANUP=true; skipping teardown")
	} else {
		defer s.teardown()
	}
	s.setup()

	t.Run("EndToEndFlow", func(t *testing.T) {
		s.TestScaleUp()
		s.TestSchedulingLabels()
		s.TestShapeConfig()
		s.TestCapacityReservation()
		s.TestDriftDetection()
		s.TestCostBasedConsolidation()
		// compute clusters are immutable, so have the test at last
		if largeShapesEnabled {
			s.TestComputeCluster()
		} else {
			t.Log("Skipping compute cluster test because LARGE_SHAPE_TEST_ENABLED != true")
		}
		s.TestScaleDown()
	})
}

func TestKarpenterE2ENpn(t *testing.T) {
	s, done := createE2ETest(t, "KUBECONFIG_NPN", "testdata/e2e_test_config_npn.json")
	if done {
		return
	}
	skipCleanup := strings.ToLower(strings.TrimSpace(os.Getenv("SKIP_CLEANUP"))) == TrueStr
	if skipCleanup {
		t.Log("SKIP_CLEANUP=true; skipping teardown")
	} else {
		defer s.teardown()
	}
	s.setup()

	t.Run("EndToEndFlow", func(t *testing.T) {
		s.TestScaleUp()
		s.TestNodeOverlay()
		s.TestNpnDriftDetection()
		s.TestScaleDown()
		s.TestStaticCapacity()
	})
}

func getOciConfigProvider(e2eTestConfig *KarpenterE2ETestConfig) (common.ConfigurationProvider, error) {
	switch e2eTestConfig.OciAuthMethodForTest {
	case "INSTANCE_PRINCIPAL":
		return auth.InstancePrincipalConfigurationProvider()
	default:
		u, err := user.Current()
		if err != nil {
			fmt.Printf("Error in finding user.")
			return nil, err
		}

		return common.CustomProfileSessionTokenConfigProvider(fmt.Sprintf("%s/.oci/config", u.HomeDir),
			e2eTestConfig.OciProfile), nil
	}
}

func createE2ETest(t *testing.T, kubeConfigEnvVar string, testConfigFile string) (*E2ETestSuite, bool) {
	ctx := context.Background()

	scheme := runtime.NewScheme()
	_ = ociv1beta1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	_ = policyv1.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)
	_ = npnv1beta1.AddToScheme(scheme)
	gv := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	metav1.AddToGroupVersion(scheme, gv)
	scheme.AddKnownTypes(gv,
		&karpenterv1.NodePool{},
		&karpenterv1.NodePoolList{},
		&karpenterv1.NodeClaim{},
		&karpenterv1.NodeClaimList{},
	)
	gvAlpha := schema.GroupVersion{Group: "karpenter.sh", Version: "v1alpha1"}
	metav1.AddToGroupVersion(scheme, gvAlpha)
	scheme.AddKnownTypes(gvAlpha,
		&karpenterv1alpha1.NodeOverlay{},
		&karpenterv1alpha1.NodeOverlayList{},
	)

	// Initialize Kubernetes client
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fallback to local kubeconfig for development
		loadingRules := &clientcmd.ClientConfigLoadingRules{
			ExplicitPath: os.Getenv(kubeConfigEnvVar),
		}
		configOverrides := &clientcmd.ConfigOverrides{ClusterInfo: api.Cluster{}}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			loadingRules,
			configOverrides,
		)
		config, err = kubeConfig.ClientConfig()
		require.NoError(t, err, "Failed to create Kubernetes config from kubeconfig")
	}

	// Initialize controller-runtime client
	ctrlClient, err := client.New(config, client.Options{Scheme: scheme})
	require.NoError(t, err, "Failed to create controller-runtime client")

	e2eTestConfig, err := loadE2ETestConfig(testConfigFile)
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		return nil, true
	}

	configProvider, err := getOciConfigProvider(e2eTestConfig)
	require.NoError(t, err, "Failed to create config provider for OCI auth method")

	computeClient, err := ocicore.NewComputeClientWithConfigurationProvider(configProvider)
	require.NoError(t, err, "Failed to create ComputeClient with OCI session auth")
	netWorkClient, err := ocicore.NewVirtualNetworkClientWithConfigurationProvider(configProvider)
	require.NoError(t, err, "Failed to create VCN client with OCI session auth")
	blockStorageClient, err := ocicore.NewBlockstorageClientWithConfigurationProvider(configProvider)
	require.NoError(t, err, "Failed to create block storage client with OCI session auth")
	rawHttpClient, err := rest.HTTPClientFor(config)
	require.NoError(t, err, "Failed to create kube http client")

	s := &E2ETestSuite{
		ctx:                ctx,
		t:                  t,
		ctrlClient:         ctrlClient,
		testConfig:         e2eTestConfig,
		computeClient:      &computeClient,
		networkClient:      &netWorkClient,
		blockStorageClient: &blockStorageClient,
		httpClient:         rawHttpClient,
		clusterApiHost:     config.Host,
	}
	return s, false
}

func (s *E2ETestSuite) TestScaleUp() {
	s.t.Run("ScaleUp", func(t *testing.T) {
		t.Log("Start Scaleup testing")
		s.ensureDeploymentReplicas(s.testConfig.TestDeployment.Name, s.testConfig.Namespace,
			s.testConfig.TestDeployment.Replicas)

		err := s.waitAndVerifyNodes(s.verifyScaleup, "")
		require.NoError(t, err, "Node don't came up correctly for scaleup test")
		testConfig := s.testConfig.OCINodeClass
		s.verifyNodeClaim(s.testConfig.NodePool.Name, karpenterv1.CapacityTypeOnDemand, testConfig.ImageId)
		err = s.checkPodsReady(s.testConfig.TestDeployment.Name, int(s.testConfig.TestDeployment.Replicas))
		require.NoError(t, err, "All pods should be running after scaleup test")
		nodeClass, err := s.GetOciNodeClass(s.testConfig.OCINodeClass.Name)
		require.NoError(t, err, "should be able to get nodeclass")
		s.verifyOciNodeClassStatusConditions(nodeClass, expectedConditions)
		s.verifyOciNodeClassStatusNetwork(nodeClass, testConfig.NsgName, testConfig.Nsgs[0],
			testConfig.SubnetName, testConfig.SubnetID)
		s.verifyOciNodeClassStatusVolume(nodeClass, testConfig.KmsKey, testConfig.KmsKeyName)
		t.Log("Scaleup test successful")
	})
}

func (s *E2ETestSuite) TestNodeOverlay() {
	s.t.Run("NodeOverlay", func(t *testing.T) {
		t.Log("Start NodeOverlay testing")
		t.Logf("NodeOverlay config: name=%s candidateShapes=%v preferredShape=%s priceAdjustment=%s",
			s.testConfig.NodeOverlayTest.Name,
			s.testConfig.NodeOverlayTest.CandidateShapes,
			s.testConfig.NodeOverlayTest.PreferredShape,
			s.testConfig.NodeOverlayTest.PriceAdjustment)

		mainNodePool := &karpenterv1.NodePool{}
		require.NoError(t, s.ctrlClient.Get(s.ctx, client.ObjectKey{Name: s.testConfig.NodePool.Name}, mainNodePool),
			"should be able to get main nodepool")
		originalRequirements := append([]karpenterv1.NodeSelectorRequirementWithMinValues(nil),
			mainNodePool.Spec.Template.Spec.Requirements...)

		t.Log("Scaling workload down and deleting existing NodeClaims before applying NodeOverlay")
		s.ensureDeploymentReplicas(s.testConfig.TestDeployment.Name, s.testConfig.Namespace, 0)
		s.ensureAllNodeClaimDeleted()

		t.Logf("Patching NodePool %s requirements to candidate shapes %v",
			s.testConfig.NodePool.Name, s.testConfig.NodeOverlayTest.CandidateShapes)
		require.NoError(t, s.PatchNodePoolRequirements(
			mainNodePool,
			[]string{corev1.LabelInstanceTypeStable, ociv1beta1.OciInstanceShape},
			map[string][]string{ociv1beta1.OciInstanceShape: s.testConfig.NodeOverlayTest.CandidateShapes},
		), "failed to patch nodepool for nodeoverlay test")

		t.Logf("Creating NodeOverlay %s and waiting for Ready", s.testConfig.NodeOverlayTest.Name)
		require.NoError(t, s.createNodeOverlay(), "failed to create nodeoverlay")
		require.NoError(t, wait.PollUntilContextTimeout(s.ctx, PollInterval, ResourceCreationTimeout, true,
			func(context.Context) (bool, error) {
				overlay := &karpenterv1alpha1.NodeOverlay{}
				if err := s.ctrlClient.Get(s.ctx, client.ObjectKey{Name: s.testConfig.NodeOverlayTest.Name}, overlay); err != nil {
					return false, err
				}
				readyCond, found := lo.Find(overlay.Status.Conditions, func(c status.Condition) bool {
					return c.Type == ReadyConditionType
				})
				if !found {
					s.t.Logf("NodeOverlay %s has no Ready condition yet", overlay.Name)
					return false, nil
				}
				if readyCond.ObservedGeneration != overlay.Generation {
					s.t.Logf("NodeOverlay %s Ready condition observedGeneration=%d generation=%d",
						overlay.Name, readyCond.ObservedGeneration, overlay.Generation)
					return false, nil
				}
				if string(readyCond.Status) != TrueConditionStatus {
					s.t.Logf("NodeOverlay %s Ready status=%s", overlay.Name, readyCond.Status)
					return false, nil
				}
				return true, nil
			}), "nodeoverlay should become ready")

		t.Logf("Scaling workload back up and validating preferred shape %s",
			s.testConfig.NodeOverlayTest.PreferredShape)
		s.ensureDeploymentReplicas(s.testConfig.TestDeployment.Name, s.testConfig.Namespace,
			s.testConfig.TestDeployment.Replicas)
		require.NoError(t, s.waitAndVerifyNodes(s.verifyInstanceShape, s.testConfig.NodeOverlayTest.PreferredShape),
			"nodeoverlay should steer provisioning to the preferred shape")
		s.verifyNodeClaim(s.testConfig.NodePool.Name, karpenterv1.CapacityTypeOnDemand, s.testConfig.OCINodeClass.ImageId)
		require.NoError(t, s.checkPodsReady(s.testConfig.TestDeployment.Name, int(s.testConfig.TestDeployment.Replicas)),
			"all pods should be running after nodeoverlay test")

		t.Log("Scaling workload down and removing NodeOverlay before restoring NodePool requirements")
		s.ensureDeploymentReplicas(s.testConfig.TestDeployment.Name, s.testConfig.Namespace, 0)
		s.ensureAllNodeClaimDeleted()
		require.NoError(t, s.deleteNodeOverlay(), "failed to delete nodeoverlay")

		t.Logf("Restoring original requirements on NodePool %s", s.testConfig.NodePool.Name)
		require.NoError(t, s.ctrlClient.Get(s.ctx, client.ObjectKey{Name: s.testConfig.NodePool.Name}, mainNodePool),
			"should be able to get nodepool for restore")
		mainNodePool.Spec.Template.Spec.Requirements = originalRequirements
		require.NoError(t, s.ctrlClient.Update(s.ctx, mainNodePool), "failed to restore nodepool requirements")

		t.Log("Scaling workload back up and validating baseline provisioning after cleanup")
		s.ensureDeploymentReplicas(s.testConfig.TestDeployment.Name, s.testConfig.Namespace,
			s.testConfig.TestDeployment.Replicas)
		require.NoError(t, s.waitAndVerifyNodes(s.verifyScaleup, ""),
			"nodepool should return to baseline provisioning after nodeoverlay cleanup")
		s.verifyNodeClaim(s.testConfig.NodePool.Name, karpenterv1.CapacityTypeOnDemand, s.testConfig.OCINodeClass.ImageId)
		require.NoError(t, s.checkPodsReady(s.testConfig.TestDeployment.Name, int(s.testConfig.TestDeployment.Replicas)),
			"all pods should be running after restoring nodepool requirements")
		t.Log("NodeOverlay test successful")
	})
}

func (s *E2ETestSuite) TestSchedulingLabels() {
	s.t.Log("Start SchedulingLabels testing")
	s.ensureDeploymentReplicas(s.testConfig.TestDeployment.Name, s.testConfig.Namespace,
		s.testConfig.TestDeployment.Replicas)

	flexShapeDeleteKey := ociv1beta1.OciDenseIoShape
	// LARGE_SHAPE_TEST_ENABLED env var (default "true") gates gpu-shape, baremetal-shape, and denseio-shape together
	if !largeShapesEnabled {
		// If large shapes are disabled, previous case is "instance-shape"; remove it before adding "flex-shape"
		flexShapeDeleteKey = ociv1beta1.OciInstanceShape
	}
	cases := []struct {
		name       string
		deleteKey  string
		addKey     string
		addValue   string
		verifyFunc func(*corev1.Node, any) bool
		expected   any
	}{
		{"instance-shape", corev1.LabelInstanceTypeStable, ociv1beta1.OciInstanceShape,
			"VM.Standard.E3.Flex", s.verifyInstanceShape, "VM.Standard.E3.Flex"},
		{"gpu-shape", ociv1beta1.OciInstanceShape, ociv1beta1.OciGpuShape, TrueStr,
			s.verifyGPUShape, ""},
		{"baremetal-shape", ociv1beta1.OciGpuShape, ociv1beta1.OciBmShape, TrueStr, s.verifyBareMetalShape, ""},
		{"denseio-shape", ociv1beta1.OciBmShape, ociv1beta1.OciDenseIoShape, TrueStr, s.verifyDenseIOShape, ""},
		{"flex-shape", flexShapeDeleteKey, ociv1beta1.OciFlexShape, TrueStr, s.verifyFlexShape, ""},
	}

	for _, tc := range cases {
		s.t.Run(tc.name, func(t *testing.T) {
			// Skip shape tests when feature flags are disabled
			if !largeShapesEnabled && (tc.name == "gpu-shape" || tc.name == "baremetal-shape" || tc.name == "denseio-shape") {
				t.Skip("Skipping large shape tests because LARGE_SHAPE_TEST_ENABLED != true")
			}
			// 1. patch NodePool to add requirement for addKey=addValue and remove deleteKey
			nodePool := &karpenterv1.NodePool{}
			require.NoError(t, s.ctrlClient.Get(s.ctx, client.ObjectKey{Name: s.testConfig.NodePool.Name}, nodePool))

			require.NoError(t, s.PatchNodePoolRequirements(
				nodePool,
				[]string{tc.deleteKey},
				map[string][]string{tc.addKey: {tc.addValue}},
			), "nodepool should be patched successfully")

			// 2. wait for nodes with the required label to be ready and verify they are correct
			s.t.Logf("Waiting for nodes with %s=%s to be ready", tc.addKey, tc.addValue)
			err := s.waitAndVerifyNodes(tc.verifyFunc, tc.addValue)
			require.NoError(s.t, err, "Node don't came up correctly for SchedulingLabels test "+tc.name)
			s.verifyNodeClaim(s.testConfig.NodePool.Name, karpenterv1.CapacityTypeOnDemand,
				s.testConfig.OCINodeClass.ImageId)
			err = s.checkPodsReady(s.testConfig.TestDeployment.Name, int(s.testConfig.TestDeployment.Replicas))
			require.NoError(s.t, err, "All pods should be running after SchedulingLabels test "+tc.name)
			s.t.Log("SchedulingLabels test successful for " + tc.name)
		})
	}
}

func (s *E2ETestSuite) TestShapeConfig() {
	s.t.Log("Start ShapeConfig testing")
	s.ensureDeploymentReplicas(s.testConfig.TestDeployment.Name, s.testConfig.Namespace,
		s.testConfig.TestDeployment.Replicas)
	nodePool := &karpenterv1.NodePool{}
	require.NoError(s.t, s.ctrlClient.Get(s.ctx, client.ObjectKey{Name: s.testConfig.NodePool.Name}, nodePool))

	cases := []struct {
		name         string
		patchFunc    func(*ociv1beta1.OCINodeClass)
		verifyFunc   func(*corev1.Node, any) bool
		capacityType string
		expected     string
	}{
		{
			name: "ShapeConfigAdd",
			patchFunc: func(p *ociv1beta1.OCINodeClass) {
				p.Spec.ShapeConfigs = createShapeConfig(s.testConfig.OCINodeClass.ShapeConfigs)
			},
			verifyFunc:   s.verifyShapeConfig,
			capacityType: karpenterv1.CapacityTypeOnDemand,
			expected:     "VM.Standard.E4.Flex.16o.128g.1_8b",
		},
		{
			name: "ShapeConfigDrift",
			patchFunc: func(p *ociv1beta1.OCINodeClass) {
				p.Spec.ShapeConfigs = createShapeConfig(s.testConfig.DriftTestData.DriftShapeConfigs)
			},
			verifyFunc:   s.verifyShapeConfig,
			capacityType: karpenterv1.CapacityTypeSpot,
			expected:     "VM.Standard.E4.Flex.4o.32g.1_1b",
		},
	}

	for _, tc := range cases {
		s.t.Run(tc.name, func(t *testing.T) {
			t.Logf("Start %s shape config testing", tc.name)
			require.NoError(s.t, s.PatchNodePoolRequirements(nodePool, []string{corev1.LabelInstanceTypeStable,
				karpenterv1.CapacityTypeLabelKey}, map[string][]string{
				ociv1beta1.OciInstanceShape:      {"VM.Standard.E4.Flex"},
				karpenterv1.CapacityTypeLabelKey: {tc.capacityType}}),
				"nodepool should be patched successfully")
			require.NoError(t, s.patchOCINodeClass(tc.patchFunc), "Failed to patch OCINodeClass for %s", tc.name)

			err := s.waitAndVerifyNodes(tc.verifyFunc, tc.expected)
			require.NoError(s.t, err, "Nodes should reflect the shape config change for %s", tc.name)
			s.verifyNodeClaim(s.testConfig.NodePool.Name, tc.capacityType, s.testConfig.OCINodeClass.ImageId)

			err = s.checkPodsReady(s.testConfig.TestDeployment.Name, int(s.testConfig.TestDeployment.Replicas))
			require.NoError(s.t, err, "All pods should be running after %s test", tc.name)

			s.t.Logf("%s shape config test successful", tc.name)
		})
	}
}

func (s *E2ETestSuite) TestCapacityReservation() {
	s.t.Log("Start CapacityReservation testing")
	s.ensureDeploymentReplicas(s.testConfig.TestDeployment.Name,
		s.testConfig.Namespace, s.testConfig.TestDeployment.Replicas)
	nodePool := &karpenterv1.NodePool{}
	require.NoError(s.t, s.ctrlClient.Get(s.ctx, client.ObjectKey{Name: s.testConfig.NodePool.Name}, nodePool))

	cases := []struct {
		name       string
		patchFunc  func(*ociv1beta1.OCINodeClass)
		verifyFunc func(*corev1.Node, any) bool
		expected   capacityReservationExpectedResult
	}{
		{
			name: "CapacityReservationAdd",
			patchFunc: func(p *ociv1beta1.OCINodeClass) {
				p.Spec.CapacityReservationConfigs = []*ociv1beta1.CapacityReservationConfig{
					{
						CapacityReservationFilter: &ociv1beta1.OciResourceSelectorTerm{
							CompartmentId: lo.ToPtr(s.testConfig.CompartmentID),
							DisplayName:   lo.ToPtr(s.testConfig.OCINodeClass.CapacityReservationName),
						},
					},
				}
				p.Spec.KubeletConfig = nil
				p.Spec.ShapeConfigs = createShapeConfig([]ociv1beta1.ShapeConfig{
					{
						Ocpus:       common.Float32(2),
						MemoryInGbs: common.Float32(32),
					},
				})
			},
			verifyFunc: s.verifyCapacityReservation,
			expected: capacityReservationExpectedResult{
				capacityReservationId:   s.testConfig.OCINodeClass.CapacityReservationId,
				capacityReservationName: s.testConfig.OCINodeClass.CapacityReservationName,
				shape:                   "VM.Standard.E5.Flex",
			},
		},
		{
			name: "CapacityReservationDrift",
			patchFunc: func(p *ociv1beta1.OCINodeClass) {
				p.Spec.CapacityReservationConfigs = []*ociv1beta1.CapacityReservationConfig{
					{
						CapacityReservationId: lo.ToPtr(s.testConfig.DriftTestData.DriftCapacityReservationId),
					},
				}
				p.Spec.ShapeConfigs = nil
			},
			verifyFunc: s.verifyCapacityReservation,
			expected: capacityReservationExpectedResult{
				capacityReservationId:   s.testConfig.DriftTestData.DriftCapacityReservationId,
				capacityReservationName: s.testConfig.DriftTestData.DriftCapacityReservationName,
				shape:                   "VM.Standard2.2",
			},
		},
	}

	require.NoError(s.t, s.PatchNodePoolRequirements(nodePool, []string{corev1.LabelInstanceTypeStable,
		karpenterv1.CapacityTypeLabelKey, ociv1beta1.OciInstanceShape, ociv1beta1.OciFlexShape},
		map[string][]string{ociv1beta1.OciInstanceShape: {"VM.Standard.E5.Flex", "VM.Standard2.2"},
			karpenterv1.CapacityTypeLabelKey: {karpenterv1.CapacityTypeReserved}}),
		"nodepool should be patched successfully")
	conditions := expectedConditions.Conditions
	expectedCond := ociv1beta1.OCINodeClassStatus{
		Conditions: append(conditions, status.Condition{
			Type:   "CapacityReservation",
			Status: "True",
			Reason: "CapacityReservation",
		}),
	}
	for _, tc := range cases {
		s.t.Run(tc.name, func(t *testing.T) {
			t.Logf("Start %s capacity reservation testing", tc.name)
			require.NoError(t, s.patchOCINodeClass(tc.patchFunc), "Failed to patch OCINodeClass for %s", tc.name)

			err := s.waitAndVerifyNodes(tc.verifyFunc, tc.expected)
			require.NoError(s.t, err, "Nodes should reflect the capacity reservation change for %s", tc.name)
			s.verifyNodeClaim(s.testConfig.NodePool.Name, karpenterv1.CapacityTypeReserved,
				s.testConfig.OCINodeClass.ImageId)

			err = s.checkPodsReady(s.testConfig.TestDeployment.Name, int(s.testConfig.TestDeployment.Replicas))
			require.NoError(s.t, err, "All pods should be running after %s test", tc.name)

			nodeClass, err := s.GetOciNodeClass(s.testConfig.OCINodeClass.Name)
			require.NoError(s.t, err, "should be able to get nodeclass")

			s.verifyOciNodeClassStatusConditions(nodeClass, expectedCond)
			s.verifyOciNodeClassStatusCapacityReservation(nodeClass, &tc.expected)

			s.t.Logf("%s capacity reservation test successful", tc.name)
		})
	}
	require.NoError(s.t, s.patchOCINodeClass(func(p *ociv1beta1.OCINodeClass) {
		p.Spec.CapacityReservationConfigs = nil
	}), "Failed to patch OCINodeClass")
	time.Sleep(10 * time.Second)
	nodeClass, err := s.GetOciNodeClass(s.testConfig.OCINodeClass.Name)
	require.NoError(s.t, err, "should be able to get nodeclass")
	s.verifyOciNodeClassStatusConditions(nodeClass, expectedConditions)
	s.verifyOciNodeClassStatusCapacityReservation(nodeClass, nil)
	s.t.Log("removing capacity reservation removes the capacity reservation status in nodeclass")

}

func (s *E2ETestSuite) TestComputeCluster() {
	s.t.Log("Start ComputeCluster testing")
	s.ensureDeploymentReplicas(s.testConfig.TestDeployment.Name,
		s.testConfig.Namespace, s.testConfig.TestDeployment.Replicas)
	nodePool := &karpenterv1.NodePool{}
	require.NoError(s.t, s.ctrlClient.Get(s.ctx, client.ObjectKey{Name: s.testConfig.NodePool.Name}, nodePool))
	require.NoError(s.t, s.PatchNodePoolRequirements(nodePool, []string{corev1.LabelInstanceTypeStable,
		karpenterv1.CapacityTypeLabelKey, ociv1beta1.OciInstanceShape},
		map[string][]string{ociv1beta1.OciInstanceShape: {"BM.Optimized3.36"},
			karpenterv1.CapacityTypeLabelKey: {karpenterv1.CapacityTypeOnDemand}}),
		"nodepool should be patched successfully")

	cases := []struct {
		name       string
		patchFunc  func(*ociv1beta1.OCINodeClass)
		verifyFunc func(*corev1.Node, any) bool
		expected   string
	}{
		{
			name: "ComputeClusterAdd",
			patchFunc: func(p *ociv1beta1.OCINodeClass) {
				p.Spec.ComputeClusterConfig = &ociv1beta1.ComputeClusterConfig{
					ComputeClusterFilter: &ociv1beta1.OciResourceSelectorTerm{
						CompartmentId: lo.ToPtr(s.testConfig.CompartmentID),
						DisplayName:   lo.ToPtr(s.testConfig.OCINodeClass.ComputeClusterName),
					},
				}
			},
			verifyFunc: s.verifyComputeCluster,
			expected:   s.testConfig.OCINodeClass.ComputeClusterId,
		},
	}
	conditions := expectedConditions.Conditions
	expectedCond := ociv1beta1.OCINodeClassStatus{
		Conditions: append(conditions, status.Condition{
			Type:   "ComputeCluster",
			Status: "True",
			Reason: "ComputeCluster",
		}),
	}

	for _, tc := range cases {
		s.t.Run(tc.name, func(t *testing.T) {
			t.Logf("Start %s compute cluster testing", tc.name)
			require.NoError(t, s.patchOCINodeClass(tc.patchFunc), "Failed to patch OCINodeClass for %s", tc.name)

			err := s.waitAndVerifyNodes(tc.verifyFunc, tc.expected)
			require.NoError(s.t, err, "Nodes should reflect the compute cluster change for %s", tc.name)
			s.verifyNodeClaim(s.testConfig.NodePool.Name, karpenterv1.CapacityTypeOnDemand,
				s.testConfig.DriftTestData.DriftImageId)

			err = s.checkPodsReady(s.testConfig.TestDeployment.Name, int(s.testConfig.TestDeployment.Replicas))
			require.NoError(s.t, err, "All pods should be running after %s test", tc.name)

			nodeClass, err := s.GetOciNodeClass(s.testConfig.OCINodeClass.Name)
			require.NoError(s.t, err, "should be able to get nodeclass")
			s.verifyOciNodeClassStatusConditions(nodeClass, expectedCond)
			s.verifyOciNodeClassStatusComputeCluster(nodeClass, s.testConfig.OCINodeClass.ComputeClusterId,
				s.testConfig.OCINodeClass.ComputeClusterName)
			s.t.Logf("%s compute cluster test successful", tc.name)
		})
	}
}

func (s *E2ETestSuite) TestDriftDetection() {
	s.t.Log("Start DriftDetection testing")
	nodePool := &karpenterv1.NodePool{}
	require.NoError(s.t, s.ctrlClient.Get(s.ctx, client.ObjectKey{Name: s.testConfig.NodePool.Name}, nodePool))
	require.NoError(s.t, s.PatchNodePoolRequirements(nodePool, []string{corev1.LabelInstanceTypeStable,
		karpenterv1.CapacityTypeLabelKey, ociv1beta1.OciInstanceShape},
		map[string][]string{ociv1beta1.OciInstanceShape: {"VM.Standard2.4"},
			karpenterv1.CapacityTypeLabelKey: {karpenterv1.CapacityTypeOnDemand}}),
		"nodepool should be patched successfully")
	s.ensureDeploymentReplicas(s.testConfig.TestDeployment.Name, s.testConfig.Namespace,
		s.testConfig.TestDeployment.Replicas)
	userData := map[string]string{"user_data": "I2Nsb3VkLWNvbmZpZwpydW5jbWQ6Ci0gb2tlIGJvb3RzdHJhcA=="}
	cases := []struct {
		name       string
		patchFunc  func(*ociv1beta1.OCINodeClass)
		verifyFunc func(*corev1.Node, any) bool
		expected   any
		imageId    string
	}{
		{
			name: "ubuntuImage",
			patchFunc: func(p *ociv1beta1.OCINodeClass) {
				p.Spec.VolumeConfig.BootVolumeConfig.ImageConfig.ImageId = &s.testConfig.OCINodeClass.UbuntuImagId
				p.Spec.VolumeConfig.BootVolumeConfig.ImageConfig.ImageFilter = nil
				p.Spec.Metadata = userData
			},
			verifyFunc: s.verifyImageId,
			expected:   s.testConfig.OCINodeClass.UbuntuImagId,
			imageId:    s.testConfig.OCINodeClass.UbuntuImagId,
		},
		{
			name: "customImage",
			patchFunc: func(p *ociv1beta1.OCINodeClass) {
				p.Spec.VolumeConfig.BootVolumeConfig.ImageConfig.ImageId = nil
				p.Spec.Metadata = nil
				p.Spec.VolumeConfig.BootVolumeConfig.ImageConfig.ImageFilter = &ociv1beta1.ImageSelectorTerm{
					OsFilter:        s.testConfig.OCINodeClass.ImageOsFilter,
					OsVersionFilter: s.testConfig.OCINodeClass.ImageOsVersionFilter,
					CompartmentId:   &s.testConfig.CompartmentID,
				}

			},
			verifyFunc: s.verifyImageId,
			expected:   s.testConfig.OCINodeClass.CustomImagId,
			imageId:    s.testConfig.OCINodeClass.CustomImagId,
		},
		{
			name: "olImageId",
			patchFunc: func(p *ociv1beta1.OCINodeClass) {
				p.Spec.VolumeConfig.BootVolumeConfig.ImageConfig.ImageId = &s.testConfig.DriftTestData.DriftImageId
				p.Spec.VolumeConfig.BootVolumeConfig.ImageConfig.ImageFilter = nil
			},
			verifyFunc: s.verifyImageId,
			expected:   s.testConfig.DriftTestData.DriftImageId,
			imageId:    s.testConfig.DriftTestData.DriftImageId,
		},
		{
			name: "kmsKeyId",
			patchFunc: func(p *ociv1beta1.OCINodeClass) {
				p.Spec.VolumeConfig.BootVolumeConfig.KmsKeyConfig.KmsKeyId = &s.testConfig.DriftTestData.DriftKmsKey
			},
			verifyFunc: s.verifyKmsKeyId,
			expected:   s.testConfig.DriftTestData.DriftKmsKey,
			imageId:    s.testConfig.DriftTestData.DriftImageId,
		},
		{
			name: "primaryVnicSubnet",
			patchFunc: func(p *ociv1beta1.OCINodeClass) {
				p.Spec.NetworkConfig.PrimaryVnicConfig.SubnetConfig.SubnetId = &s.testConfig.DriftTestData.DriftSubnetID
				p.Spec.NetworkConfig.PrimaryVnicConfig.SubnetConfig.SubnetFilter = nil
			},
			verifyFunc: s.verifyPrimaryVnicSubnet,
			expected:   s.testConfig.DriftTestData.DriftSubnetID,
			imageId:    s.testConfig.DriftTestData.DriftImageId,
		},
		{
			name: "primaryVnicNsg",
			patchFunc: func(p *ociv1beta1.OCINodeClass) {
				p.Spec.NetworkConfig.PrimaryVnicConfig.NetworkSecurityGroupConfigs = lo.Map(
					s.testConfig.DriftTestData.DriftNsg, func(item string, _ int) *ociv1beta1.NetworkSecurityGroupConfig {
						return &ociv1beta1.NetworkSecurityGroupConfig{NetworkSecurityGroupId: &item}
					})
			},
			verifyFunc: s.verifyPrimaryVnicNsg,
			expected:   s.testConfig.DriftTestData.DriftNsg,
			imageId:    s.testConfig.DriftTestData.DriftImageId,
		},
		{
			name: "pvEncryptionInTransit",
			patchFunc: func(p *ociv1beta1.OCINodeClass) {
				p.Spec.VolumeConfig.BootVolumeConfig.PvEncryptionInTransit = &s.testConfig.DriftTestData.DriftPVEncryptionInTransit
			},
			verifyFunc: s.verifyPvEncryptionInTransit,
			expected:   s.testConfig.DriftTestData.DriftPVEncryptionInTransit,
			imageId:    s.testConfig.DriftTestData.DriftImageId,
		},
		{
			name: "bootVolumeSize",
			patchFunc: func(p *ociv1beta1.OCINodeClass) {
				p.Spec.VolumeConfig.BootVolumeConfig.SizeInGBs = &s.testConfig.DriftTestData.DriftBootVolumeSizeInGB
			},
			verifyFunc: s.verifyBootVolumeSize,
			expected:   s.testConfig.DriftTestData.DriftBootVolumeSizeInGB,
			imageId:    s.testConfig.DriftTestData.DriftImageId,
		},
		{
			name: "compartment",
			patchFunc: func(p *ociv1beta1.OCINodeClass) {
				p.Spec.NodeCompartmentId = &s.testConfig.DriftTestData.DriftCompartment
			},
			verifyFunc: s.verifyCompartment,
			expected:   s.testConfig.DriftTestData.DriftCompartment,
			imageId:    s.testConfig.DriftTestData.DriftImageId,
		},
	}

	for _, tc := range cases {
		s.t.Run(tc.name, func(t *testing.T) {
			t.Logf("Start %s drift testing", tc.name)
			err := s.patchOCINodeClass(tc.patchFunc)
			require.NoError(s.t, err, "Failed to patch nodeclass for %s drift", tc.name)

			err = s.waitAndVerifyNodes(tc.verifyFunc, tc.expected)
			require.NoError(s.t, err, "Nodes should be drifted (%s)", tc.name)
			s.verifyNodeClaim(s.testConfig.NodePool.Name, karpenterv1.CapacityTypeOnDemand, tc.imageId)
			err = s.checkPodsReady(s.testConfig.TestDeployment.Name, int(s.testConfig.TestDeployment.Replicas))
			require.NoError(s.t, err, "All pods should be running after SchedulingLabels test "+tc.name)
			s.t.Log("DriftDetection test successful for " + tc.name)
			t.Logf("successfully validated node drifted for %s changed", tc.name)
		})
	}
}

func (s *E2ETestSuite) TestNpnDriftDetection() {
	s.t.Log("Start NPN drift detection testing")
	s.ensureDeploymentReplicas(s.testConfig.TestDeployment.Name, s.testConfig.Namespace,
		s.testConfig.TestDeployment.Replicas)

	cases := []struct {
		name       string
		patchFunc  func(*ociv1beta1.OCINodeClass)
		verifyFunc func(*corev1.Node, any) bool
		expected   any
	}{
		{
			name: "primaryVnicSubnet",
			patchFunc: func(p *ociv1beta1.OCINodeClass) {
				p.Spec.NetworkConfig.PrimaryVnicConfig.SubnetConfig.SubnetId = &s.testConfig.DriftTestData.DriftSubnetID
				p.Spec.NetworkConfig.PrimaryVnicConfig.SubnetConfig.SubnetFilter = nil
			},
			verifyFunc: s.verifyPrimaryVnicSubnet,
			expected:   s.testConfig.DriftTestData.DriftSubnetID,
		},
		{
			name: "primaryVnicNsg",
			patchFunc: func(p *ociv1beta1.OCINodeClass) {
				p.Spec.NetworkConfig.PrimaryVnicConfig.NetworkSecurityGroupConfigs = lo.Map(
					s.testConfig.DriftTestData.DriftNsg,
					func(item string, _ int) *ociv1beta1.NetworkSecurityGroupConfig {
						return &ociv1beta1.NetworkSecurityGroupConfig{NetworkSecurityGroupId: &item}
					})
			},
			verifyFunc: s.verifyPrimaryVnicNsg,
			expected:   s.testConfig.DriftTestData.DriftNsg,
		},
		{
			name: "updateSecondaryVnic",
			patchFunc: func(p *ociv1beta1.OCINodeClass) {
				expectedDriftSubnet := s.testConfig.DriftTestData.DriftSubnetID
				expectedDriftNSGs := s.testConfig.DriftTestData.DriftNsg
				p.Spec.NetworkConfig.SecondaryVnicConfigs[0].SubnetConfig.SubnetId = &expectedDriftSubnet
				p.Spec.NetworkConfig.SecondaryVnicConfigs[0].SubnetConfig.SubnetFilter = nil
				p.Spec.NetworkConfig.SecondaryVnicConfigs[0].NetworkSecurityGroupConfigs = lo.Map(
					expectedDriftNSGs, func(item string, _ int) *ociv1beta1.NetworkSecurityGroupConfig {
						return &ociv1beta1.NetworkSecurityGroupConfig{NetworkSecurityGroupId: &item}
					})
			},
			verifyFunc: s.verifySecondaryVnicUpdate,
			expected: map[string]interface{}{
				"subnet": s.testConfig.DriftTestData.DriftSubnetID,
				"nsgs":   s.testConfig.DriftTestData.DriftNsg,
			},
		},
		{
			name: "addSecondaryVnic",
			patchFunc: func(p *ociv1beta1.OCINodeClass) {
				firstConfig := p.Spec.NetworkConfig.SecondaryVnicConfigs[0]
				if firstConfig != nil {
					secondDisplayName := "SVnic2"
					secondConfig := firstConfig.DeepCopy()
					secondConfig.VnicDisplayName = &secondDisplayName
					secondConfig.SubnetConfig.SubnetId = &s.testConfig.DriftTestData.DriftSubnetID
					secondConfig.SubnetConfig.SubnetFilter = nil
					p.Spec.NetworkConfig.SecondaryVnicConfigs = append(p.Spec.NetworkConfig.SecondaryVnicConfigs, secondConfig)
				}
			},
			verifyFunc: s.verifyAddedSecondaryVnic,
			expected:   nil,
		},
	}

	for _, tc := range cases {
		s.t.Run(tc.name, func(t *testing.T) {
			t.Logf("Start %s drift testing", tc.name)

			err := s.patchOCINodeClass(tc.patchFunc)
			require.NoError(t, err, "Failed to patch OCINodeClass for %s drift", tc.name)

			err = s.waitAndVerifyNodes(tc.verifyFunc, tc.expected)
			require.NoError(t, err, "Nodes should be drifted for %s", tc.name)

			t.Logf("Successfully validated node drifted for %s change", tc.name)
		})
	}
}

func (s *E2ETestSuite) TestCostBasedConsolidation() {
	s.t.Log("Start cost based consolidation testing")
	s.ensureDeploymentReplicas(s.testConfig.TestDeployment.Name, s.testConfig.Namespace,
		s.testConfig.TestDeployment.Replicas)

	initialNodes, err := s.getReadyNodes(map[string]string{
		NodePoolLabel: s.testConfig.NodePool.Name,
	})
	require.NoError(s.t, err, "Could not get initial nodes")
	require.NotZero(s.t, len(initialNodes), "initial nodes size should not be zero")

	instance, err := s.getInstance(&initialNodes[0])
	require.NoError(s.t, err, "Should be able to get instance details")
	s.t.Logf("old shape of the nodes are is: %s", *instance.Shape)

	if *instance.Shape != "VM.Standard2.4" {
		s.t.Log("Initial shape is not VM.Standard2.4, patching NodePool to enforce it")

		// Get NodePool and patch to require only VM.Standard2.4
		nodePool := &karpenterv1.NodePool{}
		err = s.ctrlClient.Get(
			s.ctx,
			client.ObjectKey{Name: s.testConfig.NodePool.Name},
			nodePool,
		)
		require.NoError(s.t, err, "Failed to get NodePool for initial enforcement")

		err = s.PatchNodePoolRequirements(
			nodePool, []string{corev1.LabelInstanceTypeStable},
			map[string][]string{corev1.LabelInstanceTypeStable: {"VM.Standard2.4"}})
		require.NoError(s.t, err, "Failed to patch NodePool to enforce VM.Standard2.4")

		// Wait for node with VM.Standard2.4
		err = s.waitAndVerifyNodes(s.verifyInstanceShape, "VM.Standard2.4")
		require.NoError(s.t, err, "Should provision node with VM.Standard2.4 after enforcement")
		s.t.Log("Enforced initial shape to VM.Standard2.4")
	} else {
		require.Equal(s.t, "VM.Standard2.4", *instance.Shape, "KPO should provision node with lowest cost")
	}

	// Get and patch NodePool for consolidation
	nodePool := &karpenterv1.NodePool{}
	err = s.ctrlClient.Get(
		s.ctx,
		client.ObjectKey{Name: s.testConfig.NodePool.Name},
		nodePool,
	)
	require.NoError(s.t, err, "Failed to get NodePool")

	err = s.PatchNodePoolRequirements(
		nodePool,
		[]string{corev1.LabelInstanceTypeStable, ociv1beta1.OciInstanceShape},
		map[string][]string{corev1.LabelInstanceTypeStable: {"VM.Standard2.4", "VM.Standard2.1", "VM.Standard2.2"}})
	require.NoError(s.t, err, "Failed to update NodePool with new instance types")

	s.t.Logf("adding shapes VM.Standard2.1 and VM.Standard2.2 to the nodepool")

	cases := []struct {
		name       string
		replicas   int32
		verifyFunc func(*corev1.Node, any) bool
		expected   any
	}{
		{
			name:       "cost-disruption-3-replicas",
			replicas:   3,
			verifyFunc: s.verifyInstanceShape,
			expected:   "VM.Standard2.2",
		},
		{
			name:       "utilization-disruption-1-replica",
			replicas:   1,
			verifyFunc: s.verifyInstanceShape,
			expected:   "VM.Standard2.1",
		},
	}

	for _, tc := range cases {
		s.t.Run(tc.name, func(t *testing.T) {
			s.ensureDeploymentReplicas(s.testConfig.TestDeployment.Name, s.testConfig.Namespace, tc.replicas)

			err = s.waitAndVerifyNodes(tc.verifyFunc, tc.expected)
			require.NoError(s.t, err, "Nodes should be disrupted to %s", tc.expected)
			s.verifyNodeClaim(s.testConfig.NodePool.Name, karpenterv1.CapacityTypeOnDemand,
				s.testConfig.DriftTestData.DriftImageId)

			err = s.checkPodsReady(s.testConfig.TestDeployment.Name, int(tc.replicas))
			require.NoError(s.t, err, "All pods should be running after disruption")
			s.t.Logf("%s successful", tc.name)
		})
	}
}

func (s *E2ETestSuite) TestScaleDown() {
	// Fetch the deployment
	s.t.Log("Start Scaledown testing")
	s.ensureDeploymentReplicas(s.testConfig.TestDeployment.Name, s.testConfig.Namespace, 0)

	// Wait for nodes to be scaled down
	err := wait.PollUntilContextTimeout(s.ctx, PollInterval, TestTimeout, true, func(context.Context) (bool, error) {
		nodes, err := s.getReadyNodes(map[string]string{NodePoolLabel: s.testConfig.NodePool.Name})
		if err != nil {
			return false, err
		}
		return len(nodes) == 0, nil
	})
	s.ensureAllNodeClaimDeleted()
	require.NoError(s.t, err, "Nodes should be scaled down when not needed")
	s.t.Log("nodes scale down successfully")
}

func (s *E2ETestSuite) TestStaticCapacity() {
	s.t.Run("StaticCapacity", func(t *testing.T) {
		t.Log("Start StaticCapacity testing")

		staticPoolName := s.testConfig.StaticCapacityTest.NodePoolName
		if err := s.deleteClusterScopedByName(staticPoolName, func() client.Object {
			return &karpenterv1.NodePool{}
		}); err != nil && !apierrors.IsNotFound(err) {
			require.NoError(t, err, "failed to clean up static nodepool before test")
		}

		staticInstanceTypes := s.testConfig.StaticCapacityTest.InstanceTypes
		if len(staticInstanceTypes) == 0 {
			staticInstanceTypes = s.testConfig.NodePool.InstanceTypes
		}
		t.Logf("StaticCapacity config: nodePool=%s initialReplicas=%d scaledReplicas=%d instanceTypes=%v",
			staticPoolName,
			s.testConfig.StaticCapacityTest.InitialReplicas,
			s.testConfig.StaticCapacityTest.ScaledReplicas,
			staticInstanceTypes)

		staticPool := staticKarpenterNodePool(s.testConfig)
		t.Logf("Creating static NodePool %s with replicas=%d",
			staticPoolName, s.testConfig.StaticCapacityTest.InitialReplicas)
		require.NoError(t, s.ctrlClient.Create(s.ctx, staticPool), "failed to create static nodepool")
		defer func() {
			t.Logf("Cleaning up static NodePool %s", staticPoolName)
			if err := s.deleteClusterScopedByName(staticPoolName, func() client.Object {
				return &karpenterv1.NodePool{}
			}); err != nil && !apierrors.IsNotFound(err) {
				s.t.Logf("Error deleting static NodePool: %v", err)
			}
			labels := client.MatchingLabels(map[string]string{NodePoolLabel: staticPoolName})
			if err := wait.PollUntilContextTimeout(s.ctx, PollInterval, TestTimeout, true,
				func(context.Context) (bool, error) {
					var nodeClaimList karpenterv1.NodeClaimList
					if err := s.ctrlClient.List(s.ctx, &nodeClaimList, labels); err != nil {
						return false, err
					}
					return len(nodeClaimList.Items) == 0, nil
				}); err != nil {
				s.t.Logf("Error waiting for static NodeClaims cleanup: %v", err)
			}
		}()

		waitForStaticCounts := func(expected int64) {
			labels := client.MatchingLabels(map[string]string{NodePoolLabel: staticPoolName})
			t.Logf("Waiting for static NodePool %s to converge to replicas=%d", staticPoolName, expected)
			require.NoError(t, wait.PollUntilContextTimeout(s.ctx, PollInterval, NodeProvisionTimeout, true,
				func(context.Context) (bool, error) {
					nodes, err := s.getReadyNodes(labels)
					if err != nil {
						return false, err
					}
					if int64(len(nodes)) != expected {
						s.t.Logf("static pool ready nodes=%d expected=%d", len(nodes), expected)
						return false, nil
					}

					var nodeClaimList karpenterv1.NodeClaimList
					if err := s.ctrlClient.List(s.ctx, &nodeClaimList, labels); err != nil {
						return false, err
					}
					if int64(len(nodeClaimList.Items)) != expected {
						s.t.Logf("static pool nodeclaims=%d expected=%d", len(nodeClaimList.Items), expected)
						return false, nil
					}

					for _, nodeClaim := range nodeClaimList.Items {
						initialized := nodeClaim.StatusConditions().Get(karpenterv1.ConditionTypeInitialized)
						if !initialized.IsTrue() {
							registered := nodeClaim.StatusConditions().Get(karpenterv1.ConditionTypeRegistered)
							s.t.Logf(
								"static NodeClaim %s not initialized yet (initialized=%s reason=%s message=%s registered=%s/%s)",
								nodeClaim.Name,
								initialized.Status,
								initialized.Reason,
								initialized.Message,
								registered.Status,
								registered.Reason,
							)
							return false, nil
						}
						readyCond, found := lo.Find(nodeClaim.Status.Conditions, func(c status.Condition) bool {
							return c.Type == ReadyConditionType
						})
						if !found || string(readyCond.Status) != TrueConditionStatus {
							s.t.Logf("static NodeClaim %s not ready yet (status=%s reason=%s message=%s)",
								nodeClaim.Name, readyCond.Status, readyCond.Reason, readyCond.Message)
							return false, nil
						}
					}
					return true, nil
				}), "static capacity should converge to the expected replica count")
			t.Logf("Static NodePool %s converged to replicas=%d", staticPoolName, expected)
		}

		t.Log("Validating initial static replica count")
		waitForStaticCounts(s.testConfig.StaticCapacityTest.InitialReplicas)

		if s.testConfig.StaticCapacityTest.ScaledReplicas > s.testConfig.StaticCapacityTest.InitialReplicas {
			current := &karpenterv1.NodePool{}
			require.NoError(t, s.ctrlClient.Get(s.ctx, client.ObjectKey{Name: staticPoolName}, current),
				"failed to get static nodepool before scaling")
			t.Logf("Scaling static NodePool %s from replicas=%d to replicas=%d",
				staticPoolName,
				lo.FromPtr(current.Spec.Replicas),
				s.testConfig.StaticCapacityTest.ScaledReplicas)
			current.Spec.Replicas = lo.ToPtr(s.testConfig.StaticCapacityTest.ScaledReplicas)
			require.NoError(t, s.ctrlClient.Update(s.ctx, current), "failed to scale static nodepool")
			waitForStaticCounts(s.testConfig.StaticCapacityTest.ScaledReplicas)
		}
	})
}

func (s *E2ETestSuite) verifyScaleup(node *corev1.Node, _ any) bool {
	instance, err := s.getInstance(node)
	require.NoError(s.t, err, "Should be able to get instance details")
	require.Equal(s.t, "VM.Standard2.4", *instance.Shape,
		"KPO should provision node with lowest cost")
	require.Equal(s.t, s.testConfig.OCINodeClass.NodeCompartmentID, *instance.CompartmentId,
		"the CompartmentId should match with expected CompartmentId")
	require.Equal(s.t, s.testConfig.OCINodeClass.ImageId,
		*instance.SourceDetails.(ocicore.InstanceSourceViaImageDetails).ImageId,
		"ImageId should match with expected ImageId")
	require.Equal(s.t, s.testConfig.OCINodeClass.SshPubKey, instance.Metadata["ssh_authorized_keys"],
		"SshAuthorizedKeys should match with expected SshAuthorizedKeys")
	for key, expectedValue := range s.testConfig.OCINodeClass.Metadata {
		actualValue, ok := instance.Metadata[key]
		s.t.Logf("checking metadatam, key=%s, ok=%t, instance=%s", key, ok, *instance.Id)
		require.True(s.t, ok, "metadata should be set to compute metadata")
		require.Equal(s.t, expectedValue, actualValue, "metadata should be equal to compute metadata")
	}
	// Default FreeFrom tags
	expectedFreeFromTags := s.testConfig.OCINodeClass.FreeformTags
	expectedFreeFromTags[instancepkg.NodePoolOciFreeFormTagKey] = s.testConfig.NodePool.Name
	expectedFreeFromTags[instancepkg.NodeClassOciFreeFormTagKey] = s.testConfig.OCINodeClass.Name

	for key, expectedValue := range expectedFreeFromTags {
		actualValue, ok := instance.FreeformTags[key]
		s.t.Logf("checking FreeformTags, key=%s, ok=%t, instance=%s", key, ok, *instance.Id)
		require.True(s.t, ok, "FreeformTags key should exist in instance FreeformTags")
		require.Equal(s.t, expectedValue, actualValue, "FreeformTags value should match for key %s", key)
	}

	for ns, expectedTagMap := range s.testConfig.OCINodeClass.DefinedTags {
		actualTagMap, ok := instance.DefinedTags[ns]
		require.True(s.t, ok, "DefinedTags namespace %s should exist in instance DefinedTags", ns)
		for innerKey, expectedValue := range expectedTagMap {
			actualValue, ok := actualTagMap[innerKey]
			s.t.Logf("checking DefinedTags, namespace=%s, key=%s, ok=%t, instance=%s", ns, innerKey, ok, *instance.Id)
			require.True(s.t, ok, "DefinedTags key %s in namespace %s should exist", innerKey, ns)
			require.Equal(s.t, expectedValue, actualValue, "DefinedTags value should match for namespace %s key %s",
				ns, innerKey)
		}
	}
	bvAttachment, err := s.getBootVolumeAttachment(node)
	require.NoError(s.t, err, "Should not be any error to fetch bootVolume attachment")
	require.Equal(s.t,
		s.testConfig.OCINodeClass.PVEncryptionInTransit, *bvAttachment.IsPvEncryptionInTransitEnabled,
		"IsPvEncryptionInTransitEnabled should match with expected IsPvEncryptionInTransitEnabled")
	bootVolume, err := s.getBootVolume(*bvAttachment)
	require.NoError(s.t, err, "Should not be any error to fetch bootVolume")
	require.Equal(s.t, s.testConfig.OCINodeClass.BootVolumeSizeInGB, *bootVolume.SizeInGBs,
		"BootVolume SizeInGBs should match with expected BootVolume SizeInGBs")
	require.Equal(s.t, s.testConfig.OCINodeClass.KmsKey, *bootVolume.KmsKeyId,
		"KmsKey should match with expected KmsKey")
	subnetId, nsgIds, pVnic, err := s.getVnicSubnetAndNSGs(s.testConfig.OCINodeClass.NodeCompartmentID, *instance.Id)
	if err != nil {
		return false
	}
	require.Equal(s.t, s.testConfig.OCINodeClass.SubnetID, subnetId,
		"KmsKey should match with expected KmsKey")
	require.Equal(s.t, s.testConfig.OCINodeClass.Nsgs[0], nsgIds[0],
		"NsgId should match with expected NsgId")
	if s.testConfig.OCINodeClass.PrimaryVnicDisplayName != nil {
		require.Equal(s.t, s.testConfig.OCINodeClass.PrimaryVnicDisplayName, pVnic.DisplayName,
			"Primary Vnic DisplayName should match with expected DisplayName")
	}
	if s.testConfig.OCINodeClass.PrimaryVnicSkipSourceDestCheck != nil {
		require.Equal(s.t, s.testConfig.OCINodeClass.PrimaryVnicSkipSourceDestCheck, pVnic.SkipSourceDestCheck,
			"Primary Vnic SkipSourceDestCheck should match with expected SkipSourceDestCheck")
	}

	if s.testConfig.OciVcnIpNative {
		s.assertNpnAndSecondVnics(instance)
	}
	kubeletConfig, err := s.getKubeletConfig(node.Name)
	require.NoError(s.t, err, "Should not be any error to fetch KubeletConfig")

	expectedMaxPods := s.testConfig.OCINodeClass.KubeletConfig.MaxPods
	if s.testConfig.OciVcnIpNative && len(s.testConfig.OCINodeClass.SecondVnicConfigs) > 0 {
		// Max expected pod is the min of maxPods config in kubelet config and max allowed IP for NPN
		maxAllowedIp := int32(len(s.testConfig.OCINodeClass.SecondVnicConfigs) * 16)
		expectedMaxPods = min(expectedMaxPods, maxAllowedIp)
	}

	require.Equal(s.t, expectedMaxPods, kubeletConfig.KubeletConfig.MaxPods,
		"MaxPods should match with expected MaxPods in KubeletConfig")
	require.Equal(s.t, s.testConfig.OCINodeClass.KubeletConfig.PodsPerCore, kubeletConfig.KubeletConfig.PodsPerCore,
		"PodsPerCore should match with expected PodsPerCore in KubeletConfig")
	return true
}

func (s *E2ETestSuite) checkPodsReady(labelValue string, count int) error {
	err := wait.PollUntilContextTimeout(s.ctx, PollInterval, PodScheduleTimeout, true,
		func(context.Context) (bool, error) {
			var podList corev1.PodList
			labelSelector := client.MatchingLabels(map[string]string{"app": labelValue})
			if inerr := s.ctrlClient.List(s.ctx, &podList,
				client.InNamespace(s.testConfig.Namespace), labelSelector); inerr != nil {
				return false, inerr
			}

			runningCount := 0
			for _, pod := range podList.Items {
				if pod.Status.Phase == corev1.PodRunning {
					runningCount++
				}
			}
			return runningCount == count, nil
		})
	return err
}

func (s *E2ETestSuite) assertNpnAndSecondVnics(instance *ocicore.Instance) {
	npnObj, err := s.getNpnObjectForInstance(*instance.Id)
	require.NoError(s.t, err, "unable to get npn object")
	require.Equal(s.t, ocidSuffix(*instance.Id), npnObj.Name)

	secondVnicMap := s.getSecondVnicSubnetAndNSGs(s.testConfig.OCINodeClass.NodeCompartmentID, *instance.Id)
	s.t.Logf("secondVnicDetails %v", secondVnicMap)
	for index, value := range s.testConfig.OCINodeClass.SecondVnicConfigs {
		svnicDetails, ok := secondVnicMap[value.VnicDisplayname]
		require.True(s.t, ok, "SecondVnic should be created")

		require.Equal(s.t, value.VnicDisplayname, *svnicDetails.vnic.DisplayName,
			"SecondVnic VnicDisplayName should match with expected VnicDisplayName")
		require.Equal(s.t, value.SubnetId, *svnicDetails.vnicAttachment.SubnetId,
			"SecondVnic Subnet should match with expected Subnet")
		require.Equal(s.t, value.Nsgs[0], svnicDetails.vnic.NsgIds[0],
			"SecondVnic NsgId should match with expected NsgId")
		require.Equal(s.t, value.SkipSourceDestCheck, *svnicDetails.vnic.SkipSourceDestCheck,
			"SecondVnic SkipSourceDestCheck should match with expected SkipSourceDestCheck")

		require.Equal(s.t, value.IpCount, *npnObj.Spec.SecondaryVnics[index].CreateVnicDetails.IpCount,
			"SecondVnic IpCount should match with expected IpCount")
	}
}

func (s *E2ETestSuite) verifyImageId(node *corev1.Node, expected any) bool {
	actual, err := s.getOCIInstanceImageId(node)
	require.NoError(s.t, err, "there should not be error getting instance imageId")
	s.t.Logf("Actual ImageId is %s and expected ImageId is %s", actual, expected)
	return actual == expected
}

func (s *E2ETestSuite) verifyKmsKeyId(node *corev1.Node, expected any) bool {
	actual, err := s.getOCIInstanceKmsKeyId(node)
	require.NoError(s.t, err, "there should not be error getting instance KmsKeyId")
	s.t.Logf("Actual KmsKeyId is %s and expected KmsKeyId is %s", actual, expected)
	return actual == expected
}

func (s *E2ETestSuite) verifyPvEncryptionInTransit(node *corev1.Node, expected any) bool {
	bvAttachment, err := s.getBootVolumeAttachment(node)
	require.NoError(s.t, err, "there should not be error getting instance BootVolumeAttachment")
	s.t.Logf("Actual IsPvEncryptionInTransitEnabled is %t and expected IsPvEncryptionInTransitEnabled is %t",
		*bvAttachment.IsPvEncryptionInTransitEnabled, expected)
	return *bvAttachment.IsPvEncryptionInTransitEnabled == expected
}

func (s *E2ETestSuite) verifyBootVolumeSize(node *corev1.Node, expected any) bool {
	bvAttachment, err := s.getBootVolumeAttachment(node)
	require.NoError(s.t, err, "there should not be error getting instance BootVolumeAttachment")
	bootVolume, err := s.getBootVolume(*bvAttachment)
	require.NoError(s.t, err, "there should not be error getting instance getBootVolume")
	s.t.Logf("Actual BootVolumeSize is %d and expected IsPvEncryptionInTransitEnabled is %d",
		*bootVolume.SizeInGBs, expected)
	return *bootVolume.SizeInGBs == expected
}

func (s *E2ETestSuite) verifyCompartment(node *corev1.Node, expected any) bool {
	instance, err := s.getInstance(node)
	require.NoError(s.t, err, "there should not be error getting Compartment")
	s.t.Logf("Actual Compartment is %s and expected Compartment is %s", *instance.CompartmentId, expected)
	return *instance.CompartmentId == expected
}

func (s *E2ETestSuite) verifyPrimaryVnicSubnet(node *corev1.Node, expected any) bool {
	instanceId := node.Spec.ProviderID
	actualSubnetId, _, _, err := s.getVnicSubnetAndNSGs(s.testConfig.OCINodeClass.NodeCompartmentID, instanceId)
	if err != nil {
		return false
	}
	s.t.Logf("Actual Subnet is %s and expected Subnet is %s", actualSubnetId, expected)
	return actualSubnetId == expected
}

func (s *E2ETestSuite) verifyPrimaryVnicNsg(node *corev1.Node, expected any) bool {
	instanceId := node.Spec.ProviderID
	_, actualNsgs, _, err := s.getVnicSubnetAndNSGs(s.testConfig.OCINodeClass.NodeCompartmentID, instanceId)
	if err != nil {
		return false
	}
	s.t.Logf("Actual Nsg is %v and expected Nsg is %v", actualNsgs, expected)
	return lo.ElementsMatch(actualNsgs, expected.([]string))
}

func (s *E2ETestSuite) verifyInstanceShape(node *corev1.Node, expectedShape any) bool {
	instance, err := s.getInstance(node)
	require.NoError(s.t, err, "Should be able to get instance details")
	s.t.Logf("InstanceShape is %s, expected shape is %s", *instance.Shape, expectedShape)
	return expectedShape == *instance.Shape
}

func (s *E2ETestSuite) verifyGPUShape(node *corev1.Node, _ any) bool {
	instance, err := s.getInstance(node)
	require.NoError(s.t, err, "Should be able to get instance details")
	s.t.Logf("InstanceShape is %s, expected shape is GPU", *instance.Shape)
	return strings.Contains(strings.ToUpper(*instance.Shape), "GPU")
}

func (s *E2ETestSuite) verifyBareMetalShape(node *corev1.Node, _ any) bool {
	instance, err := s.getInstance(node)
	require.NoError(s.t, err, "Should be able to get instance details")
	s.t.Logf("InstanceShape is %s, expected shape is BM", *instance.Shape)
	return instancetype.IsBmShape(*instance.Shape)
}

func (s *E2ETestSuite) verifyDenseIOShape(node *corev1.Node, _ any) bool {
	instance, err := s.getInstance(node)
	require.NoError(s.t, err, "Should be able to get instance details")
	s.t.Logf("InstanceShape is %s, expected shape is DENSEIO", *instance.Shape)
	return strings.Contains(strings.ToUpper(*instance.Shape), "DENSEIO")
}

func (s *E2ETestSuite) verifyFlexShape(node *corev1.Node, _ any) bool {
	instance, err := s.getInstance(node)
	require.NoError(s.t, err, "Should be able to get instance details")
	s.t.Logf("InstanceShape is %s, expected shape is FLEX", *instance.Shape)
	return strings.Contains(strings.ToUpper(*instance.Shape), "FLEX")
}

func (s *E2ETestSuite) verifySecondaryVnicUpdate(node *corev1.Node, expected any) bool {
	instance, err := s.getInstance(node)
	require.NoError(s.t, err, "Should be able to get instance details")
	instanceId := *instance.Id
	compartmentId := s.testConfig.OCINodeClass.NodeCompartmentID
	secondaryVnicDetails := s.getSecondVnicSubnetAndNSGs(compartmentId, instanceId)
	s.t.Logf("Instance should have 1 secondary vnics, actual secondary vnic size is %d", len(secondaryVnicDetails))
	if len(secondaryVnicDetails) < 1 {
		return false
	}

	for _, v := range secondaryVnicDetails {
		expectedMap := expected.(map[string]any)
		expectedSubnet := expectedMap["subnet"].(string)
		expectedNSGs := expectedMap["nsgs"].([]string)

		s.t.Logf("Actual subnet is %v and expected subnet is %v", *v.vnicAttachment.SubnetId, expectedSubnet)
		subnetEqual := expectedSubnet == *v.vnicAttachment.SubnetId
		if !subnetEqual {
			return false
		}

		s.t.Logf("Actual Nsg is %v and expected Nsg is %v", v.vnic.NsgIds, expectedNSGs)
		nsgEqual := lo.ElementsMatch(v.vnic.NsgIds, expectedNSGs)
		if !nsgEqual {
			return false
		}
	}
	return true
}

func (s *E2ETestSuite) verifyAddedSecondaryVnic(node *corev1.Node, _ any) bool {
	instance, err := s.getInstance(node)
	require.NoError(s.t, err, "Should be able to get instance details")
	instanceId := *instance.Id
	compartmentId := s.testConfig.OCINodeClass.NodeCompartmentID
	secondaryVnicDetails := s.getSecondVnicSubnetAndNSGs(compartmentId, instanceId)
	s.t.Logf("Secondary VNIC details: %v", secondaryVnicDetails)
	return len(secondaryVnicDetails) == 2
}

func (s *E2ETestSuite) verifyShapeConfig(node *corev1.Node, expected any) bool {
	instance, err := s.getInstance(node)
	require.NoError(s.t, err, "Should be able to get instance details")

	sc := instance.ShapeConfig
	require.NotNil(s.t, sc, "ShapeConfig should not be nil")

	expectedStr, ok := expected.(string)
	if !ok {
		s.t.Logf("Expected must be a string, got %T", expected)
		return false
	}

	shape := *instance.Shape

	ocpuStr := fmt.Sprintf("%.0fo", *sc.Ocpus)
	memoryStr := fmt.Sprintf("%.0fg", *sc.MemoryInGBs)

	baselineEnum := sc.BaselineOcpuUtilization
	baselineStr := string(baselineEnum)
	if strings.HasPrefix(baselineStr, "BASELINE_") {
		baselineStr = strings.TrimPrefix(baselineStr, "BASELINE_") + "b"
	} else {
		return false
	}
	constructed := shape + "." + ocpuStr + "." + memoryStr + "." + baselineStr
	s.t.Logf("Constructed ShapeConfig string: %s; Expected: %s", constructed, expectedStr)

	return constructed == expectedStr
}

func (s *E2ETestSuite) verifyCapacityReservation(node *corev1.Node, expected any) bool {
	expectedRes := expected.(capacityReservationExpectedResult)
	instance, err := s.getInstance(node)
	require.NoError(s.t, err, "Should be able to get instance details")
	if instance.CapacityReservationId != nil {
		s.t.Logf("Capacity Reservation Actual %s; Expected: %s, instanceShape Actual %s; Expected: %s",
			*instance.CapacityReservationId, expectedRes.capacityReservationId, *instance.Shape, expectedRes.shape)
		return (expectedRes.capacityReservationId == *instance.CapacityReservationId) &&
			(expectedRes.shape == *instance.Shape)
	} else {
		return false
	}
}

func (s *E2ETestSuite) verifyComputeCluster(node *corev1.Node, expected any) bool {
	instance, err := s.getInstance(node)
	require.NoError(s.t, err, "Should be able to get instance details")
	instanceListRequest := ocicore.ListInstancesRequest{
		CompartmentId:    instance.CompartmentId,
		ComputeClusterId: common.String(expected.(string)),
		LifecycleState:   ocicore.InstanceLifecycleStateRunning,
	}
	listResp, err := s.computeClient.ListInstances(s.ctx, instanceListRequest)
	require.NoError(s.t, err, "Should be able to list instance with compute cluster")
	s.t.Logf("list instance with compute cluster, Actual count %d; Expected count: %d", len(listResp.Items), 1)
	found := false
	for _, item := range listResp.Items {
		if *item.Id == *instance.Id {
			found = true
		}
	}
	return found
}

func (s *E2ETestSuite) waitAndVerifyNodes(verifyFn func(*corev1.Node, any) bool, expected any) error {
	return wait.PollUntilContextTimeout(s.ctx, PollInterval, NodeProvisionTimeout,
		true, func(context.Context) (bool, error) {
			nodes, err := s.getReadyNodes(map[string]string{NodePoolLabel: s.testConfig.NodePool.Name})
			if err != nil {
				s.t.Logf("Error getting nodes: %v", err)
				return false, err
			}

			// Verify each node using the provided verification function
			var verifiedNodes []corev1.Node
			for _, node := range nodes {
				if verifyFn(&node, expected) {
					verifiedNodes = append(verifiedNodes, node)
				}
			}
			return len(nodes) > 0 && len(verifiedNodes) == len(nodes), nil
		})
}

// getInstancePrimaryVnicOCID finds the OCID of the primary VNIC for the instance.
func (s *E2ETestSuite) getVnicSubnetAndNSGs(compartmentId,
	instanceId string) (string, []string, *ocicore.GetVnicResponse, error) {
	listVnicsReq := ocicore.ListVnicAttachmentsRequest{
		CompartmentId: &compartmentId,
		InstanceId:    &instanceId,
	}
	vnicResp, err := s.computeClient.ListVnicAttachments(s.ctx, listVnicsReq)
	require.NoError(s.t, err, "unable to ListVnicAttachments")
	for _, vnicAttachment := range vnicResp.Items {
		var vnic ocicore.GetVnicResponse
		vnic, err = s.networkClient.GetVnic(s.ctx, ocicore.GetVnicRequest{
			VnicId: vnicAttachment.VnicId,
		})
		if err != nil {
			return "", nil, nil, err
		}
		if vnic.IsPrimary != nil && *vnic.IsPrimary && vnic.Vnic.Id != nil {
			return *vnic.SubnetId, vnic.NsgIds, &vnic, nil
		}
	}
	return "", nil, nil, nil
}

// getSecondVnicSubnetAndNSGs finds second VNICs for the instance.
func (s *E2ETestSuite) getSecondVnicSubnetAndNSGs(compartmentId, instanceId string) map[string]secondVnicDetails {
	listVnicsReq := ocicore.ListVnicAttachmentsRequest{
		CompartmentId: &compartmentId,
		InstanceId:    &instanceId,
	}

	vnicResp, err := s.computeClient.ListVnicAttachments(s.ctx, listVnicsReq)
	require.NoError(s.t, err, "unable to ListVnicAttachments")

	secondVnicMap := make(map[string]secondVnicDetails)
	for _, vnicAttachment := range vnicResp.Items {
		if vnicAttachment.LifecycleState == ocicore.VnicAttachmentLifecycleStateAttached &&
			vnicAttachment.VnicId != nil {
			var vnic ocicore.GetVnicResponse
			vnic, err = s.networkClient.GetVnic(s.ctx, ocicore.GetVnicRequest{
				VnicId: vnicAttachment.VnicId,
			})
			require.NoError(s.t, err, "unable to get vnic")

			attachementDisplayName := ""
			if vnicAttachment.DisplayName != nil {
				attachementDisplayName = *vnicAttachment.DisplayName
			}
			vnicDisplayName := ""
			if vnic.DisplayName != nil {
				vnicDisplayName = *vnic.DisplayName
			}
			vnicIsPrimary := ""
			if vnic.IsPrimary != nil {
				vnicIsPrimary = strconv.FormatBool(*vnic.IsPrimary)
			}
			s.t.Logf("VnicAttachment displayName %s, Vnic displayName %s, IsPrimary %s",
				attachementDisplayName, vnicDisplayName, vnicIsPrimary)

			if (vnic.IsPrimary == nil || !*vnic.IsPrimary) && vnic.Vnic.Id != nil {
				secondVnicMap[*vnic.DisplayName] = secondVnicDetails{
					vnicAttachment: vnicAttachment,
					vnic:           vnic.Vnic,
				}
			}
		}
	}
	return secondVnicMap
}

func (s *E2ETestSuite) getOCIInstanceImageId(node *corev1.Node) (string, error) {
	instance, err := s.getInstance(node)
	if err != nil {
		return "", err
	}

	details := instance.SourceDetails.(ocicore.InstanceSourceViaImageDetails)
	return *details.ImageId, nil
}

func (s *E2ETestSuite) getInstance(node *corev1.Node) (*ocicore.Instance, error) {
	instanceOCID := node.Spec.ProviderID
	getReq := ocicore.GetInstanceRequest{
		InstanceId: &instanceOCID,
	}
	getResp, err := s.computeClient.GetInstance(s.ctx, getReq)
	if err != nil {
		return nil, err
	}
	return &getResp.Instance, nil
}

func (s *E2ETestSuite) getOCIInstanceKmsKeyId(node *corev1.Node) (string, error) {
	bvAttachment, err := s.getBootVolumeAttachment(node)
	if err != nil {
		return "", nil
	}
	bootVolume, err := s.getBootVolume(*bvAttachment)
	if err != nil {
		return "", err
	}
	return *bootVolume.KmsKeyId, nil
}

func (s *E2ETestSuite) getBootVolumeAttachment(node *corev1.Node) (*ocicore.BootVolumeAttachment, error) {
	instance, err := s.getInstance(node)
	if err != nil {
		return nil, err
	}

	listBvaResp, err := s.computeClient.ListBootVolumeAttachments(s.ctx, ocicore.ListBootVolumeAttachmentsRequest{
		CompartmentId:      instance.CompartmentId,
		InstanceId:         instance.Id,
		AvailabilityDomain: instance.AvailabilityDomain,
	})
	if err != nil {
		return nil, err
	}
	return &listBvaResp.Items[0], nil
}

func (s *E2ETestSuite) getBootVolume(bvAttachment ocicore.BootVolumeAttachment) (*ocicore.BootVolume, error) {
	bootVolumeResp, err := s.blockStorageClient.GetBootVolume(s.ctx, ocicore.GetBootVolumeRequest{
		BootVolumeId: bvAttachment.BootVolumeId,
	})
	if err != nil {
		return nil, err
	}
	return &bootVolumeResp.BootVolume, nil
}

func (s *E2ETestSuite) GetOciNodeClass(name string) (*ociv1beta1.OCINodeClass, error) {
	nodeClass := &ociv1beta1.OCINodeClass{}
	err := s.ctrlClient.Get(
		s.ctx,
		client.ObjectKey{Name: name},
		nodeClass,
	)
	if err != nil {
		return nil, err
	}
	return nodeClass, nil
}

// patchOCINodeClass loads the OCINodeClass and applies the provided mutate function, then patches it.
func (s *E2ETestSuite) patchOCINodeClass(mutate func(*ociv1beta1.OCINodeClass)) error {
	nodeClass, err := s.GetOciNodeClass(s.testConfig.OCINodeClass.Name)
	require.NoError(s.t, err, "should be able to get nodeclass")
	patched := nodeClass.DeepCopy()
	mutate(patched)
	return s.ctrlClient.Patch(
		s.ctx, patched, client.MergeFrom(nodeClass),
	)
}

func (s *E2ETestSuite) getReadyNodes(labels client.MatchingLabels) ([]corev1.Node, error) {
	var nodeList corev1.NodeList
	if err := s.ctrlClient.List(s.ctx, &nodeList, labels); err != nil {
		return nil, err
	}
	var readyNodes []corev1.Node
	for _, node := range nodeList.Items {
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				readyNodes = append(readyNodes, node)
				break
			}
		}
	}
	return readyNodes, nil
}

func loadE2ETestConfig(configPath string) (*KarpenterE2ETestConfig, error) {
	configFile, err := os.Open(configPath)
	if err != nil {
		return nil, err
	}

	data, err := io.ReadAll(configFile)

	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config KarpenterE2ETestConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return &config, nil
}

// PatchNodePoolRequirement removes requirements with keys in deleteKeys and adds new requirements from addReqs.
// addReqs is a map where key is the NodeSelector key and value is the slice of values (using In operator).
// Returns error on failure.
func (s *E2ETestSuite) PatchNodePoolRequirements(nodePool *karpenterv1.NodePool,
	deleteKeys []string, addReqs map[string][]string) error {
	// Deep copy for patch target
	patched := nodePool.DeepCopy()

	// Remove any requirement with keys in deleteKeys
	patched.Spec.Template.Spec.Requirements = lo.Filter(
		patched.Spec.Template.Spec.Requirements,
		func(r karpenterv1.NodeSelectorRequirementWithMinValues, _ int) bool {
			for _, dk := range deleteKeys {
				if r.Key == dk {
					return false
				}
			}
			return true
		})

	// Add new requirements from addReqs
	for addKey, values := range addReqs {
		if len(values) == 0 {
			continue
		}
		newReq := karpenterv1.NodeSelectorRequirementWithMinValues{
			Key:       addKey,
			Operator:  corev1.NodeSelectorOpIn,
			Values:    values,
			MinValues: nil,
		}
		patched.Spec.Template.Spec.Requirements = append(patched.Spec.Template.Spec.Requirements, newReq)
	}

	// Patch over the original
	return s.ctrlClient.Patch(s.ctx, patched, client.MergeFrom(nodePool))
}

func (s *E2ETestSuite) ensureDeploymentReplicas(name, namespace string, replicas int32) {
	deployment := &appsv1.Deployment{}

	// Get the current deployment
	err := s.ctrlClient.Get(s.ctx, client.ObjectKey{Name: name, Namespace: namespace}, deployment)
	require.NoError(s.t, err, "unable to get deployment")

	if *deployment.Spec.Replicas == replicas {
		return
	}
	// Update the replicas
	deployment.Spec.Replicas = &replicas
	err = s.ctrlClient.Update(s.ctx, deployment)
	require.NoError(s.t, err, "unable to scale deployment")
}

func (s *E2ETestSuite) getNpnObjectForInstance(instanceId string) (*npnv1beta1.NativePodNetwork, error) {
	npnObj := &npnv1beta1.NativePodNetwork{}
	npnName := ocidSuffix(instanceId)

	err := s.ctrlClient.Get(s.ctx, client.ObjectKey{Name: npnName}, npnObj)
	if err != nil {
		return nil, err
	}

	return npnObj, nil
}

func (s *E2ETestSuite) verifyOciNodeClassStatusConditions(nodeClass *ociv1beta1.OCINodeClass,
	expectedConditions ociv1beta1.OCINodeClassStatus) {
	// Verify actual conditions match expected
	for _, actualCondition := range nodeClass.Status.Conditions {
		found := false
		for _, expectedCondition := range expectedConditions.Conditions {
			if actualCondition.Type == expectedCondition.Type {
				require.Equal(s.t, expectedCondition.Status, actualCondition.Status,
					"Condition %s status should match", expectedCondition.Type)
				require.Equal(s.t, expectedCondition.Reason, actualCondition.Reason,
					"Condition %s reason should match", expectedCondition.Type)
				s.t.Logf("expected condition type %s, status: %s, reason: %s; actual condition type %s, "+
					"status: %s, reason: %s", expectedCondition.Type, expectedCondition.Status,
					expectedCondition.Reason, actualCondition.Type, actualCondition.Status, actualCondition.Reason)
				found = true
				break
			}
		}
		require.Truef(s.t, found, "Expected condition %s not found in actual conditions", actualCondition.Type)
	}

	// Verify all expected conditions are present
	for _, expectedCondition := range expectedConditions.Conditions {
		found := false
		for _, actualCondition := range nodeClass.Status.Conditions {
			if actualCondition.Type == expectedCondition.Type {
				found = true
				break
			}
		}
		require.Truef(s.t, found, "Expected condition %s not present", expectedCondition.Type)
	}
}

func (s *E2ETestSuite) getKubeletConfig(nodeName string) (*NodeResponse, error) {
	fullUrl := fmt.Sprintf("%s/api/v1/nodes/%s/proxy/configz", s.clusterApiHost, nodeName)
	s.t.Logf("Kubelet Confg Query URL = %s", fullUrl)

	resp, err := s.httpClient.Get(fullUrl)
	if err != nil {
		return nil, err
	}

	defer func() {
		err := resp.Body.Close()
		if err != nil {
			fmt.Printf("Error %s", err.Error())
		}
	}()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var kubeletConfig NodeResponse
	if err := json.Unmarshal(b, &kubeletConfig); err != nil {
		return nil, err
	}

	return &kubeletConfig, nil
}

func createShapeConfig(shapeConfig []ociv1beta1.ShapeConfig) []*ociv1beta1.ShapeConfig {
	shapeConfigsPtr := make([]*ociv1beta1.ShapeConfig, len(shapeConfig))
	for i, cfg := range shapeConfig {
		cfgCopy := cfg
		shapeConfigsPtr[i] = &cfgCopy
	}
	return shapeConfigsPtr
}

func (s *E2ETestSuite) verifyOciNodeClassStatusNetwork(nodeClass *ociv1beta1.OCINodeClass,
	nsgName, nsgId, subnetName, subnetId string) {
	require.NotNil(s.t, nodeClass.Status.Network, "Network status should be populated")
	require.NotNil(s.t, nodeClass.Status.Network.PrimaryVnic, "PrimaryVnic should be populated")

	pv := nodeClass.Status.Network.PrimaryVnic
	require.Equal(s.t, subnetId, pv.Subnet.SubnetId, "Subnet ID should match expected")
	require.Equal(s.t, subnetName, pv.Subnet.DisplayName, "Subnet name should match expected")

	require.Len(s.t, pv.NetworkSecurityGroups, 1, "Should have exactly one NSG")
	nsg := pv.NetworkSecurityGroups[0]
	require.Equal(s.t, nsgId, nsg.NetworkSecurityGroupId, "NSG ID should match expected")
	require.Equal(s.t, nsgName, nsg.DisplayName, "NSG name should match expected")
}

func (s *E2ETestSuite) verifyOciNodeClassStatusVolume(nodeClass *ociv1beta1.OCINodeClass, kmsKey, kmsKeyName string) {
	require.NotNil(s.t, nodeClass.Status.Volume, "Volume status should be populated")

	require.NotNil(s.t, nodeClass.Status.Volume.ImageCandidates, "Image should be populated")
	require.GreaterOrEqual(s.t, len(nodeClass.Status.Volume.ImageCandidates), 1, "Should have at least one ImageCandidate")
	require.NotNil(s.t, nodeClass.Status.Volume.KmsKeys, "KmsKey should be populated")
	require.Len(s.t, nodeClass.Status.Volume.KmsKeys, 1, "Should have exactly one Kms key")
	kms := nodeClass.Status.Volume.KmsKeys[0]
	require.Equal(s.t, kmsKey, kms.KmsKeyId, "KMS Key ID should match expected")
	require.Equal(s.t, kmsKeyName, kms.DisplayName, "KMS Key name should match expected")
	s.t.Logf("OciNodeClass Status ImageCandidates=%+v, KmsKeys=%+v",
		lo.Map(nodeClass.Status.Volume.ImageCandidates, func(ic *ociv1beta1.Image,
			_ int) ociv1beta1.Image {
			return *ic
		}), lo.Map(nodeClass.Status.Volume.KmsKeys,
			func(k *ociv1beta1.KmsKey, _ int) ociv1beta1.KmsKey { return *k }))
}

func (s *E2ETestSuite) verifyOciNodeClassStatusCapacityReservation(nodeClass *ociv1beta1.OCINodeClass,
	expected *capacityReservationExpectedResult) {
	if expected != nil {
		require.NotNil(s.t, nodeClass.Status.CapacityReservations, "CapacityReservations status should be populated")
		require.Len(s.t, nodeClass.Status.CapacityReservations, 1, "Should have exactly one CapacityReservation")
		cr := nodeClass.Status.CapacityReservations[0]
		require.Equal(s.t, expected.capacityReservationId, cr.CapacityReservationId,
			"CapacityReservation ID should match expected")
		require.Equal(s.t, expected.capacityReservationName, cr.DisplayName, "CapacityReservation name should match expected")
		require.NotNil(s.t, cr.AvailabilityDomain, "capacity reservation AD should not be nil")
		s.t.Logf("OciNodeClass Status CapacityReservation=%v", nodeClass.Status.CapacityReservations)
	} else {
		require.Nil(s.t, nodeClass.Status.CapacityReservations, "CapacityReservations status should be nil when no config")
	}
}

func (s *E2ETestSuite) verifyOciNodeClassStatusComputeCluster(nodeClass *ociv1beta1.OCINodeClass,
	computeClusterId string, computeClusterName string) {
	if computeClusterId != "" {
		require.NotNil(s.t, nodeClass.Status.ComputeCluster, "ComputeCluster status should be populated")
		cc := nodeClass.Status.ComputeCluster
		require.Equal(s.t, computeClusterId, cc.ComputeClusterId, "ComputeCluster ID should match expected")
		require.Equal(s.t, computeClusterName, cc.DisplayName, "ComputeCluster name should match expected")
		require.NotNil(s.t, cc.AvailabilityDomain, "compute cluster AD should not be nil")
		s.t.Logf("OciNodeClass Status ComputeCluster=%v", nodeClass.Status.ComputeCluster)
	} else {
		require.Nil(s.t, nodeClass.Status.ComputeCluster, "ComputeCluster status should be nil when no config")
	}

}

func (s *E2ETestSuite) verifyNodeClaim(nodePoolName string, expectedCapacityType string, imageId string) {
	labels := client.MatchingLabels(map[string]string{NodePoolLabel: nodePoolName})

	err := wait.PollUntilContextTimeout(s.ctx, PollInterval, NodeProvisionTimeout, true,
		func(context.Context) (bool, error) {
			var nodeClaimList karpenterv1.NodeClaimList
			if err := s.ctrlClient.List(s.ctx, &nodeClaimList, labels); err != nil {
				s.t.Logf("failed to list NodeClaims: %v", err)
				return false, err
			}

			if len(nodeClaimList.Items) == 0 {
				s.t.Logf("No NodeClaims yet for NodePool %s", nodePoolName)
				return false, nil
			}

			for _, nodeClaim := range nodeClaimList.Items {
				// Must be registered/initialized
				if !nodeClaim.StatusConditions().Get(karpenterv1.ConditionTypeInitialized).IsTrue() {
					s.t.Logf("NodeClaim %s not initialized/registered yet; waiting", nodeClaim.Name)
					return false, nil
				}

				// Ready must be True
				readyCond, readyFound := lo.Find(nodeClaim.Status.Conditions, func(c status.Condition) bool {
					return c.Type == ReadyConditionType
				})
				if !readyFound || string(readyCond.Status) != TrueConditionStatus {
					s.t.Logf("NodeClaim %s Ready not satisfied (found=%t, status=%s)",
						nodeClaim.Name, readyFound, string(readyCond.Status))
					return false, nil
				}

				// Drifted, if present, must be False
				driftedCond, driftedFound := lo.Find(nodeClaim.Status.Conditions, func(c status.Condition) bool {
					return c.Type == "Drifted"
				})
				if driftedFound && string(driftedCond.Status) != "False" {
					s.t.Logf("NodeClaim %s is drifted (status=%s); waiting to be False",
						nodeClaim.Name, string(driftedCond.Status))
					return false, nil
				}

				// Capacity type must match
				actualCapacityType := nodeClaim.Labels[karpenterv1.CapacityTypeLabelKey]
				if actualCapacityType != expectedCapacityType {
					s.t.Logf("NodeClaim %s capacity-type=%s; expected=%s",
						nodeClaim.Name, actualCapacityType, expectedCapacityType)
					return false, nil
				}

				// ImageID must match
				if nodeClaim.Status.ImageID != imageId {
					s.t.Logf("NodeClaim %s ImageID=%s; expected=%s",
						nodeClaim.Name, nodeClaim.Status.ImageID, imageId)
					return false, nil
				}
			}
			return true, nil
		})

	require.NoError(s.t, err, "NodeClaims for NodePool %s should become Ready, registered, "+
		"and match expected attributes", nodePoolName)
}
