/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package nodeclasses

import (
	"context"

	"github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type Hash struct {
}

func (h *Hash) Reconcile(ctx context.Context, nodeClass *v1beta1.OCINodeClass) (reconcile.Result, error) {
	if nodeClass.StatusConditions().Root().IsTrue() {
		// TODO may need to update nodeClaim hash here if NodeClassHashVersion is changed
		hash := utils.HashNodeClassSpec(nodeClass)

		if nodeClass.Annotations == nil {
			nodeClass.Annotations = make(map[string]string)
		}

		nodeClass.Annotations[v1beta1.NodeClassHash] = hash
		nodeClass.Annotations[v1beta1.NodeClassHashVersion] = v1beta1.OCINodeClassHashVersion
	}

	return reconcile.Result{}, nil
}
