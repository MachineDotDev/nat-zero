package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

// --- resolveAZ() ---

func TestResolveAZUnit(t *testing.T) {
	t.Run("instance not found", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(), nil
		}
		h := newTestHandler(mock)
		inst, az, vpc := h.resolveAZ(context.Background(), "i-gone")
		if inst != nil || az != "" || vpc != "" {
			t.Errorf("expected (nil, '', ''), got (%v, %q, %q)", inst, az, vpc)
		}
	})

	t.Run("wrong VPC", func(t *testing.T) {
		mock := &mockEC2{}
		inst := makeTestInstance("i-other", "running", "vpc-other", testAZ, nil, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(inst), nil
		}
		h := newTestHandler(mock)
		gotInst, az, vpc := h.resolveAZ(context.Background(), "i-other")
		if gotInst != nil || az != "" || vpc != "" {
			t.Errorf("expected (nil, '', ''), got (%v, %q, %q)", gotInst, az, vpc)
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
		gotInst, az, vpc := h.resolveAZ(context.Background(), "i-ign")
		if gotInst != nil || az != "" || vpc != "" {
			t.Errorf("expected (nil, '', ''), got (%v, %q, %q)", gotInst, az, vpc)
		}
	})

	t.Run("NAT instance resolves normally", func(t *testing.T) {
		mock := &mockEC2{}
		inst := makeTestInstance("i-nat", "running", testVPC, testAZ,
			[]ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(inst), nil
		}
		h := newTestHandler(mock)
		gotInst, az, vpc := h.resolveAZ(context.Background(), "i-nat")
		if gotInst == nil || az != testAZ || vpc != testVPC {
			t.Errorf("expected (inst, %q, %q), got (%v, %q, %q)", testAZ, testVPC, gotInst, az, vpc)
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
		gotInst, az, vpc := h.resolveAZ(context.Background(), "i-work")
		if gotInst == nil || az != testAZ || vpc != testVPC {
			t.Errorf("expected (inst, %q, %q), got (%v, %q, %q)", testAZ, testVPC, gotInst, az, vpc)
		}
	})
}

// --- findWorkloads() ---

func TestFindWorkloads(t *testing.T) {
	t.Run("returns workload instances", func(t *testing.T) {
		mock := &mockEC2{}
		work := makeTestInstance("i-work", "running", testVPC, testAZ,
			[]ec2types.Tag{{Key: aws.String("App"), Value: aws.String("api")}}, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(work), nil
		}
		h := newTestHandler(mock)
		wl := h.findWorkloads(context.Background(), testAZ, testVPC)
		if len(wl) != 1 || wl[0].InstanceID != "i-work" {
			t.Errorf("expected [i-work], got %v", wl)
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
		wl := h.findWorkloads(context.Background(), testAZ, testVPC)
		if len(wl) != 1 || wl[0].InstanceID != "i-work" {
			t.Errorf("expected [i-work], got %v", wl)
		}
	})

	t.Run("no workloads", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(), nil
		}
		h := newTestHandler(mock)
		wl := h.findWorkloads(context.Background(), testAZ, testVPC)
		if len(wl) != 0 {
			t.Errorf("expected 0 workloads, got %d", len(wl))
		}
	})
}

// --- findNATs() ---

func TestFindNATs(t *testing.T) {
	t.Run("no NATs", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(), nil
		}
		h := newTestHandler(mock)
		nats := h.findNATs(context.Background(), testAZ, testVPC)
		if len(nats) != 0 {
			t.Errorf("expected 0, got %d", len(nats))
		}
	})

	t.Run("single NAT", func(t *testing.T) {
		mock := &mockEC2{}
		nat := makeTestInstance("i-nat1", "running", testVPC, testAZ, nil, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(nat), nil
		}
		h := newTestHandler(mock)
		nats := h.findNATs(context.Background(), testAZ, testVPC)
		if len(nats) != 1 || nats[0].InstanceID != "i-nat1" {
			t.Errorf("expected [i-nat1], got %v", nats)
		}
	})

	t.Run("multiple NATs", func(t *testing.T) {
		mock := &mockEC2{}
		nat1 := makeTestInstance("i-nat1", "running", testVPC, testAZ, nil, nil)
		nat2 := makeTestInstance("i-nat2", "stopped", testVPC, testAZ, nil, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(nat1, nat2), nil
		}
		h := newTestHandler(mock)
		nats := h.findNATs(context.Background(), testAZ, testVPC)
		if len(nats) != 2 {
			t.Errorf("expected 2 NATs, got %d", len(nats))
		}
	})
}

