# Architecture

## Overview

nat-zero uses a **reconciliation pattern** to manage NAT instance lifecycles. A single Lambda function (concurrency=1) observes the current state of an AZ and takes one action to converge toward desired state, then returns. The next event picks up where this one left off.

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

## Config Versioning

The Lambda tags each NAT instance with a `ConfigVersion` hash derived from AMI, instance type, market type, and volume size. When a workload event arrives and the existing NAT has an outdated hash, the reconciler terminates it. The next event creates a replacement with the current config.
