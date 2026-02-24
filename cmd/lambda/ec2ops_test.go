package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// --- classify() ---

func TestClassify(t *testing.T) {
	t.Run("instance not found", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(), nil
		}
		h := newTestHandler(mock)
		ignore, isNAT, az, vpc := h.classify(context.Background(), "i-gone")
		if !ignore || isNAT || az != "" || vpc != "" {
			t.Errorf("expected (true, false, '', ''), got (%v, %v, %q, %q)", ignore, isNAT, az, vpc)
		}
	})

	t.Run("wrong VPC", func(t *testing.T) {
		mock := &mockEC2{}
		inst := makeTestInstance("i-other", "running", "vpc-other", testAZ, nil, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(inst), nil
		}
		h := newTestHandler(mock)
		ignore, isNAT, _, _ := h.classify(context.Background(), "i-other")
		if !ignore || isNAT {
			t.Errorf("expected (true, false), got (%v, %v)", ignore, isNAT)
		}
	})

	t.Run("ignore tag", func(t *testing.T) {
		mock := &mockEC2{}
		inst := makeTestInstance("i-ign", "running", testVPC, testAZ,
			[]ec2types.Tag{{Key: aws.String("nat-zero:ignore"), Value: aws.String("true")}}, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(inst), nil
		}
		h := newTestHandler(mock)
		ignore, isNAT, az, vpc := h.classify(context.Background(), "i-ign")
		if !ignore || isNAT || az != testAZ || vpc != testVPC {
			t.Errorf("expected (true, false, %q, %q), got (%v, %v, %q, %q)", testAZ, testVPC, ignore, isNAT, az, vpc)
		}
	})

	t.Run("NAT instance", func(t *testing.T) {
		mock := &mockEC2{}
		inst := makeTestInstance("i-nat", "running", testVPC, testAZ,
			[]ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(inst), nil
		}
		h := newTestHandler(mock)
		ignore, isNAT, az, vpc := h.classify(context.Background(), "i-nat")
		if ignore || !isNAT || az != testAZ || vpc != testVPC {
			t.Errorf("expected (false, true, %q, %q), got (%v, %v, %q, %q)", testAZ, testVPC, ignore, isNAT, az, vpc)
		}
	})

	t.Run("normal workload", func(t *testing.T) {
		mock := &mockEC2{}
		inst := makeTestInstance("i-work", "running", testVPC, testAZ,
			[]ec2types.Tag{{Key: aws.String("App"), Value: aws.String("web")}}, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(inst), nil
		}
		h := newTestHandler(mock)
		ignore, isNAT, az, vpc := h.classify(context.Background(), "i-work")
		if ignore || isNAT || az != testAZ || vpc != testVPC {
			t.Errorf("expected (false, false, %q, %q), got (%v, %v, %q, %q)", testAZ, testVPC, ignore, isNAT, az, vpc)
		}
	})
}

// --- waitForState() ---

func TestWaitForState(t *testing.T) {
	t.Run("already in desired state", func(t *testing.T) {
		mock := &mockEC2{}
		inst := makeTestInstance("i-1", "running", testVPC, testAZ, nil, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(inst), nil
		}
		h := newTestHandler(mock)
		if !h.waitForState(context.Background(), "i-1", []string{"running"}, 10) {
			t.Error("expected true")
		}
	})

	t.Run("transitions to desired state", func(t *testing.T) {
		mock := &mockEC2{}
		var idx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			i := atomic.AddInt32(&idx, 1)
			if i == 1 {
				return describeResponse(makeTestInstance("i-1", "pending", testVPC, testAZ, nil, nil)), nil
			}
			return describeResponse(makeTestInstance("i-1", "running", testVPC, testAZ, nil, nil)), nil
		}
		h := newTestHandler(mock)
		if !h.waitForState(context.Background(), "i-1", []string{"running"}, 10) {
			t.Error("expected true")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-1", "pending", testVPC, testAZ, nil, nil)), nil
		}
		h := newTestHandler(mock)
		if h.waitForState(context.Background(), "i-1", []string{"running"}, 10) {
			t.Error("expected false (timeout)")
		}
	})

	t.Run("instance disappears", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(), nil
		}
		h := newTestHandler(mock)
		if h.waitForState(context.Background(), "i-gone", []string{"running"}, 10) {
			t.Error("expected false")
		}
	})
}

