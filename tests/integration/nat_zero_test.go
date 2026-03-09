package test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudwatchevents"
	"github.com/aws/aws-sdk-go/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/iam"
	"github.com/aws/aws-sdk-go/service/lambda"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/gruntwork-io/terratest/modules/retry"
	"github.com/gruntwork-io/terratest/modules/terraform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	awsRegion   = "us-east-1"
	natTagKey   = "nat-zero:managed"
	natTagValue = "true"
	testTagKey  = "TerratestRun"
)

// userDataScript generates a base64-encoded userdata script that curls
// checkip.amazonaws.com and sends the result to the given SQS queue URL.
func userDataScript(queueURL string) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf(`#!/bin/bash
BOOT_MS=$(($(date +%%s%%N)/1000000))
TOKEN=$(curl -sf -X PUT "http://169.254.169.254/latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 60")
IID=$(curl -sf -H "X-aws-ec2-metadata-token: $TOKEN" http://169.254.169.254/latest/meta-data/instance-id)
REGION=$(curl -sf -H "X-aws-ec2-metadata-token: $TOKEN" http://169.254.169.254/latest/meta-data/placement/region)
for i in $(seq 1 60); do
  IP=$(curl -sf --max-time 5 https://checkip.amazonaws.com) && break
  sleep 2
done
CONNECTED_MS=$(($(date +%%s%%N)/1000000))
if [ -n "$IP" ]; then
  MSG=$(printf '{"instance_id":"%%s","egress_ip":"%%s","boot_ms":%%d,"connected_ms":%%d}' "$IID" "$IP" "$BOOT_MS" "$CONNECTED_MS")
  aws sqs send-message --queue-url "%s" --message-body "$MSG" --region "$REGION"
fi
`, queueURL)))
}

// phase records the name and duration of a test phase for the timing summary.
type phase struct {
	name     string
	duration time.Duration
}

