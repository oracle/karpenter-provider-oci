/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package capacitydiscovery

import (
	"context"
	"testing"

	"github.com/awslabs/operatorpkg/object"
	"github.com/awslabs/operatorpkg/status"
	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancetype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpapis "sigs.k8s.io/karpenter/pkg/apis"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// nodeutils.IsManaged resolves an object's kind against the global client-go scheme, which
// cmd/main.go populates at startup. Do the same here, or IsManaged panics on OCINodeClass.
func init() {
	utilruntime.Must(ociv1beta1.AddToScheme(clientgoscheme.Scheme))
}

// recordingProvider captures what the controller passes on, so these tests assert the controller's
// own decisions rather than capacity arithmetic, which is covered in the instancetype package.
type recordingProvider struct {
	nodes      []*v1.Node
	nodeClaims []*karpv1.NodeClaim
	err        error
}

func (r *recordingProvider) UpdateInstanceTypeCapacityFromNode(_ context.Context, node *v1.Node,
	nodeClaim *karpv1.NodeClaim) error {
	r.nodes = append(r.nodes, node)
	r.nodeClaims = append(r.nodeClaims, nodeClaim)
	return r.err
}

func (r *recordingProvider) DiscoveryEnabled() bool { return true }

func testCloudProvider() cloudprovider.CloudProvider {
	return &fakes.FakeCloudProvider{
		NameStub: func() string { return "oci" },
		GetSupportedNodeClassesStub: func() []status.Object {
			return []status.Object{&ociv1beta1.OCINodeClass{}}
		},
	}
}

// karpenter's apis/v1 package registers its types into the client-go scheme from an init(), so
// build on that rather than a bare scheme, which would not know about NodeClaim.
func testScheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, ociv1beta1.AddToScheme(s))

	gv := schema.GroupVersion{Group: karpapis.Group, Version: "v1"}
	s.AddKnownTypes(gv, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{})
	metav1.AddToGroupVersion(s, gv)
	return s
}

func managedNode(memory string) *v1.Node {
	n := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-1",
			Labels: map[string]string{
				v1.LabelInstanceTypeStable: "VM.Standard.E5.Flex.8o.32g.1_1b",
				karpv1.NodePoolLabelKey:    "default",
				// IsManaged matches this label against the cloud provider's node classes; without
				// it the controller treats the node as someone else's and returns immediately.
				karpv1.NodeClassLabelKey(object.GVK(&ociv1beta1.OCINodeClass{}).GroupKind()): "default",
			},
		},
		Spec: v1.NodeSpec{ProviderID: "oci://instance-1"},
	}
	if memory != "" {
		n.Status.Capacity = v1.ResourceList{v1.ResourceMemory: resource.MustParse(memory)}
	}
	return n
}

func nodeClaimFor(node *v1.Node) *karpv1.NodeClaim {
	return &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "nodeclaim-1",
			Labels: map[string]string{karpv1.NodePoolLabelKey: "default"},
		},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Group: ociv1beta1.GroupVersion.Group, Kind: "OCINodeClass", Name: "default",
			},
		},
		Status: karpv1.NodeClaimStatus{
			ProviderID: node.Spec.ProviderID,
			ImageID:    "ocid1.image.oc1..a",
		},
	}
}

func newTestController(t *testing.T, p *recordingProvider, objs ...client.Object) (*Controller, *v1.Node) {
	node := managedNode("30890Mi")
	all := append([]client.Object{node}, objs...)
	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(all...).
		// NodeClaimForNode lists by this field index; without it the fake client returns nothing
		// and every lookup looks like "no NodeClaim".
		WithIndex(&karpv1.NodeClaim{}, "status.providerID", func(o client.Object) []string {
			return []string{o.(*karpv1.NodeClaim).Status.ProviderID}
		}).
		Build()
	return NewController(kubeClient, testCloudProvider(), p), node
}

// The measurement is keyed by the Node's instance-type label and the NodeClaim's ImageID, so the
// OCINodeClass is never read. A NodeClass that has been deleted or renamed must not discard an
// otherwise valid observation.
func TestReconcile_RecordsWithoutReadingTheNodeClass(t *testing.T) {
	p := &recordingProvider{}
	c, node := newTestController(t, p, nodeClaimFor(managedNode("")))

	// Deliberately no OCINodeClass object exists in the client.
	res, err := c.Reconcile(context.Background(), node)

	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)
	require.Len(t, p.nodes, 1, "the observation must be recorded even with no NodeClass present")
	assert.Equal(t, node.Name, p.nodes[0].Name)

	// The NodeClaim carries the ImageID that forms half the cache key, so passing the wrong one -
	// or nil - would leave the provider unable to record anything.
	require.Len(t, p.nodeClaims, 1)
	require.NotNil(t, p.nodeClaims[0], "the fetched NodeClaim must be passed through")
	assert.Equal(t, "ocid1.image.oc1..a", p.nodeClaims[0].Status.ImageID)
	assert.Equal(t, node.Spec.ProviderID, p.nodeClaims[0].Status.ProviderID,
		"the NodeClaim passed must be the one matching this node")
}

// A node with no NodeClaim cannot be attributed to an instance type and image, so there is nothing
// to learn from it - and it must not fail the reconcile.
func TestReconcile_SkipsNodeWithoutNodeClaim(t *testing.T) {
	p := &recordingProvider{}
	c, node := newTestController(t, p)

	res, err := c.Reconcile(context.Background(), node)

	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)
	assert.Empty(t, p.nodes, "a node with no NodeClaim must not be recorded")
}

// A node that registered before publishing its memory would never be revisited, because the watch
// only fires on the transition into the registered state. It must be requeued instead.
func TestReconcile_RequeuesWhenCapacityNotReported(t *testing.T) {
	p := &recordingProvider{err: instancetype.ErrCapacityNotReported}
	c, node := newTestController(t, p, nodeClaimFor(managedNode("")))

	res, err := c.Reconcile(context.Background(), node)

	require.NoError(t, err, "a node that has not reported memory is not an error")
	assert.Equal(t, capacityNotReportedRequeue, res.RequeueAfter)
}

// Any other failure is genuinely unexpected and should surface rather than be swallowed.
func TestReconcile_PropagatesUnexpectedErrors(t *testing.T) {
	p := &recordingProvider{err: assert.AnError}
	c, node := newTestController(t, p, nodeClaimFor(managedNode("")))

	_, err := c.Reconcile(context.Background(), node)

	assert.ErrorIs(t, err, assert.AnError)
}

// Nodes this provider does not manage belong to someone else; touching them would record
// measurements from a different cloud's instance types.
func TestReconcile_IgnoresUnmanagedNodes(t *testing.T) {
	p := &recordingProvider{}
	node := managedNode("30890Mi")
	delete(node.Labels, karpv1.NodeClassLabelKey(object.GVK(&ociv1beta1.OCINodeClass{}).GroupKind()))
	kubeClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(node).Build()

	c := NewController(kubeClient, testCloudProvider(), p)
	res, err := c.Reconcile(context.Background(), node)

	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)
	assert.Empty(t, p.nodes, "an unmanaged node must not be recorded")
}
