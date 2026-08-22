package main

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// Single-NAT mode: the one NAT lives in SingleInstanceAZ and serves every
// private route table, so reconciliation must always target that AZ and count
// workloads VPC-wide. These tests pin the two failure modes that per-AZ
// scoping would reintroduce: a workload outside the NAT's AZ failing to wake
// it, and the NAT being stopped while another AZ still has workloads.

const otherAZ = "us-east-1b"

func natTags() []ec2types.Tag {
	return []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
}

func newSingleModeHandler(mock *mockEC2) *Handler {
	h := newTestHandler(mock)
	h.SingleInstanceAZ = testAZ
	return h
}

// hasFilter returns the filter values for name, or nil if absent.
func filterValues(params *ec2.DescribeInstancesInput, name string) []string {
	for _, f := range params.Filters {
		if aws.ToString(f.Name) == name {
			return f.Values
		}
	}
	return nil
}

func TestSingleInstanceWorkloadInOtherAZWakesNAT(t *testing.T) {
	mock := &mockEC2{}
	workload := makeTestInstance("i-work", "running", testVPC, otherAZ, nil, nil)
	nat := makeTestInstance("i-nat", "stopped", testVPC, testAZ, natTags(), nil)

	var workloadScanAZFilter []string
	mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
		if len(params.InstanceIds) > 0 {
			// resolveAZ lookup of the trigger instance
			return describeResponse(workload), nil
		}
		if filterValues(params, "tag:nat-zero:managed") != nil {
			// findNATs — must be queried in the single-instance AZ
			if got := filterValues(params, "availability-zone"); len(got) != 1 || got[0] != testAZ {
				t.Errorf("findNATs AZ filter = %v, want [%s]", got, testAZ)
			}
			return describeResponse(nat), nil
		}
		// findWorkloads — must NOT be AZ-filtered in single mode
		workloadScanAZFilter = filterValues(params, "availability-zone")
		return describeResponse(workload), nil
	}
	mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
		return &ec2.DescribeAddressesOutput{}, nil
	}
	started := false
	mock.StartInstancesFn = func(ctx context.Context, params *ec2.StartInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
		if len(params.InstanceIds) == 1 && params.InstanceIds[0] == "i-nat" {
			started = true
		}
		return &ec2.StartInstancesOutput{}, nil
	}

	h := newSingleModeHandler(mock)
	if err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work", State: "running"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if workloadScanAZFilter != nil {
		t.Errorf("findWorkloads used AZ filter %v in single mode; want VPC-wide scan", workloadScanAZFilter)
	}
	if !started {
		t.Fatal("workload in another AZ did not start the single NAT")
	}
}

func TestSingleInstanceNATNotStoppedWhileOtherAZBusy(t *testing.T) {
	mock := &mockEC2{}
	// Trigger: a workload in the NAT's own AZ just terminated…
	deadWorkload := makeTestInstance("i-dead", "terminated", testVPC, testAZ, nil, nil)
	// …but another AZ still has a running workload.
	liveWorkload := makeTestInstance("i-live", "running", testVPC, otherAZ, nil, nil)
	nat := makeTestInstance("i-nat", "running", testVPC, testAZ, natTags(), nil)

	mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
		if len(params.InstanceIds) > 0 {
			return describeResponse(deadWorkload), nil
		}
		if filterValues(params, "tag:nat-zero:managed") != nil {
			return describeResponse(nat), nil
		}
		return describeResponse(liveWorkload), nil
	}
	mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
		return &ec2.DescribeAddressesOutput{Addresses: []ec2types.Address{{AllocationId: aws.String("eipalloc-1")}}}, nil
	}
	mock.StopInstancesFn = func(ctx context.Context, params *ec2.StopInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error) {
		t.Fatalf("NAT stopped while a workload is still running in another AZ")
		return &ec2.StopInstancesOutput{}, nil
	}

	h := newSingleModeHandler(mock)
	if err := h.HandleRequest(context.Background(), Event{InstanceID: "i-dead", State: "terminated"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