// --- findNAT() ---

func TestFindNAT(t *testing.T) {
	t.Run("no NATs", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(), nil
		}
		h := newTestHandler(mock)
		if h.findNAT(context.Background(), testAZ, testVPC) != nil {
			t.Error("expected nil")
		}
	})

	t.Run("single NAT", func(t *testing.T) {
		mock := &mockEC2{}
		nat := makeTestInstance("i-nat1", "running", testVPC, testAZ, nil, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(nat), nil
		}
		h := newTestHandler(mock)
		result := h.findNAT(context.Background(), testAZ, testVPC)
		if result == nil || result.InstanceID != "i-nat1" {
			t.Errorf("expected i-nat1, got %v", result)
		}
	})

	t.Run("deduplicates keeps running", func(t *testing.T) {
		mock := &mockEC2{}
		running := makeTestInstance("i-run", "running", testVPC, testAZ, nil, nil)
		stopped1 := makeTestInstance("i-stop1", "stopped", testVPC, testAZ, nil, nil)
		stopped2 := makeTestInstance("i-stop2", "stopped", testVPC, testAZ, nil, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(running, stopped1, stopped2), nil
		}
		h := newTestHandler(mock)
		result := h.findNAT(context.Background(), testAZ, testVPC)
		if result == nil || result.InstanceID != "i-run" {
			t.Errorf("expected i-run, got %v", result)
		}
		if mock.callCount("TerminateInstances") != 2 {
			t.Errorf("expected 2 TerminateInstances calls, got %d", mock.callCount("TerminateInstances"))
		}
	})

	t.Run("deduplicates no running keeps first", func(t *testing.T) {
		mock := &mockEC2{}
		s1 := makeTestInstance("i-s1", "stopped", testVPC, testAZ, nil, nil)
		s2 := makeTestInstance("i-s2", "stopped", testVPC, testAZ, nil, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(s1, s2), nil
		}
		h := newTestHandler(mock)
		result := h.findNAT(context.Background(), testAZ, testVPC)
		if result == nil || result.InstanceID != "i-s1" {
			t.Errorf("expected i-s1, got %v", result)
		}
		if mock.callCount("TerminateInstances") != 1 {
			t.Errorf("expected 1 TerminateInstances call, got %d", mock.callCount("TerminateInstances"))
		}
	})

	t.Run("deduplication handles terminate failure", func(t *testing.T) {
		mock := &mockEC2{}
		running := makeTestInstance("i-run", "running", testVPC, testAZ, nil, nil)
		extra := makeTestInstance("i-extra", "stopped", testVPC, testAZ, nil, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(running, extra), nil
		}
		mock.TerminateInstancesFn = func(ctx context.Context, params *ec2.TerminateInstancesInput, optFns ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
			return nil, fmt.Errorf("UnauthorizedOperation: Not allowed")
		}
		h := newTestHandler(mock)
		result := h.findNAT(context.Background(), testAZ, testVPC)
		if result == nil || result.InstanceID != "i-run" {
			t.Errorf("expected i-run despite terminate failure, got %v", result)
		}
	})
}

// --- findSiblings() ---

func TestFindSiblings(t *testing.T) {
	t.Run("no siblings", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(), nil
		}
		h := newTestHandler(mock)
		sibs := h.findSiblings(context.Background(), testAZ, testVPC)
		if len(sibs) != 0 {
			t.Errorf("expected 0 siblings, got %d", len(sibs))
		}
	})

	t.Run("returns workload instances", func(t *testing.T) {
		mock := &mockEC2{}
		work := makeTestInstance("i-work", "running", testVPC, testAZ,
			[]ec2types.Tag{{Key: aws.String("App"), Value: aws.String("api")}}, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(work), nil
		}
		h := newTestHandler(mock)
		sibs := h.findSiblings(context.Background(), testAZ, testVPC)
		if len(sibs) != 1 || sibs[0].InstanceID != "i-work" {
			t.Errorf("expected [i-work], got %v", sibs)
		}
	})

	t.Run("excludes NAT and ignored", func(t *testing.T) {
		mock := &mockEC2{}
		work := makeTestInstance("i-work", "running", testVPC, testAZ,
			[]ec2types.Tag{{Key: aws.String("App"), Value: aws.String("api")}}, nil)
		nat := makeTestInstance("i-nat", "running", testVPC, testAZ,
			[]ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}, nil)
		ignored := makeTestInstance("i-ign", "running", testVPC, testAZ,
			[]ec2types.Tag{{Key: aws.String("nat-zero:ignore"), Value: aws.String("true")}}, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(work, nat, ignored), nil
		}
		h := newTestHandler(mock)
		sibs := h.findSiblings(context.Background(), testAZ, testVPC)
		if len(sibs) != 1 || sibs[0].InstanceID != "i-work" {
			t.Errorf("expected [i-work], got %v", sibs)
		}
	})
}

