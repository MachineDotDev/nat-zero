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
| Complexity | Low (ASG only) | Higher (Lambda + EventBridge) |
| Best for | Production 24/7 | Dev/staging, intermittent workloads |

## Race Conditions

Because multiple Lambda invocations can fire concurrently from overlapping EventBridge events, and because the EC2 API is eventually consistent, the Lambda must handle numerous race conditions. This section catalogs each identified race, its severity, and how (or whether) it is mitigated.

### Race Condition Catalog

| ID | Description | Trigger | Mitigation | Status | Test |
|----|-------------|---------|------------|--------|------|
| R1 | **Stale sibling from EC2 eventual consistency** — dying workload still shows as `running` in DescribeInstances | Scale-down event fires before EC2 API reflects the state change | `findSiblings` excludes trigger instance ID; `maybeStopNAT` retries 3x with 2s delay | MITIGATED | `TestRace_R1` |
| R2 | **Terminated instance gone from API** — `classify` returns `ignore=true`, scale-down event lost | Instance already purged from EC2 API by the time Lambda runs | Handler detects `isTerminating(state)` + `ignore` and calls `sweepIdleNATs` to check all NATs | MITIGATED | `TestRace_R2` |
| R3 | **Retry exhaustion** — EC2 consistency takes >6s (3x2s retries), false siblings persist | Unusually long EC2 API propagation delay | None — NAT stays running until next event or sweep catches it | ACCEPTED | `TestRace_R3` |
| R4 | **Duplicate NAT creation** — two concurrent workload events both see no NAT, both call `createNAT` | Two workloads start simultaneously in the same AZ | `findNAT` detects multiple NATs, keeps the first running one, terminates extras | MITIGATED | `TestRace_R4` |
| R5 | **Start/stop overlap** — scale-up starts NAT while concurrent scale-down stops it | Workload starts while last workload is terminating | `startNAT` waits for `stopped` state then starts; brief delay but correct | ACCEPTED | `TestRace_R5` |
| R6 | **Double EIP allocation** — concurrent pending+running events both allocate EIPs | Two EventBridge events for same NAT instance arrive concurrently | `attachEIP` re-checks ENI after `AllocateAddress`; releases duplicate if EIP already present | MITIGATED | `TestRace_R6` |
| R7 | **Associate fails after re-check** — another invocation associates between re-check and `AssociateAddress` | Very tight race window between DescribeNetworkInterfaces and AssociateAddress | `attachEIP` releases allocated EIP on `AssociateAddress` failure | MITIGATED | `TestRace_R7` |
| R8 | **Disassociate on already-removed association** — EC2 auto-disassociates EIP on stop before Lambda runs | EC2 instance stop completes and auto-removes EIP before `detachEIP` | `detachEIP` catches `InvalidAssociationID.NotFound` and still releases the allocation | MITIGATED | `TestRace_R8` |
| R9 | **Orphan EIP from non-NotFound error** — `DisassociateAddress` fails with throttle/other error | API throttling during EIP detach | `detachEIP` returns early without releasing; orphan sweep on next detach cleans up | UNMITIGATED | `TestRace_R9` |
| R10 | **ENI availability timeout** — ENI never reaches `available` after terminate | EC2 delay in releasing ENI from terminated instance | `replaceNAT` proceeds with `createNAT` after timeout; launch template may fail but next event retries | ACCEPTED | `TestRace_R10` |
| R11 | **EIP orphan on NAT termination** — NAT terminated without stop cycle, `detachEIP` never fires | `replaceNAT`, spot reclaim, manual termination | Handler detects `isTerminating(state)` for NAT events and calls `sweepOrphanEIPs` to release tagged EIPs | MITIGATED | `TestRace_R11` |
| R12 | **sweepIdleNATs lacks retry** — stale sibling blocks sweep from stopping idle NAT | EC2 eventual consistency during fallback sweep path | None — sweep is itself a rare fallback (R2); retry budget would compound Lambda execution time | ACCEPTED | `TestRace_R12` |

### Why Event-Driven NAT Has Races

Traditional NAT (e.g. fck-nat with ASG) runs a single instance continuously — no concurrency, no races. nat-zero trades that simplicity for cost savings by reacting to events. This means:

1. **Multiple triggers per lifecycle**: A single workload going from `pending` → `running` fires two EventBridge events, each invoking a separate Lambda. A NAT instance similarly fires `pending` → `running` → `stopping` → `stopped`, each potentially overlapping with workload events.

