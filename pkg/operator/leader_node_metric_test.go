/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package operator

import (
	"context"
	"testing"
	"time"

	"github.com/oracle/karpenter-provider-oci/pkg/fakes"
	ocimetrics "github.com/oracle/karpenter-provider-oci/pkg/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLeaderNodeMetric(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runnable := &leaderNodeMetric{nodeName: "leader-worker-node"}
	done := make(chan error, 1)

	go func() {
		done <- runnable.Start(ctx)
	}()

	require.Eventually(t, func() bool {
		metric, found := fakes.FindMetricWithLabelValues(t,
			"leader_node_info",
			map[string]string{
				ocimetrics.NodeNameLabel: "leader-worker-node",
			})
		return found && metric.GetGauge().GetValue() == 1
	}, time.Second, 10*time.Millisecond)
	assert.True(t, runnable.NeedLeaderElection())

	cancel()
	require.NoError(t, <-done)

	_, found := fakes.FindMetricWithLabelValues(t,
		"leader_node_info",
		map[string]string{
			ocimetrics.NodeNameLabel: "leader-worker-node",
		})
	assert.False(t, found)
}
