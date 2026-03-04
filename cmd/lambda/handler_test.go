package main

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// --- Cleanup action ---

func TestHandlerCleanup(t *testing.T) {
	t.Run("cleanup action calls cleanupAll", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{Addresses: []ec2types.Address{}}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{Action: "cleanup"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("DescribeInstances") == 0 {
			t.Error("expected DescribeInstances to be called during cleanup")
		}
	})
}

// --- resolveAZ ---

func TestResolveAZ(t *testing.T) {
	t.Run("instance not found triggers sweep", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(), nil
		}
		// Sweep will call DescribeLaunchTemplates but find nothing
		mock.DescribeLaunchTemplatesFn = func(ctx context.Context, params *ec2.DescribeLaunchTemplatesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplatesOutput, error) {
			return &ec2.DescribeLaunchTemplatesOutput{}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-gone", State: "terminated"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// DescribeLaunchTemplates called for sweep
		if mock.callCount("DescribeLaunchTemplates") != 1 {
			t.Errorf("expected sweep via DescribeLaunchTemplates, got %d", mock.callCount("DescribeLaunchTemplates"))
		}
	})

	t.Run("wrong VPC triggers sweep", func(t *testing.T) {
		mock := &mockEC2{}
		inst := makeTestInstance("i-other", "running", "vpc-other", testAZ, nil, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				return describeResponse(inst), nil
			}
			return describeResponse(), nil
		}
		mock.DescribeLaunchTemplatesFn = func(ctx context.Context, params *ec2.DescribeLaunchTemplatesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplatesOutput, error) {
			return &ec2.DescribeLaunchTemplatesOutput{}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-other", State: "running"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("ignored instance triggers sweep", func(t *testing.T) {
		mock := &mockEC2{}
		inst := makeTestInstance("i-ign", "running", testVPC, testAZ,
			[]ec2types.Tag{{Key: aws.String("nat-zero:ignore"), Value: aws.String("true")}}, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				return describeResponse(inst), nil
			}
			return describeResponse(), nil
		}
		mock.DescribeLaunchTemplatesFn = func(ctx context.Context, params *ec2.DescribeLaunchTemplatesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplatesOutput, error) {
			return &ec2.DescribeLaunchTemplatesOutput{}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-ign", State: "running"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// --- Reconcile: scale-up ---

func TestReconcileScaleUp(t *testing.T) {
	workTags := []ec2types.Tag{{Key: aws.String("App"), Value: aws.String("web")}}

	t.Run("workloads exist no NAT creates one", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "running", testVPC, testAZ, workTags, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				return describeResponse(workInst), nil
			}
			// Filter queries
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "tag:nat-zero:managed" {
					return describeResponse(), nil // no NAT
				}
			}
			return describeResponse(workInst), nil // workloads query
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{}, nil
		}
		mock.DescribeLaunchTemplatesFn = func(ctx context.Context, params *ec2.DescribeLaunchTemplatesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplatesOutput, error) {
			return &ec2.DescribeLaunchTemplatesOutput{
				LaunchTemplates: []ec2types.LaunchTemplate{{LaunchTemplateId: aws.String("lt-123")}},
			}, nil
		}
		mock.DescribeLaunchTemplateVersionsFn = func(ctx context.Context, params *ec2.DescribeLaunchTemplateVersionsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplateVersionsOutput, error) {
			return &ec2.DescribeLaunchTemplateVersionsOutput{
				LaunchTemplateVersions: []ec2types.LaunchTemplateVersion{{
					LaunchTemplateId: aws.String("lt-123"), VersionNumber: aws.Int64(1),
				}},
			}, nil
		}
		mock.RunInstancesFn = func(ctx context.Context, params *ec2.RunInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
			return &ec2.RunInstancesOutput{
				Instances: []ec2types.Instance{{InstanceId: aws.String("i-new1")}},
			}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "pending"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("RunInstances") != 1 {
			t.Error("expected RunInstances to be called")
		}
	})

	t.Run("workloads exist stopped NAT starts it", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "running", testVPC, testAZ, workTags, nil)
		natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
		natInst := makeTestInstance("i-nat1", "stopped", testVPC, testAZ, natTags, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				return describeResponse(workInst), nil
			}
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "tag:nat-zero:managed" {
					return describeResponse(natInst), nil
				}
			}
			return describeResponse(workInst), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "pending"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StartInstances") != 1 {
			t.Error("expected StartInstances to be called")
		}
	})

	t.Run("workloads exist running NAT is noop", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "running", testVPC, testAZ, workTags, nil)
		natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", &ec2types.InstanceNetworkInterfaceAssociation{PublicIp: aws.String("1.2.3.4")})
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, []ec2types.InstanceNetworkInterface{eni})
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				return describeResponse(workInst), nil
			}
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "tag:nat-zero:managed" {
					return describeResponse(natInst), nil
				}
			}
			return describeResponse(workInst), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{
				Addresses: []ec2types.Address{{AllocationId: aws.String("eipalloc-1")}},
			}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "running"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("RunInstances") != 0 {
			t.Error("expected no RunInstances")
		}
		if mock.callCount("StartInstances") != 0 {
			t.Error("expected no StartInstances")
		}
		if mock.callCount("StopInstances") != 0 {
			t.Error("expected no StopInstances")
		}
	})

	t.Run("workloads exist stopping NAT waits", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "running", testVPC, testAZ, workTags, nil)
		natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
		natInst := makeTestInstance("i-nat1", "stopping", testVPC, testAZ, natTags, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				return describeResponse(workInst), nil
			}
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "tag:nat-zero:managed" {
					return describeResponse(natInst), nil
				}
			}
			return describeResponse(workInst), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "pending"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// No action — wait for next event when NAT reaches stopped
		if mock.callCount("StartInstances") != 0 {
			t.Error("expected no StartInstances (NAT is stopping)")
		}
		if mock.callCount("RunInstances") != 0 {
			t.Error("expected no RunInstances (NAT is stopping)")
		}
	})
}