// TestNatZero exercises the full NAT lifecycle: deploy, NAT creation,
// connectivity, scale-down, restart, cleanup action, and terraform destroy.
func TestNatZero(t *testing.T) {
	runID := fmt.Sprintf("tt-%d", time.Now().Unix())
	moduleName := fmt.Sprintf("nat-test-%s", runID)
	sess := session.Must(session.NewSession(&aws.Config{Region: aws.String(awsRegion)}))
	ec2Client := ec2.New(sess)
	iamClient := iam.New(sess)
	lambdaClient := lambda.New(sess)
	sqsClient := sqs.New(sess)

	// Timing infrastructure — records duration of each test phase.
	var phases []phase
	record := func(name string, d time.Duration) {
		phases = append(phases, phase{name, d})
		t.Logf("[TIMER] %-45s %s", name, d.Round(time.Millisecond))
	}
	var encryptRootVolume string
	defer func() {
		t.Log("")
		t.Log("=== TIMING SUMMARY ===")
		encryptLabel := "enabled"
		if encryptRootVolume == "false" {
			encryptLabel = "disabled"
		}
		t.Logf("  EBS Encryption: %s", encryptLabel)
		t.Log("")
		t.Logf("  %-45s %s", "PHASE", "DURATION")
		t.Log("  " + strings.Repeat("-", 60))
		var total time.Duration
		for _, p := range phases {
			total += p.duration
			t.Logf("  %-45s %s", p.name, p.duration.Round(time.Millisecond))
		}
		t.Log("  " + strings.Repeat("-", 60))
		t.Logf("  %-45s %s", "TOTAL", total.Round(time.Millisecond))
		t.Log("=== END TIMING SUMMARY ===")
	}()

	// Create workload IAM profile first — propagates while Terraform applies.
	iamStart := time.Now()
	profileName := createWorkloadProfile(t, iamClient, runID)
	record("IAM profile creation", time.Since(iamStart))
	defer deleteWorkloadProfile(t, iamClient, runID)

	// Create SQS queue for workload connectivity reporting.
	queueName := fmt.Sprintf("nat-test-%s", runID)
	createOut, err := sqsClient.CreateQueue(&sqs.CreateQueueInput{
		QueueName: aws.String(queueName),
	})
	require.NoError(t, err)
	queueURL := aws.StringValue(createOut.QueueUrl)
	t.Logf("Created SQS queue: %s", queueURL)
	defer func() {
		sqsClient.DeleteQueue(&sqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)})
		t.Logf("Deleted SQS queue %s", queueName)
	}()

	initialNatAMI := strings.TrimSpace(os.Getenv("NAT_ZERO_TEST_NAT_AMI_ID"))
	updatedNatAMI := strings.TrimSpace(os.Getenv("NAT_ZERO_TEST_UPDATED_NAT_AMI_ID"))
	tfVars := map[string]interface{}{
		"name": moduleName,
	}
	t.Logf("Integration module name: %s", moduleName)
	if initialNatAMI != "" {
		tfVars["nat_ami_id"] = initialNatAMI
		t.Logf("Initial NAT AMI override: %s", initialNatAMI)
	}
	if updatedNatAMI != "" {
		t.Logf("Updated NAT AMI target: %s", updatedNatAMI)
	}

	opts := terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		TerraformDir: "./fixture",
		NoColor:      true,
		Vars:         tfVars,
	})
	defer func() {
		destroyStart := time.Now()
		terraform.Destroy(t, opts)
		record("Terraform destroy", time.Since(destroyStart))
	}()
	tfStart := time.Now()
	terraform.InitAndApply(t, opts)
	record("Terraform init+apply", time.Since(tfStart))

	vpcID := terraform.Output(t, opts, "vpc_id")
	privateSubnet := terraform.Output(t, opts, "private_subnet_id")
	lambdaName := terraform.Output(t, opts, "lambda_function_name")
	encryptRootVolume = terraform.Output(t, opts, "encrypt_root_volume")
	t.Logf("VPC: %s, private subnet: %s, Lambda: %s", vpcID, privateSubnet, lambdaName)

	// Terminate test workload instances before terraform destroy.
	defer func() {
		t.Log("Terminating test workload instances...")
		out, err := ec2Client.DescribeInstances(&ec2.DescribeInstancesInput{
			Filters: []*ec2.Filter{
				{Name: aws.String(fmt.Sprintf("tag:%s", testTagKey)), Values: []*string{aws.String(runID)}},
				{Name: aws.String("instance-state-name"), Values: []*string{
					aws.String("pending"), aws.String("running"),
					aws.String("stopping"), aws.String("stopped"),
				}},
			},
		})
		if err != nil {
			t.Logf("Warning: describe instances: %v", err)
			return
		}
		var ids []*string
		for _, r := range out.Reservations {
			for _, i := range r.Instances {
				ids = append(ids, i.InstanceId)
			}
		}
		if len(ids) > 0 {
			t.Logf("Terminating %d test workload instances", len(ids))
			ec2Client.TerminateInstances(&ec2.TerminateInstancesInput{InstanceIds: ids})
			ec2Client.WaitUntilInstanceTerminated(&ec2.DescribeInstancesInput{InstanceIds: ids})
		}
	}()

	// Dump Lambda CloudWatch logs before destroy for diagnostics.
	cwClient := cloudwatchlogs.New(sess)
	logGroup := fmt.Sprintf("/aws/lambda/%s", lambdaName)
	defer func() {
		dumpLambdaLogs(t, cwClient, logGroup)
	}()

	amiID := getLatestAL2023AMI(t, ec2Client)

	// Shared across phases — set by Phase 1, used by Phase 2.
	var activeWorkloadID string
	runPhase := func(name string, fn func(t *testing.T)) bool {
		if t.Run(name, fn) {
			return true
		}
		t.Logf("Phase %s failed, aborting remaining phases so deferred cleanup can run", name)
		return false
	}

	// ── Phase 1: NAT creation and connectivity ──────────────────────────
	// Launch a workload and let EventBridge trigger the Lambda automatically.

	if !runPhase("NATCreationAndConnectivity", func(t *testing.T) {
		wlStart := time.Now()
		activeWorkloadID = launchWorkload(t, ec2Client, privateSubnet, amiID, runID, profileName, queueURL)
		record("Launch workload instance", time.Since(wlStart))
		t.Logf("Launched workload %s in VPC %s", activeWorkloadID, vpcID)

		// EventBridge fires when the workload goes pending/running,
		// triggering the Lambda to create a NAT and attach an EIP.
		t.Log("Waiting for NAT to be running with EIP (via EventBridge)...")
		start := time.Now()
		natInstance := waitForRunningNATWithEIP(t, ec2Client, vpcID, "NAT running with EIP")
		natUpTime := time.Since(start)
		record("Wait for NAT running with EIP", natUpTime)
		t.Logf("NAT up with EIP in %s", natUpTime.Round(time.Millisecond))

		natEIP := natPublicIP(natInstance)
		require.NotEmpty(t, natEIP, "NAT should have a public IP")

		// Validate NAT tags.
		hasScalingTag := false
		for _, tag := range natInstance.Tags {
			if aws.StringValue(tag.Key) == natTagKey && aws.StringValue(tag.Value) == natTagValue {
				hasScalingTag = true
				break
			}
		}
		assert.True(t, hasScalingTag, "NAT missing tag %s=%s", natTagKey, natTagValue)

		// Validate dual ENIs (public + private).
		eniIndices := map[int64]bool{}
		for _, eni := range natInstance.NetworkInterfaces {
			eniIndices[aws.Int64Value(eni.Attachment.DeviceIndex)] = true
		}
		assert.True(t, eniIndices[0] && eniIndices[1], "NAT should have ENIs at device index 0 and 1")

		assertRouteTableEntry(t, ec2Client, vpcID, natInstance)

		// Wait for workload to report its egress IP via SQS.
		t.Log("Waiting for workload connectivity check (SQS)...")
		egressStart := time.Now()
		msg := waitForEgress(t, sqsClient, queueURL, 4*time.Minute)
		record("Wait for workload egress IP", time.Since(egressStart))
		if msg.ConnectedMs > 0 && msg.BootMs > 0 {
			t.Logf("Workload-measured connectivity latency: %dms", msg.ConnectedMs-msg.BootMs)
		}
		assert.Equal(t, natEIP, msg.EgressIP,
			"workload egress IP should match NAT EIP")
		t.Logf("Confirmed: workload egresses via NAT EIP %s", natEIP)
	}) {
		return
	}

	// ── Phase 2: NAT scale-down ─────────────────────────────────────────
	// Terminate the workload and let EventBridge drive the full
	// scale-down flow: stop NAT, then detach/release EIP.

	if !runPhase("NATScaleDown", func(t *testing.T) {
		require.NotEmpty(t, activeWorkloadID, "Phase 1 must set activeWorkloadID")

		// Terminate the workload instance. EventBridge fires shutting-down
		// and terminated events which trigger the Lambda to stop the NAT.
		t.Log("Terminating workload to trigger NAT scale-down...")
		termStart := time.Now()
		_, err := ec2Client.TerminateInstances(&ec2.TerminateInstancesInput{
			InstanceIds: []*string{aws.String(activeWorkloadID)},
		})
		require.NoError(t, err)
		record("Terminate workload instance", time.Since(termStart))
		activeWorkloadID = ""

		// Wait for NAT to reach stopped state.
		t.Log("Waiting for NAT to stop (via EventBridge)...")
		stopStart := time.Now()
		retry.DoWithRetry(t, "NAT stopped", 100, 2*time.Second, func() (string, error) {
			nats := findNATInstancesInState(t, ec2Client, vpcID,
				[]string{"pending", "running", "stopping", "stopped"})
			for _, n := range nats {
				state := aws.StringValue(n.State.Name)
				if state == "stopped" {
					return "OK", nil
				}
				if state == "stopping" {
					return "", fmt.Errorf("NAT still stopping")
				}
				return "", fmt.Errorf("NAT in unexpected state: %s", state)
			}
			return "", fmt.Errorf("no NAT instances found")
		})
		natStopTime := time.Since(stopStart)
		record("Wait for NAT stopped", natStopTime)
		t.Logf("NAT stopped in %s", natStopTime.Round(time.Second))

		// EventBridge fires the NAT's stopping/stopped events which trigger
		// the Lambda to detach and release the EIP automatically.
		t.Log("Verifying EIP released (via EventBridge)...")
		eipStart := time.Now()
		retry.DoWithRetry(t, "EIP released", 20, 5*time.Second, func() (string, error) {
			out, err := ec2Client.DescribeAddresses(&ec2.DescribeAddressesInput{
				Filters: []*ec2.Filter{
					{Name: aws.String(fmt.Sprintf("tag:%s", natTagKey)),
						Values: []*string{aws.String(natTagValue)}},
				},
			})
			if err != nil {
				return "", err
			}
			if len(out.Addresses) > 0 {
				return "", fmt.Errorf("still %d NAT EIPs", len(out.Addresses))
			}
			return "OK", nil
		})
		record("Wait for EIP released", time.Since(eipStart))
		t.Log("NAT stopped and EIP released")
	}) {
		return
	}

	// ── Phase 3: NAT restart from stopped state ─────────────────────────
	// Launch a new workload and let EventBridge trigger the restart.

	if !runPhase("NATRestart", func(t *testing.T) {
		t.Log("Launching new workload to trigger NAT restart...")
		wlStart := time.Now()
		newWorkloadID := launchWorkload(t, ec2Client, privateSubnet, amiID, runID, profileName, queueURL)
		record("Launch workload instance (restart)", time.Since(wlStart))
		t.Logf("Launched workload %s", newWorkloadID)
		activeWorkloadID = newWorkloadID

		// EventBridge fires when the new workload goes pending/running,
		// triggering the Lambda to start the stopped NAT.
		t.Log("Waiting for restarted NAT to be running with EIP (via EventBridge)...")
		start := time.Now()
		natInstance := waitForRunningNATWithEIP(t, ec2Client, vpcID, "NAT restarted with EIP")
		natRestartTime := time.Since(start)
		record("Wait for NAT restarted with EIP", natRestartTime)
		t.Logf("NAT restarted with EIP in %s", natRestartTime.Round(time.Millisecond))

		require.NotNil(t, natInstance, "NAT should be running")

		// Verify the restarted NAT has an EIP.
		natEIP := natPublicIP(natInstance)
		require.NotEmpty(t, natEIP, "Restarted NAT should have a public IP")
		t.Logf("Restarted NAT has EIP %s", natEIP)

		// Verify connectivity — wait for new workload to report egress IP via SQS.
		t.Log("Waiting for workload connectivity via restarted NAT (SQS)...")
		egressStart := time.Now()
		msg := waitForEgress(t, sqsClient, queueURL, 4*time.Minute)
		record("Wait for workload egress IP (restart)", time.Since(egressStart))
		if msg.ConnectedMs > 0 && msg.BootMs > 0 {
			t.Logf("Workload-measured connectivity latency: %dms", msg.ConnectedMs-msg.BootMs)
		}
		require.NotEmpty(t, msg.EgressIP, "workload should have internet connectivity via restarted NAT")
		if msg.EgressIP == natEIP {
			t.Logf("Workload egresses via NAT EIP %s", natEIP)
		} else {
			t.Logf("Workload egressed via NAT auto-assigned IP %s (EIP %s attached after; expected during restart)", msg.EgressIP, natEIP)
		}
	}) {
		return
	}

	// ── Phase 4: NAT replacement on AMI update ─────────────────────────

	if !runPhase("NATAMIUpgrade", func(t *testing.T) {
		if updatedNatAMI == "" {
			t.Skip("NAT_ZERO_TEST_UPDATED_NAT_AMI_ID not set")
		}
		require.NotEmpty(t, activeWorkloadID, "AMI update phase requires an active workload")

		currentNat := waitForRunningNATWithEIP(t, ec2Client, vpcID, "current NAT running with EIP")
		oldNatID := aws.StringValue(currentNat.InstanceId)
		oldNatAMI := aws.StringValue(currentNat.ImageId)
		require.NotEmpty(t, oldNatID, "current NAT should have an instance id")
		require.NotEmpty(t, oldNatAMI, "current NAT should have an AMI id")
		if oldNatAMI == updatedNatAMI {
			t.Skipf("current NAT already uses target AMI %s", updatedNatAMI)
		}
		t.Logf("Updating NAT AMI from %s to %s", oldNatAMI, updatedNatAMI)

		applyStart := time.Now()
		opts.Vars["nat_ami_id"] = updatedNatAMI
		terraform.Apply(t, opts)
		record("Terraform apply (AMI update)", time.Since(applyStart))

		invokeTerminateStart := time.Now()
		invokeLambda(t, lambdaClient, lambdaName, map[string]string{
			"instance_id": activeWorkloadID,
			"state":       "running",
		})
		record("Lambda invoke (AMI update terminate)", time.Since(invokeTerminateStart))

		waitTermStart := time.Now()
		waitForInstanceTerminated(t, ec2Client, oldNatID)
		record("Wait for outdated NAT terminated", time.Since(waitTermStart))

		// The old NAT termination emits the next EventBridge signal. That
		// should drive creation of the replacement NAT without another manual
		// invoke, which would race the single-concurrency reconciler.
		replacementStart := time.Now()
		replacementNat := waitForRunningNATWithEIP(t, ec2Client, vpcID, "replacement NAT running with EIP")
		record("Wait for replacement NAT running with EIP", time.Since(replacementStart))

		require.NotEqual(t, oldNatID, aws.StringValue(replacementNat.InstanceId), "replacement NAT should be a new instance")
		require.Equal(t, updatedNatAMI, aws.StringValue(replacementNat.ImageId), "replacement NAT should use updated AMI")

		replacementEIP := natPublicIP(replacementNat)
		require.NotEmpty(t, replacementEIP, "replacement NAT should have a public IP")

		upgradeWorkloadStart := time.Now()
		upgradeWorkloadID := launchWorkload(t, ec2Client, privateSubnet, amiID, runID, profileName, queueURL)
		record("Launch workload instance (AMI update)", time.Since(upgradeWorkloadStart))
		activeWorkloadID = upgradeWorkloadID

		t.Log("Waiting for workload connectivity via replacement NAT (SQS)...")
		egressStart := time.Now()
		msg := waitForEgress(t, sqsClient, queueURL, 4*time.Minute)
		record("Wait for workload egress IP (AMI update)", time.Since(egressStart))
		require.Equal(t, replacementEIP, msg.EgressIP, "workload egress IP should match replacement NAT EIP")
	}) {
		return
	}

	// ── Phase 5: Cleanup action ─────────────────────────────────────────

	runPhase("CleanupAction", func(t *testing.T) {
		// Terminate all test workloads before cleanup to match production
		// destroy ordering where Terraform deletes the EventBridge target
		// (stopping new events) before invoking the cleanup Lambda.
		// Without this, EventBridge delivers NAT terminated events to the
		// reconciler which sees running workloads and creates new NATs.
		termWlStart := time.Now()
		wlOut, err := ec2Client.DescribeInstances(&ec2.DescribeInstancesInput{
			Filters: []*ec2.Filter{
				{Name: aws.String(fmt.Sprintf("tag:%s", testTagKey)), Values: []*string{aws.String(runID)}},
				{Name: aws.String("instance-state-name"), Values: []*string{
					aws.String("pending"), aws.String("running"),
					aws.String("stopping"), aws.String("stopped"),
				}},
			},
		})
		require.NoError(t, err)
		var wlIDs []*string
		for _, r := range wlOut.Reservations {
			for _, i := range r.Instances {
				wlIDs = append(wlIDs, i.InstanceId)
			}
		}
		if len(wlIDs) > 0 {
			t.Logf("Terminating %d workload(s) before cleanup", len(wlIDs))
			_, err := ec2Client.TerminateInstances(&ec2.TerminateInstancesInput{InstanceIds: wlIDs})
			require.NoError(t, err)
			// Wait until workloads leave pending/running so the reconciler
			// won't see them as active. Don't wait for full termination
			// (which takes 90+ seconds) — shutting-down is sufficient.
			retry.DoWithRetry(t, "workloads not active", 30, 2*time.Second, func() (string, error) {
				active := findWorkloadsInState(t, ec2Client, vpcID, runID, []string{"pending", "running"})
				if len(active) > 0 {
					return "", fmt.Errorf("still %d active workloads", len(active))
				}
				return "OK", nil
			})
		}
		record("Terminate workloads before cleanup", time.Since(termWlStart))

		t.Log("Invoking Lambda with cleanup action...")
		cleanupStart := time.Now()
		invokeLambda(t, lambdaClient, lambdaName, map[string]string{"action": "cleanup"})
		record("Lambda invoke (cleanup)", time.Since(cleanupStart))

		// Verify NAT instances are terminated.
		t.Log("Verifying NAT instances terminated...")
		natTermStart := time.Now()
		retry.DoWithRetry(t, "NAT terminated", 40, 5*time.Second, func() (string, error) {
			// Wait for fully terminated (not just absent from pending/running)
			// so ENIs are released before terraform destroy tries to delete them.
			nats := findNATInstancesInState(t, ec2Client, vpcID,
				[]string{"pending", "running", "shutting-down", "stopping", "stopped"})
			if len(nats) > 0 {
				return "", fmt.Errorf("still %d NAT instances (%s)",
					len(nats), aws.StringValue(nats[0].State.Name))
			}
			return "OK", nil
		})
		record("Wait for NAT terminated", time.Since(natTermStart))

		// Verify EIPs are released.
		t.Log("Verifying EIPs released...")
		eipStart := time.Now()
		retry.DoWithRetry(t, "EIPs released", 10, 5*time.Second, func() (string, error) {
			out, err := ec2Client.DescribeAddresses(&ec2.DescribeAddressesInput{
				Filters: []*ec2.Filter{
					{Name: aws.String(fmt.Sprintf("tag:%s", natTagKey)),
						Values: []*string{aws.String(natTagValue)}},
				},
			})
			if err != nil {
				return "", err
			}
			if len(out.Addresses) > 0 {
				return "", fmt.Errorf("still %d NAT EIPs", len(out.Addresses))
			}
			return "OK", nil
		})
		record("Wait for EIPs released", time.Since(eipStart))
		t.Log("Cleanup action verified: NAT instances terminated and EIPs released")
	})

	// terraform destroy runs via deferred cleanup — should succeed cleanly
	// since the cleanup action already removed Lambda-created resources.
}