// --- attachEIP() ---

func TestAttachEIP(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		mock := &mockEC2{}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-nat1", "running", testVPC, testAZ, nil, []ec2types.InstanceNetworkInterface{eni})), nil
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
		h.attachEIP(context.Background(), "i-nat1", testAZ)
		if mock.callCount("AllocateAddress") != 1 {
			t.Error("expected AllocateAddress")
		}
		if mock.callCount("AssociateAddress") != 1 {
			t.Error("expected AssociateAddress")
		}
	})

	t.Run("already has EIP is noop", func(t *testing.T) {
		mock := &mockEC2{}
		assoc := &ec2types.InstanceNetworkInterfaceAssociation{PublicIp: aws.String("5.6.7.8")}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", assoc)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-nat1", "running", testVPC, testAZ, nil, []ec2types.InstanceNetworkInterface{eni})), nil
		}
		h := newTestHandler(mock)
		h.attachEIP(context.Background(), "i-nat1", testAZ)
		if mock.callCount("AllocateAddress") != 0 {
			t.Error("expected no AllocateAddress when ENI already has EIP")
		}
	})

	t.Run("allocation fails", func(t *testing.T) {
		mock := &mockEC2{}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-nat1", "running", testVPC, testAZ, nil, []ec2types.InstanceNetworkInterface{eni})), nil
		}
		mock.AllocateAddressFn = func(ctx context.Context, params *ec2.AllocateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AllocateAddressOutput, error) {
			return nil, fmt.Errorf("AddressLimitExceeded: Too many EIPs")
		}
		h := newTestHandler(mock)
		h.attachEIP(context.Background(), "i-nat1", testAZ)
		if mock.callCount("AssociateAddress") != 0 {
			t.Error("expected AssociateAddress NOT to be called")
		}
	})

	t.Run("association fails releases EIP", func(t *testing.T) {
		mock := &mockEC2{}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-nat1", "running", testVPC, testAZ, nil, []ec2types.InstanceNetworkInterface{eni})), nil
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
			return nil, fmt.Errorf("InvalidParameterValue: Bad param")
		}
		h := newTestHandler(mock)
		h.attachEIP(context.Background(), "i-nat1", testAZ)
		if mock.callCount("ReleaseAddress") != 1 {
			t.Errorf("expected 1 ReleaseAddress call, got %d", mock.callCount("ReleaseAddress"))
		}
	})

	t.Run("no public ENI", func(t *testing.T) {
		mock := &mockEC2{}
		// Instance with no ENIs
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-nat1", "running", testVPC, testAZ, nil, nil)), nil
		}
		h := newTestHandler(mock)
		h.attachEIP(context.Background(), "i-nat1", testAZ)
		if mock.callCount("AllocateAddress") != 0 {
			t.Error("expected no AllocateAddress when no public ENI")
		}
	})

	t.Run("race detected releases allocated EIP", func(t *testing.T) {
		mock := &mockEC2{}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-nat1", "running", testVPC, testAZ, nil, []ec2types.InstanceNetworkInterface{eni})), nil
		}
		mock.AllocateAddressFn = func(ctx context.Context, params *ec2.AllocateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AllocateAddressOutput, error) {
			return &ec2.AllocateAddressOutput{AllocationId: aws.String("eipalloc-1"), PublicIp: aws.String("1.2.3.4")}, nil
		}
		// Re-check shows another invocation already attached an EIP
		mock.DescribeNetworkInterfacesFn = func(ctx context.Context, params *ec2.DescribeNetworkInterfacesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
			return &ec2.DescribeNetworkInterfacesOutput{
				NetworkInterfaces: []ec2types.NetworkInterface{{
					NetworkInterfaceId: aws.String("eni-pub1"),
					Association: &ec2types.NetworkInterfaceAssociation{
						PublicIp: aws.String("9.9.9.9"),
					},
				}},
			}, nil
		}
		h := newTestHandler(mock)
		h.attachEIP(context.Background(), "i-nat1", testAZ)
		if mock.callCount("AssociateAddress") != 0 {
			t.Error("expected no AssociateAddress when race detected")
		}
		if mock.callCount("ReleaseAddress") != 1 {
			t.Errorf("expected 1 ReleaseAddress call, got %d", mock.callCount("ReleaseAddress"))
		}
	})

	t.Run("describe ENI failure still associates", func(t *testing.T) {
		mock := &mockEC2{}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-nat1", "running", testVPC, testAZ, nil, []ec2types.InstanceNetworkInterface{eni})), nil
		}
		mock.AllocateAddressFn = func(ctx context.Context, params *ec2.AllocateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AllocateAddressOutput, error) {
			return &ec2.AllocateAddressOutput{AllocationId: aws.String("eipalloc-1"), PublicIp: aws.String("1.2.3.4")}, nil
		}
		mock.DescribeNetworkInterfacesFn = func(ctx context.Context, params *ec2.DescribeNetworkInterfacesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
			return nil, fmt.Errorf("Throttling: Rate exceeded")
		}
		mock.AssociateAddressFn = func(ctx context.Context, params *ec2.AssociateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AssociateAddressOutput, error) {
			return &ec2.AssociateAddressOutput{}, nil
		}
		h := newTestHandler(mock)
		h.attachEIP(context.Background(), "i-nat1", testAZ)
		if mock.callCount("AssociateAddress") != 1 {
			t.Error("expected AssociateAddress to proceed despite describe failure")
		}
	})
}

