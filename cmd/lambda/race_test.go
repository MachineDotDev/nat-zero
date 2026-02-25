package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

// =============================================================================
// Race Condition Tests
//
// Each TestRace_R* function documents and verifies the behavior of a specific
// race condition identified in the nat-zero Lambda. Race conditions arise from:
//   - Multiple concurrent Lambda invocations from overlapping EventBridge events
//   - EC2 API eventual consistency (state changes not immediately visible)
//
// See docs/ARCHITECTURE.md "Race Conditions" section for the full catalog.
// =============================================================================

// TestRace_R1_StaleSiblingEventualConsistency verifies the retry logic in
// maybeStopNAT when EC2 eventual consistency causes a dying workload to still
// appear as "running" in DescribeInstances.
//
// Race scenario:
//   - Workload i-dying fires shutting-down event
//   - Lambda calls findSiblings, but EC2 API still returns i-dying as "running"
//   - Without mitigation, the NAT would never be stopped
//
// Mitigation: maybeStopNAT excludes the trigger instance ID from siblings AND
// retries up to 3 times with 2s sleep between attempts.
func TestRace_R1_StaleSiblingEventualConsistency(t *testing.T) {
	t.Run("trigger excluded from siblings on first attempt", func(t *testing.T) {
		mock := &mockEC2{}
		workTags := []ec2types.Tag{{Key: aws.String("App"), Value: aws.String("web")}}
		natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
		workInst := makeTestInstance("i-dying", "running", testVPC, testAZ, workTags, nil)
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, nil)

		var callIdx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				// classify: returns the workload instance
				return describeResponse(workInst), nil
			}
			if params.Filters != nil {
				for _, f := range params.Filters {
					if aws.ToString(f.Name) == "tag:nat-zero:managed" {
						return describeResponse(natInst), nil
					}
				}
			}
			// findSiblings: trigger still appears as running (eventual consistency)
			// but should be excluded by excludeID
			return describeResponse(workInst), nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-dying", State: "shutting-down"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StopInstances") != 1 {
			t.Errorf("expected StopInstances=1 (trigger excluded), got %d", mock.callCount("StopInstances"))
		}
	})

	t.Run("other stale sibling clears on retry", func(t *testing.T) {
		mock := &mockEC2{}
		workTags := []ec2types.Tag{{Key: aws.String("App"), Value: aws.String("web")}}
		natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
		workInst := makeTestInstance("i-dying", "stopping", testVPC, testAZ, workTags, nil)
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, nil)
		staleInst := makeTestInstance("i-stale", "running", testVPC, testAZ, workTags, nil)

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
			// First findSiblings call: stale sibling still running
			// Subsequent calls: sibling gone (EC2 caught up)
			if idx <= 3 {
				return describeResponse(staleInst), nil
			}
			return describeResponse(), nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-dying", State: "stopping"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.callCount("StopInstances") != 1 {
			t.Errorf("expected StopInstances=1 after retry succeeds, got %d", mock.callCount("StopInstances"))
		}
	})
}

