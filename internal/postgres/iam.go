package postgres

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
)

// GetRDSAuthToken generates an IAM authentication token for RDS
// This token is used as the password when connecting to RDS with IAM auth
func GetRDSAuthToken(ctx context.Context, host string, port int, user, region string) (string, error) {
	// Load AWS configuration from environment/instance role
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return "", fmt.Errorf("loading AWS config: %w", err)
	}

	// Build the endpoint
	endpoint := fmt.Sprintf("%s:%d", host, port)

	// Generate the auth token
	token, err := auth.BuildAuthToken(ctx, endpoint, region, user, cfg.Credentials)
	if err != nil {
		return "", fmt.Errorf("building auth token: %w", err)
	}

	return token, nil
}