// ── Lambda helpers ────────────────────────────────────────────────────────

// invokeLambda calls the nat-zero Lambda with the given payload. Requests log
// tailing to capture and display the Lambda REPORT line.
func invokeLambda(t *testing.T, client *lambda.Lambda, funcName string, payload map[string]string) {
	t.Helper()
	body, _ := json.Marshal(payload)
	var out *lambda.InvokeOutput
	_, err := retry.DoWithRetryE(t, "lambda invoke", 20, 3*time.Second, func() (string, error) {
		var invokeErr error
		out, invokeErr = client.Invoke(&lambda.InvokeInput{
			FunctionName: aws.String(funcName),
			Payload:      body,
			LogType:      aws.String("Tail"),
		})
		if invokeErr == nil {
			return "OK", nil
		}
		if isLambdaConcurrencyThrottle(invokeErr) {
			return "", invokeErr
		}
		return "", retry.FatalError{Underlying: invokeErr}
	})
	require.NoError(t, err, "Lambda invocation failed")
	if out.FunctionError != nil {
		t.Fatalf("Lambda returned error (%s): %s",
			aws.StringValue(out.FunctionError), string(out.Payload))
	}

	if out.LogResult != nil {
		logBytes, _ := base64.StdEncoding.DecodeString(aws.StringValue(out.LogResult))
		for _, line := range strings.Split(string(logBytes), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "REPORT") {
				t.Logf("[LAMBDA REPORT] %s", trimmed)
			}
		}
	}

	t.Logf("Lambda invoked: %v", payload)
}