// --- detachEIP() ---

func TestDetachEIP(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		mock := &mockEC2{}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-nat1", "stopped", testVPC, testAZ, nil, []ec2types.InstanceNetworkInterface{eni})), nil
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
		h.detachEIP(context.Background(), "i-nat1", testAZ)
		if mock.callCount("DisassociateAddress") != 1 {
			t.Error("expected DisassociateAddress")
		}
		if mock.callCount("ReleaseAddress") != 1 {
			t.Error("expected ReleaseAddress")
		}
	})

	t.Run("no association is noop", func(t *testing.T) {
		mock := &mockEC2{}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-nat1", "stopped", testVPC, testAZ, nil, []ec2types.InstanceNetworkInterface{eni})), nil
		}
		mock.DescribeNetworkInterfacesFn = func(ctx context.Context, params *ec2.DescribeNetworkInterfacesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
			return &ec2.DescribeNetworkInterfacesOutput{
				NetworkInterfaces: []ec2types.NetworkInterface{{
					NetworkInterfaceId: aws.String("eni-pub1"),
				}},
			}, nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{Addresses: []ec2types.Address{}}, nil
		}
		h := newTestHandler(mock)
		h.detachEIP(context.Background(), "i-nat1", testAZ)
		if mock.callCount("DisassociateAddress") != 0 {
			t.Error("expected DisassociateAddress NOT to be called")
		}
	})

	t.Run("cleans up orphaned EIPs", func(t *testing.T) {
		mock := &mockEC2{}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-nat1", "stopped", testVPC, testAZ, nil, []ec2types.InstanceNetworkInterface{eni})), nil
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
			return &ec2.DescribeAddressesOutput{
				Addresses: []ec2types.Address{{
					AllocationId: aws.String("eipalloc-orphan"),
				}},
			}, nil
		}
		h := newTestHandler(mock)
		h.detachEIP(context.Background(), "i-nat1", testAZ)
		// 1 from current association + 1 from orphan sweep
		if mock.callCount("ReleaseAddress") != 2 {
			t.Errorf("expected 2 ReleaseAddress calls, got %d", mock.callCount("ReleaseAddress"))
		}
	})

	t.Run("orphan sweep error is non-fatal", func(t *testing.T) {
		mock := &mockEC2{}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-nat1", "stopped", testVPC, testAZ, nil, []ec2types.InstanceNetworkInterface{eni})), nil
		}
		mock.DescribeNetworkInterfacesFn = func(ctx context.Context, params *ec2.DescribeNetworkInterfacesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
			return &ec2.DescribeNetworkInterfacesOutput{
				NetworkInterfaces: []ec2types.NetworkInterface{{
					NetworkInterfaceId: aws.String("eni-pub1"),
				}},
			}, nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return nil, fmt.Errorf("Throttling: Rate exceeded")
		}
		h := newTestHandler(mock)
		// Should not panic
		h.detachEIP(context.Background(), "i-nat1", testAZ)
		if mock.callCount("DisassociateAddress") != 0 {
			t.Error("expected no DisassociateAddress when no ENI association")
		}
	})
}

