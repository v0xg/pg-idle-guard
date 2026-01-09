package cli

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/v0xg/pg-idle-guard/internal/alerts"
	"github.com/v0xg/pg-idle-guard/internal/config"
	"github.com/v0xg/pg-idle-guard/internal/postgres"
)

var configureCmd = &cobra.Command{
	Use:   "configure",
	Short: "Interactive configuration wizard",
	Long:  `Set up pguard with an interactive wizard that guides you through database connection, credential storage, and alerting configuration.`,
	RunE:  runConfigure,
}

var configureTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Test current configuration",
	RunE:  runConfigureTest,
}

var configureShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show current configuration",
	RunE:  runConfigureShow,
}

func init() {
	configureCmd.AddCommand(configureTestCmd)
	configureCmd.AddCommand(configureShowCmd)
}

// readLine reads a line from the reader and returns the trimmed result.
// Returns an error if reading fails (e.g., stdin closed).
func readLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("reading input: %w", err)
	}
	return strings.TrimSpace(line), nil
}

func runConfigure(cmd *cobra.Command, args []string) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println()
	fmt.Println("pguard Configuration")
	fmt.Println(strings.Repeat("=", 44))
	fmt.Println()

	newCfg := config.DefaultConfig()

	// Database connection
	fmt.Println("Database Connection")
	fmt.Println(strings.Repeat("-", 44))
	fmt.Println()

	// Host
	fmt.Printf("Database host [localhost]: ")
	host, err := readLine(reader)
	if err != nil {
		return err
	}
	if host == "" {
		host = "localhost"
	}
	newCfg.Connection.Host = host

	// Port
	fmt.Printf("Database port [5432]: ")
	portStr, err := readLine(reader)
	if err != nil {
		return err
	}
	if portStr == "" {
		newCfg.Connection.Port = 5432
	} else {
		port, portErr := strconv.Atoi(portStr)
		if portErr != nil {
			return fmt.Errorf("invalid port: %s", portStr)
		}
		newCfg.Connection.Port = port
	}

	// Database name
	fmt.Printf("Database name: ")
	dbname, err := readLine(reader)
	if err != nil {
		return err
	}
	if dbname == "" {
		return fmt.Errorf("database name is required")
	}
	newCfg.Connection.Database = dbname

	// Username
	fmt.Printf("Database user: ")
	user, err := readLine(reader)
	if err != nil {
		return err
	}
	if user == "" {
		return fmt.Errorf("database user is required")
	}
	newCfg.Connection.User = user

	// Authentication method
	fmt.Println()
	fmt.Println("Authentication method:")
	fmt.Println("  1. Password (direct)")
	fmt.Println("  2. Password from environment variable")
	fmt.Println("  3. AWS IAM Authentication (for RDS)")
	fmt.Println()
	fmt.Printf("Select [1]: ")

	authChoice, err := readLine(reader)
	if err != nil {
		return err
	}
	if authChoice == "" {
		authChoice = "1"
	}

	switch authChoice {
	case "1":
		newCfg.Connection.AuthMethod = "password"
		fmt.Printf("Database password: ")
		password, pwErr := readLine(reader)
		if pwErr != nil {
			return pwErr
		}
		newCfg.Connection.Password = password
	case "2":
		newCfg.Connection.AuthMethod = "env"
		fmt.Printf("Environment variable name [PGPASSWORD]: ")
		envVar, envErr := readLine(reader)
		if envErr != nil {
			return envErr
		}
		if envVar == "" {
			envVar = "PGPASSWORD"
		}
		newCfg.Connection.PasswordEnv = envVar
		newCfg.Connection.Password = fmt.Sprintf("${%s}", envVar)
	case "3":
		newCfg.Connection.AuthMethod = "iam"
		fmt.Printf("AWS Region [us-east-1]: ")
		region, regionErr := readLine(reader)
		if regionErr != nil {
			return regionErr
		}
		if region == "" {
			region = "us-east-1"
		}
		newCfg.Connection.AWSRegion = region
		fmt.Println()
		fmt.Println("Note: Make sure your database user has the rds_iam role:")
		fmt.Printf("  GRANT rds_iam TO %s;\n", user)
	default:
		return fmt.Errorf("invalid choice: %s", authChoice)
	}

	// SSL mode
	fmt.Println()
	fmt.Printf("SSL mode [prefer]: ")
	sslmode, err := readLine(reader)
	if err != nil {
		return err
	}
	if sslmode == "" {
		sslmode = "prefer"
	}
	newCfg.Connection.SSLMode = sslmode

	// Test connection
	fmt.Println()
	fmt.Printf("Test connection now? [Y/n]: ")
	testChoice, err := readLine(reader)
	if err != nil {
		return err
	}
	testChoice = strings.ToLower(testChoice)

	if testChoice != "n" && testChoice != "no" {
		fmt.Print("Testing connection... ")

		// Use TestConnectionWithConfig for IAM auth support
		if testErr := postgres.TestConnectionWithConfig(newCfg); testErr != nil {
			fmt.Println("[FAILED]")
			fmt.Printf("Error: %v\n", testErr)
			fmt.Println()
			fmt.Printf("Save configuration anyway? [y/N]: ")
			saveChoice, saveErr := readLine(reader)
			if saveErr != nil {
				return saveErr
			}
			saveChoice = strings.ToLower(saveChoice)
			if saveChoice != "y" && saveChoice != "yes" {
				fmt.Println("Configuration not saved.")
				return nil
			}
		} else {
			fmt.Println("[OK]")
		}
	}

	// Thresholds
	fmt.Println()
	fmt.Println("Thresholds")
	fmt.Println(strings.Repeat("-", 44))

	fmt.Printf("Idle transaction warning [30s]: ")
	warnStr, err := readLine(reader)
	if err != nil {
		return err
	}
	if warnStr != "" {
		warn, parseErr := time.ParseDuration(warnStr)
		if parseErr != nil {
			return fmt.Errorf("invalid duration: %s", warnStr)
		}
		newCfg.Thresholds.IdleTransaction.Warning = warn
	}

	fmt.Printf("Idle transaction critical [2m]: ")
	critStr, err := readLine(reader)
	if err != nil {
		return err
	}
	if critStr != "" {
		crit, parseErr := time.ParseDuration(critStr)
		if parseErr != nil {
			return fmt.Errorf("invalid duration: %s", critStr)
		}
		newCfg.Thresholds.IdleTransaction.Critical = crit
	}

	// Slack alerts (optional)
	fmt.Println()
	fmt.Printf("Configure Slack alerts? [y/N]: ")
	slackChoice, err := readLine(reader)
	if err != nil {
		return err
	}
	slackChoice = strings.ToLower(slackChoice)

	if slackChoice == "y" || slackChoice == "yes" {
		newCfg.Alerts.Slack.Enabled = true

		fmt.Printf("Slack webhook URL: ")
		webhookURL, urlErr := readLine(reader)
		if urlErr != nil {
			return urlErr
		}
		newCfg.Alerts.Slack.WebhookURL = webhookURL

		fmt.Printf("Slack channel [#alerts]: ")
		channel, chanErr := readLine(reader)
		if chanErr != nil {
			return chanErr
		}
		if channel == "" {
			channel = "#alerts"
		}
		newCfg.Alerts.Slack.Channel = channel
	}

	// Save configuration
	configPath, err := config.Path()
	if err != nil {
		return fmt.Errorf("getting config path: %w", err)
	}

	fmt.Println()
	fmt.Printf("Save configuration to %s? [Y/n]: ", configPath)
	saveChoice, err := readLine(reader)
	if err != nil {
		return err
	}
	saveChoice = strings.ToLower(saveChoice)

	if saveChoice == "n" || saveChoice == "no" {
		fmt.Println("Configuration not saved.")
		return nil
	}

	if err := newCfg.Save(configPath); err != nil {
		return fmt.Errorf("saving configuration: %w", err)
	}

	fmt.Println()
	fmt.Printf("[+] Configuration saved to %s\n", configPath)
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  pguard status    # Check current connections")
	fmt.Println("  pguard watch     # Monitor in real-time")
	fmt.Println("  pguard daemon    # Run as background service")
	fmt.Println()

	return nil
}

