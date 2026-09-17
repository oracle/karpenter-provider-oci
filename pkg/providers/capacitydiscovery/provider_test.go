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
	"time"

	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	"github.com/oracle/karpenter-provider-oci/pkg/metrics"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/image"
	"github.com/oracle/oci-go-sdk/v65/common"
	ocicore "github.com/oracle/oci-go-sdk/v65/core"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	corev1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// resolvedImage is resolveImage's image alone, for the many tests that assert on what resolved
// rather than on why it did not.
func (p *DefaultProvider) resolvedImage(ctx context.Context, shape string,
	nodeClass *ociv1beta1.OCINodeClass) string {
	imageID, _ := p.resolveImage(ctx, shape, nodeClass)
	return imageID
}

// The two halves are separate interfaces so that a consumer of one is not coupled to the other,
// and one provider serves both.
func TestDefaultProviderSatisfiesBothInterfaces(t *testing.T) {
	p := discoveryProvider()

	var advisor Advisor = p
	var recorder Recorder = p
	var both Provider = p

	assert.NotNil(t, advisor)
	assert.NotNil(t, both)
	assert.True(t, recorder.Enabled())
}

const (
	testInstanceTypeName = "VM.Standard.E5.Flex.8o.32g.1_1b"
	testShape            = "VM.Standard.E5.Flex"
	testImageID          = "ocid1.image.oc1..a"
)

// fakeImageProvider stands in for cached-only image resolution. It implements imageResolver and
// nothing else, because that is all discovery is given - reaching an API-capable call from here is
// a compile error, not something a test has to watch for. That advice costs no API calls in
// practice is covered against the real provider, in TestAdviseForShape_EndToEndWithRealImageResolution.
type fakeImageProvider struct {
	imageID string
	// notCached models the cache holding nothing for this configuration - the ordinary miss, and
	// not a failure. notCachedShapes does the same for particular shapes only.
	notCached       bool
	notCachedShapes map[string]bool
	// The four ways a result can come back successful but unusable.
	emptyResult   bool
	nilResult     bool
	nilImageID    bool
	nilImageEntry bool
	// gotShapes records what was asked for, so passing the wrong identifier - the instance type
	// name instead of the shape, say - cannot pass unnoticed.
	gotShapes []string
}

var _ imageResolver = (*fakeImageProvider)(nil)

func (f *fakeImageProvider) ResolveImageForShapeCached(_ context.Context, _ *ociv1beta1.ImageConfig,
	shape string) (*image.ImageResolveResult, bool) {
	f.gotShapes = append(f.gotShapes, shape)

	if f.notCached || f.notCachedShapes[shape] {
		return nil, false
	}
	switch {
	case f.nilResult:
		return nil, true
	case f.emptyResult:
		return &image.ImageResolveResult{}, true
	case f.nilImageEntry:
		return &image.ImageResolveResult{Images: []*ocicore.Image{nil}}, true
	case f.nilImageID:
		return &image.ImageResolveResult{Images: []*ocicore.Image{{}}}, true
	}

	return &image.ImageResolveResult{Images: []*ocicore.Image{{Id: lo.ToPtr(f.imageID)}}}, true
}

func discoveryNodeClass(imageIDs ...string) *ociv1beta1.OCINodeClass {
	return &ociv1beta1.OCINodeClass{
		Spec: ociv1beta1.OCINodeClassSpec{
			VolumeConfig: &ociv1beta1.VolumeConfig{
				BootVolumeConfig: &ociv1beta1.BootVolumeConfig{
					ImageConfig: &ociv1beta1.ImageConfig{},
				},
			},
		},
		Status: ociv1beta1.OCINodeClassStatus{
			Volume: &ociv1beta1.Volume{
				ImageCandidates: lo.Map(imageIDs, func(id string, _ int) *ociv1beta1.Image {
					return &ociv1beta1.Image{ImageId: id, DisplayName: id}
				}),
			},
		},
	}
}

