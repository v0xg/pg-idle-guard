package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// Client provides access to secret storage backends
type Client struct {
	region    string
	smClient  *secretsmanager.Client
	ssmClient *ssm.Client
}

// NewClient creates a new secrets client for the given AWS region
func NewClient(ctx context.Context, region string) (*Client, error) {
	if region == "" {
		region = os.Getenv("AWS_REGION")
		if region == "" {
			region = os.Getenv("AWS_DEFAULT_REGION")
		}
	}

	opts := []func(*config.LoadOptions) error{}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}

	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}

	return &Client{
		region:    region,
		smClient:  secretsmanager.NewFromConfig(cfg),
		ssmClient: ssm.NewFromConfig(cfg),
	}, nil
}

// GetSecretString retrieves a secret string from AWS Secrets Manager
// The secretID can be either a secret name or full ARN
func (c *Client) GetSecretString(ctx context.Context, secretID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	output, err := c.smClient.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: &secretID,
	})
	if err != nil {
		return "", fmt.Errorf("retrieving secret %s: %w", secretID, err)
	}

	if output.SecretString != nil {
		return *output.SecretString, nil
	}

	return "", fmt.Errorf("secret %s has no string value", secretID)
}

// GetSecretJSON retrieves a secret and parses it as JSON, returning the value for the given key
func (c *Client) GetSecretJSON(ctx context.Context, secretID, key string) (string, error) {
	secretStr, err := c.GetSecretString(ctx, secretID)
	if err != nil {
		return "", err
	}

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(secretStr), &data); err != nil {
		return "", fmt.Errorf("parsing secret JSON: %w", err)
	}

	value, ok := data[key]
	if !ok {
		return "", fmt.Errorf("key %s not found in secret", key)
	}

	strValue, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("key %s is not a string", key)
	}

	return strValue, nil
}

// GetParameter retrieves a parameter from AWS Systems Manager Parameter Store
// The paramName should be the full parameter path (e.g., /myapp/db/password)
func (c *Client) GetParameter(ctx context.Context, paramName string, decrypt bool) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	output, err := c.ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           &paramName,
		WithDecryption: &decrypt,
	})
	if err != nil {
		return "", fmt.Errorf("retrieving parameter %s: %w", paramName, err)
	}

	if output.Parameter == nil || output.Parameter.Value == nil {
		return "", fmt.Errorf("parameter %s has no value", paramName)
	}

	return *output.Parameter.Value, nil
}

// ResolvePassword resolves a database password based on the auth method
// Supports: password (direct), env (environment variable), secrets_manager, parameter_store
func ResolvePassword(ctx context.Context, authMethod, password, passwordSecret, passwordEnv, region string) (string, error) {
	switch authMethod {
	case "password", "":
		// Direct password (may be empty for IAM auth)
		return password, nil

	case "env":
		// Password from environment variable
		envVar := passwordEnv
		if envVar == "" {
			envVar = "PGPASSWORD"
		}
		value := os.Getenv(envVar)
		if value == "" {
			return "", fmt.Errorf("environment variable %s not set", envVar)
		}
		return value, nil

	case "secrets_manager":
		if passwordSecret == "" {
			return "", fmt.Errorf("password_secret required for secrets_manager auth method")
		}
		client, err := NewClient(ctx, region)
		if err != nil {
			return "", err
		}
		// Try to get as plain string first, then as JSON with "password" key
		secret, err := client.GetSecretString(ctx, passwordSecret)
		if err != nil {
			return "", err
		}
		// Check if it's JSON with a password field
		var data map[string]interface{}
		if json.Unmarshal([]byte(secret), &data) == nil {
			if pw, ok := data["password"].(string); ok {
				return pw, nil
			}
		}
		// Return as plain string
		return secret, nil

	case "parameter_store":
		if passwordSecret == "" {
			return "", fmt.Errorf("password_secret required for parameter_store auth method")
		}
		client, err := NewClient(ctx, region)
		if err != nil {
			return "", err
		}
		return client.GetParameter(ctx, passwordSecret, true)

	case "iam":
		// IAM auth doesn't use a password - handled separately
		return "", nil

	default:
		return "", fmt.Errorf("unknown auth method: %s", authMethod)
	}
}

// ResolveWebhookSecret retrieves a webhook URL from Secrets Manager
func ResolveWebhookSecret(ctx context.Context, secretARN, region string) (string, error) {
	if secretARN == "" {
		return "", nil
	}

	client, err := NewClient(ctx, region)
	if err != nil {
		return "", err
	}

	return client.GetSecretString(ctx, secretARN)
}
