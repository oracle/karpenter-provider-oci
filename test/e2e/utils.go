/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package e2e

import (
	"strings"
	"time"

	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/providers/instancetype"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/samber/lo"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpenterv1alpha1 "sigs.k8s.io/karpenter/pkg/apis/v1alpha1"
)

func karpenterNodePool(name string, nodeClassName string, config *KarpenterE2ETestConfig) *karpenterv1.NodePool {
	second := time.Second
	return &karpenterv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: config.Namespace,
		},
		Spec: karpenterv1.NodePoolSpec{
			Template: karpenterv1.NodeClaimTemplate{
				Spec: karpenterv1.NodeClaimTemplateSpec{
					Requirements: []karpenterv1.NodeSelectorRequirementWithMinValues{
						{
							Key:       corev1.LabelArchStable,
							Operator:  corev1.NodeSelectorOpIn,
							Values:    config.NodePool.Architecture,
							MinValues: nil,
						},
						{
							Key:       corev1.LabelOSStable,
							Operator:  corev1.NodeSelectorOpIn,
							Values:    config.NodePool.Os,
							MinValues: nil,
						},
						{
							Key:       karpenterv1.CapacityTypeLabelKey,
							Operator:  corev1.NodeSelectorOpIn,
							Values:    config.NodePool.CapacityType,
							MinValues: nil,
						},
						{
							Key:       corev1.LabelInstanceTypeStable,
							Operator:  corev1.NodeSelectorOpIn,
							Values:    config.NodePool.InstanceTypes,
							MinValues: nil,
						},
					},
					Taints: []corev1.Taint{{
						Key:    instancetype.PreemptibleTaintKey,
						Effect: corev1.TaintEffectNoSchedule,
						Value:  "present",
					},
						{
							Key:    instancetype.NvidiaGpuTaintKey,
							Effect: corev1.TaintEffectNoSchedule,
							Value:  "present",
						},
						{
							Key:    instancetype.AmdGpuTaintKey,
							Effect: corev1.TaintEffectNoSchedule,
							Value:  "present",
						},
					},
					NodeClassRef: &karpenterv1.NodeClassReference{
						Kind:  "OCINodeClass",
						Name:  nodeClassName,
						Group: "oci.oraclecloud.com",
					},
				},
			},
			Disruption: karpenterv1.Disruption{
				ConsolidateAfter: karpenterv1.NillableDuration{
					Duration: &second,
				},
				ConsolidationPolicy: karpenterv1.ConsolidationPolicyWhenEmptyOrUnderutilized,
			},
		},
	}
}

func nodeOverlay(config *KarpenterE2ETestConfig) *karpenterv1alpha1.NodeOverlay {
	return &karpenterv1alpha1.NodeOverlay{
		ObjectMeta: metav1.ObjectMeta{
			Name: config.NodeOverlayTest.Name,
		},
		Spec: karpenterv1alpha1.NodeOverlaySpec{
			Requirements: []karpenterv1alpha1.NodeSelectorRequirement{
				{
					Key:      NodePoolLabel,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{config.NodePool.Name},
				},
				{
					Key:      ociv1beta1.OciInstanceShape,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{config.NodeOverlayTest.PreferredShape},
				},
			},
			PriceAdjustment: lo.ToPtr(config.NodeOverlayTest.PriceAdjustment),
			Weight:          lo.ToPtr(int32(100)),
		},
	}
}

