# Architecture

## High-Level Overview

The nat-zero module provides event-driven, scale-to-zero NAT instances for AWS. It uses EventBridge to capture EC2 instance state changes and a Lambda function to orchestrate the NAT instance lifecycle.

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
  │  └──────────────────┘    └────────┬─────────┘                    │
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

## Event Flow

Every EC2 state change in the account fires an EventBridge event. The Lambda classifies each instance as: **ignore** (wrong VPC / ignore tag), **NAT** (has `nat-zero:managed=true` tag), or **workload** (everything else).

### Scale-up: Workload starts, NAT created

```
1. Workload → pending
   Lambda: classify → workload, starting
   Action: findNAT → none → createNAT (RunInstances)

2. NAT → pending
   Lambda: classify → NAT, starting
   Action: attachEIP → wait for running... (not yet, will retry on next event)

3. NAT → running
   Lambda: classify → NAT, starting
   Action: attachEIP → instance running → allocate EIP → associate to public ENI
   Result: NAT has internet via EIP ✓

4. Workload → running
   Lambda: classify → workload, starting
   Action: findNAT → found running NAT → no-op
```

### Scale-down: Workload terminates, NAT stopped

```
1. Workload → shutting-down
   Lambda: classify → workload, terminating
   Action: findNAT → found running NAT → findSiblings → none (3x retry) → stopNAT

2. NAT → stopping
   Lambda: classify → NAT, stopping
   Action: detachEIP → wait for stopped... (not yet)

3. NAT → stopped
   Lambda: classify → NAT, stopping
   Action: detachEIP → instance stopped → disassociate EIP → release EIP
   Result: NAT idle, no EIP charge ✓

4. Workload → terminated
   Lambda: classify → workload, terminating
   Action: findNAT → found stopped NAT → NAT not in starting state → no-op
```

### Restart: New workload starts, stopped NAT restarted

```
1. New workload → pending
   Lambda: classify → workload, starting
   Action: findNAT → found stopped NAT → startNAT (wait stopped → StartInstances)

2. NAT → pending
   Lambda: classify → NAT, starting
   Action: attachEIP → wait for running... (not yet)

3. NAT → running
   Lambda: classify → NAT, starting
   Action: attachEIP → instance running → allocate EIP → associate to public ENI
   Result: NAT has internet via EIP ✓

4. New workload → running
   Lambda: classify → workload, starting
   Action: findNAT → found running NAT → no-op
```

### Terraform destroy

```
Terraform invokes Lambda with {action: "cleanup"}
Action: find all NAT instances → terminate → release all EIPs
Result: clean state for ENI/SG destruction ✓
```

### Why this is safe from races

- **EIP attach is idempotent**: `attachEIP` checks if the ENI already has an EIP before allocating. Multiple concurrent `running` events for the same NAT are harmless.
- **EIP detach is idempotent**: `detachEIP` checks if the ENI has an association before releasing.
- **NAT dedup**: `findNAT` terminates extras if multiple NATs exist in one AZ.
- **Workload handlers never touch EIPs**: Only NAT events manage EIPs. Workload events only start/stop/create NAT instances.

## Scale-Up Sequence

```
  Workload        EventBridge       Lambda          EC2 API         NAT Instance
  Instance                                                          (per AZ)
     │                │                │                │                │
     │ state:"pending"│                │                │                │
     ├───────────────>│                │                │                │
     │                │  invoke        │                │                │
     │                ├───────────────>│                │                │
     │                │                │                │                │
     │                │                │ describe_instances(id)          │
     │                │                ├───────────────>│                │
     │                │                │<───────────────┤                │
     │                │                │                │                │
     │                │                │  Check: VPC matches? Not ignored? Not NAT?
     │                │                │                │                │
     │                │                │ describe_instances(NAT tag, AZ, VPC)
     │                │                ├───────────────>│                │
     │                │                │<───────────────┤                │
     │                │                │                │                │
     │          ┌─────┴────────────────┴────────────────┴────────┐      │
     │          │ IF no NAT instance:                            │      │
     │          │   describe_launch_templates(AZ, VPC)           │      │
     │          │   describe_images(fck-nat pattern)             │      │
     │          │   run_instances(template, AMI)  ──────────────>│──────>│ Created
     │          │                                                │      │
     │          │ IF NAT stopped:                                │      │
     │          │   start_instances(nat_id)  ───────────────────>│──────>│ Starting
     │          │                                                │      │
     │          │ IF NAT already running:                        │      │
     │          │   No action needed                             │      │
     │          └─────┬────────────────┬────────────────┬────────┘      │
     │                │                │                │                │
     │                │                │                │  state:"running"
     │                │  invoke        │                │<───────────────┤
     │                ├───────────────>│                │                │
     │                │                │ allocate_address                │
     │                │                │ associate_address               │
     │                │                ├───────────────>│                │
     │                │                │                │──── EIP ──────>│
     │                │                │                │           NAT ready
```