func discoveryNode(instanceType, memory string) *v1.Node {
	n := &v1.Node{}
	if instanceType != "" {
		n.Labels = map[string]string{v1.LabelInstanceTypeStable: instanceType}
	}
	if memory != "" {
		n.Status.Capacity = v1.ResourceList{v1.ResourceMemory: resource.MustParse(memory)}
	}
	return n
}

func discoveryNodeClaim(imageID string) *corev1.NodeClaim {
	return &corev1.NodeClaim{Status: corev1.NodeClaimStatus{ImageID: imageID}}
}

func discoveryProvider() *DefaultProvider {
	return &DefaultProvider{
		measured:      newNodeCapacity(DefaultNodeCapacityTTL),
		imageProvider: &fakeImageProvider{imageID: testImageID},
	}
}

// A node reporting less memory than was modelled is the whole point: record it so the next launch
// of that combination is sized from measurement rather than the estimate.
func TestRecordNodeCapacity_RecordsObservedMemory(t *testing.T) {
	p := discoveryProvider()

	err := p.RecordNodeCapacity(context.Background(),
		discoveryNode(testInstanceTypeName, "30890Mi"), discoveryNodeClaim("ocid1.image.oc1..a"))
	assert.NoError(t, err)

	got, ok := p.measured.Get(cacheKey(testInstanceTypeName, testImageID))
	assert.True(t, ok, "expected the observation to be recorded")
	want := resource.MustParse("30890Mi")
	assert.Equal(t, want.Value(), got.Value())
}

// Nodes of nominally the same kind can report slightly different totals. Keeping the smallest
// keeps the model on the safe side: over-estimating drives the launch loop this exists to stop,
// while under-estimating only wastes memory.
func TestRecordNodeCapacity_KeepsSmallestObserved(t *testing.T) {
	p := discoveryProvider()
	ctx := context.Background()
	key := cacheKey(testInstanceTypeName, testImageID)

	for _, mem := range []string{"30890Mi", "30800Mi", "31000Mi"} {
		assert.NoError(t, p.RecordNodeCapacity(ctx,
			discoveryNode(testInstanceTypeName, mem), discoveryNodeClaim("ocid1.image.oc1..a")))
	}

	got, ok := p.measured.Get(key)
	assert.True(t, ok)
	want := resource.MustParse("30800Mi")
	assert.Equal(t, want.Value(), got.Value(),
		"a larger later observation must not raise the recorded capacity")
}

func TestRecordNodeCapacity_Skips(t *testing.T) {
	tests := []struct {
		name      string
		node      *v1.Node
		nodeClaim *corev1.NodeClaim
		reason    string
	}{
		{
			name:      "no instance-type label",
			node:      discoveryNode("", "30890Mi"),
			nodeClaim: discoveryNodeClaim("ocid1.image.oc1..a"),
			reason:    "there is nothing to key the measurement on",
		},
		{
			name:      "nodeclaim has no image id",
			node:      discoveryNode(testInstanceTypeName, "30890Mi"),
			nodeClaim: discoveryNodeClaim(""),
			reason:    "we cannot tell which image produced this measurement",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := discoveryProvider()

			assert.NoError(t, p.RecordNodeCapacity(
				context.Background(), tt.node, tt.nodeClaim))

			_, ok := p.measured.Get(cacheKey(testInstanceTypeName, testImageID))
			assert.False(t, ok, "must not record: %s", tt.reason)
		})
	}
}

func TestRecordNodeCapacity_NilInputs(t *testing.T) {
	p := discoveryProvider()
	ctx := context.Background()

	assert.NoError(t, p.RecordNodeCapacity(ctx, nil, discoveryNodeClaim("a")))
	assert.NoError(t, p.RecordNodeCapacity(ctx, discoveryNode(testInstanceTypeName, "1Gi"), nil))

	_, ok := p.measured.Get(cacheKey(testInstanceTypeName, "a"))
	assert.False(t, ok, "a nil node or nodeclaim must record nothing")
}