func isLambdaConcurrencyThrottle(err error) bool {
	awsErr, ok := err.(awserr.Error)
	if !ok {
		return false
	}
	if awsErr.Code() != "TooManyRequestsException" {
		return false
	}
	return strings.Contains(awsErr.Message(), "ReservedFunctionConcurrentInvocationLimitExceeded")
}

// dumpLambdaLogs prints recent Lambda CloudWatch log events for post-mortem debugging.
func dumpLambdaLogs(t *testing.T, client *cloudwatchlogs.CloudWatchLogs, logGroup string) {
	t.Helper()
	t.Logf("=== Lambda logs from %s ===", logGroup)
	streams, err := client.DescribeLogStreams(&cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(logGroup),
		OrderBy:      aws.String("LastEventTime"),
		Descending:   aws.Bool(true),
		Limit:        aws.Int64(5),
	})
	if err != nil || len(streams.LogStreams) == 0 {
		t.Log("No log streams found")
		return
	}
	for _, stream := range streams.LogStreams {
		t.Logf("--- stream: %s ---", aws.StringValue(stream.LogStreamName))
		events, err := client.GetLogEvents(&cloudwatchlogs.GetLogEventsInput{
			LogGroupName:  aws.String(logGroup),
			LogStreamName: stream.LogStreamName,
			StartFromHead: aws.Bool(true),
			Limit:         aws.Int64(500),
		})
		if err != nil {
			t.Logf("Warning: could not read log events: %v", err)
			continue
		}
		for _, e := range events.Events {
			t.Logf("  [%s] %s",
				time.UnixMilli(aws.Int64Value(e.Timestamp)).UTC().Format("15:04:05"),
				strings.TrimSpace(aws.StringValue(e.Message)))
		}
	}
	t.Log("=== End Lambda logs ===")
}