// TestRace_R2_TerminatedInstanceGoneFromAPI verifies the sweepIdleNATs fallback
// when classify returns ignore=true because the terminated instance has already
// been purged from the EC2 API.
//
// Race scenario:
//   - Workload terminates and EventBridge fires "terminated" event
//   - By the time Lambda calls DescribeInstances, the instance is gone
//   - classify returns ignore=true, normal scale-down path is skipped
//
// Mitigation: handler detects isTerminating(state) + ignore and calls
// sweepIdleNATs to check all NATs in the VPC for idle ones.
func TestRace_R2_TerminatedInstanceGoneFromAPI(t *testing.T) {
	t.Run("sweep stops idle NAT when trigger gone", func(t *testing.T) {
		// Already covered by handler_test.go "terminated event sweeps idle NATs"
		// This variant ensures the sweep mechanism works end-to-end.
		mock := &mockEC2{}
		natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
		natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, nil)
		var callIdx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				// classify: instance gone
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
			t.Errorf("expected sweep to stop idle NAT, got StopInstances=%d", mock.callCount("StopInstances"))
		}
	})

	t.Run("sweep handles multiple NATs across AZs", func(t *testing.T) {
		mock := &mockEC2{}
		natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
		workTags := []ec2types.Tag{{Key: aws.String("App"), Value: aws.String("web")}}
		natAZ1 := makeTestInstance("i-nat-az1", "running", testVPC, "us-east-1a", natTags, nil)
		natAZ2 := makeTestInstance("i-nat-az2", "running", testVPC, "us-east-1b", natTags, nil)
		sibAZ2 := makeTestInstance("i-sib-az2", "running", testVPC, "us-east-1b", workTags, nil)

		var callIdx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				// classify: instance gone
				return describeResponse(), nil
			}
			if params.Filters != nil {
				for _, f := range params.Filters {
					if aws.ToString(f.Name) == "tag:nat-zero:managed" {
						// sweep: both NATs found
						return describeResponse(natAZ1, natAZ2), nil
					}
				}
				// findSiblings: check AZ filter
				for _, f := range params.Filters {
					if aws.ToString(f.Name) == "availability-zone" {
						if f.Values[0] == "us-east-1b" {
							return describeResponse(sibAZ2), nil
						}
						return describeResponse(), nil
					}
				}
			}
			return describeResponse(), nil
		}
		h := newTestHandler(mock)
		err := h.HandleRequest(context.Background(), Event{InstanceID: "i-gone", State: "shutting-down"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Only NAT in AZ1 should be stopped (AZ2 has a sibling)
		if mock.callCount("StopInstances") != 1 {
			t.Errorf("expected 1 StopInstances (only idle NAT), got %d", mock.callCount("StopInstances"))
		}
	})
}