// --- findEIPs() ---

func TestFindEIPs(t *testing.T) {
	t.Run("no EIPs", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{}, nil
		}
		h := newTestHandler(mock)
		eips := h.findEIPs(context.Background(), testAZ)
		if len(eips) != 0 {
			t.Errorf("expected 0, got %d", len(eips))
		}
	})

	t.Run("returns tagged EIPs", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{
				Addresses: []ec2types.Address{
					{AllocationId: aws.String("eipalloc-1")},
					{AllocationId: aws.String("eipalloc-2")},
				},
			}, nil
		}
		h := newTestHandler(mock)
		eips := h.findEIPs(context.Background(), testAZ)
		if len(eips) != 2 {
			t.Errorf("expected 2, got %d", len(eips))
		}
	})
}

// --- terminateDuplicateNATs() ---

func TestTerminateDuplicateNATs(t *testing.T) {
	t.Run("keeps running terminates others", func(t *testing.T) {
		mock := &mockEC2{}
		h := newTestHandler(mock)
		nat1 := &Instance{InstanceID: "i-nat1", StateName: "running"}
		nat2 := &Instance{InstanceID: "i-nat2", StateName: "stopped"}
		nat3 := &Instance{InstanceID: "i-nat3", StateName: "pending"}
		result := h.terminateDuplicateNATs(context.Background(), []*Instance{nat1, nat2, nat3})
		if len(result) != 1 || result[0].InstanceID != "i-nat1" {
			t.Errorf("expected [i-nat1], got %v", result)
		}
		if mock.callCount("TerminateInstances") != 2 {
			t.Errorf("expected 2 TerminateInstances, got %d", mock.callCount("TerminateInstances"))
		}
	})

	t.Run("no running keeps first", func(t *testing.T) {
		mock := &mockEC2{}
		h := newTestHandler(mock)
		nat1 := &Instance{InstanceID: "i-s1", StateName: "stopped"}
		nat2 := &Instance{InstanceID: "i-s2", StateName: "stopped"}
		result := h.terminateDuplicateNATs(context.Background(), []*Instance{nat1, nat2})
		if len(result) != 1 || result[0].InstanceID != "i-s1" {
			t.Errorf("expected [i-s1], got %v", result)
		}
		if mock.callCount("TerminateInstances") != 1 {
			t.Errorf("expected 1 TerminateInstances, got %d", mock.callCount("TerminateInstances"))
		}
	})
}

// --- allocateAndAttachEIP() ---

