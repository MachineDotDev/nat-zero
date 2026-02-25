package main

import (
	"context"
	"sync/atomic"
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

	t.Run("cleanup action ignores other fields", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{Addresses: []ec2types.Address{}}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{
			Action: "cleanup", InstanceID: "i-1", State: "running",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// --- Ignored instances ---

func TestHandlerIgnored(t *testing.T) {
	t.Run("ignored instance returns early", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(), nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-skip", State: "running"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("DescribeInstances") != 1 {
			t.Errorf("expected 1 DescribeInstances call (classify), got %d", mock.callCount("DescribeInstances"))
		}
	})

	t.Run("terminated event sweeps idle NATs when instance gone", func(t *testing.T) {
		mock := &mockEC2{}
		natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, nil)
		var callIdx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				// classify: instance not found (already gone from API)
				return describeResponse(), nil
			}
			if params.Filters != nil {
				for _, f := range params.Filters {
					if aws.ToString(f.Name) == "tag:nat-zero:managed" {
						return describeResponse(natInst), nil
					}
				}
			}
			// findSiblings: no siblings
			return describeResponse(), nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-gone", State: "terminated"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StopInstances") != 1 {
			t.Errorf("expected StopInstances via sweep, got %d", mock.callCount("StopInstances"))
		}
	})

	t.Run("non-terminating ignored event does not sweep", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(), nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-skip", State: "running"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StopInstances") != 0 {
			t.Error("expected no sweep for non-terminating event")
		}
	})
}

// --- NAT instance events (EventBridge-driven EIP management) ---

func TestHandlerNatEvents(t *testing.T) {
	natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}

	t.Run("running NAT triggers attachEIP", func(t *testing.T) {
		mock := &mockEC2{}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, []ec2types.InstanceNetworkInterface{eni})
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(natInst), nil
		}
		mock.AllocateAddressFn = func(ctx context.Context, params *ec2.AllocateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AllocateAddressOutput, error) {
			return &ec2.AllocateAddressOutput{AllocationId: aws.String("eipalloc-1"), PublicIp: aws.String("1.2.3.4")}, nil
		}
		mock.DescribeNetworkInterfacesFn = func(ctx context.Context, params *ec2.DescribeNetworkInterfacesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
			return &ec2.DescribeNetworkInterfacesOutput{
				NetworkInterfaces: []ec2types.NetworkInterface{{
					NetworkInterfaceId: aws.String("eni-pub1"),
				}},
			}, nil
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
		if mock.callCount("AssociateAddress") != 1 {
			t.Error("expected AssociateAddress for NAT running event")
		}
	})

	t.Run("pending NAT triggers attachEIP", func(t *testing.T) {
		mock := &mockEC2{}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		// classify returns pending NAT, then waitForState polls until running
		var describeCount int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&describeCount, 1)
			if idx == 1 {
				// classify
				return describeResponse(makeTestInstance("i-nat1", "pending", testVPC, testAZ, natTags, nil)), nil
			}
			// waitForState + getInstance — return running
			return describeResponse(makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, []ec2types.InstanceNetworkInterface{eni})), nil
		}
		mock.AllocateAddressFn = func(ctx context.Context, params *ec2.AllocateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AllocateAddressOutput, error) {
			return &ec2.AllocateAddressOutput{AllocationId: aws.String("eipalloc-1"), PublicIp: aws.String("1.2.3.4")}, nil
		}
		mock.DescribeNetworkInterfacesFn = func(ctx context.Context, params *ec2.DescribeNetworkInterfacesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
			return &ec2.DescribeNetworkInterfacesOutput{
				NetworkInterfaces: []ec2types.NetworkInterface{{
					NetworkInterfaceId: aws.String("eni-pub1"),
				}},
			}, nil
		}
		mock.AssociateAddressFn = func(ctx context.Context, params *ec2.AssociateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AssociateAddressOutput, error) {
			return &ec2.AssociateAddressOutput{}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-nat1", State: "pending"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("AllocateAddress") != 1 {
			t.Error("expected AllocateAddress for NAT pending event")
		}
	})

	t.Run("running NAT with existing EIP is noop", func(t *testing.T) {
		mock := &mockEC2{}
		assoc := &ec2types.InstanceNetworkInterfaceAssociation{PublicIp: aws.String("5.6.7.8")}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", assoc)
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, []ec2types.InstanceNetworkInterface{eni})
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(natInst), nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-nat1", State: "running"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("AllocateAddress") != 0 {
			t.Error("expected no AllocateAddress when ENI already has EIP")
		}
	})

	t.Run("stopped NAT triggers detachEIP", func(t *testing.T) {
		mock := &mockEC2{}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		natInst := makeTestInstance("i-nat1", "stopped", testVPC, testAZ, natTags, []ec2types.InstanceNetworkInterface{eni})
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(natInst), nil
		}
		mock.DescribeNetworkInterfacesFn = func(ctx context.Context, params *ec2.DescribeNetworkInterfacesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
			return &ec2.DescribeNetworkInterfacesOutput{
				NetworkInterfaces: []ec2types.NetworkInterface{{
					NetworkInterfaceId: aws.String("eni-pub1"),
					Association: &ec2types.NetworkInterfaceAssociation{
						AssociationId: aws.String("eipassoc-1"),
						AllocationId:  aws.String("eipalloc-1"),
						PublicIp:      aws.String("1.2.3.4"),
					},
				}},
			}, nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{Addresses: []ec2types.Address{}}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-nat1", State: "stopped"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("DisassociateAddress") != 1 {
			t.Error("expected DisassociateAddress for NAT stopped event")
		}
		if mock.callCount("ReleaseAddress") != 1 {
			t.Error("expected ReleaseAddress for NAT stopped event")
		}
	})

	t.Run("terminated NAT sweeps orphan EIPs", func(t *testing.T) {
		mock := &mockEC2{}
		natInst := makeTestInstance("i-nat1", "terminated", testVPC, testAZ, natTags, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(natInst), nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-nat1", State: "terminated"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("AllocateAddress") != 0 {
			t.Error("expected no AllocateAddress for terminated NAT")
		}
		// sweepOrphanEIPs runs (DescribeAddresses called)
		if mock.callCount("DescribeAddresses") != 1 {
			t.Errorf("expected DescribeAddresses=1 (orphan sweep), got %d", mock.callCount("DescribeAddresses"))
		}
	})
}

