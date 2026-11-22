package main

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// noWorkloadsMock serves one NAT (in natState) and no workloads in testAZ.
func noWorkloadsMock(natState string) *mockEC2 {
	natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
	workInst := makeTestInstance("i-work1", "terminated", testVPC, testAZ, nil, nil)
	natInst := makeTestInstance("i-nat1", natState, testVPC, testAZ, natTags, nil)
	mock := &mockEC2{}
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
	mock.DescribeLaunchTemplatesFn = func(ctx context.Context, params *ec2.DescribeLaunchTemplatesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplatesOutput, error) {
		return &ec2.DescribeLaunchTemplatesOutput{LaunchTemplates: []ec2types.LaunchTemplate{{
			Tags: []ec2types.Tag{{Key: aws.String("AvailabilityZone"), Value: aws.String(testAZ)}},
		}}}, nil
	}
	return mock
}

func TestAlwaysOnAZs(t *testing.T) {
	t.Run("always-on AZ keeps a running NAT with no workloads", func(t *testing.T) {
		mock := noWorkloadsMock("running")
		h := newTestHandler(mock)
		h.AlwaysOnAZs = map[string]bool{testAZ: true}
		if err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "terminated"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StopInstances") != 0 {
			t.Errorf("expected no StopInstances in an always-on AZ, got %d", mock.callCount("StopInstances"))
		}
	})

	t.Run("always-on AZ starts a stopped NAT with no workloads", func(t *testing.T) {
		mock := noWorkloadsMock("stopped")
		h := newTestHandler(mock)
		h.AlwaysOnAZs = map[string]bool{testAZ: true}
		if err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "terminated"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StartInstances") != 1 {
			t.Errorf("expected StartInstances=1, got %d", mock.callCount("StartInstances"))
		}
	})

	t.Run("other AZs still scale to zero", func(t *testing.T) {
		mock := noWorkloadsMock("running")
		h := newTestHandler(mock)
		h.AlwaysOnAZs = map[string]bool{"some-other-az": true}
		if err := h.HandleRequest(context.Background(), Event{InstanceID: "i-work1", State: "terminated"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StopInstances") != 1 {
			t.Errorf("expected StopInstances=1, got %d", mock.callCount("StopInstances"))
		}
	})

	t.Run("sweep action converges an always-on AZ", func(t *testing.T) {
		mock := noWorkloadsMock("stopped")
		h := newTestHandler(mock)
		h.AlwaysOnAZs = map[string]bool{testAZ: true}
		if err := h.HandleRequest(context.Background(), Event{Action: "sweep"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StartInstances") != 1 {
			t.Errorf("expected the sweep to start the NAT, got StartInstances=%d", mock.callCount("StartInstances"))
		}
	})
}

func TestParseAZSet(t *testing.T) {
	got := parseAZSet(" us-east-1a, us-east-1d ,,")
	if len(got) != 2 || !got["us-east-1a"] || !got["us-east-1d"] {
		t.Errorf("unexpected set %v", got)
	}
	if len(parseAZSet("")) != 0 {
		t.Error("empty input should give an empty set")
	}
}