// --- Reconcile: scale-down ---

func TestReconcileScaleDown(t *testing.T) {
	natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}

	t.Run("no workloads stops running NAT", func(t *testing.T) {
		mock := &mockEC2{}
		// Trigger is a workload that's shutting down (resolveAZ finds it)
		workInst := makeTestInstance("i-work1", "shutting-down", testVPC, testAZ, nil, nil)
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				return describeResponse(workInst), nil
			}
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "tag:nat-zero:managed" {
					return describeResponse(natInst), nil
				}
			}
			// workloads query: nothing pending/running
			return describeResponse(), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "shutting-down"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StopInstances") != 1 {
			t.Errorf("expected StopInstances=1, got %d", mock.callCount("StopInstances"))
		}
	})

	t.Run("no workloads stopped NAT is noop", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "terminated", testVPC, testAZ, nil, nil)
		natInst := makeTestInstance("i-nat1", "stopped", testVPC, testAZ, natTags, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				return describeResponse(workInst), nil
			}
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "tag:nat-zero:managed" {
					return describeResponse(natInst), nil
				}
			}
			return describeResponse(), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "terminated"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StopInstances") != 0 {
			t.Error("expected no StopInstances (NAT already stopped)")
		}
	})

	t.Run("workloads exist keeps NAT running", func(t *testing.T) {
		mock := &mockEC2{}
		workTags := []ec2types.Tag{{Key: aws.String("App"), Value: aws.String("web")}}
		triggerInst := makeTestInstance("i-work1", "stopping", testVPC, testAZ, workTags, nil)
		sibInst := makeTestInstance("i-sib1", "running", testVPC, testAZ, workTags, nil)
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				return describeResponse(triggerInst), nil
			}
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "tag:nat-zero:managed" {
					return describeResponse(natInst), nil
				}
			}
			return describeResponse(sibInst), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{
				Addresses: []ec2types.Address{{AllocationId: aws.String("eipalloc-1")}},
			}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "stopping"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StopInstances") != 0 {
			t.Error("expected no StopInstances (siblings exist)")
		}
	})

	t.Run("no workloads no NAT is noop", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "terminated", testVPC, testAZ, nil, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				return describeResponse(workInst), nil
			}
			return describeResponse(), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "terminated"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StopInstances") != 0 {
			t.Error("expected no StopInstances")
		}
	})
}