2. **EC2 eventual consistency**: When EventBridge fires a `shutting-down` event, `DescribeInstances` may still return the instance as `running` for several seconds. This is the root cause of R1, R2, and R3.

3. **No distributed lock**: Lambda invocations run independently with no shared state. The EC2 API itself is the only coordination point, and it's eventually consistent.

### Sequence Diagrams

#### R1: Stale Sibling (Scale-Down Race)

```
  EventBridge          Lambda A                EC2 API
      │                    │                      │
      │ shutting-down      │                      │
      │ (i-work1)         │                      │
      ├───────────────────>│                      │
      │                    │ DescribeInstances     │
      │                    │ (findSiblings,        │
      │                    │  exclude=i-work1)     │
      │                    ├─────────────────────>│
      │                    │                      │ i-work1 still shows
      │                    │<─────────────────────┤ "running" (stale!)
      │                    │                      │ BUT excluded by ID
      │                    │                      │
      │                    │ No siblings found     │
      │                    │ StopInstances(NAT)    │
      │                    ├─────────────────────>│
      │                    │                      │
```

Without the `excludeID` parameter, i-work1 would count as a sibling and the NAT would never stop. The retry loop (R3) handles cases where a *different* workload is stale.

#### R4: Duplicate NAT Creation (Scale-Up Race)

```
  EventBridge       Lambda A              Lambda B              EC2 API
      │                │                      │                    │
      │ pending        │                      │                    │
      │ (i-work1)     │                      │                    │
      ├───────────────>│                      │                    │
      │ pending        │                      │                    │
      │ (i-work2)     │                      │                    │
      ├───────────────────────────────────────>│                    │
      │                │                      │                    │
      │                │ findNAT → nil        │                    │
      │                ├──────────────────────────────────────────>│
      │                │                      │ findNAT → nil     │
      │                │                      ├───────────────────>│
      │                │                      │                    │
      │                │ RunInstances          │                    │
      │                │ → i-nat1             │                    │
      │                ├──────────────────────────────────────────>│
      │                │                      │ RunInstances       │
      │                │                      │ → i-nat2          │
      │                │                      ├───────────────────>│
      │                │                      │                    │
      │          ┌─────┴──────────────────────┴─────┐              │
      │          │ Later: any findNAT call sees     │              │
      │          │ both i-nat1 and i-nat2           │              │
      │          │ → keeps first running NAT        │              │
      │          │ → TerminateInstances(extra)      │              │
      │          └──────────────────────────────────┘              │
```

#### R6: Double EIP Allocation (Concurrent attachEIP)

```
  EventBridge       Lambda A              Lambda B              EC2 API
      │                │                      │                    │
      │ pending        │                      │                    │
      │ (NAT)         │                      │                    │
      ├───────────────>│                      │                    │
      │ running        │                      │                    │
      │ (NAT)         │                      │                    │
      ├───────────────────────────────────────>│                    │
      │                │                      │                    │
      │                │ Check ENI: no EIP    │                    │
      │                ├──────────────────────────────────────────>│
      │                │                      │ Check ENI: no EIP │
      │                │                      ├───────────────────>│
      │                │                      │                    │
      │                │ AllocateAddress       │                    │
      │                │ → eipalloc-A         │                    │
      │                ├──────────────────────────────────────────>│
      │                │                      │ AllocateAddress    │
      │                │                      │ → eipalloc-B      │
      │                │                      ├───────────────────>│
      │                │                      │                    │
      │                │ Re-check ENI:        │                    │
      │                │ still no EIP         │                    │
      │                ├──────────────────────────────────────────>│
      │                │                      │                    │
      │                │ AssociateAddress      │                    │
      │                │ (eipalloc-A)         │                    │
      │                ├──────────────────────────────────────────>│
      │                │                      │                    │
      │                │                      │ Re-check ENI:     │
      │                │                      │ EIP-A present!    │
      │                │                      ├───────────────────>│
      │                │                      │                    │
      │                │                      │ Race detected!     │
      │                │                      │ ReleaseAddress     │
      │                │                      │ (eipalloc-B)      │
      │                │                      ├───────────────────>│
      │                │                      │                    │
```

If Lambda B's re-check also misses EIP-A (very tight window), `AssociateAddress` will fail and Lambda B releases eipalloc-B in the error handler. The orphan sweep in `detachEIP` provides a final safety net.