// TestRace_R3_RetryExhaustion verifies the accepted risk when EC2 eventual
// consistency takes longer than the retry budget (3 attempts x 2s = 6s).
//
// Race scenario:
//   - A sibling workload is shutting down but EC2 API never reflects the change
//     within the retry window
//   - findSiblings persistently returns a stale sibling on all 3 attempts
//
// Accepted risk: NAT stays running. The next scale-down event or sweepIdleNATs
// will eventually catch it.
func TestRace_R3_RetryExhaustion(t *testing.T) {
	mock := &mockEC2{}
	workTags := []ec2types.Tag{{Key: aws.String("App"), Value: aws.String("web")}}
	natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
	workInst := makeTestInstance("i-dying", "stopping", testVPC, testAZ, workTags, nil)
	natInst := makeTestInstance("i-nat1", "running", testVPC, testAZ, natTags, nil)
	staleInst := makeTestInstance("i-stale", "running", testVPC, testAZ, workTags, nil)

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
		// All 3 findSiblings attempts: stale sibling persists
		return describeResponse(staleInst), nil
	}
	h := newTestHandler(mock)
	err := h.HandleRequest(context.Background(), Event{InstanceID: "i-dying", State: "stopping"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Accepted risk: NAT kept alive because stale sibling never cleared
	if mock.callCount("StopInstances") != 0 {
		t.Error("expected StopInstances NOT called (retry exhaustion, accepted risk)")
	}
}

// TestRace_R4_DuplicateNATCreation verifies the reactive deduplication in
// findNAT when two concurrent Lambda invocations both create a NAT instance.
//
// Race scenario:
//   - Two workload pending events arrive simultaneously
//   - Both Lambda invocations call findNAT → nil, both call createNAT
//   - Two NAT instances now exist in the same AZ
//
// Mitigation: findNAT detects multiple NATs, keeps the first running one,
// and terminates the extras via TerminateInstances.
func TestRace_R4_DuplicateNATCreation(t *testing.T) {
	t.Run("two running NATs deduplicates to one", func(t *testing.T) {
		mock := &mockEC2{}
		nat1 := makeTestInstance("i-nat1", "running", testVPC, testAZ, nil, nil)
		nat2 := makeTestInstance("i-nat2", "running", testVPC, testAZ, nil, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(nat1, nat2), nil
		}
		h := newTestHandler(mock)
		result := h.findNAT(context.Background(), testAZ, testVPC)
		if result == nil {
			t.Fatal("expected a NAT to be returned")
		}
		if result.InstanceID != "i-nat1" {
			t.Errorf("expected first running NAT i-nat1, got %s", result.InstanceID)
		}
		if mock.callCount("TerminateInstances") != 1 {
			t.Errorf("expected 1 TerminateInstances (extra NAT), got %d", mock.callCount("TerminateInstances"))
		}
	})

	t.Run("running NAT preferred over stopped", func(t *testing.T) {
		mock := &mockEC2{}
		stopped := makeTestInstance("i-stopped", "stopped", testVPC, testAZ, nil, nil)
		running := makeTestInstance("i-running", "running", testVPC, testAZ, nil, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(stopped, running), nil
		}
		h := newTestHandler(mock)
		result := h.findNAT(context.Background(), testAZ, testVPC)
		if result == nil || result.InstanceID != "i-running" {
			t.Errorf("expected running NAT to be kept, got %v", result)
		}
		if mock.callCount("TerminateInstances") != 1 {
			t.Errorf("expected 1 TerminateInstances, got %d", mock.callCount("TerminateInstances"))
		}
	})

	t.Run("three NATs terminates two extras", func(t *testing.T) {
		mock := &mockEC2{}
		nat1 := makeTestInstance("i-nat1", "running", testVPC, testAZ, nil, nil)
		nat2 := makeTestInstance("i-nat2", "running", testVPC, testAZ, nil, nil)
		nat3 := makeTestInstance("i-nat3", "pending", testVPC, testAZ, nil, nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(nat1, nat2, nat3), nil
		}
		h := newTestHandler(mock)
		result := h.findNAT(context.Background(), testAZ, testVPC)
		if result == nil || result.InstanceID != "i-nat1" {
			t.Errorf("expected first running NAT kept, got %v", result)
		}
		if mock.callCount("TerminateInstances") != 2 {
			t.Errorf("expected 2 TerminateInstances (two extras), got %d", mock.callCount("TerminateInstances"))
		}
	})
}

// TestRace_R5_StartStopOverlap verifies behavior when a scale-up event fires
// while a concurrent scale-down is stopping the NAT.
//
// Race scenario:
//   - Scale-down Lambda invocation calls StopInstances on the NAT
//   - New workload pending event fires, Lambda sees NAT in "stopping" state
//   - ensureNAT sees isStopping → calls startNAT
//   - startNAT waits for "stopped" then calls StartInstances
//
// Accepted risk: Brief delay while NAT transitions stopping→stopped→starting.
func TestRace_R5_StartStopOverlap(t *testing.T) {
	t.Run("stopping NAT waits then starts", func(t *testing.T) {
		mock := &mockEC2{}
		workTags := []ec2types.Tag{{Key: aws.String("App"), Value: aws.String("web")}}
		natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}
		workInst := makeTestInstance("i-work1", "pending", testVPC, testAZ, workTags, nil)
		stoppingNAT := makeTestInstance("i-nat1", "stopping", testVPC, testAZ, natTags, nil)
		stoppedNAT := makeTestInstance("i-nat1", "stopped", testVPC, testAZ, natTags, nil)

		var callIdx int32
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				// classify: workload instance
				return describeResponse(workInst), nil
			}
			if params.Filters != nil {
				// findNAT: NAT is stopping
				return describeResponse(stoppingNAT), nil
			}
			// waitForState in startNAT: first call returns stopping, second returns stopped
			if idx <= 3 {
				return describeResponse(stoppingNAT), nil
			}
			return describeResponse(stoppedNAT), nil
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
			t.Errorf("expected StartInstances=1 (NAT restarted after stop), got %d", mock.callCount("StartInstances"))
		}
	})
}

