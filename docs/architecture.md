# Architecture

## Overview

nat-zero uses a **reconciliation pattern** to manage NAT instance lifecycles. A single Lambda function (concurrency=1) observes the current state of an AZ and takes one action to converge toward desired state, then returns. The next event picks up where this one left off.

### Pattern Origins

The reconciliation loop pattern has deep roots:

- **Control theory (1788+)**: Feedback loops comparing actual state to desired state, taking corrective action
- **CFEngine (1993)**: Mark Burgess introduced "convergence" to configuration management
- **Google Borg/Omega (2005+)**: Internal cluster managers used reconciliation controllers
- **Kubernetes (2014+)**: Popularized the pattern as "level-triggered" vs "edge-triggered" logic

The key insight: **state is more useful than events**. Rather than tracking event sequences, we observe current state and compute the delta. This makes the system robust to missed events, crashes, and restarts.

See: [Borg, Omega, and Kubernetes (ACM Queue)](https://queue.acm.org/detail.cfm?id=2898444), [Tim Hockin - Edge vs Level Triggered Logic](https://speakerdeck.com/thockin/edge-vs-level-triggered-logic)

```
  EventBridge (EC2 state changes)
         │
         ▼
  ┌─────────────────────────┐
  │  Lambda (concurrency=1) │
  │                         │
  │  1. Resolve AZ          │
  │  2. Observe state       │
  │  3. Take one action     │
  │  4. Return              │
  └─────────────────────────┘
         │
    ┌────┴────┐
    ▼         ▼
  EC2 API   EIP API
  (NATs)    (allocate/release)
```

## Reconciliation Loop

Every invocation runs the same loop regardless of which event triggered it:

```
reconcile(az):
    workloads = pending/running non-NAT instances in AZ
    nats      = non-terminated NAT instances in AZ
    eips      = EIPs tagged for this AZ
    needNAT   = len(workloads) > 0

    # One action per invocation, then return
```

### Decision Matrix

| Workloads? | NAT State | EIP State | Action |
|:----------:|-----------|-----------|--------|
| Yes | None / shutting-down | — | **Create** NAT |
| Yes | Stopped | — | **Start** NAT |
| Yes | Stopping | — | Wait (no-op) |
| Yes | Outdated config | — | **Terminate** NAT (recreate on next event) |
| Yes | Running | No EIP | **Allocate + attach** EIP |
| Yes | Running | Has EIP | Converged |
| No | Running / pending | — | **Stop** NAT |
| No | Stopped | Has EIP | **Release** EIP |
| No | Stopped | No EIP | Converged |
| No | Stopping | — | Wait (no-op) |
| — | Multiple NATs | — | **Terminate** duplicates |
| — | — | Multiple EIPs | **Release** extras |

### Why Single Writer

`reserved_concurrent_executions = 1` means only one Lambda runs at a time. Events that arrive during execution are queued and processed sequentially. This eliminates:

- Duplicate NAT creation
- Double EIP allocation
- Start/stop race conditions
- Need for distributed locking

### Event Agnosticism

The reconciler does not care what type of instance triggered the event. It observes all workloads and NATs in the AZ, computes desired state, and acts. The event is just a signal that "something changed."

- Workload `pending` → reconcile → creates NAT if needed
- NAT `running` → reconcile → attaches EIP if needed
- Workload `terminated` → reconcile → stops NAT if no workloads
- NAT `stopped` → reconcile → releases EIP if present
- Instance gone from API → sweep all configured AZs

## Event Flows

### Scale-up

```
Workload launches (pending)
  → reconcile: workloads=1, NAT=nil       → createNAT

NAT reaches running
  → reconcile: workloads=1, NAT=running, EIP=0  → allocateAndAttachEIP

Next event
  → reconcile: workloads=1, NAT=running, EIP=1  → converged ✓
```

### Scale-down

```
Last workload terminates
  → reconcile: workloads=0, NAT=running    → stopNAT

NAT reaches stopped
  → reconcile: workloads=0, NAT=stopped, EIP=1  → releaseEIP

Next event
  → reconcile: workloads=0, NAT=stopped, EIP=0  → converged ✓
```

### Restart

```
New workload launches, NAT is stopped
  → reconcile: workloads=1, NAT=stopped    → startNAT

NAT reaches running
  → reconcile: workloads=1, NAT=running, EIP=0  → allocateAndAttachEIP
  → converged ✓
```

### Terraform Destroy

```
Terraform invokes Lambda with {action: "cleanup"}
  → terminate all NAT instances
  → wait for full termination (ENI detachment)
  → release all EIPs
  → return (Terraform proceeds to delete ENIs/SGs)
```

## Dual ENI Architecture

Each NAT instance uses two ENIs to separate public and private traffic:

```
  Private Subnet          NAT Instance              Public Subnet
  ┌──────────────┐   ┌──────────────────┐   ┌──────────────────┐
  │ Route Table   │   │                  │   │                  │
  │ 0.0.0.0/0 ───┼──→│ Private ENI      │   │ Public ENI       │
  │              │   │ (ens6)           │   │ (ens5) + EIP     │──→ IGW
  │              │   │     ↓ iptables ──┼──→│                  │
  │              │   │   MASQUERADE     │   │ src_dst_check=off│
  └──────────────┘   └──────────────────┘   └──────────────────┘
```

- **Pre-created by Terraform**: ENIs persist across stop/start cycles, keeping route tables intact
- **source_dest_check=false**: Required on both ENIs for NAT forwarding
- **EIP lifecycle**: Allocated on NAT running, released on NAT stopped — no charge when idle
- **AMI requirement**: nat-zero needs its own AMI because the ENIs are defined up front by the launch template and the EIP is attached later by the reconciler. `fck-nat` AMIs are intentionally unsupported here because their bootstrap discovers ENIs via IMDS/AWS calls, which does not match nat-zero's deterministic dual-ENI lifecycle.

## Config Versioning

The Lambda tags each NAT instance with a `ConfigVersion` hash derived from AMI, instance type, market type, volume size, and encryption setting.

When the reconciler detects an outdated NAT, replacement takes two events (following the "one action per invocation" pattern):

1. **Event 1**: Outdated config detected → terminate NAT → return
2. **Event 2**: NAT is now `shutting-down`/`terminated` → create new NAT with current config

This avoids racing with ENI detachment and keeps error handling simple.

## Reliability

### EC2 API Eventual Consistency

The EC2 API is eventually consistent. When EventBridge fires a state change event (e.g., `running`), the EC2 DescribeInstances API may still return the previous state (e.g., `pending`) for several seconds.

nat-zero handles this by **trusting the event state** for the trigger instance:

```go
// Trust event state over EC2 API (eventual consistency)
if triggerInst != nil {
    triggerInst.StateName = event.State
}
```

This also applies to NAT instances that may not appear in filter-based queries immediately after creation (tag propagation delay). The reconciler adds the trigger instance to the NAT list if it's missing.

### EventBridge Propagation Delay

After Terraform creates the EventBridge rule and target, there's a propagation delay before events are reliably delivered. Events fired during this window may be silently dropped.

nat-zero includes a 60-second `time_sleep` resource after target creation to mitigate this. Workloads launched immediately after `terraform apply` may still miss their initial events, but subsequent events will trigger reconciliation.

### NAT Stop Behavior

NAT instances are stopped with `Force=true` because they're stateless packet forwarders. There's no graceful shutdown needed — the routing table instantly fails over when the ENI becomes unreachable, and workloads retry their connections.

### Lambda Timeout

The Lambda has a 90-second timeout. Typical invocations complete in 400-600ms. The extended timeout accommodates:
- Cleanup operations during `terraform destroy` (terminate NATs, wait for ENI detachment, release EIPs)
- Slow EC2 API responses under load
