/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

// Package apis contains Kubernetes API groups.
package apis

import (
	_ "embed"

	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/yaml"

	"github.com/samber/lo"

	"sigs.k8s.io/karpenter/pkg/apis"
)

//go:generate controller-gen object crd:allowDangerousTypes=true paths="./..." output:crd:artifacts:config=crds
var (
	//go:embed crds/oci.oraclecloud.com_ocinodeclasses.yaml
	OCINodeClassCRD []byte
	CRDs            = append(apis.CRDs, lo.Must(Unmarshal[v1.CustomResourceDefinition](OCINodeClassCRD)))
)

func Unmarshal[T any](raw []byte) (*T, error) {
	t := *new(T)
	if err := yaml.Unmarshal(raw, &t); err != nil {
		return nil, err
	}
	return &t, nil
}