// TestRace_R6_DoubleEIPAllocation verifies the race-detection re-check in
// attachEIP when two concurrent Lambda invocations (from pending + running
// events) both try to allocate an EIP for the same NAT.
//
// Race scenario:
//   - NAT pending event → Lambda A calls attachEIP, allocates EIP-A
//   - NAT running event → Lambda B calls attachEIP, allocates EIP-B
//   - Both try to associate to the same ENI
//
// Mitigation: After AllocateAddress, attachEIP re-checks the ENI via
// DescribeNetworkInterfaces. If another EIP is already associated, it releases
// the duplicate allocation.
func TestRace_R6_DoubleEIPAllocation(t *testing.T) {
	t.Run("re-check detects existing EIP and releases duplicate", func(t *testing.T) {
		mock := &mockEC2{}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-nat1", "running", testVPC, testAZ, nil, []ec2types.InstanceNetworkInterface{eni})), nil
		}
		mock.AllocateAddressFn = func(ctx context.Context, params *ec2.AllocateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AllocateAddressOutput, error) {
			return &ec2.AllocateAddressOutput{AllocationId: aws.String("eipalloc-dup"), PublicIp: aws.String("2.2.2.2")}, nil
		}
		// Re-check: another invocation already attached an EIP
		mock.DescribeNetworkInterfacesFn = func(ctx context.Context, params *ec2.DescribeNetworkInterfacesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
			return &ec2.DescribeNetworkInterfacesOutput{
				NetworkInterfaces: []ec2types.NetworkInterface{{
					NetworkInterfaceId: aws.String("eni-pub1"),
					Association: &ec2types.NetworkInterfaceAssociation{
						PublicIp: aws.String("1.1.1.1"), // already attached by other invocation
					},
				}},
			}, nil
		}
		h := newTestHandler(mock)
		h.attachEIP(context.Background(), "i-nat1", testAZ)

		if mock.callCount("AssociateAddress") != 0 {
			t.Error("expected no AssociateAddress (race detected)")
		}
		if mock.callCount("ReleaseAddress") != 1 {
			t.Errorf("expected ReleaseAddress=1 (duplicate released), got %d", mock.callCount("ReleaseAddress"))
		}
	})

	t.Run("associate fails also releases duplicate", func(t *testing.T) {
		mock := &mockEC2{}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-nat1", "running", testVPC, testAZ, nil, []ec2types.InstanceNetworkInterface{eni})), nil
		}
		mock.AllocateAddressFn = func(ctx context.Context, params *ec2.AllocateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AllocateAddressOutput, error) {
			return &ec2.AllocateAddressOutput{AllocationId: aws.String("eipalloc-dup"), PublicIp: aws.String("2.2.2.2")}, nil
		}
		// Re-check: no EIP yet (race window)
		mock.DescribeNetworkInterfacesFn = func(ctx context.Context, params *ec2.DescribeNetworkInterfacesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
			return &ec2.DescribeNetworkInterfacesOutput{
				NetworkInterfaces: []ec2types.NetworkInterface{{
					NetworkInterfaceId: aws.String("eni-pub1"),
				}},
			}, nil
		}
		// Associate fails (e.g. other invocation won the race)
		mock.AssociateAddressFn = func(ctx context.Context, params *ec2.AssociateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AssociateAddressOutput, error) {
			return nil, fmt.Errorf("Resource.AlreadyAssociated: EIP already associated")
		}
		h := newTestHandler(mock)
		h.attachEIP(context.Background(), "i-nat1", testAZ)

		if mock.callCount("ReleaseAddress") != 1 {
			t.Errorf("expected ReleaseAddress=1 (orphaned alloc released), got %d", mock.callCount("ReleaseAddress"))
		}
	})
}

// TestRace_R7_AssociateFailsAfterRecheck verifies that when the re-check shows
// no EIP but AssociateAddress still fails (another invocation raced between
// re-check and associate), the allocated EIP is properly released.
//
// Race scenario:
//   - Lambda A: AllocateAddress → re-check ENI → no EIP → AssociateAddress
//   - Lambda B: between A's re-check and associate, B associates its own EIP
//   - Lambda A: AssociateAddress fails
//
// Mitigation: attachEIP releases the allocated EIP on AssociateAddress failure.
func TestRace_R7_AssociateFailsAfterRecheck(t *testing.T) {
	mock := &mockEC2{}
	eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
	mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
		return describeResponse(makeTestInstance("i-nat1", "running", testVPC, testAZ, nil, []ec2types.InstanceNetworkInterface{eni})), nil
	}
	mock.AllocateAddressFn = func(ctx context.Context, params *ec2.AllocateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AllocateAddressOutput, error) {
		return &ec2.AllocateAddressOutput{AllocationId: aws.String("eipalloc-orphan"), PublicIp: aws.String("3.3.3.3")}, nil
	}
	// Re-check: no EIP (race window still open)
	mock.DescribeNetworkInterfacesFn = func(ctx context.Context, params *ec2.DescribeNetworkInterfacesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
		return &ec2.DescribeNetworkInterfacesOutput{
			NetworkInterfaces: []ec2types.NetworkInterface{{
				NetworkInterfaceId: aws.String("eni-pub1"),
			}},
		}, nil
	}
	// Associate fails: another invocation raced us
	mock.AssociateAddressFn = func(ctx context.Context, params *ec2.AssociateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AssociateAddressOutput, error) {
		return nil, fmt.Errorf("InvalidParameterValue: EIP already in use")
	}
	h := newTestHandler(mock)
	h.attachEIP(context.Background(), "i-nat1", testAZ)

	if mock.callCount("ReleaseAddress") != 1 {
		t.Errorf("expected ReleaseAddress=1 (orphaned allocation), got %d", mock.callCount("ReleaseAddress"))
	}
}