func TestAllocateAndAttachEIP(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		mock := &mockEC2{}
		mock.AllocateAddressFn = func(ctx context.Context, params *ec2.AllocateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AllocateAddressOutput, error) {
			return &ec2.AllocateAddressOutput{AllocationId: aws.String("eipalloc-1"), PublicIp: aws.String("1.2.3.4")}, nil
		}
		mock.AssociateAddressFn = func(ctx context.Context, params *ec2.AssociateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AssociateAddressOutput, error) {
			return &ec2.AssociateAddressOutput{}, nil
		}
		h := newTestHandler(mock)
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		nat := &Instance{InstanceID: "i-nat1", NetworkInterfaces: []ec2types.InstanceNetworkInterface{eni}}
		h.allocateAndAttachEIP(context.Background(), nat, testAZ)
		if mock.callCount("AllocateAddress") != 1 {
			t.Error("expected AllocateAddress")
		}
		if mock.callCount("AssociateAddress") != 1 {
			t.Error("expected AssociateAddress")
		}
	})

	t.Run("no public ENI", func(t *testing.T) {
		mock := &mockEC2{}
		h := newTestHandler(mock)
		nat := &Instance{InstanceID: "i-nat1", NetworkInterfaces: nil}
		h.allocateAndAttachEIP(context.Background(), nat, testAZ)
		if mock.callCount("AllocateAddress") != 0 {
			t.Error("expected no AllocateAddress when no ENI")
		}
	})

	t.Run("ENI already has EIP", func(t *testing.T) {
		mock := &mockEC2{}
		h := newTestHandler(mock)
		assoc := &ec2types.InstanceNetworkInterfaceAssociation{PublicIp: aws.String("5.6.7.8")}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", assoc)
		nat := &Instance{InstanceID: "i-nat1", NetworkInterfaces: []ec2types.InstanceNetworkInterface{eni}}
		h.allocateAndAttachEIP(context.Background(), nat, testAZ)
		if mock.callCount("AllocateAddress") != 0 {
			t.Error("expected no AllocateAddress when ENI already has EIP")
		}
	})

	t.Run("allocation fails", func(t *testing.T) {
		mock := &mockEC2{}
		mock.AllocateAddressFn = func(ctx context.Context, params *ec2.AllocateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AllocateAddressOutput, error) {
			return nil, fmt.Errorf("AddressLimitExceeded: Too many EIPs")
		}
		h := newTestHandler(mock)
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		nat := &Instance{InstanceID: "i-nat1", NetworkInterfaces: []ec2types.InstanceNetworkInterface{eni}}
		h.allocateAndAttachEIP(context.Background(), nat, testAZ)
		if mock.callCount("AssociateAddress") != 0 {
			t.Error("expected no AssociateAddress after allocation failure")
		}
	})

	t.Run("association fails releases EIP", func(t *testing.T) {
		mock := &mockEC2{}
		mock.AllocateAddressFn = func(ctx context.Context, params *ec2.AllocateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AllocateAddressOutput, error) {
			return &ec2.AllocateAddressOutput{AllocationId: aws.String("eipalloc-1"), PublicIp: aws.String("1.2.3.4")}, nil
		}
		mock.AssociateAddressFn = func(ctx context.Context, params *ec2.AssociateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AssociateAddressOutput, error) {
			return nil, fmt.Errorf("InvalidParameterValue: Bad param")
		}
		h := newTestHandler(mock)
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		nat := &Instance{InstanceID: "i-nat1", NetworkInterfaces: []ec2types.InstanceNetworkInterface{eni}}
		h.allocateAndAttachEIP(context.Background(), nat, testAZ)
		if mock.callCount("ReleaseAddress") != 1 {
			t.Errorf("expected ReleaseAddress=1, got %d", mock.callCount("ReleaseAddress"))
		}
	})
}

// --- releaseEIPs() ---