func runConfigureTest(cmd *cobra.Command, args []string) error {
	fmt.Println("Testing configuration...")
	fmt.Println()

	// Validate config
	if err := cfg.Validate(); err != nil {
		fmt.Printf("[FAILED] Configuration: %v\n", err)
		return nil
	}
	fmt.Println("[OK] Configuration valid")

	// Test database connection
	fmt.Print("Testing PostgreSQL connection... ")
	if err := postgres.TestConnectionWithConfig(cfg); err != nil {
		fmt.Println("[FAILED]")
		fmt.Printf("    Error: %v\n", err)
	} else {
		fmt.Println("[OK]")
	}

	// Test Slack webhook if configured
	if cfg.Alerts.Slack.Enabled {
		fmt.Print("Testing Slack webhook... ")
		webhookURL := cfg.Alerts.Slack.WebhookURL
		if webhookURL == "" {
			webhookURL = os.Getenv("SLACK_WEBHOOK_URL")
		}
		if webhookURL != "" {
			slackClient := alerts.NewSlackClient(webhookURL, cfg.Alerts.Slack.Channel, nil)
			if err := slackClient.TestConnection(); err != nil {
				fmt.Println("[FAILED]")
				fmt.Printf("    Error: %v\n", err)
			} else {
				fmt.Println("[OK]")
			}
		} else {
			fmt.Println("[SKIP] No webhook URL configured")
		}
	}

	fmt.Println()
	return nil
}

