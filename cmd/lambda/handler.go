package main

import (
	"context"
	"log"
	"time"
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

	// SleepFunc can be replaced in tests to eliminate real waits.
	SleepFunc func(time.Duration)
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

	iid, state := event.InstanceID, event.State
	log.Printf("instance=%s state=%s", iid, state)

	ignore, isNAT, az, vpc := h.classify(ctx, iid)
	if ignore {
		// If the instance can no longer be found (e.g. terminated and gone
		// from the API), fall back to a VPC-wide sweep so we don't miss the
		// scale-down opportunity.
		if isTerminating(state) {
			log.Printf("Instance %s gone (state=%s), sweeping for idle NATs", iid, state)
			h.sweepIdleNATs(ctx, iid)
		}
		return nil
	}

	// NAT events → manage EIP via EventBridge
	if isNAT {
		if isStarting(state) {
			h.attachEIP(ctx, iid, az)
		} else if isStopping(state) {
			h.detachEIP(ctx, iid, az)
		}
		return nil
	}

	// Workload events → manage NAT lifecycle
	nat := h.findNAT(ctx, az, vpc)

	if isStarting(state) {
		h.ensureNAT(ctx, nat, az, vpc)
		return nil
	}

	if isStopping(state) || isTerminating(state) {
		h.maybeStopNAT(ctx, nat, az, vpc, iid)
	}
	return nil
}

// ensureNAT ensures a NAT instance is running in the given AZ.
func (h *Handler) ensureNAT(ctx context.Context, nat *Instance, az, vpc string) {
	if nat == nil || isTerminating(nat.StateName) {
		if nat != nil {
			log.Printf("NAT %s terminated, creating new", nat.InstanceID)
		} else {
			log.Printf("Creating NAT in %s", az)
		}
		h.createNAT(ctx, az, vpc)
		return
	}
	if !h.isCurrentConfig(nat) {
		log.Printf("NAT %s has outdated config, replacing", nat.InstanceID)
		h.replaceNAT(ctx, nat, az, vpc)
		return
	}
	if isStopping(nat.StateName) {
		log.Printf("Starting NAT %s", nat.InstanceID)
		h.startNAT(ctx, nat, az)
	}
}

// maybeStopNAT stops the NAT if no sibling workloads remain.
// triggerID is the instance whose state change triggered this check; it is
// excluded from the sibling query so that a dying workload doesn't count
// itself as a reason to keep the NAT alive.
func (h *Handler) maybeStopNAT(ctx context.Context, nat *Instance, az, vpc, triggerID string) {
	if nat == nil {
		return
	}
	// Retry to let EC2 API eventual consistency settle.
	// Sleep before each check so DescribeInstances reflects the latest state.
	var siblings []*Instance
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			h.sleep(2 * time.Second)
		}
		siblings = h.findSiblings(ctx, az, vpc, triggerID)
		if len(siblings) == 0 {
			break
		}
		log.Printf("Siblings found in %s (attempt %d/3), rechecking", az, attempt+1)
	}
	if len(siblings) > 0 {
		log.Printf("Siblings still running in %s after retries, keeping NAT", az)
		return
	}

	if isStarting(nat.StateName) {
		log.Printf("No siblings, stopping NAT %s", nat.InstanceID)
		h.stopNAT(ctx, nat)
	}
}

func (h *Handler) sleep(d time.Duration) {
	if h.SleepFunc != nil {
		h.SleepFunc(d)
		return
	}
	time.Sleep(d)
}

func isStarting(state string) bool {
	return state == "pending" || state == "running"
}

func isStopping(state string) bool {
	return state == "stopping" || state == "stopped"
}

func isTerminating(state string) bool {
	return state == "shutting-down" || state == "terminated"
}
