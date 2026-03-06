# Performance and Cost

All measurements from real integration tests in us-east-1 with `t4g.nano` instances and 128 MB Lambda memory.

## Startup Latency

| Scenario | Time to connectivity |
|----------|---------------------|
| First workload (cold create) | **~10.7 s** |
| Restart from stopped | **~8.5 s** |
| NAT already running | **Instant** |

### Cold create breakdown

```
 0.0 s   Workload enters "pending"
 0.3 s   EventBridge delivers event
 0.4 s   Lambda cold start (55 ms init)
 0.9 s   Reconcile: observe state, decide to create NAT
 2.3 s   RunInstances returns — NAT is "pending"
         Lambda returns.

~8.0 s   NAT reaches "running" (EC2 boot + NAT config)
~8.3 s   EventBridge delivers NAT "running" event
~8.9 s   Lambda: allocate EIP + associate (~3 s)

~10.7 s  Workload can reach the internet
```

The ~8 second gap is EC2 instance lifecycle (placement, OS boot, iptables config) — not the Lambda.

### Restart breakdown

```
 0.0 s   New workload enters "pending"
 0.4 s   Lambda finds stopped NAT → StartInstances
         Lambda returns.

~6.0 s   NAT reaches "running" (faster than cold create)
~6.3 s   Lambda: allocate EIP + associate

~8.5 s   Workload can reach the internet
```

Restart is ~2 seconds faster — `StartInstances` skips AMI resolution and launch template processing.

## Lambda Execution

| Metric | Value |
|--------|-------|
| Cold start (Init Duration) | 55 ms |
| Typical invocation | 400-600 ms |
| EIP allocation + association | ~3 s |
| Peak memory | 29-30 MB |
| Lambda memory allocation | 128 MB |

The Lambda is a compiled Go ARM64 binary on `provided.al2023`. No interpreter, no framework — just direct AWS SDK calls.

## Scale-Down Timing

```
 0.0 s   Last workload enters "shutting-down"
 0.3 s   EventBridge delivers event
 0.5 s   Lambda: reconcile → workloads=0, NAT running → stopNAT
         Lambda returns.

~10 s    NAT reaches "stopped"
~10.3 s  EventBridge delivers NAT "stopped" event
~10.5 s  Lambda: release EIP

~11 s    EIP released, no IPv4 charge
```

## Cost

Per AZ, per month. us-east-1 on-demand prices. Includes the [$3.60/month public IPv4 charge](https://aws.amazon.com/blogs/aws/new-aws-public-ipv4-address-charge-public-ip-insights/).

| State | nat-zero | fck-nat | NAT Gateway |
|-------|----------|---------|-------------|
| **Idle** | **~$0.80** | ~$7-8 | ~$36+ |
| **Active** | ~$7-8 | ~$7-8 | ~$36+ |

**Idle**: EBS volume only (~$0.80 for 2 GB gp3). No instance, no EIP.

**Active**: t4g.nano ($3.07) + EIP ($3.60) + EBS ($0.80) = ~$7.50.

### Instance types

| Type | Network | $/month (24x7) | $/month (12hr/day) |
|------|---------|:--------------:|:------------------:|
| **t4g.nano** (default) | Up to 5 Gbps | $3.07 | $1.53 |
| t4g.micro | Up to 5 Gbps | $6.13 | $3.07 |
| t4g.small | Up to 5 Gbps | $12.26 | $6.13 |
| c7gn.medium | Up to 25 Gbps | $45.55 | $22.78 |

Spot pricing typically offers 60-70% savings. Use `market_type = "spot"`.

**t4g.nano** handles typical dev/staging traffic. Instance type does not affect startup time — the bottleneck is EC2 lifecycle, not CPU.
