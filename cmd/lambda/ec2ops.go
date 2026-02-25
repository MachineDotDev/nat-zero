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

func (h *Handler) classify(ctx context.Context, instanceID string) (ignore, isNAT bool, az, vpc string) {
	defer timed("classify")()
	inst := h.getInstance(ctx, instanceID)
	if inst == nil {
		return true, false, "", ""
	}
	if inst.VpcID != h.TargetVPC {
		return true, false, "", ""
	}
	if hasTag(inst.Tags, h.IgnoreTagKey, h.IgnoreTagValue) {
		return true, false, inst.AZ, inst.VpcID
	}
	return false, hasTag(inst.Tags, h.NATTagKey, h.NATTagValue), inst.AZ, inst.VpcID
}

func (h *Handler) waitForState(ctx context.Context, instanceID string, states []string, timeout int) bool {
	iterations := timeout / 2
	for i := 0; i < iterations; i++ {
		inst := h.getInstance(ctx, instanceID)
		if inst == nil {
			return false
		}
		for _, s := range states {
			if inst.StateName == s {
				return true
			}
		}
		h.sleep(2 * time.Second)
	}
	log.Printf("Timeout: %s never reached %v", instanceID, states)
	return false
}

// findNAT finds the NAT instance in an AZ. Deduplicates if multiple exist.
func (h *Handler) findNAT(ctx context.Context, az, vpc string) *Instance {
	defer timed("find_nat")()
	resp, err := h.EC2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:" + h.NATTagKey), Values: []string{h.NATTagValue}},
			{Name: aws.String("availability-zone"), Values: []string{az}},
			{Name: aws.String("vpc-id"), Values: []string{vpc}},
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running", "stopping", "stopped"}},
		},
	})
	if err != nil {
		log.Printf("Error finding NAT: %v", err)
		return nil
	}

	var nats []*Instance
	for _, r := range resp.Reservations {
		for _, i := range r.Instances {
			nats = append(nats, instanceFromAPI(i))
		}
	}

	if len(nats) == 0 {
		return nil
	}
	if len(nats) == 1 {
		return nats[0]
	}

	// Race condition: multiple NATs. Keep the running one, terminate extras.
	log.Printf("%d NAT instances in %s, deduplicating", len(nats), az)
	var running []*Instance
	for _, n := range nats {
		if isStarting(n.StateName) {
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
			_, err := h.EC2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
				InstanceIds: []string{n.InstanceID},
			})
			if err != nil {
				log.Printf("Failed to terminate %s: %v", n.InstanceID, err)
			}
		}
	}
	return keep
}

func (h *Handler) findSiblings(ctx context.Context, az, vpc, excludeID string) []*Instance {
	defer timed("find_siblings")()
	resp, err := h.EC2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("availability-zone"), Values: []string{az}},
			{Name: aws.String("vpc-id"), Values: []string{vpc}},
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running"}},
		},
	})
	if err != nil {
		log.Printf("Error finding siblings: %v", err)
		return nil
	}

	var siblings []*Instance
	for _, r := range resp.Reservations {
		for _, i := range r.Instances {
			inst := instanceFromAPI(i)
			if inst.InstanceID == excludeID {
				continue
			}
			if !hasTag(inst.Tags, h.NATTagKey, h.NATTagValue) &&
				!hasTag(inst.Tags, h.IgnoreTagKey, h.IgnoreTagValue) {
				siblings = append(siblings, inst)
			}
		}
	}
	return siblings
}

// --- EIP management (EventBridge-driven) ---

func getPublicENI(inst *Instance) *ec2types.InstanceNetworkInterface {
	for i := range inst.NetworkInterfaces {
		if aws.ToInt32(inst.NetworkInterfaces[i].Attachment.DeviceIndex) == 0 {
			return &inst.NetworkInterfaces[i]
		}
	}
	return nil
}

