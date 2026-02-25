package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

// Instance is a simplified EC2 instance representation.
type Instance struct {
	InstanceID        string
	StateName         string
	VpcID             string
	AZ                string
	Tags              []ec2types.Tag
	NetworkInterfaces []ec2types.InstanceNetworkInterface
}

func instanceFromAPI(i ec2types.Instance) *Instance {
	var stateName string
	if i.State != nil {
		stateName = string(i.State.Name)
	}
	var az string
	if i.Placement != nil {
		az = aws.ToString(i.Placement.AvailabilityZone)
	}
	return &Instance{
		InstanceID:        aws.ToString(i.InstanceId),
		StateName:         stateName,
		VpcID:             aws.ToString(i.VpcId),
		AZ:                az,
		Tags:              i.Tags,
		NetworkInterfaces: i.NetworkInterfaces,
	}
}

func hasTag(tags []ec2types.Tag, key, value string) bool {
	for _, t := range tags {
		if aws.ToString(t.Key) == key && aws.ToString(t.Value) == value {
			return true
		}
	}
	return false
}

func (h *Handler) getInstance(ctx context.Context, instanceID string) *Instance {
	resp, err := h.EC2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil || len(resp.Reservations) == 0 || len(resp.Reservations[0].Instances) == 0 {
		return nil
	}
	return instanceFromAPI(resp.Reservations[0].Instances[0])
}

// --- Reconciliation queries ---

// findWorkloads returns all pending/running instances in the AZ that are not
// NAT instances and not ignored.
func (h *Handler) findWorkloads(ctx context.Context, az, vpc string) []*Instance {
	defer timed("find_workloads")()
	resp, err := h.EC2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("availability-zone"), Values: []string{az}},
			{Name: aws.String("vpc-id"), Values: []string{vpc}},
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running"}},
		},
	})
	if err != nil {
		log.Printf("Error finding workloads: %v", err)
		return nil
	}

	var workloads []*Instance
	for _, r := range resp.Reservations {
		for _, i := range r.Instances {
			inst := instanceFromAPI(i)
			if hasTag(inst.Tags, h.NATTagKey, h.NATTagValue) {
				continue
			}
			if hasTag(inst.Tags, h.IgnoreTagKey, h.IgnoreTagValue) {
				continue
			}
			workloads = append(workloads, inst)
		}
	}
	return workloads
}

// findNATs returns all NAT instances in an AZ (any non-terminated state).
func (h *Handler) findNATs(ctx context.Context, az, vpc string) []*Instance {
	defer timed("find_nats")()
	resp, err := h.EC2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:" + h.NATTagKey), Values: []string{h.NATTagValue}},
			{Name: aws.String("availability-zone"), Values: []string{az}},
			{Name: aws.String("vpc-id"), Values: []string{vpc}},
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	if err != nil {
		log.Printf("Error finding NATs: %v", err)
		return nil
	}

	var nats []*Instance
	for _, r := range resp.Reservations {
		for _, i := range r.Instances {
			nats = append(nats, instanceFromAPI(i))
		}
	}
	return nats
}

// findEIPs returns all EIPs tagged for this AZ.
func (h *Handler) findEIPs(ctx context.Context, az string) []ec2types.Address {
	defer timed("find_eips")()
	resp, err := h.EC2.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:" + h.NATTagKey), Values: []string{h.NATTagValue}},
			{Name: aws.String("tag:AZ"), Values: []string{az}},
		},
	})
	if err != nil {
		log.Printf("Error finding EIPs: %v", err)
		return nil
	}
	return resp.Addresses
}

// findConfiguredAZs returns the AZs that have a launch template configured.
func (h *Handler) findConfiguredAZs(ctx context.Context) []string {
	defer timed("find_configured_azs")()
	resp, err := h.EC2.DescribeLaunchTemplates(ctx, &ec2.DescribeLaunchTemplatesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:VpcId"), Values: []string{h.TargetVPC}},
		},
	})
	if err != nil || len(resp.LaunchTemplates) == 0 {
		return nil
	}

	var azs []string
	for _, lt := range resp.LaunchTemplates {
		for _, tag := range lt.Tags {
			if aws.ToString(tag.Key) == "AvailabilityZone" {
				azs = append(azs, aws.ToString(tag.Value))
			}
		}
	}
	return azs
}

