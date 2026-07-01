package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/v0xg/pg-idle-guard/internal/postgres"
	"github.com/v0xg/pg-idle-guard/internal/report"
	"github.com/v0xg/pg-idle-guard/internal/util"
)

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Show aggregated idle-transaction leaks per application",
	Long: `Aggregate recorded idle-in-transaction leaks by application_name over a
trailing window, so you can see which service to fix instead of which PID
to kill.

History comes from the event file written by the daemon when
report.enabled is set, so this command must run where the daemon writes
its state. Ongoing (still open) leaks are fetched live from the database.`,
	RunE: runReport,
}

func init() {
	reportCmd.Flags().Int("days", 7, "Trailing window in days")
	reportCmd.Flags().Bool("json", false, "Output in JSON format")
	rootCmd.AddCommand(reportCmd)
}

func runReport(cmd *cobra.Command, args []string) error {
	days, _ := cmd.Flags().GetInt("days")
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if days < 1 {
		return fmt.Errorf("--days must be at least 1, got %d", days)
	}

	path := cfg.Report.DataFile
	if path == "" {
		var err error
		path, err = report.DefaultPath()
		if err != nil {
			return fmt.Errorf("resolving report data path: %w", err)
		}
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Printf("No leak history found at %s\n\n", path)
		fmt.Println("To start recording, set report.enabled: true in the daemon's config.")
		fmt.Println("If it is already enabled, run this command where the daemon writes its")
		fmt.Println("state — on the daemon's host, or with its state directory volume-mounted.")
		return nil
	}

	now := time.Now()
	events, err := report.NewStore(path).ReadSince(now.AddDate(0, 0, -days))
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("event file %s is not readable: %w\nthe daemon writes it with mode 0600 — run pguard report as the daemon's user", path, err)
		}
		return err
	}

	if days > cfg.Report.RetentionDays {
		fmt.Fprintf(os.Stderr, "note: history is retained for %d days; showing what's available\n", cfg.Report.RetentionDays)
	}

	summaries := report.Aggregate(events)
	ongoing := fetchOngoingLeaks()

	if jsonOutput {
		return printReportJSON(days, now, summaries, ongoing)
	}
	printReportTables(days, summaries, ongoing)
	return nil
}

// fetchOngoingLeaks lists current idle-in-transaction sessions past the
// warning threshold straight from pg_stat_activity. Failures degrade to a
// note on stderr: the historical report is still useful without the DB.
func fetchOngoingLeaks() []report.OngoingLeak {
	client, err := postgres.NewClient(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: couldn't fetch ongoing leaks: %v\n", err)
		return nil
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conns, err := client.GetIdleTransactions(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: couldn't fetch ongoing leaks: %v\n", err)
		return nil
	}

	var ongoing []report.OngoingLeak
	for _, conn := range conns {
		duration := conn.IdleDuration()
		if duration >= cfg.Thresholds.IdleTransaction.Warning {
			ongoing = append(ongoing, report.OngoingLeak{
				PID:      conn.PID,
				App:      conn.ApplicationName,
				Duration: duration,
				Query:    util.TruncateQuery(conn.Query, report.MaxQueryChars),
			})
		}
	}
	sort.Slice(ongoing, func(i, j int) bool { return ongoing[i].Duration > ongoing[j].Duration })
	return ongoing
}

func printReportJSON(days int, now time.Time, summaries []report.AppSummary, ongoing []report.OngoingLeak) error {
	if summaries == nil {
		summaries = []report.AppSummary{}
	}
	if ongoing == nil {
		ongoing = []report.OngoingLeak{}
	}
	out := struct {
		WindowDays  int                  `json:"window_days"`
		GeneratedAt time.Time            `json:"generated_at"`
		Apps        []report.AppSummary  `json:"apps"`
		Ongoing     []report.OngoingLeak `json:"ongoing"`
	}{days, now, summaries, ongoing}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling JSON: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

func printReportTables(days int, summaries []report.AppSummary, ongoing []report.OngoingLeak) {
	fmt.Println()
	fmt.Printf("Leak Report — last %d days\n", days)
	fmt.Println(strings.Repeat("-", 80))

	if len(summaries) == 0 {
		fmt.Println("No completed leaks recorded.")
	} else {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "APP\tLEAKS\tMEDIAN\tMAX\tTERMINATED\tTOP QUERY")
		for _, sum := range summaries {
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%d\t%s\n",
				util.Truncate(report.DisplayName(sum.App), 25),
				sum.Count,
				util.FormatDuration(sum.MedianDuration),
				util.FormatDuration(sum.MaxDuration),
				sum.TerminatedCount,
				util.TruncateQuery(sum.TopQuery, 40),
			)
		}
		w.Flush()
	}

	if len(ongoing) > 0 {
		fmt.Println()
		fmt.Printf("Ongoing — %d still open\n", len(ongoing))
		fmt.Println(strings.Repeat("-", 80))

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PID\tAPP\tIDLE FOR\tQUERY")
		for _, o := range ongoing {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\n",
				o.PID,
				util.Truncate(report.DisplayName(o.App), 25),
				util.FormatDuration(o.Duration),
				util.TruncateQuery(o.Query, 40),
			)
		}
		w.Flush()
	}

	fmt.Println()
}