// The key names the image a node actually booted, so a measurement taken under one image is never
// served for another.
func TestCacheKey(t *testing.T) {
	const a, b = "ocid1.image.oc1..a", "ocid1.image.oc1..b"

	assert.Equal(t, cacheKey(testInstanceTypeName, a),
		cacheKey(testInstanceTypeName, a), "key must be stable")
	assert.NotEqual(t, cacheKey(testInstanceTypeName, a),
		cacheKey(testInstanceTypeName, b),
		"a different image must not reuse the entry")
	assert.NotEqual(t, cacheKey(testInstanceTypeName, a),
		cacheKey("VM.Standard.E5.Flex.4o.16g.1_1b", a),
		"a different instance type must not reuse the entry")
}

// Naming the resolved image rather than the NodeClass's whole candidate list is the point of the
// key: OKE publishes images regularly, and a key derived from the list would discard every learned
// value each time one appeared, including for shapes whose selection did not change.
func TestUnrelatedImageDoesNotInvalidate(t *testing.T) {
	p := discoveryProvider()
	nc := discoveryNodeClass(testImageID)
	ctx := context.Background()

	assert.NoError(t, p.RecordNodeCapacity(ctx,
		discoveryNode(testInstanceTypeName, "30890Mi"), discoveryNodeClaim(testImageID)))

	// A new image is published and joins the candidate list, but this shape still selects the same
	// one, so the measurement must still be found.
	nc.Status.Volume.ImageCandidates = append(nc.Status.Volume.ImageCandidates,
		&ociv1beta1.Image{ImageId: "ocid1.image.oc1..newly-published"})

	measured, ok := p.AdviseForShape(ctx, testShape, nc).MemoryFor(testInstanceTypeName)

	require.True(t, ok, "an unrelated image joining the candidate list must not discard what we learned")
	want := resource.MustParse("30890Mi")
	assert.Equal(t, want.Value(), measured.Value())
}

// A cold cache yields no key, so the modelled estimate - which is deliberately conservative -
// stands. Scheduling never waits on the image API to find that out.
func TestResolveImage_NotCachedYieldsNoKey(t *testing.T) {
	p := discoveryProvider()
	p.imageProvider = &fakeImageProvider{notCached: true}

	assert.Equal(t, "", p.resolvedImage(context.Background(), testShape, discoveryNodeClass(testImageID)))
}

// Turning the feature off must skip the lookup too, not just the application of its result.
func TestResolveImage_DisabledDoesNoWork(t *testing.T) {
	fake := &fakeImageProvider{imageID: testImageID}
	p := &DefaultProvider{
		measured:      newNodeCapacity(0), // 0 == disabled
		imageProvider: fake,
	}

	assert.Equal(t, "", p.resolvedImage(context.Background(), testShape, discoveryNodeClass(testImageID)))
	assert.Empty(t, fake.gotShapes, "a disabled cache must not consult image resolution at all")
}

// The resolver must be asked about the shape, not the synthetic instance type name: flexible
// instance types are named Shape.<X>o.<Y>g.<Z>b, which no image is compatible with.
func TestResolveImage_AsksForTheShape(t *testing.T) {
	fake := &fakeImageProvider{imageID: testImageID}
	p := &DefaultProvider{
		measured:      newNodeCapacity(DefaultNodeCapacityTTL),
		imageProvider: fake,
	}

	p.resolvedImage(context.Background(), testShape, discoveryNodeClass(testImageID))

	assert.Equal(t, []string{testShape}, fake.gotShapes)
}

// Advice is offered only once something has been measured, so that a caller with no advice keeps
// whatever it modelled for itself.
func TestAdviseForShape(t *testing.T) {
	ctx := context.Background()

	t.Run("offers the measured value", func(t *testing.T) {
		p := discoveryProvider()
		p.measured.Record(ctx, cacheKey(testInstanceTypeName, testImageID), resource.MustParse("30890Mi"))

		measured, ok := p.AdviseForShape(ctx, testShape, discoveryNodeClass(testImageID)).
			MemoryFor(testInstanceTypeName)

		require.True(t, ok)
		want := resource.MustParse("30890Mi")
		assert.Equal(t, want.Value(), measured.Value())
	})

	t.Run("declines before anything is measured", func(t *testing.T) {
		p := discoveryProvider()

		_, ok := p.AdviseForShape(ctx, testShape, discoveryNodeClass(testImageID)).
			MemoryFor(testInstanceTypeName)

		assert.False(t, ok, "the first launch of a combination has nothing to learn from")
	})
}