// --- createNAT() ---

func TestCreateNAT(t *testing.T) {
	setupLTAndAMI := func(mock *mockEC2) {
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
	}

	t.Run("happy path without inline EIP", func(t *testing.T) {
		mock := &mockEC2{}
		setupLTAndAMI(mock)
		mock.DescribeImagesFn = func(ctx context.Context, params *ec2.DescribeImagesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
			return &ec2.DescribeImagesOutput{
				Images: []ec2types.Image{{
					ImageId:      aws.String("ami-fcknat"),
					Name:         aws.String("fck-nat-al2023-1.0-arm64-20240101"),
					CreationDate: aws.String("2024-01-01T00:00:00.000Z"),
				}},
			}, nil
		}
		mock.RunInstancesFn = func(ctx context.Context, params *ec2.RunInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
			return &ec2.RunInstancesOutput{
				Instances: []ec2types.Instance{{InstanceId: aws.String("i-new1")}},
			}, nil
		}
		h := newTestHandler(mock)
		result := h.createNAT(context.Background(), testAZ, testVPC)
		if result != "i-new1" {
			t.Errorf("expected i-new1, got %s", result)
		}
		// No inline EIP — that's handled by EventBridge now
		if mock.callCount("AllocateAddress") != 0 {
			t.Error("expected no AllocateAddress (EIP managed via EventBridge)")
		}
	})

	t.Run("no launch template", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeLaunchTemplatesFn = func(ctx context.Context, params *ec2.DescribeLaunchTemplatesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplatesOutput, error) {
			return &ec2.DescribeLaunchTemplatesOutput{LaunchTemplates: []ec2types.LaunchTemplate{}}, nil
		}
		h := newTestHandler(mock)
		result := h.createNAT(context.Background(), testAZ, testVPC)
		if result != "" {
			t.Errorf("expected empty, got %s", result)
		}
	})

	t.Run("AMI lookup fails uses template default", func(t *testing.T) {
		mock := &mockEC2{}
		setupLTAndAMI(mock)
		mock.DescribeImagesFn = func(ctx context.Context, params *ec2.DescribeImagesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
			return nil, fmt.Errorf("InvalidParameterValue: Bad filter")
		}
		mock.RunInstancesFn = func(ctx context.Context, params *ec2.RunInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
			if params.ImageId != nil {
				t.Error("expected no ImageId when AMI lookup fails")
			}
			return &ec2.RunInstancesOutput{
				Instances: []ec2types.Instance{{InstanceId: aws.String("i-new2")}},
			}, nil
		}
		h := newTestHandler(mock)
		result := h.createNAT(context.Background(), testAZ, testVPC)
		if result != "i-new2" {
			t.Errorf("expected i-new2, got %s", result)
		}
	})

	t.Run("no images found uses template default", func(t *testing.T) {
		mock := &mockEC2{}
		setupLTAndAMI(mock)
		mock.DescribeImagesFn = func(ctx context.Context, params *ec2.DescribeImagesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
			return &ec2.DescribeImagesOutput{Images: []ec2types.Image{}}, nil
		}
		mock.RunInstancesFn = func(ctx context.Context, params *ec2.RunInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
			return &ec2.RunInstancesOutput{
				Instances: []ec2types.Instance{{InstanceId: aws.String("i-new3")}},
			}, nil
		}
		h := newTestHandler(mock)
		result := h.createNAT(context.Background(), testAZ, testVPC)
		if result != "i-new3" {
			t.Errorf("expected i-new3, got %s", result)
		}
	})

	t.Run("run instances fails", func(t *testing.T) {
		mock := &mockEC2{}
		setupLTAndAMI(mock)
		mock.DescribeImagesFn = func(ctx context.Context, params *ec2.DescribeImagesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
			return &ec2.DescribeImagesOutput{Images: []ec2types.Image{}}, nil
		}
		mock.RunInstancesFn = func(ctx context.Context, params *ec2.RunInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
			return nil, fmt.Errorf("InsufficientInstanceCapacity: No capacity")
		}
		h := newTestHandler(mock)
		result := h.createNAT(context.Background(), testAZ, testVPC)
		if result != "" {
			t.Errorf("expected empty, got %s", result)
		}
	})
}