// ── IAM (workload needs sqs:SendMessage for connectivity reporting) ──────

func createWorkloadProfile(t *testing.T, client *iam.IAM, runID string) string {
	t.Helper()
	name := fmt.Sprintf("nat-test-wl-%s", runID)
	tags := []*iam.Tag{{Key: aws.String(testTagKey), Value: aws.String(runID)}}

	_, err := client.CreateRole(&iam.CreateRoleInput{
		RoleName: aws.String(name),
		Tags:     tags,
		AssumeRolePolicyDocument: aws.String(`{
			"Version":"2012-10-17",
			"Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]
		}`),
	})
	require.NoError(t, err)

	_, err = client.PutRolePolicy(&iam.PutRolePolicyInput{
		RoleName:   aws.String(name),
		PolicyName: aws.String("sqs-send"),
		PolicyDocument: aws.String(`{
			"Version":"2012-10-17",
			"Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"*"}]
		}`),
	})
	require.NoError(t, err)

	_, err = client.CreateInstanceProfile(&iam.CreateInstanceProfileInput{
		InstanceProfileName: aws.String(name),
		Tags:                tags,
	})
	require.NoError(t, err)

	_, err = client.AddRoleToInstanceProfile(&iam.AddRoleToInstanceProfileInput{
		InstanceProfileName: aws.String(name),
		RoleName:            aws.String(name),
	})
	require.NoError(t, err)
	return name
}