func staticKarpenterNodePool(config *KarpenterE2ETestConfig) *karpenterv1.NodePool {
	staticInstanceTypes := config.StaticCapacityTest.InstanceTypes
	if len(staticInstanceTypes) == 0 {
		staticInstanceTypes = config.NodePool.InstanceTypes
	}

	return &karpenterv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: config.StaticCapacityTest.NodePoolName,
		},
		Spec: karpenterv1.NodePoolSpec{
			Replicas: lo.ToPtr(config.StaticCapacityTest.InitialReplicas),
			Template: karpenterv1.NodeClaimTemplate{
				Spec: karpenterv1.NodeClaimTemplateSpec{
					Requirements: []karpenterv1.NodeSelectorRequirementWithMinValues{
						{
							Key:      corev1.LabelArchStable,
							Operator: corev1.NodeSelectorOpIn,
							Values:   config.NodePool.Architecture,
						},
						{
							Key:      corev1.LabelOSStable,
							Operator: corev1.NodeSelectorOpIn,
							Values:   config.NodePool.Os,
						},
						{
							Key:      karpenterv1.CapacityTypeLabelKey,
							Operator: corev1.NodeSelectorOpIn,
							Values:   config.NodePool.CapacityType,
						},
						{
							Key:      corev1.LabelInstanceTypeStable,
							Operator: corev1.NodeSelectorOpIn,
							Values:   staticInstanceTypes,
						},
					},
					Taints: []corev1.Taint{
						{
							Key:    instancetype.PreemptibleTaintKey,
							Effect: corev1.TaintEffectNoSchedule,
							Value:  "present",
						},
						{
							Key:    instancetype.NvidiaGpuTaintKey,
							Effect: corev1.TaintEffectNoSchedule,
							Value:  "present",
						},
						{
							Key:    instancetype.AmdGpuTaintKey,
							Effect: corev1.TaintEffectNoSchedule,
							Value:  "present",
						},
					},
					NodeClassRef: &karpenterv1.NodeClassReference{
						Kind:  "OCINodeClass",
						Name:  config.OCINodeClass.Name,
						Group: "oci.oraclecloud.com",
					},
				},
			},
			Disruption: karpenterv1.Disruption{
				ConsolidationPolicy: karpenterv1.ConsolidationPolicyWhenEmptyOrUnderutilized,
			},
		},
	}
}

func oCINodeClass(name string, config *KarpenterE2ETestConfig) *ociv1beta1.OCINodeClass {
	nsgSlector := ociv1beta1.OciResourceSelectorTerm{
		CompartmentId: &config.CompartmentID,
		DisplayName:   &config.OCINodeClass.NsgName,
	}

	secondVnicConfigs := make([]*ociv1beta1.SecondaryVnicConfig, 0)
	// Only add secondary vnic to nodeclass if it is a npn cluster and secondVnicConfigs are set
	if config.OciVcnIpNative && len(config.OCINodeClass.SecondVnicConfigs) > 0 {
		for _, value := range config.OCINodeClass.SecondVnicConfigs {
			secondVnicConfigs = append(secondVnicConfigs, &ociv1beta1.SecondaryVnicConfig{
				SimpleVnicConfig: ociv1beta1.SimpleVnicConfig{
					SubnetAndNsgConfig: &ociv1beta1.SubnetAndNsgConfig{
						SubnetConfig: &ociv1beta1.SubnetConfig{
							SubnetFilter: &ociv1beta1.OciResourceSelectorTerm{
								CompartmentId: &config.CompartmentID,
								DisplayName:   &value.SubnetName,
							},
						},
						NetworkSecurityGroupConfigs: []*ociv1beta1.NetworkSecurityGroupConfig{
							{
								NetworkSecurityGroupFilter: &nsgSlector,
							},
						},
					},
					VnicDisplayName:     &value.VnicDisplayname,
					AssignIpV6Ip:        &value.AssignIpV6Ip,
					AssignPublicIp:      &value.AssignPublicIp,
					SkipSourceDestCheck: &value.SkipSourceDestCheck,
				},
				IpCount:  &value.IpCount,
				NicIndex: &value.NicIndex,
			})
		}

	}

	return &ociv1beta1.OCINodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: config.Namespace,
		},
		Spec: ociv1beta1.OCINodeClassSpec{
			VolumeConfig: &ociv1beta1.VolumeConfig{
				BootVolumeConfig: &ociv1beta1.BootVolumeConfig{
					ImageConfig: &ociv1beta1.ImageConfig{
						ImageType: ociv1beta1.OKEImage,
						ImageFilter: &ociv1beta1.ImageSelectorTerm{
							OsFilter:        config.OCINodeClass.ImageOsFilter,
							OsVersionFilter: config.OCINodeClass.ImageOsVersionFilter,
						},
					},
					VolumeAttribute: ociv1beta1.VolumeAttribute{
						KmsKeyConfig: &ociv1beta1.KmsKeyConfig{
							KmsKeyId: common.String(config.OCINodeClass.KmsKey),
						},
						SizeInGBs: common.Int64(config.OCINodeClass.BootVolumeSizeInGB),
					},
					PvEncryptionInTransit: common.Bool(config.OCINodeClass.PVEncryptionInTransit),
				},
			},
			NodeCompartmentId: common.String(config.OCINodeClass.NodeCompartmentID),
			NetworkConfig: &ociv1beta1.NetworkConfig{
				PrimaryVnicConfig: &ociv1beta1.SimpleVnicConfig{
					SubnetAndNsgConfig: &ociv1beta1.SubnetAndNsgConfig{
						SubnetConfig: &ociv1beta1.SubnetConfig{
							SubnetFilter: &ociv1beta1.OciResourceSelectorTerm{
								CompartmentId: &config.CompartmentID,
								DisplayName:   &config.OCINodeClass.SubnetName,
							},
						},
						NetworkSecurityGroupConfigs: []*ociv1beta1.NetworkSecurityGroupConfig{
							{
								NetworkSecurityGroupFilter: &nsgSlector,
							},
						},
					},
					VnicDisplayName:     config.OCINodeClass.PrimaryVnicDisplayName,
					SkipSourceDestCheck: config.OCINodeClass.PrimaryVnicSkipSourceDestCheck,
				},
				SecondaryVnicConfigs: secondVnicConfigs,
			},
			KubeletConfig: &ociv1beta1.KubeletConfiguration{
				MaxPods:        &config.OCINodeClass.KubeletConfig.MaxPods,
				PodsPerCore:    &config.OCINodeClass.KubeletConfig.PodsPerCore,
				SystemReserved: config.OCINodeClass.KubeletConfig.SystemReserved,
				KubeReserved:   config.OCINodeClass.KubeletConfig.KubeReserved,
			},
			SshAuthorizedKeys: []string{config.OCINodeClass.SshPubKey},
			Metadata:          config.OCINodeClass.Metadata,
			FreeformTags:      config.OCINodeClass.FreeformTags,
			DefinedTags:       config.OCINodeClass.DefinedTags,
		},
	}
}

