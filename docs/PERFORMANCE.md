# Performance and Cost

nat-zero's orchestrator Lambda was rewritten from Python 3.11 to Go, compiled to a native ARM64 binary running on the `provided.al2023` runtime. The result: **90% faster cold starts**, 69% less memory, and faster end-to-end execution. All measurements below are from real integration tests running in us-east-1 with `t4g.nano` instances.

## Startup Latency

**First workload in an AZ — NAT created from scratch: ~15 seconds to connectivity.**

```
 0.0 s  Workload instance enters "running" state
 0.3 s  EventBridge delivers workload event to Lambda
 0.4 s  Lambda cold start completes (55-67 ms init)
 0.9 s  Lambda classifies instance, checks for existing NAT
 2.3 s  Lambda calls RunInstances — NAT instance is now "pending"
        Lambda returns. EIP will be attached separately via EventBridge.

~12  s  NAT instance reaches "running" state
        (fck-nat AMI boots, configures iptables, attaches ENIs)
~12.3 s EventBridge delivers NAT "running" event to Lambda
~12.5 s Lambda allocates EIP and associates to public ENI

~15  s  Workload can reach the internet via NAT
```

The ~10 second gap between `RunInstances` and NAT reaching "running" is spent on EC2 placement (~2-3 s), OS boot (~3-4 s), and fck-nat network configuration (~2-3 s). This is consistent across all instance types tested — the bottleneck is EC2's instance lifecycle, not CPU or memory.

### Restart from stopped state: ~12 seconds

When a stopped NAT is restarted (new workload arrives after previous scale-down):

```
 0.0 s  New workload enters "running" state
 0.3 s  EventBridge delivers workload event to Lambda
 0.4 s  Lambda classifies, finds stopped NAT → calls StartInstances
        Lambda returns.

~10  s  NAT instance reaches "running" state (reboot from stopped)
~10.3 s EventBridge delivers NAT "running" event to Lambda
~10.5 s Lambda allocates EIP and associates to public ENI

~12  s  Workload can reach the internet via NAT
```

Restart is ~3 seconds faster than cold create because `StartInstances` is faster than `RunInstances` and skips AMI/launch template resolution.

### NAT already running: instant

If a NAT is already running in the AZ (e.g. second workload starts), no action is needed. The route table already points to the NAT's private ENI, so connectivity is immediate.

### Summary table

| Scenario | Lambda Duration | Time to NAT Running + EIP | Time to Connectivity |
|----------|-----------------|--------------------------|---------------------|
| First workload (cold create) | ~2 s | ~12 s | **~15 s** |
| NAT already running | — | — | **0 s** |
| Restart from stopped | ~0.5 s | ~10 s | **~12 s** |
| Config outdated (replace) | ~60+ s | ~12 s | **~70 s** |

## Scale-Down Timing

When the last workload in an AZ stops or terminates:

```
 0.0 s  Last workload enters "shutting-down" state
 0.3 s  EventBridge delivers workload event to Lambda
 0.4 s  Lambda classifies, finds NAT, checks for sibling workloads
 4.5 s  No siblings after 3 retries (2 s apart) → calls StopInstances
        Lambda returns.

~15  s  NAT instance reaches "stopped" state
~15.3 s EventBridge delivers NAT "stopped" event to Lambda
~15.5 s Lambda disassociates and releases EIP

~16  s  EIP released, no IPv4 charge
```

The 3x retry with 2-second delays (~4 seconds total) is a safety margin to prevent flapping when instances are being replaced. The Lambda only checks for `pending` or `running` siblings — stopping or terminated instances don't count.

## Lambda Execution

The Lambda is a compiled Go binary on the `provided.al2023` runtime with 256 MB memory.

| Metric | Duration | Notes |
|--------|----------|-------|
| Cold start (Init Duration) | 55-67 ms | Go binary; no interpreter overhead |
| classify (DescribeInstances) | 100-700 ms | Single API call; varies with API latency |
| findNAT (DescribeInstances) | 65-100 ms | Filter by tag + AZ + VPC |
| resolveAMI (DescribeImages) | 60-120 ms | Sorts by creation date |
| resolveLT (DescribeLaunchTemplates) | 70-100 ms | Filter by AZ + VPC tags |
| RunInstances | 1.2-1.6 s | AWS API latency |
| attachEIP (Allocate + Associate) | 150-300 ms | Includes idempotency check |
| detachEIP (Disassociate + Release) | 100-200 ms | Includes idempotency check |
| **Scale-up handler total** | **~2 s** | classify + findNAT + createNAT |
| **Scale-down handler total** | **~5 s** | classify + findNAT + 3x findSiblings + stopNAT |
| **attachEIP handler total** | **~0.5 s** | classify + waitForState + attachEIP |
| **detachEIP handler total** | **~0.5 s** | classify + waitForState + detachEIP |