// attachEIP waits for the NAT instance to reach "running", then allocates and
// associates an EIP to the public ENI. Idempotent: no-op if ENI already has an EIP.
func (h *Handler) attachEIP(ctx context.Context, instanceID, az string) {
	defer timed("attach_eip")()

	if !h.waitForState(ctx, instanceID, []string{"running"}, 120) {
		return
	}

	inst := h.getInstance(ctx, instanceID)
	if inst == nil {
		return
	}
	eni := getPublicENI(inst)
	if eni == nil {
		log.Printf("No public ENI on %s", instanceID)
		return
	}

	// Idempotent: if ENI already has an EIP, nothing to do.
	if eni.Association != nil && aws.ToString(eni.Association.PublicIp) != "" {
		log.Printf("ENI %s already has EIP %s", aws.ToString(eni.NetworkInterfaceId), aws.ToString(eni.Association.PublicIp))
		return
	}

	eniID := aws.ToString(eni.NetworkInterfaceId)

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

	// Race-detection: re-check ENI before associating. Another invocation may
	// have already attached an EIP between our first check and AllocateAddress.
	niResp, descErr := h.EC2.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{
		NetworkInterfaceIds: []string{eniID},
	})
	if descErr == nil && len(niResp.NetworkInterfaces) > 0 {
		ni := niResp.NetworkInterfaces[0]
		if ni.Association != nil && aws.ToString(ni.Association.PublicIp) != "" {
			log.Printf("Race detected: ENI %s already has EIP %s, releasing %s",
				eniID, aws.ToString(ni.Association.PublicIp), allocID)
			h.EC2.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{AllocationId: aws.String(allocID)})
			return
		}
	} else if descErr != nil {
		log.Printf("Failed to re-check ENI %s (proceeding with associate): %v", eniID, descErr)
	}

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

// detachEIP waits for the NAT instance to reach "stopped", then disassociates
// and releases the EIP from the public ENI. Also sweeps for orphaned EIPs
// left by concurrent attachEIP races.
func (h *Handler) detachEIP(ctx context.Context, instanceID, az string) {
	defer timed("detach_eip")()

	if !h.waitForState(ctx, instanceID, []string{"stopped"}, 120) {
		return
	}

	inst := h.getInstance(ctx, instanceID)
	if inst == nil {
		return
	}
	eni := getPublicENI(inst)
	if eni == nil {
		return
	}
	eniID := aws.ToString(eni.NetworkInterfaceId)

	niResp, err := h.EC2.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{
		NetworkInterfaceIds: []string{eniID},
	})
	if err != nil {
		log.Printf("Failed to describe ENI %s: %v", eniID, err)
		return
	}
	if len(niResp.NetworkInterfaces) > 0 {
		ni := niResp.NetworkInterfaces[0]
		if ni.Association != nil && aws.ToString(ni.Association.AssociationId) != "" {
			assocID := aws.ToString(ni.Association.AssociationId)
			allocID := aws.ToString(ni.Association.AllocationId)
			publicIP := aws.ToString(ni.Association.PublicIp)

			_, err = h.EC2.DisassociateAddress(ctx, &ec2.DisassociateAddressInput{
				AssociationId: aws.String(assocID),
			})
			if err != nil {
				if isErrCode(err, "InvalidAssociationID.NotFound") {
					log.Printf("EIP already disassociated from %s", eniID)
				} else {
					log.Printf("Failed to detach EIP from %s: %v", eniID, err)
					return
				}
			}
			_, err = h.EC2.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{
				AllocationId: aws.String(allocID),
			})
			if err != nil {
				log.Printf("Failed to release EIP %s: %v", allocID, err)
			} else {
				log.Printf("Released EIP %s from %s", publicIP, eniID)
			}
		}
	}

	h.sweepOrphanEIPs(ctx, az)
}

// sweepOrphanEIPs releases any EIPs tagged for this AZ that were left behind
// by concurrent attachEIP races or NAT termination without a stop cycle.
func (h *Handler) sweepOrphanEIPs(ctx context.Context, az string) {
	defer timed("sweep_orphan_eips")()
	addrResp, err := h.EC2.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:" + h.NATTagKey), Values: []string{h.NATTagValue}},
			{Name: aws.String("tag:AZ"), Values: []string{az}},
		},
	})
	if err != nil {
		log.Printf("Orphan EIP sweep failed for %s: %v", az, err)
		return
	}
	for _, addr := range addrResp.Addresses {
		orphanAllocID := aws.ToString(addr.AllocationId)
		if addr.AssociationId != nil {
			h.EC2.DisassociateAddress(ctx, &ec2.DisassociateAddressInput{
				AssociationId: addr.AssociationId,
			})
		}
		_, err := h.EC2.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{
			AllocationId: aws.String(orphanAllocID),
		})
		if err != nil {
			log.Printf("Failed to release orphan EIP %s: %v", orphanAllocID, err)
		} else {
			log.Printf("Released orphan EIP %s in %s", orphanAllocID, az)
		}
	}
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