// apiError implements smithy.APIError for test use.
type apiError struct {
	code    string
	message string
}

func (e *apiError) Error() string            { return e.message }
func (e *apiError) ErrorCode() string        { return e.code }
func (e *apiError) ErrorMessage() string     { return e.message }
func (e *apiError) ErrorFault() smithy.ErrorFault { return smithy.FaultServer }

// Ensure apiError satisfies the smithy.APIError interface.
var _ smithy.APIError = (*apiError)(nil)

// TestRace_R8_DisassociateAlreadyRemoved verifies that detachEIP handles the
// case where EC2 auto-disassociates the EIP when the instance stops, before
// Lambda's detachEIP runs.
//
// Race scenario:
//   - NAT instance stops, EC2 auto-disassociates the EIP from the ENI
//   - Lambda's detachEIP fires, gets stale association data from DescribeNetworkInterfaces
//   - DisassociateAddress returns InvalidAssociationID.NotFound
//
// Mitigation: detachEIP catches InvalidAssociationID.NotFound and still proceeds
// to release the EIP allocation.
func TestRace_R8_DisassociateAlreadyRemoved(t *testing.T) {
	mock := &mockEC2{}
	eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
	mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
		return describeResponse(makeTestInstance("i-nat1", "stopped", testVPC, testAZ, nil, []ec2types.InstanceNetworkInterface{eni})), nil
	}
	// ENI still shows stale association data
	mock.DescribeNetworkInterfacesFn = func(ctx context.Context, params *ec2.DescribeNetworkInterfacesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
		return &ec2.DescribeNetworkInterfacesOutput{
			NetworkInterfaces: []ec2types.NetworkInterface{{
				NetworkInterfaceId: aws.String("eni-pub1"),
				Association: &ec2types.NetworkInterfaceAssociation{
					AssociationId: aws.String("eipassoc-stale"),
					AllocationId:  aws.String("eipalloc-1"),
					PublicIp:      aws.String("1.2.3.4"),
				},
			}},
		}, nil
	}
	// Disassociate fails: EC2 already removed it
	mock.DisassociateAddressFn = func(ctx context.Context, params *ec2.DisassociateAddressInput, optFns ...func(*ec2.Options)) (*ec2.DisassociateAddressOutput, error) {
		return nil, &apiError{code: "InvalidAssociationID.NotFound", message: "Association not found"}
	}
	mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
		return &ec2.DescribeAddressesOutput{Addresses: []ec2types.Address{}}, nil
	}
	h := newTestHandler(mock)
	h.detachEIP(context.Background(), "i-nat1", testAZ)

	if mock.callCount("DisassociateAddress") != 1 {
		t.Errorf("expected DisassociateAddress=1 (attempted), got %d", mock.callCount("DisassociateAddress"))
	}
	// Critical: ReleaseAddress must still be called despite disassociate "failure"
	if mock.callCount("ReleaseAddress") != 1 {
		t.Errorf("expected ReleaseAddress=1 (EIP freed despite NotFound), got %d", mock.callCount("ReleaseAddress"))
	}
}

