package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/v0xg/pg-idle-guard/internal/config"
)

var (
	cfgFile string
	cfg     *config.Config

	// Build-time variables (set via -ldflags)
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "pguard",
	Short: "Monitor PostgreSQL connections and catch idle transactions",
	Long: `pguard monitors your PostgreSQL connection pool and alerts you
when transactions are stuck in "idle in transaction" state, preventing
connection pool exhaustion.`,
	SilenceUsage: true,
}

// Execute runs the root command
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: ~/.config/pguard/config.yaml)")

	// Add subcommands
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(watchCmd)
	rootCmd.AddCommand(killCmd)
	rootCmd.AddCommand(configureCmd)
	rootCmd.AddCommand(versionCmd)
}

func initConfig() {
	var err error

	if cfgFile != "" {
		cfg, err = config.Load(cfgFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
			os.Exit(1)
		}
	} else {
		cfg, err = config.LoadOrDefault()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
			os.Exit(1)
		}
	}
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("pguard %s\n", Version)
		fmt.Printf("  commit: %s\n", Commit)
		fmt.Printf("  built:  %s\n", Date)
	},
}
