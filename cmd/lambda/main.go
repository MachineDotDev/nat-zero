package main

import (
	"context"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		panic("unable to load AWS config: " + err.Error())
	}

	h := &Handler{
		EC2:            ec2.NewFromConfig(cfg),
		NATTagKey:      envOr("NAT_TAG_KEY", "nat-zero:managed"),
		NATTagValue:    envOr("NAT_TAG_VALUE", "true"),
		IgnoreTagKey:   envOr("IGNORE_TAG_KEY", "nat-zero:ignore"),
		IgnoreTagValue: envOr("IGNORE_TAG_VALUE", "true"),
		TargetVPC:      os.Getenv("TARGET_VPC_ID"),
		AMIOwner:       envOr("AMI_OWNER_ACCOUNT", "568608671756"),
		AMIPattern:     envOr("AMI_NAME_PATTERN", "fck-nat-al2023-*-arm64-*"),
		ConfigVersion:  os.Getenv("CONFIG_VERSION"),
	}

	lambda.Start(h.HandleRequest)
}