func runConfigureShow(cmd *cobra.Command, args []string) error {
	configPath, _ := config.Path()

	fmt.Printf("Config file: %s\n", configPath)
	fmt.Println()

	fmt.Println("Connection")
	fmt.Println(strings.Repeat("-", 44))
	fmt.Printf("  Host:      %s\n", cfg.Connection.Host)
	fmt.Printf("  Port:      %d\n", cfg.Connection.Port)
	fmt.Printf("  Database:  %s\n", cfg.Connection.Database)
	fmt.Printf("  User:      %s\n", cfg.Connection.User)
	fmt.Printf("  Auth:      %s\n", cfg.Connection.AuthMethod)
	fmt.Printf("  SSL:       %s\n", cfg.Connection.SSLMode)

	fmt.Println()
	fmt.Println("Thresholds")
	fmt.Println(strings.Repeat("-", 44))
	fmt.Printf("  Idle warning:   %s\n", cfg.Thresholds.IdleTransaction.Warning)
	fmt.Printf("  Idle critical:  %s\n", cfg.Thresholds.IdleTransaction.Critical)
	fmt.Printf("  Pool warning:   %d%%\n", cfg.Thresholds.ConnectionPool.WarningPercent)
	fmt.Printf("  Pool critical:  %d%%\n", cfg.Thresholds.ConnectionPool.CriticalPercent)

	fmt.Println()
	fmt.Println("Alerts")
	fmt.Println(strings.Repeat("-", 44))
	if cfg.Alerts.Slack.Enabled {
		fmt.Printf("  Slack:     enabled (%s)\n", cfg.Alerts.Slack.Channel)
	} else {
		fmt.Println("  Slack:     disabled")
	}

	fmt.Println()
	fmt.Println("Auto-Terminate")
	fmt.Println(strings.Repeat("-", 44))
	if cfg.AutoTerm.Enabled {
		fmt.Printf("  Enabled:   yes (after %s)\n", cfg.AutoTerm.After)
		fmt.Printf("  Dry run:   %v\n", cfg.AutoTerm.DryRun)
	} else {
		fmt.Println("  Enabled:   no")
	}

	fmt.Println()
	return nil
}