// --- Reconcile: EIP convergence ---

func TestReconcileEIP(t *testing.T) {
	natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
	workTags := []ec2types.Tag{{Key: aws.String("App"), Value: aws.String("web")}}

	t.Run("running NAT no EIP allocates one", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "running", testVPC, testAZ, workTags, nil)
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, []ec2types.InstanceNetworkInterface{eni})
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				return describeResponse(workInst), nil
			}
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "tag:nat-zero:managed" {
					return describeResponse(natInst), nil
				}
			}
			return describeResponse(workInst), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{}, nil
		}
		mock.AllocateAddressFn = func(ctx context.Context, params *ec2.AllocateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AllocateAddressOutput, error) {
			return &ec2.AllocateAddressOutput{AllocationId: aws.String("eipalloc-1"), PublicIp: aws.String("1.2.3.4")}, nil
		}
		mock.AssociateAddressFn = func(ctx context.Context, params *ec2.AssociateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AssociateAddressOutput, error) {
			return &ec2.AssociateAddressOutput{}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "running"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("AllocateAddress") != 1 {
			t.Error("expected AllocateAddress")
		}
		if mock.callCount("AssociateAddress") != 1 {
			t.Error("expected AssociateAddress")
		}
	})

	t.Run("NAT not running releases EIPs", func(t *testing.T) {
		mock := &mockEC2{}
		natInst := makeTestInstance("i-nat1", "stopped", testVPC, testAZ, natTags, nil)
		// NAT stopped event — resolveAZ finds the NAT itself (it's in our VPC, no ignore tag)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				return describeResponse(natInst), nil
			}
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "tag:nat-zero:managed" {
					return describeResponse(natInst), nil
				}
			}
			return describeResponse(), nil // no workloads
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{
				Addresses: []ec2types.Address{{
					AllocationId:  aws.String("eipalloc-1"),
					AssociationId: aws.String("eipassoc-1"),
				}},
			}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-nat1", State: "stopped"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("DisassociateAddress") != 1 {
			t.Errorf("expected DisassociateAddress=1, got %d", mock.callCount("DisassociateAddress"))
		}
		if mock.callCount("ReleaseAddress") != 1 {
			t.Errorf("expected ReleaseAddress=1, got %d", mock.callCount("ReleaseAddress"))
		}
	})

	t.Run("multiple EIPs releases extras", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "running", testVPC, testAZ, workTags, nil)
		eni := makeENI("eni-pub1", 0, "10.0.1.10", &ec2types.InstanceNetworkInterfaceAssociation{PublicIp: aws.String("1.2.3.4")})
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, []ec2types.InstanceNetworkInterface{eni})
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				return describeResponse(workInst), nil
			}
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "tag:nat-zero:managed" {
					return describeResponse(natInst), nil
				}
			}
			return describeResponse(workInst), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{
				Addresses: []ec2types.Address{
					{AllocationId: aws.String("eipalloc-1")},
					{AllocationId: aws.String("eipalloc-2")},
				},
			}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "running"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Only the extra EIP should be released (eips[1:])
		if mock.callCount("ReleaseAddress") != 1 {
			t.Errorf("expected ReleaseAddress=1 (extra EIP), got %d", mock.callCount("ReleaseAddress"))
		}
	})
}

// --- Reconcile: config version ---