// --- Workload scale-up ---

func TestHandlerWorkloadScaleUp(t *testing.T) {
	workTags := []ec2types.Tag{{Key: aws.String("App"), Value: aws.String("web")}}

	t.Run("no NAT creates one", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "pending", testVPC, testAZ, workTags, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 && params.InstanceIds[0] == "i-work1" {
				return describeResponse(workInst), nil
			}
			return describeResponse(), nil
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
		mock.DescribeImagesFn = func(ctx context.Context, params *ec2.DescribeImagesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
			return &ec2.DescribeImagesOutput{Images: []ec2types.Image{}}, nil
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
		// EIP is NOT managed inline anymore — no AllocateAddress expected
		if mock.callCount("AllocateAddress") != 0 {
			t.Error("expected no AllocateAddress (EIP managed via EventBridge)")
		}
	})

	t.Run("stopped NAT starts it", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "pending", testVPC, testAZ, workTags, nil)
		natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
		natInst := makeTestInstance("i-nat1", "stopped", testVPC, testAZ, natTags, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if len(params.InstanceIds) > 0 && params.InstanceIds[0] == "i-work1" {
				return describeResponse(workInst), nil
			}
			if params.Filters != nil {
				return describeResponse(natInst), nil
			}
			// waitForState for NAT → return stopped
			return describeResponse(natInst), nil
		}
		mock.StartInstancesFn = func(ctx context.Context, params *ec2.StartInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
			return &ec2.StartInstancesOutput{}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "pending"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StartInstances") != 1 {
			t.Error("expected StartInstances to be called")
		}
		// EIP is NOT managed inline anymore
		if mock.callCount("AllocateAddress") != 0 {
			t.Error("expected no AllocateAddress (EIP managed via EventBridge)")
		}
	})

	t.Run("running NAT is noop", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "pending", testVPC, testAZ, workTags, nil)
		natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, nil)
		var callIdx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				return describeResponse(workInst), nil
			}
			return describeResponse(natInst), nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "pending"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("RunInstances") != 0 {
			t.Error("expected RunInstances NOT to be called")
		}
		if mock.callCount("StartInstances") != 0 {
			t.Error("expected StartInstances NOT to be called")
		}
	})

	t.Run("terminated NAT creates new", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "running", testVPC, testAZ, workTags, nil)
		natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
		natInst := makeTestInstance("i-nat1", "terminated", testVPC, testAZ, natTags, nil)
		var callIdx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				return describeResponse(workInst), nil
			}
			return describeResponse(natInst), nil
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
		mock.DescribeImagesFn = func(ctx context.Context, params *ec2.DescribeImagesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
			return &ec2.DescribeImagesOutput{Images: []ec2types.Image{}}, nil
		}
		mock.RunInstancesFn = func(ctx context.Context, params *ec2.RunInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
			return &ec2.RunInstancesOutput{
				Instances: []ec2types.Instance{{InstanceId: aws.String("i-new")}},
			}, nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "running"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("RunInstances") != 1 {
			t.Error("expected RunInstances to be called")
		}
	})

	t.Run("shutting-down NAT creates new", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "pending", testVPC, testAZ, workTags, nil)
		natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
		natInst := makeTestInstance("i-nat1", "shutting-down", testVPC, testAZ, natTags, nil)
		var callIdx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				return describeResponse(workInst), nil
			}
			return describeResponse(natInst), nil
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
		mock.DescribeImagesFn = func(ctx context.Context, params *ec2.DescribeImagesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
			return &ec2.DescribeImagesOutput{Images: []ec2types.Image{}}, nil
		}
		mock.RunInstancesFn = func(ctx context.Context, params *ec2.RunInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
			return &ec2.RunInstancesOutput{
				Instances: []ec2types.Instance{{InstanceId: aws.String("i-new")}},
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
}

// --- Workload scale-down ---

func TestHandlerWorkloadScaleDown(t *testing.T) {
	workTags := []ec2types.Tag{{Key: aws.String("App"), Value: aws.String("web")}}
	natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}

	t.Run("no NAT returns early", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "stopping", testVPC, testAZ, workTags, nil)
		var callIdx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				return describeResponse(workInst), nil
			}
			return describeResponse(), nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "stopping"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StopInstances") != 0 {
			t.Error("expected StopInstances NOT to be called")
		}
	})

	t.Run("siblings exist keeps NAT", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "stopping", testVPC, testAZ, workTags, nil)
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, nil)
		sibInst := makeTestInstance("i-sib1", "running", testVPC, testAZ, workTags, nil)
		var callIdx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				return describeResponse(workInst), nil
			}
			if params.Filters != nil {
				for _, f := range params.Filters {
					if aws.ToString(f.Name) == "tag:nat-zero:managed" {
						return describeResponse(natInst), nil
					}
				}
			}
			return describeResponse(sibInst), nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "stopping"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StopInstances") != 0 {
			t.Error("expected StopInstances NOT to be called")
		}
	})

	t.Run("no siblings stops running NAT", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "terminated", testVPC, testAZ, workTags, nil)
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, nil)
		var callIdx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				return describeResponse(workInst), nil
			}
			if params.Filters != nil {
				for _, f := range params.Filters {
					if aws.ToString(f.Name) == "tag:nat-zero:managed" {
						return describeResponse(natInst), nil
					}
				}
			}
			return describeResponse(), nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "terminated"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StopInstances") != 1 {
			t.Errorf("expected StopInstances to be called once, got %d", mock.callCount("StopInstances"))
		}
		// EIP is NOT released inline anymore — detachEIP happens via EventBridge
		if mock.callCount("DisassociateAddress") != 0 {
			t.Error("expected no DisassociateAddress (EIP managed via EventBridge)")
		}
	})

	t.Run("no siblings NAT already stopped is noop", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "stopping", testVPC, testAZ, workTags, nil)
		natInst := makeTestInstance("i-nat1", "stopped", testVPC, testAZ, natTags, nil)
		var callIdx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				return describeResponse(workInst), nil
			}
			if params.Filters != nil {
				for _, f := range params.Filters {
					if aws.ToString(f.Name) == "tag:nat-zero:managed" {
						return describeResponse(natInst), nil
					}
				}
			}
			return describeResponse(), nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "stopping"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StopInstances") != 0 {
			t.Error("expected StopInstances NOT to be called")
		}
	})

	t.Run("persistent siblings keeps NAT after retries", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "stopping", testVPC, testAZ, workTags, nil)
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, nil)
		sibInst := makeTestInstance("i-sib1", "running", testVPC, testAZ, workTags, nil)
		var callIdx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				return describeResponse(workInst), nil
			}
			if params.Filters != nil {
				for _, f := range params.Filters {
					if aws.ToString(f.Name) == "tag:nat-zero:managed" {
						return describeResponse(natInst), nil
					}
				}
			}
			// All findSiblings calls return a sibling
			return describeResponse(sibInst), nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "stopping"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StopInstances") != 0 {
			t.Error("expected StopInstances NOT to be called")
		}
	})

	t.Run("trigger instance excluded from siblings", func(t *testing.T) {
		mock := &mockEC2{}
		// The trigger workload still shows as "running" due to EC2 eventual consistency
		workInst := makeTestInstance("i-work1", "running", testVPC, testAZ, workTags, nil)
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, nil)
		var callIdx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				// classify: the trigger instance
				return describeResponse(workInst), nil
			}
			if params.Filters != nil {
				for _, f := range params.Filters {
					if aws.ToString(f.Name) == "tag:nat-zero:managed" {
						return describeResponse(natInst), nil
					}
				}
			}
			// findSiblings: the trigger instance shows as running (eventual consistency)
			// but should be excluded by its ID
			return describeResponse(workInst), nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "shutting-down"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StopInstances") != 1 {
			t.Errorf("expected StopInstances (trigger excluded from siblings), got %d", mock.callCount("StopInstances"))
		}
	})

	t.Run("pending NAT no siblings stops", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "stopped", testVPC, testAZ, workTags, nil)
		natInst := makeTestInstance("i-nat1", "pending", testVPC, testAZ, natTags, nil)
		var callIdx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				return describeResponse(workInst), nil
			}
			if params.Filters != nil {
				for _, f := range params.Filters {
					if aws.ToString(f.Name) == "tag:nat-zero:managed" {
						return describeResponse(natInst), nil
					}
				}
			}
			return describeResponse(), nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "stopped"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StopInstances") != 1 {
			t.Errorf("expected StopInstances to be called once, got %d", mock.callCount("StopInstances"))
		}
	})
}

