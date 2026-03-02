# Testing

nat-zero is integration-tested against real AWS infrastructure on every PR. The test deploys the full module, exercises the complete NAT lifecycle, then tears everything down.

## Running Tests

```bash
# Unit tests (Lambda logic)
cd cmd/lambda && go test -v -race ./...

# Integration tests (requires AWS credentials)
cd tests/integration && go test -v -timeout 30m
```

Integration tests require AWS credentials with permissions to manage EC2, IAM, Lambda, EventBridge, and CloudWatch resources.

## Integration Test Lifecycle

The test uses [Terratest](https://terratest.gruntwork.io/) with a single `terraform apply` / `destroy` cycle and four phases:

### Phase 1: NAT Creation and Connectivity

1. Deploy fixture (private subnet + nat-zero module in default VPC)
2. Launch workload instance in private subnet
3. EventBridge fires workload state change → Lambda creates NAT instance
4. Wait for NAT running with EIP attached
5. Verify workload's egress IP matches NAT's EIP

### Phase 2: Scale-Down

1. Terminate workload
2. EventBridge fires workload terminated → Lambda stops NAT
3. Wait for NAT stopped
4. EventBridge fires NAT stopped → Lambda releases EIP
5. Verify no EIPs remain

### Phase 3: Restart

1. Launch new workload
2. EventBridge fires workload state change → Lambda restarts stopped NAT
3. Wait for NAT running with new EIP
4. Verify connectivity

### Phase 4: Cleanup Action

1. Invoke Lambda with `{action: "cleanup"}`
2. Verify all NAT instances terminated and EIPs released

### Teardown

`terraform destroy` removes all Terraform-managed resources. The cleanup action (Phase 4) ensures Lambda-created NAT instances are terminated first, so ENI deletion succeeds.

## CI

Integration tests run in GitHub Actions when the `integration-test` label is added to a PR. They use OIDC to assume an AWS role in a dedicated test account.

- Concurrency: one test at a time (`cancel-in-progress: false`)
- Timeout: 15 minutes
- Region: us-east-1

## Orphan Detection

`TestNoOrphanedResources` runs after the main test and checks for leftover AWS resources with the `nat-test` prefix (subnets, ENIs, security groups, Lambda functions, IAM roles, EIPs). If any are found, it fails and lists them for manual cleanup.

## Config Version Replacement

The Lambda tags NAT instances with a `ConfigVersion` hash (AMI + instance type + market type + volume size + encryption). When the config changes and a workload triggers reconciliation, the Lambda terminates the outdated NAT and creates a replacement. The integration test doesn't exercise this path directly, but it's covered by unit tests.
