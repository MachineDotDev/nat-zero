# Architecture

## High-Level Overview

nat-zero takes a fundamentally different approach to NAT on AWS. Instead of running infrastructure around the clock, it treats NAT as a **reactive service**: infrastructure that exists only when something needs it.

The module deploys an EventBridge rule that watches for EC2 state changes, and a Go Lambda that orchestrates NAT instance lifecycles in response. No polling, no cron jobs, no always-on compute -- just event-driven reactions to what's actually happening in your VPC.

```
                          DATA PLANE
  ┌──────────────────────────────────────────────────────────────────┐
  │                                                                  │
  │  Private Subnet            NAT Instance          Public Subnet   │
  │  ┌─────────────┐    ┌───────────────────┐    ┌───────────────┐   │
  │  │  Workload    │    │  Linux Kernel      │    │  Public ENI   │   │
  │  │  Instance    │───>│  iptables          │───>│  (ens5)       │──>│── Internet
  │  │             │    │  MASQUERADE        │    │  + EIP        │   │   Gateway
  │  └─────────────┘    └───────────────────┘    └───────────────┘   │
  │        │              Private ENI (ens6)                          │
  │        └──────────────────┘                                      │
  │          route 0.0.0.0/0                                         │
  └──────────────────────────────────────────────────────────────────┘

                         CONTROL PLANE
  ┌──────────────────────────────────────────────────────────────────┐
  │                                                                  │
  │  ┌──────────────────┐    ┌──────────────────┐                    │
  │  │  EventBridge      │───>│  Lambda Function  │                    │
  │  │  EC2 State Change │    │  nat-zero       │                    │
  │  └──────────────────┘    │  concurrency = 1 │                    │
  │                          └────────┬─────────┘                    │
  │                                   │                              │
  │                    ┌──────────────┼──────────────┐               │
  │                    │              │              │               │
  │                    v              v              v               │
  │              start/stop      allocate/      on failure           │
  │              NAT instance    release EIP    ┌─────────┐          │
  │                                             │ SQS DLQ │          │
  │                                             └─────────┘          │
  └──────────────────────────────────────────────────────────────────┘
```

## Reconciliation Model

The Lambda uses a **reconciliation pattern** with **reserved concurrency of 1** (single writer). Every invocation performs the same observe-compare-act loop regardless of which event triggered it:

1. **Resolve**: determine the AZ from the trigger instance (or sweep all AZs if the instance is gone)
2. **Observe**: query workloads, NAT instances, and EIPs for that AZ
3. **Decide**: compare actual state to desired state
4. **Act**: take at most ONE mutating action, then return

The next event picks up where this one left off. No waiting, no polling, no retries.

### Why Single Writer Eliminates Races

With `reserved_concurrent_executions = 1`, only one Lambda invocation runs at a time. Events that arrive during execution are queued by the Lambda service and processed sequentially. This means:

- No duplicate NAT creation (only one invocation can call `RunInstances`)
- No double EIP allocation (only one invocation can call `AllocateAddress`)
- No start/stop overlap (only one invocation can modify the NAT state)
- No need for re-check loops or retry logic

### Reconciliation Logic

```
reconcile(az, vpc):
    workloads = findWorkloads(az, vpc)     # pending/running, excluding NAT + ignored
    nats      = findNATs(az, vpc)          # pending/running/stopping/stopped
    eips      = findEIPs(az)               # tagged for this AZ

    # Deduplicate NATs (safety net for pre-existing duplicates)
    if len(nats) > 1:  terminateDuplicateNATs(nats)

    needNAT = len(workloads) > 0

    # --- NAT convergence (one action per invocation) ---
    if needNAT:
        if no NAT or NAT terminating:    createNAT         → return
        if NAT has outdated config:      terminateNAT       → return
        if NAT stopped:                  startNAT           → return
        if NAT stopping:                 return (wait for next event)
        # NAT pending or running — good
    else:
        if NAT running or pending:       stopNAT (Force)    → return
        # NAT stopping/stopped/nil — good

    # --- EIP convergence ---
    if NAT running and no EIPs:          allocateAndAttachEIP  → return
    if NAT not running and EIPs exist:   releaseEIPs           → return
    if multiple EIPs:                    releaseExtras         → return

    # Converged — no action needed
```

### Event Agnosticism

The reconciler does NOT care whether the trigger event came from a NAT instance or a workload. There is no classify step that branches on instance type.

- **Workload `pending` event** → resolveAZ → reconcile → creates/starts NAT if needed
- **NAT `running` event** → resolveAZ → reconcile → attaches EIP if needed
- **Workload `terminated` event** → resolveAZ → reconcile → stops NAT if no workloads
- **NAT `stopped` event** → resolveAZ → reconcile → releases EIP if present
- **Instance gone from API** → sweep all configured AZs → reconcile each

The event is just a signal that "something changed in this AZ." The reconciler always computes the correct answer from current state.

## Event Flow

### Scale-up: Workload starts, NAT created

```
1. Workload → pending
   reconcile: workloads=1, NAT=nil → createNAT

2. NAT → pending
   reconcile: workloads=1, NAT=pending, EIPs=0 → converged (NAT not yet running)

3. NAT → running
   reconcile: workloads=1, NAT=running, EIPs=0 → allocateAndAttachEIP
   Result: NAT has internet via EIP ✓

4. Workload → running
   reconcile: workloads=1, NAT=running, EIPs=1 → converged ✓
```

### Scale-down: Workload terminates, NAT stopped