// --- Reconciliation actions ---

// terminateDuplicateNATs keeps the best NAT (prefer running) and terminates the rest.
// Returns the kept NAT as a single-element slice.
func (h *Handler) terminateDuplicateNATs(ctx context.Context, nats []*Instance) []*Instance {
	log.Printf("%d NAT instances found, deduplicating", len(nats))

	// Prefer running instances.
	var running []*Instance
	for _, n := range nats {
		if n.StateName == "pending" || n.StateName == "running" {
			running = append(running, n)
		}
	}
	keep := nats[0]
	if len(running) > 0 {
		keep = running[0]
	}

	for _, n := range nats {
		if n.InstanceID != keep.InstanceID {
			log.Printf("Terminating duplicate NAT %s", n.InstanceID)
			h.terminateInstance(ctx, n.InstanceID)
		}
	}
	return []*Instance{keep}
}

func (h *Handler) terminateInstance(ctx context.Context, instanceID string) {
	_, err := h.EC2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		log.Printf("Failed to terminate %s: %v", instanceID, err)
	}
}

func (h *Handler) startInstance(ctx context.Context, instanceID string) {
	_, err := h.EC2.StartInstances(ctx, &ec2.StartInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		log.Printf("Failed to start %s: %v", instanceID, err)
	} else {
		log.Printf("Started %s", instanceID)
	}
}

func (h *Handler) stopInstance(ctx context.Context, instanceID string) {
	_, err := h.EC2.StopInstances(ctx, &ec2.StopInstancesInput{
		InstanceIds: []string{instanceID},
		Force:       aws.Bool(true),
	})
	if err != nil {
		log.Printf("Failed to stop %s: %v", instanceID, err)
	} else {
		log.Printf("Stopped %s", instanceID)
	}
}

// allocateAndAttachEIP allocates an EIP and associates it to the NAT's public ENI.
func (h *Handler) allocateAndAttachEIP(ctx context.Context, nat *Instance, az string) {
	defer timed("allocate_and_attach_eip")()

	eni := getPublicENI(nat)
	if eni == nil {
		log.Printf("No public ENI on %s", nat.InstanceID)
		return
	}

	eniID := aws.ToString(eni.NetworkInterfaceId)

	// If ENI already has an EIP (e.g. EIP tag query lagged), skip.
	if eni.Association != nil && aws.ToString(eni.Association.PublicIp) != "" {
		log.Printf("ENI %s already has EIP %s", eniID, aws.ToString(eni.Association.PublicIp))
		return
	}

	alloc, err := h.EC2.AllocateAddress(ctx, &ec2.AllocateAddressInput{
		Domain: ec2types.DomainTypeVpc,
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeElasticIp,
			Tags: []ec2types.Tag{
				{Key: aws.String("AZ"), Value: aws.String(az)},
				{Key: aws.String(h.NATTagKey), Value: aws.String(h.NATTagValue)},
				{Key: aws.String("Name"), Value: aws.String(fmt.Sprintf("nat-eip-%s", az))},
			},
		}},
	})
	if err != nil {
		log.Printf("Failed to allocate EIP: %v", err)
		return
	}
	allocID := aws.ToString(alloc.AllocationId)

	_, err = h.EC2.AssociateAddress(ctx, &ec2.AssociateAddressInput{
		AllocationId:       aws.String(allocID),
		NetworkInterfaceId: aws.String(eniID),
	})
	if err != nil {
		log.Printf("Failed to associate EIP: %v", err)
		h.EC2.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{AllocationId: aws.String(allocID)})
		return
	}
	log.Printf("Attached EIP %s to %s", aws.ToString(alloc.PublicIp), eniID)
}

