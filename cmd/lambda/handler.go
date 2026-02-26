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

	triggerInst, az, vpc := h.resolveAZ(ctx, event.InstanceID)
	if az == "" {
		// Instance gone from API or wrong VPC/ignored — sweep all AZs.
		h.sweepAllAZs(ctx)
		return nil
	}

	h.reconcile(ctx, az, vpc, event, triggerInst)
	return nil
}

// resolveAZ looks up the trigger instance to determine which AZ to reconcile.
// Returns the instance itself (for use in reconcile) plus its AZ and VPC.
// Returns (nil, "", "") if the instance is gone, wrong VPC, or has the ignore tag.
func (h *Handler) resolveAZ(ctx context.Context, instanceID string) (*Instance, string, string) {
	defer timed("resolve_az")()
	inst := h.getInstance(ctx, instanceID)
	if inst == nil {
		return nil, "", ""
	}
	if inst.VpcID != h.TargetVPC {
		return nil, "", ""
	}
	if hasTag(inst.Tags, h.IgnoreTagKey, h.IgnoreTagValue) {
		return nil, "", ""
	}
	return inst, inst.AZ, inst.VpcID
}

// sweepAllAZs reconciles every AZ that has a launch template configured.
func (h *Handler) sweepAllAZs(ctx context.Context) {
	defer timed("sweep_all_azs")()
	azs := h.findConfiguredAZs(ctx)
	for _, az := range azs {
		h.reconcile(ctx, az, h.TargetVPC, Event{}, nil)
	}
}

// reconcile observes the current state of workloads, NAT, and EIPs in an AZ,
// then takes at most one mutating action to converge toward the desired state.
// triggerInst is the instance that triggered this reconcile (from resolveAZ).
func (h *Handler) reconcile(ctx context.Context, az, vpc string, event Event, triggerInst *Instance) {
	defer timed("reconcile")()

	workloads := h.findWorkloads(ctx, az, vpc)
	nats := h.findNATs(ctx, az, vpc)
	eips := h.findEIPs(ctx, az)

	// --- Handle EC2 eventual consistency for NAT instances ---
	// If the trigger instance is a NAT that findNATs() missed (because tags
	// haven't propagated yet), add it to the list. This prevents the Lambda
	// from trying to create a duplicate NAT when processing a newly-created
	// NAT's pending/running event.
	if triggerInst != nil && hasTag(triggerInst.Tags, h.NATTagKey, h.NATTagValue) {
		found := false
		for _, n := range nats {
			if n.InstanceID == triggerInst.InstanceID {
				found = true
				break
			}
		}
		if !found && (triggerInst.StateName == "pending" || triggerInst.StateName == "running" ||
			triggerInst.StateName == "stopping" || triggerInst.StateName == "stopped") {
			log.Printf("Adding trigger NAT %s to nats list (eventual consistency)", triggerInst.InstanceID)
			nats = append([]*Instance{triggerInst}, nats...)
		}
	}

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
		// nat is pending or running — good.
		// If the NAT appears "pending" but the EventBridge event says "running",
		// trust the event. EC2 API responses are eventually consistent and may
		// lag behind the actual state transition. EventBridge events are
		// authoritative for state changes.
		if nat.StateName == "pending" && event.InstanceID == nat.InstanceID && event.State == "running" {
			log.Printf("NAT %s shows pending but event says running, trusting event", nat.InstanceID)
			nat.StateName = "running"
		}
	} else {
		if nat != nil && (nat.StateName == "running" || nat.StateName == "pending") {
			log.Printf("No workloads in %s, stopping NAT %s", az, nat.InstanceID)
			h.stopInstance(ctx, nat.InstanceID)
			return
		}
		if nat != nil && nat.StateName == "stopping" {
			// Trust event state - EC2 API may lag behind the actual transition
			if event.InstanceID == nat.InstanceID && event.State == "stopped" {
				log.Printf("NAT %s shows stopping but event says stopped, trusting event", nat.InstanceID)
				nat.StateName = "stopped"
			} else {
				log.Printf("Reconcile %s: waiting (workloads=0, nat=stopping, eips=%d)",
					az, len(eips))
				return
			}
		}
		// nat is stopped/nil — good
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

	if nat != nil && nat.StateName == "pending" {
		log.Printf("Reconcile %s: waiting (workloads=%d, nat=pending, eips=%d)",
			az, len(workloads), len(eips))
	} else {
		log.Printf("Reconcile %s: converged (workloads=%d, nat=%s, eips=%d)",
			az, len(workloads), natState(nat), len(eips))
	}
}

func natState(nat *Instance) string {
	if nat == nil {
		return "none"
	}
	return nat.StateName
}