func TestReconcileConfigVersion(t *testing.T) {
	workTags := []ec2types.Tag{{Key: aws.String("App"), Value: aws.String("web")}}
	natTags := []ec2types.Tag{
		{Key: aws.String("nat-zero:managed"), Value: aws.String("true")},
		{Key: aws.String("ConfigVersion"), Value: aws.String("old456")},
	}

	t.Run("outdated config triggers terminate", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "running", testVPC, testAZ, workTags, nil)
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				return describeResponse(workInst), nil
			}
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "tag:nat-zero:managed" {
					return describeResponse(natInst), nil
				}
			}
			return describeResponse(workInst), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{}, nil
		}
		h := newTestHandler(mock)
		h.ConfigVersion = "abc123"
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "running"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("TerminateInstances") != 1 {
			t.Error("expected TerminateInstances (outdated config)")
		}
		// No immediate replacement — next event creates new
		if mock.callCount("RunInstances") != 0 {
			t.Error("expected no RunInstances (replacement deferred to next event)")
		}
	})

	t.Run("current config is noop", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "running", testVPC, testAZ, workTags, nil)
		currentTags := []ec2types.Tag{
			{Key: aws.String("nat-zero:managed"), Value: aws.String("true")},
			{Key: aws.String("ConfigVersion"), Value: aws.String("abc123")},
		}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", &ec2types.InstanceNetworkInterfaceAssociation{PublicIp: aws.String("1.2.3.4")})
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, currentTags, []ec2types.InstanceNetworkInterface{eni})
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				return describeResponse(workInst), nil
			}
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "tag:nat-zero:managed" {
					return describeResponse(natInst), nil
				}
			}
			return describeResponse(workInst), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{
				Addresses: []ec2types.Address{{AllocationId: aws.String("eipalloc-1")}},
			}, nil
		}
		h := newTestHandler(mock)
		h.ConfigVersion = "abc123"
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "running"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("TerminateInstances") != 0 {
			t.Error("expected no TerminateInstances")
		}
		if mock.callCount("RunInstances") != 0 {
			t.Error("expected no RunInstances")
		}
	})
}

// --- Reconcile: NAT event triggers reconcile ---