// --- startNAT() ---

func TestStartNAT(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-nat1", "stopped", testVPC, testAZ, nil, nil)), nil
		}
		mock.StartInstancesFn = func(ctx context.Context, params *ec2.StartInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
			return &ec2.StartInstancesOutput{}, nil
		}
		h := newTestHandler(mock)
		h.startNAT(context.Background(), &Instance{InstanceID: "i-nat1"}, testAZ)
		if mock.callCount("StartInstances") != 1 {
			t.Error("expected StartInstances to be called")
		}
		// No inline EIP — that's handled by EventBridge now
		if mock.callCount("AllocateAddress") != 0 {
			t.Error("expected no AllocateAddress (EIP managed via EventBridge)")
		}
	})

	t.Run("wait timeout", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-nat1", "stopping", testVPC, testAZ, nil, nil)), nil
		}
		h := newTestHandler(mock)
		h.startNAT(context.Background(), &Instance{InstanceID: "i-nat1"}, testAZ)
		if mock.callCount("StartInstances") != 0 {
			t.Error("expected StartInstances NOT to be called after timeout")
		}
	})
}

// --- stopNAT() ---

func TestStopNAT(t *testing.T) {
	t.Run("happy path just stops", func(t *testing.T) {
		mock := &mockEC2{}
		h := newTestHandler(mock)
		h.stopNAT(context.Background(), &Instance{InstanceID: "i-nat1"})
		if mock.callCount("StopInstances") != 1 {
			t.Error("expected StopInstances to be called")
		}
		// No inline EIP release — that's handled by EventBridge now
		if mock.callCount("DisassociateAddress") != 0 {
			t.Error("expected no DisassociateAddress (EIP managed via EventBridge)")
		}
	})

	t.Run("stop fails", func(t *testing.T) {
		mock := &mockEC2{}
		mock.StopInstancesFn = func(ctx context.Context, params *ec2.StopInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error) {
			return nil, fmt.Errorf("IncorrectInstanceState: Already stopping")
		}
		h := newTestHandler(mock)
		h.stopNAT(context.Background(), &Instance{InstanceID: "i-nat1"})
	})
}

// --- cleanupAll() ---

func TestCleanupAll(t *testing.T) {
	t.Run("terminates instances and releases EIPs", func(t *testing.T) {
		mock := &mockEC2{}
		nat1 := makeTestInstance("i-nat1", "running", testVPC, testAZ, nil, nil)
		nat2 := makeTestInstance("i-nat2", "stopped", testVPC, testAZ, nil, nil)
		var describeIdx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&describeIdx, 1)
			if idx == 1 {
				return describeResponse(nat1, nat2), nil
			}
			return describeResponse(makeTestInstance("i-nat1", "terminated", testVPC, testAZ, nil, nil)), nil
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
		h.cleanupAll(context.Background())
		if mock.callCount("TerminateInstances") != 1 {
			t.Errorf("expected 1 TerminateInstances call, got %d", mock.callCount("TerminateInstances"))
		}
		if mock.callCount("DisassociateAddress") != 1 {
			t.Error("expected DisassociateAddress to be called")
		}
		if mock.callCount("ReleaseAddress") != 1 {
			t.Error("expected ReleaseAddress to be called")
		}
	})

	t.Run("no instances still cleans EIPs", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{
				Addresses: []ec2types.Address{{AllocationId: aws.String("eipalloc-1")}},
			}, nil
		}
		h := newTestHandler(mock)
		h.cleanupAll(context.Background())
		if mock.callCount("ReleaseAddress") != 1 {
			t.Error("expected ReleaseAddress to be called")
		}
	})

	t.Run("no instances no EIPs", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{Addresses: []ec2types.Address{}}, nil
		}
		h := newTestHandler(mock)
		h.cleanupAll(context.Background())
		if mock.callCount("TerminateInstances") != 0 {
			t.Error("expected no TerminateInstances calls")
		}
	})

	t.Run("EIP release failure continues", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(), nil
		}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{
				Addresses: []ec2types.Address{
					{AllocationId: aws.String("eipalloc-1")},
					{AllocationId: aws.String("eipalloc-2")},
				},
			}, nil
		}
		var releaseIdx int32
		mock.ReleaseAddressFn = func(ctx context.Context, params *ec2.ReleaseAddressInput, optFns ...func(*ec2.Options)) (*ec2.ReleaseAddressOutput, error) {
			idx := atomic.AddInt32(&releaseIdx, 1)
			if idx == 1 {
				return nil, fmt.Errorf("InvalidAddress.NotFound: Not found")
			}
			return &ec2.ReleaseAddressOutput{}, nil
		}
		h := newTestHandler(mock)
		h.cleanupAll(context.Background())
		if mock.callCount("ReleaseAddress") != 2 {
			t.Errorf("expected 2 ReleaseAddress calls, got %d", mock.callCount("ReleaseAddress"))
		}
	})
}