```
1. Workload → shutting-down
   reconcile: workloads=0, NAT=running → stopNAT (Force=true)

2. NAT → stopping
   reconcile: workloads=0, NAT=stopping → converged (waiting for stopped)

3. NAT → stopped
   reconcile: workloads=0, NAT=stopped, EIPs=1 → releaseEIPs
   Result: NAT idle, no EIP charge ✓
```

### Restart: New workload starts, stopped NAT restarted

```
1. New workload → pending
   reconcile: workloads=1, NAT=stopped → startNAT

2. NAT → pending → running
   reconcile: workloads=1, NAT=running, EIPs=0 → allocateAndAttachEIP
   Result: NAT has internet via EIP ✓
```

### Terraform destroy

```
Terraform invokes Lambda with {action: "cleanup"}
Action: find all NAT instances → terminate → release all EIPs
Result: clean state for ENI/SG destruction ✓
```

## Dual ENI Architecture

Each NAT instance uses two Elastic Network Interfaces (ENIs) to separate public and private traffic. ENIs are pre-created by Terraform and attached via the launch template, so they persist across instance stop/start cycles.

```
  Private Subnet                NAT Instance                  Public Subnet
  ┌──────────────────┐    ┌──────────────────────┐    ┌──────────────────────┐
  │                  │    │                      │    │                      │
  │  Route Table     │    │   ┌──────────────┐   │    │                      │
  │  0.0.0.0/0 ──────┼───>│   │  iptables    │   │    │                      │
  │       │          │    │   │              │   │    │                      │
  │       v          │    │   │  MASQUERADE  │   │    │                      │
  │  ┌────────────┐  │    │   │  on ens5     │   │    │  ┌────────────────┐  │
  │  │ Private ENI│  │    │   │              │───┼───>│  │  Public ENI    │  │
  │  │ (ens6)     │──┼───>│   │  FORWARD     │   │    │  │  (ens5)        │──┼──> Internet
  │  │            │  │    │   │  ens6 → ens5 │   │    │  │  + EIP         │  │   Gateway
  │  │ No pub IP  │  │    │   │              │   │    │  │                │  │
  │  │ src_dst=off│  │    │   │  RELATED,    │   │    │  │  src_dst=off   │  │
  │  └────────────┘  │    │   │  ESTABLISHED │   │    │  └────────────────┘  │
  │                  │    │   └──────────────┘   │    │                      │
  └──────────────────┘    └──────────────────────┘    └──────────────────────┘
```

Key design decisions:
- **Pre-created ENIs**: ENIs are Terraform-managed and referenced in the launch template. They survive instance stop/start, preserving route table entries.
- **source_dest_check=false**: Required on both ENIs for NAT to work (instance forwards packets not addressed to itself).
- **EIP lifecycle**: Elastic IPs are allocated when the NAT instance reaches "running" and released when it reaches "stopped", both managed by the reconciliation loop. This avoids charges for unused EIPs.

## Comparison with fck-nat

nat-zero builds on top of fck-nat -- it uses the same AMI and the same iptables-based NAT approach. The difference is the orchestration layer: instead of an always-on ASG, nat-zero uses event-driven Lambda to start and stop NAT instances on demand.

```
  fck-nat (Always-On)                    nat-zero (Scale-to-Zero)
  ┌────────────────────────────┐         ┌────────────────────────────────┐
  │                            │         │                                │
  │  ┌──────────────────────┐  │         │  ┌────────────┐               │
  │  │  Auto Scaling Group  │  │         │  │ EventBridge │               │
  │  │  min=1, max=1        │  │         │  │ EC2 state   │               │
  │  └──────────┬───────────┘  │         │  │ changes     │               │
  │             │              │         │  └──────┬─────┘               │
  │             v              │         │         │                      │
  │  ┌──────────────────────┐  │         │         v                      │
  │  │  NAT Instance        │  │         │  ┌────────────┐               │
  │  │  Always running      │  │         │  │  Lambda     │               │
  │  │                      │  │         │  │  Reconciler │               │
  │  └──────────────────────┘  │         │  │  (conc = 1) │               │
  │                            │         │  └──────┬─────┘               │
  │  Cost: ~$7-8/mo           │         │         │                      │
  │  (instance + EIP 24/7)     │         │         v                      │
  │  Self-healing via ASG      │         │  ┌────────────────────┐       │
  │  No Lambda needed          │         │  │  NAT Instance      │       │
  └────────────────────────────┘         │  │  Started on demand  │       │
                                         │  │  Stopped when idle  │       │
                                         │  └────────────────────┘       │
                                         │                                │
                                         │  Cost: ~$0.80/mo (idle)       │
                                         │  EIP released when stopped     │
                                         │  Zero IPv4 charge when idle    │
                                         └────────────────────────────────┘
```

Costs per AZ, per month. Includes the [AWS public IPv4 charge](https://aws.amazon.com/blogs/aws/new-aws-public-ipv4-address-charge-public-ip-insights/) (\$3.60/mo per public IP, effective Feb 2024).

| Aspect | fck-nat | nat-zero |
|--------|---------|-------------------|
| Architecture | ASG with min=1 | Lambda + EventBridge |
| Idle cost | ~\$7-8/mo (instance + EIP 24/7) | ~\$0.80/mo (EBS only, no EIP) |
| Active cost | ~\$7-8/mo | ~\$7-8/mo (same) |
| Public IPv4 charge | \$3.60/mo always | \$0 when idle (EIP released) |
| Scale-to-zero | No | Yes |
| Self-healing | ASG replaces unhealthy | Lambda creates new on demand |
| AMI | fck-nat AMI | fck-nat AMI (same) |
| Complexity | Low (ASG only) | Moderate (Lambda + EventBridge) |
| Best for | Production 24/7 | Dev/staging, intermittent workloads |