func TestReconcileNATEvent(t *testing.T) {
	natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
	workTags := []ec2types.Tag{{Key: aws.String("App"), Value: aws.String("web")}}

	t.Run("NAT running event with workloads attaches EIP", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "running", testVPC, testAZ, workTags, nil)
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, []ec2types.InstanceNetworkInterface{eni})
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				return describeResponse(natInst), nil // resolveAZ on NAT
			}
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "tag:nat-zero:managed" {
					return describeResponse(natInst), nil
				}
			}
			return describeResponse(workInst), nil // workloads
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{}, nil
		}
		mock.AllocateAddressFn = func(ctx context.Context, params *ec2.AllocateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AllocateAddressOutput, error) {
			return &ec2.AllocateAddressOutput{AllocationId: aws.String("eipalloc-1"), PublicIp: aws.String("1.2.3.4")}, nil
		}
		mock.AssociateAddressFn = func(ctx context.Context, params *ec2.AssociateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AssociateAddressOutput, error) {
			return &ec2.AssociateAddressOutput{}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-nat1", State: "running"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("AllocateAddress") != 1 {
			t.Error("expected AllocateAddress for NAT running event")
		}
	})

	t.Run("NAT running event with stale pending filter attaches EIP", func(t *testing.T) {
		// Simulates EC2 eventual consistency: EventBridge says "running" but
		// filter-based DescribeInstances still returns "pending". The reconciler
		// should re-query by instance ID and get the true "running" state.
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "running", testVPC, testAZ, workTags, nil)
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		natPending := makeTestInstance("i-nat1", "pending", testVPC, testAZ, natTags, []ec2types.InstanceNetworkInterface{eni})
		natRunning := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, []ec2types.InstanceNetworkInterface{eni})
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				// By-ID queries return the true state
				return describeResponse(natRunning), nil
			}
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "tag:nat-zero:managed" {
					// Filter query lags — still shows pending
					return describeResponse(natPending), nil
				}
			}
			return describeResponse(workInst), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{}, nil
		}
		mock.AllocateAddressFn = func(ctx context.Context, params *ec2.AllocateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AllocateAddressOutput, error) {
			return &ec2.AllocateAddressOutput{AllocationId: aws.String("eipalloc-1"), PublicIp: aws.String("1.2.3.4")}, nil
		}
		mock.AssociateAddressFn = func(ctx context.Context, params *ec2.AssociateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AssociateAddressOutput, error) {
			return &ec2.AssociateAddressOutput{}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-nat1", State: "running"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("AllocateAddress") != 1 {
			t.Errorf("expected AllocateAddress=1, got %d (stale pending should be corrected via by-ID query)", mock.callCount("AllocateAddress"))
		}
		if mock.callCount("AssociateAddress") != 1 {
			t.Errorf("expected AssociateAddress=1, got %d", mock.callCount("AssociateAddress"))
		}
	})

	t.Run("NAT pending event not found by filter uses triggerInst", func(t *testing.T) {
		// Simulates EC2 eventual consistency: NAT was just created, its pending
		// event fires, but findNATs() doesn't see it yet because tags haven't
		// propagated. The reconciler should use the trigger instance directly
		// to avoid trying to create a duplicate NAT.
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "running", testVPC, testAZ, workTags, nil)
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		natInst := makeTestInstance("i-nat1", "pending", testVPC, testAZ, natTags, []ec2types.InstanceNetworkInterface{eni})
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				// By-ID query finds the NAT
				return describeResponse(natInst), nil
			}
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "tag:nat-zero:managed" {
					// Filter query doesn't see it yet (tags not propagated)
					return describeResponse(), nil
				}
			}
			return describeResponse(workInst), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-nat1", State: "pending"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should NOT try to create a new NAT (would fail with ENI-in-use)
		if mock.callCount("RunInstances") != 0 {
			t.Error("should not call RunInstances when trigger NAT exists but filter doesn't see it")
		}
	})

	t.Run("NAT stopped event with stale stopping filter releases EIP", func(t *testing.T) {
		// Simulates EC2 eventual consistency: EventBridge says "stopped" but
		// filter-based DescribeInstances still returns "stopping". The reconciler
		// should trust the event state and release the EIP.
		mock := &mockEC2{}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", &ec2types.InstanceNetworkInterfaceAssociation{PublicIp: aws.String("1.2.3.4")})
		natStopping := makeTestInstance("i-nat1", "stopping", testVPC, testAZ, natTags, []ec2types.InstanceNetworkInterface{eni})
		eip := ec2types.Address{
			AllocationId: aws.String("eipalloc-1"),
			PublicIp:     aws.String("1.2.3.4"),
			Tags:         []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}},
		}
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				// By-ID queries still show stopping (API lag)
				return describeResponse(natStopping), nil
			}
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "tag:nat-zero:managed" {
					// Filter query lags — still shows stopping
					return describeResponse(natStopping), nil
				}
			}
			// No workloads
			return describeResponse(), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{Addresses: []ec2types.Address{eip}}, nil
		}
		mock.DisassociateAddressFn = func(ctx context.Context, params *ec2.DisassociateAddressInput, optFns ...func(*ec2.Options)) (*ec2.DisassociateAddressOutput, error) {
			return &ec2.DisassociateAddressOutput{}, nil
		}
		mock.ReleaseAddressFn = func(ctx context.Context, params *ec2.ReleaseAddressInput, optFns ...func(*ec2.Options)) (*ec2.ReleaseAddressOutput, error) {
			return &ec2.ReleaseAddressOutput{}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-nat1", State: "stopped"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("ReleaseAddress") != 1 {
			t.Errorf("expected ReleaseAddress=1, got %d (stale stopping should be corrected by trusting event)", mock.callCount("ReleaseAddress"))
		}
	})

	t.Run("NAT terminated event with workloads creates new", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "running", testVPC, testAZ, workTags, nil)
		natInst := makeTestInstance("i-nat1", "terminated", testVPC, testAZ, natTags, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				return describeResponse(natInst), nil // resolveAZ
			}
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "tag:nat-zero:managed" {
					// findNATs: terminated NATs are filtered by state
					return describeResponse(), nil
				}
			}
			return describeResponse(workInst), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{}, nil
		}
		mock.DescribeLaunchTemplatesFn = func(ctx context.Context, params *ec2.DescribeLaunchTemplatesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplatesOutput, error) {
			return &ec2.DescribeLaunchTemplatesOutput{
				LaunchTemplates: []ec2types.LaunchTemplate{{LaunchTemplateId: aws.String("lt-123")}},
			}, nil
		}
		mock.DescribeLaunchTemplateVersionsFn = func(ctx context.Context, params *ec2.DescribeLaunchTemplateVersionsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplateVersionsOutput, error) {
			return &ec2.DescribeLaunchTemplateVersionsOutput{
				LaunchTemplateVersions: []ec2types.LaunchTemplateVersion{{
					LaunchTemplateId: aws.String("lt-123"), VersionNumber: aws.Int64(1),
				}},
			}, nil
		}
		mock.RunInstancesFn = func(ctx context.Context, params *ec2.RunInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
			return &ec2.RunInstancesOutput{
				Instances: []ec2types.Instance{{InstanceId: aws.String("i-new")}},
			}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-nat1", State: "terminated"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("RunInstances") != 1 {
			t.Error("expected RunInstances for terminated NAT with active workloads")
		}
	})
}