func (h *Handler) replaceNAT(ctx context.Context, inst *Instance, az, vpc string) string {
	defer timed("replace_nat")()
	iid := inst.InstanceID
	var eniIDs []string
	for _, eni := range inst.NetworkInterfaces {
		eniIDs = append(eniIDs, aws.ToString(eni.NetworkInterfaceId))
	}

	log.Printf("Replacing outdated NAT %s in %s", iid, az)
	h.EC2.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: []string{iid},
	})

	// Wait for termination using polling.
	h.waitForTermination(ctx, iid)

	// Wait for ENIs to become available.
	if len(eniIDs) > 0 {
		for i := 0; i < 60; i++ {
			niResp, err := h.EC2.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{
				NetworkInterfaceIds: eniIDs,
			})
			if err == nil {
				allAvailable := true
				for _, ni := range niResp.NetworkInterfaces {
					if ni.Status != ec2types.NetworkInterfaceStatusAvailable {
						allAvailable = false
						break
					}
				}
				if allAvailable {
					break
				}
			}
			h.sleep(2 * time.Second)
		}
	}

	return h.createNAT(ctx, az, vpc)
}

func (h *Handler) waitForTermination(ctx context.Context, instanceID string) {
	for i := 0; i < 100; i++ {
		inst := h.getInstance(ctx, instanceID)
		if inst == nil || inst.StateName == "terminated" {
			return
		}
		h.sleep(2 * time.Second)
	}
	log.Printf("Timeout waiting for %s to terminate", instanceID)
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

	// Pick the latest by CreationDate.
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

func (h *Handler) startNAT(ctx context.Context, inst *Instance, az string) {
	defer timed("start_nat")()
	iid := inst.InstanceID
	if !h.waitForState(ctx, iid, []string{"stopped"}, 90) {
		return
	}
	_, err := h.EC2.StartInstances(ctx, &ec2.StartInstancesInput{
		InstanceIds: []string{iid},
	})
	if err != nil {
		log.Printf("Failed to start NAT %s: %v", iid, err)
		return
	}
	log.Printf("Started NAT %s", iid)
}

func (h *Handler) stopNAT(ctx context.Context, inst *Instance) {
	defer timed("stop_nat")()
	iid := inst.InstanceID
	_, err := h.EC2.StopInstances(ctx, &ec2.StopInstancesInput{
		InstanceIds: []string{iid},
		Force:       aws.Bool(true),
	})
	if err != nil {
		log.Printf("Failed to stop NAT %s: %v", iid, err)
		return
	}
	log.Printf("Stopped NAT %s", iid)
}

// sweepIdleNATs is a fallback for when classify can't find the triggering
// instance (e.g. it's already gone from the EC2 API after termination).
// It checks every running NAT in the VPC and stops any with no siblings.
func (h *Handler) sweepIdleNATs(ctx context.Context, triggerID string) {
	defer timed("sweep_idle_nats")()
	resp, err := h.EC2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:" + h.NATTagKey), Values: []string{h.NATTagValue}},
			{Name: aws.String("vpc-id"), Values: []string{h.TargetVPC}},
			{Name: aws.String("instance-state-name"), Values: []string{"pending", "running"}},
		},
	})
	if err != nil {
		log.Printf("Sweep failed: %v", err)
		return
	}
	for _, r := range resp.Reservations {
		for _, i := range r.Instances {
			nat := instanceFromAPI(i)
			if len(h.findSiblings(ctx, nat.AZ, nat.VpcID, triggerID)) == 0 {
				log.Printf("Sweep: no siblings for NAT %s in %s, stopping", nat.InstanceID, nat.AZ)
				h.stopNAT(ctx, nat)
			}
		}
	}
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
	}

	// Release EIPs while instances are terminating (overlap the wait).
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

	// Wait for instance termination.
	if len(instanceIDs) > 0 {
		for _, iid := range instanceIDs {
			h.waitForTermination(ctx, iid)
		}
		log.Println("All NAT instances terminated")
	}
}

// isErrCode returns true if the error (or any wrapped error) has the given
// AWS API error code. Works with both smithy APIError and legacy awserr.
func isErrCode(err error, code string) bool {
	var ae smithy.APIError
	if ok := errors.As(err, &ae); ok {
		return ae.ErrorCode() == code
	}
	// Fallback: check the error string for SDKs that don't implement APIError.
	return strings.Contains(err.Error(), code)
}