// --- isCurrentConfig() ---

func TestIsCurrentConfig(t *testing.T) {
	t.Run("matching config", func(t *testing.T) {
		h := newTestHandler(nil)
		h.ConfigVersion = "abc123"
		inst := &Instance{Tags: []ec2types.Tag{{Key: aws.String("ConfigVersion"), Value: aws.String("abc123")}}}
		if !h.isCurrentConfig(inst) {
			t.Error("expected true")
		}
	})

	t.Run("mismatched config", func(t *testing.T) {
		h := newTestHandler(nil)
		h.ConfigVersion = "abc123"
		inst := &Instance{Tags: []ec2types.Tag{{Key: aws.String("ConfigVersion"), Value: aws.String("old456")}}}
		if h.isCurrentConfig(inst) {
			t.Error("expected false")
		}
	})

	t.Run("no tag assumes current", func(t *testing.T) {
		h := newTestHandler(nil)
		h.ConfigVersion = "abc123"
		inst := &Instance{Tags: []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("nat")}}}
		if !h.isCurrentConfig(inst) {
			t.Error("expected true — missing tag means nothing to compare")
		}
	})

	t.Run("no tags at all assumes current", func(t *testing.T) {
		h := newTestHandler(nil)
		h.ConfigVersion = "abc123"
		inst := &Instance{Tags: []ec2types.Tag{}}
		if !h.isCurrentConfig(inst) {
			t.Error("expected true — missing tag means nothing to compare")
		}
	})

	t.Run("empty config version skips check", func(t *testing.T) {
		h := newTestHandler(nil)
		h.ConfigVersion = ""
		inst := &Instance{Tags: []ec2types.Tag{}}
		if !h.isCurrentConfig(inst) {
			t.Error("expected true")
		}
	})
}

// --- replaceNAT() ---

