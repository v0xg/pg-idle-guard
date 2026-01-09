package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/v0xg/pg-idle-guard/internal/postgres"
	"github.com/v0xg/pg-idle-guard/internal/util"
)

var killCmd = &cobra.Command{
	Use:   "kill <pid>",
	Short: "Terminate a database connection by PID",
	Long: `Terminate a PostgreSQL backend connection by PID.

This will rollback any uncommitted transaction on that connection.`,
	Args: cobra.ExactArgs(1),
	RunE: runKill,
}

func init() {
	killCmd.Flags().BoolP("force", "f", false, "Skip confirmation prompt")
	killCmd.Flags().Bool("cancel", false, "Cancel current query instead of terminating")
}

func runKill(cmd *cobra.Command, args []string) error {
	force, _ := cmd.Flags().GetBool("force")
	cancelOnly, _ := cmd.Flags().GetBool("cancel")

	// Parse PID
	pid, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("invalid PID: %s", args[0])
	}

	// Create PostgreSQL client
	client, err := postgres.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get connection details
	conns, err := client.GetConnections(ctx)
	if err != nil {
		return fmt.Errorf("getting connections: %w", err)
	}

	var targetConn *postgres.Connection
	for _, conn := range conns {
		if conn.PID == pid {
			targetConn = conn
			break
		}
	}

	if targetConn == nil {
		return fmt.Errorf("no connection found with PID %d", pid)
	}

	// Show connection details
	fmt.Println()
	fmt.Println("Connection Details")
	fmt.Println(strings.Repeat("-", 44))
	fmt.Printf("PID:             %d\n", targetConn.PID)
	fmt.Printf("Application:     %s\n", targetConn.ApplicationName)
	fmt.Printf("Client:          %s\n", targetConn.ClientAddr)
	fmt.Printf("User:            %s\n", targetConn.Username)
	fmt.Printf("State:           %s\n", targetConn.State)
	fmt.Printf("State duration:  %s\n", util.FormatDuration(targetConn.IdleDuration()))

	if targetConn.XactStart != nil {
		fmt.Printf("Transaction:     %s\n", util.FormatDuration(targetConn.TransactionDuration()))
	}

	fmt.Println()
	fmt.Println("Query:")
	fmt.Printf("  %s\n", util.TruncateQuery(targetConn.Query, 70))
	fmt.Println()

	action := "terminate"
	if cancelOnly {
		action = "cancel the current query on"
	}

	// Confirmation
	if !force {
		if targetConn.IsIdleInTransaction() {
			fmt.Printf("Warning: This will %s the backend and rollback any uncommitted work.\n", action)
		} else if string(targetConn.State) == "active" {
			fmt.Printf("Warning: This connection is actively running a query.\n")
		}
		fmt.Println()

		fmt.Printf("Proceed? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		response, readErr := readLine(reader)
		if readErr != nil {
			return readErr
		}
		response = strings.ToLower(response)

		if response != "y" && response != "yes" {
			fmt.Println("Canceled.")
			return nil
		}
	}

	// Execute termination
	var success bool
	if cancelOnly {
		success, err = client.CancelBackend(ctx, pid)
	} else {
		success, err = client.TerminateBackend(ctx, pid)
	}

	if err != nil {
		return fmt.Errorf("failed to %s backend: %w", action, err)
	}

	if success {
		if cancelOnly {
			fmt.Printf("[+] Query canceled on PID %d\n", pid)
		} else {
			fmt.Printf("[+] Backend %d terminated\n", pid)
		}
	} else {
		fmt.Printf("[!] Backend %d may have already terminated\n", pid)
	}

	return nil
}
