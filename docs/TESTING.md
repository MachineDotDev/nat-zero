# Integration Tests

nat-zero is tested against real AWS infrastructure, not mocks. The integration test suite deploys the full module into a live AWS account, launches actual EC2 workloads, verifies that NAT instances come up with working internet connectivity, exercises scale-down and restart, then tears everything down cleanly.

These tests run in CI on every PR (triggered by adding the `integration-test` label) and take about 5 minutes end-to-end. They use [Terratest](https://terratest.gruntwork.io/) (Go) and run against `us-east-1`.

## Test Fixture

The Terraform fixture at `tests/integration/fixture/main.tf` creates:

- A **private subnet** (`172.31.128.0/24`) in the account's default VPC
- A **route table** and association for that subnet
- The **nat_zero module** (`name = "nat-test"`) wired to the private subnet and a default public subnet

All module resources (Lambda, EventBridge, ENIs, security groups, launch templates, IAM roles) are created inside the module.

## TestNatZero

A single test that exercises the full NAT lifecycle in four phases using subtests, with one `terraform apply` / `destroy` cycle. Each phase records wall-clock timing for the [TIMING SUMMARY](#timing-summary) printed at the end of the test.

### Setup

1. **Create workload IAM profile** — An IAM role/profile (`nat-test-wl-tt-<unix_ts>`) is created that allows the workload instance to call `ec2:CreateTags` on itself. This lets the user-data script tag the instance with its egress IP. The profile is deferred for deletion at the end of the test.

2. **Terraform apply** — Runs `terraform init` and `terraform apply` on the fixture. This creates the private subnet, route table, and the entire nat_zero module (Lambda, EventBridge rule, ENIs, security groups, launch template, IAM roles). `terraform destroy` is deferred for cleanup.

3. **Read Terraform outputs** — Captures `vpc_id`, `private_subnet_id`, and `lambda_function_name` from the Terraform state.

4. **Register cleanup handlers** — Defers workload instance termination and a Lambda log dumper that prints CloudWatch logs if the test fails.

### Phase 1: NATCreationAndConnectivity

Verifies the scale-up path: workload starts, NAT comes up with an EIP, workload reaches the internet through the NAT.

1. **Launch workload instance** — Launches a `t4g.nano` EC2 instance in the private subnet with a user-data script. The script retries `curl https://checkip.amazonaws.com` every 2 seconds until the NAT provides internet, then tags the instance with `EgressIP=<public_ip>`.

2. **Invoke Lambda** — Calls the Lambda with `{"instance_id": "<workload_id>", "state": "running"}`, bypassing EventBridge for reliability. This triggers `createNAT` (RunInstances).

3. **Wait for NAT with EIP** — Polls every 2 seconds for a NAT instance that is running with an EIP on its public ENI (device index 0). The EIP is attached by a separate Lambda invocation triggered by the NAT's "running" EventBridge event.

4. **Validate NAT configuration** — Asserts:
   - NAT has the `nat-zero:managed=true` tag
   - NAT has dual ENIs at device index 0 (public) and 1 (private)
   - A `0.0.0.0/0` route exists pointing to the NAT's private ENI

5. **Verify workload connectivity** — Polls for the workload's `EgressIP` tag. Asserts the egress IP matches the NAT's EIP.

### Phase 2: NATScaleDown

Verifies the scale-down path: workload terminates, NAT stops, EIP is released.

1. **Terminate workload** — Terminates the Phase 1 workload and waits for termination.

2. **Invoke Lambda (scale-down)** — Calls the Lambda with `{"instance_id": "<workload_id>", "state": "terminated"}`. This triggers `maybeStopNAT` → 3x sibling check → `stopNAT` (StopInstances).

3. **Wait for NAT stopped** — Polls until the NAT reaches `stopped` state.

4. **Invoke Lambda (detach EIP)** — Calls the Lambda with `{"instance_id": "<nat_id>", "state": "stopped"}` to simulate the EventBridge event. This triggers `detachEIP` → DisassociateAddress + ReleaseAddress.

5. **Verify EIP released** — Polls until no EIPs tagged `nat-zero:managed=true` remain.

### Phase 3: NATRestart

Verifies the restart path: new workload starts, stopped NAT is restarted with a new EIP, workload gets connectivity.

1. **Launch new workload** — New `t4g.nano` in the private subnet.

2. **Invoke Lambda (restart)** — Calls the Lambda with `{"instance_id": "<new_workload_id>", "state": "running"}`. This triggers `ensureNAT` → finds stopped NAT → `startNAT` (StartInstances).

3. **Wait for NAT with EIP** — Polls until the NAT is running with a new EIP (attached via EventBridge).

4. **Verify connectivity** — Polls for the new workload's `EgressIP` tag and confirms internet access.

### Phase 4: CleanupAction

Verifies the destroy-time cleanup action works correctly.

1. **Count EIPs** — Asserts at least one NAT EIP exists before cleanup.

2. **Invoke cleanup** — Calls the Lambda with `{"action": "cleanup"}`. The Lambda terminates all NAT instances and releases all EIPs.

3. **Verify resources cleaned** — Polls until no running NAT instances and no NAT EIPs remain.

### Teardown (deferred, runs in LIFO order)

1. Lambda log dump (only on failure)
2. Terminate test workload instances and wait
3. `terraform destroy` — removes all Terraform-managed resources
4. Delete workload IAM profile

## Timing Summary

The test prints a timing summary at the end showing wall-clock duration of each phase:

```
=== TIMING SUMMARY ===
  PHASE                                         DURATION
  ------------------------------------------------------------
  IAM profile creation                          1.234s
  Terraform init+apply                          45.678s
  Launch workload instance                      0.890s
  Lambda invoke (scale-up)                      2.345s
  Wait for NAT running with EIP                 14.567s
  Wait for workload egress IP                   25.890s
  Terminate workload instance                   30.123s
  Lambda invoke (scale-down)                    5.456s
  Wait for NAT stopped                          45.678s
  Lambda invoke (detach EIP)                    1.234s
  Wait for EIP released                         2.345s
  Launch workload instance (restart)            0.890s
  Lambda invoke (restart)                       0.567s
  Wait for NAT restarted with EIP               12.345s
  Wait for workload egress IP (restart)         20.123s
  Lambda invoke (cleanup)                       45.678s
  Wait for NAT terminated                       5.678s
  Wait for EIPs released                        1.234s
  Terraform destroy                             60.123s
  ------------------------------------------------------------
  TOTAL                                         5m15.678s
=== END TIMING SUMMARY ===
```

Key timings to watch:
- **Wait for NAT running with EIP**: How long from Lambda invocation to NAT with internet (cold create). Expect ~14 s.
- **Wait for NAT restarted with EIP**: Same metric for restart path. Expect ~12 s.
- **Lambda invoke (scale-down)**: Includes the 3x sibling retry (~4 s). Expect ~5 s.

## TestNoOrphanedResources

Runs after the main test. Searches for AWS resources with the `nat-test` prefix that were left behind by failed test runs. Checks for:

- Subnet with test CIDR (`172.31.128.0/24`)
- ENIs, security groups, and launch templates named `nat-test-*`
- EventBridge rules named `nat-test-*`
- Lambda function `nat-test-nat-zero`
- CloudWatch log group `/aws/lambda/nat-test-*`
- IAM roles and instance profiles prefixed `nat-test`
- EIPs tagged `nat-zero:managed=true`

If any are found, the test fails and lists them for manual cleanup.

## Why the Cleanup Action Matters

NAT instances and EIPs are created by the Lambda at runtime, not by Terraform. During `terraform destroy`, Terraform doesn't know these exist. Without the cleanup action:

1. `terraform destroy` tries to delete ENIs
2. ENIs are still attached to running NAT instances
3. Deletion fails, leaving the entire stack half-destroyed

The `aws_lambda_invocation.cleanup` resource invokes the Lambda with `{"action": "cleanup"}` during destroy, which terminates instances and releases EIPs before Terraform tries to remove ENIs and security groups.

## Config Version Replacement

The Lambda tracks a `CONFIG_VERSION` hash (derived from AMI, instance type, market type, and volume size). When a workload scales up and the existing NAT has an outdated `ConfigVersion` tag, the Lambda:

1. Terminates the outdated NAT instance
2. Waits for the ENIs to become available
3. Creates a new NAT instance with the current config

This ensures AMI or instance type changes propagate to NAT instances without manual intervention.

## Running Locally

```bash
cd nat-zero/tests/integration
go test -v -timeout 30m
```

Requires AWS credentials with permissions to create/destroy all resources in the fixture (EC2, IAM, Lambda, EventBridge, CloudWatch).
