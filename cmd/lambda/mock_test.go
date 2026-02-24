package main

import (
	"context"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// mockEC2 implements EC2API with per-method function fields for test control.
type mockEC2 struct {
	DescribeInstancesFn              func(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	RunInstancesFn                   func(ctx context.Context, params *ec2.RunInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error)
	StartInstancesFn                 func(ctx context.Context, params *ec2.StartInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error)
	StopInstancesFn                  func(ctx context.Context, params *ec2.StopInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error)
	TerminateInstancesFn             func(ctx context.Context, params *ec2.TerminateInstancesInput, optFns ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error)
	AllocateAddressFn                func(ctx context.Context, params *ec2.AllocateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AllocateAddressOutput, error)
	AssociateAddressFn               func(ctx context.Context, params *ec2.AssociateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AssociateAddressOutput, error)
	DisassociateAddressFn            func(ctx context.Context, params *ec2.DisassociateAddressInput, optFns ...func(*ec2.Options)) (*ec2.DisassociateAddressOutput, error)
	ReleaseAddressFn                 func(ctx context.Context, params *ec2.ReleaseAddressInput, optFns ...func(*ec2.Options)) (*ec2.ReleaseAddressOutput, error)
	DescribeAddressesFn              func(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error)
	DescribeNetworkInterfacesFn      func(ctx context.Context, params *ec2.DescribeNetworkInterfacesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error)
	DescribeImagesFn                 func(ctx context.Context, params *ec2.DescribeImagesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error)
	DescribeLaunchTemplatesFn        func(ctx context.Context, params *ec2.DescribeLaunchTemplatesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplatesOutput, error)
	DescribeLaunchTemplateVersionsFn func(ctx context.Context, params *ec2.DescribeLaunchTemplateVersionsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplateVersionsOutput, error)

	// Call tracking (mutex-protected for concurrent access)
	mu    sync.Mutex
	Calls []mockCall
}

type mockCall struct {
	Method string
	Input  interface{}
}

func (m *mockEC2) track(method string, input interface{}) {
	m.mu.Lock()
	m.Calls = append(m.Calls, mockCall{Method: method, Input: input})
	m.mu.Unlock()
}

func (m *mockEC2) callCount(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.Calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

func (m *mockEC2) DescribeInstances(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	m.track("DescribeInstances", params)
	if m.DescribeInstancesFn != nil {
		return m.DescribeInstancesFn(ctx, params, optFns...)
	}
	return &ec2.DescribeInstancesOutput{}, nil
}

func (m *mockEC2) RunInstances(ctx context.Context, params *ec2.RunInstancesInput, optFns ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error) {
	m.track("RunInstances", params)
	if m.RunInstancesFn != nil {
		return m.RunInstancesFn(ctx, params, optFns...)
	}
	return &ec2.RunInstancesOutput{}, nil
}

func (m *mockEC2) StartInstances(ctx context.Context, params *ec2.StartInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
	m.track("StartInstances", params)
	if m.StartInstancesFn != nil {
		return m.StartInstancesFn(ctx, params, optFns...)
	}
	return &ec2.StartInstancesOutput{}, nil
}

func (m *mockEC2) StopInstances(ctx context.Context, params *ec2.StopInstancesInput, optFns ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error) {
	m.track("StopInstances", params)
	if m.StopInstancesFn != nil {
		return m.StopInstancesFn(ctx, params, optFns...)
	}
	return &ec2.StopInstancesOutput{}, nil
}

func (m *mockEC2) TerminateInstances(ctx context.Context, params *ec2.TerminateInstancesInput, optFns ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	m.track("TerminateInstances", params)
	if m.TerminateInstancesFn != nil {
		return m.TerminateInstancesFn(ctx, params, optFns...)
	}
	return &ec2.TerminateInstancesOutput{}, nil
}

func (m *mockEC2) AllocateAddress(ctx context.Context, params *ec2.AllocateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AllocateAddressOutput, error) {
	m.track("AllocateAddress", params)
	if m.AllocateAddressFn != nil {
		return m.AllocateAddressFn(ctx, params, optFns...)
	}
	return &ec2.AllocateAddressOutput{}, nil
}

func (m *mockEC2) AssociateAddress(ctx context.Context, params *ec2.AssociateAddressInput, optFns ...func(*ec2.Options)) (*ec2.AssociateAddressOutput, error) {
	m.track("AssociateAddress", params)
	if m.AssociateAddressFn != nil {
		return m.AssociateAddressFn(ctx, params, optFns...)
	}
	return &ec2.AssociateAddressOutput{}, nil
}

func (m *mockEC2) DisassociateAddress(ctx context.Context, params *ec2.DisassociateAddressInput, optFns ...func(*ec2.Options)) (*ec2.DisassociateAddressOutput, error) {
	m.track("DisassociateAddress", params)
	if m.DisassociateAddressFn != nil {
		return m.DisassociateAddressFn(ctx, params, optFns...)
	}
	return &ec2.DisassociateAddressOutput{}, nil
}

func (m *mockEC2) ReleaseAddress(ctx context.Context, params *ec2.ReleaseAddressInput, optFns ...func(*ec2.Options)) (*ec2.ReleaseAddressOutput, error) {
	m.track("ReleaseAddress", params)
	if m.ReleaseAddressFn != nil {
		return m.ReleaseAddressFn(ctx, params, optFns...)
	}
	return &ec2.ReleaseAddressOutput{}, nil
}

func (m *mockEC2) DescribeAddresses(ctx context.Context, params *ec2.DescribeAddressesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
	m.track("DescribeAddresses", params)
	if m.DescribeAddressesFn != nil {
		return m.DescribeAddressesFn(ctx, params, optFns...)
	}
	return &ec2.DescribeAddressesOutput{}, nil
}

func (m *mockEC2) DescribeNetworkInterfaces(ctx context.Context, params *ec2.DescribeNetworkInterfacesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
	m.track("DescribeNetworkInterfaces", params)
	if m.DescribeNetworkInterfacesFn != nil {
		return m.DescribeNetworkInterfacesFn(ctx, params, optFns...)
	}
	return &ec2.DescribeNetworkInterfacesOutput{}, nil
}

func (m *mockEC2) DescribeImages(ctx context.Context, params *ec2.DescribeImagesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error) {
	m.track("DescribeImages", params)
	if m.DescribeImagesFn != nil {
		return m.DescribeImagesFn(ctx, params, optFns...)
	}
	return &ec2.DescribeImagesOutput{}, nil
}

func (m *mockEC2) DescribeLaunchTemplates(ctx context.Context, params *ec2.DescribeLaunchTemplatesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplatesOutput, error) {
	m.track("DescribeLaunchTemplates", params)
	if m.DescribeLaunchTemplatesFn != nil {
		return m.DescribeLaunchTemplatesFn(ctx, params, optFns...)
	}
	return &ec2.DescribeLaunchTemplatesOutput{}, nil
}

func (m *mockEC2) DescribeLaunchTemplateVersions(ctx context.Context, params *ec2.DescribeLaunchTemplateVersionsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplateVersionsOutput, error) {
	m.track("DescribeLaunchTemplateVersions", params)
	if m.DescribeLaunchTemplateVersionsFn != nil {
		return m.DescribeLaunchTemplateVersionsFn(ctx, params, optFns...)
	}
	return &ec2.DescribeLaunchTemplateVersionsOutput{}, nil
}

// --- Test helper builders ---

const (
	testVPC = "vpc-test123"
	testAZ  = "us-east-1a"
)

func makeTestInstance(id, state, vpcID, az string, tags []ec2types.Tag, enis []ec2types.InstanceNetworkInterface) ec2types.Instance {
	stateCode := map[string]int32{
		"pending": 0, "running": 16, "shutting-down": 32,
		"terminated": 48, "stopping": 64, "stopped": 80,
	}
	return ec2types.Instance{
		InstanceId: aws.String(id),
		State: &ec2types.InstanceState{
			Name: ec2types.InstanceStateName(state),
			Code: aws.Int32(stateCode[state]),
		},
		VpcId:             aws.String(vpcID),
		Placement:         &ec2types.Placement{AvailabilityZone: aws.String(az)},
		Tags:              tags,
		NetworkInterfaces: enis,
	}
}

func makeENI(id string, deviceIndex int32, privateIP string, association *ec2types.InstanceNetworkInterfaceAssociation) ec2types.InstanceNetworkInterface {
	eni := ec2types.InstanceNetworkInterface{
		NetworkInterfaceId: aws.String(id),
		Attachment:         &ec2types.InstanceNetworkInterfaceAttachment{DeviceIndex: aws.Int32(deviceIndex)},
		PrivateIpAddress:   aws.String(privateIP),
	}
	if association != nil {
		eni.Association = association
	}
	return eni
}

func describeResponse(instances ...ec2types.Instance) *ec2.DescribeInstancesOutput {
	if len(instances) == 0 {
		return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{}}
	}
	return &ec2.DescribeInstancesOutput{
		Reservations: []ec2types.Reservation{{Instances: instances}},
	}
}

func newTestHandler(mock *mockEC2) *Handler {
	return &Handler{
		EC2:            mock,
		NATTagKey:      "nat-zero:managed",
		NATTagValue:    "true",
		IgnoreTagKey:   "nat-zero:ignore",
		IgnoreTagValue: "true",
		TargetVPC:      testVPC,
		AMIOwner:       "568608671756",
		AMIPattern:     "fck-nat-al2023-*-arm64-*",
		ConfigVersion:  "",
		SleepFunc:      func(d time.Duration) {}, // no-op sleep
	}
}
