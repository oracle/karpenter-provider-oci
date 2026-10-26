/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package fakes

import (
	"context"

	"github.com/awslabs/operatorpkg/status"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

var _ cloudprovider.CloudProvider = (*FakeCloudProvider)(nil)

type FakeCloudProvider struct {
	CreateStub                  func(context.Context, *v1.NodeClaim) (*v1.NodeClaim, error)
	RebootStub                  func(context.Context, *v1.NodeClaim, string) error
	DeleteStub                  func(context.Context, *v1.NodeClaim) error
	GetStub                     func(context.Context, string) (*v1.NodeClaim, error)
	ListStub                    func(context.Context) ([]*v1.NodeClaim, error)
	GetInstanceTypesStub        func(context.Context, *v1.NodePool) ([]*cloudprovider.InstanceType, error)
	IsDriftedStub               func(context.Context, *v1.NodeClaim) (cloudprovider.DriftReason, error)
	RepairPoliciesStub          func() []cloudprovider.RepairPolicy
	NameStub                    func() string
	GetSupportedNodeClassesStub func() []status.Object
}

func (f *FakeCloudProvider) Create(ctx context.Context, nc *v1.NodeClaim) (*v1.NodeClaim, error) {
	return f.CreateStub(ctx, nc)
}

func (f *FakeCloudProvider) Delete(ctx context.Context, nc *v1.NodeClaim) error {
	return f.DeleteStub(ctx, nc)
}

func (f *FakeCloudProvider) Reboot(ctx context.Context, nc *v1.NodeClaim, operationID string) error {
	if f.RebootStub != nil {
		return f.RebootStub(ctx, nc, operationID)
	}
	return cloudprovider.NewNodeRebootNotImplementedError()
}

func (f *FakeCloudProvider) Get(ctx context.Context, providerId string) (*v1.NodeClaim, error) {
	return f.GetStub(ctx, providerId)
}

func (f *FakeCloudProvider) List(ctx context.Context) ([]*v1.NodeClaim, error) {
	return f.ListStub(ctx)
}

func (f *FakeCloudProvider) GetInstanceTypes(ctx context.Context,
	np *v1.NodePool) ([]*cloudprovider.InstanceType, error) {
	return f.GetInstanceTypesStub(ctx, np)
}

func (f *FakeCloudProvider) IsDrifted(ctx context.Context, nc *v1.NodeClaim) (cloudprovider.DriftReason, error) {
	return f.IsDriftedStub(ctx, nc)
}

func (f *FakeCloudProvider) RepairPolicies() []cloudprovider.RepairPolicy {
	return f.RepairPoliciesStub()
}

func (f *FakeCloudProvider) Name() string {
	return f.NameStub()
}

func (f *FakeCloudProvider) GetSupportedNodeClasses() []status.Object {
	return f.GetSupportedNodeClassesStub()
}
