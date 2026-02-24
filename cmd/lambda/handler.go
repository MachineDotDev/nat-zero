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
		return nil
	}

	// NAT events → manage EIP via EventBridge
	if isNAT {
		if isStarting(state) {
			h.attachEIP(ctx, iid, az)
		} else if isStopping(state) {
			h.detachEIP(ctx, iid)
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
		h.maybeStopNAT(ctx, nat, az, vpc)
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
func (h *Handler) maybeStopNAT(ctx context.Context, nat *Instance, az, vpc string) {
	if nat == nil {
		return
	}
	// Brief retry to let concurrent events settle.
	for attempt := 0; attempt < 3; attempt++ {
		if len(h.findSiblings(ctx, az, vpc)) > 0 {
			log.Printf("Siblings still running in %s, keeping NAT", az)
			return
		}
		if attempt < 2 {
			h.sleep(2 * time.Second)
		}
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