func deleteWorkloadProfile(t *testing.T, client *iam.IAM, runID string) {
	t.Helper()
	name := fmt.Sprintf("nat-test-wl-%s", runID)
	client.RemoveRoleFromInstanceProfile(&iam.RemoveRoleFromInstanceProfileInput{
		InstanceProfileName: aws.String(name), RoleName: aws.String(name),
	})
	client.DeleteInstanceProfile(&iam.DeleteInstanceProfileInput{
		InstanceProfileName: aws.String(name),
	})
	client.DeleteRolePolicy(&iam.DeleteRolePolicyInput{
		RoleName: aws.String(name), PolicyName: aws.String("sqs-send"),
	})
	client.DeleteRole(&iam.DeleteRoleInput{RoleName: aws.String(name)})
	t.Logf("Deleted IAM profile %s", name)
}

// ── EC2 helpers ──────────────────────────────────────────────────────────

func getLatestAL2023AMI(t *testing.T, c *ec2.EC2) string {
	t.Helper()
	out, err := c.DescribeImages(&ec2.DescribeImagesInput{
		Owners: []*string{aws.String("amazon")},
		Filters: []*ec2.Filter{
			{Name: aws.String("name"), Values: []*string{aws.String("al2023-ami-2023*-arm64")}},
			{Name: aws.String("state"), Values: []*string{aws.String("available")}},
		},
	})
	require.NoError(t, err)
	var latest *ec2.Image
	for _, img := range out.Images {
		if strings.Contains(aws.StringValue(img.Name), "minimal") {
			continue
		}
		if latest == nil || aws.StringValue(img.CreationDate) > aws.StringValue(latest.CreationDate) {
			latest = img
		}
	}
	require.NotNil(t, latest, "no standard AL2023 ARM64 AMI found")
	return aws.StringValue(latest.ImageId)
}

func findNATInstances(t *testing.T, c *ec2.EC2, vpcID string) []*ec2.Instance {
	t.Helper()
	return findNATInstancesInState(t, c, vpcID, []string{"pending", "running"})
}

func findNATInstancesInState(t *testing.T, c *ec2.EC2, vpcID string, states []string) []*ec2.Instance {
	t.Helper()
	stateValues := make([]*string, len(states))
	for i, s := range states {
		stateValues[i] = aws.String(s)
	}
	out, err := c.DescribeInstances(&ec2.DescribeInstancesInput{
		Filters: []*ec2.Filter{
			{Name: aws.String(fmt.Sprintf("tag:%s", natTagKey)), Values: []*string{aws.String(natTagValue)}},
			{Name: aws.String("vpc-id"), Values: []*string{aws.String(vpcID)}},
			{Name: aws.String("instance-state-name"), Values: stateValues},
		},
	})
	require.NoError(t, err)
	var res []*ec2.Instance
	for _, r := range out.Reservations {
		res = append(res, r.Instances...)
	}
	return res
}

func findWorkloadsInState(t *testing.T, c *ec2.EC2, vpcID, runID string, states []string) []*ec2.Instance {
	t.Helper()
	stateValues := make([]*string, len(states))
	for i, s := range states {
		stateValues[i] = aws.String(s)
	}
	out, err := c.DescribeInstances(&ec2.DescribeInstancesInput{
		Filters: []*ec2.Filter{
			{Name: aws.String("vpc-id"), Values: []*string{aws.String(vpcID)}},
			{Name: aws.String("instance-state-name"), Values: stateValues},
			{Name: aws.String(fmt.Sprintf("tag:%s", testTagKey)), Values: []*string{aws.String(runID)}},
		},
	})
	require.NoError(t, err)
	var res []*ec2.Instance
	for _, r := range out.Reservations {
		res = append(res, r.Instances...)
	}
	return res
}

func waitForRunningNATWithEIP(t *testing.T, c *ec2.EC2, vpcID, description string) *ec2.Instance {
	t.Helper()

	var natInstance *ec2.Instance
	retry.DoWithRetry(t, description, 100, 2*time.Second, func() (string, error) {
		nats := findNATInstances(t, c, vpcID)
		for _, n := range nats {
			if aws.StringValue(n.State.Name) == "running" && natPublicIP(n) != "" {
				natInstance = n
				return "OK", nil
			}
			if aws.StringValue(n.State.Name) == "running" {
				return "", fmt.Errorf("NAT running but no EIP yet")
			}
		}
		return "", fmt.Errorf("no running NAT (%d found)", len(nats))
	})
	return natInstance
}

func waitForInstanceTerminated(t *testing.T, c *ec2.EC2, instanceID string) {
	t.Helper()

	retry.DoWithRetry(t, "instance terminated", 60, 2*time.Second, func() (string, error) {
		out, err := c.DescribeInstances(&ec2.DescribeInstancesInput{
			InstanceIds: []*string{aws.String(instanceID)},
		})
		if err != nil {
			return "", err
		}
		if len(out.Reservations) == 0 || len(out.Reservations[0].Instances) == 0 {
			return "OK", nil
		}
		state := aws.StringValue(out.Reservations[0].Instances[0].State.Name)
		if state == ec2.InstanceStateNameTerminated {
			return "OK", nil
		}
		return "", fmt.Errorf("instance %s still %s", instanceID, state)
	})
}

func natPublicIP(nat *ec2.Instance) string {
	for _, eni := range nat.NetworkInterfaces {
		if aws.Int64Value(eni.Attachment.DeviceIndex) == 0 && eni.Association != nil {
			return aws.StringValue(eni.Association.PublicIp)
		}
	}
	return ""
}