// --- Config version replacement ---

func TestHandlerConfigVersion(t *testing.T) {
	workTags := []ec2types.Tag{{Key: aws.String("App"), Value: aws.String("web")}}
	natTags := []ec2types.Tag{
		{Key: aws.String("nat-zero:managed"), Value: aws.String("true")},
		{Key: aws.String("ConfigVersion"), Value: aws.String("old456")},
	}

	t.Run("outdated config triggers replace", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "running", testVPC, testAZ, workTags, nil)
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, nil)
		var callIdx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				return describeResponse(workInst), nil
			}
			if params.Filters != nil {
				for _, f := range params.Filters {
					if aws.ToString(f.Name) == "tag:nat-zero:managed" {
						return describeResponse(natInst), nil
					}
				}
			}
			return describeResponse(makeTestInstance("i-nat1", "terminated", testVPC, testAZ, natTags, nil)), nil
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
		mock.DescribeImagesFn = func(ctx context.Context, params *ec2.DescribeImagesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
			return &ec2.DescribeImagesOutput{Images: []ec2types.Image{}}, nil
		}
		mock.RunInstancesFn = func(ctx context.Context, params *ec2.RunInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
			return &ec2.RunInstancesOutput{
				Instances: []ec2types.Instance{{InstanceId: aws.String("i-new")}},
			}, nil
		}
		h := newTestHandler(mock)
		h.ConfigVersion = "abc123"
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "running"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("TerminateInstances") != 1 {
			t.Error("expected TerminateInstances to be called (replace)")
		}
		if mock.callCount("RunInstances") != 1 {
			t.Error("expected RunInstances to be called (create replacement)")
		}
	})

	t.Run("missing config tag skips replace", func(t *testing.T) {
		// When the ConfigVersion tag is absent (e.g. EC2 eventual consistency
		// delay on a just-created instance, or an older NAT), there is nothing
		// to compare against so isCurrentConfig returns true and no replacement
		// happens.
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "running", testVPC, testAZ, workTags, nil)
		noVersionTags := []ec2types.Tag{
			{Key: aws.String("nat-zero:managed"), Value: aws.String("true")},
			// No ConfigVersion tag
		}
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, noVersionTags, nil)
		var callIdx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				return describeResponse(workInst), nil
			}
			return describeResponse(natInst), nil
		}
		h := newTestHandler(mock)
		h.ConfigVersion = "abc123"
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "running"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("TerminateInstances") != 0 {
			t.Error("expected TerminateInstances NOT to be called when tag is missing")
		}
		if mock.callCount("RunInstances") != 0 {
			t.Error("expected RunInstances NOT to be called when tag is missing")
		}
	})

	t.Run("current config is noop", func(t *testing.T) {
		mock := &mockEC2{}
		workInst := makeTestInstance("i-work1", "pending", testVPC, testAZ, workTags, nil)
		currentTags := []ec2types.Tag{
			{Key: aws.String("nat-zero:managed"), Value: aws.String("true")},
			{Key: aws.String("ConfigVersion"), Value: aws.String("abc123")},
		}
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, currentTags, nil)
		var callIdx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				return describeResponse(workInst), nil
			}
			return describeResponse(natInst), nil
		}
		h := newTestHandler(mock)
		h.ConfigVersion = "abc123"
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "pending"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("RunInstances") != 0 {
			t.Error("expected RunInstances NOT to be called")
		}
		if mock.callCount("StartInstances") != 0 {
			t.Error("expected StartInstances NOT to be called")
		}
		if mock.callCount("TerminateInstances") != 0 {
			t.Error("expected TerminateInstances NOT to be called")
		}
	})
}