// TestRace_R9_DisassociateNonNotFoundError verifies the current behavior when
// DisassociateAddress fails with a non-NotFound error (e.g. throttling).
//
// Race scenario:
//   - Lambda calls DisassociateAddress but gets throttled
//   - detachEIP returns early without releasing the EIP allocation
//
// UNMITIGATED: The EIP is leaked. However, the orphan sweep in a subsequent
// detachEIP invocation will clean it up.
func TestRace_R9_DisassociateNonNotFoundError(t *testing.T) {
	t.Run("throttle error skips release (documents gap)", func(t *testing.T) {
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
		// DisassociateAddress fails with throttle (not NotFound)
		mock.DisassociateAddressFn = func(ctx context.Context, params *ec2.DisassociateAddressInput, optFns ...func(*ec2.Options)) (*ec2.DisassociateAddressOutput, error) {
			return nil, fmt.Errorf("Throttling: Rate exceeded")
		}
		h := newTestHandler(mock)
		h.detachEIP(context.Background(), "i-nat1", testAZ)

		// Current behavior: returns early, ReleaseAddress NOT called (the gap)
		if mock.callCount("ReleaseAddress") != 0 {
			t.Error("expected ReleaseAddress=0 (current behavior: early return on non-NotFound error)")
		}
		// Orphan sweep also skipped because we returned early
		if mock.callCount("DescribeAddresses") != 0 {
			t.Error("expected DescribeAddresses=0 (orphan sweep skipped due to early return)")
		}
	})

	t.Run("orphan sweep cleans up on next successful detach", func(t *testing.T) {
		mock := &mockEC2{}
		eni := makeENI("eni-pub1", 0, "10.0.1.10", nil)
		mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
			return describeResponse(makeTestInstance("i-nat1", "stopped", testVPC, testAZ, nil, []ec2types.InstanceNetworkInterface{eni})), nil
		}
		// No current association (already gone)
		mock.DescribeNetworkInterfacesFn = func(ctx context.Context, params *ec2.DescribeNetworkInterfacesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
			return &ec2.DescribeNetworkInterfacesOutput{
				NetworkInterfaces: []ec2types.NetworkInterface{{
					NetworkInterfaceId: aws.String("eni-pub1"),
				}},
			}, nil
		}
		// Orphan sweep finds the leaked EIP from previous failed detach
		mock.DescribeAddressesFn = func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
			return &ec2.DescribeAddressesOutput{
				Addresses: []ec2types.Address{{
					AllocationId: aws.String("eipalloc-leaked"),
				}},
			}, nil
		}
		h := newTestHandler(mock)
		h.detachEIP(context.Background(), "i-nat1", testAZ)

		// Orphan sweep cleans up the leaked EIP
		if mock.callCount("ReleaseAddress") != 1 {
			t.Errorf("expected ReleaseAddress=1 (orphan sweep), got %d", mock.callCount("ReleaseAddress"))
		}
	})
}

// TestRace_R10_ENIAvailabilityTimeout verifies the behavior when ENIs never
// reach "available" status during replaceNAT, e.g. due to EC2 API delays.
//
// Race scenario:
//   - replaceNAT terminates old NAT and waits for ENIs to become "available"
//   - DescribeNetworkInterfaces keeps returning "in-use" (EC2 delay)
//   - Wait loop exhausts all 60 iterations
//
// Accepted risk: createNAT proceeds anyway. The launch template may fail to
// attach the ENI, but the next workload event will retry.
func TestRace_R10_ENIAvailabilityTimeout(t *testing.T) {
	mock := &mockEC2{}
	natTags := []ec2types.Tag{{Key: aws.String("nat-zero:managed"), Value: aws.String("true")}}

	mock.DescribeInstancesFn = func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
		// All getInstance calls return terminated (waitForTermination succeeds immediately)
		return describeResponse(makeTestInstance("i-old", "terminated", testVPC, testAZ, natTags, nil)), nil
	}
	// ENI never becomes available (always in-use)
	mock.DescribeNetworkInterfacesFn = func(ctx context.Context, params *ec2.DescribeNetworkInterfacesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
		return &ec2.DescribeNetworkInterfacesOutput{
			NetworkInterfaces: []ec2types.NetworkInterface{
				{NetworkInterfaceId: aws.String("eni-1"), Status: ec2types.NetworkInterfaceStatusInUse},
			},
		}, nil
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
	eni := makeENI("eni-1", 0, "10.0.1.10", nil)
	inst := &Instance{
		InstanceID:        "i-old",
		StateName:         "running",
		NetworkInterfaces: []ec2types.InstanceNetworkInterface{eni},
	}
	result := h.replaceNAT(context.Background(), inst, testAZ, testVPC)

	// createNAT still called despite ENI timeout (accepted risk)
	if result != "i-new" {
		t.Errorf("expected createNAT to proceed despite ENI timeout, got %q", result)
	}
	if mock.callCount("RunInstances") != 1 {
		t.Errorf("expected RunInstances=1 (createNAT proceeded), got %d", mock.callCount("RunInstances"))
	}
	// ENI wait should have polled all 60 iterations
	if mock.callCount("DescribeNetworkInterfaces") < 2 {
		t.Errorf("expected multiple DescribeNetworkInterfaces polls, got %d", mock.callCount("DescribeNetworkInterfaces"))
	}
}