func TestReplaceNAT(t *testing.T) {
	setupLTAndAMI := func(mock *mockEC2) {
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
	}

	t.Run("happy path", func(t *testing.T) {
		mock := &mockEC2{}
		setupLTAndAMI(mock)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-old", "terminated", testVPC, testAZ, nil, nil)), nil
		}
		mock.DescribeNetworkInterfacesFn = func(ctx context.Context, params *ec2.DescribeNetworkInterfacesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
			return &ec2.DescribeNetworkInterfacesOutput{
				NetworkInterfaces: []ec2types.NetworkInterface{{
					NetworkInterfaceId: aws.String("eni-1"),
					Status:             ec2types.NetworkInterfaceStatusAvailable,
				}},
			}, nil
		}
		h := newTestHandler(mock)
		eni := makeENI("eni-1", 0, "10.0.1.10", nil)
		inst := &Instance{
			InstanceID:        "i-old",
			StateName:         "running",
			NetworkInterfaces: []ec2types.InstanceNetworkInterface{eni},
		}
		result := h.replaceNAT(context.Background(), inst, testAZ, testVPC)
		if result != "i-new" {
			t.Errorf("expected i-new, got %s", result)
		}
		if mock.callCount("TerminateInstances") != 1 {
			t.Error("expected TerminateInstances to be called")
		}
	})

	t.Run("ENI wait polls until available", func(t *testing.T) {
		mock := &mockEC2{}
		setupLTAndAMI(mock)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-old", "terminated", testVPC, testAZ, nil, nil)), nil
		}
		var niIdx int32
		mock.DescribeNetworkInterfacesFn = func(ctx context.Context, params *ec2.DescribeNetworkInterfacesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
			idx := atomic.AddInt32(&niIdx, 1)
			if idx == 1 {
				return &ec2.DescribeNetworkInterfacesOutput{
					NetworkInterfaces: []ec2types.NetworkInterface{
						{NetworkInterfaceId: aws.String("eni-1"), Status: ec2types.NetworkInterfaceStatusInUse},
						{NetworkInterfaceId: aws.String("eni-2"), Status: ec2types.NetworkInterfaceStatusInUse},
					},
				}, nil
			}
			return &ec2.DescribeNetworkInterfacesOutput{
				NetworkInterfaces: []ec2types.NetworkInterface{
					{NetworkInterfaceId: aws.String("eni-1"), Status: ec2types.NetworkInterfaceStatusAvailable},
					{NetworkInterfaceId: aws.String("eni-2"), Status: ec2types.NetworkInterfaceStatusAvailable},
				},
			}, nil
		}
		h := newTestHandler(mock)
		eni1 := makeENI("eni-1", 0, "10.0.1.10", nil)
		eni2 := makeENI("eni-2", 1, "10.0.1.11", nil)
		inst := &Instance{
			InstanceID:        "i-old",
			StateName:         "running",
			NetworkInterfaces: []ec2types.InstanceNetworkInterface{eni1, eni2},
		}
		result := h.replaceNAT(context.Background(), inst, testAZ, testVPC)
		if result != "i-new" {
			t.Errorf("expected i-new, got %s", result)
		}
		if mock.callCount("DescribeNetworkInterfaces") != 2 {
			t.Errorf("expected 2 DescribeNetworkInterfaces calls, got %d", mock.callCount("DescribeNetworkInterfaces"))
		}
	})

	t.Run("no ENIs skips wait", func(t *testing.T) {
		mock := &mockEC2{}
		setupLTAndAMI(mock)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-old", "terminated", testVPC, testAZ, nil, nil)), nil
		}
		h := newTestHandler(mock)
		inst := &Instance{
			InstanceID:        "i-old",
			StateName:         "running",
			NetworkInterfaces: nil,
		}
		result := h.replaceNAT(context.Background(), inst, testAZ, testVPC)
		if result != "i-new" {
			t.Errorf("expected i-new, got %s", result)
		}
		if mock.callCount("DescribeNetworkInterfaces") != 0 {
			t.Error("expected no DescribeNetworkInterfaces calls")
		}
	})
}

// --- createNAT() config tag ---

func TestCreateNATConfigTag(t *testing.T) {
	setupLTAndAMI := func(mock *mockEC2) {
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
	}

	t.Run("includes config version tag", func(t *testing.T) {
		mock := &mockEC2{}
		setupLTAndAMI(mock)
		mock.RunInstancesFn = func(ctx context.Context, params *ec2.RunInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
			if len(params.TagSpecifications) == 0 {
				t.Error("expected TagSpecifications")
			} else {
				found := false
				for _, tag := range params.TagSpecifications[0].Tags {
					if aws.ToString(tag.Key) == "ConfigVersion" && aws.ToString(tag.Value) == "abc123" {
						found = true
					}
				}
				if !found {
					t.Error("expected ConfigVersion tag")
				}
			}
			return &ec2.RunInstancesOutput{
				Instances: []ec2types.Instance{{InstanceId: aws.String("i-tagged")}},
			}, nil
		}
		h := newTestHandler(mock)
		h.ConfigVersion = "abc123"
		h.createNAT(context.Background(), testAZ, testVPC)
	})

	t.Run("no tag when config version empty", func(t *testing.T) {
		mock := &mockEC2{}
		setupLTAndAMI(mock)
		mock.RunInstancesFn = func(ctx context.Context, params *ec2.RunInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
			if len(params.TagSpecifications) != 0 {
				t.Error("expected no TagSpecifications")
			}
			return &ec2.RunInstancesOutput{
				Instances: []ec2types.Instance{{InstanceId: aws.String("i-notag")}},
			}, nil
		}
		h := newTestHandler(mock)
		h.ConfigVersion = ""
		h.createNAT(context.Background(), testAZ, testVPC)
	})
}