// A disabled cache must leave modelling exactly as it was, so the feature can be turned off.
func TestDisabled(t *testing.T) {
	p := &DefaultProvider{measured: newNodeCapacity(0)}

	assert.NoError(t, p.RecordNodeCapacity(context.Background(),
		discoveryNode(testInstanceTypeName, "30890Mi"), discoveryNodeClaim("ocid1.image.oc1..a")))

	_, ok := p.AdviseForShape(context.Background(), testShape, discoveryNodeClass(testImageID)).
		MemoryFor(testInstanceTypeName)

	assert.False(t, ok, "a disabled provider must have nothing to offer")
}

// A registered node that has not published memory yet must be retried, not dropped: the controller
// only watches the transition into the registered state, so nothing else would bring it back.
func TestRecordNodeCapacity_RetriesWhenMemoryNotReported(t *testing.T) {
	p := discoveryProvider()

	err := p.RecordNodeCapacity(context.Background(),
		discoveryNode(testInstanceTypeName, ""), discoveryNodeClaim("ocid1.image.oc1..a"))

	assert.ErrorIs(t, err, ErrCapacityNotReported)
	_, ok := p.measured.Get(cacheKey(testInstanceTypeName, testImageID))
	assert.False(t, ok, "nothing should be recorded from a node that reported no memory")
}

// With the image in the key, a node that booted an image the NodeClass has since stopped selecting
// files its measurement under that old image. Nothing looks there, so it neither leaks into the
// current image's estimate nor needs a staleness guard to suppress it.
func TestRecordNodeCapacity_OldImageDoesNotLeak(t *testing.T) {
	p := discoveryProvider()
	ctx := context.Background()

	// A node launched earlier, from an image this NodeClass no longer selects.
	assert.NoError(t, p.RecordNodeCapacity(ctx,
		discoveryNode(testInstanceTypeName, "20000Mi"),
		discoveryNodeClaim("ocid1.image.oc1..superseded")))

	// The shape now resolves to testImageID, so the superseded measurement must not be served.
	_, ok := p.AdviseForShape(ctx, testShape, discoveryNodeClass(testImageID)).
		MemoryFor(testInstanceTypeName)

	assert.False(t, ok, "a measurement from a superseded image must not be served for the current one")

	// It is still filed under its own image, which is what makes the guard unnecessary.
	_, filed := p.measured.Get(cacheKey(testInstanceTypeName, "ocid1.image.oc1..superseded"))
	assert.True(t, filed)
}

// Enabled is what decides whether the controller is registered at all, so tie it to a
// provider built the way production builds one: from a TTL.
func TestEnabled(t *testing.T) {
	tests := []struct {
		name          string
		ttl           time.Duration
		imageProvider imageResolver
		want          bool
	}{
		{"default TTL", DefaultNodeCapacityTTL, &fakeImageProvider{imageID: testImageID}, true},
		{"zero TTL disables", 0, &fakeImageProvider{imageID: testImageID}, false},
		{"no image provider", DefaultNodeCapacityTTL, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &DefaultProvider{
				measured:      newNodeCapacity(tt.ttl),
				imageProvider: tt.imageProvider,
			}

			assert.Equal(t, tt.want, p.Enabled())
		})
	}
}

// Advising and recording are two narrow interfaces rather than one concrete provider, so a
// consumer of either is not coupled to the other. These fail at compile time if one value stops
// serving both, which is what lets the operator construct it once and share it.
var (
	_ Advisor  = (*DefaultProvider)(nil)
	_ Recorder = (*DefaultProvider)(nil)
	_ Provider = (*DefaultProvider)(nil)
	_ Advice   = shapeAdvice{}
	_ Advice   = noAdvice{}
)

