/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package e2e

import (
	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
)

type NodePoolConfig struct {
	// Name of NodePool
	Name string
	// kubernetes.io/arch
	Architecture []string
	// kubernetes.io/os
	Os []string
	// karpenter.sh/capacity-type
	CapacityType []string
	// node.kubernetes.io/instance-type
	InstanceTypes []string
}

type OCINodeClassConfig struct {
	// Name of OCINodeClass
	Name string
	// ImageId is the K8S 1.32 image ocid
	ImageId string
	// BootVolumeSizeInGB is the size of the boot volume in GB.
	ImageOsFilter        string
	ImageOsVersionFilter string
	BootVolumeSizeInGB   int64
	// PVEncryptionInTransit controls "isPvEncryptionInTransitEnabled" flag
	PVEncryptionInTransit bool
	// NodeCompartmentID is optional to place instance in a different compartment from the cluster
	NodeCompartmentID string
	// Subnet is node subnet Ocid
	SubnetID                       string
	SubnetName                     string
	PrimaryVnicDisplayName         *string
	PrimaryVnicSkipSourceDestCheck *bool
	// KmsKey is the OCID of KMS key to be used for encryption
	KmsKey                  string
	UbuntuImagId            string
	CustomImagId            string
	Nsgs                    []string
	NsgName                 string
	SecondVnicConfigs       []SecondVnicConfig
	KubeletConfig           KubeletConfig
	CapacityReservationName string
	CapacityReservationId   string
	ComputeClusterName      string
	ComputeClusterId        string
	ShapeConfigs            []ociv1beta1.ShapeConfig
	ShapeConfigOcpu         float32
	ShapeConfigMem          float32
	SshPubKey               string
	ImageDisplayName        string
	KmsKeyName              string
	Metadata                map[string]string
	FreeformTags            map[string]string
	DefinedTags             map[string]map[string]string
}

type TestDeploymentConfig struct {
	// test deployment name
	Name string
	// test deployment image
	Image string
	// CPU req of test app
	CPUReq string
	// Replicas of test app
	Replicas int32
}

type DriftTestData struct {
	// Drift image ocid
	DriftImageId string
	// Drift compartment ocid
	DriftCompartment string
	// Drift KMS key ocid
	DriftKmsKey string
	// Drift subnet ocid
	DriftSubnetID string
	// Drift Network Security Group ocid
	DriftNsg []string
	// Drift Boot Volume Size in GB
	DriftBootVolumeSizeInGB int64
	// PVEncryptionInTransit controls "isPvEncryptionInTransitEnabled" flag for drift tests
	DriftPVEncryptionInTransit bool
	// ShapeConfig Drift
	DriftShapeConfigs            []ociv1beta1.ShapeConfig
	DriftCapacityReservationId   string
	DriftCapacityReservationName string
	DriftImageDisplayName        string
	DriftKmsKeyName              string
}

type NodeOverlayTestConfig struct {
	Name            string
	CandidateShapes []string
	PreferredShape  string
	PriceAdjustment string
}

type StaticCapacityTestConfig struct {
	NodePoolName    string
	InitialReplicas int64
	ScaledReplicas  int64
	InstanceTypes   []string
}

type KarpenterE2ETestConfig struct {
	// Oci Profile used to run the test
	OciAuthMethodForTest string
	// Oci Profile used to run the test
	OciProfile string
	// Namespace for karpenter and related resource deployment
	Namespace string
	// CompartmentID for network resources
	CompartmentID      string
	NodePool           NodePoolConfig
	OCINodeClass       OCINodeClassConfig
	TestDeployment     TestDeploymentConfig
	NodeOverlayTest    NodeOverlayTestConfig
	StaticCapacityTest StaticCapacityTestConfig
	DriftTestData      DriftTestData
	OciVcnIpNative     bool
}

type SecondVnicConfig struct {
	VnicDisplayname     string
	SubnetId            string
	SubnetName          string
	AssignIpV6Ip        bool
	AssignPublicIp      bool
	SkipSourceDestCheck bool
	IpCount             int
	NicIndex            int
	Nsgs                []string
	NsgName             string
}

type KubeletConfig struct {
	MaxPods        int32             `json:"maxPods"`
	PodsPerCore    int32             `json:"podsPerCore"`
	SystemReserved map[string]string `json:"systemReserved"`
	KubeReserved   map[string]string `json:"kubeReserved"`
}

type NodeResponse struct {
	KubeletConfig KubeletConfig `json:"kubeletconfig"`
}