func TestReleaseEIPs(t *testing.T) {
	t.Run("releases with disassociate", func(t *testing.T) {
		mock := &mockEC2{}
		h := newTestHandler(mock)
		eips := []ec2types.Address{{
			AllocationId:  aws.String("eipalloc-1"),
			AssociationId: aws.String("eipassoc-1"),
		}}
		h.releaseEIPs(context.Background(), eips)
		if mock.callCount("DisassociateAddress") != 1 {
			t.Error("expected DisassociateAddress")
		}
		if mock.callCount("ReleaseAddress") != 1 {
			t.Error("expected ReleaseAddress")
		}
	})

	t.Run("releases without association", func(t *testing.T) {
		mock := &mockEC2{}
		h := newTestHandler(mock)
		eips := []ec2types.Address{{AllocationId: aws.String("eipalloc-1")}}
		h.releaseEIPs(context.Background(), eips)
		if mock.callCount("DisassociateAddress") != 0 {
			t.Error("expected no DisassociateAddress")
		}
		if mock.callCount("ReleaseAddress") != 1 {
			t.Error("expected ReleaseAddress")
		}
	})

	t.Run("handles InvalidAssociationID.NotFound", func(t *testing.T) {
		mock := &mockEC2{}
		mock.DisassociateAddressFn = func(ctx context.Context, params *ec2.DisassociateAddressInput, optFns ...func(*ec2.Options)) (*ec2.DisassociateAddressOutput, error) {
			return nil, &apiError{code: "InvalidAssociationID.NotFound", message: "Not found"}
		}
		h := newTestHandler(mock)
		eips := []ec2types.Address{{
			AllocationId:  aws.String("eipalloc-1"),
			AssociationId: aws.String("eipassoc-stale"),
		}}
		h.releaseEIPs(context.Background(), eips)
		// Should still release despite disassociate NotFound
		if mock.callCount("ReleaseAddress") != 1 {
			t.Errorf("expected ReleaseAddress=1, got %d", mock.callCount("ReleaseAddress"))
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

	t.Run("happy path", func(t *testing.T) {
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

	t.Run("config version tag included", func(t *testing.T) {
		mock := &mockEC2{}
		setupLTAndAMI(mock)
		mock.DescribeImagesFn = func(ctx context.Context, params *ec2.DescribeImagesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
			return &ec2.DescribeImagesOutput{Images: []ec2types.Image{}}, nil
		}
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

	t.Run("empty config version skips check", func(t *testing.T) {
		h := newTestHandler(nil)
		h.ConfigVersion = ""
		inst := &Instance{Tags: []ec2types.Tag{}}
		if !h.isCurrentConfig(inst) {
			t.Error("expected true")
		}
	})
}

// --- cleanupAll() ---

func TestCleanupAll(t *testing.T) {
	t.Run("terminates instances and releases EIPs", func(t *testing.T) {
		mock := &mockEC2{}
		nat1 := makeTestInstance("i-nat1", "running", testVPC, testAZ, nil, nil)
		nat2 := makeTestInstance("i-nat2", "stopped", testVPC, testAZ, nil, nil)
		terminated := false
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			if terminated {
				// After termination, instances are gone (waitForTermination polls this)
				return describeResponse(), nil
			}
			return describeResponse(nat1, nat2), nil
		}
		mock.TerminateInstancesFn = func(ctx context.Context, params *ec2.TerminateInstancesInput, optFns ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
			terminated = true
			return &ec2.TerminateInstancesOutput{}, nil
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
			t.Errorf("expected 1 TerminateInstances, got %d", mock.callCount("TerminateInstances"))
		}
		if mock.callCount("DisassociateAddress") != 1 {
			t.Error("expected DisassociateAddress")
		}
		if mock.callCount("ReleaseAddress") != 1 {
			t.Error("expected ReleaseAddress")
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
			t.Error("expected no TerminateInstances")
		}
	})
}

// --- isErrCode() ---

// apiError implements smithy.APIError for test use.
type apiError struct {
	code    string
	message string
}

func (e *apiError) Error() string                 { return e.message }
func (e *apiError) ErrorCode() string             { return e.code }
func (e *apiError) ErrorMessage() string          { return e.message }
func (e *apiError) ErrorFault() smithy.ErrorFault { return smithy.FaultServer }

var _ smithy.APIError = (*apiError)(nil)

func TestIsErrCode(t *testing.T) {
	t.Run("smithy API error", func(t *testing.T) {
		err := &apiError{code: "InvalidAssociationID.NotFound", message: "not found"}
		if !isErrCode(err, "InvalidAssociationID.NotFound") {
			t.Error("expected true")
		}
		if isErrCode(err, "SomeOtherCode") {
			t.Error("expected false")
		}
	})

	t.Run("string fallback", func(t *testing.T) {
		err := fmt.Errorf("InvalidAssociationID.NotFound: blah")
		if !isErrCode(err, "InvalidAssociationID.NotFound") {
			t.Error("expected true")
		}
	})
}
