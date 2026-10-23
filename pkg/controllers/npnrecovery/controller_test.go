/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package npnrecovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/awslabs/operatorpkg/status"
	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	npnv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/npn/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instance"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/network"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpapis "sigs.k8s.io/karpenter/pkg/apis"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

const (
	testInstanceID = "ocid1.instance.oc1.phx.abc123"
	testNpnName    = "abc123"
)

var launchedAt = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// The controller only calls GetInstanceCached and ResolveNetworkConfig; embedding the interfaces makes any
// other call panic, which would flag the controller reaching for something it should not.
type fakeInstanceProvider struct {
	instance.Provider
}

func (f *fakeInstanceProvider) GetInstanceCached(_ context.Context, id string) (*instance.InstanceInfo, error) {
	return &instance.InstanceInfo{Instance: &ocicore.Instance{Id: &id}}, nil
}

type fakeNetworkProvider struct {
	network.Provider
	err error
}

func (f *fakeNetworkProvider) ResolveNetworkConfig(_ context.Context,
	_ *ociv1beta1.NetworkConfig) (*network.NetworkResolveResult, error) {
	return &network.NetworkResolveResult{}, f.err
}

type fakeNpnProvider struct{}

func (f *fakeNpnProvider) CreateNpnCustomObject(_ context.Context, inst *ocicore.Instance,
	_ *ociv1beta1.OCINodeClass, _ *network.NetworkResolveResult) (*npnv1beta1.NativePodNetwork, error) {
	return &npnv1beta1.NativePodNetwork{
		ObjectMeta: metav1.ObjectMeta{Name: testNpnName},
		Spec:       npnv1beta1.NativePodNetworkSpec{ID: *inst.Id},
	}, nil
}

func (f *fakeNpnProvider) NpnCluster() bool { return true }

func testScheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, ociv1beta1.AddToScheme(s))
	require.NoError(t, npnv1beta1.AddToScheme(s))

	gv := schema.GroupVersion{Group: karpapis.Group, Version: "v1"}
	s.AddKnownTypes(gv, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{})
	metav1.AddToGroupVersion(s, gv)
	return s
}

func launchedNodeClaim() *karpv1.NodeClaim {
	nc := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "nodeclaim-1"},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Group: ociv1beta1.GroupVersion.Group, Kind: "OCINodeClass", Name: "default",
			},
		},
		Status: karpv1.NodeClaimStatus{ProviderID: testInstanceID},
	}
	nc.Status.Conditions = []status.Condition{{
		Type:               karpv1.ConditionTypeLaunched,
		Status:             metav1.ConditionTrue,
		Reason:             karpv1.ConditionTypeLaunched,
		LastTransitionTime: metav1.NewTime(launchedAt),
	}}
	return nc
}

type testEnv struct {
	controller *Controller
	client     client.Client
	network    *fakeNetworkProvider
}

func newTestEnv(t *testing.T, sinceLaunch time.Duration, objs ...client.Object) *testEnv {
	nodeClass := &ociv1beta1.OCINodeClass{ObjectMeta: metav1.ObjectMeta{Name: "default"}}
	kubeClient := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(append(objs, nodeClass)...).
		WithStatusSubresource(&karpv1.NodeClaim{}).Build()
	clk := clocktesting.NewFakeClock(launchedAt.Add(sinceLaunch))
	networkProvider := &fakeNetworkProvider{}

	return &testEnv{
		controller: NewController(clk, kubeClient, kubeClient, &fakes.FakeCloudProvider{},
			&fakeInstanceProvider{}, networkProvider, &fakeNpnProvider{}),
		client:  kubeClient,
		network: networkProvider,
	}
}

func (e *testEnv) npnExists(t *testing.T) bool {
	err := e.client.Get(context.Background(), types.NamespacedName{Name: testNpnName}, &npnv1beta1.NativePodNetwork{})
	if apierrors.IsNotFound(err) {
		return false
	}
	require.NoError(t, err)
	return true
}