// And the instance type provider must ask once per shape, not once per generated instance type: a
// flexible shape expands into many configurations that all share an image.
func TestAdviseForShape_AdviceIsReusableAcrossInstanceTypes(t *testing.T) {
	p := discoveryProvider()
	ctx := context.Background()
	fake := p.imageProvider.(*fakeImageProvider)

	p.measured.Record(ctx, cacheKey("VM.Standard.E5.Flex.8o.32g.1_1b", testImageID),
		resource.MustParse("30890Mi"))
	p.measured.Record(ctx, cacheKey("VM.Standard.E5.Flex.4o.16g.1_1b", testImageID),
		resource.MustParse("15000Mi"))

	advice := p.AdviseForShape(ctx, testShape, discoveryNodeClass(testImageID))

	big, ok := advice.MemoryFor("VM.Standard.E5.Flex.8o.32g.1_1b")
	require.True(t, ok)
	small, ok := advice.MemoryFor("VM.Standard.E5.Flex.4o.16g.1_1b")
	require.True(t, ok)

	assert.NotEqual(t, big.Value(), small.Value(), "each configuration keeps its own measurement")
	assert.Len(t, fake.gotShapes, 1, "one resolution serves every configuration of the shape")
}

// Every reason for having no advice reports a distinct outcome, so an operator can tell "nothing
// has launched this recently" from "the image provider is misbehaving" - and so that only the
// latter reads as a problem.
func TestResolveImage_ReportsDistinctOutcomes(t *testing.T) {
	const shape = "VM.Standard.E5.Flex"
	nodeClass := discoveryNodeClass(testImageID)

	tests := []struct {
		name      string
		provider  *DefaultProvider
		nodeClass *ociv1beta1.OCINodeClass
		want      string
	}{
		{
			name:      "discovery switched off",
			provider:  New(&fakeImageProvider{imageID: testImageID}, 0),
			nodeClass: nodeClass,
			want:      metrics.OutcomeDisabled,
		},
		{
			name:      "nothing to resolve from",
			provider:  New(&fakeImageProvider{imageID: testImageID}, DefaultNodeCapacityTTL),
			nodeClass: &ociv1beta1.OCINodeClass{},
			want:      metrics.OutcomeNoImage,
		},
		{
			name:      "nothing cached for this configuration",
			provider:  New(&fakeImageProvider{notCached: true}, DefaultNodeCapacityTTL),
			nodeClass: nodeClass,
			want:      metrics.OutcomeNotCached,
		},
		{
			name:      "cached, but the result is unusable",
			provider:  New(&fakeImageProvider{emptyResult: true}, DefaultNodeCapacityTTL),
			nodeClass: nodeClass,
			want:      metrics.OutcomeResolutionFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imageID, outcome := tt.provider.resolveImage(context.Background(), shape, tt.nodeClass)

			assert.Equal(t, "", imageID)
			assert.Equal(t, tt.want, outcome)
		})
	}
}

// And a success reports nothing, because the outcome is then whether a measurement exists, which
// only the lookup can say.
func TestResolveImage_SuccessHasNoOutcome(t *testing.T) {
	p := discoveryProvider()

	imageID, outcome := p.resolveImage(context.Background(), testShape, discoveryNodeClass(testImageID))

	assert.Equal(t, testImageID, imageID)
	assert.Equal(t, "", outcome)
}

// A success that carries nothing an instance type could be keyed on is the image provider breaking
// its contract. Each shape of that is a failure with an error to report, not a silent miss and not
// a panic.
func TestResolveImage_UnusableResultsAreContractViolations(t *testing.T) {
	tests := map[string]*fakeImageProvider{
		"nil result":      {nilResult: true},
		"no images":       {emptyResult: true},
		"nil image entry": {nilImageEntry: true},
		"nil image id":    {nilImageID: true},
		"empty image id":  {imageID: ""},
	}

	for name, fake := range tests {
		t.Run(name, func(t *testing.T) {
			p := &DefaultProvider{
				measured:      newNodeCapacity(DefaultNodeCapacityTTL),
				imageProvider: fake,
			}

			imageID, outcome := p.resolveImage(context.Background(), testShape,
				discoveryNodeClass(testImageID))

			assert.Equal(t, "", imageID)
			assert.Equal(t, metrics.OutcomeResolutionFailed, outcome,
				"an unusable result is a failure of the image provider, not a settled answer")
		})
	}
}

