package main

import (
	"context"
	"os"
	"strings"

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

// parseAZSet turns a comma-separated AZ list into a set; empty means none.
func parseAZSet(v string) map[string]bool {
	set := map[string]bool{}
	for _, az := range strings.Split(v, ",") {
		if az = strings.TrimSpace(az); az != "" {
			set[az] = true
		}
	}
	return set
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
		ConfigVersion:  os.Getenv("CONFIG_VERSION"),
		AlwaysOnAZs:    parseAZSet(os.Getenv("ALWAYS_ON_AZS")),
	}

	lambda.Start(h.HandleRequest)
}