// releaseEIPs disassociates and releases a list of EIPs.
func (h *Handler) releaseEIPs(ctx context.Context, eips []ec2types.Address) {
	for _, addr := range eips {
		allocID := aws.ToString(addr.AllocationId)
		if addr.AssociationId != nil {
			_, err := h.EC2.DisassociateAddress(ctx, &ec2.DisassociateAddressInput{
				AssociationId: addr.AssociationId,
			})
			if err != nil && !isErrCode(err, "InvalidAssociationID.NotFound") {
				log.Printf("Failed to disassociate EIP %s: %v", allocID, err)
			}
		}
		_, err := h.EC2.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{
			AllocationId: aws.String(allocID),
		})
		if err != nil {
			log.Printf("Failed to release EIP %s: %v", allocID, err)
		} else {
			log.Printf("Released EIP %s", allocID)
		}
	}
}

// --- ENI helper ---

func getPublicENI(inst *Instance) *ec2types.InstanceNetworkInterface {
	for i := range inst.NetworkInterfaces {
		if aws.ToInt32(inst.NetworkInterfaces[i].Attachment.DeviceIndex) == 0 {
			return &inst.NetworkInterfaces[i]
		}
	}
	return nil
}

// --- Config version ---

func (h *Handler) isCurrentConfig(inst *Instance) bool {
	if h.ConfigVersion == "" {
		return true
	}
	for _, t := range inst.Tags {
		if aws.ToString(t.Key) == "ConfigVersion" {
			return aws.ToString(t.Value) == h.ConfigVersion
		}
	}
	return true // no tag to compare — assume current
}

// --- NAT lifecycle helpers ---

func (h *Handler) resolveAMI(ctx context.Context) string {
	defer timed("resolve_ami")()
	resp, err := h.EC2.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{h.AMIOwner},
		Filters: []ec2types.Filter{
			{Name: aws.String("name"), Values: []string{h.AMIPattern}},
			{Name: aws.String("state"), Values: []string{"available"}},
		},
	})
	if err != nil {
		log.Printf("AMI lookup failed, using launch template default: %v", err)
		return ""
	}
	if len(resp.Images) == 0 {
		return ""
	}

	images := resp.Images
	sort.Slice(images, func(i, j int) bool {
		return aws.ToString(images[i].CreationDate) > aws.ToString(images[j].CreationDate)
	})
	ami := images[0]
	amiID := aws.ToString(ami.ImageId)
	log.Printf("Using AMI %s (%s)", amiID, aws.ToString(ami.Name))
	return amiID
}

func (h *Handler) resolveLT(ctx context.Context, az, vpc string) (string, int64) {
	defer timed("resolve_lt")()
	resp, err := h.EC2.DescribeLaunchTemplates(ctx, &ec2.DescribeLaunchTemplatesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:AvailabilityZone"), Values: []string{az}},
			{Name: aws.String("tag:VpcId"), Values: []string{vpc}},
		},
	})
	if err != nil || len(resp.LaunchTemplates) == 0 {
		return "", 0
	}

	ltID := aws.ToString(resp.LaunchTemplates[0].LaunchTemplateId)

	verResp, err := h.EC2.DescribeLaunchTemplateVersions(ctx, &ec2.DescribeLaunchTemplateVersionsInput{
		LaunchTemplateId: aws.String(ltID),
		Versions:         []string{"$Latest"},
	})
	if err != nil || len(verResp.LaunchTemplateVersions) == 0 {
		return "", 0
	}

	version := aws.ToInt64(verResp.LaunchTemplateVersions[0].VersionNumber)
	return ltID, version
}