// adviceCount reads the counter for one outcome, so tests can assert what was actually emitted
// rather than what the code said it would emit.
func adviceCount(t *testing.T, outcome string) float64 {
	t.Helper()

	gathered, err := crmetrics.Registry.Gather()
	require.NoError(t, err)

	for _, family := range gathered {
		if family.GetName() != "karpenter_cloudprovider_capacity_discovery_advice_count" {
			continue
		}
		for _, m := range family.GetMetric() {
			for _, label := range m.GetLabel() {
				if label.GetName() == metrics.OutcomeLabel && label.GetValue() == outcome {
					return m.GetCounter().GetValue()
				}
			}
		}
	}

	return 0
}

// Every instance type modelled contributes exactly one sample, whatever the outcome. Counting
// resolution outcomes per shape and lookups per instance type would make the denominator depend on
// how far a flexible shape expands, so the ratios would not mean anything.
func TestAdviseForShape_CountsOneSamplePerInstanceType(t *testing.T) {
	ctx := context.Background()

	t.Run("applied and no_measurement", func(t *testing.T) {
		p := discoveryProvider()
		p.measured.Record(ctx, cacheKey(testInstanceTypeName, testImageID), resource.MustParse("30890Mi"))

		appliedBefore := adviceCount(t, metrics.OutcomeApplied)
		missBefore := adviceCount(t, metrics.OutcomeNoMeasurement)

		advice := p.AdviseForShape(ctx, testShape, discoveryNodeClass(testImageID))
		advice.MemoryFor(testInstanceTypeName)
		advice.MemoryFor("VM.Standard.E5.Flex.4o.16g.1_1b")

		assert.Equal(t, appliedBefore+1, adviceCount(t, metrics.OutcomeApplied))
		assert.Equal(t, missBefore+1, adviceCount(t, metrics.OutcomeNoMeasurement))
	})

	t.Run("a cache miss counts per instance type too", func(t *testing.T) {
		p := New(&fakeImageProvider{notCached: true}, DefaultNodeCapacityTTL)

		before := adviceCount(t, metrics.OutcomeNotCached)

		advice := p.AdviseForShape(ctx, testShape, discoveryNodeClass(testImageID))
		advice.MemoryFor(testInstanceTypeName)
		advice.MemoryFor("VM.Standard.E5.Flex.4o.16g.1_1b")

		assert.Equal(t, before+2, adviceCount(t, metrics.OutcomeNotCached),
			"two instance types modelled, two samples - not one per shape")
	})

	t.Run("no advisor configured reports disabled", func(t *testing.T) {
		before := adviceCount(t, metrics.OutcomeDisabled)

		NoAdvice().MemoryFor(testInstanceTypeName)

		assert.Equal(t, before+1, adviceCount(t, metrics.OutcomeDisabled))
	})
}

// The failure branch logs the error it was given, so a successful call carrying nothing usable has
// to be given one: logging a nil error would report a failure with nothing to diagnose it by.
// usableResult is what decides that, so pin every field it looks at.
func TestUsableResult(t *testing.T) {
	tests := map[string]struct {
		resolved *image.ImageResolveResult
		want     bool
	}{
		"nil result":      {nil, false},
		"no images":       {&image.ImageResolveResult{}, false},
		"nil image entry": {&image.ImageResolveResult{Images: []*ocicore.Image{nil}}, false},
		"nil image id":    {&image.ImageResolveResult{Images: []*ocicore.Image{{}}}, false},
		"empty image id":  {&image.ImageResolveResult{Images: []*ocicore.Image{{Id: lo.ToPtr("")}}}, false},
		"usable":          {&image.ImageResolveResult{Images: []*ocicore.Image{{Id: lo.ToPtr(testImageID)}}}, true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, usableResult(tt.resolved))
		})
	}
}