func launchWorkload(t *testing.T, c *ec2.EC2, subnet, ami, runID, profile, queueURL string) string {
	t.Helper()
	out, err := c.RunInstances(&ec2.RunInstancesInput{
		ImageId:      aws.String(ami),
		InstanceType: aws.String("t4g.nano"),
		SubnetId:     aws.String(subnet),
		MinCount:     aws.Int64(1),
		MaxCount:     aws.Int64(1),
		UserData:     aws.String(userDataScript(queueURL)),
		IamInstanceProfile: &ec2.IamInstanceProfileSpecification{
			Name: aws.String(profile),
		},
		TagSpecifications: []*ec2.TagSpecification{{
			ResourceType: aws.String("instance"),
			Tags: []*ec2.Tag{
				{Key: aws.String("Name"), Value: aws.String("nat-zero-test-workload")},
				{Key: aws.String(testTagKey), Value: aws.String(runID)},
			},
		}},
	})
	require.NoError(t, err)
	return aws.StringValue(out.Instances[0].InstanceId)
}

// egressMessage is the JSON payload the workload sends to SQS on connectivity.
type egressMessage struct {
	InstanceID  string `json:"instance_id"`
	EgressIP    string `json:"egress_ip"`
	BootMs      int64  `json:"boot_ms"`
	ConnectedMs int64  `json:"connected_ms"`
}

// waitForEgress uses SQS long polling to wait for a workload to report its
// egress IP. Returns near-instantly when the message arrives instead of
// polling EC2 tags every 5 seconds.
func waitForEgress(t *testing.T, client *sqs.SQS, queueURL string, timeout time.Duration) egressMessage {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := client.ReceiveMessage(&sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queueURL),
			MaxNumberOfMessages: aws.Int64(1),
			WaitTimeSeconds:     aws.Int64(20),
		})
		require.NoError(t, err)
		if len(out.Messages) > 0 {
			// Delete the message so it doesn't interfere with the next phase.
			client.DeleteMessage(&sqs.DeleteMessageInput{
				QueueUrl:      aws.String(queueURL),
				ReceiptHandle: out.Messages[0].ReceiptHandle,
			})
			var msg egressMessage
			require.NoError(t, json.Unmarshal([]byte(aws.StringValue(out.Messages[0].Body)), &msg))
			return msg
		}
	}
	t.Fatalf("timed out waiting for egress message on SQS queue %s", queueURL)
	return egressMessage{} // unreachable
}

func assertRouteTableEntry(t *testing.T, c *ec2.EC2, vpcID string, nat *ec2.Instance) {
	t.Helper()
	var privateENI string
	for _, eni := range nat.NetworkInterfaces {
		if aws.Int64Value(eni.Attachment.DeviceIndex) == 1 {
			privateENI = aws.StringValue(eni.NetworkInterfaceId)
		}
	}
	require.NotEmpty(t, privateENI)

	out, err := c.DescribeRouteTables(&ec2.DescribeRouteTablesInput{
		Filters: []*ec2.Filter{
			{Name: aws.String("vpc-id"), Values: []*string{aws.String(vpcID)}},
			{Name: aws.String("route.destination-cidr-block"), Values: []*string{aws.String("0.0.0.0/0")}},
		},
	})
	require.NoError(t, err)

	for _, rt := range out.RouteTables {
		for _, r := range rt.Routes {
			if aws.StringValue(r.DestinationCidrBlock) == "0.0.0.0/0" &&
				strings.EqualFold(aws.StringValue(r.NetworkInterfaceId), privateENI) {
				return
			}
		}
	}
	t.Errorf("no 0.0.0.0/0 route pointing to NAT private ENI %s", privateENI)
}

// ── Orphan detection ─────────────────────────────────────────────────────