### Why Go?

The original Lambda was written in Python 3.11. It worked, but Python's interpreter overhead meant a 667 ms cold start and 98 MB memory footprint -- meaningful for a function that might be invoked dozens of times during a busy scaling period.

Rewriting in Go and compiling to a native binary eliminated the interpreter entirely:

| Metric | Python 3.11 (128 MB) | Go (256 MB) | Improvement |
|--------|----------------------|-------------|-------------|
| Cold start | 667 ms | 55-67 ms | **~90% faster** |
| Handler total (scale-up) | 2,439 ms | ~2,000 ms | **~18% faster** |
| Max memory used | 98 MB | 30 MB | **69% less** |

The Go binary is ~4 MB, boots in under 70 ms, and the entire scale-up path completes in about 2 seconds. For a Lambda that runs on every EC2 state change in your account, that matters.

## What This Means for Your Workloads

- **First workload takes ~15 seconds to get internet.** Design startup scripts to retry outbound connections (e.g. `apt update`, `pip install`, `curl`). Most package managers already retry.
- **Subsequent workloads are instant.** Once a NAT is running in an AZ, the route table already points to it.
- **Restart after idle is ~12 seconds.** If your workloads run sporadically (CI jobs, cron tasks), expect a ~12 second delay when the first job starts after an idle period.
- **Scale-down is conservative.** The Lambda waits 6 seconds (3 retries) before stopping a NAT, preventing flapping during instance replacements.
- **Instance type doesn't affect startup time.** The ~10 second EC2 boot time is the same for `t4g.nano` and `c7gn.medium`.

## Cost

Per AZ, per month. All prices are us-east-1 on-demand. Includes the [AWS public IPv4 charge](https://aws.amazon.com/blogs/aws/new-aws-public-ipv4-address-charge-public-ip-insights/) (\$0.005/hr per public IP).

### Idle vs active

| State | nat-zero | fck-nat | NAT Gateway |
|-------|----------|---------|-------------|
| **Idle** (no workloads) | **~\$0.80** | ~\$7-8 | ~\$36+ |
| **Active** (workloads running) | ~\$7-8 | ~\$7-8 | ~\$36+ |

**Idle breakdown**: EBS volume only (~\$0.80/mo for 2 GB gp3). No instance running, no EIP allocated.

**Active breakdown**: t4g.nano instance (\$3.07/mo) + EIP (\$3.60/mo) + EBS (\$0.80/mo) = ~\$7.50/mo.

The key difference: nat-zero **releases the EIP when idle**, saving the \$3.60/mo public IPv4 charge that fck-nat and NAT Gateway pay 24/7.

### Instance type options

| Instance Type | vCPUs | RAM | Network | \$/hour | \$/month (24x7) | \$/month (12hr/day) |
|---------------|-------|-----|---------|--------|---------------|-------------------|
| **t4g.nano** (default) | 2 | 0.5 GiB | Up to 5 Gbps | \$0.0042 | \$3.07 | \$1.53 |
| t4g.micro | 2 | 1 GiB | Up to 5 Gbps | \$0.0084 | \$6.13 | \$3.07 |
| t4g.small | 2 | 2 GiB | Up to 5 Gbps | \$0.0168 | \$12.26 | \$6.13 |
| c7gn.medium | 1 | 2 GiB | Up to 25 Gbps | \$0.0624 | \$45.55 | \$22.78 |

Spot pricing typically offers 60-70% savings on t4g instances. Use `market_type = "spot"` to enable.

### Choosing an instance type

**t4g.nano** (default) is right for most workloads:
- Handles typical dev/staging NAT traffic
- Burstable up to 5 Gbps with CPU credits
- \$3/month on-demand, ~\$1/month on spot

**t4g.micro / t4g.small** — consider if you need sustained throughput beyond t4g.nano's baseline or workloads transfer large volumes consistently.

**c7gn.medium** — consider if you need consistently high network throughput (up to 25 Gbps). At \$45/month it's still cheaper than NAT Gateway for most data transfer patterns.

Instance type does **not** affect startup time (~12 s regardless), only maximum sustained throughput and monthly cost.