// A cold cache costs nothing and blocks nothing: no advice, straight away.
func TestAdviseForShape_ColdCacheDeclinesImmediately(t *testing.T) {
	fake := &fakeImageProvider{notCached: true}
	p := New(fake, DefaultNodeCapacityTTL)
	ctx := context.Background()

	// A measurement exists, but without the image it cannot be keyed - so it cannot be served.
	p.measured.Record(ctx, cacheKey(testInstanceTypeName, testImageID), resource.MustParse("30890Mi"))

	_, ok := p.AdviseForShape(ctx, testShape, discoveryNodeClass(testImageID)).
		MemoryFor(testInstanceTypeName)

	assert.False(t, ok, "no key can be built, so the estimate stands")
}

// ...and the measurement is not lost by that, only temporarily unreadable. Once something warms
// the cache again - the next launch of this combination does - it is served as before. This is
// what bounds the cost of the cached-only design to one estimate-modelled launch per idle period
// rather than to losing what was learned.
func TestAdviseForShape_MeasurementSurvivesAColdCache(t *testing.T) {
	fake := &fakeImageProvider{notCached: true}
	p := New(fake, DefaultNodeCapacityTTL)
	ctx := context.Background()

	p.measured.Record(ctx, cacheKey(testInstanceTypeName, testImageID), resource.MustParse("30890Mi"))

	_, ok := p.AdviseForShape(ctx, testShape, discoveryNodeClass(testImageID)).
		MemoryFor(testInstanceTypeName)
	require.False(t, ok)

	// A launch resolves the image, which warms the cache this reads.
	fake.notCached = false
	fake.imageID = testImageID

	measured, ok := p.AdviseForShape(ctx, testShape, discoveryNodeClass(testImageID)).
		MemoryFor(testInstanceTypeName)

	require.True(t, ok, "the measurement was never lost, only unkeyable")
	want := resource.MustParse("30890Mi")
	assert.Equal(t, want.Value(), measured.Value())
}

// Shapes are independent: one shape being uncached says nothing about another, and there is no
// shared state left that could make it do so.
func TestAdviseForShape_OneColdShapeDoesNotAffectAnother(t *testing.T) {
	const cold, warm = "BM.GPU.H100.8", "VM.Standard.E5.Flex"

	fake := &fakeImageProvider{imageID: testImageID, notCachedShapes: map[string]bool{cold: true}}
	p := New(fake, DefaultNodeCapacityTTL)
	ctx := context.Background()
	nodeClass := discoveryNodeClass(testImageID)

	p.measured.Record(ctx, cacheKey(testInstanceTypeName, testImageID), resource.MustParse("30890Mi"))

	_, ok := p.AdviseForShape(ctx, cold, nodeClass).MemoryFor(testInstanceTypeName)
	assert.False(t, ok)

	_, ok = p.AdviseForShape(ctx, warm, nodeClass).MemoryFor(testInstanceTypeName)
	assert.True(t, ok, "a cold shape must not withhold advice for a warm one")
}

// A NodeClass with nothing to resolve from is a different outcome than one whose image simply is
// not cached, and the metric has to keep them apart to stay readable.
func TestResolveImage_NoImageConfigIsNotACacheMiss(t *testing.T) {
	fake := &fakeImageProvider{imageID: testImageID}
	p := New(fake, DefaultNodeCapacityTTL)

	nodeClass := discoveryNodeClass(testImageID)
	nodeClass.Spec.VolumeConfig.BootVolumeConfig.ImageConfig = nil

	imageID, outcome := p.resolveImage(context.Background(), testShape, nodeClass)

	assert.Equal(t, "", imageID)
	assert.Equal(t, metrics.OutcomeNoImage, outcome,
		"there is no image configuration here, so nothing was looked up at all")
	assert.Empty(t, fake.gotShapes, "and resolution should not have been consulted")
}