## Scale-Down Sequence

```
  Workload        EventBridge       Lambda          EC2 API         NAT Instance
  Instance                                                          (per AZ)
     │                │                │                │                │
     │state:"stopping"│                │                │                │
     ├───────────────>│                │                │                │
     │                │  invoke        │                │                │
     │                ├───────────────>│                │                │
     │                │                │                │                │
     │                │                │ describe_instances(id)          │
     │                │                ├───────────────>│                │
     │                │                │  Check: VPC, not ignored, not NAT
     │                │                │                │                │
     │                │     ┌──────────┴──────────┐     │                │
     │                │     │ Retry loop (3x, 2s) │     │                │
     │                │     │  describe_instances  │     │                │
     │                │     │  (AZ, VPC, running)  ├───>│                │
     │                │     │  filter out NAT +    │<───┤                │
     │                │     │  ignored instances   │     │                │
     │                │     └──────────┬──────────┘     │                │
     │                │                │                │                │
     │          ┌─────┴────────────────┴────────────────┴────────┐      │
     │          │ IF no siblings remain:                         │      │
     │          │   stop_instances(nat_id) ─────────────────────>│──────>│ Stopping
     │          │                                                │      │
     │          │ IF siblings still running:                     │      │
     │          │   Keep NAT running, no action                  │      │
     │          └─────┬────────────────┬────────────────┬────────┘      │
     │                │                │                │                │
     │                │                │                │ state:"stopped"
     │                │  invoke        │                │<───────────────┤
     │                ├───────────────>│                │                │
     │                │                │ disassociate_address            │
     │                │                │ release_address                 │
     │                │                ├───────────────>│                │
     │                │                │                │  EIP released  │
     │                │                │                │                │
     │                │                │                │   NAT stopped  │
     │                │                │                │   Cost: ~$0.80/mo
     │                │                │                │   (EBS only)   │
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
- **EIP lifecycle**: Elastic IPs are allocated when the NAT instance reaches "running" and released when it reaches "stopped", both via EventBridge events. This avoids charges for unused EIPs.

## Comparison with fck-nat

This module complements fck-nat by adding scale-to-zero capability.

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
  │  │                      │  │         │  │  Orchestr.  │               │
  │  └──────────────────────┘  │         │  └──────┬─────┘               │
  │                            │         │         │                      │
  │  Cost: ~$7-8/mo           │         │         v                      │
  │  (instance + EIP 24/7)     │         │  ┌────────────────────┐       │
  │  Self-healing via ASG      │         │  │  NAT Instance      │       │
  │  No Lambda needed          │         │  │  Started on demand  │       │
  └────────────────────────────┘         │  │  Stopped when idle  │       │
                                         │  └────────────────────┘       │
                                         │                                │
                                         │  Cost: ~$0.80/mo (idle)       │
                                         │  EIP released when stopped     │
                                         │  Zero IPv4 charge when idle    │
                                         └────────────────────────────────┘
```

Costs per AZ, per month. Includes the [AWS public IPv4 charge](https://aws.amazon.com/blogs/aws/new-aws-public-ipv4-address-charge-public-ip-insights/) ($3.60/mo per public IP, effective Feb 2024).

| Aspect | fck-nat | nat-zero |
|--------|---------|-------------------|
| Architecture | ASG with min=1 | Lambda + EventBridge |
| Idle cost | ~$7-8/mo (instance + EIP 24/7) | ~$0.80/mo (EBS only, no EIP) |
| Active cost | ~$7-8/mo | ~$7-8/mo (same) |
| Public IPv4 charge | $3.60/mo always | $0 when idle (EIP released) |
| Scale-to-zero | No | Yes |
| Self-healing | ASG replaces unhealthy | Lambda creates new on demand |
| AMI | fck-nat AMI | fck-nat AMI (same) |
| Complexity | Low (ASG only) | Higher (Lambda + EventBridge) |
| Best for | Production 24/7 | Dev/staging, intermittent workloads |