func testDeployment(config *KarpenterE2ETestConfig) *appsv1.Deployment {
	cpuReq, _ := resource.ParseQuantity(config.TestDeployment.CPUReq)

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.TestDeployment.Name,
			Namespace: config.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &config.TestDeployment.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": config.TestDeployment.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": config.TestDeployment.Name,
					},
				},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: lo.ToPtr(int64(0)),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:  lo.ToPtr(int64(1000)),
						RunAsGroup: lo.ToPtr(int64(3000)),
						FSGroup:    lo.ToPtr(int64(2000)),
					},
					Tolerations: []corev1.Toleration{
						{
							Key:      instancetype.NvidiaGpuTaintKey,
							Operator: corev1.TolerationOpExists,
							Effect:   corev1.TaintEffectNoSchedule,
						},
						{
							Key:      instancetype.AmdGpuTaintKey,
							Operator: corev1.TolerationOpExists,
							Effect:   corev1.TaintEffectNoSchedule,
						},
						{
							Key:      instancetype.PreemptibleTaintKey,
							Operator: corev1.TolerationOpExists,
							Effect:   corev1.TaintEffectNoSchedule,
						},
					},
					Affinity: &corev1.Affinity{
						NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "karpenter.sh/nodepool",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{config.NodePool.Name},
											},
										},
									},
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:    config.TestDeployment.Name,
							Image:   config.TestDeployment.Image,
							Command: []string{"/bin/sh", "-c", "while true; do echo hello; sleep 5; done"},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: cpuReq,
								},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: func() *bool { b := false; return &b }(),
							},
						},
					},
				},
			},
		},
	}
}

func ocidSuffix(ocid string) string {
	tokens := strings.Split(ocid, ".")
	return tokens[len(tokens)-1]
}