// End to end against the real image provider rather than a stand-in: a launch resolves the image
// and warms the caches, a node of that kind reports its memory, and the next listing must key off
// the same image the launch used and hand back what was measured - without touching OCI again.
// Every other test here fakes one half or the other; this one joins them.
func TestAdviseForShape_EndToEndWithRealImageResolution(t *testing.T) {
	ctx := context.Background()
	fakeClient := &fakes.FakeCompute{}
	startCh := make(chan struct{})
	close(startCh)
	imageProvider, err := image.NewProvider(ctx, nil, fakeClient, "prebaked-comp", "cio-comp", startCh)
	require.NoError(t, err)

	const imageID = "ocid1.image.oc1..real"
	fakeClient.GetImageResp = ocicore.GetImageResponse{
		Image: ocicore.Image{
			Id: lo.ToPtr(imageID), DisplayName: lo.ToPtr("real-image"),
			TimeCreated: &common.SDKTime{Time: time.Now()}, OperatingSystem: lo.ToPtr("Oracle Linux"),
		},
	}
	fakeClient.OnListImageShapeCompatibilityEntries = func(context.Context,
		ocicore.ListImageShapeCompatibilityEntriesRequest) (ocicore.ListImageShapeCompatibilityEntriesResponse,
		error) {
		return ocicore.ListImageShapeCompatibilityEntriesResponse{
			Items: []ocicore.ImageShapeCompatibilitySummary{{Shape: lo.ToPtr(testShape)}},
		}, nil
	}

	p := New(imageProvider, DefaultNodeCapacityTTL)
	nodeClass := discoveryNodeClass(imageID)
	nodeClass.Spec.VolumeConfig.BootVolumeConfig.ImageConfig = &ociv1beta1.ImageConfig{
		ImageId: lo.ToPtr(imageID),
	}

	// Record the measurement up front, so that when advice is withheld below the only possible
	// reason is the cold image cache - not an empty measurement cache.
	require.NoError(t, p.RecordNodeCapacity(ctx,
		discoveryNode(testInstanceTypeName, "30890Mi"), discoveryNodeClaim(imageID)))

	// Nothing has resolved this configuration yet, so the measurement cannot be keyed...
	_, outcome := p.resolveImage(ctx, testShape, nodeClass)
	require.Equal(t, metrics.OutcomeNotCached, outcome)

	_, ok := p.AdviseForShape(ctx, testShape, nodeClass).MemoryFor(testInstanceTypeName)
	require.False(t, ok, "a recorded measurement is unreachable while the image is unknown")
	require.Equal(t, 0, fakeClient.GetImageCount.Get(),
		"and the advice path must not have issued the lookup itself")
	require.Equal(t, 0, fakeClient.ListImageShapeCompatibilityEntriesCount.Get())

	// ...until a launch resolves the image for this shape, which is what warms the caches.
	launched, err := imageProvider.ResolveImageForShape(ctx,
		nodeClass.Spec.VolumeConfig.BootVolumeConfig.ImageConfig, testShape)
	require.NoError(t, err)
	require.Equal(t, imageID, *launched.Images[0].Id)

	callsAfterLaunch := fakeClient.GetImageCount.Get()
	shapeCallsAfterLaunch := fakeClient.ListImageShapeCompatibilityEntriesCount.Get()

	measured, ok := p.AdviseForShape(ctx, testShape, nodeClass).MemoryFor(testInstanceTypeName)

	require.True(t, ok, "the measurement recorded against the launched image must now be found")
	want := resource.MustParse("30890Mi")
	assert.Equal(t, want.Value(), measured.Value())
	assert.Equal(t, callsAfterLaunch, fakeClient.GetImageCount.Get(),
		"and reading it must cost no further API calls")
	assert.Equal(t, shapeCallsAfterLaunch, fakeClient.ListImageShapeCompatibilityEntriesCount.Get())
}