// TestNoOrphanedResources searches for resources left behind by previous test
// runs. It runs last (Go runs tests in source order within a package) and
// reports any orphans so they can be cleaned up.
func TestNoOrphanedResources(t *testing.T) {
	sess := session.Must(session.NewSession(&aws.Config{Region: aws.String(awsRegion)}))
	ec2Client := ec2.New(sess)
	iamClient := iam.New(sess)
	lambdaClient := lambda.New(sess)
	cwClient := cloudwatchlogs.New(sess)
	sqsClient := sqs.New(sess)

	const testPrefix = "nat-test"
	checks := []struct {
		name    string
		checkFn func() []string
	}{
		{"Subnets", func() []string {
			out, err := ec2Client.DescribeSubnets(&ec2.DescribeSubnetsInput{
				Filters: []*ec2.Filter{
					{Name: aws.String("cidr-block"), Values: []*string{aws.String("172.31.128.0/24")}},
				},
			})
			if err != nil {
				return nil
			}
			var found []string
			for _, s := range out.Subnets {
				found = append(found, fmt.Sprintf("Subnet %s (%s)",
					aws.StringValue(s.SubnetId), aws.StringValue(s.CidrBlock)))
			}
			return found
		}},
		{"ENIs", func() []string {
			out, err := ec2Client.DescribeNetworkInterfaces(&ec2.DescribeNetworkInterfacesInput{
				Filters: []*ec2.Filter{
					{Name: aws.String("tag:Name"), Values: []*string{aws.String(testPrefix + "-*")}},
				},
			})
			if err != nil {
				return nil
			}
			var found []string
			for _, e := range out.NetworkInterfaces {
				name := ""
				for _, tag := range e.TagSet {
					if aws.StringValue(tag.Key) == "Name" {
						name = aws.StringValue(tag.Value)
					}
				}
				found = append(found, fmt.Sprintf("ENI %s (%s, %s)",
					aws.StringValue(e.NetworkInterfaceId), name, aws.StringValue(e.Status)))
			}
			return found
		}},
		{"SecurityGroups", func() []string {
			out, err := ec2Client.DescribeSecurityGroups(&ec2.DescribeSecurityGroupsInput{
				Filters: []*ec2.Filter{
					{Name: aws.String("group-name"), Values: []*string{aws.String(testPrefix + "-*")}},
				},
			})
			if err != nil {
				return nil
			}
			var found []string
			for _, sg := range out.SecurityGroups {
				found = append(found, fmt.Sprintf("SecurityGroup %s (%s)",
					aws.StringValue(sg.GroupId), aws.StringValue(sg.GroupName)))
			}
			return found
		}},
		{"LaunchTemplates", func() []string {
			out, err := ec2Client.DescribeLaunchTemplates(&ec2.DescribeLaunchTemplatesInput{
				Filters: []*ec2.Filter{
					{Name: aws.String("launch-template-name"), Values: []*string{aws.String(testPrefix + "-*")}},
				},
			})
			if err != nil {
				return nil
			}
			var found []string
			for _, lt := range out.LaunchTemplates {
				found = append(found, fmt.Sprintf("LaunchTemplate %s (%s)",
					aws.StringValue(lt.LaunchTemplateId), aws.StringValue(lt.LaunchTemplateName)))
			}
			return found
		}},
		{"EventBridgeRules", func() []string {
			out, err := cloudwatchevents.New(sess).ListRules(&cloudwatchevents.ListRulesInput{
				NamePrefix: aws.String(testPrefix),
			})
			if err != nil {
				return nil
			}
			var found []string
			for _, r := range out.Rules {
				found = append(found, fmt.Sprintf("EventBridgeRule %s", aws.StringValue(r.Name)))
			}
			return found
		}},
		{"Lambda", func() []string {
			var found []string
			var marker *string
			for {
				out, err := lambdaClient.ListFunctions(&lambda.ListFunctionsInput{Marker: marker})
				if err != nil {
					return nil
				}
				for _, fn := range out.Functions {
					name := aws.StringValue(fn.FunctionName)
					if strings.HasPrefix(name, testPrefix) {
						found = append(found, fmt.Sprintf("Lambda %s", name))
					}
				}
				if out.NextMarker == nil || aws.StringValue(out.NextMarker) == "" {
					break
				}
				marker = out.NextMarker
			}
			return found
		}},
		{"LogGroups", func() []string {
			out, err := cwClient.DescribeLogGroups(&cloudwatchlogs.DescribeLogGroupsInput{
				LogGroupNamePrefix: aws.String("/aws/lambda/" + testPrefix),
			})
			if err != nil {
				return nil
			}
			var found []string
			for _, lg := range out.LogGroups {
				found = append(found, fmt.Sprintf("LogGroup %s", aws.StringValue(lg.LogGroupName)))
			}
			return found
		}},
		{"IAMRoles", func() []string {
			out, err := iamClient.ListRoles(&iam.ListRolesInput{})
			if err != nil {
				return nil
			}
			var found []string
			for _, r := range out.Roles {
				if strings.HasPrefix(aws.StringValue(r.RoleName), testPrefix) {
					found = append(found, fmt.Sprintf("IAMRole %s", aws.StringValue(r.RoleName)))
				}
			}
			return found
		}},
		{"IAMProfiles", func() []string {
			out, err := iamClient.ListInstanceProfiles(&iam.ListInstanceProfilesInput{})
			if err != nil {
				return nil
			}
			var found []string
			for _, p := range out.InstanceProfiles {
				if strings.HasPrefix(aws.StringValue(p.InstanceProfileName), testPrefix) {
					found = append(found, fmt.Sprintf("IAMInstanceProfile %s",
						aws.StringValue(p.InstanceProfileName)))
				}
			}
			return found
		}},
		{"EIPs", func() []string {
			out, err := ec2Client.DescribeAddresses(&ec2.DescribeAddressesInput{
				Filters: []*ec2.Filter{
					{Name: aws.String(fmt.Sprintf("tag:%s", natTagKey)),
						Values: []*string{aws.String(natTagValue)}},
					{Name: aws.String(fmt.Sprintf("tag:%s", testTagKey)),
						Values: []*string{aws.String("*")}},
				},
			})
			if err != nil {
				return nil
			}
			var found []string
			for _, a := range out.Addresses {
				found = append(found, fmt.Sprintf("EIP %s (%s)",
					aws.StringValue(a.AllocationId), aws.StringValue(a.PublicIp)))
			}
			return found
		}},
		{"SQSQueues", func() []string {
			out, err := sqsClient.ListQueues(&sqs.ListQueuesInput{
				QueueNamePrefix: aws.String(testPrefix),
			})
			if err != nil {
				return nil
			}
			var found []string
			for _, u := range out.QueueUrls {
				found = append(found, fmt.Sprintf("SQSQueue %s", aws.StringValue(u)))
			}
			return found
		}},
	}

	var orphans []string
	for _, c := range checks {
		orphans = append(orphans, c.checkFn()...)
	}

	if len(orphans) > 0 {
		t.Log("Orphaned resources detected from previous test runs:")
		for _, o := range orphans {
			t.Logf("  - %s", o)
		}
		t.Errorf("found %d orphaned test resources — clean up manually or investigate failed runs", len(orphans))
	} else {
		t.Log("No orphaned test resources found")
	}
}
