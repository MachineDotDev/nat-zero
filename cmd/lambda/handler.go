package main

import (
	"context"
	"log"
)

// Event is the Lambda input payload.
type Event struct {
	Action     string `json:"action,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
	State      string `json:"state,omitempty"`
}

// Handler holds the EC2 client and configuration for the Lambda.
type Handler struct {
	EC2            EC2API
	NATTagKey      string
	NATTagValue    string
	IgnoreTagKey   string
	IgnoreTagValue string
	TargetVPC      string
	AMIOwner       string
	AMIPattern     string
	ConfigVersion  string
}

// HandleRequest is the Lambda entry point.
func (h *Handler) HandleRequest(ctx context.Context, event Event) error {
	defer timed("handler total")()
	return h.handle(ctx, event)
}

func (h *Handler) handle(ctx context.Context, event Event) error {
	if event.Action == "cleanup" {
		log.Println("Running destroy-time cleanup")
		h.cleanupAll(ctx)
		return nil
	}

	log.Printf("instance=%s state=%s", event.InstanceID, event.State)

	az, vpc := h.resolveAZ(ctx, event.InstanceID)
	if az == "" {
		// Instance gone from API or wrong VPC/ignored — sweep all AZs.
		h.sweepAllAZs(ctx)
		return nil
	}

	h.reconcile(ctx, az, vpc)
	return nil
}

// resolveAZ looks up the trigger instance to determine which AZ to reconcile.
// Returns ("", "") if the instance is gone, wrong VPC, or has the ignore tag.
func (h *Handler) resolveAZ(ctx context.Context, instanceID string) (az, vpc string) {
	defer timed("resolve_az")()
	inst := h.getInstance(ctx, instanceID)
	if inst == nil {
		return "", ""
	}
	if inst.VpcID != h.TargetVPC {
		return "", ""
	}
	if hasTag(inst.Tags, h.IgnoreTagKey, h.IgnoreTagValue) {
		return "", ""
	}
	return inst.AZ, inst.VpcID
}

// sweepAllAZs reconciles every AZ that has a launch template configured.
func (h *Handler) sweepAllAZs(ctx context.Context) {
	defer timed("sweep_all_azs")()
	azs := h.findConfiguredAZs(ctx)
	for _, az := range azs {
		h.reconcile(ctx, az, h.TargetVPC)
	}
}

// reconcile observes the current state of workloads, NAT, and EIPs in an AZ,
// then takes at most one mutating action to converge toward the desired state.
func (h *Handler) reconcile(ctx context.Context, az, vpc string) {
	defer timed("reconcile")()

	workloads := h.findWorkloads(ctx, az, vpc)
	nats := h.findNATs(ctx, az, vpc)
	eips := h.findEIPs(ctx, az)

	needNAT := len(workloads) > 0

	// --- Duplicate NAT cleanup (before anything else) ---
	if len(nats) > 1 {
		nats = h.terminateDuplicateNATs(ctx, nats)
	}

	var nat *Instance
	if len(nats) > 0 {
		nat = nats[0]
	}

	// --- NAT convergence (one action per invocation) ---
	if needNAT {
		if nat == nil || nat.StateName == "shutting-down" || nat.StateName == "terminated" {
			log.Printf("Creating NAT in %s (workloads=%d)", az, len(workloads))
			h.createNAT(ctx, az, vpc)
			return
		}
		if !h.isCurrentConfig(nat) {
			log.Printf("NAT %s has outdated config, terminating for replacement", nat.InstanceID)
			h.terminateInstance(ctx, nat.InstanceID)
			return
		}
		if nat.StateName == "stopped" {
			log.Printf("Starting NAT %s", nat.InstanceID)
			h.startInstance(ctx, nat.InstanceID)
			return
		}
		if nat.StateName == "stopping" {
			log.Printf("NAT %s is stopping, waiting for next event", nat.InstanceID)
			return
		}
		// nat is pending or running — good
	} else {
		if nat != nil && (nat.StateName == "running" || nat.StateName == "pending") {
			log.Printf("No workloads in %s, stopping NAT %s", az, nat.InstanceID)
			h.stopInstance(ctx, nat.InstanceID)
			return
		}
		// nat is stopping/stopped/nil — good
	}

	// --- EIP convergence ---
	natRunning := nat != nil && nat.StateName == "running"
	if natRunning && len(eips) == 0 {
		log.Printf("NAT %s running with no EIP, allocating", nat.InstanceID)
		h.allocateAndAttachEIP(ctx, nat, az)
		return
	}
	if !natRunning && len(eips) > 0 {
		log.Printf("NAT not running, releasing %d EIP(s) in %s", len(eips), az)
		h.releaseEIPs(ctx, eips)
		return
	}
	if len(eips) > 1 {
		log.Printf("Multiple EIPs (%d) in %s, releasing extras", len(eips), az)
		h.releaseEIPs(ctx, eips[1:])
		return
	}

	log.Printf("Reconcile %s: converged (workloads=%d, nat=%s, eips=%d)",
		az, len(workloads), natState(nat), len(eips))
}

func natState(nat *Instance) string {
	if nat == nil {
		return "none"
	}
	return nat.StateName
}
