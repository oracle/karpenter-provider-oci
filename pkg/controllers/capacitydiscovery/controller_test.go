/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package capacitydiscovery

import (
	"context"
	discovery "github.com/oracle/karpenter-provider-oci/pkg/providers/capacitydiscovery"
	"testing"
	"time"

	"github.com/awslabs/operatorpkg/object"
	"github.com/awslabs/operatorpkg/status"
	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	"github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpapis "sigs.k8s.io/karpenter/pkg/apis"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	nodeutils "sigs.k8s.io/karpenter/pkg/utils/node"
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

func (r *recordingProvider) RecordNodeCapacity(_ context.Context, node *v1.Node,
	nodeClaim *karpv1.NodeClaim) error {
	r.nodes = append(r.nodes, node)
	r.nodeClaims = append(r.nodeClaims, nodeClaim)
	return r.err
}

func (r *recordingProvider) Enabled() bool { return true }

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
			UID:  "node-1-uid",
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

// A node that registered before publishing its memory would never be revisited, because the watch
// only fires on the transition into the registered state. It must be requeued instead.
func TestReconcile_RequeuesWhenCapacityNotReported(t *testing.T) {
	p := &recordingProvider{err: discovery.ErrCapacityNotReported}
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

// The ordering hazard: Karpenter can mark a Node registered before the NodeClaim's
// status.providerID is persisted, and the two are paired by that field. Reconciling in the gap
// finds nothing - and nothing brings us back, because the registered transition has already fired
// and NodeClaims are not watched. So this must ask to be retried, not quietly finish.
func TestReconcile_RequeuesWhenTheNodeClaimIsNotYetVisible(t *testing.T) {
	p := &recordingProvider{}
	// No NodeClaim object at all, standing in for one whose providerID has not landed yet.
	c, node := newTestController(t, p)

	result, err := c.Reconcile(context.Background(), node)

	require.NoError(t, err, "a NodeClaim that has not appeared yet is not an error")
	assert.Equal(t, nodeClaimNotFoundRequeue, result.RequeueAfter,
		"the observation must be retried rather than dropped")
	assert.Empty(t, p.nodes, "and nothing should have been recorded from it")
}

// ...and once the NodeClaim lands, the retry records the measurement that would otherwise have
// been lost. This is the half that proves the requeue is worth having.
func TestReconcile_RecordsOnceTheNodeClaimAppears(t *testing.T) {
	p := &recordingProvider{}
	c, node := newTestController(t, p)

	result, err := c.Reconcile(context.Background(), node)
	require.NoError(t, err)
	require.NotZero(t, result.RequeueAfter)
	require.Empty(t, p.nodes)

	// The NodeClaim's providerID is persisted, as it would be moments later.
	require.NoError(t, c.kubeClient.Create(context.Background(), nodeClaimFor(node)))

	result, err = c.Reconcile(context.Background(), node)

	require.NoError(t, err)
	assert.Zero(t, result.RequeueAfter, "nothing left to wait for")
	require.Len(t, p.nodes, 1, "the retry must record what the first pass could not")
	assert.Equal(t, "ocid1.image.oc1..a", p.nodeClaims[0].Status.ImageID)
}

// Retrying has to stop. A Node whose NodeClaim is long gone would otherwise be revisited every
// few seconds for as long as it exists, learning nothing.
func TestReconcile_StopsRetryingOnceTheWindowCloses(t *testing.T) {
	p := &recordingProvider{}
	c, node := newTestController(t, p)
	// As though the first miss were long ago and we had been retrying since.
	gaveUpAt := time.Now().Add(-nodeClaimLookupWindow - time.Minute)
	c.nodeClaimMisses.firstMiss(node.UID, gaveUpAt)

	result, err := c.Reconcile(context.Background(), node)

	require.NoError(t, err)
	assert.Zero(t, result.RequeueAfter, "past the window this is absence, not a race")
	assert.Empty(t, p.nodes)

	assert.Equal(t, gaveUpAt, c.nodeClaimMisses.firstMiss(node.UID, time.Now()),
		"the decision to give up stays on record, so the next pass does not read as a fresh miss")
}

// The window runs from the first miss, not from the Node's creation. Karpenter allows a Node
// several minutes to register, so a creation-anchored bound would refuse to retry for exactly the
// late-registering Node this exists to protect.
func TestReconcile_RetriesForANodeThatRegistersLongAfterCreation(t *testing.T) {
	p := &recordingProvider{}
	c, node := newTestController(t, p)
	node.CreationTimestamp = metav1.NewTime(time.Now().Add(-30 * time.Minute))

	result, err := c.Reconcile(context.Background(), node)

	require.NoError(t, err)
	assert.Equal(t, nodeClaimNotFoundRequeue, result.RequeueAfter,
		"an old Node registering now still deserves the retry")
}

// One Node's bookkeeping must not restart another's window. Two Nodes that both ran out at the
// same time are the case a sweep would get wrong: reconciling the first would drop the second's
// timestamp, and the second would then be handed a fresh window instead of stopping.
func TestReconcile_OneNodeDoesNotResetAnothersWindow(t *testing.T) {
	p := &recordingProvider{}
	c, node := newTestController(t, p)
	expired := time.Now().Add(-nodeClaimLookupWindow - time.Minute)
	c.nodeClaimMisses.firstMiss(node.UID, expired)
	c.nodeClaimMisses.firstMiss("other-node-uid", expired)

	result, err := c.Reconcile(context.Background(), node)
	require.NoError(t, err)
	require.Zero(t, result.RequeueAfter)

	assert.Equal(t, expired, c.nodeClaimMisses.firstMiss("other-node-uid", time.Now()),
		"the other Node's record must survive this reconcile unrefreshed")
}

// Finding the NodeClaim clears the tracking, so a later miss for the same Node starts a fresh
// window rather than inheriting a spent one.
func TestReconcile_ForgetsTheMissOnceTheNodeClaimIsFound(t *testing.T) {
	p := &recordingProvider{}
	c, node := newTestController(t, p, nodeClaimFor(managedNode("")))
	c.nodeClaimMisses.firstMiss(node.UID, time.Now().Add(-time.Minute))

	_, err := c.Reconcile(context.Background(), node)

	require.NoError(t, err)
	require.Len(t, p.nodes, 1)
	// Nothing is remembered, so a later miss would start a fresh window rather than a spent one.
	now := time.Now()
	assert.Equal(t, now, c.nodeClaimMisses.firstMiss(node.UID, now))
}

// A Node without a provider ID cannot be matched to any NodeClaim either, and that is the same
// transient gap seen from the other side.
func TestReconcile_RequeuesWhenTheNodeHasNoProviderID(t *testing.T) {
	p := &recordingProvider{}
	c, node := newTestController(t, p, nodeClaimFor(managedNode("")))
	node.Spec.ProviderID = ""

	result, err := c.Reconcile(context.Background(), node)

	require.NoError(t, err)
	assert.Equal(t, nodeClaimNotFoundRequeue, result.RequeueAfter)
	assert.Empty(t, p.nodes)
}

// Recovery from the registration race depends on a NodeClaim event reaching the Node that claim
// belongs to. Register() wires that up with karpenter's NodeClaimEventHandler, which pairs them by
// provider ID; this pins the behaviour we are relying on, since our own Watches() line cannot be
// inspected without a manager. If this stops mapping, a racing observation is lost again.
func TestNodeClaimEventHandler_EnqueuesTheNodeItBelongsTo(t *testing.T) {
	node := managedNode("30890Mi")
	nodeClaim := nodeClaimFor(node)

	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(node, nodeClaim).
		WithIndex(&v1.Node{}, "spec.providerID", func(o client.Object) []string {
			return []string{o.(*v1.Node).Spec.ProviderID}
		}).
		Build()

	// The event that matters is exactly the transition the race produces: a NodeClaim that had no
	// providerID acquiring one. Old and new are deliberately different, so a handler that read
	// only the old object would not pass.
	unpaired := nodeClaimFor(node)
	unpaired.Status.ProviderID = ""

	q := &recordingQueue{}
	nodeutils.NodeClaimEventHandler(kubeClient).Update(context.Background(),
		event.TypedUpdateEvent[client.Object]{ObjectOld: unpaired, ObjectNew: nodeClaim}, q)

	require.Len(t, q.added, 1, "the NodeClaim's own Node must be enqueued once its providerID lands")
	assert.Equal(t, node.Name, q.added[0].Name)

	// While it is still unpaired there is nothing to enqueue, which is the state the race leaves
	// it in until that update arrives.
	q.added = nil
	nodeutils.NodeClaimEventHandler(kubeClient).Update(context.Background(),
		event.TypedUpdateEvent[client.Object]{ObjectOld: unpaired, ObjectNew: unpaired}, q)

	assert.Empty(t, q.added)
}

// Giving up is remembered only for a while. The record is a tombstone, not a permanent verdict:
// once it expires a Node arriving again is treated as a fresh miss and gets a fresh window, which
// is what stops one bad spell from disqualifying a Node for the life of the process.
func TestReconcile_TheGiveUpRecordExpires(t *testing.T) {
	p := &recordingProvider{}
	c, node := newTestController(t, p)
	c.nodeClaimMisses = &missTracker{entries: cache.New(40*time.Millisecond, 20*time.Millisecond)}
	c.nodeClaimMisses.firstMiss(node.UID, time.Now().Add(-nodeClaimLookupWindow-time.Minute))

	result, err := c.Reconcile(context.Background(), node)
	require.NoError(t, err)
	require.Zero(t, result.RequeueAfter, "the window has closed")

	time.Sleep(80 * time.Millisecond)

	result, err = c.Reconcile(context.Background(), node)

	require.NoError(t, err)
	assert.Equal(t, nodeClaimNotFoundRequeue, result.RequeueAfter,
		"once the record has expired this reads as a new miss, and is retried again")
}

// ...and the controller as constructed really does give its records an expiry. The test above
// substitutes a fast cache to observe the behaviour, so without this nothing would notice the
// real one being built to keep entries forever.
func TestNewController_GiveUpRecordsAreBuiltToExpire(t *testing.T) {
	p := &recordingProvider{}
	c, node := newTestController(t, p)

	c.nodeClaimMisses.firstMiss(node.UID, time.Now())

	item, found := c.nodeClaimMisses.entries.Items()[string(node.UID)]
	require.True(t, found)
	assert.NotZero(t, item.Expiration, "a record kept forever would disqualify a Node permanently")
}

// recordingQueue captures what a handler enqueues. Only Add is exercised.
type recordingQueue struct {
	workqueue.TypedRateLimitingInterface[reconcile.Request]
	added []reconcile.Request
}

func (q *recordingQueue) Add(item reconcile.Request) { q.added = append(q.added, item) }
