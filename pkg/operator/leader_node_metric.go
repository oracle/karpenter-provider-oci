/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package operator

import (
	"context"

	ocimetrics "github.com/oracle/karpenter-provider-oci/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const nodeNameEnvVar = "KPO_NODE_NAME"

var _ manager.LeaderElectionRunnable = (*leaderNodeMetric)(nil)

type leaderNodeMetric struct {
	nodeName string
}

func (m *leaderNodeMetric) Start(ctx context.Context) error {
	if m.nodeName == "" {
		<-ctx.Done()
		return nil
	}

	ocimetrics.RecordLeaderNode(m.nodeName)
	defer ocimetrics.DeleteLeaderNode(m.nodeName)

	<-ctx.Done()
	return nil
}

func (m *leaderNodeMetric) NeedLeaderElection() bool {
	return true
}