func (e *testEnv) nodeClaimExists(t *testing.T) bool {
	err := e.client.Get(context.Background(), types.NamespacedName{Name: "nodeclaim-1"}, &karpv1.NodeClaim{})
	if apierrors.IsNotFound(err) {
		return false
	}
	require.NoError(t, err)
	return true
}

func TestReconcile_RecreatesMissingNpn(t *testing.T) {
	nc := launchedNodeClaim()
	env := newTestEnv(t, 2*time.Minute, nc)

	result, err := env.controller.Reconcile(context.Background(), nc)
	require.NoError(t, err)
	assert.Zero(t, result)
	assert.True(t, env.npnExists(t))
	assert.True(t, env.nodeClaimExists(t))
}

func TestReconcile_WaitsOutGracePeriod(t *testing.T) {
	nc := launchedNodeClaim()
	env := newTestEnv(t, 20*time.Second, nc)

	result, err := env.controller.Reconcile(context.Background(), nc)
	require.NoError(t, err)
	assert.Equal(t, gracePeriod-20*time.Second, result.RequeueAfter)
	assert.False(t, env.npnExists(t))
}

func TestReconcile_LeavesExistingNpnAlone(t *testing.T) {
	nc := launchedNodeClaim()
	existing := &npnv1beta1.NativePodNetwork{ObjectMeta: metav1.ObjectMeta{Name: testNpnName}}
	env := newTestEnv(t, time.Hour, nc, existing)

	result, err := env.controller.Reconcile(context.Background(), nc)
	require.NoError(t, err)
	assert.Zero(t, result)
	// well past the timeout, but the NPN exists, so the nodeclaim must not be touched.
	assert.True(t, env.nodeClaimExists(t))
}

func TestReconcile_ReturnsErrorForBackoffWhenRecreateFails(t *testing.T) {
	nc := launchedNodeClaim()
	env := newTestEnv(t, 2*time.Minute, nc)
	env.network.err = errors.New("subnet lookup failed")

	_, err := env.controller.Reconcile(context.Background(), nc)
	assert.ErrorContains(t, err, "subnet lookup failed")
	assert.False(t, env.npnExists(t))
	assert.True(t, env.nodeClaimExists(t))
}

func TestReconcile_DeletesNodeClaimAfterTimeout(t *testing.T) {
	nc := launchedNodeClaim()
	env := newTestEnv(t, recoveryTimeout, nc)

	result, err := env.controller.Reconcile(context.Background(), nc)
	require.NoError(t, err)
	assert.Zero(t, result)
	assert.False(t, env.nodeClaimExists(t))
	assert.False(t, env.npnExists(t))
}

func TestReconcile_IgnoresNodeClaimsOutOfScope(t *testing.T) {
	for name, mutate := range map[string]func(*karpv1.NodeClaim){
		"no provider ID": func(nc *karpv1.NodeClaim) { nc.Status.ProviderID = "" },
		"not launched":   func(nc *karpv1.NodeClaim) { nc.Status.Conditions = nil },
		"initialized": func(nc *karpv1.NodeClaim) {
			nc.Status.Conditions = append(nc.Status.Conditions, status.Condition{
				Type: karpv1.ConditionTypeInitialized, Status: metav1.ConditionTrue,
				Reason: karpv1.ConditionTypeInitialized, LastTransitionTime: metav1.NewTime(launchedAt),
			})
		},
		"deleting": func(nc *karpv1.NodeClaim) {
			nc.DeletionTimestamp = &metav1.Time{Time: launchedAt}
			nc.Finalizers = []string{karpv1.TerminationFinalizer}
		},
	} {
		t.Run(name, func(t *testing.T) {
			nc := launchedNodeClaim()
			mutate(nc)
			env := newTestEnv(t, time.Hour, nc)

			result, err := env.controller.Reconcile(context.Background(), nc)
			require.NoError(t, err)
			assert.Zero(t, result)
			assert.False(t, env.npnExists(t))
			assert.True(t, env.nodeClaimExists(t))
		})
	}
}