// --- Sweep all AZs ---

func TestSweepAllAZs(t *testing.T) {
	t.Run("sweeps configured AZs", func(t *testing.T) {
		mock := &mockEC2{}
		natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
		natInst := makeTestInstance("i-nat1", "running", testVPC, "us-east-1a", natTags, nil)

		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				// resolveAZ: instance gone
				return describeResponse(), nil
			}
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "tag:nat-zero:managed" {
					return describeResponse(natInst), nil
				}
			}
			// workloads: none
			return describeResponse(), nil
		}
		mock.DescribeLaunchTemplatesFn = func(ctx context.Context, params *ec2.DescribeLaunchTemplatesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplatesOutput, error) {
			return &ec2.DescribeLaunchTemplatesOutput{
				LaunchTemplates: []ec2types.LaunchTemplate{{
					LaunchTemplateId: aws.String("lt-1a"),
					Tags: []ec2types.Tag{
						{Key: aws.String("AvailabilityZone"), Value: aws.String("us-east-1a")},
						{Key: aws.String("VpcId"), Value: aws.String(testVPC)},
					},
				}},
			}, nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-gone", State: "terminated"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should stop the idle NAT in us-east-1a
		if mock.callCount("StopInstances") != 1 {
			t.Errorf("expected StopInstances=1 (sweep), got %d", mock.callCount("StopInstances"))
		}
	})
}

// --- Duplicate NAT ---

func TestReconcileDuplicateNATs(t *testing.T) {
	t.Run("deduplicates NATs", func(t *testing.T) {
		mock := &mockEC2{}
		workTags := []ec2types.Tag{{Key: aws.String("App"), Value: aws.String("web")}}
		natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
		workInst := makeTestInstance("i-work1", "running", testVPC, testAZ, workTags, nil)
		eni := makeENI("eni-pub1", 0, "10.0.1.10", &ec2types.InstanceNetworkInterfaceAssociation{PublicIp: aws.String("1.2.3.4")})
		nat1 := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, []ec2types.InstanceNetworkInterface{eni})
		nat2 := makeTestInstance("i-nat2", "running", testVPC, testAZ, natTags, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 {
				return describeResponse(workInst), nil
			}
			for _, f := range params.Filters {
				if aws.ToString(f.Name) == "tag:nat-zero:managed" {
					return describeResponse(nat1, nat2), nil
				}
			}
			return describeResponse(workInst), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{
				Addresses: []ec2types.Address{{AllocationId: aws.String("eipalloc-1")}},
			}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "running"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("TerminateInstances") != 1 {
			t.Errorf("expected TerminateInstances=1 (duplicate), got %d", mock.callCount("TerminateInstances"))
		}
	})
}