func (h *Handler) createNAT(ctx context.Context, az, vpc string) string {
	defer timed("create_nat")()

	ltID, version := h.resolveLT(ctx, az, vpc)
	if ltID == "" {
		log.Printf("No launch template for AZ=%s VPC=%s", az, vpc)
		return ""
	}

	amiID := h.resolveAMI(ctx)

	input := &ec2.RunInstancesInput{
		LaunchTemplate: &ec2types.LaunchTemplateSpecification{
			LaunchTemplateId: aws.String(ltID),
			Version:          aws.String(fmt.Sprintf("%d", version)),
		},
		MinCount: aws.Int32(1),
		MaxCount: aws.Int32(1),
	}

	if h.ConfigVersion != "" {
		input.TagSpecifications = []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeInstance,
			Tags: []ec2types.Tag{
				{Key: aws.String("ConfigVersion"), Value: aws.String(h.ConfigVersion)},
			},
		}}
	}

	if amiID != "" {
		input.ImageId = aws.String(amiID)
	}

	resp, err := h.EC2.RunInstances(ctx, input)
	if err != nil {
		log.Printf("Failed to create NAT instance: %v", err)
		return ""
	}
	iid := aws.ToString(resp.Instances[0].InstanceId)
	log.Printf("Created NAT instance %s in %s", iid, az)
	return iid
}

// --- Cleanup (destroy-time) ---

func (h *Handler) cleanupAll(ctx context.Context) {
	defer timed("cleanup_all")()

	resp, err := h.EC2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:" + h.NATTagKey), Values: []string{h.NATTagValue}},
			{Name: aws.String("vpc-id"), Values: []string{h.TargetVPC}},
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	if err != nil {
		log.Printf("Error listing NAT instances: %v", err)
		return
	}

	var instanceIDs []string
	for _, r := range resp.Reservations {
		for _, i := range r.Instances {
			instanceIDs = append(instanceIDs, aws.ToString(i.InstanceId))
		}
	}

	if len(instanceIDs) > 0 {
		log.Printf("Terminating NAT instances: %v", instanceIDs)
		h.EC2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
			InstanceIds: instanceIDs,
		})
		h.waitForTermination(ctx, instanceIDs)
	}

	// Release EIPs.
	addrResp, err := h.EC2.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:" + h.NATTagKey), Values: []string{h.NATTagValue}},
		},
	})
	if err == nil {
		for _, addr := range addrResp.Addresses {
			allocID := aws.ToString(addr.AllocationId)
			if addr.AssociationId != nil {
				_, err := h.EC2.DisassociateAddress(ctx, &ec2.DisassociateAddressInput{
					AssociationId: addr.AssociationId,
				})
				if err != nil {
					log.Printf("Failed to disassociate EIP %s: %v", allocID, err)
				}
			}
			_, err := h.EC2.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{
				AllocationId: aws.String(allocID),
			})
			if err != nil {
				log.Printf("Failed to release EIP %s: %v", allocID, err)
			} else {
				log.Printf("Released EIP %s", allocID)
			}
		}
	}
}

// waitForTermination polls until all instances reach the terminated state,
// ensuring ENIs are fully detached before returning. This is critical for
// terraform destroy: the module's pre-created ENIs (delete_on_termination=false)
// remain attached until the instance is fully terminated. If cleanupAll returns
// before termination completes, Terraform may try to delete still-attached ENIs.
func (h *Handler) waitForTermination(ctx context.Context, instanceIDs []string) {
	defer timed("wait_for_termination")()
	for attempt := 0; attempt < 60; attempt++ {
		time.Sleep(2 * time.Second)
		resp, err := h.EC2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
			InstanceIds: instanceIDs,
			Filters: []ec2types.Filter{
				{Name: aws.String("instance-state-name"), Values: []string{
					"pending", "running", "shutting-down", "stopping", "stopped",
				}},
			},
		})
		if err != nil {
			log.Printf("Error polling termination status: %v", err)
			return
		}
		remaining := 0
		for _, r := range resp.Reservations {
			remaining += len(r.Instances)
		}
		if remaining == 0 {
			log.Printf("All %d NAT instances terminated", len(instanceIDs))
			return
		}
		log.Printf("Waiting for %d instance(s) to terminate...", remaining)
	}
	log.Printf("Timed out waiting for instance termination")
}

// isErrCode returns true if the error (or any wrapped error) has the given
// AWS API error code.
func isErrCode(err error, code string) bool {
	var ae smithy.APIError
	if ok := errors.As(err, &ae); ok {
		return ae.ErrorCode() == code
	}
	return strings.Contains(err.Error(), code)
}
